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

	"github.com/re-cinq/shift-log/internal/agent"
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

// discoverFromProjectLocalDB queries the project-local OpenCode SQLite database.
// OpenCode v1.16+ stores the database at <projectPath>/.opencode/opencode.db.
// Since the database is project-local, no project_id filter is needed.
func discoverFromProjectLocalDB(projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(projectPath, ".opencode", "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionID := queryMostRecentSession(dbPath)
	if sessionID == "" {
		return nil, nil
	}

	if !isSessionRecent(dbPath, sessionID) {
		return nil, nil
	}

	transcriptData := queryMessages(dbPath, sessionID)

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// queryMostRecentSession finds the most recently updated session in an OpenCode database.
// It tries multiple table and column name variants to handle schema evolution across versions.
func queryMostRecentSession(dbPath string) string {
	queries := []string{
		// v1.2–v1.15: singular table, time_updated column
		`SELECT id FROM session ORDER BY time_updated DESC LIMIT 1;`,
		// v1.16+: singular table, updated_at column
		`SELECT id FROM session ORDER BY updated_at DESC LIMIT 1;`,
		// v1.16+ alt: plural table, updated_at column
		`SELECT id FROM sessions ORDER BY updated_at DESC LIMIT 1;`,
		// v1.16+ alt: plural table, time_updated column
		`SELECT id FROM sessions ORDER BY time_updated DESC LIMIT 1;`,
		// fallback: any session via rowid (always exists in SQLite)
		`SELECT id FROM session ORDER BY rowid DESC LIMIT 1;`,
		`SELECT id FROM sessions ORDER BY rowid DESC LIMIT 1;`,
	}

	for _, q := range queries {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		if id := strings.TrimSpace(string(out)); id != "" {
			return id
		}
	}
	return ""
}

// isSessionRecent checks whether a session was updated within RecentSessionTimeout.
// Returns true (proceed optimistically) if the time cannot be determined.
func isSessionRecent(dbPath, sessionID string) bool {
	timeQueries := []string{
		fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT updated_at FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT time_updated FROM sessions WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT updated_at FROM sessions WHERE id='%s';`, sessionID),
	}

	for _, q := range timeQueries {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		timeStr := strings.TrimSpace(string(out))
		if timeStr == "" {
			continue
		}
		// Try numeric Unix timestamp (milliseconds or seconds)
		if t := parseTimestamp(timeStr); !t.IsZero() {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
		// Try string timestamp formats
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, timeStr); err == nil {
				return time.Since(t) <= agent.RecentSessionTimeout
			}
		}
		// Found a value but couldn't parse it — proceed optimistically
		return true
	}
	// Could not determine time — proceed optimistically
	return true
}

// parseTimestamp parses a numeric string as a Unix timestamp (milliseconds or seconds).
func parseTimestamp(s string) time.Time {
	var ms int64
	if _, err := fmt.Sscanf(s, "%d", &ms); err != nil || ms <= 0 {
		return time.Time{}
	}
	// Distinguish milliseconds (> 1e12) from seconds (< 1e12)
	if ms > 1_000_000_000_000 {
		return time.UnixMilli(ms)
	}
	return time.Unix(ms, 0)
}

// queryMessages retrieves messages for a session from an OpenCode database.
// It handles multiple schema variants across OpenCode versions.
func queryMessages(dbPath, sessionID string) []byte {
	queries := []string{
		// v1.2–v1.15: message table, data column, time_created ordering
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
		// v1.16+: message table, parts column, time_created ordering
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role, 'content', parts)) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
		// v1.16+: messages table (plural), parts column, created_at ordering
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role, 'content', parts)) FROM messages WHERE session_id='%s' ORDER BY created_at;`, sessionID),
		// v1.16+: messages table (plural), data column, created_at ordering
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM messages WHERE session_id='%s' ORDER BY created_at;`, sessionID),
		// v1.16+: message table, parts column, created_at ordering
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role, 'content', parts)) FROM message WHERE session_id='%s' ORDER BY created_at;`, sessionID),
		// minimal fallback: id and role only, singular table
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role)) FROM message WHERE session_id='%s';`, sessionID),
		// minimal fallback: id and role only, plural table
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role)) FROM messages WHERE session_id='%s';`, sessionID),
	}

	for _, q := range queries {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		result := strings.TrimSpace(string(out))
		if result == "" || result == "[null]" || result == "[]" {
			continue
		}
		return []byte(result)
	}

	// Session exists but messages not yet accessible — return empty transcript
	return []byte("[]")
}

// querySessionByProjectID finds the most recent session matching a project ID.
// Returns empty string if no matching session is found.
func querySessionByProjectID(dbPath, projectID string) string {
	queries := []string{
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY updated_at DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM sessions WHERE project_id='%s' ORDER BY updated_at DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM sessions WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
	}

	for _, q := range queries {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		if id := strings.TrimSpace(string(out)); id != "" {
			return id
		}
	}
	return ""
}

// discoverFromSQLite queries the OpenCode global SQLite database for the most recent session.
// It first tries to find a session matching the given project ID; if that fails (e.g., because
// the project ID format changed between versions), it falls back to the most recently updated
// session within RecentSessionTimeout.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Try project-specific lookup first
	sessionID := querySessionByProjectID(dbPath, projectID)
	if sessionID == "" {
		// Fall back to most-recent session, relying on RecentSessionTimeout for safety
		sessionID = queryMostRecentSession(dbPath)
		if sessionID == "" {
			return nil, nil
		}
	}

	if !isSessionRecent(dbPath, sessionID) {
		return nil, nil
	}

	transcriptData := queryMessages(dbPath, sessionID)

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}
