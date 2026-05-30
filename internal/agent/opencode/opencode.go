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
// It uses PRAGMA table_info to detect the actual column names, making it resilient
// to schema changes across OpenCode versions.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Inspect the session table schema to handle column name changes across versions.
	sessionCols := sqliteTableCols(dbPath, "session")
	if len(sessionCols) == 0 {
		return nil, nil
	}

	// Resolve column name variants (opencode has used different names over versions).
	projectIDCol := pickCol(sessionCols, "project_id", "projectID", "projectId")
	updateCol := pickCol(sessionCols, "time_updated", "updated_at", "updatedAt", "updated")

	// Find the most recent session matching this project.
	sessionID := findRecentSession(dbPath, projectIDCol, updateCol, projectID, projectPath)
	if sessionID == "" {
		return nil, nil
	}

	// Verify the session is within the recent timeout window.
	if updateCol != "" {
		timeQuery := fmt.Sprintf(`SELECT "%s" FROM session WHERE id='%s';`, updateCol, sessionID)
		cmd := exec.Command("sqlite3", dbPath, timeQuery)
		if timeOutput, err := cmd.Output(); err == nil {
			if sessionTimeIsOld(strings.TrimSpace(string(timeOutput)), agent.RecentSessionTimeout) {
				return nil, nil
			}
		}
		// If time query fails, proceed anyway — better to try than skip.
	}

	// Fetch messages with schema-aware column detection.
	msgCols := sqliteTableCols(dbPath, "message")
	transcriptData := fetchMessages(dbPath, sessionID, msgCols)

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// sqliteTableCols returns a map of lowercase→actual column name for a SQLite table.
// Uses PRAGMA table_info which always works regardless of table contents.
func sqliteTableCols(dbPath, tableName string) map[string]string {
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
		// PRAGMA table_info returns: cid|name|type|notnull|dflt_value|pk
		parts := strings.Split(line, "|")
		if len(parts) >= 2 && parts[1] != "" {
			name := parts[1]
			cols[strings.ToLower(name)] = name
		}
	}
	return cols
}

// pickCol returns the actual column name matching the first found candidate (case-insensitive).
func pickCol(cols map[string]string, candidates ...string) string {
	for _, c := range candidates {
		if actual, ok := cols[strings.ToLower(c)]; ok {
			return actual
		}
	}
	return ""
}

// findRecentSession finds the most recent session for the given project.
// It tries the project_id column with both root commit hash and project path,
// then falls back to the most recently inserted session overall.
func findRecentSession(dbPath, projectIDCol, updateCol, projectID, projectPath string) string {
	orderClause := "rowid DESC"
	if updateCol != "" {
		orderClause = fmt.Sprintf(`"%s" DESC`, updateCol)
	}

	// Try project-scoped lookup with multiple identifier values.
	if projectIDCol != "" {
		for _, pid := range []string{projectID, projectPath} {
			if pid == "" {
				continue
			}
			escapedPID := strings.ReplaceAll(pid, "'", "''")
			q := fmt.Sprintf(
				`SELECT id FROM session WHERE "%s"='%s' ORDER BY %s LIMIT 1;`,
				projectIDCol, escapedPID, orderClause,
			)
			cmd := exec.Command("sqlite3", dbPath, q)
			if out, err := cmd.Output(); err == nil {
				if s := strings.TrimSpace(string(out)); s != "" {
					return s
				}
			}
		}
	}

	// Fallback: most recently inserted/updated session overall.
	q := fmt.Sprintf("SELECT id FROM session ORDER BY %s LIMIT 1;", orderClause)
	cmd := exec.Command("sqlite3", dbPath, q)
	if out, err := cmd.Output(); err == nil {
		return strings.TrimSpace(string(out))
	}

	return ""
}

// sessionTimeIsOld parses a timestamp (ISO 8601 string or Unix milliseconds integer)
// and returns true if the session is older than timeout. Returns false if unparseable.
func sessionTimeIsOld(timeStr string, timeout time.Duration) bool {
	if timeStr == "" {
		return false
	}
	// Try Unix milliseconds (integer) — used by newer OpenCode versions.
	if ms, err := strconv.ParseInt(timeStr, 10, 64); err == nil {
		return time.Since(time.UnixMilli(ms)) > timeout
	}
	// Try ISO 8601 and common datetime formats.
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, timeStr); err == nil {
			return time.Since(t) > timeout
		}
	}
	return false // Can't parse — assume recent, better to try than skip.
}

// fetchMessages retrieves messages for a session from the SQLite database.
// It handles multiple column naming conventions used across OpenCode versions.
// Returns an empty JSON array if messages cannot be retrieved but the session exists.
func fetchMessages(dbPath, sessionID string, msgCols map[string]string) []byte {
	sessionIDCol := pickCol(msgCols, "session_id", "sessionID", "sessionId")
	if sessionIDCol == "" {
		sessionIDCol = "session_id"
	}

	orderCol := pickCol(msgCols, "time_created", "created_at", "createdAt", "created")
	orderClause := "rowid"
	if orderCol != "" {
		orderClause = fmt.Sprintf(`"%s"`, orderCol)
	}

	escapedID := strings.ReplaceAll(sessionID, "'", "''")

	// Strategy 1: "data" column holds the full message JSON object.
	if dataCol := pickCol(msgCols, "data"); dataCol != "" {
		q := fmt.Sprintf(
			`SELECT json_group_array(json_patch("%s", json_object('id', id))) FROM message WHERE "%s"='%s' ORDER BY %s;`,
			dataCol, sessionIDCol, escapedID, orderClause,
		)
		if data := runMessageQuery(dbPath, q); data != nil {
			return data
		}
	}

	// Strategy 2: "parts"/"content" column with a separate "role" column.
	if contentCol := pickCol(msgCols, "parts", "content", "body"); contentCol != "" {
		roleCol := pickCol(msgCols, "role")
		if roleCol == "" {
			roleCol = "role"
		}
		q := fmt.Sprintf(
			`SELECT json_group_array(json_object('id', id, 'role', "%s", 'content', json("%s"))) FROM message WHERE "%s"='%s' ORDER BY %s;`,
			roleCol, contentCol, sessionIDCol, escapedID, orderClause,
		)
		if data := runMessageQuery(dbPath, q); data != nil {
			return data
		}
	}

	// Fallback: return empty transcript so the note is still created with session metadata.
	return []byte("[]")
}

// runMessageQuery executes a SQLite query and returns the result bytes.
// Returns nil if the query fails, produces no output, or returns a null/empty array.
func runMessageQuery(dbPath, query string) []byte {
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
