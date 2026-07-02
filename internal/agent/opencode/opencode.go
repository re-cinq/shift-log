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
// It first tries flat file storage, then falls back to SQLite. OpenCode's
// on-disk layout (directory nesting, table/column names) has changed across
// releases, so both discovery paths are defensive about the exact schema
// rather than assuming a single fixed layout.
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// Try flat file storage first
	session, err := a.discoverFromFlatFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if session != nil {
		return session, nil
	}

	// Fall back to SQLite
	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}

	projectID := GetProjectID(projectPath)
	return discoverFromSQLite(dataDir, projectID, projectPath)
}

// flatSessionMatch describes a candidate session file found on disk.
type flatSessionMatch struct {
	id      string
	modTime time.Time
}

// discoverFromFlatFiles tries flat file session discovery. OpenCode has used
// at least two layouts: sessions nested under a per-project directory
// (storage/session/<projectID>/<id>.json), and — in some releases — all
// sessions stored in a single flat directory with the project association
// recorded inside each session's JSON instead of via directory nesting.
func (a *Agent) discoverFromFlatFiles(projectPath string) (*agent.SessionInfo, error) {
	projectID := GetProjectID(projectPath)

	if sessionDir, err := GetSessionDir(projectPath); err == nil {
		if m := scanSessionFiles(sessionDir, "", ""); m != nil {
			return sessionInfoFromMatch(m, projectPath), nil
		}
	}

	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}

	for _, dir := range []string{
		filepath.Join(dataDir, "storage", "session"),
		filepath.Join(dataDir, "storage", "session", "info"),
	} {
		if m := scanSessionFiles(dir, projectID, projectPath); m != nil {
			return sessionInfoFromMatch(m, projectPath), nil
		}
	}

	return nil, nil
}

// scanSessionFiles returns the most recently modified session JSON file in dir.
// When matchProjectID or matchProjectPath is non-empty, only files whose
// "projectID" or "directory" field equals one of them are considered — used
// for layouts where sessions from every project share a single directory.
func scanSessionFiles(dir, matchProjectID, matchProjectPath string) *flatSessionMatch {
	dirEntries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	now := time.Now()
	var best *flatSessionMatch

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

		sessionID := strings.TrimSuffix(entry.Name(), ".json")

		if matchProjectID != "" || matchProjectPath != "" {
			data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
			if err != nil {
				continue
			}
			var s sessionInfo
			if err := json.Unmarshal(data, &s); err != nil {
				continue
			}
			matched := (matchProjectID != "" && s.ProjectID == matchProjectID) ||
				(matchProjectPath != "" && s.Directory == matchProjectPath)
			if !matched {
				continue
			}
			if s.ID != "" {
				sessionID = s.ID
			}
		}

		if best == nil || modTime.After(best.modTime) {
			best = &flatSessionMatch{id: sessionID, modTime: modTime}
		}
	}

	return best
}

