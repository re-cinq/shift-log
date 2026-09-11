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

// idKeys, cwdKeys and gitRootKeys are the possible YAML keys used across
// Copilot CLI versions to record a session's identifier, working directory,
// and git repository root respectively. Copilot CLI has renamed/relocated
// these fields between releases, so session discovery probes several known
// variants (including one level of nesting, e.g. a "workspace" object)
// instead of relying on a single fixed key.
var (
	idKeys      = []string{"id", "session_id", "sessionId", "sessionID"}
	cwdKeys     = []string{"cwd", "cwd_path", "workspace_dir", "workspaceFolder", "workspace_folder", "directory", "root", "workspace"}
	gitRootKeys = []string{"git_root", "gitRoot", "repo_root", "repoRoot"}
)

// firstStringValue returns the first non-empty string value found in raw for
// any of the given keys, also probing one level of nested maps (e.g. a
// "workspace" object containing a "path"/"cwd"/"directory" key).
func firstStringValue(raw map[string]interface{}, keys []string) string {
	for _, k := range keys {
		v, ok := raw[k]
		if !ok {
			continue
		}
		switch val := v.(type) {
		case string:
			if val != "" {
				return val
			}
		case map[string]interface{}:
			for _, nestedKey := range []string{"path", "cwd", "directory"} {
				if s, ok := val[nestedKey].(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return ""
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
// It tolerates schema drift across Copilot CLI versions by probing multiple
// known key names for id/cwd/git_root rather than requiring an exact match,
// since Copilot has renamed or relocated these fields between releases.
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

	return &sessionMeta{
		ID:      firstStringValue(raw, idKeys),
		CWD:     firstStringValue(raw, cwdKeys),
		GitRoot: firstStringValue(raw, gitRootKeys),
	}, nil
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
