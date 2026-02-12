package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/re-cinq/claudit/internal/agent"
)

func TestAgentName(t *testing.T) {
	a := &Agent{}
	if a.Name() != agent.Copilot {
		t.Errorf("Name() = %q, want %q", a.Name(), agent.Copilot)
	}
}

func TestAgentDisplayName(t *testing.T) {
	a := &Agent{}
	if a.DisplayName() != "Copilot CLI" {
		t.Errorf("DisplayName() = %q, want %q", a.DisplayName(), "Copilot CLI")
	}
}

func TestParseHookInput(t *testing.T) {
	a := &Agent{}
	input := `{"timestamp":1700000000,"cwd":"/tmp/project","toolName":"shell_run","toolArgs":"{\"command\":\"git commit -m test\"}"}`

	hook, err := a.ParseHookInput([]byte(input))
	if err != nil {
		t.Fatalf("ParseHookInput() error: %v", err)
	}
	if hook.ToolName != "shell_run" {
		t.Errorf("ToolName = %q, want %q", hook.ToolName, "shell_run")
	}
	if hook.Command != "git commit -m test" {
		t.Errorf("Command = %q, want %q", hook.Command, "git commit -m test")
	}
}

func TestIsCommitCommand(t *testing.T) {
	a := &Agent{}
	tests := []struct {
		tool, cmd string
		want      bool
	}{
		{"shell_run", "git commit -m fix", true},
		{"shell_run", "git commit -am msg", true},
		{"bash", "git commit -m test", true},
		{"shell_run", "git-commit", true},
		{"shell_run", "ls -la", false},
		{"shell_run", "git status", false},
		{"view", "git commit -m test", false},
		{"edit", "git commit -m test", false},
		{"write", "git commit -m test", false},
	}

	for _, tc := range tests {
		got := a.IsCommitCommand(tc.tool, tc.cmd)
		if got != tc.want {
			t.Errorf("IsCommitCommand(%q, %q) = %v, want %v", tc.tool, tc.cmd, got, tc.want)
		}
	}
}

func TestParseCopilotTranscript(t *testing.T) {
	session := `{
		"sessionId": "sess-1",
		"cwd": "/tmp/project",
		"model": "gpt-4",
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there"},
			{"role": "user", "content": "Thanks"}
		]
	}`

	transcript, err := parseCopilotTranscript(strings.NewReader(session))
	if err != nil {
		t.Fatalf("parseCopilotTranscript() error: %v", err)
	}
	if len(transcript.Entries) != 3 {
		t.Fatalf("Expected 3 entries, got %d", len(transcript.Entries))
	}

	if transcript.Entries[0].Type != agent.MessageTypeUser {
		t.Errorf("Entry 0 type = %q, want %q", transcript.Entries[0].Type, agent.MessageTypeUser)
	}
	if transcript.Entries[1].Type != agent.MessageTypeAssistant {
		t.Errorf("Entry 1 type = %q, want %q", transcript.Entries[1].Type, agent.MessageTypeAssistant)
	}
	if transcript.Entries[1].Message == nil || len(transcript.Entries[1].Message.Content) == 0 {
		t.Fatal("Entry 1 has no message content")
	}
	if transcript.Entries[1].Message.Content[0].Text != "Hi there" {
		t.Errorf("Entry 1 text = %q, want %q", transcript.Entries[1].Message.Content[0].Text, "Hi there")
	}
}

func TestParseCopilotTranscriptWithToolCalls(t *testing.T) {
	session := `{
		"sessionId": "sess-1",
		"cwd": "/tmp/project",
		"model": "gpt-4",
		"messages": [
			{"role": "user", "content": "Run ls"},
			{"role": "assistant", "content": "Running command", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "shell_run", "arguments": "{\"command\":\"ls -la\"}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "file1.txt\nfile2.txt"}
		]
	}`

	transcript, err := parseCopilotTranscript(strings.NewReader(session))
	if err != nil {
		t.Fatalf("parseCopilotTranscript() error: %v", err)
	}
	if len(transcript.Entries) != 3 {
		t.Fatalf("Expected 3 entries, got %d", len(transcript.Entries))
	}

	// Check assistant entry has text and tool_use content blocks
	entry := transcript.Entries[1]
	if len(entry.Message.Content) != 2 {
		t.Fatalf("Expected 2 content blocks, got %d", len(entry.Message.Content))
	}
	if entry.Message.Content[0].Type != "text" {
		t.Errorf("Content[0] type = %q, want text", entry.Message.Content[0].Type)
	}
	if entry.Message.Content[1].Type != "tool_use" {
		t.Errorf("Content[1] type = %q, want tool_use", entry.Message.Content[1].Type)
	}
	if entry.Message.Content[1].Name != "shell_run" {
		t.Errorf("Content[1] name = %q, want shell_run", entry.Message.Content[1].Name)
	}

	// Check tool result entry
	toolEntry := transcript.Entries[2]
	if toolEntry.Type != agent.MessageTypeUser {
		t.Errorf("Tool entry type = %q, want %q", toolEntry.Type, agent.MessageTypeUser)
	}
}

