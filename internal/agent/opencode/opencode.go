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
//
// OpenCode's on-disk project identifier scheme has changed across releases
// (older versions keyed session directories by the git root commit hash;
// this is not guaranteed to hold for every version). We first try the
// project ID we compute locally, then fall back to scanning every known
// project directory for the most recently touched session so discovery
// keeps working even if OpenCode's own ID scheme has drifted from ours.
func (a *Agent) discoverFromFlatFiles(projectPath string) (*agent.SessionInfo, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}

	// Preferred: the project ID we compute locally.
	if sessionDir, err := GetSessionDir(projectPath); err == nil {
		if info := findMostRecentSession(sessionDir, projectPath); info != nil {
			return info, nil
		}
	}

	sessionRoot := filepath.Join(dataDir, "storage", "session")
	projectDirs, err := os.ReadDir(sessionRoot)
	if err != nil {
		return nil, nil
	}

	var best *agent.SessionInfo
	var bestModTime time.Time
	for _, pd := range projectDirs {
		if !pd.IsDir() {
			continue
		}

		info := findMostRecentSession(filepath.Join(sessionRoot, pd.Name()), projectPath)
		if info == nil {
			continue
		}

		modTime, err := time.Parse(time.RFC3339, info.StartedAt)
		if err != nil {
			continue
		}

		if best == nil || modTime.After(bestModTime) {
			best = info
			bestModTime = modTime
		}
	}

	return best, nil
}

