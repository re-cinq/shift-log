package agent

import (
	"io"
	"path/filepath"
	"strings"
	"time"
)

// RecentSessionTimeout is the default timeout for considering a session "recent"
// during session discovery across all agents.
const RecentSessionTimeout = 5 * time.Minute

// IsGitCommitCommand checks whether a shell command string represents a git commit.
func IsGitCommitCommand(command string) bool {
	return strings.Contains(command, "git commit") ||
		strings.Contains(command, "git-commit")
}

// PathsEqual compares two filesystem paths after resolving symlinks.
// Falls back to filepath.Clean comparison if symlink resolution fails.
func PathsEqual(a, b string) bool {
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		ra = filepath.Clean(a)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		rb = filepath.Clean(b)
	}
	return ra == rb
}

// HasNestedHookCommand checks if a nested hook config (used by Claude/Gemini)
// contains a specific command string. The structure is: [{hooks: [{command: "..."}]}].
func HasNestedHookCommand(hookConfig interface{}, command string) bool {
	hookList, ok := hookConfig.([]interface{})
	if !ok {
		return false
	}
	for _, h := range hookList {
		hookMap, _ := h.(map[string]interface{})
		hookCmds, _ := hookMap["hooks"].([]interface{})
		for _, hc := range hookCmds {
			hcMap, _ := hc.(map[string]interface{})
			if cmd, ok := hcMap["command"].(string); ok {
				if strings.Contains(cmd, command) {
					return true
				}
			}
		}
	}
	return false
}

// HasFlatHookCommand checks if a flat hook config (used by Copilot)
// contains a specific command string. The structure is: [{command: "..."}].
func HasFlatHookCommand(hookConfig interface{}, command string) bool {
	hookList, ok := hookConfig.([]interface{})
	if !ok {
		return false
	}
	for _, h := range hookList {
		hookMap, _ := h.(map[string]interface{})
		if cmd, ok := hookMap["command"].(string); ok {
			if strings.Contains(cmd, command) {
				return true
			}
		}
	}
	return false
}

// Name identifies a coding agent CLI.
type Name string

const (
	Claude   Name = "claude"
	Codex    Name = "codex"
	Copilot  Name = "copilot"
	Gemini   Name = "gemini"
	OpenCode Name = "opencode"
)

// DiagnosticCheck represents a single doctor check result.
type DiagnosticCheck struct {
	Name    string
	OK      bool
	Message string
}

// HookData represents parsed hook input from a coding agent.
type HookData struct {
	SessionID      string
	TranscriptPath string
	ToolName       string
	Command        string
}

// SessionInfo represents a discovered active session.
type SessionInfo struct {
	SessionID      string
	TranscriptPath string
	StartedAt      string
	ProjectPath    string
}

// Agent defines the interface that each coding agent CLI must implement.
type Agent interface {
	// Identity
	Name() Name
	DisplayName() string

	// Init: configure CLI-specific hooks/plugins
	ConfigureHooks(repoRoot string) error

	// Doctor: validate hook configuration
	DiagnoseHooks(repoRoot string) []DiagnosticCheck

	// Store: detect commit commands from hook input
	ParseHookInput(raw []byte) (*HookData, error)
	IsCommitCommand(toolName, command string) bool

	// Transcripts: parse into common format
	ParseTranscript(r io.Reader) (*Transcript, error)
	ParseTranscriptFile(path string) (*Transcript, error)

	// Sessions: discover and restore
	DiscoverSession(projectPath string) (*SessionInfo, error)
	RestoreSession(projectPath, sessionID, gitBranch string,
		transcriptData []byte, messageCount int, summary string) error
	ResumeCommand(sessionID string) (binary string, args []string)

	// Rendering: map tool names for display
	ToolAliases() map[string]string
}