func TestParseCopilotTranscriptEmpty(t *testing.T) {
	transcript, err := parseCopilotTranscript(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parseCopilotTranscript() error: %v", err)
	}
	if len(transcript.Entries) != 0 {
		t.Errorf("Expected 0 entries, got %d", len(transcript.Entries))
	}
}

func TestParseCopilotTranscriptEmptyMessages(t *testing.T) {
	session := `{"sessionId":"s1","cwd":"/tmp","model":"gpt-4","messages":[]}`
	transcript, err := parseCopilotTranscript(strings.NewReader(session))
	if err != nil {
		t.Fatalf("parseCopilotTranscript() error: %v", err)
	}
	if len(transcript.Entries) != 0 {
		t.Errorf("Expected 0 entries, got %d", len(transcript.Entries))
	}
}

func TestToolAliases(t *testing.T) {
	a := &Agent{}
	aliases := a.ToolAliases()
	if aliases["shell_run"] != "Bash" {
		t.Errorf("ToolAliases[shell_run] = %q, want Bash", aliases["shell_run"])
	}
	if aliases["bash"] != "Bash" {
		t.Errorf("ToolAliases[bash] = %q, want Bash", aliases["bash"])
	}
	if aliases["view"] != "Read" {
		t.Errorf("ToolAliases[view] = %q, want Read", aliases["view"])
	}
	if aliases["edit"] != "Edit" {
		t.Errorf("ToolAliases[edit] = %q, want Edit", aliases["edit"])
	}
	if aliases["write"] != "Write" {
		t.Errorf("ToolAliases[write] = %q, want Write", aliases["write"])
	}
}

func TestResumeCommand(t *testing.T) {
	a := &Agent{}
	bin, args := a.ResumeCommand("sess-123")
	if bin != "copilot" {
		t.Errorf("ResumeCommand binary = %q, want copilot", bin)
	}
	if len(args) != 2 || args[0] != "--resume" || args[1] != "sess-123" {
		t.Errorf("ResumeCommand args = %v, want [--resume sess-123]", args)
	}
}

