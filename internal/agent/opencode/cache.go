package opencode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/re-cinq/shift-log/internal/agent"
)

// cachedSession is a lightweight snapshot of a session's transcript,
// captured by shiftlog itself while an OpenCode session is still live (i.e.
// during a plugin tool-execution hook). Newer OpenCode releases may not
// leave a discoverable on-disk session behind for a non-interactive "run"
// once the CLI process exits, which would otherwise leave nothing for a
// later manual (post-commit hook) discovery to find. This cache is the
// fallback for that case.
type cachedSession struct {
	SessionID      string    `json:"session_id"`
	ProjectPath    string    `json:"project_path"`
	TranscriptData string    `json:"transcript_data"`
	CachedAt       time.Time `json:"cached_at"`
}

// sessionCachePath returns the path to shiftlog's own session cache file.
func sessionCachePath() (string, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "shiftlog-session-cache.json"), nil
}

func loadSessionCache() []cachedSession {
	path, err := sessionCachePath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var entries []cachedSession
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	return entries
}

func saveSessionCache(entries []cachedSession) {
	path, err := sessionCachePath()
	if err != nil {
		return
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}

// cacheSessionTranscript stashes a copy of an in-progress session's
// transcript for projectPath, captured while OpenCode's plugin hook fires
// (i.e. while the session is still live), so a later manual lookup can
// recover it even if OpenCode has since removed its own on-disk session.
// Best-effort: failures are silently ignored.
func cacheSessionTranscript(projectPath, sessionID string, transcriptData []byte) {
	if projectPath == "" || sessionID == "" || len(transcriptData) == 0 {
		return
	}

	now := time.Now()
	entries := loadSessionCache()
	fresh := entries[:0]
	for _, e := range entries {
		if now.Sub(e.CachedAt) > agent.RecentSessionTimeout {
			continue // prune stale entries
		}
		if agent.PathsEqual(e.ProjectPath, projectPath) {
			continue // superseded by the entry we're about to add
		}
		fresh = append(fresh, e)
	}
	fresh = append(fresh, cachedSession{
		SessionID:      sessionID,
		ProjectPath:    projectPath,
		TranscriptData: string(transcriptData),
		CachedAt:       now,
	})
	saveSessionCache(fresh)
}

// cachedSessionFor returns a recently cached session for projectPath, if any.
func cachedSessionFor(projectPath string) *agent.SessionInfo {
	for _, e := range loadSessionCache() {
		if time.Since(e.CachedAt) > agent.RecentSessionTimeout {
			continue
		}
		if !agent.PathsEqual(e.ProjectPath, projectPath) {
			continue
		}
		return &agent.SessionInfo{
			SessionID:      e.SessionID,
			StartedAt:      e.CachedAt.Format(time.RFC3339),
			ProjectPath:    projectPath,
			TranscriptData: []byte(e.TranscriptData),
		}
	}
	return nil
}
