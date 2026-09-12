```go
package copilot

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// sessionMeta represents lightweight metadata from a Copilot session workspace file.
type sessionMeta struct {
	ID      string `yaml:"id"`
	CWD     string `yaml:"cwd"`
	GitRoot string `yaml:"git_root,omitempty"`
}

// idKeys and cwdKeys list the workspace metadata field names Copilot CLI has used
// across versions for the session ID and working directory. Newer releases have
// been observed renaming these fields, so we probe known aliases rather than
// relying on a single hardcoded key.
var (
	idKeys  = []string{"id", "session_id", "sessionId", "sessionID"}
	cwdKeys = []string{"cwd", "directory", "workingDirectory", "working_dir", "dir"}
)

// transcriptFileNames lists known Copilot transcript filenames, most recent first.
var transcriptFileNames = []string{"events.jsonl", "session.jsonl", "transcript.jsonl", "history.jsonl"}

// metaFileNames lists known Copilot session metadata filenames.
var metaFileNames = []string{"workspace.yaml", "session.yaml", "metadata.yaml", "workspace.json"}

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

// findMetaFile locates the session metadata file within a session directory,
// trying each known filename. Returns "" if none exist.
func findMetaFile(sessionDir string) string {
	for _, name := range metaFileNames {
		path := filepath.Join(sessionDir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// firstStringValue returns the first non-empty string value found in m for any of the given keys.
func firstStringValue(m map[string]interface{}, keys []string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// parseSessionMeta reads the session metadata file from a Copilot session directory.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	path := findMetaFile(sessionDir)
	if path == "" {
		return nil, os.ErrNotExist
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var meta sessionMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, err
	}

	// Fall back to probing known field aliases for versions that renamed keys.
	if meta.ID == "" || meta.CWD == "" {
		var raw map[string]interface{}
		if err := yaml.Unmarshal(data, &raw); err == nil {
			if meta.ID == "" {
				meta.ID = firstStringValue(raw, idKeys)
			}
			if meta.CWD == "" {
				meta.CWD = firstStringValue(raw, cwdKeys)
			}
		}
	}

	return &meta, nil
}

// GetTranscriptPath returns the path to the transcript file within a session directory,
// trying known filenames and falling back to the current "events.jsonl" convention.
func GetTranscriptPath(sessionDir string) string {
	for _, name := range transcriptFileNames {
		path := filepath.Join(sessionDir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
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
