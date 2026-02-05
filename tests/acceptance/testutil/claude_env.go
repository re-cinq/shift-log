package testutil

import (
	"os"
	"path/filepath"
)

// ClaudeEnv represents an isolated Claude Code test environment
type ClaudeEnv struct {
	// TempHome is the temporary HOME directory for isolation
	TempHome string
	// OrigHome is the original HOME to restore after tests
	OrigHome string
}

// NewClaudeEnv creates a new isolated Claude environment for testing.
// It creates a temporary HOME directory to prevent tests from affecting
// the real Claude configuration.
func NewClaudeEnv() (*ClaudeEnv, error) {
	tempHome, err := os.MkdirTemp("", "claudit-test-home-*")
	if err != nil {
		return nil, err
	}

	env := &ClaudeEnv{
		TempHome: tempHome,
		OrigHome: os.Getenv("HOME"),
	}

	return env, nil
}

// Cleanup removes the temporary HOME directory and restores the original HOME
func (e *ClaudeEnv) Cleanup() {
	os.Setenv("HOME", e.OrigHome)
	os.RemoveAll(e.TempHome)
}

// Apply sets the HOME environment variable to the temporary directory
func (e *ClaudeEnv) Apply() {
	os.Setenv("HOME", e.TempHome)
}

// GetEnvVars returns environment variables for running commands with isolated HOME
func (e *ClaudeEnv) GetEnvVars() []string {
	return []string{"HOME=" + e.TempHome}
}

// GetClaudeDir returns the path to the .claude directory in the temp HOME
func (e *ClaudeEnv) GetClaudeDir() string {
	return filepath.Join(e.TempHome, ".claude")
}

// GetProjectsDir returns the path to the projects directory in the temp HOME
func (e *ClaudeEnv) GetProjectsDir() string {
	return filepath.Join(e.TempHome, ".claude", "projects")
}

// GetProjectDir returns the path to a specific project's session directory
func (e *ClaudeEnv) GetProjectDir(projectPath string) string {
	// Encode the project path (replace / with -)
	encoded := encodeProjectPath(projectPath)
	return filepath.Join(e.TempHome, ".claude", "projects", encoded)
}

// SetupProjectDir creates the directory structure for a project
func (e *ClaudeEnv) SetupProjectDir(projectPath string) error {
	projectDir := e.GetProjectDir(projectPath)
	return os.MkdirAll(projectDir, 0700)
}

// WriteSessionFile writes a session JSONL file for testing
func (e *ClaudeEnv) WriteSessionFile(projectPath, sessionID string, content []byte) (string, error) {
	projectDir := e.GetProjectDir(projectPath)
	if err := os.MkdirAll(projectDir, 0700); err != nil {
		return "", err
	}

	sessionPath := filepath.Join(projectDir, sessionID+".jsonl")
	if err := os.WriteFile(sessionPath, content, 0600); err != nil {
		return "", err
	}

	return sessionPath, nil
}

// SessionFileExists checks if a session file exists
func (e *ClaudeEnv) SessionFileExists(projectPath, sessionID string) bool {
	sessionPath := filepath.Join(e.GetProjectDir(projectPath), sessionID+".jsonl")
	_, err := os.Stat(sessionPath)
	return err == nil
}

// ReadSessionFile reads a session JSONL file
func (e *ClaudeEnv) ReadSessionFile(projectPath, sessionID string) ([]byte, error) {
	sessionPath := filepath.Join(e.GetProjectDir(projectPath), sessionID+".jsonl")
	return os.ReadFile(sessionPath)
}

// GetSessionsIndexPath returns the path to sessions-index.json for a project
func (e *ClaudeEnv) GetSessionsIndexPath(projectPath string) string {
	return filepath.Join(e.GetProjectDir(projectPath), "sessions-index.json")
}

// SessionsIndexExists checks if sessions-index.json exists for a project
func (e *ClaudeEnv) SessionsIndexExists(projectPath string) bool {
	_, err := os.Stat(e.GetSessionsIndexPath(projectPath))
	return err == nil
}

// encodeProjectPath converts an absolute path to Claude's encoded format
// This must match the production EncodeProjectPath in internal/claude/session.go
func encodeProjectPath(path string) string {
	// Replace path separators with dashes
	encoded := ""
	for _, c := range path {
		if c == '/' || c == filepath.Separator {
			encoded += "-"
		} else {
			encoded += string(c)
		}
	}
	// Ensure it starts with a dash (the root "/" becomes the leading dash)
	// This matches the production behavior in internal/claude/session.go:41-43
	if len(encoded) > 0 && encoded[0] != '-' {
		encoded = "-" + encoded
	}
	return encoded
}
