package opencode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/re-cinq/shift-log/internal/agent"
)

func init() {
	agent.Register(&Agent{})
}

// Agent implements the agent.Agent interface for OpenCode CLI.
type Agent struct{}

func (a *Agent) Name() agent.Name   { return agent.OpenCode }
func (a *Agent) DisplayName() string { return "OpenCode CLI" }

// ConfigureHooks installs the shiftlog plugin for OpenCode.
func (a *Agent) ConfigureHooks(repoRoot string) error {
	return InstallPlugin(repoRoot)
}

// RemoveHooks removes the shiftlog plugin for OpenCode.
func (a *Agent) RemoveHooks(repoRoot string) error {
	return RemovePlugin(repoRoot)
}

// DiagnoseHooks validates OpenCode plugin installation.
func (a *Agent) DiagnoseHooks(repoRoot string) []agent.DiagnosticCheck {
	var checks []agent.DiagnosticCheck

	if HasPlugin(repoRoot) {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "OpenCode plugin",
			OK:      true,
			Message: "Found .opencode/plugins/shiftlog.js",
		})
	} else {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "OpenCode plugin",
			OK:      false,
			Message: "Missing .opencode/plugins/shiftlog.js. Run 'shiftlog init --agent=opencode' to install.",
		})
	}

	return checks
}

// ParseHookInput parses OpenCode's plugin hook JSON.
func (a *Agent) ParseHookInput(raw []byte) (*agent.HookData, error) {
	var hook struct {
		SessionID      string `json:"session_id"`
		DataDir        string `json:"data_dir"`
		ProjectDir     string `json:"project_dir"`
		ToolName       string `json:"tool_name"`
		TranscriptData string `json:"transcript_data"`
		ToolInput      struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(raw, &hook); err != nil {
		return nil, err
	}

	// For OpenCode, we don't have a single transcript path.
	// Instead, we reconstruct from the data directory and session ID.
	transcriptPath := ""
	if hook.DataDir != "" && hook.SessionID != "" {
		transcriptPath = filepath.Join(hook.DataDir, "storage", "message", hook.SessionID)
	}

	// Use inline transcript data from the plugin SDK client if available
	var transcriptData []byte
	if hook.TranscriptData != "" {
		transcriptData = []byte(hook.TranscriptData)
	}

	return &agent.HookData{
		SessionID:      hook.SessionID,
		TranscriptPath: transcriptPath,
		ToolName:       hook.ToolName,
		Command:        hook.ToolInput.Command,
		TranscriptData: transcriptData,
	}, nil
}

// IsCommitCommand checks if a tool invocation represents a git commit.
func (a *Agent) IsCommitCommand(toolName, command string) bool {
	// OpenCode tool names for shell execution
	shellTools := map[string]bool{
		"bash":               true,
		"shell":              true,
		"terminal":           true,
		"execute":            true,
		"run":                true,
		"command":            true,
	}

	if !shellTools[toolName] {
		return false
	}
	return agent.IsGitCommitCommand(command)
}

// ParseTranscript parses an OpenCode transcript.
// OpenCode stores messages as individual JSON files, but we also handle
// the case where a single combined file is provided (e.g., during restore).
func (a *Agent) ParseTranscript(r io.Reader) (*agent.Transcript, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	var entries []agent.TranscriptEntry

	// If data starts with '[', try JSON array first
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") {
		var messages []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &messages); err == nil {
			for _, msgData := range messages {
				var raw map[string]json.RawMessage
				if err := json.Unmarshal(msgData, &raw); err == nil {
					entry := parseOpenCodeEntry(raw, msgData)
					if entry.Type != "" {
						entries = append(entries, entry)
					}
				}
			}
			t := &agent.Transcript{Entries: entries}
			t.Turns = t.CountTurns()
			return t, nil
		}
	}

	// Try as JSONL (for restored transcripts and compatibility)
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}

		entry := parseOpenCodeEntry(raw, []byte(line))
		if entry.Type != "" {
			entries = append(entries, entry)
		}
	}

	t := &agent.Transcript{Entries: entries}
	t.Turns = t.CountTurns()
	return t, nil
}

