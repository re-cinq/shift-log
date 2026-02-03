package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// ANSI color codes
const (
	colorReset  = "\033[0m"
	colorBold   = "\033[1m"
	colorDim    = "\033[2m"
	colorBlue   = "\033[34m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorCyan   = "\033[36m"
)

// Renderer renders transcript entries to the terminal
type Renderer struct {
	w        io.Writer
	useColor bool
}

// NewRenderer creates a new terminal renderer
func NewRenderer(w io.Writer) *Renderer {
	// Respect NO_COLOR environment variable
	useColor := os.Getenv("NO_COLOR") == ""
	return &Renderer{w: w, useColor: useColor}
}

// color returns the ANSI code if colors are enabled, empty string otherwise
func (r *Renderer) color(code string) string {
	if r.useColor {
		return code
	}
	return ""
}

// RenderTranscript renders the full transcript to the writer
func (r *Renderer) RenderTranscript(t *Transcript) error {
	return r.RenderEntries(t.Entries)
}

// RenderEntries renders a slice of transcript entries to the writer
func (r *Renderer) RenderEntries(entries []TranscriptEntry) error {
	hadPrevious := false
	for _, entry := range entries {
		if r.shouldRender(&entry) {
			if hadPrevious {
				fmt.Fprintln(r.w)
			}
			r.RenderEntry(&entry)
			hadPrevious = true
		}
	}
	return nil
}

// shouldRender returns true if the entry type should be rendered
func (r *Renderer) shouldRender(entry *TranscriptEntry) bool {
	switch entry.Type {
	case MessageTypeUser, MessageTypeAssistant, MessageTypeSystem:
		return true
	default:
		return false
	}
}

// RenderEntry renders a single transcript entry
func (r *Renderer) RenderEntry(entry *TranscriptEntry) {
	switch entry.Type {
	case MessageTypeUser:
		r.renderUser(entry)
	case MessageTypeAssistant:
		r.renderAssistant(entry)
	case MessageTypeSystem:
		r.renderSystem(entry)
	}
}

func (r *Renderer) renderUser(entry *TranscriptEntry) {
	fmt.Fprintf(r.w, "%s%sUser:%s\n", r.color(colorBold), r.color(colorBlue), r.color(colorReset))
	r.renderMessageContent(entry.Message)
}

func (r *Renderer) renderAssistant(entry *TranscriptEntry) {
	fmt.Fprintf(r.w, "%s%sAssistant:%s\n", r.color(colorBold), r.color(colorGreen), r.color(colorReset))
	r.renderMessageContent(entry.Message)
}

func (r *Renderer) renderSystem(entry *TranscriptEntry) {
	fmt.Fprintf(r.w, "%s%sSystem:%s\n", r.color(colorBold), r.color(colorYellow), r.color(colorReset))
	r.renderMessageContent(entry.Message)
}

func (r *Renderer) renderMessageContent(msg *Message) {
	if msg == nil {
		return
	}

	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			r.renderText(block.Text)
		case "thinking":
			r.renderThinking(block.Thinking)
		case "tool_use":
			r.renderToolUse(block)
		case "tool_result":
			r.renderToolResult(block)
		}
	}
}

func (r *Renderer) renderText(text string) {
	if text == "" {
		return
	}
	// Indent the text for readability
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		fmt.Fprintf(r.w, "  %s\n", line)
	}
}

func (r *Renderer) renderThinking(thinking string) {
	if thinking == "" {
		return
	}
	fmt.Fprintf(r.w, "  %s[thinking]%s\n", r.color(colorDim), r.color(colorReset))
	lines := strings.Split(thinking, "\n")
	// Show just a summary - first few lines
	maxLines := 3
	for i, line := range lines {
		if i >= maxLines {
			fmt.Fprintf(r.w, "  %s... (%d more lines)%s\n", r.color(colorDim), len(lines)-maxLines, r.color(colorReset))
			break
		}
		fmt.Fprintf(r.w, "  %s%s%s\n", r.color(colorDim), line, r.color(colorReset))
	}
}

