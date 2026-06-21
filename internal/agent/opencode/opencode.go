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
// It detects the actual schema at runtime to handle opencode schema evolution across versions.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Detect actual column names to handle schema differences across opencode versions.
	// opencode <=1.14 uses snake_case; >=1.15 uses camelCase column names.
	sessionCols := sqliteTableColumns(dbPath, "session")
	msgCols := sqliteTableColumns(dbPath, "message")

	projectIDCol := firstExistingCol(sessionCols, "project_id", "projectID")
	timeUpdatedCol := firstExistingCol(sessionCols, "time_updated", "timeUpdated")
	sessionIDCol := firstExistingCol(msgCols, "session_id", "sessionID")
	timeCreatedCol := firstExistingCol(msgCols, "time_created", "timeCreated")

	// Try to find the session using multiple fingerprint strategies.
	// opencode <=1.14: project_id = git root commit hash (what GetProjectID returns)
	// opencode >=1.15: project table stores project path; session.projectID = project UUID
	sessionID := sqliteFindSessionAny(dbPath, projectIDCol, timeUpdatedCol, projectID, projectPath)
	if sessionID == "" {
		return nil, nil
	}

	// Check recency using the detected time column.
	if timeUpdatedCol != "" {
		timeQuery := fmt.Sprintf(`SELECT %s FROM session WHERE id='%s';`, timeUpdatedCol, sessionID)
		cmd := exec.Command("sqlite3", dbPath, timeQuery)
		if timeOutput, err := cmd.Output(); err == nil {
			if isSessionTooOld(strings.TrimSpace(string(timeOutput))) {
				return nil, nil
			}
		}
	}

	// Fetch messages as a JSON array in the format ParseTranscript expects.
	transcriptData := sqliteFetchMessages(dbPath, sessionIDCol, timeCreatedCol, msgCols, sessionID)
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

// sqliteFindSessionAny tries multiple strategies to find the most recent session,
// covering both old (root commit hash) and new (project table + path) opencode schemas.
func sqliteFindSessionAny(dbPath, projectIDCol, timeUpdatedCol, projectID, projectPath string) string {
	// Strategy 1: direct match by git root commit hash (opencode <=1.14)
	if id := sqliteFindSession(dbPath, projectIDCol, timeUpdatedCol, projectID); id != "" {
		return id
	}

	// Strategy 2: match via project table by path (opencode >=1.15)
	if sqliteTableExists(dbPath, "project") {
		projectCols := sqliteTableColumns(dbPath, "project")
		pathCol := firstExistingCol(projectCols, "path", "fingerprint", "dir", "directory")

		// Try matching by absolute project path
		if projectPath != "" {
			if id := sqliteFindSessionViaProjectTable(dbPath, projectIDCol, timeUpdatedCol, pathCol, projectPath); id != "" {
				return id
			}
		}

		// Try matching by git root commit hash in the project table fingerprint column
		if id := sqliteFindSessionViaProjectTable(dbPath, projectIDCol, timeUpdatedCol, pathCol, projectID); id != "" {
			return id
		}
	}

	return ""
}

// sqliteTableColumns returns the set of column names for a table via PRAGMA table_info.
func sqliteTableColumns(dbPath, tableName string) map[string]bool {
	cmd := exec.Command("sqlite3", dbPath, fmt.Sprintf("PRAGMA table_info(%s);", tableName))
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	cols := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) >= 2 && parts[1] != "" {
			cols[parts[1]] = true
		}
	}
	return cols
}

