```go
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

	"github.com/re-cinq/shift-log/internal/agent"
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

// discoverFromProjectSQLite queries the project-local SQLite database used by
// OpenCode v1.17+, located at <projectPath>/.opencode/opencode.db.
//
// Schema (v1.17+):
//
//	sessions(id TEXT, title TEXT, message_count INTEGER, updated_at INTEGER, created_at INTEGER)
//	messages(id TEXT, session_id TEXT, role TEXT, parts TEXT, model TEXT, created_at INTEGER)
//
// Unlike pre-v1.17, the sessions table has no project_id column, so we simply
// return the most recently updated session within the recency window.
func discoverFromProjectSQLite(dbPath, projectPath string) (*agent.SessionInfo, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Get the most recently updated session
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath,
		`SELECT id, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 1;`)
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}

	fields := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	if len(fields) < 2 {
		return nil, nil
	}
	sessionID := strings.TrimSpace(fields[0])
	timeStr := strings.TrimSpace(fields[1])

	// Reject stale sessions outside the recency window
	if t, err := parseUnixOrISO(timeStr); err == nil {
		if time.Since(t) > agent.RecentSessionTimeout {
			return nil, nil
		}
	}

	// Fetch messages as a JSON array
	msgQuery := fmt.Sprintf(
		`SELECT json_group_array(json_object(`+
			`'id', id, 'role', role, 'parts', json(parts), `+
			`'model', COALESCE(model, ''), 'created_at', created_at`+
			`)) FROM messages WHERE session_id='%s' ORDER BY created_at;`,
		sessionID,
	)
	cmd = exec.Command("sqlite3", dbPath, msgQuery)
	msgOut, err := cmd.Output()
	if err != nil {
		// Session found but messages query failed — still return session info
		return &agent.SessionInfo{
			SessionID:   sessionID,
			ProjectPath: projectPath,
			StartedAt:   time.Now().Format(time.RFC3339),
		}, nil
	}

	transcriptData := []byte(strings.TrimSpace(string(msgOut)))
	if string(transcriptData) == "[null]" || string(transcriptData) == "[]" || len(transcriptData) == 0 {
		return &agent.SessionInfo{
			SessionID:   sessionID,
			ProjectPath: projectPath,
			StartedAt:   time.Now().Format(time.RFC3339),
		}, nil
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// parseUnixOrISO parses a timestamp that may be a Unix integer (seconds or
// milliseconds) or an ISO 8601 / RFC 3339 string.
func parseUnixOrISO(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}

	// Integer timestamps — Unix seconds or milliseconds
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1_000_000_000_000 { // threshold: year 33658 in seconds → must be ms
			return time.UnixMilli(n), nil
		}
		return time.Unix(n, 0), nil
	}

	// String formats
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}

	return time.Time{}, fmt.Errorf("cannot parse time %q", s)
}
```
