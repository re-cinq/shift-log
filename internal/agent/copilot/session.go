package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// GetSessionStateDir returns the primary session state directory. This is the
// canonical directory used when writing new session fixtures.
func GetSessionStateDir() (string, error) {
	copilotDir, err := GetCopilotDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(copilotDir, "session-state"), nil
}

// candidateSessionStateDirs returns every known session-state directory name
// Copilot CLI has used across versions, most canonical first. Discovery scans
// all of them since the CLI's on-disk storage layout has moved around from
// release to release.
func candidateSessionStateDirs() ([]string, error) {
	copilotDir, err := GetCopilotDir()
	if err != nil {
		return nil, err
	}

	names := []string{"session-state", "sessions", "session_state", "history-session-state", "history"}
	dirs := make([]string, 0, len(names))
	for _, name := range names {
		dirs = append(dirs, filepath.Join(copilotDir, name))
	}
	return dirs, nil
}

// metaFilenames are the known metadata filenames Copilot CLI has used to
// describe a session's workspace, in preference order.
var metaFilenames = []string{"workspace.yaml", "workspace.yml", "workspace.json", "session.json", "metadata.json", "state.json"}

// parseSessionMeta reads a workspace metadata file from a Copilot session directory.
// It tries several known filenames/formats and, if the canonical fields are
// absent, falls back to scanning the raw document for a handful of plausible
// field names so a renamed/relocated field doesn't blind discovery entirely.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	var lastErr error

	for _, name := range metaFilenames {
		path := filepath.Join(sessionDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue
		}

		isYAML := strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")

		var meta sessionMeta
		var raw map[string]interface{}

		if isYAML {
			if err := yaml.Unmarshal(data, &meta); err != nil {
				lastErr = err
				continue
			}
			_ = yaml.Unmarshal(data, &raw)
		} else {
			if err := json.Unmarshal(data, &meta); err != nil {
				lastErr = err
				continue
			}
			_ = json.Unmarshal(data, &raw)
		}

		if meta.CWD == "" {
			meta.CWD = extractCWD(raw)
		}
		if meta.ID == "" {
			meta.ID = extractString(raw, "id", "sessionId", "session_id", "sessionID")
		}

		return &meta, nil
	}

	return nil, lastErr
}

// extractString returns the first non-empty string value found for the given keys.
func extractString(raw map[string]interface{}, keys ...string) string {
	if raw == nil {
		return ""
	}
	for _, k := range keys {
		if v, ok := raw[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// extractCWD looks for a working-directory field under a handful of plausible
// key names, including nested under a "workspace" object.
func extractCWD(raw map[string]interface{}) string {
	if raw == nil {
		return ""
	}
	if v := extractString(raw, "cwd", "directory", "workingDirectory", "working_directory", "dir"); v != "" {
		return v
	}
	if ws, ok := raw["workspace"].(map[string]interface{}); ok {
		return extractString(ws, "cwd", "directory", "workingDirectory", "working_directory", "dir")
	}
	return ""
}

// GetTranscriptPath returns the path to the events.jsonl transcript within a session directory.
func GetTranscriptPath(sessionDir string) string {
	return filepath.Join(sessionDir, "events.jsonl")
}

// resolveTranscriptPath returns the transcript path for a session directory,
// preferring the canonical events.jsonl but falling back to any other
// *.jsonl file present if that name isn't used.
func resolveTranscriptPath(sessionDir string) string {
	primary := GetTranscriptPath(sessionDir)
	if _, err := os.Stat(primary); err == nil {
		return primary
	}

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return primary
	}

	var best string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestMod) {
			best = filepath.Join(sessionDir, e.Name())
			bestMod = info.ModTime()
		}
	}

	if best != "" {
		return best
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
