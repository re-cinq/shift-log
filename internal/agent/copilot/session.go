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

// workspaceMetaFilenames are candidate filenames for Copilot's session metadata
// file, tried in order. The CLI's own naming/serialization format has changed
// across versions (e.g. workspace.yaml -> workspace.json), so we probe for
// whichever one actually exists rather than assuming a single fixed name.
var workspaceMetaFilenames = []string{
	"workspace.yaml",
	"workspace.yml",
	"workspace.json",
	"session.yaml",
	"session.json",
	"metadata.json",
}

// transcriptFilenames are candidate filenames for a Copilot session's
// transcript, tried in order when discovering an existing session. Newer CLI
// versions may rename events.jsonl.
var transcriptFilenames = []string{
	"events.jsonl",
	"transcript.jsonl",
	"history.jsonl",
	"session.jsonl",
}

// firstStringField returns the first non-empty string value found in raw for
// any of the given candidate keys. Used to tolerate field renames across
// Copilot CLI versions.
func firstStringField(raw map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := raw[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
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

// parseSessionMeta reads session metadata from a Copilot session directory.
// It probes several known filenames and field names since the CLI's on-disk
// format has changed across versions.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	var lastErr error = os.ErrNotExist

	for _, name := range workspaceMetaFilenames {
		data, err := os.ReadFile(filepath.Join(sessionDir, name))
		if err != nil {
			lastErr = err
			continue
		}

		var raw map[string]interface{}
		if err := yaml.Unmarshal(data, &raw); err != nil {
			lastErr = err
			continue
		}

		return &sessionMeta{
			ID: firstStringField(raw, "id", "session_id", "sessionId", "ID"),
			CWD: firstStringField(raw,
				"cwd", "working_directory", "workingDirectory",
				"directory", "path", "project_path", "projectPath", "root"),
			GitRoot: firstStringField(raw, "git_root", "gitRoot"),
		}, nil
	}

	return nil, lastErr
}

// GetTranscriptPath returns the path to the transcript file within a session
// directory. It prefers whichever known transcript filename already exists on
// disk, falling back to the default (events.jsonl) when none is found yet
// (e.g. when computing the path to write a new transcript).
func GetTranscriptPath(sessionDir string) string {
	for _, name := range transcriptFilenames {
		path := filepath.Join(sessionDir, name)
		if _, err := os.Stat(path); err == nil {
			return path
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
