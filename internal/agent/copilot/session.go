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

// sessionMeta represents lightweight metadata from a Copilot session directory.
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

// metaFileCandidates are the known/possible filenames Copilot CLI has used
// across releases to store per-session metadata within a session directory.
var metaFileCandidates = []string{
	"workspace.yaml", "workspace.yml",
	"session.yaml", "session.yml", "session.json",
	"metadata.yaml", "metadata.yml", "metadata.json",
	"state.yaml", "state.yml", "state.json",
}

// idFieldCandidates and cwdFieldCandidates are the known/possible field names
// used to store the session ID and working directory. Copilot CLI has
// changed these across releases, so several spellings are tried, including
// nested under a "workspace"/"session"/"meta" object.
var idFieldCandidates = []string{"id", "session_id", "sessionId", "sessionID", "uuid"}
var cwdFieldCandidates = []string{
	"cwd", "directory", "dir", "workdir", "working_dir", "workingDirectory",
	"root", "path", "project_dir", "projectDir", "git_root", "gitRoot",
}

// parseSessionMeta reads session metadata from a Copilot session directory,
// trying every known metadata filename and field name since the on-disk
// format has changed across Copilot CLI releases.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	for _, name := range metaFileCandidates {
		path := filepath.Join(sessionDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		raw := map[string]interface{}{}
		if strings.HasSuffix(name, ".json") {
			if err := json.Unmarshal(data, &raw); err != nil {
				continue
			}
		} else {
			if err := yaml.Unmarshal(data, &raw); err != nil {
				continue
			}
		}

		meta := &sessionMeta{
			ID:  extractStringField(raw, idFieldCandidates),
			CWD: extractStringField(raw, cwdFieldCandidates),
		}
		if meta.ID == "" {
			meta.ID = filepath.Base(sessionDir)
		}
		return meta, nil
	}
	return nil, os.ErrNotExist
}

// extractStringField looks up the first matching non-empty string field
// among candidates, including inside common nested containers.
func extractStringField(raw map[string]interface{}, candidates []string) string {
	for _, key := range candidates {
		if v, ok := raw[key]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	for _, nestKey := range []string{"workspace", "session", "meta"} {
		nested, ok := raw[nestKey].(map[string]interface{})
		if !ok {
			continue
		}
		if s := extractStringField(nested, candidates); s != "" {
			return s
		}
	}
	return ""
}

// transcriptFileCandidates are known/possible filenames for the event
// transcript within a Copilot session directory.
var transcriptFileCandidates = []string{
	"events.jsonl", "session.jsonl", "transcript.jsonl", "log.jsonl", "messages.jsonl",
}

// GetTranscriptPath returns the path to the event transcript within a
// session directory. It tries known filenames first, then falls back to
// the most recently modified .jsonl file in the directory, since Copilot
// CLI has renamed this file across releases.
func GetTranscriptPath(sessionDir string) string {
	for _, name := range transcriptFileCandidates {
		path := filepath.Join(sessionDir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return filepath.Join(sessionDir, "events.jsonl")
	}

	var bestPath string
	var bestModTime time.Time
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if bestPath == "" || info.ModTime().After(bestModTime) {
			bestPath = filepath.Join(sessionDir, entry.Name())
			bestModTime = info.ModTime()
		}
	}
	if bestPath != "" {
		return bestPath
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
	eventsPath := GetTranscriptPath(sessionDir)
	return eventsPath, os.WriteFile(eventsPath, data, 0600)
}
