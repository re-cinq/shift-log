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
// Supports both legacy flat-file message storage (v1.2–v1.14) and the session_diff
// file storage introduced in v1.15+.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Find most recent session. Try project_id (classic: root commit hash) first,
	// then directory (v1.15+: session stores its working directory directly).
	sessionID := sqliteQueryOne(dbPath, fmt.Sprintf(
		`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`,
		escapeSQLString(projectID),
	))
	if sessionID == "" {
		sessionID = sqliteQueryOne(dbPath, fmt.Sprintf(
			`SELECT id FROM session WHERE directory='%s' ORDER BY time_updated DESC LIMIT 1;`,
			escapeSQLString(projectPath),
		))
	}
	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout)
	timeStr := sqliteQueryOne(dbPath, fmt.Sprintf(
		`SELECT time_updated FROM session WHERE id='%s';`,
		escapeSQLString(sessionID),
	))
	if timeStr != "" {
		if t, err := parseOpenCodeTimestamp(timeStr); err == nil {
			if time.Since(t) > agent.RecentSessionTimeout {
				return nil, nil
			}
		}
		// If we can't parse the time, proceed anyway — better to try than skip
	}

	// Get transcript: try session_diff file (v1.15+) first, then message table (v1.2–v1.14).
	transcriptData := readSessionDiffFile(dataDir, sessionID)
	if len(transcriptData) == 0 {
		transcriptData = readSessionMessageTable(dbPath, sessionID)
	}
	if len(transcriptData) == 0 {
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

// readSessionDiffFile reads the session transcript from the session_diff directory
// introduced in OpenCode v1.15+. Returns nil if the file does not exist or is empty.
func readSessionDiffFile(dataDir, sessionID string) []byte {
	diffPath := filepath.Join(dataDir, "storage", "session_diff", sessionID+".json")
	data, err := os.ReadFile(diffPath)
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil
	}
	return []byte(trimmed)
}

// readSessionMessageTable reads transcript data from the SQLite message table
// used by OpenCode v1.2–v1.14.
func readSessionMessageTable(dbPath, sessionID string) []byte {
	msgQuery := fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
		escapeSQLString(sessionID),
	)
	cmd := exec.Command("sqlite3", dbPath, msgQuery)
	msgOutput, err := cmd.Output()
	if err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(msgOutput))
	if trimmed == "[null]" || trimmed == "[]" || trimmed == "" {
		return nil
	}
	return []byte(trimmed)
}

// sqliteQueryOne runs a SQLite query and returns the trimmed first column of the first row.
// Returns empty string on any error or empty result.
func sqliteQueryOne(dbPath, query string) string {
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, query)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// escapeSQLString escapes single quotes for use in SQLite string literals.
func escapeSQLString(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// parseOpenCodeTimestamp parses a timestamp string stored by OpenCode.
// Handles RFC3339, custom ISO formats, plain datetime, and Unix milliseconds
// (integer), which is the format used by OpenCode v1.15+.
func parseOpenCodeTimestamp(timeStr string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, timeStr); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", timeStr); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05", timeStr); err == nil {
		return t, nil
	}
	// OpenCode v1.15+ stores timestamps as Unix milliseconds (integer)
	if ms, err := strconv.ParseInt(timeStr, 10, 64); err == nil && ms > 0 {
		return time.UnixMilli(ms), nil
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %q", timeStr)
}
