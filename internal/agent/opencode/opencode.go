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
// It tries, in order:
//  1. Project-local .opencode/opencode.db (OpenCode v1.16+)
//  2. Flat file storage (pre-v1.2 OpenCode)
//  3. Global SQLite at ~/.local/share/opencode/opencode.db (v1.2–v1.15)
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// Try project-local SQLite first (OpenCode v1.16+)
	session, err := discoverFromLocalDB(projectPath)
	if err != nil {
		return nil, err
	}
	if session != nil {
		return session, nil
	}

	// Try flat file storage (pre-v1.2 OpenCode)
	session, err = a.discoverFromFlatFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if session != nil {
		return session, nil
	}

	// Fall back to global SQLite (OpenCode v1.2–v1.15)
	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}

	projectID := GetProjectID(projectPath)
	return discoverFromSQLite(dataDir, projectID, projectPath)
}

// discoverFromLocalDB finds a recent session in the project-local
// .opencode/opencode.db file, used by OpenCode v1.16+.
func discoverFromLocalDB(projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(projectPath, ".opencode", "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Try several table/column combinations to handle schema variations.
	// OpenCode v1.16+ uses 'sessions' with 'updated_at'; older local schemas
	// may use 'session' with 'time_updated'.
	type candidate struct{ table, tsCol string }
	var sessionID, tsStr string
	for _, c := range []candidate{
		{"sessions", "updated_at"},
		{"session", "updated_at"},
		{"session", "time_updated"},
	} {
		q := fmt.Sprintf(
			`SELECT id, %s FROM %s ORDER BY %s DESC LIMIT 1;`,
			c.tsCol, c.table, c.tsCol,
		)
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, "\t", 2)
		sessionID = strings.TrimSpace(parts[0])
		if len(parts) == 2 {
			tsStr = strings.TrimSpace(parts[1])
		}
		break
	}

	if sessionID == "" {
		return nil, nil
	}

	// Reject sessions that are older than the recent timeout.
	if tsStr != "" && !localDBTimestampIsRecent(tsStr) {
		return nil, nil
	}

	transcriptData := localDBGetMessages(dbPath, sessionID)

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// localDBTimestampIsRecent returns true if ts represents a time within
// agent.RecentSessionTimeout. Returns true (proceed) when ts cannot be parsed.
func localDBTimestampIsRecent(ts string) bool {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, ts); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
	}
	// Handle Unix timestamps stored as integers (seconds or milliseconds).
	if n, err := strconv.ParseInt(ts, 10, 64); err == nil {
		var t time.Time
		if n > 1_000_000_000_000 { // value is in milliseconds (> year 2001 in ms)
			t = time.UnixMilli(n)
		} else {
			t = time.Unix(n, 0)
		}
		return time.Since(t) <= agent.RecentSessionTimeout
	}
	return true // proceed if unparseable
}

