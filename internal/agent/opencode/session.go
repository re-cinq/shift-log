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

// openCodeCandidateDirs returns all plausible OpenCode data directories to check
// during session discovery. OpenCode has used both XDG_DATA_HOME and XDG_STATE_HOME
// across versions, so we probe all likely locations.
func openCodeCandidateDirs() []string {
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			return []string{
				filepath.Join(home, "Library", "Application Support", "opencode"),
			}
		}
		return nil
	}

	var dirs []string
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		dirs = append(dirs, filepath.Join(xdg, "opencode"))
	}
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		dirs = append(dirs, filepath.Join(xdg, "opencode"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".local", "share", "opencode"))
		dirs = append(dirs, filepath.Join(home, ".local", "state", "opencode"))
	}
	return dirs
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

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Find most recent session for this project.
	// Try time_updated ordering first, then rowid as fallback (handles column renames).
	sessionID := sqliteQueryFirst(dbPath,
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY rowid DESC LIMIT 1;`, projectID),
	)
	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout)
	if !openCodeSessionIsRecent(dbPath, sessionID) {
		return nil, nil
	}

	// Get messages for this session, trying multiple schema formats.
	// Returns []byte("[]") as fallback so we still create a note even if messages
	// can't be retrieved (e.g. schema changed between OpenCode versions).
	transcriptData := openCodeQueryMessages(dbPath, sessionID)

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "", // no file path for SQLite
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// sqliteQueryFirst runs each query against dbPath and returns the first non-empty result.
func sqliteQueryFirst(dbPath string, queries ...string) string {
	for _, q := range queries {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err == nil {
			if s := strings.TrimSpace(string(out)); s != "" {
				return s
			}
		}
	}
	return ""
}

// openCodeSessionIsRecent checks whether a session falls within the recent session window.
func openCodeSessionIsRecent(dbPath, sessionID string) bool {
	// Try time_updated first, then time_created as fallback
	for _, col := range []string{"time_updated", "time_created"} {
		q := fmt.Sprintf(`SELECT %s FROM session WHERE id='%s';`, col, sessionID)
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		timeStr := strings.TrimSpace(string(out))
		if t := parseOpenCodeTimestamp(timeStr); !t.IsZero() {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
	}
	// Cannot determine timestamp — proceed rather than skip
	return true
}

// parseOpenCodeTimestamp parses timestamp strings used by OpenCode.
// Handles ISO 8601, common variants, and Unix millisecond integers.
func parseOpenCodeTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, f := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	// Try Unix milliseconds (integer storage, common in newer JS-based tools)
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil && ms > 0 {
		return time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
	}
	return time.Time{}
}

// openCodeQueryMessages retrieves messages for a session from SQLite,
// trying multiple query formats to handle schema variations across OpenCode versions.
// Returns []byte("[]") as a safe fallback when no format succeeds.
func openCodeQueryMessages(dbPath, sessionID string) []byte {
	queries := []string{
		// v1.x: messages stored as JSON blob in 'data' column, ordered by time_created
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
		// Fallback: same schema but order by rowid (if time_created column was renamed)
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY rowid;`, sessionID),
		// Alternative: messages with separate role/content columns (newer schema variants)
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role, 'content', content)) FROM message WHERE session_id='%s' ORDER BY rowid;`, sessionID),
	}
	for _, q := range queries {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		data := []byte(strings.TrimSpace(string(out)))
		if len(data) > 0 && string(data) != "[null]" && string(data) != "[]" {
			return data
		}
	}
	// Return empty JSON array — storeConversation handles this gracefully
	return []byte("[]")
}
```
