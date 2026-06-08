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

	transcriptPath := ""
	if hook.DataDir != "" && hook.SessionID != "" {
		transcriptPath = filepath.Join(hook.DataDir, "storage", "message", hook.SessionID)
	}

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
func (a *Agent) ParseTranscript(r io.Reader) (*agent.Transcript, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}

	var entries []agent.TranscriptEntry

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
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}

	if info.IsDir() {
		return a.parseMessageDir(path)
	}

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
// It tries flat file storage (pre-v1.2), project-local SQLite,
// then the XDG data directory SQLite.
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

	// Try project-local SQLite (.opencode/opencode.db in project dir)
	localDB := filepath.Join(projectPath, ".opencode", "opencode.db")
	if _, err := os.Stat(localDB); err == nil {
		if s, err := discoverFromSQLiteDB(localDB, projectID, projectPath); s != nil || err != nil {
			return s, err
		}
	}

	// Try XDG data directory SQLite
	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}
	return discoverFromSQLiteDB(filepath.Join(dataDir, "opencode.db"), projectID, projectPath)
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

	msgDir, _ := GetMessageDir(bestSessionID)

	return &agent.SessionInfo{
		SessionID:      bestSessionID,
		TranscriptPath: msgDir,
		StartedAt:      bestModTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
	}, nil
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	return discoverFromSQLiteDB(filepath.Join(dataDir, "opencode.db"), projectID, projectPath)
}

// discoverFromSQLiteDB queries a specific OpenCode SQLite database file.
// It supports both the v1.16+ schema (sessions/updated_at) and the
// pre-v1.16 schema (session/project_id/time_updated).
func discoverFromSQLiteDB(dbPath, projectID, projectPath string) (*agent.SessionInfo, error) {
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionID, isNewSchema := sqliteFindSession(dbPath, projectID)
	if sessionID == "" {
		return nil, nil
	}

	if !sqliteCheckRecent(dbPath, sessionID, isNewSchema) {
		return nil, nil
	}

	transcriptData := sqliteGetMessages(dbPath, sessionID, isNewSchema)
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

// sqliteFindSession finds the most recent session ID in the database.
// Tries the v1.16+ schema (sessions/updated_at) first, then pre-v1.16 (session/project_id/time_updated).
// Returns the session ID and whether the new schema was used.
func sqliteFindSession(dbPath, projectID string) (string, bool) {
	// v1.16+ schema: "sessions" table, ordered by updated_at, no project_id filter
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath,
		"SELECT id FROM sessions ORDER BY updated_at DESC LIMIT 1;")
	if out, err := cmd.Output(); err == nil {
		if id := strings.TrimSpace(string(out)); id != "" {
			return id, true
		}
	}

	// pre-v1.16 schema: "session" table with project_id and time_updated
	q := fmt.Sprintf(
		"SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;",
		projectID,
	)
	cmd = exec.Command("sqlite3", "-separator", "\t", dbPath, q)
	if out, err := cmd.Output(); err == nil {
		if id := strings.TrimSpace(string(out)); id != "" {
			return id, false
		}
	}

	return "", false
}

// sqliteCheckRecent returns true if the session was updated within RecentSessionTimeout.
func sqliteCheckRecent(dbPath, sessionID string, isNewSchema bool) bool {
	var q string
	if isNewSchema {
		q = fmt.Sprintf("SELECT updated_at FROM sessions WHERE id='%s';", sessionID)
	} else {
		q = fmt.Sprintf("SELECT time_updated FROM session WHERE id='%s';", sessionID)
	}

	cmd := exec.Command("sqlite3", dbPath, q)
	out, err := cmd.Output()
	if err != nil {
		return true
	}

	timeStr := strings.TrimSpace(string(out))
	if timeStr == "" {
		return true
	}

	if isNewSchema {
		// v1.16+: integer Unix timestamp (seconds or milliseconds)
		if ts, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
			var t time.Time
			if ts > 1_000_000_000_000 {
				t = time.UnixMilli(ts)
			} else {
				t = time.Unix(ts, 0)
			}
			return time.Since(t) <= agent.RecentSessionTimeout
		}
		return true
	}

	// pre-v1.16: string timestamp in various formats
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, timeStr); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
	}
	return true
}

// sqliteGetMessages retrieves messages for a session as a JSON array.
func sqliteGetMessages(dbPath, sessionID string, isNewSchema bool) []byte {
	var q string
	if isNewSchema {
		// v1.16+: messages table, parts column (JSON array of typed content parts)
		q = fmt.Sprintf(
			"SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(coalesce(parts, '[]')))) FROM messages WHERE session_id='%s' ORDER BY created_at;",
			sessionID,
		)
	} else {
		// pre-v1.16: message table, data column
		q = fmt.Sprintf(
			"SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;",
			sessionID,
		)
	}

	cmd := exec.Command("sqlite3", dbPath, q)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "[null]" || trimmed == "[]" || trimmed == "" {
		return nil
	}
	return []byte(trimmed)
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

	// Parse timestamp: try "time.created" object (legacy), then "created_at" integer (v1.16+)
	if timeRaw, ok := raw["time"]; ok {
		var timeObj struct {
			Created string `json:"created"`
		}
		if err := json.Unmarshal(timeRaw, &timeObj); err == nil {
			entry.Timestamp = timeObj.Created
		}
	}
	if entry.Timestamp == "" {
		if caRaw, ok := raw["created_at"]; ok {
			var ts int64
			if err := json.Unmarshal(caRaw, &ts); err == nil {
				var t time.Time
				if ts > 1_000_000_000_000 {
					t = time.UnixMilli(ts)
				} else {
					t = time.Unix(ts, 0)
				}
				entry.Timestamp = t.UTC().Format(time.RFC3339)
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

	// Try "parts" field (OpenCode v1.16+ format: typed content parts array)
	if partsRaw, ok := raw["parts"]; ok {
		var parts []struct {
			Type string `json:"type"`
			Data struct {
				Text string `json:"text"`
			} `json:"data"`
		}
		if err := json.Unmarshal(partsRaw, &parts); err == nil {
			var content []agent.ContentBlock
			for _, p := range parts {
				if p.Type == "text" && p.Data.Text != "" {
					content = append(content, agent.ContentBlock{Type: "text", Text: p.Data.Text})
				}
			}
			if len(content) > 0 {
				msg.Content = content
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
