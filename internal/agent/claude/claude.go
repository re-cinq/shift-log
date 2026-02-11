package claude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/re-cinq/claudit/internal/agent"
)

func init() {
	agent.Register(&Agent{})
}

// Agent implements the agent.Agent interface for Claude Code.
type Agent struct{}

func (a *Agent) Name() agent.Name        { return agent.Claude }
func (a *Agent) DisplayName() string      { return "Claude Code" }

// ConfigureHooks sets up Claude Code hooks in .claude/settings.local.json.
func (a *Agent) ConfigureHooks(repoRoot string) error {
	claudeDir := filepath.Join(repoRoot, ".claude")
	settings, err := ReadSettings(claudeDir)
	if err != nil {
		return fmt.Errorf("failed to read Claude settings: %w", err)
	}

	AddClauditHook(settings)
	AddSessionHooks(settings)

	if err := WriteSettings(claudeDir, settings); err != nil {
		return fmt.Errorf("failed to write Claude settings: %w", err)
	}
	return nil
}

// DiagnoseHooks validates Claude Code hook configuration.
func (a *Agent) DiagnoseHooks(repoRoot string) []agent.DiagnosticCheck {
	var checks []agent.DiagnosticCheck

	settingsPath := filepath.Join(repoRoot, ".claude", "settings.local.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "Claude Code hook configuration",
			OK:      false,
			Message: "No .claude/settings.local.json found. Run 'claudit init' to configure.",
		})
		return checks
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "Claude Code hook configuration",
			OK:      false,
			Message: fmt.Sprintf("Invalid JSON in settings file: %v", err),
		})
		return checks
	}

	hooks, hasHooks := settings["hooks"].(map[string]interface{})
	if !hasHooks {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "Claude Code hooks",
			OK:      false,
			Message: "Missing 'hooks' key in settings. Run 'claudit init' to fix.",
		})
		return checks
	}

	// Check PostToolUse
	postToolUse, hasPostToolUse := hooks["PostToolUse"]
	if !hasPostToolUse || !hasClauditCommand(postToolUse, "claudit store") {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "PostToolUse hook",
			OK:      false,
			Message: "'claudit store' hook not found in PostToolUse. Run 'claudit init' to fix.",
		})
	} else {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "PostToolUse hook",
			OK:      true,
			Message: "Found PostToolUse hook configuration",
		})
	}

	// Check SessionStart
	sessionStart, hasSessionStart := hooks["SessionStart"]
	if !hasSessionStart || !hasClauditCommand(sessionStart, "claudit session-start") {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "SessionStart hook",
			OK:      false,
			Message: "Missing SessionStart hook (manual commit capture won't work). Run 'claudit init' to add.",
		})
	} else {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "SessionStart hook",
			OK:      true,
			Message: "Found SessionStart hook",
		})
	}

	// Check SessionEnd
	sessionEnd, hasSessionEnd := hooks["SessionEnd"]
	if !hasSessionEnd || !hasClauditCommand(sessionEnd, "claudit session-end") {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "SessionEnd hook",
			OK:      false,
			Message: "Missing SessionEnd hook (manual commit capture won't work). Run 'claudit init' to add.",
		})
	} else {
		checks = append(checks, agent.DiagnosticCheck{
			Name:    "SessionEnd hook",
			OK:      true,
			Message: "Found SessionEnd hook",
		})
	}

	return checks
}

// ParseHookInput parses Claude Code's PostToolUse hook JSON.
func (a *Agent) ParseHookInput(raw []byte) (*agent.HookData, error) {
	var hook struct {
		SessionID      string `json:"session_id"`
		TranscriptPath string `json:"transcript_path"`
		ToolName       string `json:"tool_name"`
		ToolInput      struct {
			Command string `json:"command"`
		} `json:"tool_input"`
	}
	if err := json.Unmarshal(raw, &hook); err != nil {
		return nil, err
	}
	return &agent.HookData{
		SessionID:      hook.SessionID,
		TranscriptPath: hook.TranscriptPath,
		ToolName:       hook.ToolName,
		Command:        hook.ToolInput.Command,
	}, nil
}

// IsCommitCommand checks if a tool invocation represents a git commit.
func (a *Agent) IsCommitCommand(toolName, command string) bool {
	if toolName != "Bash" {
		return false
	}
	return strings.Contains(command, "git commit") ||
		strings.Contains(command, "git-commit")
}

// ParseTranscript parses a Claude Code JSONL transcript.
func (a *Agent) ParseTranscript(r io.Reader) (*agent.Transcript, error) {
	return ParseJSONLTranscript(r)
}

// ParseTranscriptFile parses a Claude Code JSONL transcript from a file.
func (a *Agent) ParseTranscriptFile(path string) (*agent.Transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return ParseJSONLTranscript(f)
}

// DiscoverSession finds an active or recent Claude Code session.
func (a *Agent) DiscoverSession(projectPath string) (*agent.SessionInfo, error) {
	// Try sessions-index.json first
	index, err := ReadSessionsIndex(projectPath)
	if err == nil && len(index.Entries) > 0 {
		session := findRecentSession(index, projectPath)
		if session != nil {
			return session, nil
		}
	}

	// Fallback: scan for recent .jsonl files
	return scanForRecentSession(projectPath)
}

