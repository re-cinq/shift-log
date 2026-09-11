```go
package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// sessionMeta represents lightweight metadata extracted from a Copilot
// session's metadata file, regardless of which filename/encoding Copilot
// CLI happens to use for it in a given release.
type sessionMeta struct {
	ID      string
	CWD     string
	GitRoot string
}

// metaFileNames lists the known/likely names Copilot CLI has used across
// versions for its per-session metadata file. The CLI is still under rapid
// 1.0.x iteration and has renamed this file before, so several candidates
// are probed instead of assuming a single fixed name.
var metaFileNames = []string{
	"workspace.yaml",
	"workspace.yml",
	"workspace.json",
	"session.yaml",
	"session.json",
	"metadata.yaml",
	"metadata.json",
	"state.yaml",
	"state.json",
}

// idKeys and cwdKeys enumerate the field names Copilot CLI has used (or is
// likely to use) for the session identifier and working directory.
var idKeys = []string{"id", "sessionId", "session_id", "sessionID"}
var cwdKeys = []string{
	"cwd", "cwd_path", "cwdPath",
	"workingDirectory", "working_directory",
	"dir", "directory",
	"project_dir", "projectDir",
	"workspace", "workspacePath", "workspace_path",
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

// parseSessionMeta reads a per-session metadata file from a Copilot session
// directory, trying several known filenames and both YAML and JSON
// encodings, and extracting the id/cwd fields under any of their known
// aliases. Returns (nil, nil) if no metadata file could be found or parsed;
// callers should treat that as "unknown metadata" rather than an error and
// fall back to directory-name-derived identification.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	for _, name := range metaFileNames {
		data, err := os.ReadFile(filepath.Join(sessionDir, name))
		if err != nil {
			continue
		}

		raw := map[string]interface{}{}
		if jsonErr := json.Unmarshal(data, &raw); jsonErr != nil {
			if yamlErr := yaml.Unmarshal(data, &raw); yamlErr != nil {
				continue
			}
		}

		meta := &sessionMeta{}
		for _, k := range idKeys {
			if v, ok := raw[k].(string); ok && v != "" {
				meta.ID = v
				break
			}
		}
		for _, k := range cwdKeys {
			if v, ok := raw[k].(string); ok && v != "" {
				meta.CWD = v
				break
			}
		}
		if v, ok := raw["git_root"].(string); ok {
			meta.GitRoot = v
		} else if v, ok := raw["gitRoot"].(string); ok {
			meta.GitRoot = v
		}

		return meta, nil
	}

	return nil, nil
}

// GetTranscriptPath returns the path to the transcript file within a session
// directory. It probes several known/likely filenames Copilot CLI has used
// for its event log, falling back to the historical "events.jsonl" name if
// none are found on disk (e.g. for a directory that doesn't exist yet).
func GetTranscriptPath(sessionDir string) string {
	candidates := []string{"events.jsonl", "transcript.jsonl", "session.jsonl", "log.jsonl"}
	for _, name := range candidates {
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
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	return eventsPath, os.WriteFile(eventsPath, data, 0600)
}
```