// findMostRecentSession scans a single project's session directory for the
// most recently modified session file within the recency window. A session
// whose recorded "directory" field matches projectPath is always preferred
// over one selected on recency alone.
func findMostRecentSession(sessionDir, projectPath string) *agent.SessionInfo {
	dirEntries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil
	}

	now := time.Now()
	var bestSessionID string
	var bestModTime time.Time
	var bestMatchesPath bool

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

		matchesPath := false
		if data, err := os.ReadFile(filepath.Join(sessionDir, entry.Name())); err == nil {
			var s sessionInfo
			if json.Unmarshal(data, &s) == nil && s.Directory != "" {
				matchesPath = agent.PathsEqual(s.Directory, projectPath)
			}
		}

		better := bestSessionID == "" ||
			(matchesPath && !bestMatchesPath) ||
			(matchesPath == bestMatchesPath && modTime.After(bestModTime))

		if better {
			bestSessionID = strings.TrimSuffix(entry.Name(), ".json")
			bestModTime = modTime
			bestMatchesPath = matchesPath
		}
	}

	if bestSessionID == "" {
		return nil
	}

	msgDir, _ := GetMessageDir(bestSessionID)
	return &agent.SessionInfo{
		SessionID:      bestSessionID,
		TranscriptPath: msgDir,
		StartedAt:      bestModTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
	}
}

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
//
// Column and table layout for the "session"/"message" tables has changed
// across OpenCode releases, so this introspects the schema via
// PRAGMA table_info instead of assuming fixed column names. If the
// project-scoped lookup can't find a match (e.g. the project ID scheme
// changed), it falls back to the most recently touched session in the
// database, which is safe because session discovery is only used shortly
// after an agent session ran.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionCols, err := sqliteTableColumns(dbPath, "session")
	if err != nil || len(sessionCols) == 0 {
		return nil, nil
	}

	idCol := pickColumn(sessionCols, "id")
	if idCol == "" {
		return nil, nil
	}
	projectCol := pickColumn(sessionCols, "project_id", "projectid", "project", "worktree", "directory")
	timeCol := pickColumn(sessionCols, "time_updated", "updated", "updatedat", "time_created", "created", "createdat", "mtime")

	orderBy := "rowid"
	if timeCol != "" {
		orderBy = quoteIdent(timeCol)
	}

	var sessionID string

	// Preferred: filter by the project ID we compute locally.
	if projectCol != "" {
		q := fmt.Sprintf(`SELECT %s FROM session WHERE %s=%s ORDER BY %s DESC LIMIT 1;`,
			quoteIdent(idCol), quoteIdent(projectCol), sqliteQuote(projectID), orderBy)
		if out, err := exec.Command("sqlite3", "-separator", "\t", dbPath, q).Output(); err == nil {
			sessionID = strings.TrimSpace(string(out))
		}
	}

	// Fall back to the most recent session in the whole database. OpenCode's
	// project identification scheme has changed across versions, so an
	// exact project match may fail even though the session we want exists.
	if sessionID == "" {
		q := fmt.Sprintf(`SELECT %s FROM session ORDER BY %s DESC LIMIT 1;`, quoteIdent(idCol), orderBy)
		out, err := exec.Command("sqlite3", "-separator", "\t", dbPath, q).Output()
		if err != nil || strings.TrimSpace(string(out)) == "" {
			return nil, nil
		}
		sessionID = strings.TrimSpace(string(out))
	}

	// Recency check, when we have a usable timestamp column.
	if timeCol != "" {
		q := fmt.Sprintf(`SELECT %s FROM session WHERE %s=%s;`, quoteIdent(timeCol), quoteIdent(idCol), sqliteQuote(sessionID))
		if out, err := exec.Command("sqlite3", dbPath, q).Output(); err == nil {
			if !isRecentTimestamp(strings.TrimSpace(string(out))) {
				return nil, nil
			}
		}
	}

	transcriptData, err := sqliteFetchMessages(dbPath, sessionID)
	if err != nil || len(transcriptData) == 0 {
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

// sqliteFetchMessages fetches all messages for a session as a JSON array.
// It prefers a single JSON blob column (OpenCode's historical layout) but
// falls back to dumping raw rows when the message table uses normalized
// columns (e.g. role/content stored directly) instead of a blob column.
func sqliteFetchMessages(dbPath, sessionID string) ([]byte, error) {
	msgCols, err := sqliteTableColumns(dbPath, "message")
	if err != nil || len(msgCols) == 0 {
		return nil, fmt.Errorf("could not read message table schema")
	}

	sessionCol := pickColumn(msgCols, "session_id", "sessionid", "session")
	if sessionCol == "" {
		return nil, fmt.Errorf("message table has no session column")
	}

	orderCol := pickColumn(msgCols, "time_created", "created", "createdat", "time", "seq")
	orderBy := "rowid"
	if orderCol != "" {
		orderBy = quoteIdent(orderCol)
	}

	idCol := pickColumn(msgCols, "id")
	dataCol := pickColumn(msgCols, "data", "content", "body", "payload", "json")

	if dataCol != "" && idCol != "" {
		q := fmt.Sprintf(
			`SELECT json_group_array(json_patch(%s, json_object('id', %s))) FROM message WHERE %s=%s ORDER BY %s;`,
			quoteIdent(dataCol), quoteIdent(idCol), quoteIdent(sessionCol), sqliteQuote(sessionID), orderBy,
		)
		out, err := exec.Command("sqlite3", dbPath, q).Output()
		if err == nil {
			data := strings.TrimSpace(string(out))
			if data != "" && data != "[null]" && data != "[]" {
				return []byte(data), nil
			}
		}
	}

	// Fall back to raw rows: the message table may store message fields as
	// normal columns instead of a single JSON blob column.
	q := fmt.Sprintf(`SELECT * FROM message WHERE %s=%s ORDER BY %s;`, quoteIdent(sessionCol), sqliteQuote(sessionID), orderBy)
	out, err := exec.Command("sqlite3", "-json", dbPath, q).Output()
	if err != nil {
		return nil, err
	}

	data := strings.TrimSpace(string(out))
	if data == "" || data == "[]" {
		return nil, fmt.Errorf("no messages found for session")
	}

	return []byte(data), nil
}

// sqliteTableColumns returns the column names for a SQLite table.
func sqliteTableColumns(dbPath, table string) ([]string, error) {
	cmd := exec.Command("sqlite3", "-separator", "|", dbPath, fmt.Sprintf("PRAGMA table_info(%s);", table))
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var cols []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 2 {
			cols = append(cols, parts[1])
		}
	}
	return cols, nil
}

// pickColumn returns the actual column name matching one of the candidates
// (case-insensitively), or "" if none of them exist in cols.
func pickColumn(cols []string, candidates ...string) string {
	lookup := make(map[string]string, len(cols))
	for _, c := range cols {
		lookup[strings.ToLower(c)] = c
	}
	for _, cand := range candidates {
		if actual, ok := lookup[strings.ToLower(cand)]; ok {
			return actual
		}
	}
	return ""
}

// quoteIdent safely quotes a SQLite identifier (column/table name).
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// sqliteQuote safely quotes a SQLite string literal.
func sqliteQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// isRecentTimestamp reports whether a timestamp string (in any of several
// formats OpenCode has used, including unix epoch seconds/milliseconds)
// falls within RecentSessionTimeout. Unparseable or empty values are
// treated as recent — better to try using the session than to skip it.
func isRecentTimestamp(timeStr string) bool {
	if timeStr == "" {
		return true
	}

	formats := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, timeStr); err == nil {
			return time.Since(t) <= agent.RecentSessionTimeout
		}
	}

	if n, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		t := time.Unix(n, 0)
		if n > 1_000_000_000_000 { // milliseconds since epoch
			t = time.UnixMilli(n)
		}
		return time.Since(t) <= agent.RecentSessionTimeout
	}

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
