```go
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

// cwdKeys are the field names Copilot CLI has used across versions to record
// a session's working directory in workspace.yaml.
var cwdKeys = []string{
	"cwd", "cwdPath", "cwd_path", "workingDirectory", "working_directory",
	"directory", "workspacePath", "workspace_path", "path",
}

// idKeys are the field names used to record the session ID in workspace.yaml.
var idKeys = []string{"id", "sessionId", "session_id"}

// parseSessionMeta reads a workspace.yaml from a Copilot session directory.
// It tolerates schema drift across Copilot CLI versions: if the known "cwd"/
// "id" fields aren't populated by the strict struct decode, it falls back to
// scanning a generic map for alternate field names, so a rename upstream
// doesn't silently make every session invisible to shiftlog.
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

	if meta.CWD == "" || meta.ID == "" {
		var raw map[string]interface{}
		if err := yaml.Unmarshal(data, &raw); err == nil {
			if meta.CWD == "" {
				meta.CWD = firstStringField(raw, cwdKeys)
			}
			if meta.ID == "" {
				meta.ID = firstStringField(raw, idKeys)
			}
		}
	}

	return &meta, nil
}

// firstStringField returns the first non-empty string value found in raw
// for any of the given keys.
func firstStringField(raw map[string]interface{}, keys []string) string {
	for _, key := range keys {
		if v, ok := raw[key].(string); ok && v != "" {
			return v
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
```
