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
// It tries multiple schema variants to handle different versions of OpenCode:
//   - v1.2+:  "session" table, project_id=root-commit-hash, time_updated, "message" table with "data" column
//   - v1.17+: "sessions" table (plural), updated_at as Unix-ms integer, no project_id,
//             "messages" table (plural) with "parts" column
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	type schemaVariant struct {
		// sessionSQL selects (id, time) from the sessions table.
		sessionSQL string
	}

	variants := []schemaVariant{
		// v1.17+: "sessions" table, ordered by updated_at (Unix ms), no project filter.
		{sessionSQL: `SELECT id, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 1;`},
		// v1.2+: "session" table filtered by root-commit-hash as project_id.
		{sessionSQL: fmt.Sprintf(`SELECT id, time_updated FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID)},
		// v1.2+ alt: "session" table filtered by project path (some builds use path as ID).
		{sessionSQL: fmt.Sprintf(`SELECT id, time_updated FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectPath)},
		// Final fallback: most recent session regardless of project.
		{sessionSQL: `SELECT id, time_updated FROM session ORDER BY time_updated DESC LIMIT 1;`},
	}

	for _, v := range variants {
		sessionID, timeStr := sqliteQueryRow(dbPath, v.sessionSQL)
		if sessionID == "" {
			continue
		}

		// Check recency.
		if timeStr != "" && !sqliteTimeIsRecent(timeStr) {
			continue
		}

		// Retrieve messages using whichever message-table schema matches.
		transcriptData := sqliteGetMessages(dbPath, sessionID)
		if len(transcriptData) == 0 || string(transcriptData) == "[null]" || string(transcriptData) == "[]" {
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

// sqliteQueryRow runs a sqlite3 query that returns a single tab-separated row with
// two columns (e.g. "id\ttime"). Returns ("", "") on error or empty output.
func sqliteQueryRow(dbPath, query string) (col1, col2 string) {
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, query)
	output, err := cmd.Output()
	if err != nil {
		return "", ""
	}
	line := strings.TrimSpace(string(output))
	if line == "" {
		return "", ""
	}
	cols := strings.SplitN(line, "\t", 2)
	col1 = cols[0]
	if len(cols) > 1 {
		col2 = cols[1]
	}
	return col1, col2
}

// sqliteQuerySingle runs a sqlite3 query returning a single value.
func sqliteQuerySingle(dbPath, query string) string {
	cmd := exec.Command("sqlite3", dbPath, query)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// sqliteTimeIsRecent returns true if the time string represents a moment within
// agent.RecentSessionTimeout. Handles RFC3339, ISO 8601 with ms, plain datetime,
// and Unix milliseconds (used by OpenCode v1.17+). Returns true when unparseable
// so that we try rather than silently skip a valid session.
func sqliteTimeIsRecent(timeStr string) bool {
	if t, err := time.Parse(time.RFC3339Nano, timeStr); err == nil {
		return time.Since(t) <= agent.RecentSessionTimeout
	}
	if t, err := time.Parse("2006-01-02T15:04:05.000Z", timeStr); err == nil {
		return time.Since(t) <= agent.RecentSessionTimeout
	}
	if t, err := time.Parse("2006-01-02 15:04:05", timeStr); err == nil {
		return time.Since(t) <= agent.RecentSessionTimeout
	}
	// OpenCode v1.17+ stores time as Unix milliseconds (integer).
	if ms, err := strconv.ParseInt(timeStr, 10, 64); err == nil && ms > 0 {
		t := time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
		return time.Since(t) <= agent.RecentSessionTimeout
	}
	// Cannot parse: proceed rather than skip.
	return true
}

// sqliteGetMessages retrieves session messages from SQLite, trying multiple
// schema variants:
//   - v1.17+: "messages" table with "parts" column (JSON array of typed parts)
//   - v1.2+:  "message" table with "data" column (full JSON object per row)
func sqliteGetMessages(dbPath, sessionID string) []byte {
	// v1.17+ schema: "messages" table, "parts" column.
	newQuery := fmt.Sprintf(
		`SELECT json_group_array(json_object('role', role, 'id', id, 'content', json(parts), 'time', json_object('created', created_at))) FROM messages WHERE session_id='%s' ORDER BY created_at;`,
		sessionID,
	)
	if data := sqliteQuerySingle(dbPath, newQuery); data != "" && data != "[null]" && data != "[]" {
		return []byte(data)
	}

	// v1.2+ schema: "message" table, "data" column.
	oldQuery := fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sessionID,
	)
	if data := sqliteQuerySingle(dbPath, oldQuery); data != "" && data != "[null]" && data != "[]" {
		return []byte(data)
	}

	return nil
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

		// Try as array of content blocks (standard format: [{type, text}])
		var blocks []agent.ContentBlock
		if err := json.Unmarshal(contentRaw, &blocks); err == nil && len(blocks) > 0 {
			msg.Content = blocks
			return msg
		}

		// Try as OpenCode v1.17+ parts format: [{type, data: {text}}]
		var parts []struct {
			Type string `json:"type"`
			Data struct {
				Text string `json:"text"`
			} `json:"data"`
		}
		if err := json.Unmarshal(contentRaw, &parts); err == nil && len(parts) > 0 {
			var extracted []agent.ContentBlock
			for _, p := range parts {
				if p.Type == "text" && p.Data.Text != "" {
					extracted = append(extracted, agent.ContentBlock{Type: "text", Text: p.Data.Text})
				}
			}
			if len(extracted) > 0 {
				msg.Content = extracted
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
