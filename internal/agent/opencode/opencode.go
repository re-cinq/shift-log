```go
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

// ParseTranscriptFile parses an OpenCode session from the message directory.
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
// Tries (in order): flat files (pre-v1.2), project-local SQLite (v1.16+),
// global XDG SQLite (v1.2–v1.15).
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// Flat file storage (pre-v1.2 OpenCode)
	session, err := a.discoverFromFlatFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if session != nil {
		return session, nil
	}

	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Project-local SQLite database (OpenCode v1.16+)
	localDBPath := filepath.Join(projectPath, ".opencode", "opencode.db")
	if info, _ := os.Stat(localDBPath); info != nil {
		if s := tryOpenCodeDB(localDBPath, "", projectPath); s != nil {
			return s, nil
		}
	}

	// Global XDG SQLite database (OpenCode v1.2–v1.15)
	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}
	projectID := GetProjectID(projectPath)
	globalDBPath := filepath.Join(dataDir, "opencode.db")
	if info, _ := os.Stat(globalDBPath); info != nil {
		if s := tryOpenCodeDB(globalDBPath, projectID, projectPath); s != nil {
			return s, nil
		}
	}

	return nil, nil
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

	msgDir, _ := GetMessageDir(bestSessionID)

	return &agent.SessionInfo{
		SessionID:      bestSessionID,
		TranscriptPath: msgDir,
		StartedAt:      bestModTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
	}, nil
}

// tryOpenCodeDB attempts to discover a recent session from an OpenCode SQLite database.
// Tries the new schema (v1.16+) first, then falls back to the old schema (v1.2–v1.15).
func tryOpenCodeDB(dbPath, projectID, projectPath string) *agent.SessionInfo {
	if s := tryNewOpenCodeSchema(dbPath, projectPath); s != nil {
		return s
	}
	return tryOldOpenCodeSchema(dbPath, projectID, projectPath)
}

// tryNewOpenCodeSchema handles OpenCode v1.16+ SQLite schema:
//   - Table "sessions" (plural), updated_at as Unix milliseconds integer
//   - Table "messages" (plural), parts JSON array, no project_id
func tryNewOpenCodeSchema(dbPath, projectPath string) *agent.SessionInfo {
	checkCmd := exec.Command("sqlite3", dbPath,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='sessions';`)
	checkOut, err := checkCmd.Output()
	if err != nil || strings.TrimSpace(string(checkOut)) != "1" {
		return nil
	}

	// Verify 'messages' table also exists
	checkCmd2 := exec.Command("sqlite3", dbPath,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='messages';`)
	checkOut2, err := checkCmd2.Output()
	if err != nil || strings.TrimSpace(string(checkOut2)) != "1" {
		return nil
	}

	sessionQuery := `SELECT id, updated_at FROM sessions ORDER BY updated_at DESC LIMIT 1;`
	cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, sessionQuery)
	sessionOut, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(sessionOut)) == "" {
		return nil
	}

	fields := strings.SplitN(strings.TrimSpace(string(sessionOut)), "\t", 2)
	if len(fields) == 0 || fields[0] == "" {
		return nil
	}
	sessionID := fields[0]

	if len(fields) >= 2 {
		if ms, err := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64); err == nil {
			if time.Since(time.UnixMilli(ms)) > agent.RecentSessionTimeout {
				return nil
			}
		}
	}

	msgQuery := fmt.Sprintf(
		`SELECT json_group_array(json_object('id', id, 'role', role, 'parts', json(parts), 'model', COALESCE(model, ''))) FROM messages WHERE session_id='%s' ORDER BY created_at;`,
		sessionID,
	)
	cmd = exec.Command("sqlite3", dbPath, msgQuery)
	msgOut, err := cmd.Output()
	if err != nil {
		return nil
	}

	transcriptData := []byte(strings.TrimSpace(string(msgOut)))
	if string(transcriptData) == "[null]" || string(transcriptData) == "[]" || len(transcriptData) == 0 {
		transcriptData = []byte("[]")
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}
}

// tryOldOpenCodeSchema handles OpenCode v1.2–v1.15 SQLite schema:
//   - Table "session" (singular), time_updated as text, project_id column
//   - Table "message" (singular), data JSON field
func tryOldOpenCodeSchema(dbPath, projectID, projectPath string) *agent.SessionInfo {
	// Try filtering by project_id using the root commit hash, then the project path
	var sessionID string
	for _, pid := range uniqueNonEmpty(projectID, projectPath) {
		q := fmt.Sprintf(
			`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`,
			pid,
		)
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
		out, err := cmd.Output()
		if err == nil {
			if id := strings.TrimSpace(string(out)); id != "" {
				sessionID = id
				break
			}
		}
	}

	if sessionID == "" {
		return nil
	}

	// Check recency
	timeQuery := fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID)
	cmd := exec.Command("sqlite3", dbPath, timeQuery)
	timeOut, err := cmd.Output()
	if err == nil {
		timeStr := strings.TrimSpace(string(timeOut))
		if t, parseErr := parseFlexTime(timeStr); parseErr == nil {
			if time.Since(t) > agent.RecentSessionTimeout {
				return nil
			}
		}
	}

	msgQuery := fmt.Sprintf(
		`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`,
		sessionID,
	)
	cmd = exec.Command("sqlite3", dbPath, msgQuery)
	msgOut, err := cmd.Output()
	if err != nil {
		return nil
	}

	transcriptData := []byte(strings.TrimSpace(string(msgOut)))
	if string(transcriptData) == "[null]" || string(transcriptData) == "[]" {
		return nil
	}

	return &agent.SessionInfo{
		SessionID:      sessionID,
		TranscriptPath: "",
		StartedAt:      time.Now().Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}
}

// parseFlexTime parses a timestamp string in multiple common formats.
func parseFlexTime(s string) (time.Time, error) {
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02T15:04:05.000Z",
		time.RFC3339,
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time %q", s)
}

// uniqueNonEmpty returns deduplicated non-empty strings (excluding "global").
func uniqueNonEmpty(vals ...string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, v := range vals {
		if v != "" && v != "global" && !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	return result
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
// Handles the newer "parts" array format (v1.16+) and the legacy "content" field.
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

	// Try "parts" field (new OpenCode schema: typed part objects)
	if partsRaw, ok := raw["parts"]; ok {
		var parts []struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(partsRaw, &parts); err == nil {
			var blocks []agent.ContentBlock
			for _, part := range parts {
				switch part.Type {
				case "text":
					var textData struct {
						Text string `json:"text"`
					}
					if err := json.Unmarshal(part.Data, &textData); err == nil && textData.Text != "" {
						blocks = append(blocks, agent.ContentBlock{Type: "text", Text: textData.Text})
					}
				case "tool_call":
					var toolData struct {
						ID    string `json:"id"`
						Name  string `json:"name"`
						Input string `json:"input"`
					}
					if err := json.Unmarshal(part.Data, &toolData); err == nil {
						inputRaw := json.RawMessage(`{}`)
						if toolData.Input != "" {
							_ = json.Unmarshal([]byte(toolData.Input), &inputRaw)
						}
						blocks = append(blocks, agent.ContentBlock{
							Type:  "tool_use",
							ID:    toolData.ID,
							Name:  toolData.Name,
							Input: inputRaw,
						})
					}
				}
			}
			msg.Content = blocks
			return msg
		}
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
```
