package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DanielJonesEB/claudit/internal/claude"
	"github.com/DanielJonesEB/claudit/internal/util"
)

// ActiveSession represents the currently active Claude session
type ActiveSession struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	StartedAt      string `json:"started_at"`
	ProjectPath    string `json:"project_path"`
}

const (
	activeSessionFile     = "active-session.json"
	staleSessionTimeout   = 10 * time.Minute
	recentSessionTimeout  = 5 * time.Minute
)

// WriteActiveSession writes the active session state to .claudit/active-session.json
func WriteActiveSession(session *ActiveSession) error {
	sessionPath, err := getActiveSessionPath()
	if err != nil {
		return err
	}

	// Ensure .claudit directory exists
	dir := filepath.Dir(sessionPath)
	if err := util.EnsureDir(dir); err != nil {
		return fmt.Errorf("failed to create .claudit directory: %w", err)
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal session: %w", err)
	}

	return os.WriteFile(sessionPath, data, 0644)
}

// ReadActiveSession reads the active session state from .claudit/active-session.json
// Returns nil if no active session file exists
func ReadActiveSession() (*ActiveSession, error) {
	sessionPath, err := getActiveSessionPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(sessionPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read session file: %w", err)
	}

	var session ActiveSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse session file: %w", err)
	}

	return &session, nil
}

// ClearActiveSession removes the active session state file
func ClearActiveSession() error {
	sessionPath, err := getActiveSessionPath()
	if err != nil {
		return err
	}

	err = os.Remove(sessionPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove session file: %w", err)
	}

	return nil
}

// IsSessionActive checks if the session is still active by validating transcript mtime
// A session is considered inactive if the transcript hasn't been modified in 10+ minutes
func IsSessionActive(session *ActiveSession) bool {
	if session == nil || session.TranscriptPath == "" {
		return false
	}

	info, err := os.Stat(session.TranscriptPath)
	if err != nil {
		return false
	}

	// Check if transcript was modified within the stale timeout
	return time.Since(info.ModTime()) < staleSessionTimeout
}

// DiscoverSession attempts to find a relevant session for the current project
// It first checks the active session file, then falls back to sessions-index.json
// Returns nil if no relevant session is found
func DiscoverSession(projectPath string) (*ActiveSession, error) {
	// First, check active session file
	active, err := ReadActiveSession()
	if err != nil {
		// Log but continue to fallback
		return nil, nil
	}

	if active != nil {
		// Validate project path matches
		if active.ProjectPath == projectPath && IsSessionActive(active) {
			return active, nil
		}
	}

	// Fall back to sessions-index.json lookup
	return discoverRecentSession(projectPath)
}

// discoverRecentSession looks for a recent session in Claude's sessions-index.json
// or by scanning the session directory for recent .jsonl files
func discoverRecentSession(projectPath string) (*ActiveSession, error) {
	// First try sessions-index.json
	index, err := claude.ReadSessionsIndex(projectPath)
	if err == nil && len(index.Entries) > 0 {
		session := findRecentSessionFromIndex(index, projectPath)
		if session != nil {
			return session, nil
		}
	}

	// Fallback: scan for recent .jsonl files directly
	// This handles the case where Claude Code doesn't create sessions-index.json
	return scanForRecentSession(projectPath)
}

// findRecentSessionFromIndex finds a recent session from the sessions-index
func findRecentSessionFromIndex(index *claude.SessionsIndex, projectPath string) *ActiveSession {
	now := time.Now()

	var bestEntry *claude.SessionEntry
	var bestModified time.Time

	for i := range index.Entries {
		entry := &index.Entries[i]

		// Validate project path matches
		if entry.ProjectPath != projectPath {
			continue
		}

		// Parse the modified timestamp
		modified, err := time.Parse(time.RFC3339Nano, entry.Modified)
		if err != nil {
			// Try RFC3339 without nano
			modified, err = time.Parse(time.RFC3339, entry.Modified)
			if err != nil {
				continue
			}
		}

		// Check if within the recent timeout
		if now.Sub(modified) > recentSessionTimeout {
			continue
		}

		// Keep track of most recent
		if bestEntry == nil || modified.After(bestModified) {
			bestEntry = entry
			bestModified = modified
		}
	}

	if bestEntry == nil {
		return nil
	}

	return &ActiveSession{
		SessionID:      bestEntry.SessionID,
		TranscriptPath: bestEntry.FullPath,
		StartedAt:      bestEntry.Created,
		ProjectPath:    bestEntry.ProjectPath,
	}
}

// scanForRecentSession scans Claude's session directory for recently modified .jsonl files
func scanForRecentSession(projectPath string) (*ActiveSession, error) {
	sessionDir, err := claude.GetSessionDir(projectPath)
	if err != nil {
		return nil, nil
	}

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, nil
	}

	now := time.Now()
	var bestPath string
	var bestSessionID string
	var bestModTime time.Time

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		modTime := info.ModTime()

		// Check if within the recent timeout
		if now.Sub(modTime) > recentSessionTimeout {
			continue
		}

		// Keep track of most recent
		if bestPath == "" || modTime.After(bestModTime) {
			bestPath = filepath.Join(sessionDir, name)
			bestSessionID = strings.TrimSuffix(name, ".jsonl")
			bestModTime = modTime
		}
	}

	if bestPath == "" {
		return nil, nil
	}

	return &ActiveSession{
		SessionID:      bestSessionID,
		TranscriptPath: bestPath,
		StartedAt:      bestModTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
	}, nil
}

// getActiveSessionPath returns the path to .claudit/active-session.json
func getActiveSessionPath() (string, error) {
	root, err := util.GetProjectRoot()
	if err != nil {
		return "", fmt.Errorf("failed to get project root: %w", err)
	}
	return filepath.Join(root, ".claudit", activeSessionFile), nil
}
