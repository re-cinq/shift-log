package gemini

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

// Agent implements the agent.Agent interface for Gemini CLI.
type Agent struct{}

func (a *Agent) Name() agent.Name   { return agent.Gemini }
func (a *Agent) DisplayName() string { return "Gemini CLI" }

// ConfigureHooks sets up Gemini CLI hooks in .gemini/settings.json.
func (a *Agent) ConfigureHooks(repoRoot string) error {
	geminiDir := filepath.Join(repoRoot, ".gemini")
	settings, err := ReadSettings(geminiDir)
	if err != nil {
		return fmt.Errorf("failed to read Gemini settings: %w", err)
	}

	AddClauditHook(settings)
	AddSessionHooks(settings)

	if err := WriteSettings(geminiDir, settings); err != nil {
		return fmt.Errorf("failed to write Gemini settings: %w", err)
	}
	return nil
}

// DiagnoseHooks validates Gemini CLI hook configuration.
func (a *Agent) DiagnoseHooks(repoRoot string) []agent.DiagnosticCheck {
	var checks []agent.DiagnosticCheck

	settingsPath := filepath.Join(repoRoot, ".gemini", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "Gemini CLI hook configuration",
			OK:      false,
			Message: "No .gemini/settings.json found. Run 'claudit init --agent=gemini' to configure.",
		})
		return checks
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "Gemini CLI hook configuration",
			OK:      false,
			Message: fmt.Sprintf("Invalid JSON in settings file: %v", err),
		})
		return checks
	}

	hooks, hasHooks := settings["hooks"].(map[string]interface{})
	if !hasHooks {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "Gemini CLI hooks",
			OK:      false,
			Message: "Missing 'hooks' key in settings. Run 'claudit init --agent=gemini' to fix.",
		})
		return checks
	}

	afterTool, hasAfterTool := hooks["AfterTool"]
	if !hasAfterTool || !hasClauditCommand(afterTool, "claudit store") {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "AfterTool hook",
			OK:      false,
			Message: "'claudit store' hook not found in AfterTool. Run 'claudit init --agent=gemini' to fix.",
		})
	} else {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "AfterTool hook",
			OK:      true,
			Message: "Found AfterTool hook configuration",
		})
	}

	return checks
}

// ParseHookInput parses Gemini CLI's AfterTool hook JSON.
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

// shellToolNames are the known tool names Gemini CLI uses for shell execution.
var shellToolNames = map[string]bool{
	"run_shell_command": true,
}

// IsCommitCommand checks if a tool invocation represents a git commit.
func (a *Agent) IsCommitCommand(toolName, command string) bool {
	if !shellToolNames[toolName] {
		return false
	}
	return strings.Contains(command, "git commit") ||
		strings.Contains(command, "git-commit")
}

// ParseTranscript parses a Gemini CLI session JSON transcript.
func (a *Agent) ParseTranscript(r io.Reader) (*agent.Transcript, error) {
	return ParseGeminiTranscript(r)
}

// ParseTranscriptFile parses a Gemini CLI session JSON transcript from a file.
func (a *Agent) ParseTranscriptFile(path string) (*agent.Transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseGeminiTranscript(f)
}

// DiscoverSession finds an active or recent Gemini CLI session.
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	return scanForRecentSession(projectPath)
}

// RestoreSession writes a transcript to Gemini CLI's expected location.
func (a *Agent) RestoreSession(projectPath, sessionID, gitBranch string,
	transcriptData []byte, messageCount int, summary string) error {

	sessionPath, err := WriteSessionFile(projectPath, sessionID, transcriptData)
	if err != nil {
		return err
	}

	index, err := ReadSessionsIndex(projectPath)
	if err != nil {
		return fmt.Errorf("could not read sessions index: %w", err)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	entry := SessionEntry{
		SessionID:    sessionID,
		FullPath:     sessionPath,
		MessageCount: messageCount,
		Created:      now,
		Modified:     now,
		ProjectPath:  projectPath,
	}

	AddOrUpdateSessionEntry(index, entry)

	if err := WriteSessionsIndex(projectPath, index); err != nil {
		return fmt.Errorf("could not write sessions index: %w", err)
	}

	return nil
}

// ResumeCommand returns the command to resume a Gemini CLI session.
func (a *Agent) ResumeCommand(sessionID string) (string, []string) {
	return "gemini", []string{"--resume", sessionID}
}

// ToolAliases returns Gemini CLI's tool name mappings to canonical names.
func (a *Agent) ToolAliases() map[string]string {
	return map[string]string{
		"run_shell_command": "Bash",
		"replace":          "Edit",
		"grep_search":      "Grep",
		"glob":             "Glob",
		"list_directory":   "Glob",
		"google_web_search": "WebSearch",
		"web_fetch":        "WebFetch",
	}
}

// geminiSession represents the top-level JSON structure of a Gemini CLI session file.
type geminiSession struct {
	Messages []geminiMessage `json:"messages"`
}

// geminiMessage represents a single message in a Gemini session.
type geminiMessage struct {
	Role      string           `json:"role"`
	Parts     []geminiPart     `json:"parts,omitempty"`
	ToolCalls []geminiToolCall `json:"toolCalls,omitempty"`
}

// geminiPart represents a part of a Gemini message.
type geminiPart struct {
	Text         string          `json:"text,omitempty"`
	FunctionCall *geminiFuncCall `json:"functionCall,omitempty"`
}

// geminiFuncCall represents a function call within a part.
type geminiFuncCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args,omitempty"`
}

