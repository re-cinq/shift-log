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

func (a *Agent) Name() agent.Name    { return agent.OpenCode }
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
		"bash":     true,
		"shell":    true,
		"terminal": true,
		"execute":  true,
		"run":      true,
		"command":  true,
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
// It tries multiple database locations and schema versions for forward compatibility:
//   - Project-local: <project>/.opencode/opencode.db with new schema (sessions/updated_at/parts)
//   - XDG data dir: ~/.local/share/opencode/opencode.db with new or old schema
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Try project-local database first (OpenCode v1.15+)
	projectLocalDB := filepath.Join(projectPath, ".opencode", "opencode.db")
	if _, err := os.Stat(projectLocalDB); err == nil {
		if session := tryNewSchema(projectLocalDB, projectPath); session != nil {
			return session, nil
		}
		if session := tryOldSchema(projectLocalDB, projectID, projectPath); session != nil {
			return session, nil
		}
	}

	// Try XDG data directory (OpenCode v1.2–v1.14)
	xdgDB := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(xdgDB); os.IsNotExist(err) {
		return nil, nil
	}

	if session := tryNewSchema(xdgDB, projectPath); session != nil {
		return session, nil
	}
	return tryOldSchema(xdgDB, projectID, projectPath), nil
}

// tryNewSchema attempts session discovery using the new OpenCode schema
// (sessions table with updated_at INTEGER and messages with parts column).
func tryNewSchema(dbPath, projectPath string) *agent.SessionInfo {
	// Verify the new schema exists (check for 'sessions' table)
	checkCmd := exec.Command("sqlite3", dbPath,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sessions';")
	checkOut, err := checkCmd.Output()
	if err != nil || strings.TrimSpace(string(checkOut)) != "1" {
		return nil
	}

	// Find the most recently updated session
	sessionQuery := `SELECT id, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 1;`
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, sessionQuery)
	sessionOutput, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(sessionOutput)) == "" {
		return nil
	}

	fields := strings.SplitN(strings.TrimSpace(string(sessionOutput)), "\t", 2)
	if len(fields) == 0 || fields[0] == "" {
		return nil
	}
	sessionID := fields[0]

	// Check recency — updated_at is stored as Unix milliseconds
	if len(fields) >= 2 {
		if t, ok := parseOpenCodeTimestamp(strings.TrimSpace(fields[1])); ok {
			if time.Since(t) > agent.RecentSessionTimeout {
				return nil
			}
		}
	}

	transcriptData := readMessagesNewSchema(dbPath, sessionID)
	if len(transcriptData) == 0 {
		// Return session with empty transcript rather than nil — the session existed
		transcriptData = []byte("[]")
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}
}

// readMessagesNewSchema reads messages from the new OpenCode schema (messages table, parts column).
func readMessagesNewSchema(dbPath, sessionID string) []byte {
	msgQuery := fmt.Sprintf(
		`SELECT id, role, parts, COALESCE(model, '') AS model FROM messages WHERE session_id='%s' ORDER BY created_at;`,
		sessionID,
	)
	cmd := exec.Command("sqlite3", "-json", dbPath, msgQuery)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil
	}

	var rows []struct {
		ID    string `json:"id"`
		Role  string `json:"role"`
		Parts string `json:"parts"`
		Model string `json:"model"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil
	}

	var messages []map[string]interface{}
	for _, row := range rows {
		msg := map[string]interface{}{
			"id":   row.ID,
			"role": row.Role,
		}
		if row.Model != "" {
			msg["model"] = row.Model
		}
		if row.Parts != "" {
			var rawParts json.RawMessage
			if json.Unmarshal([]byte(row.Parts), &rawParts) == nil {
				msg["parts"] = rawParts
			}
		}
		messages = append(messages, msg)
	}

	data, err := json.Marshal(messages)
	if err != nil {
		return nil
	}
	return data
}

// tryOldSchema attempts session discovery using the old OpenCode schema
// (session table with project_id, time_updated TEXT and message table with data column).
func tryOldSchema(dbPath, projectID, projectPath string) *agent.SessionInfo {
	sessionQuery := fmt.Sprintf(
		`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`,
		projectID,
	)
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, sessionQuery)
	sessionOutput, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(sessionOutput)) == "" {
		return nil
	}
	sessionID := strings.TrimSpace(string(sessionOutput))

	// Check recency
	timeQuery := fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID)
	cmd = exec.Command("sqlite3", dbPath, timeQuery)
	timeOutput, err := cmd.Output()
	if err == nil {
		timeStr := strings.TrimSpace(string(timeOutput))
		if t, ok := parseOpenCodeTimestamp(timeStr); ok {
			if time.Since(t) > agent.RecentSessionTimeout {
				return nil
			}
		}
	}

	transcriptData := readMessagesOldSchema(dbPath, sessionID)
	if len(transcriptData) == 0 {
		transcriptData = []byte("[]")
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}
}

// readMessagesOldSchema reads messages from the old OpenCode schema (message table, data column).
func readMessagesOldSchema(dbPath, sessionID string) []byte {
	msgQuery := fmt.Sprintf(
		`SELECT id, data FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sessionID,
	)
	cmd := exec.Command("sqlite3", "-json", dbPath, msgQuery)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" || trimmed == "[]" || trimmed == "null" {
		return nil
	}

	var rows []struct {
		ID   string `json:"id"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil
	}

	var messages []json.RawMessage
	for _, row := range rows {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(row.Data), &obj); err != nil {
			continue
		}
		idJSON, _ := json.Marshal(row.ID)
		obj["id"] = idJSON
		msgData, err := json.Marshal(obj)
		if err != nil {
			continue
		}
		messages = append(messages, msgData)
	}

	data, err := json.Marshal(messages)
	if err != nil {
		return nil
	}
	return data
}

// parseOpenCodeTimestamp parses a timestamp string or integer (unix ms) from OpenCode.
// Returns the parsed time and whether parsing succeeded.
func parseOpenCodeTimestamp(s string) (time.Time, bool) {
	// Try as Unix milliseconds (integer) — used by newer OpenCode versions
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
		var t time.Time
		if ms > 1e12 {
			// Milliseconds
			t = time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
		} else {
			// Seconds
			t = time.Unix(ms, 0)
		}
		return t, true
	}

	// Try ISO string formats used by older OpenCode versions
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}

	return time.Time{}, false
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

	// Handle "parts" field (newer OpenCode schema: [{type, data}, ...])
	if partsRaw, ok := raw["parts"]; ok {
		var parts []struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(partsRaw, &parts); err == nil && len(parts) > 0 {
			var blocks []agent.ContentBlock
			for _, part := range parts {
				switch part.Type {
				case "text":
					var textData struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(part.Data, &textData) == nil && textData.Text != "" {
						blocks = append(blocks, agent.ContentBlock{Type: "text", Text: textData.Text})
					}
				case "tool_call":
					var toolData struct {
						Name string `json:"name"`
					}
					if json.Unmarshal(part.Data, &toolData) == nil && toolData.Name != "" {
						blocks = append(blocks, agent.ContentBlock{Type: "tool_use", Text: toolData.Name})
					}
				}
			}
			if len(blocks) > 0 {
				msg.Content = blocks
			}
			return msg
		}
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

	// Try "message" field
	if msgRaw, ok := raw["message"]; ok {
		var innerMsg agent.Message
		if err := json.Unmarshal(msgRaw, &innerMsg); err == nil {
			return &innerMsg
		}
	}

	return msg
}
