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
// It first tries flat file storage, then falls back to SQLite.
// OpenCode's on-disk layout (how sessions are scoped to a project, whether
// sessions live directly under storage/session/ or in nested/sharded
// subdirectories, and the SQLite schema) has changed across releases, so
// both discovery strategies below match sessions by inspecting their
// content rather than assuming a fixed path or column layout.
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

// rawSessionFields captures a session JSON file loosely. OpenCode has used
// different field names for project scoping across releases (e.g.
// "directory" vs "cwd" vs "worktree", "projectID" vs "project_id"), so every
// known variant is checked rather than assuming one fixed schema.
type rawSessionFields struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectID"`
	Project   string `json:"project_id"`
	Directory string `json:"directory"`
	Cwd       string `json:"cwd"`
	Worktree  string `json:"worktree"`
}

// matchesProject reports whether this session belongs to the given project,
// comparing every project-scoping field OpenCode is known to have used.
func (r rawSessionFields) matchesProject(projectPath, projectID string) bool {
	for _, dir := range []string{r.Directory, r.Cwd, r.Worktree} {
		if dir != "" && agent.PathsEqual(dir, projectPath) {
			return true
		}
	}
	for _, id := range []string{r.ProjectID, r.Project} {
		if id != "" && id == projectID {
			return true
		}
	}
	return false
}

// discoverFromFlatFiles scans OpenCode's flat-file session storage for the
// most recently modified session that belongs to this project. Rather than
// assuming a fixed "storage/session/<projectID>/<sessionID>.json" layout
// (which has changed across OpenCode releases, e.g. added/removed project
// subdirectories or ID sharding), it walks the whole session storage tree
// and matches each session file by its own content.
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

	var bestSessionID string
	var bestModTime time.Time

	_ = filepath.Walk(sessionRoot, func(p string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() || !strings.HasSuffix(info.Name(), ".json") {
			return nil
		}

		modTime := info.ModTime()
		if now.Sub(modTime) > recentTimeout {
			return nil
		}
		if bestSessionID != "" && !modTime.After(bestModTime) {
			return nil
		}

		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}

		var fields rawSessionFields
		if err := json.Unmarshal(data, &fields); err != nil {
			return nil
		}
		if !fields.matchesProject(projectPath, projectID) {
			return nil
		}

		sessionID := fields.ID
		if sessionID == "" {
			sessionID = strings.TrimSuffix(info.Name(), ".json")
		}

		bestSessionID = sessionID
		bestModTime = modTime
		return nil
	})

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

