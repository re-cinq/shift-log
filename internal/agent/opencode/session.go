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

// opencodeSchema describes the detected SQLite schema variant for a given database.
type opencodeSchema struct {
	sessionTable string
	messageTable string
	updatedAtCol string
	createdAtCol string
	hasProjectID bool
}

// detectSchema probes sqlite_master to determine which schema variant is in use.
// OpenCode v1.16+ uses plural table names (sessions/messages) with updated_at/created_at.
// Earlier versions use singular table names (session/message) with time_updated/time_created.
func detectSchema(dbPath string) *opencodeSchema {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}

	cmd := exec.Command("sqlite3", dbPath,
		`SELECT name FROM sqlite_master WHERE type='table';`)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	tables := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		tables[strings.TrimSpace(line)] = true
	}

	// New schema (OpenCode v1.16+): plural names, no project_id, integer timestamps.
	if tables["sessions"] {
		msgTable := "messages"
		if !tables["messages"] && tables["message"] {
			msgTable = "message"
		}
		return &opencodeSchema{
			sessionTable: "sessions",
			messageTable: msgTable,
			updatedAtCol: "updated_at",
			createdAtCol: "created_at",
			hasProjectID: false,
		}
	}

	// Old schema (pre-v1.16): singular names, has project_id, RFC3339 timestamps.
	if tables["session"] {
		msgTable := "message"
		if tables["messages"] {
			msgTable = "messages"
		}
		return &opencodeSchema{
			sessionTable: "session",
			messageTable: msgTable,
			updatedAtCol: "time_updated",
			createdAtCol: "time_created",
			hasProjectID: true,
		}
	}

	return nil
}

// buildMessageQuery returns a sqlite3 query that fetches all messages for a
// session as a JSON array. The query format depends on the schema variant.
func buildMessageQuery(schema *opencodeSchema, sessionID string) string {
	if schema.messageTable == "" {
		return ""
	}
	if schema.hasProjectID {
		// Old schema: messages have a JSON 'data' blob; merge id back in.
		return fmt.Sprintf(
			`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM %s WHERE session_id='%s' ORDER BY %s;`,
			schema.messageTable, sessionID, schema.createdAtCol,
		)
	}
	// New schema: messages have individual columns; build a JSON object per row.
	return fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', CASE WHEN parts IS NULL THEN '[]' ELSE parts END)) FROM %s WHERE session_id='%s' ORDER BY %s;`,
		schema.messageTable, sessionID, schema.createdAtCol,
	)
}

// isRecentTimestamp returns true if the timestamp string is within
// agent.RecentSessionTimeout. Handles RFC3339, ISO 8601, plain datetime,
// and Unix integer timestamps (seconds or milliseconds).
func isRecentTimestamp(s string) bool {
	if s == "" {
		return true // can't parse → be optimistic
	}

	// Unix integer timestamp (OpenCode v1.16+ stores updated_at as int).
	if ts, err := strconv.ParseInt(s, 10, 64); err == nil {
		var t time.Time
		if ts > 1e12 {
			t = time.UnixMilli(ts)
		} else {
			t = time.Unix(ts, 0)
		}
		return time.Since(t) <= agent.RecentSessionTimeout
	}

	// String timestamp formats.
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
	}

	return true // unparseable → be optimistic
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
// It tries both per-project (.opencode/opencode.db, v1.16+) and global
// ($XDG_DATA_HOME/opencode/opencode.db, pre-v1.16) database locations.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	candidates := []string{
		// Per-project DB introduced in OpenCode v1.16+.
		filepath.Join(projectPath, ".opencode", "opencode.db"),
		// Global DB used by pre-v1.16 OpenCode.
		filepath.Join(dataDir, "opencode.db"),
	}

	for _, dbPath := range candidates {
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			continue
		}

		if info := querySessionFromDB(dbPath, projectID, projectPath); info != nil {
			return info, nil
		}
	}

	return nil, nil
}

// querySessionFromDB queries a single SQLite database file for a recent session.
func querySessionFromDB(dbPath, projectID, projectPath string) *agent.SessionInfo {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}

	schema := detectSchema(dbPath)
	if schema == nil {
		return nil
	}

	// Build session lookup query.
	var sessionQuery string
	if schema.hasProjectID {
		sessionQuery = fmt.Sprintf(
			`SELECT id FROM %s WHERE project_id='%s' ORDER BY %s DESC LIMIT 1;`,
			schema.sessionTable, projectID, schema.updatedAtCol,
		)
	} else {
		sessionQuery = fmt.Sprintf(
			`SELECT id FROM %s ORDER BY %s DESC LIMIT 1;`,
			schema.sessionTable, schema.updatedAtCol,
		)
	}

	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, sessionQuery)
	sessionOutput, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(sessionOutput)) == "" {
		return nil
	}
	sessionID := strings.TrimSpace(string(sessionOutput))

	// Check recency.
	timeQuery := fmt.Sprintf(
		`SELECT %s FROM %s WHERE id='%s';`,
		schema.updatedAtCol, schema.sessionTable, sessionID,
	)
	cmd = exec.Command("sqlite3", dbPath, timeQuery)
	timeOutput, err := cmd.Output()
	if err == nil {
		if !isRecentTimestamp(strings.TrimSpace(string(timeOutput))) {
			return nil
		}
	}

	// Fetch messages.
	var transcriptData []byte
	if msgQuery := buildMessageQuery(schema, sessionID); msgQuery != "" {
		cmd = exec.Command("sqlite3", dbPath, msgQuery)
		msgOutput, err := cmd.Output()
		if err == nil {
			raw := strings.TrimSpace(string(msgOutput))
			if raw != "[null]" && raw != "[]" && raw != "" {
				transcriptData = []byte(raw)
			}
		}
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}
}
