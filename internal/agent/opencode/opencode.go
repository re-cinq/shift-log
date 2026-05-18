package opencode

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	projectID := GetProjectID(projectPath)

	// Try SQLite in all candidate data directories (location varies across OpenCode versions)
	for _, dataDir := range candidateDataDirs() {
		s, err := discoverFromSQLite(dataDir, projectID, projectPath)
		if err != nil {
			continue
		}
		if s != nil {
			return s, nil
		}
	}

	return nil, nil
}

// candidateDataDirs returns all possible OpenCode data directory locations,
// ordered by preference. The location changed between versions.
func candidateDataDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	if runtime.GOOS == "darwin" {
		return []string{
			filepath.Join(home, "Library", "Application Support", "opencode"),
		}
	}

	var dirs []string

	// XDG_DATA_HOME/opencode (standard, pre-1.15)
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		dirs = append(dirs, filepath.Join(xdg, "opencode"))
	}
	dirs = append(dirs, filepath.Join(home, ".local", "share", "opencode"))

	// XDG_CONFIG_HOME/opencode (used in some OpenCode versions)
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		dirs = append(dirs, filepath.Join(xdgConfig, "opencode"))
	}
	dirs = append(dirs, filepath.Join(home, ".config", "opencode"))

	return dirs
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
// It introspects the schema at runtime to handle column name changes across OpenCode versions.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	// Check sqlite3 is available
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Introspect the session table schema to handle column name changes between versions
	// (e.g., project_id → projectID, time_updated → timeUpdated in OpenCode 1.15+)
	sessionCols := sqliteTableColumns(dbPath, "session")
	if len(sessionCols) == 0 {
		return nil, nil
	}

	// Find the timestamp column — name varies across versions
	timeUpdatedCol := findColumn(sessionCols,
		"time_updated", "timeUpdated", "timeupdated",
		"updated_at", "updatedAt", "updatedat",
		"time", "created_at", "createdAt",
	)
	if timeUpdatedCol == "" {
		return nil, nil
	}

	// Find the project identifier column
	projectIDCol := findColumn(sessionCols,
		"project_id", "projectID", "projectid",
		"workspace_id", "workspaceID", "workspaceid",
	)

	// Find the directory column (present in OpenCode 1.15+)
	directoryCol := findColumn(sessionCols,
		"directory", "dir", "path", "workdir",
		"working_dir", "workingDir", "workingdir",
	)

	var sessionID string

	// Strategy 1: match by project_id column with multiple identifier formats.
	// OpenCode 1.14.x uses the git root commit hash; 1.15+ may use the directory path.
	if projectIDCol != "" {
		for _, candidate := range []string{projectID, projectPath} {
			q := fmt.Sprintf(
				"SELECT id FROM session WHERE %s='%s' ORDER BY %s DESC LIMIT 1;",
				projectIDCol, candidate, timeUpdatedCol,
			)
			cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
			if out, err := cmd.Output(); err == nil {
				if id := strings.TrimSpace(string(out)); id != "" {
					sessionID = id
					break
				}
			}
		}
	}

	// Strategy 2: match by directory column (stable across versions)
	if sessionID == "" && directoryCol != "" {
		q := fmt.Sprintf(
			"SELECT id FROM session WHERE %s='%s' ORDER BY %s DESC LIMIT 1;",
			directoryCol, projectPath, timeUpdatedCol,
		)
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
		if out, err := cmd.Output(); err == nil {
			sessionID = strings.TrimSpace(string(out))
		}
	}

	if sessionID == "" {
		return nil, nil
	}

	// Check if this session was recent (within timeout)
	timeQuery := fmt.Sprintf("SELECT %s FROM session WHERE id='%s';", timeUpdatedCol, sessionID)
	cmd := exec.Command("sqlite3", dbPath, timeQuery)
	if timeOutput, err := cmd.Output(); err == nil {
		timeStr := strings.TrimSpace(string(timeOutput))
		if t, err := parseOpenCodeTimestamp(timeStr); err == nil {
			if time.Since(t) > agent.RecentSessionTimeout {
				return nil, nil
			}
		}
		// If we can't parse the time, proceed anyway — better to try than skip
	}

	// Retrieve messages; fall back to empty array so the note is still created
	transcriptData := fetchSessionMessages(dbPath, sessionID)

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// fetchSessionMessages retrieves messages for a session as a JSON array.
// Returns an empty JSON array on any error so callers can still write a git note.
func fetchSessionMessages(dbPath, sessionID string) []byte {
	msgCols := sqliteTableColumns(dbPath, "message")
	if len(msgCols) == 0 {
		return []byte("[]")
	}

	sessionIDCol := findColumn(msgCols,
		"session_id", "sessionID", "sessionid",
	)
	timeCreatedCol := findColumn(msgCols,
		"time_created", "timeCreated", "timecreated",
		"created_at", "createdAt", "createdat",
	)

	if sessionIDCol == "" {
		return []byte("[]")
	}

	if _, hasData := msgCols["data"]; hasData {
		var q string
		if timeCreatedCol != "" {
			q = fmt.Sprintf(
				"SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE %s='%s' ORDER BY %s;",
				sessionIDCol, sessionID, timeCreatedCol,
			)
		} else {
			q = fmt.Sprintf(
				"SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE %s='%s';",
				sessionIDCol, sessionID,
			)
		}
		cmd := exec.Command("sqlite3", dbPath, q)
		if out, err := cmd.Output(); err == nil {
			data := []byte(strings.TrimSpace(string(out)))
			if string(data) != "[null]" && string(data) != "[]" && len(data) > 0 {
				return data
			}
		}
	}

	return []byte("[]")
}

// sqliteTableColumns returns the column names of a SQLite table,
// keyed by lowercase name for case-insensitive lookup.
// Returns nil if the table does not exist or sqlite3 fails.
func sqliteTableColumns(dbPath, tableName string) map[string]string {
	cmd := exec.Command("sqlite3", dbPath, fmt.Sprintf("PRAGMA table_info(%s);", tableName))
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	cols := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// PRAGMA table_info format: cid|name|type|notnull|dflt_value|pk
		parts := strings.Split(line, "|")
		if len(parts) >= 2 {
			colName := strings.TrimSpace(parts[1])
			if colName != "" {
				cols[strings.ToLower(colName)] = colName
			}
		}
	}
	if len(cols) == 0 {
		return nil
	}
	return cols
}

// findColumn finds the first matching column name from a prioritized list of candidates.
// Matching is case-insensitive against the keys of cols (which are already lowercased).
func findColumn(cols map[string]string, candidates ...string) string {
	for _, candidate := range candidates {
		if name, ok := cols[strings.ToLower(candidate)]; ok {
			return name
		}
	}
	return ""
}

// parseOpenCodeTimestamp tries multiple time formats used by OpenCode across versions.
func parseOpenCodeTimestamp(timeStr string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
		time.RFC3339,
	}
	for _, format := range formats {
		if t, err := time.Parse(format, timeStr); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse timestamp %q", timeStr)
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
