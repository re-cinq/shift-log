package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

// sqliteQuery runs a sqlite3 query and returns trimmed stdout, or "" on error.
func sqliteQuery(dbPath string, query string) string {
	cmd := exec.Command("sqlite3", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// findRecentSessionID queries the SQLite database for the most recent session
// matching the given project_id, trying multiple time column formats for
// compatibility across OpenCode versions.
func findRecentSessionID(dbPath, projectID string) string {
	// OpenCode v1.15+: time stored as JSON object {"created": "...", "updated": "..."}
	// OpenCode v1.2–v1.14: time stored in separate time_updated column
	// Fallback: order by rowid (insertion order)
	queries := []string{
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY json_extract(time, '$.updated') DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY rowid DESC LIMIT 1;`, projectID),
	}
	for _, q := range queries {
		if id := sqliteQuery(dbPath, q); id != "" {
			return id
		}
	}
	return ""
}

// getSessionTimeUpdated retrieves the last-updated timestamp for a session,
// trying multiple column formats for compatibility across OpenCode versions.
func getSessionTimeUpdated(dbPath, sessionID string) string {
	queries := []string{
		fmt.Sprintf(`SELECT json_extract(time, '$.updated') FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT json_extract(time, '$.created') FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT time_created FROM session WHERE id='%s';`, sessionID),
	}
	for _, q := range queries {
		if ts := sqliteQuery(dbPath, q); ts != "" {
			return ts
		}
	}
	return ""
}

// querySessionMessages retrieves all messages for a session as a JSON array,
// trying multiple schema formats for compatibility across OpenCode versions.
func querySessionMessages(dbPath, sessionID string) []byte {
	// OpenCode v1.15+: time stored as JSON, ordering by json_extract(time, '$.created')
	// OpenCode v1.2–v1.14: time stored in time_created column
	queries := []string{
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY json_extract(time, '$.created');`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s';`, sessionID),
	}
	for _, q := range queries {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" && trimmed != "[null]" && trimmed != "[]" {
			return []byte(trimmed)
		}
	}
	return nil
}
