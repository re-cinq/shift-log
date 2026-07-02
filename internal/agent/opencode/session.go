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

// sqliteColumns returns the column names of a SQLite table via PRAGMA table_info.
// OpenCode's SQLite schema has changed column naming conventions across
// releases, so callers resolve column names dynamically via pickColumn
// rather than assuming one fixed set of names.
func sqliteColumns(dbPath, table string) ([]string, error) {
	cmd := exec.Command("sqlite3", dbPath, fmt.Sprintf("PRAGMA table_info(%s);", table))
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var cols []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		// Default sqlite3 list mode: cid|name|type|notnull|dflt_value|pk
		parts := strings.Split(line, "|")
		if len(parts) >= 2 {
			cols = append(cols, parts[1])
		}
	}
	return cols, nil
}

// pickColumn returns the actual column name (preserving original case) matching
// the first candidate found, case-insensitively. Returns "" if none match.
func pickColumn(cols []string, candidates ...string) string {
	byLower := make(map[string]string, len(cols))
	for _, c := range cols {
		byLower[strings.ToLower(c)] = c
	}
	for _, cand := range candidates {
		if actual, ok := byLower[strings.ToLower(cand)]; ok {
			return actual
		}
	}
	return ""
}

// isRecentTimestamp attempts to parse a timestamp value read back from SQLite,
// which may be an RFC3339-ish string or a numeric Unix epoch (seconds,
// milliseconds, or microseconds). It returns whether the timestamp is within
// agent.RecentSessionTimeout, and whether parsing succeeded at all. When
// parsing fails, callers should treat the session as recent (fail open)
// rather than discarding a session we simply couldn't timestamp.
func isRecentTimestamp(s string) (recent bool, parsed bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return true, false
	}

	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		var t time.Time
		switch {
		case n > 1e15:
			t = time.UnixMicro(n)
		case n > 1e12:
			t = time.UnixMilli(n)
		case n > 0:
			t = time.Unix(n, 0)
		default:
			return true, false
		}
		return time.Since(t) <= agent.RecentSessionTimeout, true
	}

	formats := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout, true
		}
	}

	return true, false
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
// Column names are resolved dynamically via PRAGMA table_info since OpenCode's
// schema has changed naming conventions across releases (e.g. snake_case vs
// camelCase, or the timestamp representation).
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionCols, err := sqliteColumns(dbPath, "session")
	if err != nil || len(sessionCols) == 0 {
		return nil, nil
	}
	messageCols, err := sqliteColumns(dbPath, "message")
	if err != nil || len(messageCols) == 0 {
		return nil, nil
	}

	sessionIDCol := pickColumn(sessionCols, "id")
	projectIDCol := pickColumn(sessionCols, "project_id", "projectID", "projectid", "project")
	updatedCol := pickColumn(sessionCols, "time_updated", "timeUpdated", "updated", "updated_at", "updatedat")

	if sessionIDCol == "" || projectIDCol == "" {
		return nil, nil
	}

	// Find most recent session for this project
	orderClause := ""
	if updatedCol != "" {
		orderClause = fmt.Sprintf(" ORDER BY \"%s\" DESC", updatedCol)
	}
	sessionQuery := fmt.Sprintf(
		`SELECT "%s" FROM session WHERE "%s"='%s'%s LIMIT 1;`,
		sessionIDCol, projectIDCol, projectID, orderClause,
	)
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, sessionQuery)
	sessionOutput, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(sessionOutput)) == "" {
		return nil, nil
	}
	sessionID := strings.TrimSpace(string(sessionOutput))

	// Check if this session was recent (within timeout)
	if updatedCol != "" {
		timeQuery := fmt.Sprintf(`SELECT "%s" FROM session WHERE "%s"='%s';`, updatedCol, sessionIDCol, sessionID)
		cmd = exec.Command("sqlite3", dbPath, timeQuery)
		if timeOutput, err := cmd.Output(); err == nil {
			if recent, parsed := isRecentTimestamp(string(timeOutput)); parsed && !recent {
				return nil, nil
			}
		}
		// If we can't fetch or parse the time, proceed anyway — better to try than skip
	}

	msgSessionIDCol := pickColumn(messageCols, "session_id", "sessionID", "sessionid")
	dataCol := pickColumn(messageCols, "data", "content", "body")
	msgIDCol := pickColumn(messageCols, "id")
	createdCol := pickColumn(messageCols, "time_created", "timeCreated", "created", "created_at", "createdat")

	if msgSessionIDCol == "" || dataCol == "" {
		return nil, nil
	}

	selectExpr := fmt.Sprintf("\"%s\"", dataCol)
	if msgIDCol != "" {
		selectExpr = fmt.Sprintf("json_patch(\"%s\", json_object('id', \"%s\"))", dataCol, msgIDCol)
	}

	orderClause = ""
	if createdCol != "" {
		orderClause = fmt.Sprintf(" ORDER BY \"%s\"", createdCol)
	}

	// Get messages for this session as a JSON array
	msgQuery := fmt.Sprintf(
		`SELECT json_group_array(%s) FROM message WHERE "%s"='%s'%s;`,
		selectExpr, msgSessionIDCol, sessionID, orderClause,
	)
	cmd = exec.Command("sqlite3", dbPath, msgQuery)
	msgOutput, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	transcriptData := []byte(strings.TrimSpace(string(msgOutput)))
	// sqlite3 returns "[null]" when no rows match
	if string(transcriptData) == "[null]" || string(transcriptData) == "[]" {
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
```