func (r *Renderer) renderToolUse(block ContentBlock) {
	fmt.Fprintf(r.w, "  %s[tool: %s]%s\n", r.color(colorCyan), block.Name, r.color(colorReset))

	if len(block.Input) == 0 {
		return
	}

	// Parse input based on tool type
	var input map[string]interface{}
	if err := json.Unmarshal(block.Input, &input); err != nil {
		return
	}

	switch block.Name {
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			r.renderToolInput("command", cmd)
		}
	case "Write":
		if path, ok := input["file_path"].(string); ok {
			r.renderToolInput("file", path)
		}
		if content, ok := input["content"].(string); ok {
			r.renderToolInput("content", content)
		}
	case "Read":
		if path, ok := input["file_path"].(string); ok {
			r.renderToolInput("file", path)
		}
	case "Edit":
		if path, ok := input["file_path"].(string); ok {
			r.renderToolInput("file", path)
		}
		if old, ok := input["old_string"].(string); ok {
			r.renderToolInput("old", old)
		}
		if new, ok := input["new_string"].(string); ok {
			r.renderToolInput("new", new)
		}
	case "Grep":
		if pattern, ok := input["pattern"].(string); ok {
			r.renderToolInput("pattern", pattern)
		}
		if path, ok := input["path"].(string); ok {
			r.renderToolInput("path", path)
		}
	case "Glob":
		if pattern, ok := input["pattern"].(string); ok {
			r.renderToolInput("pattern", pattern)
		}
	default:
		// For unknown tools, show all string inputs
		for k, v := range input {
			if s, ok := v.(string); ok && s != "" {
				r.renderToolInput(k, s)
			}
		}
	}
}

func (r *Renderer) renderToolInput(label, value string) {
	lines := strings.Split(value, "\n")
	if len(lines) == 1 {
		// Single line - show inline, truncate if long
		display := value
		if len(display) > 100 {
			display = display[:100] + "..."
		}
		fmt.Fprintf(r.w, "  %s%s: %s%s\n", r.color(colorDim), label, display, r.color(colorReset))
	} else {
		// Multi-line - show indented block
		fmt.Fprintf(r.w, "  %s%s:%s\n", r.color(colorDim), label, r.color(colorReset))
		maxLines := 10
		for i, line := range lines {
			if i >= maxLines {
				fmt.Fprintf(r.w, "    %s... (%d more lines)%s\n", r.color(colorDim), len(lines)-maxLines, r.color(colorReset))
				break
			}
			fmt.Fprintf(r.w, "    %s%s%s\n", r.color(colorDim), line, r.color(colorReset))
		}
	}
}

func (r *Renderer) renderToolResult(block ContentBlock) {
	fmt.Fprintf(r.w, "  %s[tool result]%s\n", r.color(colorDim), r.color(colorReset))

	if len(block.Content) == 0 {
		return
	}

	// Try to unmarshal as a string first (most common case)
	var content string
	if err := json.Unmarshal(block.Content, &content); err == nil {
		r.renderToolResultContent(content)
		return
	}

	// Try as array of content blocks (for complex results)
	var blocks []ContentBlock
	if err := json.Unmarshal(block.Content, &blocks); err == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				r.renderToolResultContent(b.Text)
			}
		}
		return
	}

	// Fallback: show raw content (truncated)
	raw := string(block.Content)
	if len(raw) > 200 {
		raw = raw[:200] + "..."
	}
	fmt.Fprintf(r.w, "  %s%s%s\n", r.color(colorDim), raw, r.color(colorReset))
}

func (r *Renderer) renderToolResultContent(content string) {
	if content == "" {
		return
	}
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		fmt.Fprintf(r.w, "  %s%s%s\n", r.color(colorDim), line, r.color(colorReset))
	}
}
