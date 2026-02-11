package testutil

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// AgentTestConfig holds all agent-specific bits needed to parameterize tests.
type AgentTestConfig struct {
	Name      string
	InitArgs  []string // e.g. ["init"] or ["init", "--agent=gemini"]
	StoreArgs []string // e.g. ["store"] or ["store", "--agent=gemini"]

	// Fixture functions
	SampleTranscript   func() string
	SampleHookInput    func(sessionID, transcriptPath, command string) string
	SampleNonToolInput func(sessionID string) string

	// Init verification
	SettingsFile    string  // ".claude/settings.local.json" or ".opencode/plugins/claudit.js"
	HookKey         string  // "PostToolUse" or "AfterTool" (empty for plugin-based agents)
	ToolMatcher     string  // "Bash" or "run_shell_command" (empty for plugin-based agents)
	StoreCommand    string  // "claudit store" or "claudit store --agent=opencode"
	Timeout         float64 // 30 or 30000
	HasSessionHooks bool    // Gemini has SessionStart/SessionEnd
	SessionTimeout  float64 // 5000 for Gemini
	IsPluginBased   bool    // true for agents that use a plugin file instead of JSON settings

	// Resume / session verification
	SessionFileExt         string                                                   // ".jsonl" or ".json"
	GetSessionDir          func(homeDir, projectPath string) string
	NeedsBinaryPath        bool                                                     // true when init installs hooks referencing claudit
	HasSessionsIndex       bool                                                     // true if agent creates sessions-index.json on restore
	ReadRestoredTranscript func(homeDir, projectPath, sessionID string) ([]byte, error) // nil = use session file

	// Transcript file extension for writing temp files
	TranscriptFileExt string // ".jsonl" or ".json"

	// PrepareTranscript sets up transcript data for store testing.
	// Returns the parameter to pass as the second arg to SampleHookInput.
	// For Claude/Gemini: writes a single file, returns its path.
	// For OpenCode: creates a directory structure, returns the data_dir.
	PrepareTranscript func(baseDir, sessionID, transcript string) (string, error)
}

// ClaudeTestConfig returns the test configuration for Claude agent.
func ClaudeTestConfig() AgentTestConfig {
	return AgentTestConfig{
		Name:      "Claude",
		InitArgs:  []string{"init"},
		StoreArgs: []string{"store"},

		SampleTranscript:   SampleTranscript,
		SampleHookInput:    SampleHookInput,
		SampleNonToolInput: SampleHookInputNonBash,

		SettingsFile:    ".claude/settings.local.json",
		HookKey:         "PostToolUse",
		ToolMatcher:     "Bash",
		StoreCommand:    "claudit store",
		Timeout:         30,
		HasSessionHooks: false,
		IsPluginBased:   false,

		SessionFileExt:         ".jsonl",
		TranscriptFileExt:      ".jsonl",
		GetSessionDir:          claudeSessionDir,
		NeedsBinaryPath:        false,
		HasSessionsIndex:       true,
		ReadRestoredTranscript: nil,
		PrepareTranscript:      claudePrepareTranscript,
	}
}

// GeminiTestConfig returns the test configuration for Gemini agent.
func GeminiTestConfig() AgentTestConfig {
	return AgentTestConfig{
		Name:      "Gemini",
		InitArgs:  []string{"init", "--agent=gemini"},
		StoreArgs: []string{"store", "--agent=gemini"},

		SampleTranscript:   SampleGeminiTranscript,
		SampleHookInput:    SampleGeminiHookInput,
		SampleNonToolInput: SampleGeminiHookInputNonShell,

		SettingsFile:    ".gemini/settings.json",
		HookKey:         "AfterTool",
		ToolMatcher:     "run_shell_command",
		StoreCommand:    "claudit store --agent=gemini",
		Timeout:         30000,
		HasSessionHooks: true,
		SessionTimeout:  5000,
		IsPluginBased:   false,

		SessionFileExt:         ".json",
		TranscriptFileExt:      ".json",
		GetSessionDir:          geminiSessionDir,
		NeedsBinaryPath:        true,
		HasSessionsIndex:       true,
		ReadRestoredTranscript: nil,
		PrepareTranscript:      geminiPrepareTranscript,
	}
}

