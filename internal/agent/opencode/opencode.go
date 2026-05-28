package opencode

import (
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
// It tries flat file storage (pre-v1.2), then project-local SQLite,
// then falls back to the XDG data directory SQLite.
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// Try flat file storage first (pre-v1.2 OpenCode)
	session, err := a.discoverFromFlatFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if session != nil {
		return session, nil
	}

	projectID := GetProjectID(projectPath)

	// Try project-local SQLite database (v1.15+ stores .opencode/opencode.db in project dir)
	localDBPath := filepath.Join(projectPath, ".opencode", "opencode.db")
	if _, err := os.Stat(localDBPath); err == nil {
		session := discoverFromSQLiteDB(localDBPath, projectID, projectPath, true)
		if session != nil {
			return session, nil
		}
	}

	// Fall back to XDG data directory SQLite (v1.2–v1.14)
	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}
	xdgDBPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(xdgDBPath); err == nil {
		session := discoverFromSQLiteDB(xdgDBPath, projectID, projectPath, false)
		if session != nil {
			return session, nil
		}
	}

	return nil, nil
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

// discoverFromSQLiteDB queries an OpenCode SQLite database for the most recent session.
// It handles multiple schema versions (v1.2–v1.14 and v1.15+) by trying different
// table names, column names, and query strategies.
// When projectLocal is true, the DB is project-scoped so project_id filtering is optional.
func discoverFromSQLiteDB(dbPath, projectID, projectPath string, projectLocal bool) *agent.SessionInfo {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}

	// --- Step 1: Find the session ID ---
	//
	// Try queries in order of specificity:
	// 1. project_id filter + time_updated (v1.2–v1.14 global schema)
	// 2. project_id filter + updated_at   (possible rename)
	// 3. No project_id filter + time_updated (fallback, or project-local DB)
	// 4. No project_id filter + updated_at
	// 5. sessions (plural) variants of the above

	type sessionQuery struct {
		sql string
	}
	sessionQueries := []sessionQuery{
		// v1.2–v1.14: session table with project_id and time_updated
		{fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID)},
		// renamed column: updated_at
		{fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY updated_at DESC LIMIT 1;`, projectID)},
		// plural table name variants
		{fmt.Sprintf(`SELECT id FROM sessions WHERE project_id='%s' ORDER BY updated_at DESC LIMIT 1;`, projectID)},
		{fmt.Sprintf(`SELECT id FROM sessions WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID)},
		// no project_id filter (project-local DB or fallback)
		{`SELECT id FROM session ORDER BY time_updated DESC LIMIT 1;`},
		{`SELECT id FROM session ORDER BY updated_at DESC LIMIT 1;`},
		{`SELECT id FROM sessions ORDER BY updated_at DESC LIMIT 1;`},
		{`SELECT id FROM sessions ORDER BY time_updated DESC LIMIT 1;`},
	}

	var sessionID string
	for _, q := range sessionQueries {
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q.sql)
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		if sid := strings.TrimSpace(string(output)); sid != "" {
			sessionID = sid
			break
		}
	}

	if sessionID == "" {
		return nil
	}

	// --- Step 2: Check recency ---
	//
	// Try to read the session's timestamp and check if it's recent.
	// If we can't parse the timestamp, proceed anyway.

	timeQueries := []string{
		fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT updated_at FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT updated_at FROM sessions WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT time_updated FROM sessions WHERE id='%s';`, sessionID),
	}

	for _, q := range timeQueries {
		cmd := exec.Command("sqlite3", dbPath, q)
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		timeStr := strings.TrimSpace(string(output))
		if timeStr == "" {
			continue
		}
		// Try to parse the timestamp (string or integer Unix ms)
		if t := parseOpenCodeTimestamp(timeStr); !t.IsZero() {
			if time.Since(t) > agent.RecentSessionTimeout {
				return nil
			}
		}
		break // found the time field, stop trying
	}

	// --- Step 3: Get messages ---
	//
	// Try multiple message query variants to handle schema changes.

	msgQueries := []string{
		// v1.2–v1.14: message table with data column and json_patch
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
		// renamed columns: created_at
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY created_at;`, sessionID),
		// v1.15+: message/messages table with parts column
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts))) FROM message WHERE session_id='%s' ORDER BY created_at;`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts))) FROM messages WHERE session_id='%s' ORDER BY created_at;`, sessionID),
		// Simpler fallback without json_patch (for older SQLite without json_patch support)
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role)) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role)) FROM message WHERE session_id='%s' ORDER BY created_at;`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role)) FROM messages WHERE session_id='%s' ORDER BY created_at;`, sessionID),
	}

	var transcriptData []byte
	for _, q := range msgQueries {
		cmd := exec.Command("sqlite3", dbPath, q)
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" || trimmed == "[null]" || trimmed == "[]" {
			continue
		}
		transcriptData = []byte(trimmed)
		break
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}
}

// parseOpenCodeTimestamp parses a timestamp from OpenCode's SQLite database.
// Handles ISO 8601 strings, "YYYY-MM-DD HH:MM:SS" strings, and Unix millisecond integers.
func parseOpenCodeTimestamp(s string) time.Time {
	// Try ISO 8601 variants
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	// Try Unix milliseconds (integer string)
	var ms int64
	if _, err := fmt.Sscanf(s, "%d", &ms); err == nil && ms > 1_000_000_000_000 {
		return time.UnixMilli(ms)
	}
	// Try Unix seconds
	var sec int64
	if _, err := fmt.Sscanf(s, "%d", &sec); err == nil && sec > 1_000_000_000 {
		return time.Unix(sec, 0)
	}
	return time.Time{}
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

	// Try "parts" field (v1.15+ schema: array of typed parts)
	if partsRaw, ok := raw["parts"]; ok {
		var parts []json.RawMessage
		if err := json.Unmarshal(partsRaw, &parts); err == nil && len(parts) > 0 {
			var blocks []agent.ContentBlock
			for _, partRaw := range parts {
				var part struct {
					Type string          `json:"type"`
					Data json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(partRaw, &part); err != nil {
					continue
				}
				switch part.Type {
				case "text":
					var textData struct {
						Text string `json:"text"`
					}
					if err := json.Unmarshal(part.Data, &textData); err == nil && textData.Text != "" {
						blocks = append(blocks, agent.ContentBlock{Type: "text", Text: textData.Text})
					}
				case "tool_call":
					var toolData struct {
						Name  string `json:"name"`
						Input string `json:"input"`
					}
					if err := json.Unmarshal(part.Data, &toolData); err == nil {
						blocks = append(blocks, agent.ContentBlock{
							Type:  "tool_use",
							Text:  toolData.Name + ": " + toolData.Input,
						})
					}
				case "tool_result":
					var resultData struct {
						Output string `json:"output"`
					}
					if err := json.Unmarshal(part.Data, &resultData); err == nil {
						blocks = append(blocks, agent.ContentBlock{
							Type: "tool_result",
							Text: resultData.Output,
						})
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
