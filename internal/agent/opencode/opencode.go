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
		if now.Sub(modTime) > agent.RecentSessionTimeout {
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

// sqliteQuery runs a SQLite query and returns trimmed stdout, or "" on any error.
func sqliteQuery(dbPath, query string) string {
	cmd := exec.Command("sqlite3", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// parseOpenCodeTimestamp parses time values used across OpenCode schema versions:
// RFC3339 strings, "2006-01-02T15:04:05.000Z", bare date-time, Unix milliseconds (int),
// and Unix seconds (int). Returns the zero Time if none match.
func parseOpenCodeTimestamp(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, f := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
		time.RFC3339,
	} {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1_000_000_000_000 {
			// Unix milliseconds (OpenCode v1.17+ stores timestamps this way)
			return time.Unix(n/1000, (n%1000)*int64(time.Millisecond))
		}
		if n > 1_000_000_000 {
			return time.Unix(n, 0)
		}
	}
	return time.Time{}
}

// sqliteFindSession tries multiple strategies to locate the most recent session ID
// for the given project. It accommodates schema differences across OpenCode versions:
//   - pre-1.17: project_id = git root commit hash, ordered by time_updated
//   - 1.17+:    project_id may be directory path; time column may be updated_at
func sqliteFindSession(dbPath, projectID, projectPath string) string {
	// Project filters to try in preference order.
	filters := []string{
		fmt.Sprintf("project_id='%s'", projectID),   // git root commit hash (≤1.16)
		fmt.Sprintf("project_id='%s'", projectPath), // directory path (≥1.17)
		fmt.Sprintf("path='%s'", projectPath),        // 'path' column variant
		"", // no project filter — most recent session overall (last resort)
	}
	// Time columns to try per filter.
	orderCols := []string{"time_updated", "updated_at"}

	for _, filter := range filters {
		for _, col := range orderCols {
			where := ""
			if filter != "" {
				where = "WHERE " + filter + " "
			}
			q := fmt.Sprintf("SELECT id FROM session %sORDER BY %s DESC LIMIT 1;", where, col)
			if id := sqliteQuery(dbPath, q); id != "" {
				return id
			}
		}
	}

	// Final fallback: any session at all (handles unknown column names).
	return sqliteQuery(dbPath, "SELECT id FROM session LIMIT 1;")
}

// sqliteSessionIsRecent returns true when the session was updated within
// RecentSessionTimeout, or when the update time cannot be determined (we
// prefer a false-positive over silently dropping a valid session).
func sqliteSessionIsRecent(dbPath, sessionID string) bool {
	for _, col := range []string{"time_updated", "updated_at"} {
		q := fmt.Sprintf("SELECT %s FROM session WHERE id='%s';", col, sessionID)
		s := sqliteQuery(dbPath, q)
		if s == "" {
			continue
		}
		t := parseOpenCodeTimestamp(s)
		if t.IsZero() {
			continue
		}
		return time.Since(t) <= agent.RecentSessionTimeout
	}
	// Cannot determine timestamp — assume recent rather than drop.
	return true
}

// sqliteFetchMessages retrieves session messages as a JSON array.
// Tries multiple query strategies to handle different OpenCode schema versions.
func sqliteFetchMessages(dbPath, sessionID string) []byte {
	sid := sessionID

	// Strategy 1: data column + json_patch to merge id, ordered by time_created (≤1.16)
	if out := sqliteQuery(dbPath, fmt.Sprintf(
		"SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;",
		sid,
	)); out != "" && out != "[null]" && out != "[]" {
		return []byte(out)
	}

	// Strategy 2: same but ordered by created_at (1.17+ column rename)
	if out := sqliteQuery(dbPath, fmt.Sprintf(
		"SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY created_at;",
		sid,
	)); out != "" && out != "[null]" && out != "[]" {
		return []byte(out)
	}

	// Strategy 3: data column without ORDER BY (any schema variant)
	if out := sqliteQuery(dbPath, fmt.Sprintf(
		"SELECT json_group_array(data) FROM message WHERE session_id='%s';",
		sid,
	)); out != "" && out != "[null]" && out != "[]" {
		return []byte(out)
	}

	// Strategy 4: separate role/content columns (alternative schema)
	if out := sqliteQuery(dbPath, fmt.Sprintf(
		"SELECT json_group_array(json_object('role', role, 'content', content, 'id', id)) FROM message WHERE session_id='%s';",
		sid,
	)); out != "" && out != "[null]" && out != "[]" {
		return []byte(out)
	}

	return nil
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
// It uses multiple fallback strategies to handle schema differences across versions.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionID := sqliteFindSession(dbPath, projectID, projectPath)
	if sessionID == "" {
		return nil, nil
	}

	if !sqliteSessionIsRecent(dbPath, sessionID) {
		return nil, nil
	}

	transcriptData := sqliteFetchMessages(dbPath, sessionID)
	if transcriptData == nil {
		// Session exists but messages not yet available (e.g. opencode started but LLM
		// hasn't responded yet). Use an empty array so the post-commit note is still
		// written with valid session metadata.
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

	if roleRaw, ok := raw["role"]; ok {
		var role string
		if err := json.Unmarshal(roleRaw, &role); err == nil {
			entry.Type = agent.NormalizeRole(role)
		}
	}

	if entry.Type == "" {
		if typeRaw, ok := raw["type"]; ok {
			var t string
			if err := json.Unmarshal(typeRaw, &t); err == nil {
				entry.Type = agent.NormalizeRole(t)
			}
		}
	}

	if idRaw, ok := raw["id"]; ok {
		var id string
		if err := json.Unmarshal(idRaw, &id); err == nil {
			entry.UUID = id
		}
	}

	if timeRaw, ok := raw["time"]; ok {
		var timeObj struct {
			Created string `json:"created"`
		}
		if err := json.Unmarshal(timeRaw, &timeObj); err == nil {
			entry.Timestamp = timeObj.Created
		}
	}

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

	if contentRaw, ok := raw["content"]; ok {
		var text string
		if err := json.Unmarshal(contentRaw, &text); err == nil && text != "" {
			msg.Content = []agent.ContentBlock{{Type: "text", Text: text}}
			return msg
		}
		var blocks []agent.ContentBlock
		if err := json.Unmarshal(contentRaw, &blocks); err == nil && len(blocks) > 0 {
			msg.Content = blocks
			return msg
		}
	}

	if msgRaw, ok := raw["message"]; ok {
		var innerMsg agent.Message
		if err := json.Unmarshal(msgRaw, &innerMsg); err == nil {
			return &innerMsg
		}
	}

	return msg
}
