```go
package copilot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// sessionMeta represents lightweight metadata from a Copilot session's
// metadata file.
type sessionMeta struct {
	ID  string
	CWD string
}

// candidateMetaFilenames lists the metadata filenames Copilot CLI has used
// across versions, in priority order. Recent releases have moved between
// YAML and JSON and renamed the file itself, so we try several.
var candidateMetaFilenames = []string{
	"workspace.yaml",
	"workspace.yml",
	"workspace.json",
	"session.yaml",
	"session.yml",
	"session.json",
	"state.yaml",
	"state.json",
	"meta.yaml",
	"meta.json",
}

// cwdKeys are the metadata keys (case-insensitive) that have been observed
// to hold the session's working directory across Copilot CLI versions.
var cwdKeys = []string{
	"cwd", "cwdpath", "cwd_path", "workingdirectory", "working_directory",
	"workdir", "directory", "root", "workspaceroot", "workspace_root", "path",
}

// idKeys are the metadata keys (case-insensitive) that have been observed
// to hold the session ID across Copilot CLI versions.
var idKeys = []string{
	"id", "sessionid", "session_id", "uuid", "sessionuuid", "session_uuid",
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

// parseSessionMeta reads a Copilot session's metadata file, trying several
// known filenames and field names since these have changed across releases.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	var lastErr error

	for _, name := range candidateMetaFilenames {
		data, err := os.ReadFile(filepath.Join(sessionDir, name))
		if err != nil {
			continue
		}

		var raw map[string]interface{}
		if err := yaml.Unmarshal(data, &raw); err != nil {
			lastErr = err
			continue
		}

		return &sessionMeta{
			ID:  findMetaValue(raw, idKeys),
			CWD: findMetaValue(raw, cwdKeys),
		}, nil
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, os.ErrNotExist
}

// findMetaValue searches a parsed metadata document for the first string
// value whose key case-insensitively matches one of the given candidate
// keys, looking one level deep to handle nested structures such as
// {"workspace": {"cwd": "..."}}.
func findMetaValue(raw map[string]interface{}, keys []string) string {
	if raw == nil {
		return ""
	}

	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[strings.ToLower(k)] = true
	}

	if v := searchMetaMap(raw, want); v != "" {
		return v
	}

	for _, v := range raw {
		if nested, ok := v.(map[string]interface{}); ok {
			if s := searchMetaMap(nested, want); s != "" {
				return s
			}
		}
	}

	return ""
}

func searchMetaMap(raw map[string]interface{}, want map[string]bool) string {
	for k, v := range raw {
		if !want[strings.ToLower(k)] {
			continue
		}
		if s, ok := v.(string); ok && s != "" {
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
	yamlData, err := yaml.Marshal(map[string]string{"id": sessionID})
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
