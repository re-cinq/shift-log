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
		"bash":               true,
		"shell":              true,
		"terminal":           true,
		"execute":            true,
		"run":                true,
		"command":            true,
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
//
// OpenCode's SQLite schema (table and column names) has changed across releases,
// so rather than hardcoding names that may go stale, we introspect the schema at
// query time via sqlite_master and PRAGMA table_info and adapt to whatever we find.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionTable := findSQLiteTable(dbPath, "session")
	if sessionTable == "" {
		return nil, nil
	}
	sessionCols := sqliteTableColumns(dbPath, sessionTable)
	if len(sessionCols) == 0 {
		return nil, nil
	}

	idCol := findSQLiteColumn(sessionCols, "id")
	if idCol == "" {
		return nil, nil
	}
	timeCol := findSQLiteColumn(sessionCols, "time_updated", "updated_at", "updated", "time_created", "created_at", "created", "time")
	projectCol := findSQLiteColumn(sessionCols, "project_id", "projectid", "project")
	dirCol := findSQLiteColumn(sessionCols, "directory", "worktree", "cwd", "path")

	var sessionID string
	if projectCol != "" && projectID != "" {
		sessionID = queryLatestSessionID(dbPath, sessionTable, idCol, timeCol, projectCol, projectID)
	}
	if sessionID == "" && dirCol != "" && projectPath != "" {
		sessionID = queryLatestSessionID(dbPath, sessionTable, idCol, timeCol, dirCol, projectPath)
	}
	if sessionID == "" {
		// No usable project-scoping column, or it didn't match anything
		// (e.g. the project identification scheme changed) - fall back to
		// the most recently updated session across the whole database.
		sessionID = queryLatestSessionID(dbPath, sessionTable, idCol, timeCol, "", "")
	}
	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout). If we can't determine
	// a time column or can't parse its value, proceed anyway rather than skip.
	if timeCol != "" {
		if recent, ok := sqliteSessionIsRecent(dbPath, sessionTable, idCol, timeCol, sessionID); ok && !recent {
			return nil, nil
		}
	}

	transcriptData := loadSQLiteSessionMessages(dbPath, sessionID)
	if len(transcriptData) == 0 {
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

// findSQLiteTable returns the name of the first table matching hint exactly
// (case-insensitively), or containing hint as a substring, or "" if none exists.
func findSQLiteTable(dbPath, hint string) string {
	cmd := exec.Command("sqlite3", dbPath, "SELECT name FROM sqlite_master WHERE type='table';")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	var partial string
	for _, name := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		if lower == hint {
			return name
		}
		if partial == "" && strings.Contains(lower, hint) {
			partial = name
		}
	}
	return partial
}

// sqliteTableColumns returns the column names of a table via PRAGMA table_info.
func sqliteTableColumns(dbPath, table string) []string {
	cmd := exec.Command("sqlite3", "-separator", "|", dbPath,
		fmt.Sprintf("PRAGMA table_info(%s);", quoteSQLIdent(table)))
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var cols []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, "|")
		if len(parts) >= 2 && parts[1] != "" {
			cols = append(cols, parts[1])
		}
	}
	return cols
}

// findSQLiteColumn returns the first column in cols matching any candidate
// exactly (case-insensitively), preferring exact matches over substring
// matches, and preferring earlier candidates over later ones.
func findSQLiteColumn(cols []string, candidates ...string) string {
	for _, want := range candidates {
		for _, c := range cols {
			if strings.EqualFold(c, want) {
				return c
			}
		}
	}
	for _, want := range candidates {
		for _, c := range cols {
			if strings.Contains(strings.ToLower(c), want) {
				return c
			}
		}
	}
	return ""
}

