package claude

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestRendererUserMessage(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)

	entry := &TranscriptEntry{
		Type: MessageTypeUser,
		Message: &Message{
			Role: "user",
			Content: []ContentBlock{
				{Type: "text", Text: "Hello world"},
			},
		},
	}

	r.RenderEntry(entry)

	output := buf.String()
	if !strings.Contains(output, "User:") {
		t.Errorf("Output should contain 'User:', got: %s", output)
	}
	if !strings.Contains(output, "Hello world") {
		t.Errorf("Output should contain message text, got: %s", output)
	}
}

func TestRendererAssistantMessage(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)

	entry := &TranscriptEntry{
		Type: MessageTypeAssistant,
		Message: &Message{
			Role: "assistant",
			Content: []ContentBlock{
				{Type: "text", Text: "Hi there!"},
			},
		},
	}

	r.RenderEntry(entry)

	output := buf.String()
	if !strings.Contains(output, "Assistant:") {
		t.Errorf("Output should contain 'Assistant:', got: %s", output)
	}
	if !strings.Contains(output, "Hi there!") {
		t.Errorf("Output should contain message text, got: %s", output)
	}
}

func TestRendererSystemMessage(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)

	entry := &TranscriptEntry{
		Type: MessageTypeSystem,
		Message: &Message{
			Content: []ContentBlock{
				{Type: "text", Text: "System prompt"},
			},
		},
	}

	r.RenderEntry(entry)

	output := buf.String()
	if !strings.Contains(output, "System:") {
		t.Errorf("Output should contain 'System:', got: %s", output)
	}
}

func TestRendererToolUse(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)

	entry := &TranscriptEntry{
		Type: MessageTypeAssistant,
		Message: &Message{
			Role: "assistant",
			Content: []ContentBlock{
				{Type: "tool_use", Name: "Read"},
			},
		},
	}

	r.RenderEntry(entry)

	output := buf.String()
	if !strings.Contains(output, "[tool: Read]") {
		t.Errorf("Output should contain tool name, got: %s", output)
	}
}

func TestRendererNoColor(t *testing.T) {
	// Set NO_COLOR environment variable
	origNoColor := os.Getenv("NO_COLOR")
	os.Setenv("NO_COLOR", "1")
	defer os.Setenv("NO_COLOR", origNoColor)

	var buf bytes.Buffer
	r := NewRenderer(&buf)

	entry := &TranscriptEntry{
		Type: MessageTypeUser,
		Message: &Message{
			Content: []ContentBlock{
				{Type: "text", Text: "Test"},
			},
		},
	}

	r.RenderEntry(entry)
	output := buf.String()

	// Should not contain ANSI escape codes
	if strings.Contains(output, "\033[") {
		t.Errorf("Output should not contain ANSI codes when NO_COLOR is set, got: %s", output)
	}
}

func TestRendererTranscript(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)

	transcript := &Transcript{
		Entries: []TranscriptEntry{
			{
				Type: MessageTypeUser,
				Message: &Message{
					Content: []ContentBlock{{Type: "text", Text: "Hello"}},
				},
			},
			{
				Type: MessageTypeAssistant,
				Message: &Message{
					Content: []ContentBlock{{Type: "text", Text: "Hi!"}},
				},
			},
		},
	}

	err := r.RenderTranscript(transcript)
	if err != nil {
		t.Fatalf("RenderTranscript failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "User:") {
		t.Errorf("Output should contain 'User:'")
	}
	if !strings.Contains(output, "Assistant:") {
		t.Errorf("Output should contain 'Assistant:'")
	}
	if !strings.Contains(output, "Hello") {
		t.Errorf("Output should contain user message")
	}
	if !strings.Contains(output, "Hi!") {
		t.Errorf("Output should contain assistant message")
	}
}

func TestRendererToolUseWithBashInput(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)

	input, _ := json.Marshal(map[string]string{
		"command":     "ls -la",
		"description": "List files",
	})

	entry := &TranscriptEntry{
		Type: MessageTypeAssistant,
		Message: &Message{
			Role: "assistant",
			Content: []ContentBlock{
				{Type: "tool_use", Name: "Bash", Input: input},
			},
		},
	}

	r.RenderEntry(entry)

	output := buf.String()
	if !strings.Contains(output, "[tool: Bash]") {
		t.Errorf("Output should contain tool name, got: %s", output)
	}
	if !strings.Contains(output, "command: ls -la") {
		t.Errorf("Output should contain command, got: %s", output)
	}
}

