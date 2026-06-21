```go
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

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
// It uses a cascading query strategy to handle schema changes across opencode versions:
//   - v1.2–v1.16: session table has project_id (git root commit) and time_updated columns
//   - v1.17+: column names may differ; falls back to rowid ordering and no-filter queries
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Try queries in order of specificity.  Later queries handle schema changes
	// in opencode v1.17+ where column names or project_id format may differ.
	sessionID := sqliteQueryFirst(dbPath, []string{
		// Preferred: project-scoped, most-recently-updated first
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
		// Fallback: project_id matches but time_updated column was renamed
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY rowid DESC LIMIT 1;`, projectID),
		// Fallback: project_id value format changed — grab the most recent session globally
		`SELECT id FROM session ORDER BY time_updated DESC LIMIT 1;`,
		// Fallback: neither project_id nor time_updated columns match
		`SELECT id FROM session ORDER BY rowid DESC LIMIT 1;`,
	})

	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout).
	// Try multiple column names for the timestamp to handle version differences.
	if !sessionIsRecent(dbPath, sessionID) {
		return nil, nil
	}

	// Get messages for this session as a JSON array.
	// Try time_created ordering first, fall back to rowid for schema changes.
	transcriptData := sqliteQueryRaw(dbPath, []string{
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY rowid;`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(data) FROM message WHERE session_id='%s' ORDER BY rowid;`, sessionID),
	})

	if len(transcriptData) == 0 || string(transcriptData) == "[null]" || string(transcriptData) == "[]" {
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

// sqliteQueryFirst runs a list of sqlite3 queries in order and returns the first
// non-empty trimmed result.  Queries that fail (non-zero exit) are skipped.
func sqliteQueryFirst(dbPath string, queries []string) string {
	for _, q := range queries {
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	return ""
}

// sqliteQueryRaw runs a list of sqlite3 queries in order and returns the raw
// output bytes of the first query that succeeds and produces non-empty output.
func sqliteQueryRaw(dbPath string, queries []string) []byte {
	for _, q := range queries {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		if s := strings.TrimSpace(string(out)); s != "" {
			return []byte(s)
		}
	}
	return nil
}

// sessionIsRecent returns true if the session was updated within RecentSessionTimeout.
// It tries multiple timestamp column names to handle schema changes across versions.
func sessionIsRecent(dbPath, sessionID string) bool {
	timeColumns := []string{"time_updated", "updated_at", "time_created", "created_at"}
	for _, col := range timeColumns {
		q := fmt.Sprintf(`SELECT %s FROM session WHERE id='%s';`, col, sessionID)
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		timeStr := strings.TrimSpace(string(out))
		if timeStr == "" {
			continue
		}
		// Try multiple time formats
		formats := []string{
			time.RFC3339Nano,
			"2006-01-02T15:04:05.000Z",
			"2006-01-02 15:04:05",
		}
		for _, fmt := range formats {
			if t, err := time.Parse(fmt, timeStr); err == nil {
				return time.Since(t) <= agent.RecentSessionTimeout
			}
		}
		// Unrecognised format — assume recent so we don't silently drop the session
		return true
	}
	// No timestamp column found — proceed (better to try than skip)
	return true
}
```
