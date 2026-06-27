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
// It checks (in order): plugin marker file, flat file storage (pre-v1.2), SQLite (v1.2+).
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// Try marker file first (written by plugin on first tool call)
	if info := discoverFromMarkerFile(projectPath); info != nil {
		return info, nil
	}

	// Try flat file storage (pre-v1.2 OpenCode)
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

// discoverFromMarkerFile checks the .shiftlog/opencode-session.json marker file
// written by the plugin on the first tool call of each session.
func discoverFromMarkerFile(projectPath string) *agent.SessionInfo {
	markerPath := filepath.Join(projectPath, ".shiftlog", "opencode-session.json")
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return nil
	}

	var marker struct {
		SessionID  string `json:"session_id"`
		ProjectDir string `json:"project_dir"`
		StartedAt  string `json:"started_at"`
	}
	if err := json.Unmarshal(data, &marker); err != nil || marker.SessionID == "" {
		return nil
	}

	// Use file modification time as the activity timestamp
	info, err := os.Stat(markerPath)
	if err != nil || time.Since(info.ModTime()) > agent.RecentSessionTimeout {
		return nil
	}

	// Validate project path when present
	if marker.ProjectDir != "" && !agent.PathsEqual(marker.ProjectDir, projectPath) {
		return nil
	}

	// Try to get transcript data from SQLite using the known session ID
	var transcriptData []byte
	if dataDir, err := GetDataDir(); err == nil {
		transcriptData = queryTranscriptFromSQLite(dataDir, marker.SessionID)
	}
	if len(transcriptData) == 0 {
		transcriptData = []byte("[]") // minimal valid JSON array
	}

	startedAt := marker.StartedAt
	if startedAt == "" {
		startedAt = info.ModTime().Format(time.RFC3339)
	}

	return &agent.SessionInfo{
		SessionID:      marker.SessionID,
		TranscriptPath: "",
		StartedAt:      startedAt,
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}
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
// It tries multiple column name conventions to handle schema changes across versions.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Find most recent session for this project (try multiple column name conventions)
	sessionID := querySQLiteSessionID(dbPath, projectID)
	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout)
	if !isSQLiteSessionRecent(dbPath, sessionID) {
		return nil, nil
	}

	// Get transcript data, falling back to empty array if unavailable
	transcriptData := queryTranscriptFromSQLite(dataDir, sessionID)
	if len(transcriptData) == 0 {
		transcriptData = []byte("[]")
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// querySQLiteSessionID finds the most recent session for a project by trying
// multiple column name conventions to handle opencode schema changes across versions.
func querySQLiteSessionID(dbPath, projectID string) string {
	candidates := []string{
		// snake_case columns (older schema)
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
		// camelCase columns (newer schema)
		fmt.Sprintf(`SELECT id FROM session WHERE projectId='%s' ORDER BY updatedAt DESC LIMIT 1;`, projectID),
		// mixed combinations
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY updatedAt DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE projectId='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY updated DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE projectId='%s' ORDER BY updated DESC LIMIT 1;`, projectID),
	}

	for _, q := range candidates {
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		if id := strings.TrimSpace(string(output)); id != "" {
			return id
		}
	}
	return ""
}

// isSQLiteSessionRecent checks if the given session was updated within the timeout.
// Returns true if the session is recent or if the timestamp cannot be determined.
func isSQLiteSessionRecent(dbPath, sessionID string) bool {
	timeQueries := []string{
		fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT updatedAt FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT updated FROM session WHERE id='%s';`, sessionID),
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

		// Integer milliseconds since epoch (newer opencode versions)
		if isAllDigits(timeStr) {
			ms := parseIntMS(timeStr)
			if ms > 0 {
				return time.Since(time.UnixMilli(ms)) <= agent.RecentSessionTimeout
			}
		}

		// ISO string formats
		for _, layout := range []string{
			time.RFC3339Nano,
			"2006-01-02T15:04:05.000Z",
			"2006-01-02T15:04:05Z",
			"2006-01-02 15:04:05",
		} {
			if t, err := time.Parse(layout, timeStr); err == nil {
				return time.Since(t) <= agent.RecentSessionTimeout
			}
		}
	}

	// Cannot determine time — proceed optimistically
	return true
}

// queryTranscriptFromSQLite retrieves messages for a session from SQLite,
// trying multiple schema conventions. Returns nil if unavailable.
func queryTranscriptFromSQLite(dataDir, sessionID string) []byte {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}

	// Original schema: data blob, snake_case column names
	q1 := fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sessionID,
	)
	if data := runSQLiteScalar(dbPath, q1); data != nil {
		return data
	}

	// camelCase session reference, data blob
	q2 := fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE sessionId='%s' ORDER BY createdAt;`,
		sessionID,
	)
	if data := runSQLiteScalar(dbPath, q2); data != nil {
		return data
	}

	// camelCase with separate role/content columns (newer schema)
	q3 := fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role, 'content', content)) FROM message WHERE sessionId='%s' ORDER BY createdAt;`,
		sessionID,
	)
	if data := runSQLiteScalar(dbPath, q3); data != nil {
		return data
	}

	// snake_case with separate role/content columns
	q4 := fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role, 'content', content)) FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sessionID,
	)
	if data := runSQLiteScalar(dbPath, q4); data != nil {
		return data
	}

	return nil
}

// runSQLiteScalar executes a scalar SQLite query and returns the output if
// non-empty and not a null/empty JSON array. Returns nil otherwise.
func runSQLiteScalar(dbPath, query string) []byte {
	cmd := exec.Command("sqlite3", dbPath, query)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}
	out := strings.TrimSpace(string(output))
	if out == "" || out == "[null]" || out == "[]" {
		return nil
	}
	return []byte(out)
}

// isAllDigits reports whether s consists entirely of ASCII digit characters.
func isAllDigits(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseIntMS parses a decimal integer string as milliseconds, returning 0 on failure.
func parseIntMS(s string) int64 {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
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

	// Try "message" field
	if msgRaw, ok := raw["message"]; ok {
		var innerMsg agent.Message
		if err := json.Unmarshal(msgRaw, &innerMsg); err == nil {
			return &innerMsg
		}
	}

	return msg
}
