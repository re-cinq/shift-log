package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// sessionMeta represents lightweight metadata from a Copilot session file.
type sessionMeta struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
}

// GetCopilotDir returns the path to Copilot's config/data directory.
func GetCopilotDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".copilot"), nil
}

// GetSessionStateDir returns the session state directory.
func GetSessionStateDir() (string, error) {
	copilotDir, err := GetCopilotDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(copilotDir, "session-state"), nil
}

// parseSessionMeta reads a Copilot session file and extracts metadata.
func parseSessionMeta(path string) (*sessionMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var meta sessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}

	return &meta, nil
}

// WriteSessionFile writes transcript data to the Copilot session state directory.
func WriteSessionFile(sessionID string, data []byte) (string, error) {
	sessionDir, err := GetSessionStateDir()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return "", fmt.Errorf("could not create session directory: %w", err)
	}

	path := filepath.Join(sessionDir, sessionID+".json")
	return path, os.WriteFile(path, data, 0600)
}

// pathsEqual compares two paths after resolving symlinks.
func pathsEqual(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = filepath.Clean(a)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = filepath.Clean(b)
	}
	return ra == rb
}
