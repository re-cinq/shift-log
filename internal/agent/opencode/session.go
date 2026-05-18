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

// sqliteColumns returns the set of column names for a SQLite table using
// PRAGMA table_info, which works across all SQLite versions.
func sqliteColumns(dbPath, table string) map[string]bool {
	cmd := exec.Command("sqlite3", dbPath, fmt.Sprintf("PRAGMA table_info(%s);", table))
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	cols := make(map[string]bool)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// PRAGMA table_info output: cid|name|type|notnull|dflt_value|pk
		parts := strings.Split(line, "|")
		if len(parts) >= 2 {
			cols[parts[1]] = true
		}
	}
	return cols
}

// firstCol returns the first column name from candidates that exists in cols.
func firstCol(cols map[string]bool, candidates ...string) string {
	for _, c := range candidates {
		if cols[c] {
			return c
		}
	}
	return ""
}

// runSQLiteScalar executes a SQLite query and returns the first non-empty output line.
func runSQLiteScalar(dbPath, query string) string {
	cmd := exec.Command("sqlite3", dbPath, query)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// sqliteEscape escapes a string for use as a SQLite string literal value.
func sqliteEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// parseFlexibleTime tries multiple timestamp formats used across opencode versions.
func parseFlexibleTime(s string) time.Time {
	formats := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05.999999999-07:00",
		"2006-01-02T15:04:05.000-07:00",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// sqliteSessionMessages retrieves messages for a session as a JSON array.
// It tries multiple column naming strategies to handle schema differences
// across opencode versions (snake_case vs camelCase, data blob vs individual cols).
func sqliteSessionMessages(dbPath, sessionID string) []byte {
	msgCols := sqliteColumns(dbPath, "message")
	if len(msgCols) == 0 {
		return nil
	}

	sessionIDCol := firstCol(msgCols, "session_id", "sessionId", "sessionID")
	if sessionIDCol == "" {
		return nil
	}

	timeCol := firstCol(msgCols,
		"time_created", "createdAt", "created_at", "timeCreated",
		"time_updated", "updatedAt", "updated_at", "timeUpdated",
	)

	orderBy := ""
	if timeCol != "" {
		orderBy = fmt.Sprintf(` ORDER BY "%s"`, timeCol)
	}

	sid := sqliteEscape(sessionID)

	// Strategy 1: 'data' JSON blob with 'id' injection (opencode pre-v1.15 schema)
	if msgCols["data"] && msgCols["id"] {
		q := fmt.Sprintf(
			`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE "%s"='%s'%s;`,
			sessionIDCol, sid, orderBy,
		)
		if result := runSQLiteScalar(dbPath, q); result != "" && result != "[null]" && result != "[]" {
			return []byte(result)
		}
	}

	// Strategy 2: 'data' JSON blob where id is already embedded
	if msgCols["data"] {
		q := fmt.Sprintf(
			`SELECT json_group_array(data) FROM message WHERE "%s"='%s'%s;`,
			sessionIDCol, sid, orderBy,
		)
		if result := runSQLiteScalar(dbPath, q); result != "" && result != "[null]" && result != "[]" {
			return []byte(result)
		}
	}

	// Strategy 3: reconstruct message objects from individual columns
	roleCol := firstCol(msgCols, "role")
	contentCol := firstCol(msgCols, "content", "text", "body")
	if roleCol != "" && contentCol != "" {
		var objArgs string
		if msgCols["id"] {
			objArgs = fmt.Sprintf(`'id', id, 'role', "%s", 'content', "%s"`, roleCol, contentCol)
		} else {
			objArgs = fmt.Sprintf(`'role', "%s", 'content', "%s"`, roleCol, contentCol)
		}
		q := fmt.Sprintf(
			`SELECT json_group_array(json_object(%s)) FROM message WHERE "%s"='%s'%s;`,
			objArgs, sessionIDCol, sid, orderBy,
		)
		if result := runSQLiteScalar(dbPath, q); result != "" && result != "[null]" && result != "[]" {
			return []byte(result)
		}
	}

	return nil
}
