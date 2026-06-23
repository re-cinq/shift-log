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
// Checks the project-local .opencode/opencode.db first (current opencode schema),
// then falls back to the user data directory.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Try project-local database first (current opencode stores DB at .opencode/opencode.db).
	localDB := filepath.Join(projectPath, ".opencode", "opencode.db")
	if _, err := os.Stat(localDB); err == nil {
		return discoverFromDB(localDB, projectPath)
	}

	// Fall back to user data directory (XDG-based path used by older or global installs).
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	return discoverFromDB(dbPath, projectPath)
}

// discoverFromDB finds the most recent session in an opencode SQLite database.
// Tries the current schema (sessions/updated_at/messages/parts) first,
// then falls back to the legacy schema (session/time_updated/message/data).
func discoverFromDB(dbPath, projectPath string) (*agent.SessionInfo, error) {
	// Try current schema: plural table names, Unix ms timestamps, parts column.
	cmd := exec.Command("sqlite3", dbPath, `SELECT id FROM sessions ORDER BY updated_at DESC LIMIT 1;`)
	out, err := cmd.Output()
	if err == nil && strings.TrimSpace(string(out)) != "" {
		return discoverFromDBCurrentSchema(dbPath, strings.TrimSpace(string(out)), projectPath)
	}

	// Fall back to legacy schema: singular table names, ISO timestamps, data column.
	cmd = exec.Command("sqlite3", dbPath, `SELECT id FROM session ORDER BY time_updated DESC LIMIT 1;`)
	out, err = cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	return discoverFromDBLegacySchema(dbPath, strings.TrimSpace(string(out)), projectPath)
}

// discoverFromDBCurrentSchema handles the current opencode schema
// (tables: sessions, messages; updated_at/created_at as Unix ms; message data in parts).
func discoverFromDBCurrentSchema(dbPath, sessionID, projectPath string) (*agent.SessionInfo, error) {
	// Check recency: updated_at is stored as Unix milliseconds.
	cmd := exec.Command("sqlite3", dbPath, fmt.Sprintf(`SELECT updated_at FROM sessions WHERE id='%s';`, sessionID))
	out, err := cmd.Output()
	if err == nil {
		if ms, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64); err == nil {
			t := time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
			if time.Since(t) > agent.RecentSessionTimeout {
				return nil, nil
			}
		}
		// If we can't parse (e.g. ISO string stored instead), proceed anyway.
	}

	// Get messages as a JSON array using the parts column.
	msgQuery := fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts), 'model', COALESCE(model, ''))) FROM messages WHERE session_id='%s' ORDER BY created_at;`,
		sessionID,
	)
	cmd = exec.Command("sqlite3", dbPath, msgQuery)
	out, err = cmd.Output()
	if err != nil {
		return nil, nil
	}

	transcriptData := []byte(strings.TrimSpace(string(out)))
	if string(transcriptData) == "[null]" || string(transcriptData) == "[]" {
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

// discoverFromDBLegacySchema handles the legacy opencode schema
// (tables: session, message; timestamps as ISO strings; message data in data column).
func discoverFromDBLegacySchema(dbPath, sessionID, projectPath string) (*agent.SessionInfo, error) {
	// Check recency using ISO timestamp.
	cmd := exec.Command("sqlite3", dbPath, fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID))
	out, err := cmd.Output()
	if err == nil {
		timeStr := strings.TrimSpace(string(out))
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, timeStr); err == nil {
				if time.Since(t) > agent.RecentSessionTimeout {
					return nil, nil
				}
				break
			}
		}
		// If we can't parse the time, proceed anyway — better to try than skip.
	}

	// Get messages using the data column.
	msgQuery := fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sessionID,
	)
	cmd = exec.Command("sqlite3", dbPath, msgQuery)
	out, err = cmd.Output()
	if err != nil {
		return nil, nil
	}

	transcriptData := []byte(strings.TrimSpace(string(out)))
	if string(transcriptData) == "[null]" || string(transcriptData) == "[]" {
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
