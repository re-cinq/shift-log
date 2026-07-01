package opencode

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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
// It recurses into subdirectories since OpenCode's on-disk layout for message
// content (e.g. nesting per-message "part" files in their own subfolder) has
// changed across versions.
func (a *Agent) parseMessageDir(dir string) (*agent.Transcript, error) {
	var entries []agent.TranscriptEntry

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}

		name := d.Name()
		switch {
		case strings.HasSuffix(name, ".jsonl"):
			f, ferr := os.Open(path)
			if ferr != nil {
				return nil
			}
			transcript, perr := a.ParseTranscript(f)
			_ = f.Close()
			if perr == nil {
				entries = append(entries, transcript.Entries...)
			}
		case strings.HasSuffix(name, ".json"):
			data, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			var raw map[string]json.RawMessage
			if jerr := json.Unmarshal(data, &raw); jerr != nil {
				return nil
			}
			entry := parseOpenCodeEntry(raw, data)
			if entry.Type != "" {
				entries = append(entries, entry)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
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
// It walks the entire session storage tree (OpenCode's directory nesting for
// per-project sessions has changed across versions) looking for the most
// recently modified session file within the recency window, preferring one
// whose content actually matches the current project.
func (a *Agent) discoverFromFlatFiles(projectPath string) (*agent.SessionInfo, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}

	sessionRoot := filepath.Join(dataDir, "storage", "session")
	if info, err := os.Stat(sessionRoot); err != nil || !info.IsDir() {
		return nil, nil
	}

	now := time.Now()
	recentTimeout := agent.RecentSessionTimeout
	projectID := GetProjectID(projectPath)
	absProject, err := filepath.Abs(projectPath)
	if err != nil {
		absProject = projectPath
	}

	var bestSessionID string
	var bestModTime time.Time
	var bestMatches bool
	found := false

	_ = filepath.WalkDir(sessionRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		modTime := info.ModTime()
		if now.Sub(modTime) > recentTimeout {
			return nil
		}

		sessionID := strings.TrimSuffix(d.Name(), ".json")
		matches := filepath.Base(filepath.Dir(path)) == projectID

		if data, readErr := os.ReadFile(path); readErr == nil {
			var meta struct {
				ID        string `json:"id"`
				ProjectID string `json:"projectID"`
				Directory string `json:"directory"`
				Cwd       string `json:"cwd"`
			}
			if json.Unmarshal(data, &meta) == nil {
				if meta.ID != "" {
					sessionID = meta.ID
				}
				for _, candidate := range []string{meta.ProjectID, meta.Directory, meta.Cwd} {
					if candidate == "" {
						continue
					}
					if candidate == projectID || candidate == projectPath || candidate == absProject {
						matches = true
					}
				}
			}
		}

		if !found || (matches && !bestMatches) || (matches == bestMatches && modTime.After(bestModTime)) {
			bestSessionID = sessionID
			bestModTime = modTime
			bestMatches = matches
			found = true
		}
		return nil
	})

	if !found {
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
// Table and column names are discovered dynamically via SQLite's schema
// introspection (sqlite_master / PRAGMA table_info) rather than hardcoded,
// since OpenCode's internal schema has changed across releases. When the
// project-scoping column can't be matched against the current project, the
// most recently updated session overall (within the recency window) is used
// as a best-effort fallback rather than silently reporting no session.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := findOpenCodeDB(dataDir)
	if dbPath == "" {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionTable, sessionCols, err := findTable(dbPath, "session")
	if err != nil || sessionTable == "" {
		return nil, nil
	}

	idCol := pickColumn(sessionCols, "id")
	if idCol == "" {
		return nil, nil
	}
	timeCol := pickColumn(sessionCols,
		"time_updated", "updated_at", "time_created", "created_at",
		"updated", "created", "mtime", "timestamp", "time")
	projectCol := pickColumn(sessionCols,
		"project_id", "projectid", "directory", "cwd", "path", "worktree", "root")

	orderBy := idCol
	if timeCol != "" {
		orderBy = timeCol
	}

	// Find most recent session for this project
	var sessionID string
	if projectCol != "" {
		query := fmt.Sprintf(
			`SELECT %s FROM %s WHERE %s = '%s' OR %s = '%s' OR %s LIKE '%%%s%%' ORDER BY %s DESC LIMIT 1;`,
			idCol, sessionTable,
			projectCol, sqlEscape(projectID),
			projectCol, sqlEscape(projectPath),
			projectCol, sqlEscape(projectPath),
			orderBy,
		)
		if out, err := runSQLiteQuery(dbPath, query); err == nil && out != "" {
			sessionID = firstLine(out)
		}
	}

	// Fall back to the most recently updated session across all projects if
	// the project-scoping column/scheme didn't match anything — better to
	// guess than to silently drop a real session.
	if sessionID == "" {
		query := fmt.Sprintf(`SELECT %s FROM %s ORDER BY %s DESC LIMIT 1;`, idCol, sessionTable, orderBy)
		out, err := runSQLiteQuery(dbPath, query)
		if err != nil || out == "" {
			return nil, nil
		}
		sessionID = firstLine(out)
	}

	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout). If we can't parse
	// the time, proceed anyway — better to try than to skip a real session.
	if timeCol != "" {
		timeQuery := fmt.Sprintf(`SELECT %s FROM %s WHERE %s = '%s';`, timeCol, sessionTable, idCol, sqlEscape(sessionID))
		if timeOutput, err := runSQLiteQuery(dbPath, timeQuery); err == nil && timeOutput != "" {
			if t, ok := parseFlexibleTime(firstLine(timeOutput)); ok {
				if time.Since(t) > agent.RecentSessionTimeout {
					return nil, nil
				}
			}
		}
	}

	messageTable, messageCols, err := findTable(dbPath, "message")
	if err != nil || messageTable == "" {
		return nil, nil
	}

	msgSessionCol := pickColumn(messageCols, "session_id", "sessionid", "session")
	msgIDCol := pickColumn(messageCols, "id")
	msgDataCol := pickColumn(messageCols, "data", "content", "body", "json")
	msgTimeCol := pickColumn(messageCols, "time_created", "created_at", "time", "created")

	if msgSessionCol == "" || msgDataCol == "" {
		return nil, nil
	}

	selectExpr := msgDataCol
	if msgIDCol != "" {
		selectExpr = fmt.Sprintf("json_patch(%s, json_object('id', %s))", msgDataCol, msgIDCol)
	}

	orderClause := ""
	if msgTimeCol != "" {
		orderClause = fmt.Sprintf(" ORDER BY %s", msgTimeCol)
	}

	// Get messages for this session as a JSON array
	msgQuery := fmt.Sprintf(
		`SELECT json_group_array(%s) FROM %s WHERE %s = '%s'%s;`,
		selectExpr, messageTable, msgSessionCol, sqlEscape(sessionID), orderClause,
	)
	msgOutput, err := runSQLiteQuery(dbPath, msgQuery)
	if err != nil {
		return nil, nil
	}

	transcriptData := []byte(strings.TrimSpace(msgOutput))
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

// findOpenCodeDB locates OpenCode's SQLite database. The default location is
// dataDir/opencode.db, but this searches a few other plausible locations and
// finally falls back to a shallow scan, since the database's location and
// filename have moved across OpenCode releases.
func findOpenCodeDB(dataDir string) string {
	candidates := []string{
		filepath.Join(dataDir, "opencode.db"),
		filepath.Join(dataDir, "storage", "opencode.db"),
		filepath.Join(dataDir, "db", "opencode.db"),
		filepath.Join(dataDir, "opencode.sqlite"),
		filepath.Join(dataDir, "opencode.sqlite3"),
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}

	var found string
	_ = filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" || d.IsDir() {
			return nil
		}
		name := strings.ToLower(d.Name())
		if strings.HasSuffix(name, ".db") || strings.HasSuffix(name, ".sqlite") || strings.HasSuffix(name, ".sqlite3") {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// runSQLiteQuery executes a single SQL statement against dbPath and returns
// the trimmed output, retrying briefly if the database is locked (e.g. by an
// OpenCode server process that hasn't released its handle yet).
func runSQLiteQuery(dbPath, query string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		cmd := exec.Command("sqlite3", "-cmd", ".timeout 3000", "-separator", "\t", dbPath, query)
		out, err := cmd.Output()
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
		lastErr = err
		time.Sleep(300 * time.Millisecond)
	}
	return "", lastErr
}

// findTable finds a table whose name equals or contains the given substring
// and returns its name along with its column names, discovered via
// PRAGMA table_info rather than assumed — OpenCode's table names/columns
// have changed across releases.
func findTable(dbPath, substr string) (string, []string, error) {
	out, err := runSQLiteQuery(dbPath, "SELECT name FROM sqlite_master WHERE type='table';")
	if err != nil {
		return "", nil, err
	}

	var table string
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		lower := strings.ToLower(name)
		if lower == substr {
			table = name
			break
		}
		if table == "" && strings.Contains(lower, substr) {
			table = name
		}
	}
	if table == "" {
		return "", nil, nil
	}

	colOut, err := runSQLiteQuery(dbPath, fmt.Sprintf("PRAGMA table_info(%s);", table))
	if err != nil {
		return table, nil, err
	}

	var columns []string
	for _, line := range strings.Split(colOut, "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) > 1 {
			columns = append(columns, fields[1])
		}
	}
	return table, columns, nil
}

// pickColumn returns the first column matching one of the candidate names
// (case-insensitively), falling back to a substring match.
func pickColumn(columns []string, candidates ...string) string {
	byLower := make(map[string]string, len(columns))
	for _, c := range columns {
		byLower[strings.ToLower(c)] = c
	}
	for _, cand := range candidates {
		if actual, ok := byLower[cand]; ok {
			return actual
		}
	}
	for _, cand := range candidates {
		for lower, actual := range byLower {
			if strings.Contains(lower, cand) {
				return actual
			}
		}
	}
	return ""
}

// sqlEscape escapes single quotes for safe inclusion in a SQL string literal.
func sqlEscape(s string) string {
	return strings.ReplaceAll(s, "'", "''")
}

// firstLine returns the first non-empty line of s.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			return line
		}
	}
	return ""
}

// parseFlexibleTime parses a timestamp that may be an ISO-8601 string or a
// Unix epoch integer (seconds or milliseconds), since OpenCode's schema
// stores time values differently across versions.
func parseFlexibleTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}

	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}

	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			return time.UnixMilli(n), true
		}
		return time.Unix(n, 0), true
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