// OpenCodeTestConfig returns the test configuration for OpenCode agent.
func OpenCodeTestConfig() AgentTestConfig {
	return AgentTestConfig{
		Name:      "OpenCode",
		InitArgs:  []string{"init", "--agent=opencode"},
		StoreArgs: []string{"store", "--agent=opencode"},

		SampleTranscript:   SampleOpenCodeTranscript,
		SampleHookInput:    SampleOpenCodeHookInput,
		SampleNonToolInput: SampleOpenCodeHookInputNonShell,

		SettingsFile:    ".opencode/plugins/claudit.js",
		HookKey:         "",  // plugin-based, no JSON hook key
		ToolMatcher:     "",  // plugin-based, no JSON tool matcher
		StoreCommand:    "claudit store --agent=opencode",
		Timeout:         30000,
		HasSessionHooks: false,
		IsPluginBased:   true,

		SessionFileExt:         ".json",
		TranscriptFileExt:      ".jsonl",
		GetSessionDir:          opencodeSessionDir,
		NeedsBinaryPath:        true,
		HasSessionsIndex:       false,
		ReadRestoredTranscript: opencodeReadRestoredTranscript,
		PrepareTranscript:      opencodePrepareTranscript,
	}
}

// AllAgentConfigs returns test configs for all agents.
func AllAgentConfigs() []AgentTestConfig {
	return []AgentTestConfig{ClaudeTestConfig(), GeminiTestConfig(), OpenCodeTestConfig()}
}

// claudeSessionDir computes the Claude projects session directory path.
// Claude: ~/.claude/projects/<dash-encoded-path>
func claudeSessionDir(homeDir, projectPath string) string {
	encoded := encodeProjectPath(projectPath)
	return filepath.Join(homeDir, ".claude", "projects", encoded)
}

// geminiSessionDir computes the Gemini session directory path.
// Gemini: ~/.gemini/tmp/<sha256-hash>/chats
func geminiSessionDir(homeDir, projectPath string) string {
	h := sha256.Sum256([]byte(projectPath))
	hash := fmt.Sprintf("%x", h)
	return filepath.Join(homeDir, ".gemini", "tmp", hash, "chats")
}

// opencodeSessionDir computes the OpenCode session directory path.
// OpenCode: ~/.local/share/opencode/storage/session/<project-id>
// where project-id is the root commit hash (or "global" for non-git dirs).
func opencodeSessionDir(homeDir, projectPath string) string {
	dataDir := filepath.Join(homeDir, ".local", "share", "opencode")
	projectID := getOpenCodeProjectID(projectPath)
	return filepath.Join(dataDir, "storage", "session", projectID)
}

// getOpenCodeProjectID returns the git root commit hash for the project.
// This must match the production GetProjectID in internal/agent/opencode/session.go.
func getOpenCodeProjectID(projectPath string) string {
	cmd := exec.Command("git", "rev-list", "--max-parents=0", "--all")
	cmd.Dir = projectPath
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "global"
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) > 0 && lines[0] != "" {
		return strings.TrimSpace(lines[0])
	}
	return "global"
}

// claudePrepareTranscript writes a Claude JSONL transcript file.
func claudePrepareTranscript(baseDir, sessionID, transcript string) (string, error) {
	path := filepath.Join(baseDir, "transcript.jsonl")
	return path, os.WriteFile(path, []byte(transcript), 0644)
}

// geminiPrepareTranscript writes a Gemini JSON transcript file.
func geminiPrepareTranscript(baseDir, sessionID, transcript string) (string, error) {
	path := filepath.Join(baseDir, "transcript.json")
	return path, os.WriteFile(path, []byte(transcript), 0644)
}
