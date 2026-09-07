```go
package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/re-cinq/shift-log/internal/agent"
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
func GetSessionStateDir() (string, error) {
	copilotDir, err := GetCopilotDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(copilotDir, "session-state"), nil
}

// parseSessionMeta reads a workspace.yaml from a Copilot session directory.
func parseSessionMeta(sessionDir string) (*sessionMeta, error) {
	path := filepath.Join(sessionDir, "workspace.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var meta sessionMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return nil, err
	}

	return &meta, nil
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

// activeSessionRecord is a lightweight pointer to the most recently observed
// Copilot session, persisted by ParseHookInput on every hook invocation
// (not just git-commit ones). Copilot's own ~/.copilot/session-state entry
// for a session may no longer be discoverable by the time the post-commit
// git hook runs (e.g. once the CLI process has exited), so this durable,
// shiftlog-owned pointer lets manual-commit discovery still find the right
// session.
type activeSessionRecord struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	ProjectPath    string `json:"project_path"`
	UpdatedAt      string `json:"updated_at"`
}

// activeSessionPath returns the path to the active-session pointer file for a project.
func activeSessionPath(projectPath string) string {
	return filepath.Join(projectPath, ".shiftlog", "active-session.json")
}

// writeActiveSession persists a pointer to the currently active session.
// Best-effort: this is a fallback mechanism, so failures are non-fatal and
// silently ignored by callers. No-ops if projectPath or sessionID is empty.
func writeActiveSession(projectPath, sessionID, transcriptPath string) error {
	if projectPath == "" || sessionID == "" {
		return nil
	}

	path := activeSessionPath(projectPath)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	rec := activeSessionRecord{
		SessionID:      sessionID,
		TranscriptPath: transcriptPath,
		ProjectPath:    projectPath,
		UpdatedAt:      time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// readActiveSession reads the active-session pointer written by writeActiveSession.
// Returns nil if the file is missing, stale, or belongs to a different project.
func readActiveSession(projectPath string) *agent.SessionInfo {
	path := activeSessionPath(projectPath)
	info, err := os.Stat(path)
	if err != nil || time.Since(info.ModTime()) > agent.RecentSessionTimeout {
		return nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var rec activeSessionRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.SessionID == "" {
		return nil
	}

	if !agent.PathsEqual(rec.ProjectPath, projectPath) {
		return nil
	}

	return &agent.SessionInfo{
		SessionID:      rec.SessionID,
		TranscriptPath: rec.TranscriptPath,
		StartedAt:      rec.UpdatedAt,
		ProjectPath:    rec.ProjectPath,
	}
}
```
