package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SessionEntry represents an entry in Claude's sessions-index.json
type SessionEntry struct {
	SessionID    string `json:"sessionId"`
	FullPath     string `json:"fullPath"`
	FileMtime    int64  `json:"fileMtime"`
	FirstPrompt  string `json:"firstPrompt"`
	Summary      string `json:"summary"`
	MessageCount int    `json:"messageCount"`
	Created      string `json:"created"`
	Modified     string `json:"modified"`
	GitBranch    string `json:"gitBranch"`
	ProjectPath  string `json:"projectPath"`
	IsSidechain  bool   `json:"isSidechain"`
}

// SessionsIndex represents Claude's sessions-index.json structure
type SessionsIndex struct {
	Version int            `json:"version"`
	Entries []SessionEntry `json:"entries"`
}

// EncodeProjectPath converts an absolute path to Claude's encoded format
// e.g., "/Users/dev/workspace/myproject" -> "-Users-dev-workspace-myproject"
func EncodeProjectPath(projectPath string) string {
	// Replace path separators with dashes
	encoded := strings.ReplaceAll(projectPath, string(filepath.Separator), "-")
	// On Windows, also replace forward slashes
	encoded = strings.ReplaceAll(encoded, "/", "-")
	// Ensure it starts with a dash (the root "/" becomes the leading dash)
	if !strings.HasPrefix(encoded, "-") {
		encoded = "-" + encoded
	}
	return encoded
}

// GetClaudeProjectsDir returns the path to Claude's projects directory
// Uses HOME environment variable, which can be overridden for testing
func GetClaudeProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// GetSessionDir returns the full path to a project's session directory
func GetSessionDir(projectPath string) (string, error) {
	projectsDir, err := GetClaudeProjectsDir()
	if err != nil {
		return "", err
	}
	encoded := EncodeProjectPath(projectPath)
	return filepath.Join(projectsDir, encoded), nil
}

// GetSessionFilePath returns the full path to a session's JSONL file
func GetSessionFilePath(projectPath, sessionID string) (string, error) {
	sessionDir, err := GetSessionDir(projectPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(sessionDir, sessionID+".jsonl"), nil
}

// WriteSessionFile writes a transcript to Claude's session file location
func WriteSessionFile(projectPath, sessionID string, transcriptData []byte) (string, error) {
	sessionPath, err := GetSessionFilePath(projectPath, sessionID)
	if err != nil {
		return "", err
	}

	// Ensure directory exists
	sessionDir := filepath.Dir(sessionPath)
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return "", fmt.Errorf("could not create session directory: %w", err)
	}

	// Write the transcript file
	if err := os.WriteFile(sessionPath, transcriptData, 0600); err != nil {
		return "", fmt.Errorf("could not write session file: %w", err)
	}

	return sessionPath, nil
}

// ReadSessionsIndex reads the sessions-index.json for a project
func ReadSessionsIndex(projectPath string) (*SessionsIndex, error) {
	sessionDir, err := GetSessionDir(projectPath)
	if err != nil {
		return nil, err
	}

	indexPath := filepath.Join(sessionDir, "sessions-index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &SessionsIndex{Version: 1, Entries: []SessionEntry{}}, nil
		}
		return nil, err
	}

	var index SessionsIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("could not parse sessions-index.json: %w", err)
	}

	return &index, nil
}

// WriteSessionsIndex writes the sessions-index.json for a project
func WriteSessionsIndex(projectPath string, index *SessionsIndex) error {
	sessionDir, err := GetSessionDir(projectPath)
	if err != nil {
		return err
	}

	// Ensure directory exists
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return fmt.Errorf("could not create session directory: %w", err)
	}

	indexPath := filepath.Join(sessionDir, "sessions-index.json")
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal sessions-index.json: %w", err)
	}

	return os.WriteFile(indexPath, data, 0600)
}

// AddOrUpdateSessionEntry adds or updates a session entry in the index
func AddOrUpdateSessionEntry(index *SessionsIndex, entry SessionEntry) {
	// Check if session already exists and update it
	for i, e := range index.Entries {
		if e.SessionID == entry.SessionID {
			index.Entries[i] = entry
			return
		}
	}
	// Add new entry
	index.Entries = append(index.Entries, entry)
}

// RestoreSession restores a session to Claude's location and updates the index
func RestoreSession(projectPath, sessionID, gitBranch string, transcriptData []byte, messageCount int, summary string) error {
	// Write the session file
	sessionPath, err := WriteSessionFile(projectPath, sessionID, transcriptData)
	if err != nil {
		return err
	}

	// Get file info for mtime
	fileInfo, err := os.Stat(sessionPath)
	if err != nil {
		return fmt.Errorf("could not stat session file: %w", err)
	}

	// Read current index
	index, err := ReadSessionsIndex(projectPath)
	if err != nil {
		return fmt.Errorf("could not read sessions index: %w", err)
	}

	// Extract first prompt from transcript (first user message)
	firstPrompt := extractFirstPrompt(transcriptData)

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Create or update entry
	entry := SessionEntry{
		SessionID:    sessionID,
		FullPath:     sessionPath,
		FileMtime:    fileInfo.ModTime().UnixMilli(),
		FirstPrompt:  firstPrompt,
		Summary:      summary,
		MessageCount: messageCount,
		Created:      now,
		Modified:     now,
		GitBranch:    gitBranch,
		ProjectPath:  projectPath,
		IsSidechain:  false,
	}

	AddOrUpdateSessionEntry(index, entry)

	// Write updated index
	if err := WriteSessionsIndex(projectPath, index); err != nil {
		return fmt.Errorf("could not write sessions index: %w", err)
	}

	return nil
}

// extractFirstPrompt extracts the first user message from transcript data
func extractFirstPrompt(transcriptData []byte) string {
	transcript, err := ParseTranscript(strings.NewReader(string(transcriptData)))
	if err != nil {
		return "No prompt"
	}

	for _, entry := range transcript.Entries {
		if entry.Type == MessageTypeUser && entry.Message != nil {
			// Extract text from content blocks
			var prompt string
			for _, block := range entry.Message.Content {
				if block.Type == "text" && block.Text != "" {
					prompt = block.Text
					break
				}
			}
			if prompt == "" {
				continue
			}
			// Truncate long prompts
			if len(prompt) > 200 {
				return prompt[:197] + "..."
			}
			return prompt
		}
	}

	return "No prompt"
}