// queryLatestSessionID returns the id of the most recently updated session,
// optionally filtered by filterCol=filterVal. Returns "" if none is found.
func queryLatestSessionID(dbPath, table, idCol, timeCol, filterCol, filterVal string) string {
	query := fmt.Sprintf("SELECT %s FROM %s", quoteSQLIdent(idCol), quoteSQLIdent(table))
	if filterCol != "" && filterVal != "" {
		query += fmt.Sprintf(" WHERE %s='%s'", quoteSQLIdent(filterCol), escapeSQLLiteral(filterVal))
	}
	if timeCol != "" {
		query += fmt.Sprintf(" ORDER BY %s DESC", quoteSQLIdent(timeCol))
	}
	query += " LIMIT 1;"

	cmd := exec.Command("sqlite3", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// sqliteSessionIsRecent checks whether the given session's time column value
// is within agent.RecentSessionTimeout. ok is false if the value could not
// be read or parsed, in which case the caller should not treat it as stale.
func sqliteSessionIsRecent(dbPath, table, idCol, timeCol, sessionID string) (recent bool, ok bool) {
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s='%s';",
		quoteSQLIdent(timeCol), quoteSQLIdent(table), quoteSQLIdent(idCol), escapeSQLLiteral(sessionID))
	cmd := exec.Command("sqlite3", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return false, false
	}

	timeStr := strings.TrimSpace(string(out))
	if timeStr == "" {
		return false, false
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, timeStr); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout, true
		}
	}

	// Newer OpenCode releases may store timestamps as Unix epoch
	// milliseconds (or seconds) rather than a formatted string.
	if n, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		t := time.UnixMilli(n)
		if n < 1e12 {
			t = time.Unix(n, 0)
		}
		return time.Since(t) <= agent.RecentSessionTimeout, true
	}

	return false, false
}

// loadSQLiteSessionMessages fetches all messages for a session as a JSON array.
func loadSQLiteSessionMessages(dbPath, sessionID string) []byte {
	msgTable := findSQLiteTable(dbPath, "message")
	if msgTable == "" {
		return nil
	}
	msgCols := sqliteTableColumns(dbPath, msgTable)
	if len(msgCols) == 0 {
		return nil
	}

	idCol := findSQLiteColumn(msgCols, "id")
	sessionCol := findSQLiteColumn(msgCols, "session_id", "sessionid", "session")
	timeCol := findSQLiteColumn(msgCols, "time_created", "created_at", "created", "time")
	dataCol := findSQLiteColumn(msgCols, "data", "content", "body", "message")

	if sessionCol == "" || dataCol == "" {
		return nil
	}

	runQuery := func(withID bool) []byte {
		var selectExpr string
		if withID && idCol != "" && idCol != dataCol {
			selectExpr = fmt.Sprintf("json_group_array(json_patch(%s, json_object('id', %s)))",
				quoteSQLIdent(dataCol), quoteSQLIdent(idCol))
		} else {
			selectExpr = fmt.Sprintf("json_group_array(%s)", quoteSQLIdent(dataCol))
		}

		query := fmt.Sprintf("SELECT %s FROM %s WHERE %s='%s'",
			selectExpr, quoteSQLIdent(msgTable), quoteSQLIdent(sessionCol), escapeSQLLiteral(sessionID))
		if timeCol != "" {
			query += fmt.Sprintf(" ORDER BY %s", quoteSQLIdent(timeCol))
		}
		query += ";"

		cmd := exec.Command("sqlite3", dbPath, query)
		out, err := cmd.Output()
		if err != nil {
			return nil
		}

		data := strings.TrimSpace(string(out))
		if data == "" || data == "[null]" || data == "[]" {
			return nil
		}
		return []byte(data)
	}

	if data := runQuery(true); len(data) > 0 {
		return data
	}
	// json_patch requires the data column to hold a JSON object; if it
	// doesn't (e.g. it's already an array/scalar, or json_patch itself
	// errored), fall back to selecting the raw data column.
	return runQuery(false)
}

// quoteSQLIdent safely quotes a SQLite identifier (table/column name).
func quoteSQLIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

// escapeSQLLiteral escapes a string for safe interpolation into a SQLite
// string literal.
func escapeSQLLiteral(s string) string {
	return strings.ReplaceAll(s, "'", "''")
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
