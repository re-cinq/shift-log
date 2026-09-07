```go
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
}

// cwdFieldCandidates lists known/plausible key names Copilot CLI has used
// for the session's working directory across versions.
var cwdFieldCandidates = []string{
	"cwd", "directory", "workingdirectory", "working_dir", "workdir",
	"path", "projectpath", "project_path", "cwdpath",
}

// idFieldCandidates lists known/plausible key names for the session identifier.
var idFieldCandidates = []string{"id", "sessionid", "session_id"}

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
// Copilot CLI's workspace.yaml schema has drifted across versions (field
// renames/nesting), so if the known "id"/"cwd" fields come back empty, this
// falls back to scanning all top-level (and one level of nested) keys for a
// plausibly-named replacement rather than failing outright.
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

	if meta.ID == "" || meta.CWD == "" {
		var raw map[string]interface{}
		if err := yaml.Unmarshal(data, &raw); err == nil {
			if meta.ID == "" {
				meta.ID = findYAMLField(raw, idFieldCandidates)
			}
			if meta.CWD == "" {
				meta.CWD = findYAMLField(raw, cwdFieldCandidates)
			}
		}
	}

	return &meta, nil
}

// findYAMLField searches top-level and one level of nested maps for a string
// value under any of the given candidate keys (case-insensitive).
func findYAMLField(raw map[string]interface{}, candidates []string) string {
	want := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		want[c] = true
	}

	for k, v := range raw {
		if !want[strings.ToLower(k)] {
			continue
		}
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}

	for _, v := range raw {
		nested, ok := v.(map[string]interface{})
		if !ok {
			continue
		}
		if s := findYAMLField(nested, candidates); s != "" {
			return s
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
