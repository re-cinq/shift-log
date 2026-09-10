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

// stringFromYAML looks up the first non-empty string value among the given
// keys in a decoded YAML map. Copilot CLI has used different field names
// for the same concept across releases (e.g. "cwd" vs "workingDirectory"),
// so callers probe a list of known aliases instead of a single fixed name.
func stringFromYAML(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// parseSessionMeta reads a workspace.yaml from a Copilot session directory.
// The document is decoded generically (rather than into a fixed struct) so
// that alternate or nested field names used by newer Copilot CLI releases
// are still recognized.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	path := filepath.Join(sessionDir, "workspace.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	meta := &sessionMeta{
		ID:      stringFromYAML(raw, "id", "sessionId", "session_id"),
		CWD:     stringFromYAML(raw, "cwd", "cwd_path", "workingDirectory", "working_directory", "path", "projectPath", "project_path"),
		GitRoot: stringFromYAML(raw, "git_root", "gitRoot"),
	}

	// Some releases nest workspace info under a "workspace" key instead of
	// storing it at the document root.
	if ws, ok := raw["workspace"].(map[string]interface{}); ok {
		if meta.ID == "" {
			meta.ID = stringFromYAML(ws, "id", "sessionId")
		}
		if meta.CWD == "" {
			meta.CWD = stringFromYAML(ws, "cwd", "workingDirectory", "path")
		}
		if meta.GitRoot == "" {
			meta.GitRoot = stringFromYAML(ws, "git_root", "gitRoot")
		}
	}

	return meta, nil
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