// sessionInfoFromMatch builds an agent.SessionInfo from a discovered flat file match.
func sessionInfoFromMatch(m *flatSessionMatch, projectPath string) *agent.SessionInfo {
	msgDir, _ := GetMessageDir(m.id)

	return &agent.SessionInfo{
		SessionID:      m.id,
		TranscriptPath: msgDir,
		StartedAt:      m.modTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
	}
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent
// session. Table and column names are introspected at runtime rather than
// hard-coded, since OpenCode's schema (table names, snake_case vs camelCase
// columns) has changed across releases.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath, err := findOpenCodeDB(dataDir)
	if err != nil || dbPath == "" {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionTable, err := findTable(dbPath, "session")
	if err != nil || sessionTable == "" {
		return nil, nil
	}
	messageTable, err := findTable(dbPath, "message")
	if err != nil || messageTable == "" {
		return nil, nil
	}

	sessionCols, err := tableColumns(dbPath, sessionTable)
	if err != nil || len(sessionCols) == 0 {
		return nil, nil
	}

	idCol := pickColumn(sessionCols, "id")
	if idCol == "" {
		return nil, nil
	}
	updatedCol := pickColumn(sessionCols, "timeUpdated", "time_updated", "updatedAt", "updated_at", "updated")
	dirCol := pickColumn(sessionCols, "directory", "cwd", "worktree", "path")
	projectCol := pickColumn(sessionCols, "projectID", "project_id", "projectId")

	orderClause := ""
	if updatedCol != "" {
		orderClause = fmt.Sprintf("ORDER BY %s DESC", quoteIdent(updatedCol))
	}

	sessionID, updatedAt := querySession(dbPath, sessionTable, idCol, updatedCol, orderClause, dirCol, projectPath, projectCol, projectID)
	if sessionID == "" {
		// Last resort: most recent session in the DB regardless of project,
		// in case the project-identifying column could not be matched.
		sessionID, updatedAt = querySession(dbPath, sessionTable, idCol, updatedCol, orderClause, "", "", "", "")
	}
	if sessionID == "" {
		return nil, nil
	}

	if updatedAt != "" {
		if t, ok := parseFlexibleTime(updatedAt); ok && time.Since(t) > agent.RecentSessionTimeout {
			return nil, nil
		}
		// If we can't parse the time, proceed anyway — better to try than skip.
	}

	messageCols, err := tableColumns(dbPath, messageTable)
	if err != nil || len(messageCols) == 0 {
		return nil, nil
	}

	msgIDCol := pickColumn(messageCols, "id")
	sessionRefCol := pickColumn(messageCols, "sessionID", "session_id", "sessionId")
	dataCol := pickColumn(messageCols, "data", "content", "body")
	createdCol := pickColumn(messageCols, "timeCreated", "time_created", "createdAt", "created_at", "created")

	if sessionRefCol == "" {
		return nil, nil
	}

	var selectExpr string
	switch {
	case dataCol != "" && msgIDCol != "":
		selectExpr = fmt.Sprintf("json_patch(%s, json_object('id', %s))", quoteIdent(dataCol), quoteIdent(msgIDCol))
	case dataCol != "":
		selectExpr = quoteIdent(dataCol)
	default:
		roleCol := pickColumn(messageCols, "role")
		textCol := pickColumn(messageCols, "content", "text")
		if roleCol == "" || textCol == "" {
			return nil, nil
		}
		idExpr := "NULL"
		if msgIDCol != "" {
			idExpr = quoteIdent(msgIDCol)
		}
		selectExpr = fmt.Sprintf("json_object('id', %s, 'role', %s, 'content', %s)", idExpr, quoteIdent(roleCol), quoteIdent(textCol))
	}

	msgOrder := ""
	if createdCol != "" {
		msgOrder = fmt.Sprintf("ORDER BY %s", quoteIdent(createdCol))
	}

	msgQuery := fmt.Sprintf(
		`SELECT json_group_array(%s) FROM %s WHERE %s='%s' %s;`,
		selectExpr, quoteIdent(messageTable), quoteIdent(sessionRefCol), sqlEscape(sessionID), msgOrder,
	)
	cmd := exec.Command("sqlite3", dbPath, msgQuery)
	msgOutput, err := cmd.Output()
	if err != nil {
		return nil, nil
	}

	transcriptData := []byte(strings.TrimSpace(string(msgOutput)))
	// sqlite3 returns "[null]" when no rows match
	if len(transcriptData) == 0 || string(transcriptData) == "[null]" || string(transcriptData) == "[]" {
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

// querySession looks up a session's id (and, if available, its "updated" value)
// filtering by directory or project ID when a matching column was found.
func querySession(dbPath, table, idCol, updatedCol, orderClause, dirCol, projectPath, projectCol, projectID string) (id string, updated string) {
	where := ""
	switch {
	case dirCol != "" && projectPath != "":
		where = fmt.Sprintf("WHERE %s='%s'", quoteIdent(dirCol), sqlEscape(projectPath))
	case projectCol != "" && projectID != "":
		where = fmt.Sprintf("WHERE %s='%s'", quoteIdent(projectCol), sqlEscape(projectID))
	}

	selectCols := quoteIdent(idCol)
	if updatedCol != "" {
		selectCols += ", " + quoteIdent(updatedCol)
	}

	query := fmt.Sprintf("SELECT %s FROM %s %s %s LIMIT 1;", selectCols, quoteIdent(table), where, orderClause)
	rows, err := sqliteQueryJSON(dbPath, query)
	if err != nil || len(rows) == 0 {
		return "", ""
	}

	if idVal, ok := rows[0][idCol]; ok && idVal != nil {
		id = fmt.Sprintf("%v", idVal)
	}
	if updatedCol != "" {
		if uv, ok := rows[0][updatedCol]; ok && uv != nil {
			updated = fmt.Sprintf("%v", uv)
		}
	}
	return id, updated
}

// findOpenCodeDB locates OpenCode's SQLite database under dataDir, trying
// known locations before falling back to any *.db file found directly in it.
func findOpenCodeDB(dataDir string) (string, error) {
	candidates := []string{
		filepath.Join(dataDir, "opencode.db"),
		filepath.Join(dataDir, "storage", "opencode.db"),
		filepath.Join(dataDir, "db", "opencode.db"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return "", nil
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			return filepath.Join(dataDir, e.Name()), nil
		}
	}

	return "", nil
}

// findTable returns the actual table name matching keyword (e.g. "session"),
// preferring an exact (or pluralized) match before falling back to substring.
func findTable(dbPath, keyword string) (string, error) {
	rows, err := sqliteQueryJSON(dbPath, `SELECT name FROM sqlite_master WHERE type='table';`)
	if err != nil {
		return "", err
	}

	var names []string
	for _, r := range rows {
		if n, ok := r["name"].(string); ok {
			names = append(names, n)
		}
	}

	for _, cand := range []string{keyword, keyword + "s"} {
		for _, n := range names {
			if strings.EqualFold(n, cand) {
				return n, nil
			}
		}
	}
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), keyword) {
			return n, nil
		}
	}

	return "", nil
}

