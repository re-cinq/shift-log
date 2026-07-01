package opencode

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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

// DiscoverSession finds an active or recent OpenCode session for projectPath.
//
// OpenCode's on-disk storage layout has changed across releases: early
// versions wrote one flat JSON file per session under a project-scoped
// directory (<dataDir>/storage/session/<projectID>/<sessionID>.json), while
// later versions moved to a SQLite database. The exact directory nesting and
// database schema/location are not part of OpenCode's public contract and
// have shifted between releases, so instead of hard-coding a single path we
// search the data directory for whichever form is present and match by
// content (session directory/project id) rather than by a fixed path shape.
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}

	projectID := GetProjectID(projectPath)

	if session := findFlatFileSession(dataDir, projectPath, projectID); session != nil {
		return session, nil
	}

	if session := findSQLiteSession(dataDir, projectPath, projectID); session != nil {
		return session, nil
	}

	return nil, nil
}

// findFlatFileSession searches the data directory for the most recently
// modified session JSON file belonging to projectPath. It matches sessions
// either by their recorded "directory"/"projectID" field, or, for older
// layouts that don't embed that information, by the legacy convention of
// nesting sessions under a directory named after the project id.
func findFlatFileSession(dataDir, projectPath, projectID string) *agent.SessionInfo {
	searchRoot := filepath.Join(dataDir, "storage")
	if _, err := os.Stat(searchRoot); err != nil {
		searchRoot = dataDir
	}

	now := time.Now()
	var bestSessionID string
	var bestModTime time.Time

	_ = filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipOpenCodeSearchDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		if !strings.Contains(filepath.ToSlash(path), "session") {
			return nil
		}

		info, err := d.Info()
		if err != nil || now.Sub(info.ModTime()) > agent.RecentSessionTimeout {
			return nil
		}

		sessionID := strings.TrimSuffix(d.Name(), ".json")
		matched := false

		if data, readErr := os.ReadFile(path); readErr == nil {
			var parsed struct {
				ID        string `json:"id"`
				ProjectID string `json:"projectID"`
				Directory string `json:"directory"`
			}
			if json.Unmarshal(data, &parsed) == nil {
				if parsed.Directory != "" {
					matched = samePath(parsed.Directory, projectPath)
				} else if parsed.ProjectID != "" {
					matched = parsed.ProjectID == projectID
				}
				if matched && parsed.ID != "" {
					sessionID = parsed.ID
				}
			}
		}

		// Legacy layout: <dataDir>/storage/session/<projectID>/<sessionID>.json
		if !matched {
			matched = filepath.Base(filepath.Dir(path)) == projectID
		}

		if !matched {
			return nil
		}

		if bestSessionID == "" || info.ModTime().After(bestModTime) {
			bestSessionID = sessionID
			bestModTime = info.ModTime()
		}
		return nil
	})

	if bestSessionID == "" {
		return nil
	}

	return &agent.SessionInfo{
		SessionID:      bestSessionID,
		TranscriptPath: findMessageDir(dataDir, bestSessionID),
		StartedAt:      bestModTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
	}
}

// findMessageDir locates the directory holding a session's message files.
// It searches for a directory named after sessionID under a "message"
// path component, falling back to the legacy fixed layout if not found.
func findMessageDir(dataDir, sessionID string) string {
	var found string
	_ = filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if skipOpenCodeSearchDir(d.Name()) {
			return filepath.SkipDir
		}
		if d.Name() == sessionID && strings.Contains(filepath.ToSlash(path), "message") {
			found = path
			return filepath.SkipDir
		}
		return nil
	})
	if found != "" {
		return found
	}

	msgDir, _ := GetMessageDir(sessionID)
	return msgDir
}