func TestRendererToolUseWithWriteInput(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)

	input, _ := json.Marshal(map[string]string{
		"file_path": "/path/to/file.txt",
		"content":   "Hello, world!\n",
	})

	entry := &TranscriptEntry{
		Type: MessageTypeAssistant,
		Message: &Message{
			Role: "assistant",
			Content: []ContentBlock{
				{Type: "tool_use", Name: "Write", Input: input},
			},
		},
	}

	r.RenderEntry(entry)

	output := buf.String()
	if !strings.Contains(output, "[tool: Write]") {
		t.Errorf("Output should contain tool name, got: %s", output)
	}
	if !strings.Contains(output, "file: /path/to/file.txt") {
		t.Errorf("Output should contain file path, got: %s", output)
	}
	if !strings.Contains(output, "Hello, world!") {
		t.Errorf("Output should contain file content, got: %s", output)
	}
}

func TestRendererToolUseMultilineCommand(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)

	input, _ := json.Marshal(map[string]string{
		"command": "git add file.txt && git commit -m \"$(cat <<'EOF'\nAdd file\n\nCo-Authored-By: Claude\nEOF\n)\"",
	})

	entry := &TranscriptEntry{
		Type: MessageTypeAssistant,
		Message: &Message{
			Role: "assistant",
			Content: []ContentBlock{
				{Type: "tool_use", Name: "Bash", Input: input},
			},
		},
	}

	r.RenderEntry(entry)

	output := buf.String()
	if !strings.Contains(output, "command:") {
		t.Errorf("Output should contain command label, got: %s", output)
	}
	if !strings.Contains(output, "git add file.txt") {
		t.Errorf("Output should contain git command, got: %s", output)
	}
	// Multi-line should be indented
	if !strings.Contains(output, "    ") {
		t.Errorf("Multi-line content should be indented, got: %s", output)
	}
}

func TestRendererToolResultWithContent(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)

	content, _ := json.Marshal("total 0\ndrwxr-xr-x 5 user staff 160 Jan 1 10:00 .")

	entry := &TranscriptEntry{
		Type: MessageTypeUser,
		Message: &Message{
			Role: "user",
			Content: []ContentBlock{
				{Type: "tool_result", ToolUseID: "toolu_123", Content: content},
			},
		},
	}

	r.RenderEntry(entry)

	output := buf.String()
	if !strings.Contains(output, "[tool result]") {
		t.Errorf("Output should contain tool result marker, got: %s", output)
	}
	if !strings.Contains(output, "total 0") {
		t.Errorf("Output should contain result content, got: %s", output)
	}
	if !strings.Contains(output, "drwxr-xr-x") {
		t.Errorf("Output should contain directory listing, got: %s", output)
	}
}

func TestRendererToolResultFileCreation(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)

	content, _ := json.Marshal("File created successfully at: /path/to/file.txt")

	entry := &TranscriptEntry{
		Type: MessageTypeUser,
		Message: &Message{
			Role: "user",
			Content: []ContentBlock{
				{Type: "tool_result", ToolUseID: "toolu_456", Content: content},
			},
		},
	}

	r.RenderEntry(entry)

	output := buf.String()
	if !strings.Contains(output, "File created successfully") {
		t.Errorf("Output should contain file creation message, got: %s", output)
	}
}

func TestRendererSkipsProgressEntries(t *testing.T) {
	var buf bytes.Buffer
	r := NewRenderer(&buf)

	transcript := &Transcript{
		Entries: []TranscriptEntry{
			{
				Type: MessageTypeUser,
				Message: &Message{
					Content: []ContentBlock{{Type: "text", Text: "Hello"}},
				},
			},
			{
				Type: "progress", // Should be skipped
			},
			{
				Type: "file-history-snapshot", // Should be skipped
			},
			{
				Type: MessageTypeAssistant,
				Message: &Message{
					Content: []ContentBlock{{Type: "text", Text: "Hi!"}},
				},
			},
		},
	}

	r.RenderTranscript(transcript)

	output := buf.String()
	// Should have content from user and assistant
	if !strings.Contains(output, "Hello") {
		t.Errorf("Output should contain user message")
	}
	if !strings.Contains(output, "Hi!") {
		t.Errorf("Output should contain assistant message")
	}
	// Should not have excessive blank lines from skipped entries
	if strings.Contains(output, "\n\n\n\n") {
		t.Errorf("Output should not have excessive blank lines from skipped entries")
	}
}
