package testutil

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
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
	SettingsFile    string  // ".claude/settings.local.json" or ".gemini/settings.json"
	HookKey         string  // "PostToolUse" or "AfterTool"
	ToolMatcher     string  // "Bash" or "run_shell_command"
	StoreCommand    string  // "claudit store" or "claudit store --agent=gemini"
	Timeout         float64 // 30 or 30000
	HasSessionHooks bool    // Gemini has SessionStart/SessionEnd
	SessionTimeout  float64 // 5000 for Gemini

	// Resume / session verification
	SessionFileExt  string                                // ".jsonl" or ".json"
	GetSessionDir   func(homeDir, projectPath string) string
	NeedsBinaryPath bool // true when init installs hooks referencing claudit

	// Transcript file extension for writing temp files
	TranscriptFileExt string // ".jsonl" or ".json"
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

		SessionFileExt:    ".jsonl",
		TranscriptFileExt: ".jsonl",
		GetSessionDir:     claudeSessionDir,
		NeedsBinaryPath:   false,
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

		SessionFileExt:    ".json",
		TranscriptFileExt: ".json",
		GetSessionDir:     geminiSessionDir,
		NeedsBinaryPath:   true,
	}
}

// AllAgentConfigs returns test configs for all agents.
func AllAgentConfigs() []AgentTestConfig {
	return []AgentTestConfig{ClaudeTestConfig(), GeminiTestConfig()}
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
