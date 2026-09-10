```go
package opencode

import (
	"encoding/json"
	"fmt"
	"io/fs"
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

// sessionCandidate represents an OpenCode session discovered by scanning
// disk for a matching working directory, used when the project-ID-keyed
// directory layout doesn't match the installed OpenCode CLI version.
type sessionCandidate struct {
	sessionID  string
	messageDir string
	modTime    time.Time
}

// stringField looks up the first non-empty string value among the given
// keys in a decoded JSON object.
func stringField(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return s
			}
		}
	}
	return ""
}

// findSessionsByDirectory recursively scans dataDir for OpenCode session
// JSON files (stored inside a directory literally named "session") whose
// recorded working directory matches projectPath. This tolerates OpenCode
// CLI versions that key session storage by an internal directory hash
// rather than by the git root commit hash used in GetProjectID.
func findSessionsByDirectory(dataDir, projectPath string) []sessionCandidate {
	var candidates []sessionCandidate

	_ = filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) != "session" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil
		}

		dir := stringField(raw, "directory", "path", "cwd", "worktree")
		if dir == "" || !agent.PathsEqual(dir, projectPath) {
			return nil
		}

		id := stringField(raw, "id")
		if id == "" {
			id = strings.TrimSuffix(filepath.Base(path), ".json")
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		storageDir := filepath.Dir(filepath.Dir(path)) // parent of "session"
		messageDir := filepath.Join(storageDir, "message", id)
		if _, err := os.Stat(messageDir); err != nil {
			if found, ok := findMessageDir(dataDir, id); ok {
				messageDir = found
			}
		}

		candidates = append(candidates, sessionCandidate{
			sessionID:  id,
			messageDir: messageDir,
			modTime:    info.ModTime(),
		})
		return nil
	})

	return candidates
}

// findMessageDir searches dataDir for a directory named sessionID whose
// parent directory is literally named "message".
func findMessageDir(dataDir, sessionID string) (string, bool) {
	var found string
	_ = filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() && d.Name() == sessionID && filepath.Base(filepath.Dir(path)) == "message" {
			found = path
			return fs.SkipAll
		}
		return nil
	})
	return found, found != ""
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
```
