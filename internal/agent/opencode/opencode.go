```go
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
// It handles multiple schema versions: pre-v1.17 (time_updated/data columns) and
// v1.17+ (JSON time column and parts column).
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Find most recent session for this project.
	// Try multiple query variants to handle schema differences across opencode versions:
	// - pre-v1.17: time_updated INTEGER column
	// - v1.17+: time TEXT column containing JSON {"created": ms, "updated": ms}
	sessionID := ""
	for _, q := range []string{
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY json_extract(time, '$.updated') DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY rowid DESC LIMIT 1;`, projectID),
	} {
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
		out, err := cmd.Output()
		if err == nil && strings.TrimSpace(string(out)) != "" {
			sessionID = strings.TrimSpace(string(out))
			break
		}
	}
	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout).
	// Try multiple time query variants and time formats including Unix milliseconds.
	if !isSessionRecent(dbPath, sessionID) {
		return nil, nil
	}

	// Get messages for this session as a JSON array.
	// Try multiple query variants to handle schema changes:
	// - pre-v1.17: data TEXT blob column, time_created INTEGER column
	// - v1.17+: role TEXT + parts TEXT (JSON array) + time TEXT (JSON object)
	var transcriptData []byte
	for _, q := range []string{
		// Old schema: data blob + time_created ordering
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
		// Old schema: data blob + rowid ordering (if time_created column missing)
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY rowid;`, sessionID),
		// New schema (v1.17+): parts + JSON time ordering
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts), 'time', json(time))) FROM message WHERE session_id='%s' ORDER BY json_extract(time, '$.created');`, sessionID),
		// New schema: parts + rowid ordering (if json_extract on time fails)
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts))) FROM message WHERE session_id='%s' ORDER BY rowid;`, sessionID),
		// Minimal fallback: just role info
		fmt.Sprintf(`SELECT json_group_array(json_object('id', id, 'role', role)) FROM message WHERE session_id='%s' ORDER BY rowid;`, sessionID),
	} {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		td := strings.TrimSpace(string(out))
		if td == "[null]" || td == "[]" {
			continue
		}
		transcriptData = []byte(td)
		break
	}

	if transcriptData == nil {
		return nil, nil
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "", // no file path for SQLite
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// isSessionRecent returns true if the session is recent enough to be considered active,
// or if we cannot determine the time (better to try than skip).
func isSessionRecent(dbPath, sessionID string) bool {
	for _, q := range []string{
		fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT json_extract(time, '$.updated') FROM session WHERE id='%s';`, sessionID),
	} {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		timeStr := strings.TrimSpace(string(out))
		if timeStr == "" {
			continue
		}

		// Try RFC3339 and other string formats
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
			if t, parseErr := time.Parse(layout, timeStr); parseErr == nil {
				return time.Since(t) <= agent.RecentSessionTimeout
			}
		}
		// Try Unix milliseconds (integer)
		if ms, parseErr := strconv.ParseInt(timeStr, 10, 64); parseErr == nil {
			return time.Since(time.UnixMilli(ms)) <= agent.RecentSessionTimeout
		}
		// Got a time string but couldn't parse it — proceed
		return true
	}
	// All time queries failed — proceed rather than skip
	return true
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

	// Parse timestamp — handle both string and integer (Unix ms) formats
	if timeRaw, ok := raw["time"]; ok {
		var timeObj map[string]json.RawMessage
		if err := json.Unmarshal(timeRaw, &timeObj); err == nil {
			if createdRaw, ok := timeObj["created"]; ok {
				// Try as string first
				var createdStr string
				if err := json.Unmarshal(createdRaw, &createdStr); err == nil {
					entry.Timestamp = createdStr
				} else {
					// Try as Unix milliseconds (integer)
					var ms float64
					if err := json.Unmarshal(createdRaw, &ms); err == nil {
						entry.Timestamp = time.UnixMilli(int64(ms)).Format(time.RFC3339)
					}
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

	// Try "parts" as array (newer opencode versions use parts instead of content)
	if partsRaw, ok := raw["parts"]; ok {
		var parts []json.RawMessage
		if err := json.Unmarshal(partsRaw, &parts); err == nil && len(parts) > 0 {
			for _, partData := range parts {
				var part struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(partData, &part); err == nil {
					if part.Type == "text" && part.Text != "" {
						msg.Content = append(msg.Content, agent.ContentBlock{Type: "text", Text: part.Text})
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
```
