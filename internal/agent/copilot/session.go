package copilot

import (
	"encoding/json"
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
// Tries both "session-state" and "sessions" directory names for compatibility.
func GetSessionStateDir() (string, error) {
	copilotDir, err := GetCopilotDir()
	if err != nil {
		return "", err
	}
	for _, name := range []string{"session-state", "sessions"} {
		dir := filepath.Join(copilotDir, name)
		if _, err := os.Stat(dir); err == nil {
			return dir, nil
		}
	}
	return filepath.Join(copilotDir, "session-state"), nil
}

// parseSessionMeta reads session metadata from a Copilot session directory.
// Supports workspace.yaml (old and new field names) and JSON metadata files.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	// Try workspace.yaml first
	yamlPath := filepath.Join(sessionDir, "workspace.yaml")
	if data, err := os.ReadFile(yamlPath); err == nil {
		var raw map[string]interface{}
		if yamlErr := yaml.Unmarshal(data, &raw); yamlErr == nil {
			if meta := extractSessionMeta(raw); meta.ID != "" || meta.CWD != "" {
				return meta, nil
			}
		}
	}

	// Try JSON metadata files (newer copilot versions may use JSON)
	for _, name := range []string{"context.json", "session.json", "metadata.json"} {
		jsonPath := filepath.Join(sessionDir, name)
		if data, err := os.ReadFile(jsonPath); err == nil {
			var raw map[string]interface{}
			if jsonErr := json.Unmarshal(data, &raw); jsonErr == nil {
				if meta := extractSessionMeta(raw); meta.ID != "" || meta.CWD != "" {
					return meta, nil
				}
			}
		}
	}

	return nil, fmt.Errorf("no session metadata found in %s", sessionDir)
}

// extractSessionMeta extracts session metadata from a raw map, supporting
// multiple field name conventions across copilot versions.
func extractSessionMeta(raw map[string]interface{}) *sessionMeta {
	meta := &sessionMeta{}

	// Extract session ID (try multiple field names)
	for _, field := range []string{"id", "sessionId", "session_id", "sessionID"} {
		if v, ok := raw[field].(string); ok && v != "" {
			meta.ID = v
			break
		}
	}

	// Extract working directory (try multiple field names across versions)
	for _, field := range []string{"cwd", "working_directory", "workingDirectory", "directory", "workDir", "dir"} {
		if v, ok := raw[field].(string); ok && v != "" {
			meta.CWD = v
			break
		}
	}

	// Extract git root
	for _, field := range []string{"git_root", "gitRoot"} {
		if v, ok := raw[field].(string); ok && v != "" {
			meta.GitRoot = v
			break
		}
	}

	return meta
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
