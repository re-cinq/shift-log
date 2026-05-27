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
		"bash":    true,
		"shell":   true,
		"terminal": true,
		"execute": true,
		"run":     true,
		"command": true,
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
// It tries: flat file storage (legacy), project-local SQLite (.opencode/opencode.db),
// then XDG/global SQLite (~/.local/share/opencode/opencode.db).
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// 1. Try flat file storage (pre-v1.2 OpenCode)
	session, err := a.discoverFromFlatFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if session != nil {
		return session, nil
	}

	// sqlite3 required for all DB-based discovery
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// 2. Try project-local SQLite (.opencode/opencode.db)
	// Used by OpenCode versions that store the DB inside the project directory.
	localDB := filepath.Join(projectPath, ".opencode", "opencode.db")
	if _, err := os.Stat(localDB); err == nil {
		session = discoverFromLocalDB(localDB, projectPath)
		if session != nil {
			return session, nil
		}
	}

	// 3. Try XDG/global SQLite (~/.local/share/opencode/opencode.db)
	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}

	projectID := GetProjectID(projectPath)
	return discoverFromGlobalDB(filepath.Join(dataDir, "opencode.db"), projectID, projectPath)
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

// discoverFromLocalDB queries a project-local OpenCode SQLite database.
// This handles the schema used by OpenCode versions that store data inside
// the project directory (.opencode/opencode.db), which uses plural table names
// (sessions, messages), integer timestamps, and a parts column.
func discoverFromLocalDB(dbPath, projectPath string) *agent.SessionInfo {
	// Try plural table name (sessions) first, then singular (session)
	sessionID, timeStr := sqliteQueryPair(dbPath,
		`SELECT id, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 1;`)
	if sessionID == "" {
		sessionID, timeStr = sqliteQueryPair(dbPath,
			`SELECT id, time_updated FROM session ORDER BY time_updated DESC LIMIT 1;`)
	}
	if sessionID == "" {
		return nil
	}

	if !isRecentTimestamp(timeStr) {
		return nil
	}

	// Fetch messages — try plural table with parts column first
	transcriptData := sqliteQueryScalar(dbPath, fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts))) FROM messages WHERE session_id=%s ORDER BY created_at;`,
		sqliteStr(sessionID)))
	if isEmptySQLiteJSON(transcriptData) {
		transcriptData = sqliteQueryScalar(dbPath, fmt.Sprintf(
			`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts))) FROM message WHERE session_id=%s ORDER BY created_at;`,
			sqliteStr(sessionID)))
	}
	// Fall back to data column (older format)
	if isEmptySQLiteJSON(transcriptData) {
		transcriptData = sqliteQueryScalar(dbPath, fmt.Sprintf(
			`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id=%s ORDER BY time_created;`,
			sqliteStr(sessionID)))
	}
	if isEmptySQLiteJSON(transcriptData) {
		return nil
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: []byte(transcriptData),
	}
}

// discoverFromGlobalDB queries the XDG/global OpenCode SQLite database.
// This handles the schema used by OpenCode versions that store data in the
// XDG data directory (~/.local/share/opencode/opencode.db), which uses singular
// table names and a project_id column.
func discoverFromGlobalDB(dbPath, projectID, projectPath string) (*agent.SessionInfo, error) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	var sessionID, timeStr string

	// Try with project_id filter
	if projectID != "" {
		sessionID, timeStr = sqliteQueryPair(dbPath, fmt.Sprintf(
			`SELECT id, time_updated FROM session WHERE project_id=%s ORDER BY time_updated DESC LIMIT 1;`,
			sqliteStr(projectID)))
	}
	// Fall back to most recent session regardless of project
	if sessionID == "" {
		sessionID, timeStr = sqliteQueryPair(dbPath,
			`SELECT id, time_updated FROM session ORDER BY time_updated DESC LIMIT 1;`)
	}
	if sessionID == "" {
		return nil, nil
	}

	if !isRecentTimestamp(timeStr) {
		return nil, nil
	}

	// Fetch messages — try data column first, then parts
	transcriptData := sqliteQueryScalar(dbPath, fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id=%s ORDER BY time_created;`,
		sqliteStr(sessionID)))
	if isEmptySQLiteJSON(transcriptData) {
		transcriptData = sqliteQueryScalar(dbPath, fmt.Sprintf(
			`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts))) FROM message WHERE session_id=%s ORDER BY time_created;`,
			sqliteStr(sessionID)))
	}
	if isEmptySQLiteJSON(transcriptData) {
		return nil, nil
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: []byte(transcriptData),
	}, nil
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

