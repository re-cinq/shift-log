package opencode

import (
	"bytes"
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
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Find most recent session for this project.
	// Use rowid DESC for ordering — always available regardless of OpenCode schema version.
	sessionQuery := fmt.Sprintf(
		`SELECT id FROM session WHERE project_id='%s' ORDER BY rowid DESC LIMIT 1;`,
		projectID,
	)
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, sessionQuery)
	sessionOutput, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(sessionOutput)) == "" {
		return nil, nil
	}
	sessionID := strings.TrimSpace(string(sessionOutput))

	// Check if this session was recent (within timeout).
	// Try known time column names across OpenCode schema versions (time_updated, updated, etc.).
	for _, col := range []string{"time_updated", "updated", "updatedAt", "updated_at"} {
		timeQuery := fmt.Sprintf(`SELECT %s FROM session WHERE id='%s';`, col, sessionID)
		cmd = exec.Command("sqlite3", dbPath, timeQuery)
		timeOutput, err2 := cmd.Output()
		if err2 != nil {
			continue // column does not exist in this schema version; try next
		}
		timeStr := strings.TrimSpace(string(timeOutput))
		if stale, ok := parseSessionStale(timeStr); ok && stale {
			return nil, nil
		}
		break
	}

	// Get messages for this session.
	// Use -json mode and SELECT data (without json_patch which requires SQLite 3.38+).
	// Order by rowid — works across all SQLite schema versions.
	msgQuery := fmt.Sprintf(
		`SELECT data FROM message WHERE session_id='%s' ORDER BY rowid;`,
		sessionID,
	)
	cmd = exec.Command("sqlite3", "-json", dbPath, msgQuery)
	msgOutput, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	transcriptData := buildTranscriptFromSQLiteRows(msgOutput)
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

// parseSessionStale parses a timestamp string and reports whether the session is stale.
// Returns (isStale, parsed) — parsed is false when the format is unrecognised.
func parseSessionStale(timeStr string) (bool, bool) {
	if timeStr == "" {
		return false, false
	}
	for _, format := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(format, timeStr); err == nil {
			return time.Since(t) > agent.RecentSessionTimeout, true
		}
	}
	// Unix milliseconds (integer) — used by newer OpenCode versions
	if ms, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		t := time.UnixMilli(ms)
		return time.Since(t) > agent.RecentSessionTimeout, true
	}
	return false, false
}

// buildTranscriptFromSQLiteRows converts sqlite3 -json output into a JSON array
// of message objects suitable for ParseTranscript.
//
// sqlite3 -json outputs: [{"data":"<json-string-or-object>"}, ...]
// Each "data" value may be a JSON-encoded string or a JSON object directly.
func buildTranscriptFromSQLiteRows(raw []byte) []byte {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil
	}

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil
	}

	var messages []json.RawMessage
	for _, row := range rows {
		dataRaw, ok := row["data"]
		if !ok {
			continue
		}
		// data may be stored as a quoted JSON string or an inline JSON object
		var str string
		if json.Unmarshal(dataRaw, &str) == nil {
			if json.Valid([]byte(str)) {
				messages = append(messages, json.RawMessage(str))
			}
		} else if json.Valid(dataRaw) {
			messages = append(messages, dataRaw)
		}
	}

	if len(messages) == 0 {
		return nil
	}

	result, err := json.Marshal(messages)
	if err != nil {
		return nil
	}
	return result
}