// RestoreSession writes a transcript to Claude Code's expected location.
func (a *Agent) RestoreSession(projectPath, sessionID, gitBranch string,
	transcriptData []byte, messageCount int, summary string) error {

	sessionPath, err := WriteSessionFile(projectPath, sessionID, transcriptData)
	if err != nil {
		return err
	}

	fileInfo, err := os.Stat(sessionPath)
	if err != nil {
		return fmt.Errorf("could not stat session file: %w", err)
	}

	index, err := ReadSessionsIndex(projectPath)
	if err != nil {
		return fmt.Errorf("could not read sessions index: %w", err)
	}

	firstPrompt := extractFirstPrompt(transcriptData)
	now := time.Now().UTC().Format(time.RFC3339Nano)

	entry := SessionEntry{
		SessionID:    sessionID,
		FullPath:     sessionPath,
		FileMtime:    fileInfo.ModTime().UnixMilli(),
		FirstPrompt:  firstPrompt,
		Summary:      summary,
		MessageCount: messageCount,
		Created:      now,
		Modified:     now,
		GitBranch:    gitBranch,
		ProjectPath:  projectPath,
		IsSidechain:  false,
	}

	AddOrUpdateSessionEntry(index, entry)

	if err := WriteSessionsIndex(projectPath, index); err != nil {
		return fmt.Errorf("could not write sessions index: %w", err)
	}

	return nil
}

// ResumeCommand returns the command to resume a Claude Code session.
func (a *Agent) ResumeCommand(sessionID string) (string, []string) {
	return "claude", []string{"--resume", sessionID}
}

// ToolAliases returns Claude Code's tool name mappings.
// Claude Code uses canonical names directly, so no aliasing needed.
func (a *Agent) ToolAliases() map[string]string {
	return nil
}

// ParseJSONLTranscript parses a JSONL transcript from a reader.
func ParseJSONLTranscript(r io.Reader) (*agent.Transcript, error) {
	scanner := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var entries []agent.TranscriptEntry

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry agent.TranscriptEntry
		_ = json.Unmarshal(line, &entry)
		entry.Raw = json.RawMessage(line)
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return &agent.Transcript{Entries: entries}, nil
}

// extractFirstPrompt extracts the first user message from transcript data.
func extractFirstPrompt(transcriptData []byte) string {
	transcript, err := ParseJSONLTranscript(strings.NewReader(string(transcriptData)))
	if err != nil {
		return "No prompt"
	}

	for _, entry := range transcript.Entries {
		if entry.Type == agent.MessageTypeUser && entry.Message != nil {
			var prompt string
			for _, block := range entry.Message.Content {
				if block.Type == "text" && block.Text != "" {
					prompt = block.Text
					break
				}
			}
			if prompt == "" {
				continue
			}
			if len(prompt) > 200 {
				return prompt[:197] + "..."
			}
			return prompt
		}
	}

	return "No prompt"
}

// findRecentSession finds a recent session from the sessions-index.
func findRecentSession(index *SessionsIndex, projectPath string) *agent.SessionInfo {
	now := time.Now()
	const recentTimeout = 5 * time.Minute

	var bestEntry *SessionEntry
	var bestModified time.Time

	for i := range index.Entries {
		entry := &index.Entries[i]

		if !pathsEqual(entry.ProjectPath, projectPath) {
			continue
		}

		modified, err := time.Parse(time.RFC3339Nano, entry.Modified)
		if err != nil {
			modified, err = time.Parse(time.RFC3339, entry.Modified)
			if err != nil {
				continue
			}
		}

		if now.Sub(modified) > recentTimeout {
			continue
		}

		if bestEntry == nil || modified.After(bestModified) {
			bestEntry = entry
			bestModified = modified
		}
	}

	if bestEntry == nil {
		return nil
	}

	return &agent.SessionInfo{
		SessionID:      bestEntry.SessionID,
		TranscriptPath: bestEntry.FullPath,
		StartedAt:      bestEntry.Created,
		ProjectPath:    bestEntry.ProjectPath,
	}
}

// scanForRecentSession scans Claude's session directory for recently modified .jsonl files.
func scanForRecentSession(projectPath string) (*agent.SessionInfo, error) {
	sessionDir, err := GetSessionDir(projectPath)
	if err != nil {
		return nil, nil
	}

	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, nil
	}

	now := time.Now()
	const recentTimeout = 5 * time.Minute
	var bestPath string
	var bestSessionID string
	var bestModTime time.Time

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
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

		if bestPath == "" || modTime.After(bestModTime) {
			bestPath = filepath.Join(sessionDir, entry.Name())
			bestSessionID = strings.TrimSuffix(entry.Name(), ".jsonl")
			bestModTime = modTime
		}
	}

	if bestPath == "" {
		return nil, nil
	}

	return &agent.SessionInfo{
		SessionID:      bestSessionID,
		TranscriptPath: bestPath,
		StartedAt:      bestModTime.Format(time.RFC3339),
		ProjectPath:    projectPath,
	}, nil
}

// hasClauditCommand checks if a hook list contains a specific claudit command.
func hasClauditCommand(hookConfig interface{}, command string) bool {
	hookList, ok := hookConfig.([]interface{})
	if !ok {
		return false
	}
	for _, h := range hookList {
		hookMap, _ := h.(map[string]interface{})
		hookCmds, _ := hookMap["hooks"].([]interface{})
		for _, hc := range hookCmds {
			hcMap, _ := hc.(map[string]interface{})
			if cmd, ok := hcMap["command"].(string); ok {
				if strings.Contains(cmd, command) {
					return true
				}
			}
		}
	}
	return false
}

// pathsEqual compares two paths after resolving symlinks.
func pathsEqual(a, b string) bool {
	resolvedA, errA := filepath.EvalSymlinks(a)
	resolvedB, errB := filepath.EvalSymlinks(b)
	if errA == nil && errB == nil {
		return resolvedA == resolvedB
	}
	return a == b
}

// LookupBinary checks if the claude binary is in PATH.
func LookupBinary() (string, error) {
	return exec.LookPath("claude")
}