// sqliteTableExists returns true if the named table exists in the database.
func sqliteTableExists(dbPath, tableName string) bool {
	cmd := exec.Command("sqlite3", dbPath,
		fmt.Sprintf("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='%s';", tableName))
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

// firstExistingCol returns the first candidate column that exists in cols,
// falling back to candidates[0] if none match (defensive default).
func firstExistingCol(cols map[string]bool, candidates ...string) string {
	for _, c := range candidates {
		if cols[c] {
			return c
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}
	return ""
}

// sqliteFindSession queries the session table for the most recent session matching a project ID value.
func sqliteFindSession(dbPath, projectIDCol, timeUpdatedCol, projectIDValue string) string {
	var q string
	if timeUpdatedCol != "" {
		q = fmt.Sprintf(
			`SELECT id FROM session WHERE %s='%s' ORDER BY %s DESC LIMIT 1;`,
			projectIDCol, projectIDValue, timeUpdatedCol,
		)
	} else {
		q = fmt.Sprintf(
			`SELECT id FROM session WHERE %s='%s' ORDER BY rowid DESC LIMIT 1;`,
			projectIDCol, projectIDValue,
		)
	}
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sqliteFindSessionViaProjectTable handles opencode versions that use a separate 'project'
// table, where session.projectID references project.id (UUID) and the project is identified
// by its path or fingerprint column.
func sqliteFindSessionViaProjectTable(dbPath, sessionProjectIDCol, timeUpdatedCol, projectPathCol, fingerprint string) string {
	projectQuery := fmt.Sprintf(
		`SELECT id FROM project WHERE %s='%s' LIMIT 1;`,
		projectPathCol, fingerprint,
	)
	cmd := exec.Command("sqlite3", dbPath, projectQuery)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	projectUUID := strings.TrimSpace(string(out))
	if projectUUID == "" {
		return ""
	}
	return sqliteFindSession(dbPath, sessionProjectIDCol, timeUpdatedCol, projectUUID)
}

// isSessionTooOld returns true if timeStr represents a timestamp older than RecentSessionTimeout.
// Supports ISO 8601, SQLite datetime, and Unix milliseconds (opencode >=1.15) formats.
func isSessionTooOld(timeStr string) bool {
	if timeStr == "" {
		return false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, timeStr); err == nil {
			return time.Since(t) > agent.RecentSessionTimeout
		}
	}
	// Unix milliseconds (opencode >=1.15 stores timestamps as integer ms)
	if ms, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		return time.Since(time.UnixMilli(ms)) > agent.RecentSessionTimeout
	}
	return false // unknown format — don't reject
}

// sqliteFetchMessages retrieves messages for a session as a JSON array.
// Handles both the legacy 'data' column format (opencode <=1.14) and the per-column
// format introduced in opencode >=1.15.
func sqliteFetchMessages(dbPath, sessionIDCol, timeCreatedCol string, msgCols map[string]bool, sessionID string) []byte {
	orderBy := "rowid"
	if timeCreatedCol != "" && msgCols[timeCreatedCol] {
		orderBy = timeCreatedCol
	}

	var msgQuery string
	if msgCols["data"] {
		// Legacy format: full message JSON stored in 'data' column
		msgQuery = fmt.Sprintf(
			`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE %s='%s' ORDER BY %s;`,
			sessionIDCol, sessionID, orderBy,
		)
	} else {
		// Newer format: reconstruct JSON from individual role/content columns
		msgQuery = buildMessageReconstructQuery(sessionIDCol, orderBy, msgCols, sessionID)
	}

	cmd := exec.Command("sqlite3", dbPath, msgQuery)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	result := []byte(strings.TrimSpace(string(out)))
	if string(result) == "[null]" || string(result) == "[]" || len(result) == 0 {
		return nil
	}
	return result
}

// buildMessageReconstructQuery builds a SELECT that reconstructs message JSON from individual
// columns when the legacy 'data' column is absent (opencode >=1.15).
func buildMessageReconstructQuery(sessionIDCol, orderBy string, msgCols map[string]bool, sessionID string) string {
	roleExpr := "''"
	if msgCols["role"] {
		roleExpr = "role"
	}
	contentExpr := "''"
	if msgCols["content"] {
		contentExpr = "content"
	}
	return fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', %s, 'content', %s)) FROM message WHERE %s='%s' ORDER BY %s;`,
		roleExpr, contentExpr, sessionIDCol, sessionID, orderBy,
	)
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
