package copilot

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/re-cinq/claudit/internal/agent"
)

func init() {
	agent.Register(&Agent{})
}

// Agent implements the agent.Agent interface for GitHub Copilot CLI.
type Agent struct{}

func (a *Agent) Name() agent.Name   { return agent.Copilot }
func (a *Agent) DisplayName() string { return "Copilot CLI" }

// ConfigureHooks sets up Copilot CLI hooks in hooks.json at the repo root.
func (a *Agent) ConfigureHooks(repoRoot string) error {
	hf, err := ReadHooksFile(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to read Copilot hooks: %w", err)
	}

	AddClauditHooks(hf)

	if err := WriteHooksFile(repoRoot, hf); err != nil {
		return fmt.Errorf("failed to write Copilot hooks: %w", err)
	}
	return nil
}

// DiagnoseHooks validates Copilot CLI hook configuration.
func (a *Agent) DiagnoseHooks(repoRoot string) []agent.DiagnosticCheck {
	var checks []agent.DiagnosticCheck

	hooksPath := hooksFilePath(repoRoot)
	data, err := os.ReadFile(hooksPath)
	if err != nil {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "Copilot CLI hook configuration",
			OK:      false,
			Message: "No hooks.json found. Run 'claudit init --agent=copilot' to configure.",
		})
		return checks
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "Copilot CLI hook configuration",
			OK:      false,
			Message: fmt.Sprintf("Invalid JSON in hooks.json: %v", err),
		})
		return checks
	}

	hooks, hasHooks := raw["hooks"].(map[string]interface{})
	if !hasHooks {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "Copilot CLI hooks",
			OK:      false,
			Message: "Missing 'hooks' key in hooks.json. Run 'claudit init --agent=copilot' to fix.",
		})
		return checks
	}

	postToolUse, hasPostToolUse := hooks["postToolUse"]
	if !hasPostToolUse || !hasClauditBashEntry(postToolUse, "claudit store") {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "postToolUse hook",
			OK:      false,
			Message: "'claudit store' hook not found in postToolUse. Run 'claudit init --agent=copilot' to fix.",
		})
	} else {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "postToolUse hook",
			OK:      true,
			Message: "Found postToolUse hook configuration",
		})
	}

	return checks
}

// ParseHookInput parses Copilot CLI's postToolUse hook JSON.
// Supports two formats:
//   - Copilot native: {"timestamp":N, "cwd":"...", "toolName":"...", "toolArgs":"..."}
//   - Generic (shared test path): {"session_id":"...", "transcript_path":"...", "tool_name":"...", "tool_input":{"command":"..."}}
func (a *Agent) ParseHookInput(raw []byte) (*agent.HookData, error) {
	var hook struct {
		// Generic format fields
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		GenericToolName string `json:"tool_name"`
		ToolInput      struct {
			Command string `json:"command"`
		} `json:"tool_input"`

		// Copilot native format fields
		Timestamp int64  `json:"timestamp"`
		CWD       string `json:"cwd"`
		ToolName  string `json:"toolName"`
		ToolArgs  string `json:"toolArgs"`
	}
	if err := json.Unmarshal(raw, &hook); err != nil {
		return nil, err
	}

	// Determine tool name: prefer Copilot-native field, fall back to generic
	toolName := hook.ToolName
	if toolName == "" {
		toolName = hook.GenericToolName
	}

	// Determine command: prefer generic tool_input.command, fall back to native toolArgs extraction
	command := hook.ToolInput.Command
	if command == "" && hook.ToolArgs != "" {
		command = extractCommand(toolName, hook.ToolArgs)
	}

	sessionID := hook.SessionID
	transcriptPath := hook.TranscriptPath

	// If no session info from generic fields, try CWD-based discovery
	if sessionID == "" && hook.CWD != "" {
		si, err := scanForRecentSession(hook.CWD)
		if err == nil && si != nil {
			sessionID = si.SessionID
			transcriptPath = si.TranscriptPath
		}
	}

	return &agent.HookData{
		SessionID:      sessionID,
		TranscriptPath: transcriptPath,
		ToolName:       toolName,
		Command:        command,
	}, nil
}

// shellToolNames are the known tool names Copilot CLI uses for shell execution.
var shellToolNames = map[string]bool{
	"shell_run": true,
	"bash":      true,
}

// IsCommitCommand checks if a tool invocation represents a git commit.
func (a *Agent) IsCommitCommand(toolName, command string) bool {
	if !shellToolNames[toolName] {
		return false
	}
	return strings.Contains(command, "git commit") ||
		strings.Contains(command, "git-commit")
}

// copilotSession represents the top-level JSON structure of a Copilot CLI session file.
type copilotSession struct {
	SessionID string           `json:"sessionId"`
	CWD       string           `json:"cwd"`
	Model     string           `json:"model"`
	Messages  []copilotMessage `json:"messages"`
}

// copilotMessage represents a single message in a Copilot session.
type copilotMessage struct {
	Role       string             `json:"role"`
	Content    string             `json:"content,omitempty"`
	ToolCalls  []copilotToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
}

// copilotToolCall represents a tool call in a Copilot message.
type copilotToolCall struct {
	ID       string              `json:"id"`
	Type     string              `json:"type"`
	Function copilotFunctionCall `json:"function"`
}

// copilotFunctionCall represents a function call within a tool call.
type copilotFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ParseTranscript parses a Copilot CLI session JSON transcript.
func (a *Agent) ParseTranscript(r io.Reader) (*agent.Transcript, error) {
	return parseCopilotTranscript(r)
}

