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

// sqliteColumns returns the column names for a table via PRAGMA table_info.
// Returns nil if sqlite3 is unavailable or the table doesn't exist.
func sqliteColumns(dbPath, tableName string) []string {
	cmd := exec.Command("sqlite3", dbPath, fmt.Sprintf("PRAGMA table_info(%s);", tableName))
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var cols []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// PRAGMA table_info output: cid|name|type|notnull|dflt_value|pk
		parts := strings.Split(line, "|")
		if len(parts) >= 2 {
			cols = append(cols, parts[1])
		}
	}
	return cols
}

// pickColumn returns the actual column name from cols that case-insensitively
// matches the first candidate found. Returns "" if none match.
func pickColumn(cols []string, candidates ...string) string {
	colLower := make(map[string]string, len(cols))
	for _, c := range cols {
		colLower[strings.ToLower(c)] = c
	}
	for _, candidate := range candidates {
		if found, ok := colLower[strings.ToLower(candidate)]; ok {
			return found
		}
	}
	return ""
}

// parseSessionTime parses a time value from SQLite.
// Handles RFC3339/ISO strings as well as Unix milliseconds and seconds stored
// as integers (opencode v1.17+ stores timestamps as integer milliseconds).
func parseSessionTime(timeStr string) (time.Time, bool) {
	timeStr = strings.TrimSpace(timeStr)
	if timeStr == "" {
		return time.Time{}, false
	}

	// Try standard string formats first
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, timeStr); err == nil {
			return t, true
		}
	}

	// Try Unix integer (milliseconds if > 1e12, otherwise seconds)
	if ms, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		if ms > 1_000_000_000_000 {
			return time.UnixMilli(ms), true
		}
		return time.Unix(ms, 0), true
	}

	return time.Time{}, false
}

// querySessionMessages retrieves message JSON from the SQLite database for the
// given session ID. It uses PRAGMA table_info to handle column name differences
// between opencode versions (snake_case in older, camelCase in newer releases).
func querySessionMessages(dbPath, sessionID string) ([]byte, error) {
	messageCols := sqliteColumns(dbPath, "message")
	if len(messageCols) == 0 {
		return nil, fmt.Errorf("message table not found or inaccessible")
	}

	// session_id column: "session_id" (old) or "sessionId" / "sessionID" (new)
	sessionIDCol := pickColumn(messageCols, "session_id", "sessionId", "sessionID")
	if sessionIDCol == "" {
		return nil, fmt.Errorf("no session ID column found in message table")
	}

	// data column: "data" (old) or "content" / "body" (new)
	dataCol := pickColumn(messageCols, "data", "content", "body")
	if dataCol == "" {
		return nil, fmt.Errorf("no data column found in message table")
	}

	// time column for ordering (optional)
	timeCol := pickColumn(messageCols, "time_created", "timeCreated", "time", "createdAt", "created_at")

	var msgQuery string
	if timeCol != "" {
		msgQuery = fmt.Sprintf(
			"SELECT json_group_array(json_patch(%s, json_object('id', id))) FROM message WHERE %s='%s' ORDER BY %s;",
			dataCol, sessionIDCol, sessionID, timeCol,
		)
	} else {
		msgQuery = fmt.Sprintf(
			"SELECT json_group_array(json_patch(%s, json_object('id', id))) FROM message WHERE %s='%s';",
			dataCol, sessionIDCol, sessionID,
		)
	}

	cmd := exec.Command("sqlite3", dbPath, msgQuery)
	msgOutput, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	transcriptData := []byte(strings.TrimSpace(string(msgOutput)))
	if string(transcriptData) == "[null]" || string(transcriptData) == "[]" {
		return nil, nil
	}

	return transcriptData, nil
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent
// session for the given project. It uses PRAGMA table_info to handle column name
// differences between opencode versions.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Inspect session table schema to handle column name variations between versions.
	sessionCols := sqliteColumns(dbPath, "session")
	if len(sessionCols) == 0 {
		return nil, nil
	}

	// project ID column: "project_id" (old) or "projectId" / "projectID" (new)
	projectIDCol := pickColumn(sessionCols, "project_id", "projectId", "projectID")
	if projectIDCol == "" {
		return nil, nil
	}

	// time column for ordering: "time_updated" (old) or "time" / "updatedAt" (new)
	timeCol := pickColumn(sessionCols, "time_updated", "timeUpdated", "time", "updatedAt", "updated_at")

	var sessionQuery string
	if timeCol != "" {
		sessionQuery = fmt.Sprintf(
			"SELECT id FROM session WHERE %s='%s' ORDER BY %s DESC LIMIT 1;",
			projectIDCol, projectID, timeCol,
		)
	} else {
		sessionQuery = fmt.Sprintf(
			"SELECT id FROM session WHERE %s='%s' LIMIT 1;",
			projectIDCol, projectID,
		)
	}

	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, sessionQuery)
	sessionOutput, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(sessionOutput)) == "" {
		return nil, nil
	}
	sessionID := strings.TrimSpace(string(sessionOutput))

	// Check if this session was recent (within timeout)
	if timeCol != "" {
		timeQuery := fmt.Sprintf("SELECT %s FROM session WHERE id='%s';", timeCol, sessionID)
		cmd = exec.Command("sqlite3", dbPath, timeQuery)
		timeOutput, timeErr := cmd.Output()
		if timeErr == nil {
			if t, ok := parseSessionTime(string(timeOutput)); ok {
				if time.Since(t) > agent.RecentSessionTimeout {
					return nil, nil
				}
			}
			// If we can't parse the time, proceed anyway — better to try than skip
		}
	}

	// Get messages for this session
	transcriptData, err := querySessionMessages(dbPath, sessionID)
	if err != nil || len(transcriptData) == 0 {
		return nil, nil
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "", // no file path for SQLite
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}
