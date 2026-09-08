package copilot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/re-cinq/shift-log/internal/agent"
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

// parseSessionMetaGeneric reads a session directory's workspace.yaml (if present)
// into a generic structure. Copilot CLI releases have renamed metadata fields
// (e.g. "cwd" -> something else) across versions; a generic decode lets callers
// fall back to a lenient, name-agnostic match instead of failing outright when
// the known field names change.
func parseSessionMetaGeneric(sessionDir string) (map[string]interface{}, error) {
	path := filepath.Join(sessionDir, "workspace.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// containsPathValue recursively searches a decoded YAML value for a string that
// resolves to the same filesystem location as targetPath. This is used as a
// fallback when the expected metadata field name isn't present.
func containsPathValue(v interface{}, targetPath string) bool {
	switch val := v.(type) {
	case string:
		return val != "" && agent.PathsEqual(val, targetPath)
	case map[string]interface{}:
		for _, item := range val {
			if containsPathValue(item, targetPath) {
				return true
			}
		}
	case []interface{}:
		for _, item := range val {
			if containsPathValue(item, targetPath) {
				return true
			}
		}
	}
	return false
}

// GetTranscriptPath returns the path to the events transcript within a session directory.
// Copilot CLI has historically used events.jsonl; if that file isn't present (e.g. a
// renamed transcript file in a newer CLI version), this falls back to the most recently
// modified *.jsonl file in the directory.
func GetTranscriptPath(sessionDir string) string {
	primary := filepath.Join(sessionDir, "events.jsonl")
	if _, err := os.Stat(primary); err == nil {
		return primary
	}

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return primary
	}

	var bestPath string
	var bestModTime int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if bestPath == "" || info.ModTime().Unix() > bestModTime {
			bestPath = filepath.Join(sessionDir, entry.Name())
			bestModTime = info.ModTime().Unix()
		}
	}

	if bestPath != "" {
		return bestPath
	}
	return primary
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
