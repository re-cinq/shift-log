package claude

import (
	"bytes"
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

	err := r.RenderEntry(entry)
	if err != nil {
		t.Fatalf("RenderEntry failed: %v", err)
	}

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

	err := r.RenderEntry(entry)
	if err != nil {
		t.Fatalf("RenderEntry failed: %v", err)
	}

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

	err := r.RenderEntry(entry)
	if err != nil {
		t.Fatalf("RenderEntry failed: %v", err)
	}

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

	err := r.RenderEntry(entry)
	if err != nil {
		t.Fatalf("RenderEntry failed: %v", err)
	}

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
