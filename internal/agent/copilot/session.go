package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// sessionMeta represents lightweight metadata from a Copilot session workspace file.
type sessionMeta struct {
	ID      string `yaml:"id"`
	CWD     string `yaml:"cwd,omitempty"`
	GitRoot string `yaml:"git_root,omitempty"`
}

// sessionMetaFilenames are the filenames Copilot CLI has used, across versions,
// to store lightweight session metadata within a session directory. Newer
// releases have moved away from YAML towards JSON, so both are tried.
var sessionMetaFilenames = []string{"workspace.yaml", "workspace.json"}

// idKeys, cwdKeys, and gitRootKeys list the metadata keys Copilot CLI has used,
// across versions, to record a session's identifier, working directory, and
// git root respectively. Trying multiple candidates keeps discovery working
// across upstream field renames.
var (
	idKeys      = []string{"id", "sessionId", "session_id"}
	cwdKeys     = []string{"cwd", "cwdPath", "workingDirectory", "workDir", "directory", "path"}
	gitRootKeys = []string{"git_root", "gitRoot", "repoRoot"}
)

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
// Copilot CLI has stored this file under different names and formats across
// versions, so known filename/format and field-name candidates are tried in
// turn rather than assuming a single fixed schema.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	var lastErr error

	for _, name := range sessionMetaFilenames {
		path := filepath.Join(sessionDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue
		}

		raw := map[string]interface{}{}
		if filepath.Ext(name) == ".json" {
			err = json.Unmarshal(data, &raw)
		} else {
			err = yaml.Unmarshal(data, &raw)
		}
		if err != nil {
			lastErr = err
			continue
		}

		return &sessionMeta{
			ID:      firstStringField(raw, idKeys),
			CWD:     firstStringField(raw, cwdKeys),
			GitRoot: firstStringField(raw, gitRootKeys),
		}, nil
	}

	return nil, lastErr
}

// firstStringField returns the first non-empty string value found in raw for
// any of the given candidate keys.
func firstStringField(raw map[string]interface{}, keys []string) string {
	for _, k := range keys {
		if v, ok := raw[k]; ok {
			if s, ok := v.(string); ok && s != "" {
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
