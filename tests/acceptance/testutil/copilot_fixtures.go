package testutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// SampleCopilotTranscript returns a sample Copilot CLI session JSON transcript for testing.
func SampleCopilotTranscript() string {
	session := map[string]interface{}{
		"sessionId": "test-session",
		"cwd":       "/test/project",
		"model":     "gpt-4",
		"messages": []map[string]interface{}{
			{
				"role":    "user",
				"content": "Hello, can you help me with a task?",
			},
			{
				"role":    "assistant",
				"content": "Of course! What would you like help with?",
			},
			{
				"role":    "user",
				"content": "Please create a file called test.txt",
			},
			{
				"role":    "assistant",
				"content": "I'll create that file for you.",
				"tool_calls": []map[string]interface{}{
					{
						"id":   "call_1",
						"type": "function",
						"function": map[string]interface{}{
							"name":      "shell_run",
							"arguments": `{"command":"echo 'test content' > test.txt"}`,
						},
					},
				},
			},
		},
	}

	data, _ := json.MarshalIndent(session, "", "  ")
	return string(data)
}

// SampleCopilotHookInput returns sample postToolUse hook JSON for Copilot CLI testing.
func SampleCopilotHookInput(sessionID, transcriptPath, command string) string {
	// Copilot's postToolUse hook sends: {timestamp, cwd, toolName, toolArgs}
	// We include session_id and transcript_path for the shared store test path.
	input := map[string]interface{}{
		"session_id":      sessionID,
		"transcript_path": transcriptPath,
		"tool_name":       "shell_run",
		"tool_input": map[string]interface{}{
			"command": command,
		},
	}
	data, _ := json.Marshal(input)
	return string(data)
}

// SampleCopilotHookInputNonShell returns hook JSON for a non-shell Copilot tool.
func SampleCopilotHookInputNonShell(sessionID string) string {
	input := map[string]interface{}{
		"session_id": sessionID,
		"tool_name":  "view",
		"tool_input": map[string]interface{}{
			"path": "/some/file.txt",
		},
	}
	data, _ := json.Marshal(input)
	return string(data)
}

// copilotPrepareTranscript writes a Copilot JSON transcript file.
func copilotPrepareTranscript(baseDir, sessionID, transcript string) (string, error) {
	path := filepath.Join(baseDir, "transcript.json")
	return path, os.WriteFile(path, []byte(transcript), 0644)
}

// copilotSessionDir computes the Copilot session state directory path.
func copilotSessionDir(homeDir, projectPath string) string {
	return filepath.Join(homeDir, ".copilot", "session-state")
}

// copilotReadRestoredTranscript finds and reads the restored Copilot transcript.
func copilotReadRestoredTranscript(homeDir, projectPath, sessionID string) ([]byte, error) {
	sessionDir := filepath.Join(homeDir, ".copilot", "session-state")
	var found string
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.Contains(entry.Name(), sessionID) && strings.HasSuffix(entry.Name(), ".json") {
			found = filepath.Join(sessionDir, entry.Name())
			break
		}
	}
	if found == "" {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(found)
}
