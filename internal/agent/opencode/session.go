package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// GetDataDir returns the OpenCode data directory.
// OpenCode follows XDG conventions: it uses $XDG_DATA_HOME/opencode on Linux
// and ~/Library/Application Support/opencode on macOS.
func GetDataDir() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not determine home directory: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "opencode"), nil
	}

	// Linux/other: respect XDG_DATA_HOME, default to ~/.local/share
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "opencode"), nil
}

// GetProjectID returns the project identifier for OpenCode.
// For git repos, this is the root commit hash. For non-git dirs, it's "global".
func GetProjectID(projectPath string) string {
	cmd := exec.Command("git", "rev-list", "--max-parents=0", "--all")
	cmd.Dir = projectPath
	output, err := cmd.Output()
	if err != nil {
		return "global"
	}

	// Take the first line (first root commit)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) > 0 && lines[0] != "" {
		return strings.TrimSpace(lines[0])
	}
	return "global"
}

// GetSessionDir returns the session storage directory for a project.
func GetSessionDir(projectPath string) (string, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return "", err
	}

	projectID := GetProjectID(projectPath)
	return filepath.Join(dataDir, "storage", "session", projectID), nil
}

// GetMessageDir returns the message storage directory for a session.
func GetMessageDir(sessionID string) (string, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dataDir, "storage", "message", sessionID), nil
}

// sessionInfo represents an OpenCode session JSON file.
type sessionInfo struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectID,omitempty"`
	Directory string `json:"directory,omitempty"`
	Title     string `json:"title,omitempty"`
}

// WriteSessionFile writes a session and its messages to OpenCode's storage.
func WriteSessionFile(projectPath, sessionID string, transcriptData []byte) (string, error) {
	sessionDir, err := GetSessionDir(projectPath)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return "", fmt.Errorf("could not create session directory: %w", err)
	}

	sessionPath := filepath.Join(sessionDir, sessionID+".json")

	// Write a minimal session file
	session := sessionInfo{
		ID:        sessionID,
		ProjectID: GetProjectID(projectPath),
		Directory: projectPath,
		Title:     "Restored session",
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return "", fmt.Errorf("could not marshal session: %w", err)
	}

	if err := os.WriteFile(sessionPath, data, 0600); err != nil {
		return "", fmt.Errorf("could not write session file: %w", err)
	}

	// Write messages from transcript data
	msgDir, err := GetMessageDir(sessionID)
	if err != nil {
		return sessionPath, nil // Session created, messages optional
	}

	if err := os.MkdirAll(msgDir, 0700); err != nil {
		return sessionPath, nil
	}

	// Write the raw transcript data as a single message file for restore
	msgPath := filepath.Join(msgDir, "transcript.jsonl")
	_ = os.WriteFile(msgPath, transcriptData, 0600)

	return sessionPath, nil
}

// sqliteRun executes a sqlite3 query and returns trimmed stdout.
// extraArgs are inserted before dbPath (e.g. "-separator", "\t").
func sqliteRun(dbPath, query string, extraArgs ...string) (string, error) {
	args := make([]string, 0, len(extraArgs)+2)
	args = append(args, extraArgs...)
	args = append(args, dbPath, query)
	out, err := exec.Command("sqlite3", args...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// sqliteSessionID finds the most recent session ID for a project, trying
// multiple ORDER BY strategies to handle schema variations across versions.
func sqliteSessionID(dbPath, projectID string) string {
	for _, orderBy := range []string{"time_updated DESC", "rowid DESC"} {
		q := fmt.Sprintf(
			`SELECT id FROM session WHERE project_id='%s' ORDER BY %s LIMIT 1;`,
			projectID, orderBy,
		)
		if id, err := sqliteRun(dbPath, q); err == nil && id != "" {
			return id
		}
	}
	return ""
}

// sqliteSessionRecent returns true if the session's timestamp is within timeout,
// or true when the timestamp cannot be determined (fail-open is safer than skipping).
func sqliteSessionRecent(dbPath, sessionID string, timeout time.Duration) bool {
	for _, col := range []string{"time_updated", "time_created"} {
		q := fmt.Sprintf(`SELECT %s FROM session WHERE id='%s';`, col, sessionID)
		val, err := sqliteRun(dbPath, q)
		if err != nil || val == "" {
			continue
		}
		// Unix milliseconds integer (opencode 1.16+)
		if ms, err := strconv.ParseInt(val, 10, 64); err == nil {
			t := time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
			return time.Since(t) <= timeout
		}
		// String timestamp formats
		for _, layout := range []string{
			time.RFC3339Nano,
			"2006-01-02T15:04:05.000Z",
			"2006-01-02 15:04:05",
		} {
			if t, err := time.Parse(layout, val); err == nil {
				return time.Since(t) <= timeout
			}
		}
		// Column exists but format is unknown — proceed rather than skip
		return true
	}
	// No timestamp column found — fail-open
	return true
}

// sqliteMessages retrieves transcript messages for a session from SQLite.
// It tries several strategies to handle schema changes across opencode versions:
//
//  1. data blob + JSON aggregate (opencode v1.2–v1.15)
//  2. individual role/content columns + JSON aggregate (opencode v1.16+)
//  3. row-by-row fetch without JSON aggregate functions (minimal sqlite3 builds)
//
// Always returns a valid JSON array. Returns "[]" when messages are unavailable
// so callers can still write a minimal git note.
func sqliteMessages(dbPath, sessionID string) []byte {
	// Strategy 1: data-blob column (v1.2–v1.15)
	for _, orderBy := range []string{"time_created", "rowid"} {
		q := fmt.Sprintf(
			`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY %s;`,
			sessionID, orderBy,
		)
		if out, err := sqliteRun(dbPath, q); err == nil {
			if out != "[null]" && out != "[]" && out != "" {
				return []byte(out)
			}
		}
	}

	// Strategy 2: individual role/content columns (v1.16+)
	for _, orderBy := range []string{"time_created", "rowid"} {
		q := fmt.Sprintf(
			`SELECT json_group_array(json_object('id', id, 'role', role, 'content', json(content))) FROM message WHERE session_id='%s' ORDER BY %s;`,
			sessionID, orderBy,
		)
		if out, err := sqliteRun(dbPath, q); err == nil {
			if out != "[null]" && out != "[]" && out != "" {
				return []byte(out)
			}
		}
	}

	// Strategy 3: row-by-row fetch — avoids json_group_array for minimal builds
	for _, col := range []string{"data", "content"} {
		q := fmt.Sprintf(
			`SELECT %s FROM message WHERE session_id='%s' ORDER BY rowid;`,
			col, sessionID,
		)
		out, err := sqliteRun(dbPath, q)
		if err != nil || out == "" {
			continue
		}
		var msgs []json.RawMessage
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if json.Valid([]byte(line)) {
				msgs = append(msgs, json.RawMessage(line))
			} else {
				wrapped, _ := json.Marshal(map[string]string{"content": line})
				msgs = append(msgs, json.RawMessage(wrapped))
			}
		}
		if len(msgs) > 0 {
			if encoded, err := json.Marshal(msgs); err == nil {
				return encoded
			}
		}
	}

	// Session found but messages unavailable — return empty array so
	// storeConversation can still write a minimal (message_count=0) git note.
	return []byte("[]")
}
