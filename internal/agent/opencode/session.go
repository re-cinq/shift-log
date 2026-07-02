package opencode

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/re-cinq/shift-log/internal/agent"
)

// GetDataDir returns the OpenCode data directory.
// OpenCode follows XDG conventions: it uses $XDG_DATA_HOME/opencode on Linux
// and ~/Library/Application Support/opencode on macOS.
func GetDataDir() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not determine home directory: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "opencode"), nil
	}

	// Linux/other: respect XDG_DATA_HOME, default to ~/.local/share
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode"), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "opencode"), nil
}

// GetProjectID returns the project identifier for OpenCode.
// For git repos, this is the root commit hash. For non-git dirs, it's "global".
func GetProjectID(projectPath string) string {
	cmd := exec.Command("git", "rev-list", "--max-parents=0", "--all")
	cmd.Dir = projectPath
	output, err := cmd.Output()
	if err != nil {
		return "global"
	}

	// Take the first line (first root commit)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) > 0 && lines[0] != "" {
		return strings.TrimSpace(lines[0])
	}
	return "global"
}

// GetSessionDir returns the session storage directory for a project.
func GetSessionDir(projectPath string) (string, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return "", err
	}

	projectID := GetProjectID(projectPath)
	return filepath.Join(dataDir, "storage", "session", projectID), nil
}

// GetMessageDir returns the message storage directory for a session.
func GetMessageDir(sessionID string) (string, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dataDir, "storage", "message", sessionID), nil
}

// sessionInfo represents an OpenCode session JSON file.
type sessionInfo struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectID,omitempty"`
	Directory string `json:"directory,omitempty"`
	Title     string `json:"title,omitempty"`
}

// sessionRecord captures the fields we might find in an OpenCode session
// index file when reading one back. OpenCode has changed its on-disk
// directory layout across releases (e.g. flat vs. per-project nesting), but
// session files consistently embed the project's working directory, so
// FindRecentSessionByDirectory matches against that instead of depending on
// how the parent directories happen to be named.
type sessionRecord struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectID"`
	Directory string `json:"directory"`
	Path      string `json:"path"`
	Cwd       string `json:"cwd"`
}

func (s sessionRecord) matchesDirectory(projectPath string) bool {
	return (s.Directory != "" && s.Directory == projectPath) ||
		(s.Path != "" && s.Path == projectPath) ||
		(s.Cwd != "" && s.Cwd == projectPath)
}

// FindRecentSessionByDirectory recursively searches an OpenCode data
// directory for the most recently modified session file belonging to
// projectPath. It matches by the session's embedded working-directory (or
// projectID) field rather than assuming a specific folder layout, so it
// keeps working even when OpenCode changes how it nests session files on
// disk. Returns ("", zero time, false) if nothing recent is found.
func FindRecentSessionByDirectory(dataDir, projectPath, projectID string) (string, time.Time, bool) {
	roots := []string{
		filepath.Join(dataDir, "storage", "session"),
		filepath.Join(dataDir, "storage"),
		filepath.Join(dataDir, "project"),
	}

	now := time.Now()
	var bestID string
	var bestModTime time.Time
	var bestExact bool
	seen := map[string]bool{}

	for _, root := range roots {
		if seen[root] {
			continue
		}
		seen[root] = true

		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // skip unreadable entries
			}
			if d.IsDir() {
				// Message payloads live in their own subtree and can be
				// numerous; skip them for both correctness and speed.
				if d.Name() == "message" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".json") {
				return nil
			}

			info, err := d.Info()
			if err != nil || now.Sub(info.ModTime()) > agent.RecentSessionTimeout {
				return nil
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return nil
			}

			var rec sessionRecord
			if err := json.Unmarshal(data, &rec); err != nil {
				return nil
			}

			exact := rec.matchesDirectory(projectPath)
			idMatch := rec.ProjectID != "" && rec.ProjectID == projectID
			if !exact && !idMatch {
				return nil
			}

			better := bestID == "" ||
				(exact && !bestExact) ||
				(exact == bestExact && info.ModTime().After(bestModTime))

			if better {
				id := rec.ID
				if id == "" {
					id = strings.TrimSuffix(d.Name(), ".json")
				}
				bestID = id
				bestModTime = info.ModTime()
				bestExact = exact
			}

			return nil
		})
	}

	if bestID == "" {
		return "", time.Time{}, false
	}
	return bestID, bestModTime, true
}

// WriteSessionFile writes a session and its messages to OpenCode's storage.
func WriteSessionFile(projectPath, sessionID string, transcriptData []byte) (string, error) {
	sessionDir, err := GetSessionDir(projectPath)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return "", fmt.Errorf("could not create session directory: %w", err)
	}

	sessionPath := filepath.Join(sessionDir, sessionID+".json")

	// Write a minimal session file
	session := sessionInfo{
		ID:        sessionID,
		ProjectID: GetProjectID(projectPath),
		Directory: projectPath,
		Title:     "Restored session",
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return "", fmt.Errorf("could not marshal session: %w", err)
	}

	if err := os.WriteFile(sessionPath, data, 0600); err != nil {
		return "", fmt.Errorf("could not write session file: %w", err)
	}

	// Write messages from transcript data
	msgDir, err := GetMessageDir(sessionID)
	if err != nil {
		return sessionPath, nil // Session created, messages optional
	}

	if err := os.MkdirAll(msgDir, 0700); err != nil {
		return sessionPath, nil
	}

	// Write the raw transcript data as a single message file for restore
	msgPath := filepath.Join(msgDir, "transcript.jsonl")
	_ = os.WriteFile(msgPath, transcriptData, 0600)

	return sessionPath, nil
}
