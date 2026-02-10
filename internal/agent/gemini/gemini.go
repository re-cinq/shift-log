package gemini

import (
	"bufio"
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
	"shell":              true,
	"shell_exec":         true,
	"run_in_terminal":    true,
	"execute_command":    true,
	"run_shell_command":  true,
}

// IsCommitCommand checks if a tool invocation represents a git commit.
func (a *Agent) IsCommitCommand(toolName, command string) bool {
	if !shellToolNames[toolName] {
		return false
	}
	return strings.Contains(command, "git commit") ||
		strings.Contains(command, "git-commit")
}

// ParseTranscript parses a Gemini CLI JSONL transcript.
func (a *Agent) ParseTranscript(r io.Reader) (*agent.Transcript, error) {
	return ParseJSONLTranscript(r)
}

// ParseTranscriptFile parses a Gemini CLI JSONL transcript from a file.
func (a *Agent) ParseTranscriptFile(path string) (*agent.Transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseJSONLTranscript(f)
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
		"shell":             "Bash",
		"shell_exec":        "Bash",
		"run_in_terminal":   "Bash",
		"execute_command":   "Bash",
		"run_shell_command": "Bash",
		"write_file":        "Write",
		"read_file":         "Read",
		"edit_file":         "Edit",
		"search_files":      "Grep",
		"list_files":        "Glob",
	}
}

// ParseJSONLTranscript parses a Gemini CLI JSONL transcript.
// Gemini uses type "gemini" or "model" for assistant messages.
func ParseJSONLTranscript(r io.Reader) (*agent.Transcript, error) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var entries []agent.TranscriptEntry

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			continue
		}

		entry := agent.TranscriptEntry{
			Raw: json.RawMessage(append([]byte{}, line...)),
		}

		// Parse type field
		if typeRaw, ok := raw["type"]; ok {
			var t string
			if err := json.Unmarshal(typeRaw, &t); err == nil {
				entry.Type = normalizeGeminiType(t)
			}
		}

		// Parse id field (Gemini uses "id" where Claude uses "uuid")
		if idRaw, ok := raw["id"]; ok {
			var id string
			if err := json.Unmarshal(idRaw, &id); err == nil {
				entry.UUID = id
			}
		}
		// Also try "uuid" for compatibility
		if entry.UUID == "" {
			if uuidRaw, ok := raw["uuid"]; ok {
				var uuid string
				if err := json.Unmarshal(uuidRaw, &uuid); err == nil {
					entry.UUID = uuid
				}
			}
		}

		// Parse timestamp
		if tsRaw, ok := raw["timestamp"]; ok {
			var ts string
			if err := json.Unmarshal(tsRaw, &ts); err == nil {
				entry.Timestamp = ts
			}
		}

		// Parse message/content
		entry.Message = parseGeminiMessage(raw, entry.Type)

		// Skip metadata-only entries (session_metadata, message_update, etc.)
		if entry.Type == "" {
			continue
		}

		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
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

// parseGeminiMessage parses message content from a Gemini JSONL entry.
func parseGeminiMessage(raw map[string]json.RawMessage, msgType agent.MessageType) *agent.Message {
	msg := &agent.Message{}

	switch msgType {
	case agent.MessageTypeUser:
		msg.Role = "user"
	case agent.MessageTypeAssistant:
		msg.Role = "assistant"
	case agent.MessageTypeSystem:
		msg.Role = "system"
	default:
		return nil
	}

	// Try "content" as array of objects with "text" fields
	if contentRaw, ok := raw["content"]; ok {
		var contentArr []struct {
			Text string `json:"text"`
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(contentRaw, &contentArr); err == nil {
			for _, c := range contentArr {
				blockType := c.Type
				if blockType == "" {
					blockType = "text"
				}
				msg.Content = append(msg.Content, agent.ContentBlock{
					Type: blockType,
					Text: c.Text,
					Name: c.Name,
				})
			}
			return msg
		}

		// Try as a plain string
		var text string
		if err := json.Unmarshal(contentRaw, &text); err == nil && text != "" {
			msg.Content = []agent.ContentBlock{{Type: "text", Text: text}}
			return msg
		}
	}

	// Try "message" field (Claude-compatible format)
	if msgRaw, ok := raw["message"]; ok {
		var innerMsg agent.Message
		if err := json.Unmarshal(msgRaw, &innerMsg); err == nil {
			return &innerMsg
		}
	}

	// If we have a type but no content, return an empty message
	if msgType != "" {
		return msg
	}

	return nil
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
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
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
			bestSessionID = strings.TrimSuffix(entry.Name(), ".jsonl")
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
