package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// sessionMeta represents lightweight metadata about a Copilot session,
// extracted from its workspace metadata file.
type sessionMeta struct {
	ID  string
	CWD string
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

// sessionMetaFilenames lists the workspace metadata filenames used by
// different Copilot CLI versions, tried in order.
var sessionMetaFilenames = []string{"workspace.yaml", "workspace.yml", "session.yaml", "session.json"}

// sessionMetaCWDKeys lists the field names Copilot CLI has used across
// versions to record the session's working directory.
var sessionMetaCWDKeys = []string{"cwd", "workingDirectory", "working_dir", "directory"}

// sessionMetaIDKeys lists the field names Copilot CLI has used across
// versions to record the session id.
var sessionMetaIDKeys = []string{"id", "sessionId", "session_id"}

// parseSessionMeta reads workspace metadata from a Copilot session directory.
// Copilot CLI has used different metadata filenames and field names across
// versions, so this tries several known variants before giving up. The
// session ID always falls back to the containing directory name, since
// Copilot has consistently named session directories after the session ID.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	for _, name := range sessionMetaFilenames {
		path := filepath.Join(sessionDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		raw := map[string]interface{}{}
		var parseErr error
		if filepath.Ext(name) == ".json" {
			parseErr = json.Unmarshal(data, &raw)
		} else {
			parseErr = yaml.Unmarshal(data, &raw)
		}
		if parseErr != nil {
			continue
		}

		cwd := stringFromMap(raw, sessionMetaCWDKeys...)
		if cwd == "" {
			if ws, ok := raw["workspace"].(map[string]interface{}); ok {
				cwd = stringFromMap(ws, sessionMetaCWDKeys...)
			}
		}
		if cwd == "" {
			continue
		}

		id := stringFromMap(raw, sessionMetaIDKeys...)
		if id == "" {
			id = filepath.Base(sessionDir)
		}

		return &sessionMeta{ID: id, CWD: cwd}, nil
	}

	return nil, fmt.Errorf("no session metadata found in %s", sessionDir)
}

// stringFromMap returns the first non-empty string value found under any of
// the given keys.
func stringFromMap(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// transcriptFilenames lists the event/transcript log filenames used by
// different Copilot CLI versions, tried in order.
var transcriptFilenames = []string{"events.jsonl", "history.jsonl", "transcript.jsonl"}

// GetTranscriptPath returns the path to the transcript file within a session directory.
func GetTranscriptPath(sessionDir string) string {
	for _, name := range transcriptFilenames {
		p := filepath.Join(sessionDir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(sessionDir, transcriptFilenames[0])
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
	meta := struct {
		ID string `yaml:"id"`
	}{ID: sessionID}
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
