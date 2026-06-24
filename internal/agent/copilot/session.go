package copilot

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// sessionMeta represents lightweight metadata from a Copilot session workspace.yaml.
// Field tags cover both the legacy format (v<=1.0.44) and the new format (v1.0.64+).
type sessionMeta struct {
	ID        string `yaml:"id"`
	CWD       string `yaml:"cwd"`
	Directory string `yaml:"directory"` // v1.0.64+: replaces cwd
	GitRoot   string `yaml:"git_root,omitempty"`
	Path      string `yaml:"path,omitempty"` // v1.0.64+: alternative to cwd
}

// effectiveCWD returns the best available working directory from the session metadata,
// checking multiple field names used across copilot CLI versions.
func (m *sessionMeta) effectiveCWD() string {
	if m.CWD != "" {
		return m.CWD
	}
	if m.Directory != "" {
		return m.Directory
	}
	if m.Path != "" {
		return m.Path
	}
	return m.GitRoot
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

// getSessionStateDirs returns all known session state directory paths for
// different copilot CLI versions. Tried in order; first directory that exists wins.
func getSessionStateDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	xdgState := os.Getenv("XDG_STATE_HOME")
	if xdgState == "" {
		xdgState = filepath.Join(home, ".local", "state")
	}

	return []string{
		filepath.Join(home, ".copilot", "session-state"),          // <= v1.0.44
		filepath.Join(home, ".copilot", "sessions"),                // v1.0.64+: renamed dir
		filepath.Join(xdgState, "copilot", "sessions"),             // v1.0.64+: XDG state
		filepath.Join(home, ".config", "github-copilot", "sessions"), // v1.0.64+: XDG config variant
		filepath.Join(home, ".local", "share", "copilot", "sessions"), // XDG data variant
	}
}

// parseSessionMeta reads a workspace.yaml from a Copilot session directory.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	path := filepath.Join(sessionDir, "workspace.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var meta sessionMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, err
	}

	return &meta, nil
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
