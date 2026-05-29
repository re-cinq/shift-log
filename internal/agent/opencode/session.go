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

// getSQLiteColumns returns the set of column names for a table in the SQLite database.
// Returns an empty map if the table does not exist or sqlite3 is unavailable.
func getSQLiteColumns(dbPath, table string) map[string]bool {
	cmd := exec.Command("sqlite3", dbPath, "PRAGMA table_info("+table+");")
	output, err := cmd.Output()
	if err != nil {
		return map[string]bool{}
	}
	cols := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// PRAGMA table_info output: cid|name|type|notnull|dflt_value|pk
		parts := strings.SplitN(line, "|", 3)
		if len(parts) >= 2 && parts[1] != "" {
			cols[parts[1]] = true
		}
	}
	return cols
}

// runSQLite executes a sqlite3 query and returns the trimmed stdout.
// Returns an empty string on any error.
func runSQLite(dbPath, query string) string {
	cmd := exec.Command("sqlite3", dbPath, query)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// escapeSQL escapes single quotes in s for use inside SQLite string literals.
func escapeSQL(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
// It introspects the table schema so that it works across multiple OpenCode versions
// whose column names have changed (e.g. project_id → path, time_updated → updated,
// data → parts, session_id → sessionID).
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Discover session table columns to build the right queries.
	sessionCols := getSQLiteColumns(dbPath, "session")

	// Choose the best ORDER BY column for recency.
	orderBy := "rowid"
	for _, col := range []string{"time_updated", "updated", "updatedAt", "time", "created"} {
		if sessionCols[col] {
			orderBy = col
			break
		}
	}

	// Try multiple strategies to locate the session, newest schema last so we
	// short-circuit as soon as we find a match.
	sessionID := ""

	// Strategy 1: project_id = git root-commit hash  (OpenCode <= 1.14)
	if sessionID == "" && sessionCols["project_id"] && projectID != "global" {
		sessionID = runSQLite(dbPath, fmt.Sprintf(
			`SELECT id FROM session WHERE project_id='%s' ORDER BY %s DESC LIMIT 1;`,
			escapeSQL(projectID), orderBy,
		))
	}

	// Strategy 2: path = project directory  (OpenCode >= 1.15)
	if sessionID == "" && sessionCols["path"] {
		sessionID = runSQLite(dbPath, fmt.Sprintf(
			`SELECT id FROM session WHERE path='%s' ORDER BY %s DESC LIMIT 1;`,
			escapeSQL(projectPath), orderBy,
		))
	}

	// Strategy 3: project_id = project directory  (some intermediate versions)
	if sessionID == "" && sessionCols["project_id"] {
		sessionID = runSQLite(dbPath, fmt.Sprintf(
			`SELECT id FROM session WHERE project_id='%s' ORDER BY %s DESC LIMIT 1;`,
			escapeSQL(projectPath), orderBy,
		))
	}

	// Strategy 4: directory column
	if sessionID == "" && sessionCols["directory"] {
		sessionID = runSQLite(dbPath, fmt.Sprintf(
			`SELECT id FROM session WHERE directory='%s' ORDER BY %s DESC LIMIT 1;`,
			escapeSQL(projectPath), orderBy,
		))
	}

	if sessionID == "" {
		return nil, nil
	}

	// Check whether the session falls within the recent-session window.
	timeStr := ""
	for _, col := range []string{"time_updated", "updated", "updatedAt", "time"} {
		if sessionCols[col] {
			timeStr = runSQLite(dbPath, fmt.Sprintf(
				`SELECT %s FROM session WHERE id='%s';`,
				col, escapeSQL(sessionID),
			))
			break
		}
	}

	if timeStr != "" && !isRecentTimestamp(timeStr) {
		return nil, nil
	}

	// Retrieve messages as a JSON array.
	transcriptData := getSessionTranscriptFromDB(dbPath, sessionID)
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

// isRecentTimestamp reports whether a raw timestamp string represents a time
// within the recent-session window. Handles unix milliseconds, unix seconds,
// and common string layouts (RFC3339, SQLite default).
func isRecentTimestamp(timeStr string) bool {
	// Try integer timestamp (unix milliseconds or seconds).
	if ms, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		var t time.Time
		if ms > 10_000_000_000 { // >~year 2286 in seconds → treat as milliseconds
			t = time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
		} else {
			t = time.Unix(ms, 0)
		}
		return time.Since(t) <= agent.RecentSessionTimeout
	}

	// Try string layouts.
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, timeStr); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
	}

	// Cannot parse — assume recent so we don't silently discard a valid session.
	return true
}

// getSessionTranscriptFromDB retrieves messages for a session from SQLite,
// handling column-name variants across OpenCode versions.
func getSessionTranscriptFromDB(dbPath, sessionID string) []byte {
	messageCols := getSQLiteColumns(dbPath, "message")

	// Find the column that references the parent session.
	sessionIDCol := ""
	for _, col := range []string{"session_id", "sessionId", "sessionID"} {
		if messageCols[col] {
			sessionIDCol = col
			break
		}
	}
	if sessionIDCol == "" {
		return nil
	}

	// Choose ORDER BY column.
	orderBy := "rowid"
	for _, col := range []string{"time_created", "created", "createdAt", "time"} {
		if messageCols[col] {
			orderBy = col
			break
		}
	}

	// Strategy 1: data column — OpenCode <= 1.14 stores each message as a JSON blob.
	if messageCols["data"] {
		q := fmt.Sprintf(
			`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE %s='%s' ORDER BY %s;`,
			sessionIDCol, escapeSQL(sessionID), orderBy,
		)
		if out := runSQLite(dbPath, q); out != "" && out != "[null]" && out != "[]" {
			return []byte(out)
		}
	}

	// Strategy 2: individual role + parts/content columns — OpenCode >= 1.15.
	// json(col) embeds the stored JSON value directly rather than re-encoding as a string.
	partsCol := ""
	for _, col := range []string{"parts", "content", "body"} {
		if messageCols[col] {
			partsCol = col
			break
		}
	}

	if messageCols["role"] && partsCol != "" {
		q := fmt.Sprintf(
			`SELECT json_group_array(json_object('id', id, 'role', role, 'content', json(%s))) FROM message WHERE %s='%s' ORDER BY %s;`,
			partsCol, sessionIDCol, escapeSQL(sessionID), orderBy,
		)
		if out := runSQLite(dbPath, q); out != "" && out != "[null]" && out != "[]" {
			return []byte(out)
		}
	}

	return nil
}
