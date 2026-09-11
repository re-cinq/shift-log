package copilot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// sessionMeta represents lightweight metadata from a Copilot session workspace.yaml.
type sessionMeta struct {
	ID      string `yaml:"id"`
	CWD     string `yaml:"cwd"`
	GitRoot string `yaml:"git_root,omitempty"`

	// HasCWD reports whether a recognizable working-directory field was
	// found for this session. Copilot CLI has changed this field's name
	// across versions, so its absence doesn't necessarily mean the session
	// belongs to a different project - it means we couldn't tell.
	HasCWD bool `yaml:"-"`
}

// cwdFieldNames are known/likely field names Copilot CLI has used (or may
// use in future versions) for a session's working directory.
var cwdFieldNames = []string{
	"cwd", "cwd_path", "workingdirectory", "working_directory",
	"directory", "dir", "root", "workspaceroot", "workspace_root", "path",
}

// findCWDField searches a generically-parsed YAML document for a cwd-like
// field, including one level of nesting (e.g. a "workspace" object).
func findCWDField(raw map[string]interface{}) (string, bool) {
	for k, v := range raw {
		key := strings.ToLower(k)
		for _, name := range cwdFieldNames {
			if key != name {
				continue
			}
			if s, ok := v.(string); ok && s != "" {
				return s, true
			}
		}
	}
	for _, v := range raw {
		nested, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if cwd, ok := findCWDField(nested); ok {
			return cwd, true
		}
	}
	return "", false
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

	if meta.CWD != "" {
		meta.HasCWD = true
		return &meta, nil
	}

	// The known "cwd" field wasn't present or was empty. Copilot CLI has
	// relocated/renamed this field across versions, so fall back to a
	// generic scan for any cwd-like key rather than treating the session
	// as belonging to a different project.
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err == nil {
		if cwd, ok := findCWDField(raw); ok {
			meta.CWD = cwd
			meta.HasCWD = true
		}
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
