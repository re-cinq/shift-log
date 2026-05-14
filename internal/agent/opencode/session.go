```go
package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// GetSessionDir returns the legacy session storage directory for a project (pre-v1.14).
func GetSessionDir(projectPath string) (string, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return "", err
	}

	projectID := GetProjectID(projectPath)
	return filepath.Join(dataDir, "storage", "session", projectID), nil
}

// GetSessionDiffDir returns the session_diff storage directory used by OpenCode v1.14+.
// In v1.14+, session files are stored flat in storage/session_diff/ rather than
// per-project subdirectories under storage/session/{projectID}/.
func GetSessionDiffDir() (string, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "storage", "session_diff"), nil
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
	// v1.14+ fields
	Path  string `json:"path,omitempty"`
	Title string `json:"title,omitempty"`
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

// sqliteSchema holds column names discovered via PRAGMA table_info.
type sqliteSchema struct {
	columns map[string]bool
}

// loadSchema runs PRAGMA table_info and returns the set of column names.
func loadSchema(dbPath, table string) *sqliteSchema {
	cmd := exec.Command("sqlite3", dbPath, fmt.Sprintf("PRAGMA table_info(%s);", table))
	out, err := cmd.Output()
	cols := map[string]bool{}
	if err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			parts := strings.Split(line, "|")
			if len(parts) >= 2 {
				cols[strings.TrimSpace(parts[1])] = true
			}
		}
	}
	return &sqliteSchema{columns: cols}
}

// pick returns the first candidate column name that exists in the schema.
func (s *sqliteSchema) pick(candidates ...string) string {
	for _, c := range candidates {
		if s.columns[c] {
			return c
		}
	}
	return ""
}

// parseOpenCodeTime parses timestamps stored by OpenCode in various formats.
// v1.14+ stores timestamps as Unix milliseconds (integer strings).
// Earlier versions stored RFC3339 strings.
func parseOpenCodeTime(raw string) (time.Time, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	// Unix milliseconds (v1.14+ integer timestamp)
	var ms int64
	if _, err := fmt.Sscanf(s, "%d", &ms); err == nil && ms > 1_000_000_000_000 {
		return time.UnixMilli(ms), true
	}
	return time.Time{}, false
}

// queryMessages retrieves messages for a session from SQLite, handling both
// old schema (data column with full JSON) and new schema (role/content columns).
func queryMessages(dbPath, sessionID string) []byte {
	escapedID := strings.ReplaceAll(sessionID, "'", "''")

	queries := []string{
		// v1.14+ schema: individual role/content/id columns
		fmt.Sprintf(`SELECT json_group_array(json_object('role',role,'id',id,'content',content)) FROM message WHERE session_id='%s' ORDER BY rowid;`, escapedID),
		// pre-v1.14 schema: 'data' column contains full JSON blob
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, escapedID),
		// minimal fallback
		fmt.Sprintf(`SELECT json_group_array(json_object('role',role,'content',content)) FROM message WHERE session_id='%s';`, escapedID),
	}

	for _, q := range queries {
		out, err := exec.Command("sqlite3", dbPath, q).Output()
		if err != nil {
			continue
		}
		result := strings.TrimSpace(string(out))
		if result == "" || result == "[null]" || result == "[]" {
			continue
		}
		return []byte(result)
	}
	return nil
}
```