// ParseTranscriptFile parses an OpenCode session from the message directory.
func (a *Agent) ParseTranscriptFile(path string) (*agent.Transcript, error) {
	// Check if path is a directory (message directory)
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if info.IsDir() {
		return a.parseMessageDir(path)
	}

	// Otherwise, treat as a single file
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return a.ParseTranscript(f)
}

// parseMessageDir reads all message files from an OpenCode message directory.
func (a *Agent) parseMessageDir(dir string) (*agent.Transcript, error) {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var entries []agent.TranscriptEntry
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".json") {
			// Handle .jsonl files too
			if strings.HasSuffix(de.Name(), ".jsonl") {
				f, err := os.Open(filepath.Join(dir, de.Name()))
				if err != nil {
					continue
				}
				transcript, err := a.ParseTranscript(f)
				_ = f.Close()
				if err == nil {
					entries = append(entries, transcript.Entries...)
				}
			}
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, de.Name()))
		if err != nil {
			continue
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			continue
		}

		entry := parseOpenCodeEntry(raw, data)
		if entry.Type != "" {
			entries = append(entries, entry)
		}
	}

	return &agent.Transcript{Entries: entries}, nil
}

// DiscoverSession finds an active or recent OpenCode session.
// It tries flat file storage (pre-v1.2), then session_diff (v1.15+),
// then falls back to SQLite (v1.2+).
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// Try flat file storage first (pre-v1.2 OpenCode)
	session, err := a.discoverFromFlatFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if session != nil {
		return session, nil
	}

	// Try session_diff storage (OpenCode v1.15+)
	session, err = a.discoverFromSessionDiff(projectPath)
	if err == nil && session != nil {
		return session, nil
	}

	// Fall back to SQLite (OpenCode v1.2+)
	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}

	projectID := GetProjectID(projectPath)
	return discoverFromSQLite(dataDir, projectID, projectPath)
}

// discoverFromFlatFiles tries the legacy flat file session discovery.
func (a *Agent) discoverFromFlatFiles(projectPath string) (*agent.SessionInfo, error) {
	sessionDir, err := GetSessionDir(projectPath)
	if err != nil {
		return nil, nil
	}

	dirEntries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, nil
	}

	now := time.Now()
	recentTimeout := agent.RecentSessionTimeout
	var bestSessionID string
	var bestModTime time.Time

	for _, entry := range dirEntries {
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

		if bestSessionID == "" || modTime.After(bestModTime) {
			bestSessionID = strings.TrimSuffix(entry.Name(), ".json")
			bestModTime = modTime
		}
	}

	if bestSessionID == "" {
		return nil, nil
	}

	// The transcript path for OpenCode is the message directory
	msgDir, _ := GetMessageDir(bestSessionID)

	return &agent.SessionInfo{
		SessionID:      bestSessionID,
		TranscriptPath: msgDir,
		StartedAt:      bestModTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
	}, nil
}