// tableColumns returns the column names of table.
func tableColumns(dbPath, table string) ([]string, error) {
	rows, err := sqliteQueryJSON(dbPath, fmt.Sprintf("PRAGMA table_info(%s);", quoteIdent(table)))
	if err != nil {
		return nil, err
	}

	var cols []string
	for _, r := range rows {
		if n, ok := r["name"].(string); ok {
			cols = append(cols, n)
		}
	}
	return cols, nil
}

// pickColumn returns the actual column name matching the first candidate
// found in cols (case-insensitive), or "" if none match.
func pickColumn(cols []string, candidates ...string) string {
	lower := make(map[string]string, len(cols))
	for _, c := range cols {
		lower[strings.ToLower(c)] = c
	}
	for _, cand := range candidates {
		if real, ok := lower[strings.ToLower(cand)]; ok {
			return real
		}
	}
	return ""
}

// sqliteQueryJSON runs query against dbPath and decodes the result rows.
func sqliteQueryJSON(dbPath, query string) ([]map[string]interface{}, error) {
	cmd := exec.Command("sqlite3", "-json", dbPath, query)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}

	var rows []map[string]interface{}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// quoteIdent quotes a SQL identifier (table/column name).
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// sqlEscape escapes single quotes in a SQL string literal.
func sqlEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// parseFlexibleTime parses a timestamp in any of the formats OpenCode has
// used across releases, including epoch milliseconds/seconds.
func parseFlexibleTime(s string) (time.Time, bool) {
	for _, f := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(f, s); err == nil {
			return t, true
		}
	}

	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		switch {
		case n > 1_000_000_000_000: // looks like milliseconds
			return time.UnixMilli(n), true
		case n > 1_000_000_000: // looks like seconds
			return time.Unix(n, 0), true
		}
	}

	return time.Time{}, false
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
