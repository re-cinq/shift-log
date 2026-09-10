package copilot

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// sessionMeta represents lightweight metadata from a Copilot session workspace file.
// Field names have shifted across Copilot CLI releases, so several aliases are
// accepted for the working-directory field.
type sessionMeta struct {
	ID         string `yaml:"id"`
	CWD        string `yaml:"cwd"`
	Directory  string `yaml:"directory,omitempty"`
	WorkingDir string `yaml:"workingDirectory,omitempty"`
	GitRoot    string `yaml:"git_root,omitempty"`
}

// effectiveCWD returns the best-available working-directory value, tolerating
// field renames across Copilot CLI versions.
func (m *sessionMeta) effectiveCWD() string {
	switch {
	case m.CWD != "":
		return m.CWD
	case m.Directory != "":
		return m.Directory
	case m.WorkingDir != "":
		return m.WorkingDir
	default:
		return ""
	}
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

// parseSessionMeta reads session metadata from a Copilot session directory.
// Newer Copilot CLI releases have been observed writing either
// workspace.yaml or workspace.json; both are attempted since YAML parsing
// also accepts well-formed JSON.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	var lastErr error
	for _, name := range []string{"workspace.yaml", "workspace.json"} {
		data, err := os.ReadFile(filepath.Join(sessionDir, name))
		if err != nil {
			lastErr = err
			continue
		}

		var meta sessionMeta
		if err := yaml.Unmarshal(data, &meta); err != nil {
			lastErr = err
			continue
		}

		return &meta, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no session metadata file found in %s", sessionDir)
	}
	return nil, lastErr
}

// GetTranscriptPath returns the path to the events.jsonl transcript within a session directory.
func GetTranscriptPath(sessionDir string) string {
	return filepath.Join(sessionDir, "events.jsonl")
}

// WriteSessionFile writes a session directory structure to Copilot's session state directory.
// Creates <sessionDir>/<sessionID>/ with workspace.yaml and events.jsonl.
func WriteSessionFile(sessionID string, data []byte) (string, error) {
	stateDir, err := GetSessionStateDir()
	if err != nil {
		return "", err
	}

	sessionDir := filepath.Join(stateDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return "", fmt.Errorf("could not create session directory: %w", err)
	}

	// Write workspace.yaml
	meta := sessionMeta{ID: sessionID}
	yamlData, err := yaml.Marshal(&meta)
	if err != nil {
		return "", fmt.Errorf("could not marshal workspace.yaml: %w", err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "workspace.yaml"), yamlData, 0600); err != nil {
		return "", err
	}

	// Write events.jsonl
	eventsPath := GetTranscriptPath(sessionDir)
	return eventsPath, os.WriteFile(eventsPath, data, 0600)
}
