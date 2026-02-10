package gemini

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
	if a.Name() != agent.Gemini {
		t.Errorf("Name() = %q, want %q", a.Name(), agent.Gemini)
	}
}

func TestAgentDisplayName(t *testing.T) {
	a := &Agent{}
	if a.DisplayName() != "Gemini CLI" {
		t.Errorf("DisplayName() = %q, want %q", a.DisplayName(), "Gemini CLI")
	}
}

func TestParseHookInput(t *testing.T) {
	a := &Agent{}
	input := `{"session_id":"sess-1","transcript_path":"/tmp/t.jsonl","tool_name":"shell","tool_input":{"command":"git commit -m test"}}`

	hook, err := a.ParseHookInput([]byte(input))
	if err != nil {
		t.Fatalf("ParseHookInput() error: %v", err)
	}
	if hook.SessionID != "sess-1" {
		t.Errorf("SessionID = %q, want %q", hook.SessionID, "sess-1")
	}
	if hook.TranscriptPath != "/tmp/t.jsonl" {
		t.Errorf("TranscriptPath = %q, want %q", hook.TranscriptPath, "/tmp/t.jsonl")
	}
	if hook.ToolName != "shell" {
		t.Errorf("ToolName = %q, want %q", hook.ToolName, "shell")
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
		{"shell", "git commit -m fix", true},
		{"shell_exec", "git commit -am msg", true},
		{"run_in_terminal", "git-commit", true},
		{"execute_command", "ls -la", false},
		{"read_file", "git commit -m test", false},
		{"shell", "git status", false},
	}

	for _, tc := range tests {
		got := a.IsCommitCommand(tc.tool, tc.cmd)
		if got != tc.want {
			t.Errorf("IsCommitCommand(%q, %q) = %v, want %v", tc.tool, tc.cmd, got, tc.want)
		}
	}
}

func TestParseJSONLTranscript(t *testing.T) {
	lines := []string{
		`{"type":"user","id":"u1","content":"Hello"}`,
		`{"type":"gemini","id":"g1","content":[{"text":"Hi there"}]}`,
		`{"type":"user","id":"u2","content":"Thanks"}`,
	}
	jsonl := strings.Join(lines, "\n")

	transcript, err := ParseJSONLTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ParseJSONLTranscript() error: %v", err)
	}
	if len(transcript.Entries) != 3 {
		t.Fatalf("Expected 3 entries, got %d", len(transcript.Entries))
	}

	// Check first entry (user)
	if transcript.Entries[0].Type != agent.MessageTypeUser {
		t.Errorf("Entry 0 type = %q, want %q", transcript.Entries[0].Type, agent.MessageTypeUser)
	}
	if transcript.Entries[0].UUID != "u1" {
		t.Errorf("Entry 0 UUID = %q, want %q", transcript.Entries[0].UUID, "u1")
	}

	// Check second entry (gemini -> assistant)
	if transcript.Entries[1].Type != agent.MessageTypeAssistant {
		t.Errorf("Entry 1 type = %q, want %q", transcript.Entries[1].Type, agent.MessageTypeAssistant)
	}
	if transcript.Entries[1].UUID != "g1" {
		t.Errorf("Entry 1 UUID = %q, want %q", transcript.Entries[1].UUID, "g1")
	}
}

func TestParseJSONLTranscriptModelType(t *testing.T) {
	// Gemini also uses "model" type for assistant messages
	jsonl := `{"type":"model","id":"m1","content":"Response text"}`
	transcript, err := ParseJSONLTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ParseJSONLTranscript() error: %v", err)
	}
	if len(transcript.Entries) != 1 {
		t.Fatalf("Expected 1 entry, got %d", len(transcript.Entries))
	}
	if transcript.Entries[0].Type != agent.MessageTypeAssistant {
		t.Errorf("Entry type = %q, want %q", transcript.Entries[0].Type, agent.MessageTypeAssistant)
	}
}

func TestParseJSONLTranscriptSkipsMetadata(t *testing.T) {
	lines := []string{
		`{"type":"user","id":"u1","content":"Hello"}`,
		`{"type":"session_metadata","version":"1.0"}`,
		`{"type":"message_update","status":"done"}`,
		`{"type":"assistant","id":"a1","content":"Done"}`,
	}
	jsonl := strings.Join(lines, "\n")

	transcript, err := ParseJSONLTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ParseJSONLTranscript() error: %v", err)
	}
	if len(transcript.Entries) != 2 {
		t.Errorf("Expected 2 entries (skipping metadata), got %d", len(transcript.Entries))
	}
}

func TestParseJSONLTranscriptEmpty(t *testing.T) {
	transcript, err := ParseJSONLTranscript(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseJSONLTranscript() error: %v", err)
	}
	if len(transcript.Entries) != 0 {
		t.Errorf("Expected 0 entries, got %d", len(transcript.Entries))
	}
}

func TestToolAliases(t *testing.T) {
	a := &Agent{}
	aliases := a.ToolAliases()
	if aliases["shell"] != "Bash" {
		t.Errorf("ToolAliases[shell] = %q, want Bash", aliases["shell"])
	}
	if aliases["write_file"] != "Write" {
		t.Errorf("ToolAliases[write_file] = %q, want Write", aliases["write_file"])
	}
}

func TestResumeCommand(t *testing.T) {
	a := &Agent{}
	bin, args := a.ResumeCommand("sess-123")
	if bin != "gemini" {
		t.Errorf("ResumeCommand binary = %q, want gemini", bin)
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

	// Check settings file was created
	settingsPath := filepath.Join(tmpDir, ".gemini", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read settings: %v", err)
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Invalid JSON: %v", err)
	}

	hooks, ok := raw["hooks"].(map[string]interface{})
	if !ok {
		t.Fatal("Missing hooks key in settings")
	}

	afterTool, ok := hooks["AfterTool"].([]interface{})
	if !ok || len(afterTool) == 0 {
		t.Fatal("Missing AfterTool hook")
	}
}

func TestDiagnoseHooks(t *testing.T) {
	a := &Agent{}

	t.Run("no settings file", func(t *testing.T) {
		tmpDir := t.TempDir()
		checks := a.DiagnoseHooks(tmpDir)
		if len(checks) == 0 {
			t.Fatal("Expected diagnostic checks")
		}
		if checks[0].OK {
			t.Error("Expected check to fail when no settings file")
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

func TestEncodeProjectPath(t *testing.T) {
	tests := []struct {
		path, want string
	}{
		{"/home/user/project", "home_user_project"},
		{"/tmp/test", "tmp_test"},
	}
	for _, tc := range tests {
		got := EncodeProjectPath(tc.path)
		if got != tc.want {
			t.Errorf("EncodeProjectPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestNormalizeGeminiType(t *testing.T) {
	tests := []struct {
		input string
		want  agent.MessageType
	}{
		{"user", agent.MessageTypeUser},
		{"gemini", agent.MessageTypeAssistant},
		{"model", agent.MessageTypeAssistant},
		{"assistant", agent.MessageTypeAssistant},
		{"system", agent.MessageTypeSystem},
		{"session_metadata", ""},
		{"unknown", ""},
	}
	for _, tc := range tests {
		got := normalizeGeminiType(tc.input)
		if got != tc.want {
			t.Errorf("normalizeGeminiType(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}
