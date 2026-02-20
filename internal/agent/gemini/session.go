package gemini

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// GetGeminiDir returns the path to Gemini's config/data directory.
func GetGeminiDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, ".gemini"), nil
}

// ProjectsRegistry represents Gemini's ~/.gemini/projects.json file,
// which maps project paths to slug-based directory names (v0.29+).
type ProjectsRegistry struct {
	Projects map[string]ProjectRegistryEntry `json:"projects"`
}

// ProjectRegistryEntry represents a single project in the registry.
type ProjectRegistryEntry struct {
	Slug string `json:"slug"`
}

// ReadProjectsRegistry reads and parses ~/.gemini/projects.json.
// Returns nil (no error) if the file does not exist.
func ReadProjectsRegistry() (*ProjectsRegistry, error) {
	geminiDir, err := GetGeminiDir()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filepath.Join(geminiDir, "projects.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var reg ProjectsRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("could not parse projects.json: %w", err)
	}
	return &reg, nil
}

// GetSlugForProject looks up the slug for a project path from the registry.
// Returns "" if no registry exists or the project is not registered.
func GetSlugForProject(projectPath string) string {
	reg, err := ReadProjectsRegistry()
	if err != nil || reg == nil {
		return ""
	}
	if entry, ok := reg.Projects[projectPath]; ok {
		return entry.Slug
	}
	return ""
}

// GetSessionDir returns the session directory for a project.
// Gemini v0.29+ uses slug-based paths: ~/.gemini/tmp/<slug>/chats/
// Earlier versions use SHA256 hash paths: ~/.gemini/tmp/<sha256>/chats/
// This function tries the slug first, falling back to the hash.
func GetSessionDir(projectPath string) (string, error) {
	geminiDir, err := GetGeminiDir()
	if err != nil {
		return "", err
	}

	if slug := GetSlugForProject(projectPath); slug != "" {
		return filepath.Join(geminiDir, "tmp", slug, "chats"), nil
	}

	hash := EncodeProjectPath(projectPath)
	return filepath.Join(geminiDir, "tmp", hash, "chats"), nil
}

// GetLegacySessionDir returns the SHA256-based session directory (pre-v0.29).
func GetLegacySessionDir(projectPath string) (string, error) {
	geminiDir, err := GetGeminiDir()
	if err != nil {
		return "", err
	}
	hash := EncodeProjectPath(projectPath)
	return filepath.Join(geminiDir, "tmp", hash, "chats"), nil
}

// EncodeProjectPath encodes a project path for Gemini's directory structure.
// Gemini uses a SHA256 hash of the project path.
func EncodeProjectPath(projectPath string) string {
	h := sha256.Sum256([]byte(projectPath))
	return fmt.Sprintf("%x", h)
}

// GetSessionFilePath returns the full path to a session file.
// Gemini uses session-{timestamp}-{id8}.json format.
func GetSessionFilePath(projectPath, sessionID string) (string, error) {
	sessionDir, err := GetSessionDir(projectPath)
	if err != nil {
		return "", err
	}
	return filepath.Join(sessionDir, sessionID+".json"), nil
}

// WriteSessionFile writes a transcript to Gemini's session file location.
func WriteSessionFile(projectPath, sessionID string, transcriptData []byte) (string, error) {
	sessionPath, err := GetSessionFilePath(projectPath, sessionID)
	if err != nil {
		return "", err
	}

	sessionDir := filepath.Dir(sessionPath)
	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return "", fmt.Errorf("could not create session directory: %w", err)
	}

	if err := os.WriteFile(sessionPath, transcriptData, 0600); err != nil {
		return "", fmt.Errorf("could not write session file: %w", err)
	}

	return sessionPath, nil
}

// SessionsIndex represents a sessions index for tracking Gemini sessions.
type SessionsIndex struct {
	Version int            `json:"version"`
	Entries []SessionEntry `json:"entries"`
}

// SessionEntry represents a session entry in the index.
type SessionEntry struct {
	SessionID    string `json:"sessionId"`
	FullPath     string `json:"fullPath"`
	MessageCount int    `json:"messageCount"`
	Created      string `json:"created"`
	Modified     string `json:"modified"`
	ProjectPath  string `json:"projectPath"`
}

// ReadSessionsIndex reads the sessions index for a project.
func ReadSessionsIndex(projectPath string) (*SessionsIndex, error) {
	sessionDir, err := GetSessionDir(projectPath)
	if err != nil {
		return nil, err
	}

	indexPath := filepath.Join(sessionDir, "sessions-index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &SessionsIndex{Version: 1, Entries: []SessionEntry{}}, nil
		}
		return nil, err
	}

	var index SessionsIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("could not parse sessions-index.json: %w", err)
	}

	return &index, nil
}

// WriteSessionsIndex writes the sessions index for a project.
func WriteSessionsIndex(projectPath string, index *SessionsIndex) error {
	sessionDir, err := GetSessionDir(projectPath)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(sessionDir, 0700); err != nil {
		return fmt.Errorf("could not create session directory: %w", err)
	}

	indexPath := filepath.Join(sessionDir, "sessions-index.json")
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("could not marshal sessions-index.json: %w", err)
	}

	return os.WriteFile(indexPath, data, 0600)
}

// AddOrUpdateSessionEntry adds or updates a session entry in the index.
func AddOrUpdateSessionEntry(index *SessionsIndex, entry SessionEntry) {
	for i, e := range index.Entries {
		if e.SessionID == entry.SessionID {
			index.Entries[i] = entry
			return
		}
	}
	index.Entries = append(index.Entries, entry)
}
