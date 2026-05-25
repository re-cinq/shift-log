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

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
// It tries multiple project-identification strategies to handle schema changes
// across OpenCode versions (pre-1.15 used root-commit hash; 1.15+ may use path
// or a normalized app/project table).
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionID := findSessionInDB(dbPath, projectID, projectPath)
	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout)
	timeQuery := fmt.Sprintf(
		`SELECT time_updated FROM session WHERE id='%s';`,
		sqlEscape(sessionID),
	)
	cmd := exec.Command("sqlite3", dbPath, timeQuery)
	timeOutput, err := cmd.Output()
	if err == nil {
		if isStaleSession(strings.TrimSpace(string(timeOutput))) {
			return nil, nil
		}
	}

	// Get messages for this session as a JSON array.
	// An empty transcript is acceptable — the session existed and triggered a commit.
	transcriptData := querySessionMessages(dbPath, sessionID)
	if transcriptData == nil {
		transcriptData = []byte("[]")
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// findSessionInDB tries multiple query strategies to locate a session ID for the
// given project, handling schema changes across OpenCode versions:
//
//   - Strategy 1 (pre-1.15):  project_id = git root commit hash
//   - Strategy 2 (1.15+):     project_id = absolute directory path
//   - Strategy 3 (1.15+ normalized): session → app table join via path
//   - Strategy 4 (1.15+ normalized): session → project table join via path
//   - Strategy 5 (fallback):  most-recently-updated session (staleness checked by caller)
func findSessionInDB(dbPath, projectID, projectPath string) string {
	// Strategy 1: project_id = root git commit hash (OpenCode pre-1.15)
	if id := runSQLiteQuery(dbPath, fmt.Sprintf(
		`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`,
		sqlEscape(projectID),
	)); id != "" {
		return id
	}

	// Strategy 2: project_id = absolute path (OpenCode 1.15+)
	if id := runSQLiteQuery(dbPath, fmt.Sprintf(
		`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`,
		sqlEscape(projectPath),
	)); id != "" {
		return id
	}

	// Strategy 3: join with 'app' table via path (OpenCode 1.15+ normalized schema)
	if id := runSQLiteQuery(dbPath, fmt.Sprintf(
		`SELECT s.id FROM session s JOIN app a ON s.project_id=a.id WHERE a.path='%s' ORDER BY s.time_updated DESC LIMIT 1;`,
		sqlEscape(projectPath),
	)); id != "" {
		return id
	}

	// Strategy 4: join with 'project' table via path
	if id := runSQLiteQuery(dbPath, fmt.Sprintf(
		`SELECT s.id FROM session s JOIN project p ON s.project_id=p.id WHERE p.path='%s' ORDER BY s.time_updated DESC LIMIT 1;`,
		sqlEscape(projectPath),
	)); id != "" {
		return id
	}

	// Strategy 5: most-recently-updated session regardless of project.
	// The staleness check in discoverFromSQLite provides the safety net.
	return runSQLiteQuery(dbPath, `SELECT id FROM session ORDER BY time_updated DESC LIMIT 1;`)
}

// runSQLiteQuery executes a sqlite3 query and returns the trimmed first-line output.
// Returns "" on error or empty result.
func runSQLiteQuery(dbPath, query string) string {
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sqlEscape escapes single quotes for use in SQLite string literals.
func sqlEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// isStaleSession reports whether a session timestamp indicates the session is too old.
// Returns false (not stale — proceed) when the format is unrecognized.
func isStaleSession(timeStr string) bool {
	if timeStr == "" {
		return false
	}

	// Standard ISO 8601 string formats
	for _, format := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(format, timeStr); err == nil {
			return time.Since(t) > agent.RecentSessionTimeout
		}
	}

	// Unix milliseconds integer (OpenCode 1.15+ stores timestamps this way)
	if ms, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		t := time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
		return time.Since(t) > agent.RecentSessionTimeout
	}

	// Unknown format — proceed rather than skip
	return false
}

// querySessionMessages retrieves transcript data for a session as a JSON array.
// It first tries json_group_array (requires SQLite JSON1), then falls back to
// a simple per-row query for environments without JSON extensions.
// Returns nil only on a hard query error; empty results return []byte("[]").
func querySessionMessages(dbPath, sessionID string) []byte {
	// Primary: aggregate all messages into a JSON array in SQL
	primary := fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sqlEscape(sessionID),
	)
	cmd := exec.Command("sqlite3", dbPath, primary)
	out, err := cmd.Output()
	if err == nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" && trimmed != "[null]" && trimmed != "[]" {
			return []byte(trimmed)
		}
	}

	// Fallback: fetch raw data rows and build the array in Go.
	// Handles environments without SQLite JSON1 and schema variants where
	// json_patch/json_group_array are unavailable or column names differ.
	fallback := fmt.Sprintf(
		`SELECT data FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sqlEscape(sessionID),
	)
	cmd = exec.Command("sqlite3", dbPath, fallback)
	out, err = cmd.Output()
	if err != nil {
		return nil
	}

	var messages []json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		messages = append(messages, json.RawMessage(line))
	}

	if len(messages) == 0 {
		return []byte("[]")
	}

	data, err := json.Marshal(messages)
	if err != nil {
		return nil
	}
	return data
}
