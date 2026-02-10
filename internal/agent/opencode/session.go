package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// GetDataDir returns the OpenCode data directory.
func GetDataDir() (string, error) {
	// Check environment variable first
	if dir := os.Getenv("OPENCODE_DATA_DIR"); dir != "" {
		return dir, nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}

	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "opencode"), nil
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

// SessionInfo represents an OpenCode session JSON file.
type SessionInfo struct {
	ID        string `json:"id"`
	Slug      string `json:"slug,omitempty"`
	ProjectID string `json:"projectID,omitempty"`
	Directory string `json:"directory,omitempty"`
	ParentID  string `json:"parentID,omitempty"`
	Title     string `json:"title,omitempty"`
	Version   string `json:"version,omitempty"`
	Time      struct {
		Created  string `json:"created,omitempty"`
		Updated  string `json:"updated,omitempty"`
	} `json:"time,omitempty"`
}

// MessageInfo represents an OpenCode message JSON file.
type MessageInfo struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionID,omitempty"`
	Role      string `json:"role"`
	Content   string `json:"content,omitempty"`
	Time      struct {
		Created string `json:"created,omitempty"`
	} `json:"time,omitempty"`
}

// ReadSession reads a session JSON file.
func ReadSession(path string) (*SessionInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var session SessionInfo
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}

	return &session, nil
}

// ListSessions lists all session files for a project.
func ListSessions(projectPath string) ([]SessionInfo, error) {
	sessionDir, err := GetSessionDir(projectPath)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var sessions []SessionInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		session, err := ReadSession(filepath.Join(sessionDir, entry.Name()))
		if err != nil {
			continue
		}
		sessions = append(sessions, *session)
	}

	return sessions, nil
}

// ReadMessages reads all messages for a session.
func ReadMessages(sessionID string) ([]MessageInfo, error) {
	msgDir, err := GetMessageDir(sessionID)
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(msgDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var messages []MessageInfo
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(msgDir, entry.Name()))
		if err != nil {
			continue
		}

		var msg MessageInfo
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		messages = append(messages, msg)
	}

	// Sort by filename (which are ordered by ID)
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].ID < messages[j].ID
	})

	return messages, nil
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
	session := SessionInfo{
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
