package codex

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/re-cinq/claudit/internal/agent"
)

func init() {
	agent.Register(&Agent{})
}

// Agent implements the agent.Agent interface for OpenAI Codex CLI.
type Agent struct{}

func (a *Agent) Name() agent.Name   { return agent.Codex }
func (a *Agent) DisplayName() string { return "Codex CLI" }

// ConfigureHooks is a no-op for Codex — it has no per-tool hook mechanism.
// Conversation capture relies on the post-commit git hook.
func (a *Agent) ConfigureHooks(repoRoot string) error {
	return nil
}

// DiagnoseHooks checks that the codex binary is available.
func (a *Agent) DiagnoseHooks(repoRoot string) []agent.DiagnosticCheck {
	var checks []agent.DiagnosticCheck

	if _, err := LookupBinary(); err == nil {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "Codex binary",
			OK:      true,
			Message: "Found codex in PATH",
		})
	} else {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "Codex binary",
			OK:      false,
			Message: "codex not found in PATH. Install from https://github.com/openai/codex",
		})
	}

	return checks
}

// ParseHookInput parses Codex's hook JSON from the post-commit hook.
// Codex doesn't have per-tool hooks, so this parses the manual store format.
func (a *Agent) ParseHookInput(raw []byte) (*agent.HookData, error) {
	var hook struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		ToolName       string `json:"tool_name"`
		ToolInput      struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(raw, &hook); err != nil {
		return nil, err
	}

	return &agent.HookData{
		SessionID:      hook.SessionID,
		TranscriptPath: hook.TranscriptPath,
		ToolName:       hook.ToolName,
		Command:        hook.ToolInput.Command,
	}, nil
}

// IsCommitCommand checks if a tool invocation represents a git commit.
func (a *Agent) IsCommitCommand(toolName, command string) bool {
	shellTools := map[string]bool{
		"shell":          true,
		"container.exec": true,
		"shell_command":  true,
	}

	if !shellTools[toolName] {
		return false
	}
	return agent.IsGitCommitCommand(command)
}

// rolloutLine represents a single line in a Codex rollout JSONL file.
type rolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// responseItem represents a response_item payload.
type responseItem struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Content   json.RawMessage `json:"content"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	CallID    string          `json:"call_id"`
	Output    string          `json:"output"`
}

// contentPart represents a content part within a response_item message.
type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ParseTranscript parses a Codex CLI rollout JSONL transcript.
func (a *Agent) ParseTranscript(r io.Reader) (*agent.Transcript, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	var entries []agent.TranscriptEntry

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var rl rolloutLine
		if err := json.Unmarshal([]byte(line), &rl); err != nil {
			continue
		}

		if rl.Type != "response_item" {
			continue
		}

		var item responseItem
		if err := json.Unmarshal(rl.Payload, &item); err != nil {
			continue
		}

		entry := a.parseResponseItem(item, rl.Timestamp, []byte(line))
		if entry.Type != "" {
			entries = append(entries, entry)
		}
	}

	return &agent.Transcript{Entries: entries}, nil
}

// parseResponseItem converts a Codex response_item into a TranscriptEntry.
func (a *Agent) parseResponseItem(item responseItem, timestamp string, rawLine []byte) agent.TranscriptEntry {
	entry := agent.TranscriptEntry{
		Timestamp: timestamp,
		Raw:       json.RawMessage(append([]byte{}, rawLine...)),
	}

	switch item.Type {
	case "message":
		entry.Type = normalizeCodexRole(item.Role)
		entry.Message = parseCodexMessage(item)

	case "function_call":
		entry.Type = agent.MessageTypeAssistant
		entry.Message = &agent.Message{
			Role: "assistant",
			Content: []agent.ContentBlock{{
				Type:      "tool_use",
				ToolUseID: item.CallID,
				Text:      item.Name,
				Input:     json.RawMessage(item.Arguments),
			}},
		}

	case "function_call_output":
		entry.Type = agent.MessageTypeUser
		entry.Message = &agent.Message{
			Role: "user",
			Content: []agent.ContentBlock{{
				Type:      "tool_result",
				ToolUseID: item.CallID,
				Content:   json.RawMessage(`"` + strings.ReplaceAll(item.Output, `"`, `\"`) + `"`),
			}},
		}
	}

	return entry
}

// parseCodexMessage extracts message content from a response_item.
func parseCodexMessage(item responseItem) *agent.Message {
	msg := &agent.Message{}
	switch item.Role {
	case "user":
		msg.Role = "user"
	case "assistant":
		msg.Role = "assistant"
	default:
		msg.Role = item.Role
	}

	// Parse content array
	var parts []contentPart
	if err := json.Unmarshal(item.Content, &parts); err == nil {
		for _, p := range parts {
			switch p.Type {
			case "input_text", "output_text":
				msg.Content = append(msg.Content, agent.ContentBlock{
					Type: "text",
					Text: p.Text,
				})
			}
		}
		return msg
	}

	// Try as plain string
	var text string
	if err := json.Unmarshal(item.Content, &text); err == nil && text != "" {
		msg.Content = []agent.ContentBlock{{Type: "text", Text: text}}
	}

	return msg
}

// normalizeCodexRole converts Codex roles to MessageType.
func normalizeCodexRole(role string) agent.MessageType {
	switch role {
	case "user":
		return agent.MessageTypeUser
	case "assistant":
		return agent.MessageTypeAssistant
	case "system":
		return agent.MessageTypeSystem
	default:
		return ""
	}
}

// ParseTranscriptFile parses a Codex rollout JSONL file.
func (a *Agent) ParseTranscriptFile(path string) (*agent.Transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return a.ParseTranscript(f)
}

// DiscoverSession finds an active or recent Codex CLI session.
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	recentTimeout := agent.RecentSessionTimeout

	rolloutPath, sessionID, err := FindRecentRollout(projectPath, recentTimeout)
	if err != nil {
		return nil, nil
	}
	if rolloutPath == "" {
		return nil, nil
	}

	info, err := os.Stat(rolloutPath)
	if err != nil {
		return nil, nil
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: rolloutPath,
		StartedAt:      info.ModTime().Format(time.RFC3339),
		ProjectPath:    projectPath,
	}, nil
}

// RestoreSession writes a session to the Codex sessions directory.
func (a *Agent) RestoreSession(projectPath, sessionID, gitBranch string,
	transcriptData []byte, messageCount int, summary string) error {

	_, err := WriteSessionFile(sessionID, transcriptData)
	return err
}

// ResumeCommand returns the command to resume a Codex CLI session.
func (a *Agent) ResumeCommand(sessionID string) (string, []string) {
	return "codex", []string{"resume", sessionID}
}

// ToolAliases returns Codex's tool name mappings to canonical names.
func (a *Agent) ToolAliases() map[string]string {
	return map[string]string{
		"shell":          "Bash",
		"container.exec": "Bash",
		"shell_command":  "Bash",
	}
}

// LookupBinary checks if the codex binary is in PATH.
func LookupBinary() (string, error) {
	return exec.LookPath("codex")
}
