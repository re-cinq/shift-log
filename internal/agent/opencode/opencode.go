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

	// Build transcript path: prefer session_diff (v1.14+), fall back to message dir (pre-v1.14).
	transcriptPath := ""
	if hook.DataDir != "" && hook.SessionID != "" {
		// v1.14+: sessions stored as session_diff/<sessionID>.json
		sessionDiffPath := filepath.Join(hook.DataDir, "storage", "session_diff", hook.SessionID+".json")
		if _, err := os.Stat(sessionDiffPath); err == nil {
			transcriptPath = sessionDiffPath
		} else {
			// pre-v1.14: messages stored in storage/message/<sessionID>/
			transcriptPath = filepath.Join(hook.DataDir, "storage", "message", hook.SessionID)
		}
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
// Handles JSON array, JSONL, and session_diff wrapper formats.
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

	// Try as session_diff object with a "messages" key
	if strings.HasPrefix(trimmed, "{") {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &obj); err == nil {
			if msgsRaw, ok := obj["messages"]; ok {
				var messages []json.RawMessage
				if err := json.Unmarshal(msgsRaw, &messages); err == nil {
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
		}
	}

	// Try as JSONL
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

// ParseTranscriptFile parses an OpenCode session from a file or message directory.
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
// Tries flat files (pre-v1.2), session_diff files (v1.14+), then SQLite (v1.2+).
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// Try legacy flat file storage (pre-v1.2 OpenCode)
	session, err := a.discoverFromFlatFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if session != nil {
		return session, nil
	}

	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}

	// Try session_diff directory (OpenCode v1.14+)
	session, err = a.discoverFromSessionDiff(dataDir, projectPath)
	if err == nil && session != nil {
		return session, nil
	}

	// Fall back to SQLite (OpenCode v1.2+)
	projectID := GetProjectID(projectPath)
	return discoverFromSQLite(dataDir, projectID, projectPath)
}

// discoverFromFlatFiles tries the legacy flat file session discovery (pre-v1.2).
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

// discoverFromSessionDiff scans the session_diff directory for recent sessions (v1.14+).
// Sessions are stored as storage/session_diff/<sessionID>.json without project subdirectory.
func (a *Agent) discoverFromSessionDiff(dataDir, projectPath string) (*agent.SessionInfo, error) {
	sessionDiffDir := filepath.Join(dataDir, "storage", "session_diff")
	entries, err := os.ReadDir(sessionDiffDir)
	if err != nil {
		return nil, nil
	}

	now := time.Now()
	var bestSessionID string
	var bestModTime time.Time
	var bestData []byte

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		modTime := info.ModTime()
		if now.Sub(modTime) > agent.RecentSessionTimeout {
			continue
		}

		filePath := filepath.Join(sessionDiffDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			continue
		}

		if !sessionDiffBelongsToProject(data, projectPath) {
			continue
		}

		if bestSessionID == "" || modTime.After(bestModTime) {
			bestSessionID = strings.TrimSuffix(entry.Name(), ".json")
			bestModTime = modTime
			bestData = data
		}
	}

	if bestSessionID == "" {
		return nil, nil
	}

	transcriptData := extractTranscriptFromSessionDiff(bestData)

	return &agent.SessionInfo{
		SessionID:      bestSessionID,
		TranscriptPath: "",
		StartedAt:      bestModTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// sessionDiffBelongsToProject checks if a session_diff JSON file belongs to the given project.
func sessionDiffBelongsToProject(data []byte, projectPath string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}

	for _, field := range []string{"path", "directory", "workdir", "projectPath", "project_path"} {
		if val, ok := raw[field]; ok {
			var p string
			if err := json.Unmarshal(val, &p); err == nil && p != "" {
				if agent.PathsEqual(p, projectPath) {
					return true
				}
			}
		}
	}
	return false
}

// extractTranscriptFromSessionDiff extracts message data from a session_diff file.
func extractTranscriptFromSessionDiff(data []byte) []byte {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return data
	}

	// Prefer explicit messages array
	if msgs, ok := raw["messages"]; ok {
		return []byte(msgs)
	}

	// Return whole file as transcript data for ParseTranscript to handle
	return data
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Try multiple query variants to handle schema differences across versions.
	// v1.2-v1.13: project_id=<root-commit-hash>, ordered by time_updated
	// v1.14+: may use different column names or ordering
	sessionID := sqliteFindSession(dbPath, projectID, projectPath)
	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent
	if !sqliteSessionIsRecent(dbPath, sessionID) {
		return nil, nil
	}

	// Get transcript: first try session_diff file (v1.14+), then SQLite messages (pre-v1.14)
	transcriptData := loadTranscriptForSession(dataDir, dbPath, sessionID)
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

// sqliteFindSession tries multiple query variants to find the most recent session.
func sqliteFindSession(dbPath, projectID, projectPath string) string {
	queries := []string{
		// v1.2-v1.13: project_id = git root commit hash, ordered by time_updated
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
		// fallback: project_id exists but time_updated renamed — order by rowid
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY rowid DESC LIMIT 1;`, projectID),
		// fallback: project identified by directory path
		fmt.Sprintf(`SELECT id FROM session WHERE path='%s' ORDER BY rowid DESC LIMIT 1;`, projectPath),
		// last resort: most recent session overall (only if no project filter works)
		`SELECT id FROM session ORDER BY rowid DESC LIMIT 1;`,
	}

	for _, q := range queries {
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		id := strings.TrimSpace(string(out))
		if id != "" {
			return id
		}
	}
	return ""
}

// sqliteSessionIsRecent checks whether the given session was updated within the timeout.
func sqliteSessionIsRecent(dbPath, sessionID string) bool {
	timeQueries := []string{
		fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT updated FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT updated_at FROM session WHERE id='%s';`, sessionID),
		fmt.Sprintf(`SELECT time FROM session WHERE id='%s';`, sessionID),
	}

	timeFormats := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
	}

	for _, q := range timeQueries {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		timeStr := strings.TrimSpace(string(out))
		if timeStr == "" {
			continue
		}
		for _, fmt_ := range timeFormats {
			if t, err := time.Parse(fmt_, timeStr); err == nil {
				return time.Since(t) <= agent.RecentSessionTimeout
			}
		}
		// Unparseable timestamp — treat session as recent
		return true
	}
	// No time column found — treat session as recent
	return true
}

// loadTranscriptForSession loads transcript data, preferring session_diff files (v1.14+)
// over the SQLite message table (pre-v1.14).
func loadTranscriptForSession(dataDir, dbPath, sessionID string) []byte {
	// v1.14+: check for session_diff file first
	sessionDiffFile := filepath.Join(dataDir, "storage", "session_diff", sessionID+".json")
	if data, err := os.ReadFile(sessionDiffFile); err == nil {
		return extractTranscriptFromSessionDiff(data)
	}

	// pre-v1.14: query SQLite message table
	msgQueries := []string{
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(data) FROM message WHERE session_id='%s' ORDER BY rowid;`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(content) FROM message WHERE session_id='%s' ORDER BY rowid;`, sessionID),
	}

	for _, q := range msgQueries {
		cmd := exec.Command("sqlite3", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		trimmed := strings.TrimSpace(string(out))
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
