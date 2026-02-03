package claude

import (
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
	for i, entry := range t.Entries {
		if i > 0 {
			fmt.Fprintln(r.w)
		}
		if err := r.RenderEntry(&entry); err != nil {
			return err
		}
	}
	return nil
}

// RenderEntry renders a single transcript entry
func (r *Renderer) RenderEntry(entry *TranscriptEntry) error {
	switch entry.Type {
	case MessageTypeUser:
		r.renderUser(entry)
	case MessageTypeAssistant:
		r.renderAssistant(entry)
	case MessageTypeSystem:
		r.renderSystem(entry)
	default:
		// Skip unknown types
	}
	return nil
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
}

func (r *Renderer) renderToolResult(block ContentBlock) {
	fmt.Fprintf(r.w, "  %s[tool result]%s\n", r.color(colorDim), r.color(colorReset))
}
