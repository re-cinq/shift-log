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

// getSQLiteColumns returns a lowercase set of column names for a table in the SQLite DB.
// Returns nil if the query fails (e.g. table does not exist or sqlite3 unavailable).
func getSQLiteColumns(dbPath, tableName string) map[string]bool {
	cmd := exec.Command("sqlite3", dbPath, fmt.Sprintf("PRAGMA table_info(%s);", tableName))
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	cols := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Split(line, "|")
		if len(fields) >= 2 {
			name := strings.ToLower(strings.TrimSpace(fields[1]))
			if name != "" {
				cols[name] = true
			}
		}
	}
	return cols
}

// pickColumn returns the first candidate whose lowercase form exists in cols.
// Falls back to the first candidate if cols is nil or no candidate matches.
func pickColumn(cols map[string]bool, candidates ...string) string {
	if cols != nil {
		for _, c := range candidates {
			if cols[strings.ToLower(c)] {
				return c
			}
		}
	}
	return candidates[0]
}

// readMessagesFromSQLite reads messages from the opencode SQLite DB for a session.
// It handles both the legacy schema (data column) and the v1.17+ schema (parts column).
// Returns nil when no messages are found or the DB cannot be queried.
func readMessagesFromSQLite(dbPath, sessionID string) ([]byte, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	msgCols := getSQLiteColumns(dbPath, "message")
	sessionIDCol := pickColumn(msgCols, "session_id", "sessionID")
	timeCreCol := pickColumn(msgCols, "time_created", "created", "time")

	var selectExpr string
	if msgCols != nil && !msgCols["data"] && msgCols["parts"] {
		// v1.17+ schema: separate role/parts/id columns; map parts → content for parsing.
		roleCol := pickColumn(msgCols, "role")
		selectExpr = fmt.Sprintf(
			`json_group_array(json_object('id', id, 'role', %s, 'content', json(%s)))`,
			roleCol, "parts",
		)
	} else {
		// Legacy schema: data column contains full message JSON.
		selectExpr = `json_group_array(json(data))`
	}

	query := fmt.Sprintf(
		`SELECT %s FROM message WHERE %s='%s' ORDER BY %s;`,
		selectExpr, sessionIDCol, sessionID, timeCreCol,
	)

	cmd := exec.Command("sqlite3", dbPath, query)
	output, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	data := strings.TrimSpace(string(output))
	if data == "" || data == "[]" || data == "[null]" {
		return nil, nil
	}

	return []byte(data), nil
}

// isSessionRecent reports whether a raw SQLite timestamp string represents a session
// that falls within agent.RecentSessionTimeout. Handles ISO 8601 and Unix milliseconds
// (used in opencode v1.17+). Returns true when the format is unrecognised so that
// sessions are not silently dropped.
func isSessionRecent(timeStr string) bool {
	if timeStr == "" {
		return true
	}

	// Unix milliseconds (integer) — used in opencode v1.17+
	if ms, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		t := time.Unix(ms/1000, 0)
		return time.Since(t) <= RecentSessionTimeout
	}

	for _, format := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(format, timeStr); err == nil {
			return time.Since(t) <= RecentSessionTimeout
		}
	}

	return true // assume recent when unparseable
}
