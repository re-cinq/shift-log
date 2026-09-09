package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// sessionMeta represents lightweight metadata from a Copilot session workspace file.
type sessionMeta struct {
	ID      string `yaml:"id" json:"id"`
	CWD     string `yaml:"cwd" json:"cwd"`
	GitRoot string `yaml:"git_root,omitempty" json:"git_root,omitempty"`
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

// cwdAliasKeys and idAliasKeys are alternate key names tried when the
// primary "cwd"/"id" fields come back empty, in case a newer Copilot CLI
// release renamed them.
var cwdAliasKeys = []string{"cwd", "cwdPath", "cwd_path", "directory", "workingDirectory", "working_directory", "workspace", "workspacePath", "workspace_path", "path"}
var idAliasKeys = []string{"id", "sessionId", "session_id", "sessionID"}

// parseSessionMeta reads workspace metadata from a Copilot session directory.
// Tries workspace.yaml (the originally-validated filename) first, then
// workspace.yml/workspace.json, and falls back to alternate key names if the
// expected id/cwd fields come back empty after decoding.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	var lastErr error
	for _, name := range []string{"workspace.yaml", "workspace.yml", "workspace.json"} {
		path := filepath.Join(sessionDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue
		}

		var meta sessionMeta
		var raw map[string]interface{}
		if strings.HasSuffix(name, ".json") {
			if err := json.Unmarshal(data, &meta); err != nil {
				return nil, err
			}
			_ = json.Unmarshal(data, &raw)
		} else {
			if err := yaml.Unmarshal(data, &meta); err != nil {
				return nil, err
			}
			_ = yaml.Unmarshal(data, &raw)
		}

		if meta.CWD == "" {
			meta.CWD = firstStringValue(raw, cwdAliasKeys)
		}
		if meta.ID == "" {
			meta.ID = firstStringValue(raw, idAliasKeys)
		}

		return &meta, nil
	}
	return nil, lastErr
}

// firstStringValue returns the first non-empty string value found in m for
// any of the given keys.
func firstStringValue(m map[string]interface{}, keys []string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
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

// findTranscriptFile locates the transcript file within a session directory.
// Prefers events.jsonl (the originally-validated filename); if that's not
// present, falls back to the most recently modified *.jsonl file, in case a
// newer Copilot CLI release renamed it.
func findTranscriptFile(sessionDir string) string {
	preferred := GetTranscriptPath(sessionDir)
	if _, err := os.Stat(preferred); err == nil {
		return preferred
	}

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return preferred
	}

	type candidate struct {
		path    string
		modTime int64
	}
	var candidates []candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{
			path:    filepath.Join(sessionDir, e.Name()),
			modTime: info.ModTime().UnixNano(),
		})
	}
	if len(candidates) == 0 {
		return preferred
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modTime > candidates[j].modTime })
	return candidates[0].path
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