// localDBGetMessages reads messages for sessionID from the local SQLite DB
// and returns them as a JSON array that ParseTranscript can handle.
// It tries both the v1.16+ schema (messages/parts) and the intermediate
// schema (message/data), returning an empty array if neither works.
func localDBGetMessages(dbPath, sessionID string) []byte {
	// v1.16+ schema: 'messages' table with 'parts' column (JSON text).
	// We surface parts as 'content' so parseOpenCodeMessage can handle it.
	q := fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role, 'content', json(parts))) `+
			`FROM messages WHERE session_id='%s' ORDER BY created_at;`,
		sessionID,
	)
	cmd := exec.Command("sqlite3", dbPath, q)
	out, err := cmd.Output()
	if err == nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" && trimmed != "[null]" && trimmed != "[]" && trimmed != "null" {
			return []byte(trimmed)
		}
	}

	// Intermediate schema: 'message' table with 'data' column.
	q = fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) `+
			`FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sessionID,
	)
	cmd = exec.Command("sqlite3", dbPath, q)
	out, err = cmd.Output()
	if err == nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" && trimmed != "[null]" && trimmed != "[]" && trimmed != "null" {
			return []byte(trimmed)
		}
	}

	return []byte("[]")
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

// discoverFromSQLite queries the global OpenCode SQLite database for the most
// recent session. Used for OpenCode v1.2–v1.15 which store data globally.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Find most recent session for this project by project_id.
	sessionQuery := fmt.Sprintf(
		`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`,
		projectID,
	)
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, sessionQuery)
	sessionOutput, err := cmd.Output()
	sessionID := strings.TrimSpace(string(sessionOutput))

	// Fallback: some v1.16+ builds keep global storage but dropped project_id.
	// Get the most recent session and rely on the recency check below.
	if err != nil || sessionID == "" {
		fallbackQuery := `SELECT id FROM session ORDER BY time_updated DESC LIMIT 1;`
		cmd = exec.Command("sqlite3", "-separator", "\t", dbPath, fallbackQuery)
		sessionOutput, err = cmd.Output()
		if err != nil || strings.TrimSpace(string(sessionOutput)) == "" {
			return nil, nil
		}
		sessionID = strings.TrimSpace(string(sessionOutput))
	}

	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout)
	timeQuery := fmt.Sprintf(
		`SELECT time_updated FROM session WHERE id='%s';`,
		sessionID,
	)
	cmd = exec.Command("sqlite3", dbPath, timeQuery)
	timeOutput, err := cmd.Output()
	if err == nil {
		timeStr := strings.TrimSpace(string(timeOutput))
		if !localDBTimestampIsRecent(timeStr) {
			return nil, nil
		}
	}

	// Get messages for this session as a JSON array.
	// Try the current 'message/data' schema first, then 'messages/parts'.
	transcriptData := globalDBGetMessages(dbPath, sessionID)
	if transcriptData == nil {
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

// globalDBGetMessages reads messages from the global SQLite DB.
// It tries the intermediate schema (message/data) first, then the parts schema.
func globalDBGetMessages(dbPath, sessionID string) []byte {
	// Intermediate schema: 'message' table with 'data' column.
	q := fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) `+
			`FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sessionID,
	)
	cmd := exec.Command("sqlite3", dbPath, q)
	out, err := cmd.Output()
	if err == nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" && trimmed != "[null]" && trimmed != "[]" && trimmed != "null" {
			return []byte(trimmed)
		}
	}

	// v1.16+ schema: 'messages' table with 'parts' column.
	q = fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role, 'content', json(parts))) `+
			`FROM messages WHERE session_id='%s' ORDER BY created_at;`,
		sessionID,
	)
	cmd = exec.Command("sqlite3", dbPath, q)
	out, err = cmd.Output()
	if err == nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed != "" && trimmed != "[null]" && trimmed != "[]" && trimmed != "null" {
			return []byte(trimmed)
		}
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
// It handles the standard content format, the OpenCode parts format
// ([{"type":"text","data":{"text":"..."}}]), and the nested message format.
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

	// Try "content" field — may be a string, standard content blocks, or parts format.
	if contentRaw, ok := raw["content"]; ok {
		// Try as plain string
		var text string
		if err := json.Unmarshal(contentRaw, &text); err == nil && text != "" {
			msg.Content = []agent.ContentBlock{{Type: "text", Text: text}}
			return msg
		}

		// Try as a JSON array
		var rawArr []json.RawMessage
		if err := json.Unmarshal(contentRaw, &rawArr); err == nil && len(rawArr) > 0 {
			// First pass: try as standard ContentBlock array.
			var blocks []agent.ContentBlock
			if err := json.Unmarshal(contentRaw, &blocks); err == nil {
				hasUsable := false
				for _, b := range blocks {
					if b.Text != "" || b.Type == "tool_use" || b.Type == "tool_result" {
						hasUsable = true
						break
					}
				}
				if hasUsable {
					msg.Content = blocks
					return msg
				}
			}

			// Second pass: try OpenCode parts format
			// [{"type":"text","data":{"text":"..."}}, ...]
			var textBlocks []agent.ContentBlock
			for _, rawPart := range rawArr {
				var p struct {
					Type string `json:"type"`
					Data struct {
						Text string `json:"text"`
					} `json:"data"`
				}
				if err := json.Unmarshal(rawPart, &p); err == nil &&
					p.Type == "text" && p.Data.Text != "" {
					textBlocks = append(textBlocks, agent.ContentBlock{Type: "text", Text: p.Data.Text})
				}
			}
			if len(textBlocks) > 0 {
				msg.Content = textBlocks
				return msg
			}
		}
	}

	// Try "parts" field directly (when present as a top-level key).
	if partsRaw, ok := raw["parts"]; ok {
		// parts may be stored as a JSON string (TEXT column) — unwrap if needed.
		var partsData json.RawMessage
		var partsStr string
		if err := json.Unmarshal(partsRaw, &partsStr); err == nil {
			partsData = json.RawMessage(partsStr)
		} else {
			partsData = partsRaw
		}

		var rawParts []json.RawMessage
		if err := json.Unmarshal(partsData, &rawParts); err == nil {
			var textBlocks []agent.ContentBlock
			for _, rawPart := range rawParts {
				var p struct {
					Type string `json:"type"`
					Data struct {
						Text string `json:"text"`
					} `json:"data"`
				}
				if err := json.Unmarshal(rawPart, &p); err == nil &&
					p.Type == "text" && p.Data.Text != "" {
					textBlocks = append(textBlocks, agent.ContentBlock{Type: "text", Text: p.Data.Text})
				}
			}
			if len(textBlocks) > 0 {
				msg.Content = textBlocks
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