func TestConfigureHooks(t *testing.T) {
	a := &Agent{}
	tmpDir := t.TempDir()

	if err := a.ConfigureHooks(tmpDir); err != nil {
		t.Fatalf("ConfigureHooks() error: %v", err)
	}

	hooksPath := filepath.Join(tmpDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("Failed to read hooks.json: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	// Check version
	if v, ok := raw["version"].(float64); !ok || int(v) != 1 {
		t.Errorf("version = %v, want 1", raw["version"])
	}

	hooks, ok := raw["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("Missing hooks key")
	}

	postToolUse, ok := hooks["postToolUse"].([]interface{})
	if !ok || len(postToolUse) == 0 {
		t.Fatal("Missing postToolUse hook")
	}

	hookObj := postToolUse[0].(map[string]interface{})
	if hookObj["type"] != "bash" {
		t.Errorf("hook type = %q, want bash", hookObj["type"])
	}
	if hookObj["bash"] != "claudit store --agent=copilot" {
		t.Errorf("hook bash = %q, want 'claudit store --agent=copilot'", hookObj["bash"])
	}
	if timeout, ok := hookObj["timeoutSec"].(float64); !ok || timeout != 30 {
		t.Errorf("hook timeoutSec = %v, want 30", hookObj["timeoutSec"])
	}
}

func TestConfigureHooksIdempotent(t *testing.T) {
	a := &Agent{}
	tmpDir := t.TempDir()

	if err := a.ConfigureHooks(tmpDir); err != nil {
		t.Fatalf("ConfigureHooks() first call error: %v", err)
	}
	if err := a.ConfigureHooks(tmpDir); err != nil {
		t.Fatalf("ConfigureHooks() second call error: %v", err)
	}

	hooksPath := filepath.Join(tmpDir, "hooks.json")
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("Failed to read hooks.json: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	hooks := raw["hooks"].(map[string]interface{})
	postToolUse := hooks["postToolUse"].([]interface{})
	if len(postToolUse) != 1 {
		t.Errorf("Expected 1 postToolUse entry, got %d", len(postToolUse))
	}
}

func TestConfigureHooksPreservesExisting(t *testing.T) {
	a := &Agent{}
	tmpDir := t.TempDir()

	// Write pre-existing hooks.json with extra data
	existing := `{"version":1,"hooks":{"postToolUse":[{"type":"bash","bash":"other-tool","timeoutSec":10}]},"customKey":"customValue"}`
	if err := os.WriteFile(filepath.Join(tmpDir, "hooks.json"), []byte(existing), 0644); err != nil {
		t.Fatalf("WriteFile() error: %v", err)
	}

	if err := a.ConfigureHooks(tmpDir); err != nil {
		t.Fatalf("ConfigureHooks() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "hooks.json"))
	if err != nil {
		t.Fatalf("Failed to read hooks.json: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	// Verify custom key preserved
	if raw["customKey"] != "customValue" {
		t.Errorf("customKey = %v, want customValue", raw["customKey"])
	}

	// Verify existing hook preserved and claudit hook added
	hooks := raw["hooks"].(map[string]interface{})
	postToolUse := hooks["postToolUse"].([]interface{})
	if len(postToolUse) != 2 {
		t.Errorf("Expected 2 postToolUse entries, got %d", len(postToolUse))
	}

	// First should be the existing hook
	first := postToolUse[0].(map[string]interface{})
	if first["bash"] != "other-tool" {
		t.Errorf("First entry bash = %q, want 'other-tool'", first["bash"])
	}

	// Second should be the claudit hook
	second := postToolUse[1].(map[string]interface{})
	if second["bash"] != "claudit store --agent=copilot" {
		t.Errorf("Second entry bash = %q, want 'claudit store --agent=copilot'", second["bash"])
	}
}

func TestDiagnoseHooks(t *testing.T) {
	a := &Agent{}

	t.Run("no hooks file", func(t *testing.T) {
		tmpDir := t.TempDir()
		checks := a.DiagnoseHooks(tmpDir)
		if len(checks) == 0 {
			t.Fatal("Expected diagnostic checks")
		}
		if checks[0].OK {
			t.Error("Expected check to fail when no hooks.json file")
		}
	})

	t.Run("with hooks configured", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := a.ConfigureHooks(tmpDir); err != nil {
			t.Fatalf("ConfigureHooks() error: %v", err)
		}
		checks := a.DiagnoseHooks(tmpDir)
		allOK := true
		for _, c := range checks {
			if !c.OK {
				allOK = false
				t.Errorf("Check %q failed: %s", c.Name, c.Message)
			}
		}
		if !allOK {
			t.Error("Expected all checks to pass after ConfigureHooks")
		}
	})
}

func TestExtractCommand(t *testing.T) {
	tests := []struct {
		name     string
		toolName string
		toolArgs string
		want     string
	}{
		{"shell_run with command key", "shell_run", `{"command":"git commit -m test"}`, "git commit -m test"},
		{"shell_run with cmd key", "shell_run", `{"cmd":"ls -la"}`, "ls -la"},
		{"non-shell tool", "view", `{"path":"/some/file"}`, ""},
		{"empty toolArgs", "shell_run", "", ""},
		{"invalid JSON fallback", "shell_run", "not-json", "not-json"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := extractCommand(tc.toolName, tc.toolArgs)
			if got != tc.want {
				t.Errorf("extractCommand(%q, %q) = %q, want %q", tc.toolName, tc.toolArgs, got, tc.want)
			}
		})
	}
}

func TestNormalizeCopilotRole(t *testing.T) {
	tests := []struct {
		input string
		want  agent.MessageType
	}{
		{"user", agent.MessageTypeUser},
		{"assistant", agent.MessageTypeAssistant},
		{"copilot", agent.MessageTypeAssistant},
		{"system", agent.MessageTypeSystem},
		{"tool", agent.MessageTypeUser},
		{"unknown", ""},
	}
	for _, tc := range tests {
		got := normalizeCopilotRole(tc.input)
		if got != tc.want {
			t.Errorf("normalizeCopilotRole(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
