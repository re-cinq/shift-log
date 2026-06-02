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

// GetSessionDir returns the legacy session storage directory for a project.
// Pre-v1.15 OpenCode stored sessions under storage/session/<projectID>/.
func GetSessionDir(projectPath string) (string, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return "", err
	}

	projectID := GetProjectID(projectPath)
	return filepath.Join(dataDir, "storage", "session", projectID), nil
}

// GetMessageDir returns the legacy message storage directory for a session.
// Pre-v1.15 OpenCode stored messages under storage/message/<sessionID>/.
func GetMessageDir(sessionID string) (string, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dataDir, "storage", "message", sessionID), nil
}

// sessionInfo represents an OpenCode session JSON file (legacy flat-file format).
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

	msgDir, err := GetMessageDir(sessionID)
	if err != nil {
		return sessionPath, nil
	}

	if err := os.MkdirAll(msgDir, 0700); err != nil {
		return sessionPath, nil
	}

	msgPath := filepath.Join(msgDir, "transcript.jsonl")
	_ = os.WriteFile(msgPath, transcriptData, 0600)

	return sessionPath, nil
}

// sessionDiffMatchesProject reports whether the JSON in a session_diff file
// belongs to the given projectID. Handles both camelCase ("projectId", v1.15+)
// and snake_case ("project_id") field names.
func sessionDiffMatchesProject(data []byte, projectID string) bool {
	if projectID == "" || projectID == "global" {
		return false
	}
	var obj struct {
		ProjectIDCamel string `json:"projectId"`
		ProjectIDSnake string `json:"project_id"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return false
	}
	return obj.ProjectIDCamel == projectID || obj.ProjectIDSnake == projectID
}

// sessionIDFromDiffFile extracts the session ID from the JSON "id" field or
// falls back to stripping the .json suffix from the filename.
func sessionIDFromDiffFile(data []byte, filename string) string {
	var obj struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &obj); err == nil && obj.ID != "" {
		return obj.ID
	}
	return strings.TrimSuffix(filename, ".json")
}

// extractMessagesFromSessionDiff extracts inline messages from a session_diff
// JSON file. Returns nil when the file does not embed messages or the list is empty.
func extractMessagesFromSessionDiff(data []byte) []byte {
	var obj struct {
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil
	}
	trimmed := strings.TrimSpace(string(obj.Messages))
	if trimmed == "" || trimmed == "null" || trimmed == "[]" {
		return nil
	}
	return obj.Messages
}

// parseTimeField attempts to parse a SQLite timestamp string in several known
// formats including integer milliseconds (common in JS-originated schemas).
func parseTimeField(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	// Integer milliseconds (JavaScript epoch)
	var ms int64
	if _, err := fmt.Sscanf(s, "%d", &ms); err == nil && ms > 1_000_000_000_000 {
		return time.UnixMilli(ms)
	}
	// Integer seconds
	if _, err := fmt.Sscanf(s, "%d", &ms); err == nil && ms > 1_000_000_000 {
		return time.Unix(ms, 0)
	}
	return time.Time{}
}
