package copilot

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/re-cinq/shift-log/internal/agent"
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

// cachedSessionMeta records the most recently observed Copilot session for a
// project, captured while the CLI process is still alive and invoking our
// postToolUse hook. Recent Copilot CLI versions remove their own
// session-state directory once a non-interactive ("-p") invocation exits, so
// scanning ~/.copilot/session-state after the process has already exited
// (the "manual"/post-commit discovery path) can no longer find it. Caching a
// copy of the transcript inside the repository's own .shiftlog directory as
// soon as we observe it lets discovery succeed even after Copilot has
// cleaned up its state.
type cachedSessionMeta struct {
	SessionID string `json:"session_id"`
	UpdatedAt string `json:"updated_at"`
}

func cacheDir(projectPath string) string {
	return filepath.Join(projectPath, ".shiftlog", "cache", "copilot")
}

func cachedTranscriptPath(projectPath string) string {
	return filepath.Join(cacheDir(projectPath), "transcript.jsonl")
}

func cachedMetaPath(projectPath string) string {
	return filepath.Join(cacheDir(projectPath), "meta.json")
}

// CacheSessionTranscript copies the current contents of a live session's
// transcript into shiftlog's own cache directory so the session can still be
// recovered by DiscoverSession after Copilot CLI exits and removes its
// session-state directory. Failures are silently ignored since this is a
// best-effort layer on top of the primary directory-scan discovery.
func CacheSessionTranscript(projectPath, sessionID, transcriptPath string) {
	if projectPath == "" || sessionID == "" || transcriptPath == "" {
		return
	}

	data, err := os.ReadFile(transcriptPath)
	if err != nil {
		return
	}

	dir := cacheDir(projectPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}

	meta := cachedSessionMeta{SessionID: sessionID, UpdatedAt: time.Now().Format(time.RFC3339)}
	metaData, err := json.Marshal(&meta)
	if err != nil {
		return
	}

	_ = os.WriteFile(cachedMetaPath(projectPath), metaData, 0600)
	_ = os.WriteFile(cachedTranscriptPath(projectPath), data, 0600)
}

// ReadCachedSession returns the most recently cached session for a project,
// or nil if there is no recent, valid cache entry.
func ReadCachedSession(projectPath string) *agent.SessionInfo {
	metaData, err := os.ReadFile(cachedMetaPath(projectPath))
	if err != nil {
		return nil
	}

	var meta cachedSessionMeta
	if err := json.Unmarshal(metaData, &meta); err != nil || meta.SessionID == "" {
		return nil
	}

	updatedAt, err := time.Parse(time.RFC3339, meta.UpdatedAt)
	if err != nil || time.Since(updatedAt) > agent.RecentSessionTimeout {
		return nil
	}

	transcriptPath := cachedTranscriptPath(projectPath)
	if _, err := os.Stat(transcriptPath); err != nil {
		return nil
	}

	return &agent.SessionInfo{
		SessionID:      meta.SessionID,
		TranscriptPath: transcriptPath,
		StartedAt:      meta.UpdatedAt,
		ProjectPath:    projectPath,
	}
}
