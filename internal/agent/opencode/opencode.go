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
// It tries flat file storage (pre-v1.2), then SQLite at the global XDG data dir,
// then SQLite at the project-local .opencode/ directory.
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

	// Try global XDG storage (OpenCode v1.2+)
	dataDir, err := GetDataDir()
	if err == nil {
		if session, err := discoverFromSQLite(dataDir, projectID, projectPath); session != nil || err != nil {
			return session, err
		}
	}

	// Try project-local storage (some OpenCode versions store DB in .opencode/)
	localDir := filepath.Join(projectPath, ".opencode")
	if _, statErr := os.Stat(localDir); statErr == nil {
		return discoverFromSQLite(localDir, projectID, projectPath)
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

// discoverFromSQLite queries an OpenCode SQLite database for the most recent session.
// It uses schema introspection to adapt to different column naming conventions across
// OpenCode versions (e.g., project_id vs projectId, time_updated vs updated_at).
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Discover session table (handles "session" and "sessions" naming)
	sessionTable := sqliteFirstTable(dbPath, "session", "sessions")
	if sessionTable == "" {
		return nil, nil
	}

	sessionCols := sqliteTableCols(dbPath, sessionTable)
	if len(sessionCols) == 0 {
		return nil, nil
	}

	// Find time ordering column across naming conventions
	timeCol := sqliteCol(sessionCols,
		"time_updated", "timeUpdated", "updated_at",
		"time_created", "timeCreated", "created_at")
	if timeCol == "" {
		return nil, nil
	}

	// Find project ID column (may not exist in all schema versions)
	pidCol := sqliteCol(sessionCols, "project_id", "projectId", "projectID")

	// Try multiple strategies to find the session ID
	sessionID := ""
	if pidCol != "" {
		// Strategy 1: filter by project_id = git root commit hash
		sessionID = sqliteScalar(dbPath, fmt.Sprintf(
			`SELECT id FROM %s WHERE %s='%s' ORDER BY %s DESC LIMIT 1;`,
			sessionTable, pidCol, projectID, timeCol))

		// Strategy 2: filter by project_id = absolute project path
		if sessionID == "" {
			sessionID = sqliteScalar(dbPath, fmt.Sprintf(
				`SELECT id FROM %s WHERE %s='%s' ORDER BY %s DESC LIMIT 1;`,
				sessionTable, pidCol, projectPath, timeCol))
		}
	}

	// Strategy 3: most recent session regardless of project
	if sessionID == "" {
		sessionID = sqliteScalar(dbPath, fmt.Sprintf(
			`SELECT id FROM %s ORDER BY %s DESC LIMIT 1;`,
			sessionTable, timeCol))
	}
	if sessionID == "" {
		return nil, nil
	}

	// Verify session is within the recent timeout window
	if !sqliteCheckRecent(dbPath, sessionTable, timeCol, sessionID) {
		return nil, nil
	}

	// Find message table (handles "message" and "messages" naming)
	msgTable := sqliteFirstTable(dbPath, "message", "messages")
	if msgTable == "" {
		return nil, nil
	}

	msgCols := sqliteTableCols(dbPath, msgTable)

	// Find session FK column
	fkCol := sqliteCol(msgCols, "session_id", "sessionId", "sessionID")
	if fkCol == "" {
		return nil, nil
	}

	// Find message data column (handles "data" and "parts" naming)
	dataCol := sqliteCol(msgCols, "data", "parts", "content")
	if dataCol == "" {
		return nil, nil
	}

	// Find message time column for ordering
	msgTimeCol := sqliteCol(msgCols,
		"time_created", "timeCreated", "created_at",
		"time_updated", "timeUpdated", "updated_at")

	// Retrieve messages as a JSON array
	transcriptData := sqliteGetMessages(dbPath, msgTable, fkCol, dataCol, msgTimeCol, sessionID)
	if len(transcriptData) == 0 {
		return nil, nil
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// sqliteScalar runs a SQLite query and returns the first result as a trimmed string.
func sqliteScalar(dbPath, query string) string {
	out, err := exec.Command("sqlite3", dbPath, query).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sqliteFirstTable returns the first existing table name from the candidates.
func sqliteFirstTable(dbPath string, candidates ...string) string {
	for _, name := range candidates {
		result := sqliteScalar(dbPath, fmt.Sprintf(
			"SELECT name FROM sqlite_master WHERE type='table' AND name='%s';", name))
		if result != "" {
			return name
		}
	}
	return ""
}

// sqliteTableCols returns column names for a table using PRAGMA table_info.
func sqliteTableCols(dbPath, table string) []string {
	out, err := exec.Command("sqlite3", dbPath,
		fmt.Sprintf("PRAGMA table_info(%s);", table)).Output()
	if err != nil {
		return nil
	}
	var cols []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// PRAGMA table_info output: cid|name|type|notnull|dflt_value|pk
		parts := strings.SplitN(line, "|", 3)
		if len(parts) >= 2 {
			cols = append(cols, parts[1])
		}
	}
	return cols
}

// sqliteCol finds the first column whose name (case-insensitive) matches a candidate.
// Returns the exact column name as stored in the schema.
func sqliteCol(cols []string, candidates ...string) string {
	for _, candidate := range candidates {
		cl := strings.ToLower(candidate)
		for _, col := range cols {
			if strings.ToLower(col) == cl {
				return col
			}
		}
	}
	return ""
}

// sqliteCheckRecent returns true if the session's timestamp is within the recent
// timeout window, or true if the timestamp cannot be determined.
func sqliteCheckRecent(dbPath, table, timeCol, sessionID string) bool {
	ts := sqliteScalar(dbPath, fmt.Sprintf(
		"SELECT %s FROM %s WHERE id='%s';", timeCol, table, sessionID))
	if ts == "" {
		return true
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		time.RFC3339,
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, ts); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
	}
	// Handle Unix timestamp stored as integer (seconds or milliseconds)
	if ms, err := strconv.ParseInt(ts, 10, 64); err == nil {
		var t time.Time
		if ms > 1e12 {
			t = time.UnixMilli(ms)
		} else {
			t = time.Unix(ms, 0)
		}
		return time.Since(t) <= agent.RecentSessionTimeout
	}
	return true
}

// sqliteGetMessages fetches session messages as a JSON array.
// It tries json_group_array (SQLite >= 3.38) first, then falls back to
// row-by-row assembly for older SQLite versions.
func sqliteGetMessages(dbPath, msgTable, fkCol, dataCol, timeCol, sessionID string) []byte {
	orderClause := ""
	if timeCol != "" {
		orderClause = " ORDER BY " + timeCol
	}

	// Primary: use json_group_array to build the array in SQLite
	q := fmt.Sprintf(
		`SELECT json_group_array(json_patch(%s, json_object('id', id))) FROM %s WHERE %s='%s'%s;`,
		dataCol, msgTable, fkCol, sessionID, orderClause)
	out, err := exec.Command("sqlite3", dbPath, q).Output()
	if err == nil {
		result := strings.TrimSpace(string(out))
		if result != "" && result != "[null]" && result != "[]" {
			return []byte(result)
		}
	}

	// Fallback: fetch rows individually and assemble JSON array in Go
	rowQ := fmt.Sprintf(
		`SELECT id, %s FROM %s WHERE %s='%s'%s;`,
		dataCol, msgTable, fkCol, sessionID, orderClause)
	out, err = exec.Command("sqlite3", "-separator", "|", dbPath, rowQ).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return nil
	}

	var messages []json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Split on first | (the data JSON may itself contain |)
		idx := strings.Index(line, "|")
		if idx < 0 {
			continue
		}
		msgID := line[:idx]
		msgData := line[idx+1:]
		if msgData == "" || msgData == "null" {
			continue
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(msgData), &obj); err != nil {
			continue
		}
		if obj == nil {
			obj = make(map[string]json.RawMessage)
		}
		idBytes, _ := json.Marshal(msgID)
		obj["id"] = idBytes
		merged, err := json.Marshal(obj)
		if err != nil {
			continue
		}
		messages = append(messages, merged)
	}

	if len(messages) == 0 {
		return nil
	}
	result, err := json.Marshal(messages)
	if err != nil {
		return nil
	}
	return result
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

	// Try "parts" field (OpenCode initial schema used typed parts array)
	if partsRaw, ok := raw["parts"]; ok {
		var parts []json.RawMessage
		if err := json.Unmarshal(partsRaw, &parts); err == nil {
			for _, part := range parts {
				var p struct {
					Type string          `json:"type"`
					Data json.RawMessage `json:"data"`
				}
				if err := json.Unmarshal(part, &p); err != nil {
					continue
				}
				if p.Type == "text" {
					var textData struct {
						Text string `json:"text"`
					}
					if err := json.Unmarshal(p.Data, &textData); err == nil && textData.Text != "" {
						msg.Content = append(msg.Content, agent.ContentBlock{Type: "text", Text: textData.Text})
					}
				}
			}
			if len(msg.Content) > 0 {
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