// findSQLiteSession searches the data directory for a SQLite database
// containing a recent session for projectPath. Newer OpenCode releases may
// store this database under different filenames/locations than the
// historical <dataDir>/opencode.db, so any *.db/*.sqlite* file is a
// candidate; per-file queries also tolerate schema differences (e.g. a
// "directory" column/field instead of "project_id").
func findSQLiteSession(dataDir, projectPath, projectID string) *agent.SessionInfo {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil
	}

	var dbPaths []string
	_ = filepath.WalkDir(dataDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipOpenCodeSearchDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		lower := strings.ToLower(d.Name())
		if strings.HasSuffix(lower, ".db") || strings.HasSuffix(lower, ".sqlite") || strings.HasSuffix(lower, ".sqlite3") {
			dbPaths = append(dbPaths, path)
		}
		return nil
	})

	for _, dbPath := range dbPaths {
		if session := querySQLiteSession(dbPath, projectPath, projectID); session != nil {
			return session
		}
	}
	return nil
}

// querySQLiteSession tries a set of schema variants to find the most recent
// session for projectPath/projectID in the given database.
func querySQLiteSession(dbPath, projectPath, projectID string) *agent.SessionInfo {
	sessionQueries := []string{
		fmt.Sprintf(`SELECT id FROM session WHERE directory=%s ORDER BY time_updated DESC LIMIT 1;`, sqlQuote(projectPath)),
		fmt.Sprintf(`SELECT id FROM session WHERE json_extract(data,'$.directory')=%s ORDER BY time_updated DESC LIMIT 1;`, sqlQuote(projectPath)),
		fmt.Sprintf(`SELECT id FROM session WHERE project_id=%s ORDER BY time_updated DESC LIMIT 1;`, sqlQuote(projectID)),
		fmt.Sprintf(`SELECT id FROM session WHERE json_extract(data,'$.projectID')=%s ORDER BY time_updated DESC LIMIT 1;`, sqlQuote(projectID)),
	}

	var sessionID string
	for _, q := range sessionQueries {
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
		out, err := cmd.Output()
		if err != nil {
			continue
		}
		if id := strings.TrimSpace(string(out)); id != "" {
			sessionID = id
			break
		}
	}
	if sessionID == "" {
		return nil
	}

	// Check if this session was recent (within timeout). Tolerate a missing
	// or unparsable time_updated value by proceeding anyway.
	timeQuery := fmt.Sprintf(`SELECT time_updated FROM session WHERE id=%s;`, sqlQuote(sessionID))
	cmd := exec.Command("sqlite3", dbPath, timeQuery)
	if timeOutput, err := cmd.Output(); err == nil {
		timeStr := strings.TrimSpace(string(timeOutput))
		formats := []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"}
		for _, format := range formats {
			if t, err := time.Parse(format, timeStr); err == nil {
				if time.Since(t) > agent.RecentSessionTimeout {
					return nil
				}
				break
			}
		}
	}

	// Get messages for this session as a JSON array.
	msgQuery := fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id=%s ORDER BY time_created;`,
		sqlQuote(sessionID),
	)
	cmd = exec.Command("sqlite3", dbPath, msgQuery)
	msgOutput, err := cmd.Output()
	if err != nil {
		return nil
	}

	transcriptData := []byte(strings.TrimSpace(string(msgOutput)))
	// sqlite3 returns "[null]" when no rows match
	if string(transcriptData) == "[null]" || string(transcriptData) == "[]" {
		return nil
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "", // no file path for SQLite
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}
}

// sqlQuote wraps s as a single-quoted SQLite string literal, escaping any
// embedded single quotes.
func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// samePath reports whether two filesystem paths refer to the same location,
// resolving symlinks when possible and falling back to a clean comparison.
func samePath(a, b string) bool {
	if a == b {
		return true
	}
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// skipOpenCodeSearchDir reports whether a directory should be excluded from
// session discovery searches (dependency/cache directories that could make
// the walk slow but never contain session data).
func skipOpenCodeSearchDir(name string) bool {
	switch name {
	case "node_modules", ".git", "cache", "log", "logs", "bin":
		return true
	default:
		return false
	}
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
