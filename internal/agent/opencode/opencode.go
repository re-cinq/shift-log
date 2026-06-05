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

	// Use inline transcript data from the plugin SDK client if available
	var transcriptData []byte
	if hook.TranscriptData != "" {
		transcriptData = []byte(hook.TranscriptData)
	}

	// If no inline data, try to fetch from SQLite (v1.16.0+ stores in SQLite)
	if len(transcriptData) == 0 && hook.SessionID != "" {
		transcriptData = fetchTranscriptFromSQLite(hook.ProjectDir, hook.DataDir, hook.SessionID)
	}

	// Fall back to flat file transcript path (v1.14.x)
	transcriptPath := ""
	if len(transcriptData) == 0 && hook.DataDir != "" && hook.SessionID != "" {
		transcriptPath = filepath.Join(hook.DataDir, "storage", "message", hook.SessionID)
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
// It first tries flat file storage (pre-v1.16), then falls back to SQLite.
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// Try flat file storage first (pre-v1.16 OpenCode)
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
// It tries the project-local database (v1.16.0+) first, then the global database (v1.14.x).
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// v1.16.0+: project-local database at .opencode/opencode.db
	projectDBPath := filepath.Join(projectPath, ".opencode", "opencode.db")
	// v1.14.x: global database at $XDG_DATA_HOME/opencode/opencode.db
	globalDBPath := filepath.Join(dataDir, "opencode.db")

	var dbPath string
	if _, err := os.Stat(projectDBPath); err == nil {
		dbPath = projectDBPath
	} else if _, err := os.Stat(globalDBPath); err == nil {
		dbPath = globalDBPath
	} else {
		return nil, nil
	}

	// Detect schema version: 'sessions' table = v1.16.0+, 'session' table = v1.14.x
	detectQ := `SELECT name FROM sqlite_master WHERE type='table' AND name='sessions';`
	cmd := exec.Command("sqlite3", dbPath, detectQ)
	detectOut, _ := cmd.Output()

	if strings.TrimSpace(string(detectOut)) == "sessions" {
		return discoverFromSQLiteNewSchema(dbPath, projectPath)
	}
	return discoverFromSQLiteOldSchema(dbPath, projectID, projectPath)
}

// discoverFromSQLiteNewSchema handles OpenCode v1.16.0+ schema.
// Tables: sessions (id, updated_at INTEGER ms), messages (id, session_id, role, parts, model, created_at INTEGER ms)
func discoverFromSQLiteNewSchema(dbPath, projectPath string) (*agent.SessionInfo, error) {
	// Find most recent session
	sessionQ := `SELECT id, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 1;`
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, sessionQ)
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}

	cols := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	sessionID := cols[0]
	if sessionID == "" {
		return nil, nil
	}

	// Check recency: updated_at is unix milliseconds
	if len(cols) > 1 {
		ms, err := strconv.ParseInt(cols[1], 10, 64)
		if err == nil {
			t := time.Unix(ms/1000, (ms%1000)*int64(time.Millisecond))
			if time.Since(t) > agent.RecentSessionTimeout {
				return nil, nil
			}
		}
	}

	// Get messages as JSONL using json_object to safely embed parts
	msgQ := fmt.Sprintf(
		`SELECT json_object('id',id,'role',role,'parts',json(parts),'model',coalesce(model,'')) FROM messages WHERE session_id='%s' ORDER BY created_at;`,
		sessionID,
	)
	cmd = exec.Command("sqlite3", dbPath, msgQ)
	msgOut, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	transcriptData := []byte(strings.TrimSpace(string(msgOut)))
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

// discoverFromSQLiteOldSchema handles OpenCode v1.14.x schema.
// Tables: session (id, project_id, time_updated), message (id, session_id, data, time_created)
func discoverFromSQLiteOldSchema(dbPath, projectID, projectPath string) (*agent.SessionInfo, error) {
	sessionQ := fmt.Sprintf(
		`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`,
		projectID,
	)
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, sessionQ)
	out, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	sessionID := strings.TrimSpace(string(out))

	// Check recency using time_updated
	timeQ := fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID)
	cmd = exec.Command("sqlite3", dbPath, timeQ)
	timeOut, err := cmd.Output()
	if err == nil {
		timeStr := strings.TrimSpace(string(timeOut))
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, timeStr); err == nil {
				if time.Since(t) > agent.RecentSessionTimeout {
					return nil, nil
				}
				break
			}
		}
	}

	// Get messages for this session as a JSON array
	msgQ := fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sessionID,
	)
	cmd = exec.Command("sqlite3", dbPath, msgQ)
	msgOut, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	transcriptData := []byte(strings.TrimSpace(string(msgOut)))
	if string(transcriptData) == "[null]" || string(transcriptData) == "[]" {
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

// fetchTranscriptFromSQLite retrieves transcript data from the OpenCode SQLite database.
// It tries both the project-local and global database paths, and both schema versions.
func fetchTranscriptFromSQLite(projectDir, dataDir, sessionID string) []byte {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}

	// Determine candidate database paths
	var candidates []string
	if projectDir != "" {
		candidates = append(candidates, filepath.Join(projectDir, ".opencode", "opencode.db"))
	}
	if dataDir != "" {
		candidates = append(candidates, filepath.Join(dataDir, "opencode.db"))
	}

	for _, dbPath := range candidates {
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}

		// Try new schema (messages table with parts)
		detectQ := `SELECT name FROM sqlite_master WHERE type='table' AND name='messages';`
		cmd := exec.Command("sqlite3", dbPath, detectQ)
		detectOut, _ := cmd.Output()
		if strings.TrimSpace(string(detectOut)) == "messages" {
			msgQ := fmt.Sprintf(
				`SELECT json_object('id',id,'role',role,'parts',json(parts),'model',coalesce(model,'')) FROM messages WHERE session_id='%s' ORDER BY created_at;`,
				sessionID,
			)
			cmd = exec.Command("sqlite3", dbPath, msgQ)
			msgOut, err := cmd.Output()
			if err == nil && strings.TrimSpace(string(msgOut)) != "" {
				return []byte(strings.TrimSpace(string(msgOut)))
			}
		}

		// Try old schema (message table with data)
		msgQ := fmt.Sprintf(
			`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
			sessionID,
		)
		cmd = exec.Command("sqlite3", dbPath, msgQ)
		msgOut, err := cmd.Output()
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(string(msgOut))
		if trimmed != "" && trimmed != "[null]" && trimmed != "[]" {
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

	// Try "parts" field (OpenCode v1.16.0+ format)
	if partsRaw, ok := raw["parts"]; ok {
		var parts []struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(partsRaw, &parts); err == nil && len(parts) > 0 {
			var blocks []agent.ContentBlock
			for _, p := range parts {
				switch p.Type {
				case "text":
					var d struct {
						Text string `json:"text"`
					}
					if err := json.Unmarshal(p.Data, &d); err == nil && d.Text != "" {
						blocks = append(blocks, agent.ContentBlock{Type: "text", Text: d.Text})
					}
				case "tool_call":
					var d struct {
						ID   string `json:"id"`
						Name string `json:"name"`
					}
					if err := json.Unmarshal(p.Data, &d); err == nil && d.Name != "" {
						blocks = append(blocks, agent.ContentBlock{
							Type: "tool_use",
							ID:   d.ID,
							Name: d.Name,
						})
					}
				case "tool_result":
					blocks = append(blocks, agent.ContentBlock{Type: "tool_result"})
				}
			}
			if len(blocks) > 0 {
				msg.Content = blocks
				return msg
			}
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
