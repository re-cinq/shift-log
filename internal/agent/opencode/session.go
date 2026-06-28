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

// sqliteQuery runs a sqlite3 query and returns trimmed output, or "" on error.
func sqliteQuery(dbPath string, extraArgs []string, query string) (string, error) {
	args := append(extraArgs, dbPath, query)
	cmd := exec.Command("sqlite3", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
// It tries multiple query variants to handle schema changes across opencode versions:
//   - pre-v1.17: snake_case columns (project_id, session_id, time_updated, time_created)
//   - v1.17+:    may use camelCase columns (projectId, sessionId, timeUpdated) or
//                lack the time_updated column entirely
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
	// Try multiple column name variants for cross-version compatibility.
	sessionQueries := []string{
		// pre-v1.17: snake_case, time_updated ordering
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
		// pre-v1.17 without time_updated (use stable rowid ordering)
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY rowid DESC LIMIT 1;`, projectID),
		// v1.17+: camelCase column names
		fmt.Sprintf(`SELECT id FROM session WHERE "projectId"='%s' ORDER BY rowid DESC LIMIT 1;`, projectID),
	}

	var sessionID string
	for _, q := range sessionQueries {
		out, err := sqliteQuery(dbPath, []string{"-separator", "\t"}, q)
		if err == nil && out != "" {
			sessionID = out
			break
		}
	}
	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout).
	// Try multiple time column name variants.
	timeQueries := []string{
		fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT "timeUpdated" FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT time_created FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT "timeCreated" FROM session WHERE id='%s';`, sessionID),
	}
	for _, tq := range timeQueries {
		out, err := sqliteQuery(dbPath, nil, tq)
		if err != nil || out == "" {
			continue
		}
		var t time.Time
		var parsed bool
		if t2, err2 := time.Parse(time.RFC3339Nano, out); err2 == nil {
			t, parsed = t2, true
		} else if t2, err2 := time.Parse("2006-01-02T15:04:05.000Z", out); err2 == nil {
			t, parsed = t2, true
		} else if t2, err2 := time.Parse("2006-01-02 15:04:05", out); err2 == nil {
			t, parsed = t2, true
		}
		if parsed {
			if time.Since(t) > agent.RecentSessionTimeout {
				return nil, nil
			}
			break
		}
	}
	// If we can't parse the time from any column, proceed — better to try than skip

	// Get messages for this session as a JSON array.
	// Try multiple query variants for cross-version compatibility.
	msgQueries := []string{
		// pre-v1.17: data blob + id merge + time_created ordering
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
		// pre-v1.17: data blob + id merge + rowid ordering (no time_created dependency)
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY rowid;`, sessionID),
		// v1.17+: camelCase session_id column
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE "sessionId"='%s' ORDER BY rowid;`, sessionID),
		// Fallback: data blob only, no id merge
		fmt.Sprintf(`SELECT json_group_array(data) FROM message WHERE session_id='%s' ORDER BY rowid;`, sessionID),
		// Fallback: camelCase session_id, data blob only
		fmt.Sprintf(`SELECT json_group_array(data) FROM message WHERE "sessionId"='%s' ORDER BY rowid;`, sessionID),
	}

	var transcriptData []byte
	for _, q := range msgQueries {
		out, err := sqliteQuery(dbPath, nil, q)
		if err != nil {
			continue
		}
		// sqlite3 returns "[null]" when no rows match json_group_array
		if out == "[null]" || out == "[]" || out == "" {
			continue
		}
		transcriptData = []byte(out)
		break
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