// geminiToolCall represents a tool call in a Gemini message.
type geminiToolCall struct {
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input,omitempty"`
}

// ParseGeminiTranscript parses a Gemini CLI session JSON transcript.
// Gemini sessions are stored as a single JSON object with a "messages" array.
func ParseGeminiTranscript(r io.Reader) (*agent.Transcript, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	if len(data) == 0 {
		return &agent.Transcript{}, nil
	}

	var session geminiSession
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini session JSON: %w", err)
	}

	var entries []agent.TranscriptEntry
	for i, msg := range session.Messages {
		msgType := normalizeGeminiType(msg.Role)
		if msgType == "" {
			continue
		}

		// Build content blocks from parts
		var content []agent.ContentBlock
		for _, part := range msg.Parts {
			if part.Text != "" {
				content = append(content, agent.ContentBlock{
					Type: "text",
					Text: part.Text,
				})
			}
			if part.FunctionCall != nil {
				inputJSON, _ := json.Marshal(part.FunctionCall.Args)
				content = append(content, agent.ContentBlock{
					Type:  "tool_use",
					Name:  part.FunctionCall.Name,
					Input: inputJSON,
				})
			}
		}

		// Also handle toolCalls field
		for _, tc := range msg.ToolCalls {
			inputJSON, _ := json.Marshal(tc.Input)
			content = append(content, agent.ContentBlock{
				Type:  "tool_use",
				Name:  tc.Name,
				Input: inputJSON,
			})
		}

		if len(content) == 0 {
			content = []agent.ContentBlock{{Type: "text", Text: ""}}
		}

		rawBytes, _ := json.Marshal(msg)

		entry := agent.TranscriptEntry{
			UUID: fmt.Sprintf("gemini-%d", i),
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

// normalizeGeminiType converts Gemini message types to the common MessageType.
func normalizeGeminiType(t string) agent.MessageType {
	switch t {
	case "user":
		return agent.MessageTypeUser
	case "gemini", "model", "assistant":
		return agent.MessageTypeAssistant
	case "system":
		return agent.MessageTypeSystem
	default:
		return ""
	}
}

// scanForRecentSession scans Gemini's session directory for recent files.
func scanForRecentSession(projectPath string) (*agent.SessionInfo, error) {
	sessionDir, err := GetSessionDir(projectPath)
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
		// Skip index files
		if entry.Name() == "sessions-index.json" {
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

		if bestPath == "" || modTime.After(bestModTime) {
			bestPath = filepath.Join(sessionDir, entry.Name())
			bestSessionID = strings.TrimSuffix(entry.Name(), ".json")
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

// hasClauditCommand checks if a hook list contains a specific claudit command.
func hasClauditCommand(hookConfig interface{}, command string) bool {
	hookList, ok := hookConfig.([]interface{})
	if !ok {
		return false
	}
	for _, h := range hookList {
		hookMap, _ := h.(map[string]interface{})
		hookCmds, _ := hookMap["hooks"].([]interface{})
		for _, hc := range hookCmds {
			hcMap, _ := hc.(map[string]interface{})
			if cmd, ok := hcMap["command"].(string); ok {
				if strings.Contains(cmd, command) {
					return true
				}
			}
		}
	}
	return false
}

// LookupBinary checks if the gemini binary is in PATH.
func LookupBinary() (string, error) {
	return exec.LookPath("gemini")
}