// discoverFromSessionDiff scans the session_diff storage directory (OpenCode v1.15+)
// for recently modified sessions belonging to the given project.
func (a *Agent) discoverFromSessionDiff(projectPath string) (*agent.SessionInfo, error) {
	sessionDiffDir, err := GetSessionDiffDir()
	if err != nil {
		return nil, nil
	}

	entries, err := os.ReadDir(sessionDiffDir)
	if err != nil {
		return nil, nil
	}

	// JSON-encode the project path to match how it appears in session files
	escapedPath, err := json.Marshal(projectPath)
	if err != nil {
		return nil, nil
	}

	now := time.Now()
	var bestSessionID string
	var bestModTime time.Time
	var bestData []byte

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		modTime := info.ModTime()
		if now.Sub(modTime) > agent.RecentSessionTimeout {
			continue
		}

		if bestSessionID != "" && !modTime.After(bestModTime) {
			continue
		}

		filePath := filepath.Join(sessionDiffDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		// Filter by project path: the JSON-encoded path appears as a value in the file
		if !bytes.Contains(data, escapedPath) {
			continue
		}

		bestSessionID = strings.TrimSuffix(entry.Name(), ".json")
		bestModTime = modTime
		bestData = data
	}

	if bestSessionID == "" {
		return nil, nil
	}

	return &agent.SessionInfo{
		SessionID:      bestSessionID,
		TranscriptPath: "",
		StartedAt:      bestModTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: bestData,
	}, nil
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
// Tries camelCase column names (v1.15+) before snake_case (v1.2-v1.14).
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Try camelCase (v1.15+) then snake_case (v1.2-v1.14) column names
	var sessionID string
	for _, sessionQuery := range []string{
		fmt.Sprintf(`SELECT id FROM session WHERE projectID='%s' ORDER BY timeUpdated DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
	} {
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, sessionQuery)
		out, err := cmd.Output()
		if err == nil {
			if id := strings.TrimSpace(string(out)); id != "" {
				sessionID = id
				break
			}
		}
	}

	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout)
	for _, timeQuery := range []string{
		fmt.Sprintf(`SELECT timeUpdated FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID),
	} {
		cmd := exec.Command("sqlite3", dbPath, timeQuery)
		timeOutput, err := cmd.Output()
		if err != nil {
			continue
		}
		timeStr := strings.TrimSpace(string(timeOutput))
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, timeStr); err == nil {
				if time.Since(t) > agent.RecentSessionTimeout {
					return nil, nil
				}
				break
			}
		}
		break
	}

	// Try session_diff file first (v1.15+)
	sessionDiffPath := filepath.Join(dataDir, "storage", "session_diff", sessionID+".json")
	if diffData, err := os.ReadFile(sessionDiffPath); err == nil && len(diffData) > 0 {
		return &agent.SessionInfo{
			SessionID:      sessionID,
			TranscriptPath: "",
			StartedAt:      time.Now().Format(time.RFC3339),
			ProjectPath:    projectPath,
			TranscriptData: diffData,
		}, nil
	}

	// Fall back to SQLite messages (v1.2-v1.14), try both column naming conventions
	for _, msgQuery := range []string{
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role, 'content', content)) FROM message WHERE sessionID='%s' ORDER BY timeCreated;`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
	} {
		cmd := exec.Command("sqlite3", dbPath, msgQuery)
		msgOutput, err := cmd.Output()
		if err != nil {
			continue
		}

		transcriptData := []byte(strings.TrimSpace(string(msgOutput)))
		if string(transcriptData) == "[null]" || string(transcriptData) == "[]" {
			continue
		}

		return &agent.SessionInfo{
			SessionID:      sessionID,
			TranscriptPath: "",
			StartedAt:      time.Now().Format(time.RFC3339),
			ProjectPath:    projectPath,
			TranscriptData: transcriptData,
		}, nil
	}

	return nil, nil
}

// RestoreSession writes a session to OpenCode's storage location.
func (a *Agent) RestoreSession(projectPath, sessionID, gitBranch string,
	transcriptData []byte, messageCount int, summary string) error {

	_, err := WriteSessionFile(projectPath, sessionID, transcriptData)
	return err
}

// ResumeCommand returns the command to resume an OpenCode session.
func (a *Agent) ResumeCommand(sessionID string) (string, []string) {
	return "opencode", []string{"--session", sessionID}
}

// ToolAliases returns OpenCode's tool name mappings to canonical names.
func (a *Agent) ToolAliases() map[string]string {
	return map[string]string{
		"bash":     "Bash",
		"shell":    "Bash",
		"terminal": "Bash",
		"write":    "Write",
		"read":     "Read",
		"edit":     "Edit",
		"grep":     "Grep",
		"glob":     "Glob",
	}
}

// parseOpenCodeEntry parses a single OpenCode message into a TranscriptEntry.
// Handles both direct message format and event-wrapped format (v1.15+ session_diff).
func parseOpenCodeEntry(raw map[string]json.RawMessage, fullData []byte) agent.TranscriptEntry {
	entry := agent.TranscriptEntry{
		Raw: json.RawMessage(append([]byte{}, fullData...)),
	}

	// Parse role field
	if roleRaw, ok := raw["role"]; ok {
		var role string
		if err := json.Unmarshal(roleRaw, &role); err == nil {
			entry.Type = agent.NormalizeRole(role)
		}
	}

	// Try "type" field if role not found
	if entry.Type == "" {
		if typeRaw, ok := raw["type"]; ok {
			var t string
			if err := json.Unmarshal(typeRaw, &t); err == nil {
				entry.Type = agent.NormalizeRole(t)

				// For event-wrapped format (e.g. "PartialMessage"), check nested properties
				if entry.Type == "" && strings.Contains(t, "essage") {
					if propsRaw, ok := raw["properties"]; ok {
						var props map[string]json.RawMessage
						if err := json.Unmarshal(propsRaw, &props); err == nil {
							nested := parseOpenCodeEntry(props, propsRaw)
							if nested.Type != "" {
								return nested
							}
						}
					}
				}
			}
		}
	}

	// Parse id
	if idRaw, ok := raw["id"]; ok {
		var id string
		if err := json.Unmarshal(idRaw, &id); err == nil {
			entry.UUID = id
		}
	}

	// Parse timestamp
	if timeRaw, ok := raw["time"]; ok {
		var timeObj struct {
			Created string `json:"created"`
		}
		if err := json.Unmarshal(timeRaw, &timeObj); err == nil {
			entry.Timestamp = timeObj.Created
		}
	}

	// Parse content
	entry.Message = parseOpenCodeMessage(raw, entry.Type)

	return entry
}


// parseOpenCodeMessage parses message content from an OpenCode entry.
// Handles both "content" (string/array) and "parts" (v1.15+ session_diff format).
func parseOpenCodeMessage(raw map[string]json.RawMessage, msgType agent.MessageType) *agent.Message {
	if msgType == "" {
		return nil
	}

	msg := &agent.Message{}
	switch msgType {
	case agent.MessageTypeUser:
		msg.Role = "user"
	case agent.MessageTypeAssistant:
		msg.Role = "assistant"
	case agent.MessageTypeSystem:
		msg.Role = "system"
	}

	// Try "content" as string
	if contentRaw, ok := raw["content"]; ok {
		var text string
		if err := json.Unmarshal(contentRaw, &text); err == nil && text != "" {
			msg.Content = []agent.ContentBlock{{Type: "text", Text: text}}
			return msg
		}

		// Try as array of content blocks
		var blocks []agent.ContentBlock
		if err := json.Unmarshal(contentRaw, &blocks); err == nil && len(blocks) > 0 {
			msg.Content = blocks
			return msg
		}
	}

	// Try "parts" field (session_diff v1.15+ format)
	if partsRaw, ok := raw["parts"]; ok {
		var parts []json.RawMessage
		if err := json.Unmarshal(partsRaw, &parts); err == nil {
			var blocks []agent.ContentBlock
			for _, partRaw := range parts {
				var part map[string]json.RawMessage
				if err := json.Unmarshal(partRaw, &part); err != nil {
					continue
				}
				var partType string
				if typeRaw, ok := part["type"]; ok {
					_ = json.Unmarshal(typeRaw, &partType)
				}
				if partType == "text" {
					if textRaw, ok := part["text"]; ok {
						var text string
						if err := json.Unmarshal(textRaw, &text); err == nil {
							blocks = append(blocks, agent.ContentBlock{Type: "text", Text: text})
						}
					}
				}
			}
			if len(blocks) > 0 {
				msg.Content = blocks
				return msg
			}
		}
	}

	// Try "message" field
	if msgRaw, ok := raw["message"]; ok {
		var innerMsg agent.Message
		if err := json.Unmarshal(msgRaw, &innerMsg); err == nil {
			return &innerMsg
		}
	}

	return msg
}
