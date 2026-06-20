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

// GetDataDir returns the primary OpenCode data directory.
// On Linux, opencode v1.16+ uses XDG_STATE_HOME; older versions used XDG_DATA_HOME.
// When XDG_DATA_HOME is explicitly set we honour it for backward compatibility (tests).
func GetDataDir() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not determine home directory: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "opencode"), nil
	}

	// Explicit XDG_DATA_HOME takes priority (used by isolation tests).
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode"), nil
	}

	// opencode v1.16+ switched to XDG_STATE_HOME on Linux.
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	// Default to XDG_STATE_HOME convention (~/.local/state) for v1.16+.
	return filepath.Join(home, ".local", "state", "opencode"), nil
}

// getDataDirCandidates returns all candidate data directories to probe.
// Different opencode versions write to different XDG locations, so we try both.
func getDataDirCandidates() []string {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		return []string{filepath.Join(home, "Library", "Application Support", "opencode")}
	}

	// When XDG_DATA_HOME is explicitly set, honour it only (isolation test mode).
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return []string{filepath.Join(xdg, "opencode")}
	}

	home, _ := os.UserHomeDir()

	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}

	// Try XDG_STATE_HOME (v1.16+) before XDG_DATA_HOME (legacy).
	return []string{
		filepath.Join(stateHome, "opencode"),
		filepath.Join(home, ".local", "share", "opencode"),
	}
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

// isRecentTime reports whether timeStr is within RecentSessionTimeout.
// Handles Unix milliseconds integers (opencode v1.16+), RFC3339, and ISO 8601 variants.
// Returns true for unrecognised formats so valid sessions are not silently skipped.
func isRecentTime(timeStr string) bool {
	if timeStr == "" {
		return true
	}
	// Unix milliseconds (opencode v1.16+): pure integer string
	if ms, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		return time.Since(time.UnixMilli(ms)) <= agent.RecentSessionTimeout
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, timeStr); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
	}
	// Unrecognised format — proceed rather than skip
	return true
}

// querySessionMessages retrieves transcript JSON from the message table.
// It detects the available columns via PRAGMA and selects the appropriate query,
// supporting both the legacy 'data' blob schema and the v1.17+ separate-column schema.
func querySessionMessages(dbPath, sessionID string) ([]byte, error) {
	cols := getMessageTableColumns(dbPath)

	var msgQuery string
	switch {
	case cols["data"]:
		// Legacy schema (pre-v1.17): full message JSON stored in 'data' column.
		msgQuery = fmt.Sprintf(
			`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
			sessionID,
		)
	case cols["role"] && cols["parts"]:
		// v1.17+ schema: 'role' + 'parts' (content array) columns.
		msgQuery = fmt.Sprintf(
			`SELECT json_group_array(json_object('id', id, 'role', role, 'content', json(parts))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
			sessionID,
		)
	case cols["role"] && cols["content"]:
		// v1.17+ schema variant: 'role' + 'content' columns.
		msgQuery = fmt.Sprintf(
			`SELECT json_group_array(json_object('id', id, 'role', role, 'content', content)) FROM message WHERE session_id='%s' ORDER BY time_created;`,
			sessionID,
		)
	default:
		// Unknown schema: capture at least message IDs so a session entry is created.
		msgQuery = fmt.Sprintf(
			`SELECT json_group_array(json_object('id', id)) FROM message WHERE session_id='%s';`,
			sessionID,
		)
	}

	cmd := exec.Command("sqlite3", dbPath, msgQuery)
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	trimmed := []byte(strings.TrimSpace(string(output)))
	if string(trimmed) == "[null]" || string(trimmed) == "[]" {
		return nil, nil
	}
	return trimmed, nil
}

// getMessageTableColumns returns the set of column names present in the 'message' table.
func getMessageTableColumns(dbPath string) map[string]bool {
	cols := make(map[string]bool)
	cmd := exec.Command("sqlite3", dbPath, "PRAGMA table_info(message);")
	output, err := cmd.Output()
	if err != nil {
		return cols
	}
	// Each PRAGMA line: cid|name|type|notnull|dflt_value|pk
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) >= 2 {
			cols[strings.TrimSpace(parts[1])] = true
		}
	}
	return cols
}
