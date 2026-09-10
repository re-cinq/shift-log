package copilot

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// sessionMeta represents lightweight metadata from a Copilot session workspace.yaml.
type sessionMeta struct {
	ID      string `yaml:"id"`
	CWD     string `yaml:"cwd"`
	GitRoot string `yaml:"git_root,omitempty"`
}

// metadataFileNames lists known/possible session metadata filenames, tried in
// order. Copilot CLI has renamed this file across releases.
var metadataFileNames = []string{"workspace.yaml", "workspace.yml", "session.yaml", "metadata.yaml"}

// cwdKeys lists known/possible keys for the working-directory field, tried in
// order when the primary "cwd" field is absent. Copilot CLI has renamed this
// field across releases.
var cwdKeys = []string{"cwd", "cwd_path", "working_directory", "workingDirectory", "directory", "workspace", "root", "path"}

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
// It tolerates metadata filename and field-name drift across Copilot CLI
// releases: it tries several known filenames, and if the "cwd" field isn't
// present under its usual YAML key, it re-scans the document for known
// alternate key names (including one level of nesting under "workspace").
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	var data []byte
	var err error
	for _, name := range metadataFileNames {
		data, err = os.ReadFile(filepath.Join(sessionDir, name))
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}

	var meta sessionMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, err
	}

	if meta.CWD == "" {
		var raw map[string]interface{}
		if err := yaml.Unmarshal(data, &raw); err == nil {
			meta.CWD = findStringField(raw, cwdKeys)
		}
	}

	return &meta, nil
}

// findStringField looks up the first matching key holding a non-empty string
// value, checking both the top level and one level of nesting under a
// "workspace" key (e.g. {"workspace": {"cwd": "..."}}).
func findStringField(raw map[string]interface{}, keys []string) string {
	for _, k := range keys {
		if s, ok := raw[k].(string); ok && s != "" {
			return s
		}
	}
	if ws, ok := raw["workspace"].(map[string]interface{}); ok {
		for _, k := range keys {
			if s, ok := ws[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
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
