package opencode

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
		"bash":      true,
		"shell":     true,
		"terminal":  true,
		"execute":   true,
		"run":       true,
		"command":   true,
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
// It first tries flat file storage (pre-v1.2), then falls back to SQLite (v1.2+).
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// Try flat file storage first (pre-v1.2 OpenCode)
	session, err := a.discoverFromFlatFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if session != nil {
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

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
// It tries the new schema (v1.17+: sessions/messages/updated_at) first, then falls
// back to the old schema (v1.2-v1.16: session/message/time_updated/project_id).
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Try to find the most recent session, trying new schema first then old.
	sessionID, timeVal, newSchema := querySQLiteSession(dbPath, projectID)
	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout)
	if !isSQLiteSessionRecent(timeVal, newSchema) {
		return nil, nil
	}

	// Get messages for this session as a JSON array
	transcriptData, err := querySQLiteMessages(dbPath, sessionID, newSchema)
	if err != nil || len(transcriptData) == 0 {
		return nil, nil
	}

	trimmed := strings.TrimSpace(string(transcriptData))
	if trimmed == "[null]" || trimmed == "[]" {
		return nil, nil
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "", // no file path for SQLite
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: []byte(trimmed),
	}, nil
}

// querySQLiteSession finds the most recent session ID and its timestamp.
// Tries new schema (v1.17+: "sessions" table, "updated_at" INTEGER) first,
// then falls back to old schema (v1.2-1.16: "session" table, "time_updated" string).
// Returns sessionID, timeValue string, and whether new schema was used.
func querySQLiteSession(dbPath, projectID string) (sessionID, timeVal string, newSchema bool) {
	// New schema: "sessions" table, "updated_at" column (INTEGER unix ms), no project_id
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath,
		"SELECT id, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 1;")
	if out, err := cmd.Output(); err == nil {
		line := strings.TrimSpace(string(out))
		if line != "" {
			if parts := strings.SplitN(line, "\t", 2); len(parts) == 2 && parts[0] != "" {
				return parts[0], parts[1], true
			}
		}
	}

	// Old schema: "session" table, "time_updated" column (string), filtered by project_id
	query := fmt.Sprintf(
		"SELECT id, time_updated FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;",
		projectID,
	)
	cmd = exec.Command("sqlite3", "-separator", "\t", dbPath, query)
	if out, err := cmd.Output(); err == nil {
		line := strings.TrimSpace(string(out))
		if line != "" {
			if parts := strings.SplitN(line, "\t", 2); len(parts) == 2 && parts[0] != "" {
				return parts[0], parts[1], false
			}
		}
	}

	return "", "", false
}

// isSQLiteSessionRecent checks whether the session timestamp is within RecentSessionTimeout.
func isSQLiteSessionRecent(timeVal string, newSchema bool) bool {
	if timeVal == "" {
		return true // can't determine, proceed anyway
	}

	var t time.Time
	if newSchema {
		// updated_at is INTEGER (Unix milliseconds or seconds)
		if ms, err := strconv.ParseInt(timeVal, 10, 64); err == nil {
			if ms > 1e12 { // likely milliseconds (year 2001+ in ms is > 1e12)
				t = time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
			} else {
				t = time.Unix(ms, 0)
			}
		}
	}

	if t.IsZero() {
		// Try ISO string formats (old schema or fallback)
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
			if parsed, err := time.Parse(layout, timeVal); err == nil {
				t = parsed
				break
			}
		}
	}

	if t.IsZero() {
		return true // can't parse, proceed anyway — better to try than skip
	}

	return time.Since(t) <= agent.RecentSessionTimeout
}

// querySQLiteMessages retrieves messages for a session as a JSON array.
// Uses new schema (messages/parts) or old schema (message/data) depending on newSchema.
func querySQLiteMessages(dbPath, sessionID string, newSchema bool) ([]byte, error) {
	if newSchema {
		// New schema: "messages" table, "parts" column (JSON text), "created_at" (INTEGER)
		msgQuery := fmt.Sprintf(
			"SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts))) FROM messages WHERE session_id='%s' ORDER BY created_at;",
			sessionID,
		)
		cmd := exec.Command("sqlite3", dbPath, msgQuery)
		out, err := cmd.Output()
		if err == nil && len(strings.TrimSpace(string(out))) > 0 {
			return out, nil
		}
		// json(parts) may fail on older sqlite3; fall back to string parts
		msgQuery = fmt.Sprintf(
			"SELECT json_group_array(json_object('id', id, 'role', role, 'parts', parts)) FROM messages WHERE session_id='%s' ORDER BY created_at;",
			sessionID,
		)
		cmd = exec.Command("sqlite3", dbPath, msgQuery)
		out, err = cmd.Output()
		if err == nil {
			return out, nil
		}
	}

	// Old schema: "message" table, "data" column, "time_created" column
	msgQuery := fmt.Sprintf(
		"SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;",
		sessionID,
	)
	cmd := exec.Command("sqlite3", dbPath, msgQuery)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return out, nil
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
// Handles both the legacy format (content field) and the new parts format (v1.17+).
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

	// Try "parts" (new format: opencode v1.17+)
	// Parts is a JSON array of typed entries: [{"type":"text","data":{"text":"..."}}]
	if partsRaw, ok := raw["parts"]; ok {
		blocks := parsePartsToContentBlocks(partsRaw)
		msg.Content = blocks
		return msg
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

// parsePartsToContentBlocks converts the opencode v1.17+ "parts" array to ContentBlocks.
// Parts may be a JSON array or a JSON-encoded string containing an array.
func parsePartsToContentBlocks(partsRaw json.RawMessage) []agent.ContentBlock {
	// Try direct unmarshal as array
	var parts []json.RawMessage
	if err := json.Unmarshal(partsRaw, &parts); err != nil {
		// parts may be stored as a JSON string containing an array (from SQLite text column)
		var partsStr string
		if err2 := json.Unmarshal(partsRaw, &partsStr); err2 == nil {
			if err3 := json.Unmarshal([]byte(partsStr), &parts); err3 != nil {
				return nil
			}
		} else {
			return nil
		}
	}

	var blocks []agent.ContentBlock
	for _, partRaw := range parts {
		var part struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(partRaw, &part); err != nil {
			continue
		}
		if part.Type == "text" && len(part.Data) > 0 {
			var textData struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(part.Data, &textData); err == nil && textData.Text != "" {
				blocks = append(blocks, agent.ContentBlock{Type: "text", Text: textData.Text})
			}
		}
	}
	return blocks
}
