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

type Agent struct{}

func (a *Agent) Name() agent.Name    { return agent.OpenCode }
func (a *Agent) DisplayName() string { return "OpenCode CLI" }

func (a *Agent) ConfigureHooks(repoRoot string) error { return InstallPlugin(repoRoot) }
func (a *Agent) RemoveHooks(repoRoot string) error    { return RemovePlugin(repoRoot) }

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
// Tries: plugin marker file → flat files → SQLite.
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	if session, err := a.discoverFromMarkerFile(projectPath); err == nil && session != nil {
		return session, nil
	}

	session, err := a.discoverFromFlatFiles(projectPath)
	if err != nil {
		return nil, err
	}
	if session != nil {
		return session, nil
	}

	dataDir, err := GetDataDir()
	if err != nil {
		return nil, nil
	}
	projectID := GetProjectID(projectPath)
	return discoverFromSQLite(dataDir, projectID, projectPath)
}

// discoverFromMarkerFile reads the session marker written by the plugin on every
// tool invocation. This is the primary discovery path for manual commits because
// it works even when opencode did not itself perform a git commit.
func (a *Agent) discoverFromMarkerFile(projectPath string) (*agent.SessionInfo, error) {
	markerPath := filepath.Join(projectPath, ".shiftlog", "opencode-session.json")
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return nil, nil
	}

	var marker struct {
		SessionID      string `json:"sessionId"`
		DataDir        string `json:"dataDir"`
		Timestamp      int64  `json:"timestamp"`
		TranscriptData string `json:"transcriptData"`
	}
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, nil
	}

	if marker.SessionID == "" {
		return nil, nil
	}

	markerTime := time.UnixMilli(marker.Timestamp)
	if time.Since(markerTime) > agent.RecentSessionTimeout {
		return nil, nil
	}

	var transcriptData []byte
	if marker.TranscriptData != "" {
		transcriptData = []byte(marker.TranscriptData)
	}

	// If the plugin couldn't fetch transcript via the SDK, fall back to SQLite.
	if len(transcriptData) == 0 {
		dataDir := marker.DataDir
		if dataDir == "" {
			dataDir, _ = GetDataDir()
		}
		if dataDir != "" {
			transcriptData, _ = fetchTranscriptFromDB(dataDir, marker.SessionID)
		}
	}

	return &agent.SessionInfo{
		SessionID:      marker.SessionID,
		TranscriptPath: "",
		StartedAt:      markerTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
		TranscriptData: transcriptData,
	}, nil
}

// fetchTranscriptFromDB queries the SQLite database for messages in a session.
// Tries multiple query forms to handle schema changes across opencode versions.
func fetchTranscriptFromDB(dataDir, sessionID string) ([]byte, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	queries := []string{
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s' ORDER BY time_created;`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(json_patch(data, json_object('id', id))) FROM message WHERE session_id='%s';`, sessionID),
		fmt.Sprintf(`SELECT json_group_array(data) FROM message WHERE session_id='%s';`, sessionID),
	}
	for _, q := range queries {
		cmd := exec.Command("sqlite3", dbPath, q)
		output, err := cmd.Output()
		if err != nil {
			continue
		}
		result := []byte(strings.TrimSpace(string(output)))
		if string(result) != "[null]" && string(result) != "[]" && len(result) > 0 {
			return result, nil
		}
	}
	return nil, nil
}

// discoverFromFlatFiles tries the legacy flat file session discovery (pre-v1.2).
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

// discoverFromSQLite queries the OpenCode SQLite database for the most recent session.
func discoverFromSQLite(dataDir, projectID, projectPath string) (*agent.SessionInfo, error) {
	dbPath := filepath.Join(dataDir, "opencode.db")
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, nil
	}
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, nil
	}

	// Try multiple orderings to handle column name changes across versions.
	var sessionID string
	sessionQueries := []string{
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY time_updated DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' ORDER BY rowid DESC LIMIT 1;`, projectID),
		fmt.Sprintf(`SELECT id FROM session WHERE project_id='%s' LIMIT 1;`, projectID),
	}
	for _, q := range sessionQueries {
		cmd := exec.Command("sqlite3", "-separator", "\t", dbPath, q)
		if output, err := cmd.Output(); err == nil {
			if id := strings.TrimSpace(string(output)); id != "" {
				sessionID = id
				break
			}
		}
	}
	if sessionID == "" {
		return nil, nil
	}

	// Check recency; tolerate any timestamp format.
	timeQuery := fmt.Sprintf(`SELECT time_updated FROM session WHERE id='%s';`, sessionID)
	cmd := exec.Command("sqlite3", dbPath, timeQuery)
	timeOutput, err := cmd.Output()
	if err == nil {
		timeStr := strings.TrimSpace(string(timeOutput))
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z", "2006-01-02 15:04:05"} {
			if t, err := time.Parse(layout, timeStr); err == nil {
				if time.Since(t) > agent.RecentSessionTimeout {
					return nil, nil
				}
				break
			}
		}
		// If we can't parse the time, proceed anyway — better to try than skip.
	}

	transcriptData, _ := fetchTranscriptFromDB(dataDir, sessionID)
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

func (a *Agent) RestoreSession(projectPath, sessionID, gitBranch string,
	transcriptData []byte, messageCount int, summary string) error {
	_, err := WriteSessionFile(projectPath, sessionID, transcriptData)
	return err
}

func (a *Agent) ResumeCommand(sessionID string) (string, []string) {
	return "opencode", []string{"--session", sessionID}
}

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

	if contentRaw, ok := raw["content"]; ok {
		var text string
		if err := json.Unmarshal(contentRaw, &text); err == nil && text != "" {
			msg.Content = []agent.ContentBlock{{Type: "text", Text: text}}
			return msg
		}
		var blocks []agent.ContentBlock
		if err := json.Unmarshal(contentRaw, &blocks); err == nil && len(blocks) > 0 {
			msg.Content = blocks
			return msg
		}
	}

	if msgRaw, ok := raw["message"]; ok {
		var innerMsg agent.Message
		if err := json.Unmarshal(msgRaw, &innerMsg); err == nil {
			return &innerMsg
		}
	}

	return msg
}
