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

// discoverFromProjectLocalDB looks for the project-local opencode SQLite database
// at projectPath/.opencode/opencode.db. OpenCode v1.2+ stores its database here.
// The schema uses 'sessions'/'messages' tables (plural) with integer unix timestamps
// in 'updated_at'/'created_at' columns and no project_id (DB is project-scoped).
func discoverFromProjectLocalDB(projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(projectPath, ".opencode", "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionID, rawTimestamp := queryLatestSessionFromDB(dbPath)
	if sessionID == "" {
		return nil, nil
	}

	if rawTimestamp != "" && !sqliteTimestampIsRecent(rawTimestamp) {
		return nil, nil
	}

	transcriptData := querySessionMessages(dbPath, sessionID)
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

// queryLatestSessionFromDB returns the most recent session ID and its raw timestamp
// from an opencode SQLite database. Tries 'sessions' plural (opencode v1.2+ schema)
// then 'session' singular (intermediate versions). No project_id filter is applied
// since this is used for project-local DBs where all sessions belong to one project.
func queryLatestSessionFromDB(dbPath string) (sessionID, rawTimestamp string) {
	for _, q := range []string{
		`SELECT id, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 1;`,
		`SELECT id, time_updated FROM session ORDER BY time_updated DESC LIMIT 1;`,
	} {
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		line := strings.TrimSpace(string(out))
		if line == "" {
			continue
		}
		cols := strings.SplitN(line, "\t", 2)
		if cols[0] != "" {
			sessionID = cols[0]
			if len(cols) >= 2 {
				rawTimestamp = cols[1]
			}
			return
		}
	}
	return "", ""
}

// sqliteTimestampIsRecent returns true if the timestamp string represents a time
// within agent.RecentSessionTimeout. Handles integer unix timestamps (milliseconds
// if > 1e12, else seconds) and common text formats. Returns true if unparseable
// so that sessions with unrecognised formats are not silently dropped.
func sqliteTimestampIsRecent(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}

	// Integer unix timestamp (ms if > 1e12, else seconds)
	var n int64
	if _, err := fmt.Sscan(s, &n); err == nil {
		var t time.Time
		if n > 1_000_000_000_000 {
			t = time.UnixMilli(n)
		} else {
			t = time.Unix(n, 0)
		}
		return time.Since(t) <= agent.RecentSessionTimeout
	}

	// Text timestamp formats
	for _, format := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(format, s); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
	}

	return true // can't parse — proceed anyway
}

// querySessionMessages fetches messages for a session from a SQLite DB and
// returns a JSON array suitable for ParseTranscript. Tries 'messages' plural
// (opencode v1.2+ schema) then 'message' singular (intermediate versions).
func querySessionMessages(dbPath, sessionID string) []byte {
	queries := []string{
		// opencode v1.2+: messages table, role+id fields
		fmt.Sprintf(`SELECT json_group_array(json_object('role', role, 'id', id)) FROM messages WHERE session_id='%s' ORDER BY created_at;`, sessionID),
		// intermediate schema: message table with JSON data blob
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
	}

	for _, q := range queries {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		data := []byte(strings.TrimSpace(string(out)))
		if len(data) > 0 && string(data) != "[null]" && string(data) != "[]" {
			return data
		}
	}
	return nil
}