// ParseTranscriptFile parses a Copilot CLI session JSON transcript from a file.
func (a *Agent) ParseTranscriptFile(path string) (*agent.Transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parseCopilotTranscript(f)
}

// DiscoverSession finds an active or recent Copilot CLI session.
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	return scanForRecentSession(projectPath)
}

// RestoreSession writes a transcript to Copilot CLI's expected location.
func (a *Agent) RestoreSession(projectPath, sessionID, gitBranch string,
	transcriptData []byte, messageCount int, summary string) error {
	_, err := WriteSessionFile(sessionID, transcriptData)
	return err
}

// ResumeCommand returns the command to resume a Copilot CLI session.
func (a *Agent) ResumeCommand(sessionID string) (string, []string) {
	return "copilot", []string{"--resume", sessionID}
}

// ToolAliases returns Copilot CLI's tool name mappings to canonical names.
func (a *Agent) ToolAliases() map[string]string {
	return map[string]string{
		"shell_run": "Bash",
		"bash":      "Bash",
		"view":      "Read",
		"edit":      "Edit",
		"write":     "Write",
	}
}

// parseCopilotTranscript parses a Copilot CLI session JSON transcript.
func parseCopilotTranscript(r io.Reader) (*agent.Transcript, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return &agent.Transcript{}, nil
	}

	var session copilotSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse Copilot session JSON: %w", err)
	}

	var entries []agent.TranscriptEntry
	for i, msg := range session.Messages {
		msgType := normalizeCopilotRole(msg.Role)
		if msgType == "" {
			continue
		}

		var content []agent.ContentBlock

		if msg.Content != "" {
			content = append(content, agent.ContentBlock{
				Type: "text",
				Text: msg.Content,
			})
		}

		for _, tc := range msg.ToolCalls {
			content = append(content, agent.ContentBlock{
				Type:      "tool_use",
				ToolUseID: tc.ID,
				Name:      tc.Function.Name,
				Input:     json.RawMessage(tc.Function.Arguments),
			})
		}

		if msg.ToolCallID != "" {
			content = append(content, agent.ContentBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
				Content:   json.RawMessage(`"` + strings.ReplaceAll(msg.Content, `"`, `\"`) + `"`),
			})
		}

		if len(content) == 0 {
			content = []agent.ContentBlock{{Type: "text", Text: ""}}
		}

		rawBytes, _ := json.Marshal(msg)

		entry := agent.TranscriptEntry{
			UUID: fmt.Sprintf("copilot-%d", i),
			Type: msgType,
			Message: &agent.Message{
				Role:    string(msgType),
				Content: content,
			},
			Raw: rawBytes,
		}

		entries = append(entries, entry)
	}

	return &agent.Transcript{Entries: entries}, nil
}

// normalizeCopilotRole converts Copilot message roles to the common MessageType.
func normalizeCopilotRole(role string) agent.MessageType {
	switch role {
	case "user":
		return agent.MessageTypeUser
	case "assistant", "copilot":
		return agent.MessageTypeAssistant
	case "system":
		return agent.MessageTypeSystem
	case "tool":
		return agent.MessageTypeUser
	default:
		return ""
	}
}

// extractCommand extracts the shell command from toolArgs JSON.
// toolArgs is a JSON string containing the tool's arguments.
func extractCommand(toolName, toolArgs string) string {
	if !shellToolNames[toolName] {
		return ""
	}
	if toolArgs == "" {
		return ""
	}

	var args map[string]interface{}
	if err := json.Unmarshal([]byte(toolArgs), &args); err != nil {
		return toolArgs
	}

	if cmd, ok := args["command"].(string); ok {
		return cmd
	}
	if cmd, ok := args["cmd"].(string); ok {
		return cmd
	}

	return ""
}

// scanForRecentSession scans Copilot's session state directory for recent files.
func scanForRecentSession(projectPath string) (*agent.SessionInfo, error) {
	sessionDir, err := GetSessionStateDir()
	if err != nil {
		return nil, nil
	}

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, nil
	}

	now := time.Now()
	const recentTimeout = 5 * time.Minute
	var bestPath string
	var bestSessionID string
	var bestModTime time.Time

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		modTime := info.ModTime()
		if now.Sub(modTime) > recentTimeout {
			continue
		}

		// Check if this session matches the project path
		meta, err := parseSessionMeta(filepath.Join(sessionDir, entry.Name()))
		if err != nil || meta == nil {
			continue
		}

		if !pathsEqual(meta.CWD, projectPath) {
			continue
		}

		if bestPath == "" || modTime.After(bestModTime) {
			bestPath = filepath.Join(sessionDir, entry.Name())
			bestSessionID = meta.SessionID
			bestModTime = modTime
		}
	}

	if bestPath == "" {
		return nil, nil
	}

	return &agent.SessionInfo{
		SessionID:      bestSessionID,
		TranscriptPath: bestPath,
		StartedAt:      bestModTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
	}, nil
}

// hasClauditBashEntry checks if a hook list contains a claudit bash entry.
func hasClauditBashEntry(hookConfig interface{}, command string) bool {
	hookList, ok := hookConfig.([]interface{})
	if !ok {
		return false
	}
	for _, h := range hookList {
		hookMap, _ := h.(map[string]interface{})
		if bash, ok := hookMap["bash"].(string); ok {
			if strings.Contains(bash, command) {
				return true
			}
		}
	}
	return false
}

// LookupBinary checks if the copilot binary is in PATH.
func LookupBinary() (string, error) {
	return exec.LookPath("copilot")
}