// discoverFromSQLite queries an OpenCode SQLite database for the most recent
// session belonging to this project. Table and column names are discovered
// dynamically (via sqlite_master and "SELECT * ... -json") instead of being
// hardcoded, since OpenCode's SQLite schema has changed field names across
// releases (e.g. "project_id" vs "projectID", "time_updated" vs "updated").
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	sessionTable := findTable(dbPath, "session")
	if sessionTable == "" {
		return nil, nil
	}

	sessionRows, err := queryTableJSON(dbPath, sessionTable)
	if err != nil || len(sessionRows) == 0 {
		return nil, nil
	}

	sessionID, updatedAt := bestMatchingSessionRow(sessionRows, projectPath, projectID)
	if sessionID == "" {
		return nil, nil
	}
	if !updatedAt.IsZero() && time.Since(updatedAt) > agent.RecentSessionTimeout {
		return nil, nil
	}

	messageTable := findTable(dbPath, "message")
	if messageTable == "" {
		return nil, nil
	}

	messageRows, err := queryTableJSON(dbPath, messageTable)
	if err != nil {
		return nil, nil
	}

	var sessionMessages []map[string]interface{}
	for _, row := range messageRows {
		if rowReferencesSession(row, sessionID) {
			sessionMessages = append(sessionMessages, row)
		}
	}
	if len(sessionMessages) == 0 {
		return nil, nil
	}

	transcriptData, err := json.Marshal(sessionMessages)
	if err != nil {
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

// queryTableJSON runs "SELECT * FROM <table>" against dbPath and returns the
// rows as generic maps, using sqlite3's -json output mode so column names
// don't need to be known ahead of time.
func queryTableJSON(dbPath, table string) ([]map[string]interface{}, error) {
	cmd := exec.Command("sqlite3", "-json", dbPath, fmt.Sprintf(`SELECT * FROM "%s";`, table))
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

// findTable returns the first table name in the database containing substr
// (case-insensitive), or "" if none is found.
func findTable(dbPath, substr string) string {
	cmd := exec.Command("sqlite3", "-json", dbPath, `SELECT name FROM sqlite_master WHERE type='table';`)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	var rows []map[string]interface{}
	if err := json.Unmarshal(out, &rows); err != nil {
		return ""
	}

	for _, row := range rows {
		name, _ := row["name"].(string)
		if strings.Contains(strings.ToLower(name), substr) {
			return name
		}
	}
	return ""
}

// bestMatchingSessionRow scans generic session rows for the most recently
// updated session matching the given project.
func bestMatchingSessionRow(rows []map[string]interface{}, projectPath, projectID string) (string, time.Time) {
	var bestID string
	var bestTime time.Time

	for _, row := range rows {
		if !rowMatchesProject(row, projectPath, projectID) {
			continue
		}

		id := rowID(row)
		if id == "" {
			continue
		}

		updated := rowTimestamp(row)
		if bestID == "" || updated.After(bestTime) {
			bestID = id
			bestTime = updated
		}
	}

	return bestID, bestTime
}

// rowID extracts a session's identifier, trying common column name variants.
func rowID(row map[string]interface{}) string {
	for _, key := range []string{"id", "ID", "session_id", "sessionID"} {
		if s, ok := row[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// rowMatchesProject checks a session row's fields for a value matching the
// project directory or computed project ID. Column names vary across
// OpenCode versions, so every string field is considered rather than a
// single hardcoded column name.
func rowMatchesProject(row map[string]interface{}, projectPath, projectID string) bool {
	for key, v := range row {
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		lowerKey := strings.ToLower(key)
		if strings.Contains(lowerKey, "dir") || strings.Contains(lowerKey, "cwd") || strings.Contains(lowerKey, "worktree") {
			if agent.PathsEqual(s, projectPath) {
				return true
			}
		}
		if strings.Contains(lowerKey, "project") && s == projectID {
			return true
		}
	}
	return false
}

// rowReferencesSession reports whether a message row's session-scoping
// column (whatever it's named) references sessionID.
func rowReferencesSession(row map[string]interface{}, sessionID string) bool {
	for key, v := range row {
		if !strings.Contains(strings.ToLower(key), "session") {
			continue
		}
		if s, ok := v.(string); ok && s == sessionID {
			return true
		}
	}
	return false
}

// rowTimestamp extracts the most plausible "last updated" timestamp from a
// row by scanning every time-like column and trying common encodings
// (epoch seconds/milliseconds, RFC3339, and a couple of SQLite defaults).
func rowTimestamp(row map[string]interface{}) time.Time {
	var best time.Time
	for key, v := range row {
		lowerKey := strings.ToLower(key)
		if !strings.Contains(lowerKey, "update") && !strings.Contains(lowerKey, "time") && !strings.Contains(lowerKey, "created") {
			continue
		}
		if t := parseTimestampValue(v); !t.IsZero() && t.After(best) {
			best = t
		}
	}
	return best
}

func parseTimestampValue(v interface{}) time.Time {
	switch val := v.(type) {
	case float64:
		if val > 1e12 {
			return time.UnixMilli(int64(val))
		}
		if val > 1e9 {
			return time.Unix(int64(val), 0)
		}
	case string:
		layouts := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, val); err == nil {
				return t
			}
		}
	}
	return time.Time{}
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