// sqliteQueryPair runs a sqlite3 query and returns the first two tab-separated
// columns of the first result row.
func sqliteQueryPair(dbPath, query string) (string, string) {
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, query)
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "", ""
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	first := parts[0]
	var second string
	if len(parts) > 1 {
		second = parts[1]
	}
	return first, second
}

// sqliteQueryScalar runs a sqlite3 query and returns the trimmed scalar output.
func sqliteQueryScalar(dbPath, query string) string {
	cmd := exec.Command("sqlite3", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sqliteStr safely quotes a string value for use in SQLite queries.
// Single quotes inside the value are escaped by doubling them.
func sqliteStr(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// isEmptySQLiteJSON returns true if the SQLite output represents no rows.
func isEmptySQLiteJSON(s string) bool {
	return s == "" || s == "[null]" || s == "[]"
}

// isRecentTimestamp returns true if the timestamp string represents a time
// within RecentSessionTimeout. Returns true (assume recent) if unparseable.
func isRecentTimestamp(s string) bool {
	if s == "" {
		return true
	}

	// Try as integer (Unix seconds or milliseconds)
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		var t time.Time
		// Heuristic: current time is ~1.75e12 ms or ~1.75e9 s
		if n > 1e11 {
			t = time.Unix(0, n*int64(time.Millisecond))
		} else {
			t = time.Unix(n, 0)
		}
		return time.Since(t) <= agent.RecentSessionTimeout
	}

	// Try common string formats
	formats := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
	}

	return true // can't parse — assume recent rather than silently dropping
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

	// Parse timestamp — handles both JSON object {"created":"..."} and integer formats
	if timeRaw, ok := raw["time"]; ok {
		var timeObj struct {
			Created string `json:"created"`
		}
		if err := json.Unmarshal(timeRaw, &timeObj); err == nil {
			entry.Timestamp = timeObj.Created
		}
	}
	if entry.Timestamp == "" {
		for _, key := range []string{"created_at", "updated_at"} {
			if tsRaw, ok := raw[key]; ok {
				var ts string
				if err := json.Unmarshal(tsRaw, &ts); err == nil && ts != "" {
					entry.Timestamp = ts
					break
				}
				// Integer timestamp
				var n int64
				if err := json.Unmarshal(tsRaw, &n); err == nil {
					entry.Timestamp = time.Unix(n, 0).Format(time.RFC3339)
					break
				}
			}
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

	// Try "parts" field (OpenCode project-local schema: typed parts array)
	// Format: [{"type":"text","data":{"text":"..."}}, ...] or [{"type":"text","text":"..."}, ...]
	if partsRaw, ok := raw["parts"]; ok {
		var parts []json.RawMessage
		if err := json.Unmarshal(partsRaw, &parts); err == nil {
			var blocks []agent.ContentBlock
			for _, partRaw := range parts {
				var partMap map[string]json.RawMessage
				if err := json.Unmarshal(partRaw, &partMap); err != nil {
					continue
				}
				var partType string
				if typeRaw, ok := partMap["type"]; ok {
					_ = json.Unmarshal(typeRaw, &partType)
				}
				if partType != "text" {
					continue
				}
				// Try nested data.text (opencode project-local format)
				if dataRaw, ok := partMap["data"]; ok {
					var dataObj struct {
						Text string `json:"text"`
					}
					if err := json.Unmarshal(dataRaw, &dataObj); err == nil && dataObj.Text != "" {
						blocks = append(blocks, agent.ContentBlock{Type: "text", Text: dataObj.Text})
						continue
					}
				}
				// Try direct text field
				if textRaw, ok := partMap["text"]; ok {
					var text string
					if err := json.Unmarshal(textRaw, &text); err == nil && text != "" {
						blocks = append(blocks, agent.ContentBlock{Type: "text", Text: text})
					}
				}
			}
			if len(blocks) > 0 {
				msg.Content = blocks
				return msg
			}
			// parts field existed but had no extractable text — still return msg
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
