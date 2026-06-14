```go
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
// It first tries flat file storage (pre-v1.2), then falls back to SQLite across
// all candidate data directories (data dir and config dir).
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// Try flat file storage first (pre-v1.2 OpenCode)
	session, err := a.discoverFromFlatFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if session != nil {
		return session, nil
	}

	// Try SQLite in all candidate directories (data dir and config dir).
	// OpenCode 1.17+ may store the database in either location.
	projectID := GetProjectID(projectPath)
	for _, dataDir := range CandidateDataDirs() {
		session, err := discoverFromSQLite(dataDir, projectID, projectPath)
		if err != nil {
			continue
		}
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

// runSQLiteQuery executes a SQLite query and returns trimmed output, or "" on error/empty.
func runSQLiteQuery(dbPath, query string) string {
	cmd := exec.Command("sqlite3", dbPath, query)
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	result := strings.TrimSpace(string(output))
	if result == "null" {
		return ""
	}
	return result
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
// It tries multiple SQL strategies for compatibility with different OpenCode versions.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Find session ID using multiple strategies for cross-version compatibility.
	sessionID := findSessionID(dbPath, projectID, projectPath)
	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout)
	if !isSessionRecent(dbPath, sessionID) {
		return nil, nil
	}

	// Get messages for this session using multiple column name strategies
	transcriptData := fetchTranscriptData(dbPath, sessionID)

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// findSessionID tries multiple SQL strategies to locate the most recent session.
// Handles schema differences between OpenCode versions:
//   - Older: project_id = git root commit hash, sorted by time_updated column
//   - Newer (1.17+): time_updated may not exist (use rowid); project_id may be path
func findSessionID(dbPath, projectID, projectPath string) string {
	// Strategy 1: original schema — project_id = git hash, ORDER BY time_updated
	if id := runSQLiteQuery(dbPath, fmt.Sprintf(
		`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`,
		projectID,
	)); id != "" {
		return id
	}

	// Strategy 2: project_id = git hash, ORDER BY rowid (handles missing time_updated)
	if id := runSQLiteQuery(dbPath, fmt.Sprintf(
		`SELECT id FROM session WHERE project_id='%s' ORDER BY rowid DESC LIMIT 1;`,
		projectID,
	)); id != "" {
		return id
	}

	// Strategy 3: project_id = actual project path (OpenCode 1.17+ may store path)
	if projectPath != "" && projectPath != projectID {
		if id := runSQLiteQuery(dbPath, fmt.Sprintf(
			`SELECT id FROM session WHERE project_id='%s' ORDER BY rowid DESC LIMIT 1;`,
			projectPath,
		)); id != "" {
			return id
		}
	}

	// Strategy 4: most recent session in the database regardless of project
	if id := runSQLiteQuery(dbPath,
		`SELECT id FROM session ORDER BY rowid DESC LIMIT 1;`,
	); id != "" {
		return id
	}

	return ""
}

// isSessionRecent checks if a session falls within the recent session timeout.
// Tries multiple column and format strategies across OpenCode versions.
func isSessionRecent(dbPath, sessionID string) bool {
	// Try time_updated column (original schema)
	timeStr := runSQLiteQuery(dbPath, fmt.Sprintf(
		`SELECT time_updated FROM session WHERE id='%s';`, sessionID,
	))

	// Try JSON time object: SELECT json_extract(time, '$.updated')
	if timeStr == "" {
		timeStr = runSQLiteQuery(dbPath, fmt.Sprintf(
			`SELECT json_extract(time, '$.updated') FROM session WHERE id='%s';`, sessionID,
		))
	}

	// Try time column directly (may be a JSON object or ISO string)
	if timeStr == "" {
		timeStr = runSQLiteQuery(dbPath, fmt.Sprintf(
			`SELECT time FROM session WHERE id='%s';`, sessionID,
		))
		// If it looks like a JSON object, try to extract 'updated'
		if timeStr != "" && strings.HasPrefix(timeStr, "{") {
			var timeObj map[string]string
			if err := json.Unmarshal([]byte(timeStr), &timeObj); err == nil {
				if v := timeObj["updated"]; v != "" {
					timeStr = v
				} else if v := timeObj["created"]; v != "" {
					timeStr = v
				}
			}
		}
	}

	if timeStr == "" {
		return true // Assume recent if we can't determine time
	}

	// Strip JSON string quotes if present
	timeStr = strings.Trim(timeStr, `"`)

	// Try RFC3339 variants
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, timeStr); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
	}

	// Try as Unix timestamp (milliseconds > 1e12, seconds otherwise)
	var ts int64
	if n, err := fmt.Sscanf(timeStr, "%d", &ts); n == 1 && err == nil {
		var t time.Time
		if ts > 1_000_000_000_000 {
			t = time.UnixMilli(ts)
		} else {
			t = time.Unix(ts, 0)
		}
		return time.Since(t) <= agent.RecentSessionTimeout
	}

	return true // Proceed if time format is unknown
}

// fetchTranscriptData tries multiple SQL strategies to retrieve message data.
// Returns nil only when no strategy succeeds; returns []byte("[]") as a minimal
// fallback so that a git note can still be created for the session.
func fetchTranscriptData(dbPath, sessionID string) []byte {
	isInvalid := func(s string) bool {
		return s == "" || s == "[null]" || s == "[]"
	}

	// Strategy 1: original schema — data column with json_patch
	if data := runSQLiteQuery(dbPath, fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sessionID,
	)); !isInvalid(data) {
		return []byte(data)
	}

	// Strategy 2: parts column (OpenCode 1.17+ stores content in 'parts')
	if data := runSQLiteQuery(dbPath, fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role, 'content', parts)) FROM message WHERE session_id='%s' ORDER BY rowid;`,
		sessionID,
	)); !isInvalid(data) {
		return []byte(data)
	}

	// Strategy 3: content column fallback
	if data := runSQLiteQuery(dbPath, fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role, 'content', content)) FROM message WHERE session_id='%s' ORDER BY rowid;`,
		sessionID,
	)); !isInvalid(data) {
		return []byte(data)
	}

	// Strategy 4: minimal — just id and role (sufficient for message count)
	if data := runSQLiteQuery(dbPath, fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role)) FROM message WHERE session_id='%s' ORDER BY rowid;`,
		sessionID,
	)); !isInvalid(data) {
		return []byte(data)
	}

	// Minimal valid transcript: session found but messages unreadable
	return []byte("[]")
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

	// Try "parts" field (OpenCode 1.17+ stores message content in 'parts')
	if partsRaw, ok := raw["parts"]; ok {
		// Try as array of content blocks
		var blocks []agent.ContentBlock
		if err := json.Unmarshal(partsRaw, &blocks); err == nil && len(blocks) > 0 {
			msg.Content = blocks
			return msg
		}
		// Try as string
		var text string
		if err := json.Unmarshal(partsRaw, &text); err == nil && text != "" {
			msg.Content = []agent.ContentBlock{{Type: "text", Text: text}}
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
```
