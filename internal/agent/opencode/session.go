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

// GetDataDir returns the OpenCode global data directory.
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

// GetLocalDBPath returns the local SQLite database path for OpenCode v1.17+.
// OpenCode v1.17+ stores the database locally in the project's .opencode directory.
func GetLocalDBPath(projectPath string) string {
	return filepath.Join(projectPath, ".opencode", "opencode.db")
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

// queryLatestSessionLocal returns the most recent session ID and its update time
// from a local SQLite database. Tries new schema (sessions/updated_at) then falls
// back to old schema (session/time_updated).
func queryLatestSessionLocal(dbPath string) (sessionID string, modTime time.Time) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return "", time.Time{}
	}

	// Try new schema (v1.17+): sessions table, updated_at as Unix ms integer
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath,
		`SELECT id, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 1;`)
	if out, err := cmd.Output(); err == nil {
		line := strings.TrimSpace(string(out))
		if line != "" {
			parts := strings.SplitN(line, "\t", 2)
			if len(parts) >= 1 && parts[0] != "" {
				sessionID = parts[0]
				if len(parts) >= 2 {
					if ms, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
						modTime = time.UnixMilli(ms)
					}
				}
				return sessionID, modTime
			}
		}
	}

	// Fall back to old local schema (v1.2+): session table, time_updated
	cmd = exec.Command("sqlite3", dbPath,
		`SELECT id FROM session ORDER BY time_updated DESC LIMIT 1;`)
	if out, err := cmd.Output(); err == nil {
		sid := strings.TrimSpace(string(out))
		if sid != "" {
			sessionID = sid
			// Get the time
			cmd2 := exec.Command("sqlite3", dbPath,
				fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sid))
			if t2out, err := cmd2.Output(); err == nil {
				modTime = parseOpenCodeTime(strings.TrimSpace(string(t2out)))
			}
		}
	}
	return sessionID, modTime
}

// queryMessagesLocal retrieves transcript data from a local SQLite database.
// Tries new schema (messages/parts) then falls back to old schema (message/data).
func queryMessagesLocal(dbPath, sessionID string) []byte {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}

	// Try new schema (v1.17+): messages table with parts column (JSON embedded via json())
	cmd := exec.Command("sqlite3", dbPath, fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts), 'created_at', created_at)) FROM messages WHERE session_id='%s' ORDER BY created_at;`,
		sessionID,
	))
	if out, err := cmd.Output(); err == nil {
		data := strings.TrimSpace(string(out))
		if data != "[null]" && data != "[]" && data != "" {
			return []byte(data)
		}
	}

	// Fall back to old local schema (v1.2+): message table with data column
	cmd = exec.Command("sqlite3", dbPath, fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sessionID,
	))
	if out, err := cmd.Output(); err == nil {
		data := strings.TrimSpace(string(out))
		if data != "[null]" && data != "[]" && data != "" {
			return []byte(data)
		}
	}

	return nil
}

// parseOpenCodeTime parses a session timestamp that may be in various formats:
// Unix milliseconds (integer), RFC3339, or other datetime strings.
func parseOpenCodeTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Try Unix milliseconds
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.UnixMilli(ms)
	}
	// Try common string formats
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
