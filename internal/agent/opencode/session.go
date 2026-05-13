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

// discoverFromLocalSQLite discovers a session from a project-local opencode.db.
// This handles OpenCode v1.14.48+ where the database lives at {project}/.opencode/opencode.db.
// The new schema uses plural table names (sessions, messages) and integer timestamps.
func discoverFromLocalSQLite(dbPath, projectPath string) (*agent.SessionInfo, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionID := sqliteQueryLatestSession(dbPath)
	if sessionID == "" {
		return nil, nil
	}

	if !sqliteIsSessionRecent(dbPath, sessionID) {
		return nil, nil
	}

	transcriptData := sqliteQueryMessages(dbPath, sessionID)
	if len(transcriptData) == 0 {
		return nil, nil
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// sqliteQueryLatestSession returns the ID of the most recently updated session.
// Tries the new schema (sessions table, updated_at column) then the old schema.
func sqliteQueryLatestSession(dbPath string) string {
	for _, q := range []string{
		`SELECT id FROM sessions ORDER BY updated_at DESC LIMIT 1;`,
		`SELECT id FROM sessions ORDER BY rowid DESC LIMIT 1;`,
		`SELECT id FROM session ORDER BY rowid DESC LIMIT 1;`,
	} {
		out, err := exec.Command("sqlite3", dbPath, q).Output()
		if err == nil {
			if id := strings.TrimSpace(string(out)); id != "" {
				return id
			}
		}
	}
	return ""
}

// sqliteIsSessionRecent returns true if the session was updated within the recent timeout.
// Handles both integer timestamps (new schema) and ISO string timestamps (old schema).
func sqliteIsSessionRecent(dbPath, sessionID string) bool {
	for _, q := range []string{
		fmt.Sprintf(`SELECT updated_at FROM sessions WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID),
	} {
		out, err := exec.Command("sqlite3", dbPath, q).Output()
		if err != nil {
			continue
		}
		timeStr := strings.TrimSpace(string(out))
		if timeStr == "" {
			continue
		}
		return sqliteParseTimeIsRecent(timeStr)
	}
	return true // assume recent if time cannot be determined
}

// sqliteParseTimeIsRecent parses a SQLite timestamp value and returns true if it
// falls within the recent session timeout. Handles integer ms/s and ISO strings.
func sqliteParseTimeIsRecent(timeStr string) bool {
	timeout := agent.RecentSessionTimeout

	// Integer: Unix milliseconds (>1e12) or Unix seconds
	if n, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		var t time.Time
		if n > 1_000_000_000_000 {
			t = time.Unix(n/1000, (n%1000)*int64(time.Millisecond))
		} else {
			t = time.Unix(n, 0)
		}
		return time.Since(t) <= timeout
	}

	// ISO 8601 string formats
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, timeStr); err == nil {
			return time.Since(t) <= timeout
		}
	}

	return true // assume recent if unparseable
}

// sqliteQueryMessages retrieves messages for a session as a JSON array.
// Tries the new schema (messages table, parts column) then old schema fallbacks.
func sqliteQueryMessages(dbPath, sessionID string) []byte {
	for _, q := range []string{
		// New schema: messages table with parts column (OpenCode v1.14.48+)
		fmt.Sprintf(
			`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts))) FROM messages WHERE session_id='%s';`,
			sessionID,
		),
		// Old schema: message table with data column, ordered by creation time
		fmt.Sprintf(
			`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
			sessionID,
		),
		// Old schema without ORDER BY (handles renamed time columns)
		fmt.Sprintf(
			`SELECT json_group_array(data) FROM message WHERE session_id='%s';`,
			sessionID,
		),
	} {
		out, err := exec.Command("sqlite3", dbPath, q).Output()
		if err != nil {
			continue
		}
		raw := strings.TrimSpace(string(out))
		if raw != "" && raw != "[null]" && raw != "[]" {
			return []byte(raw)
		}
	}
	return nil
}
