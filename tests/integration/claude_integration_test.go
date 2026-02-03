package integration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestClaudeCodeIntegration runs an end-to-end test with actual Claude Code CLI.
// This test requires:
// - ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN environment variable set
// - Claude Code CLI installed and in PATH
// - claudit binary built
//
// Authentication options:
// - ANTHROPIC_API_KEY: API key from console.anthropic.com (recommended for CI)
// - CLAUDE_CODE_OAUTH_TOKEN: OAuth token from `claude setup-token` (Pro/Max subscribers)
//   Note: OAuth token also requires ~/.claude.json with {"hasCompletedOnboarding": true}
//
// Opt out with: SKIP_CLAUDE_INTEGRATION=1 go test ./tests/integration/...
func TestClaudeCodeIntegration(t *testing.T) {
	// Skip conditions
	if os.Getenv("SKIP_CLAUDE_INTEGRATION") == "1" {
		t.Skip("SKIP_CLAUDE_INTEGRATION=1 is set")
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	oauthToken := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if apiKey == "" && oauthToken == "" {
		t.Fatal("Neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN set - set one of these or use SKIP_CLAUDE_INTEGRATION=1")
	}

	if oauthToken != "" && apiKey == "" {
		t.Log("Using CLAUDE_CODE_OAUTH_TOKEN authentication (Pro/Max subscription)")
	} else {
		t.Log("Using ANTHROPIC_API_KEY authentication")
	}

	// Check Claude CLI is available
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatal("Claude Code CLI not found in PATH - install it or use SKIP_CLAUDE_INTEGRATION=1")
	}

	// Check claudit binary
	clauditPath := os.Getenv("CLAUDIT_BINARY")
	if clauditPath == "" {
		// Try to find it relative to workspace
		clauditPath = filepath.Join(getWorkspaceRoot(), "claudit")
	}
	if _, err := os.Stat(clauditPath); err != nil {
		t.Fatalf("claudit binary not found at %s - run 'make build' first", clauditPath)
	}

	// Create temporary test directory
	tmpDir, err := os.MkdirTemp("", "claude-integration-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize git repo
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "Test User")

	// Create initial file and commit
	testFile := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Project\n"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}
	runGit(t, tmpDir, "add", "README.md")
	runGit(t, tmpDir, "commit", "-m", "Initial commit")

	// Initialize claudit
	cmd := exec.Command(clauditPath, "init", "--notes-ref=refs/notes/commits")
	cmd.Dir = tmpDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claudit init failed: %v\nOutput: %s", err, output)
	}

	// Verify hooks are configured
	settingsPath := filepath.Join(tmpDir, ".claude", "settings.local.json")
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read settings: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(settingsData, &settings); err != nil {
		t.Fatalf("Failed to parse settings: %v", err)
	}

	// Verify hook format is correct
	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected hooks object in settings, got: %v", settings)
	}
	postToolUse, ok := hooks["PostToolUse"].([]interface{})
	if !ok {
		t.Fatalf("Expected PostToolUse array in hooks, got: %v", hooks)
	}
	if len(postToolUse) == 0 {
		t.Fatal("PostToolUse hooks array is empty")
	}

	t.Log("Hook configuration verified successfully")

	// Create a test file that Claude will commit
	todoFile := filepath.Join(tmpDir, "todo.txt")
	if err := os.WriteFile(todoFile, []byte("- Buy milk\n- Walk dog\n"), 0644); err != nil {
		t.Fatalf("Failed to write todo file: %v", err)
	}

	// Run Claude Code with a simple prompt to commit the file
	// Use --print for non-interactive mode
	// Use --allowedTools to only allow Bash for git operations
	// Use --max-turns 5 to limit API calls
	claudeCmd := exec.Command("claude",
		"--print",
		"--allowedTools", "Bash(git:*),Read",
		"--max-turns", "5",
		"--dangerously-skip-permissions",
		"Please run: git add todo.txt && git commit -m 'Add todo list'",
	)
	claudeCmd.Dir = tmpDir
	claudeCmd.Env = append(os.Environ(),
		"PATH="+os.Getenv("PATH")+":"+filepath.Dir(clauditPath),
	)
	// Add authentication - API key takes precedence if both are set
	if apiKey != "" {
		claudeCmd.Env = append(claudeCmd.Env, "ANTHROPIC_API_KEY="+apiKey)
	}
	if oauthToken != "" {
		claudeCmd.Env = append(claudeCmd.Env, "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
	}

	// Set timeout and capture output for checking
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := claudeCmd.CombinedOutput()
		done <- result{output, err}
	}()

	var claudeOutput []byte
	select {
	case res := <-done:
		claudeOutput = res.output
		if res.err != nil {
			t.Logf("Claude command finished with error (may be expected): %v", res.err)
		}
	case <-time.After(60 * time.Second):
		claudeCmd.Process.Kill()
		t.Fatal("Claude command timed out after 60 seconds")
	}

	// Give hooks time to run
	time.Sleep(2 * time.Second)

	// Check for claudit warnings in stderr
	if strings.Contains(string(claudeOutput), "claudit: warning:") {
		t.Fatalf("FAIL: claudit logged warnings during execution, indicating hook failures:\n%s",
			string(claudeOutput))
	}

	// Check if commit was made
	cmd = exec.Command("git", "log", "--oneline", "-n", "2")
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to check git log: %v\nOutput: %s", err, output)
	}

	commitWasMade := strings.Contains(string(output), "todo")
	if !commitWasMade {
		t.Skip("Claude did not make the commit - cannot test note storage")
	}

	t.Log("✓ Commit was created successfully")
	t.Logf("Git log: %s", output)

	// CRITICAL TEST: If commit was made, note MUST exist
	// This is the core assertion that will catch silent storage failures
	cmd = exec.Command("git", "notes", "--ref=refs/notes/commits", "list")
	cmd.Dir = tmpDir
	notesOutput, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("FAIL: Commit was made but no git notes exist!\nThis means the claudit hook failed silently.\nError: %v\nOutput: %s", err, notesOutput)
	}

	if len(strings.TrimSpace(string(notesOutput))) == 0 {
		t.Fatal("FAIL: Commit was made but git notes list is empty!\nThis means the claudit hook failed silently.")
	}

	t.Log("✓ Git note was created by claudit hook")
	t.Logf("Notes: %s", notesOutput)

	// Verify note content is valid JSON
	cmd = exec.Command("git", "notes", "--ref=refs/notes/commits", "show", "HEAD")
	cmd.Dir = tmpDir
	noteContent, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("FAIL: Note exists but cannot be read: %v", err)
	}

	var noteData map[string]interface{}
	if err := json.Unmarshal(noteContent, &noteData); err != nil {
		t.Fatalf("FAIL: Note content is not valid JSON: %v\nContent: %s", err, noteContent[:min(len(noteContent), 500)])
	}

	// Verify required fields (using actual JSON field names from storage.StoredConversation)
	requiredFields := []string{"version", "session_id", "project_path", "git_branch", "message_count", "checksum", "transcript", "timestamp"}
	for _, field := range requiredFields {
		if _, ok := noteData[field]; !ok {
			t.Fatalf("FAIL: Note missing required field '%s'\nContent: %v", field, noteData)
		}
	}

	t.Log("✓ Note content is valid and contains all required fields")
	t.Logf("Note preview: version=%v, session_id=%v, message_count=%v",
		noteData["version"], noteData["session_id"], noteData["message_count"])
}

// TestClaudeCodeIntegration_MissingClaudit verifies that the test fails when claudit is not in PATH
// This proves our stricter assertions can catch real failures
func TestClaudeCodeIntegration_MissingClaudit(t *testing.T) {
	// Skip conditions
	if os.Getenv("SKIP_CLAUDE_INTEGRATION") == "1" {
		t.Skip("SKIP_CLAUDE_INTEGRATION=1 is set")
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	oauthToken := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if apiKey == "" && oauthToken == "" {
		t.Skip("Neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN set")
	}

	// Check Claude CLI is available
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("Claude Code CLI not found in PATH")
	}

	// Create temporary test directory
	tmpDir, err := os.MkdirTemp("", "claude-integration-fail-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize git repo
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "Test User")

	// Create initial file and commit
	testFile := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Project\n"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}
	runGit(t, tmpDir, "add", "README.md")
	runGit(t, tmpDir, "commit", "-m", "Initial commit")

	// Manually create hook configuration WITHOUT adding claudit to PATH
	settingsDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("Failed to create .claude dir: %v", err)
	}

	// Write hook config that references claudit (which won't be in PATH)
	settingsContent := `{
  "hooks": {
    "PostToolUse": [
      {
        "matcher": "Bash",
        "hooks": [
          {
            "type": "command",
            "command": "claudit store",
            "timeout": 30
          }
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.local.json"), []byte(settingsContent), 0644); err != nil {
		t.Fatalf("Failed to write settings: %v", err)
	}

	// Create a test file that Claude will commit
	todoFile := filepath.Join(tmpDir, "todo.txt")
	if err := os.WriteFile(todoFile, []byte("- Test item\n"), 0644); err != nil {
		t.Fatalf("Failed to write todo file: %v", err)
	}

	// Run Claude Code WITHOUT claudit in PATH
	claudeCmd := exec.Command("claude",
		"--print",
		"--allowedTools", "Bash(git:*),Read",
		"--max-turns", "5",
		"--dangerously-skip-permissions",
		"Please run: git add todo.txt && git commit -m 'Add todo list'",
	)
	claudeCmd.Dir = tmpDir
	// Explicitly set PATH without claudit directory
	claudeCmd.Env = append(os.Environ(), "PATH=/usr/bin:/bin:/usr/local/bin")
	if apiKey != "" {
		claudeCmd.Env = append(claudeCmd.Env, "ANTHROPIC_API_KEY="+apiKey)
	}
	if oauthToken != "" {
		claudeCmd.Env = append(claudeCmd.Env, "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
	}

	// Run with timeout
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := claudeCmd.CombinedOutput()
		done <- result{output, err}
	}()

	var claudeOutput []byte
	select {
	case res := <-done:
		claudeOutput = res.output
	case <-time.After(60 * time.Second):
		claudeCmd.Process.Kill()
		t.Fatal("Claude command timed out")
	}

	// Give hooks time to (fail to) run
	time.Sleep(2 * time.Second)

	// Check if commit was made
	cmd := exec.Command("git", "log", "--oneline", "-n", "2")
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("Git log: %s", output)
	}

	commitWasMade := strings.Contains(string(output), "todo")
	if !commitWasMade {
		t.Skip("Claude did not make the commit - cannot test failure case")
	}

	t.Log("✓ Commit was created")

	// THIS IS THE KEY TEST: Commit was made, but note should NOT exist
	// because claudit was not in PATH
	cmd = exec.Command("git", "notes", "--ref=refs/notes/commits", "list")
	cmd.Dir = tmpDir
	notesOutput, err := cmd.CombinedOutput()

	// We EXPECT this to fail (note should not exist)
	if err == nil && len(strings.TrimSpace(string(notesOutput))) > 0 {
		t.Fatal("UNEXPECTED: Note was created even though claudit was not in PATH!")
	}

	// Verify our updated test would catch this
	t.Log("✓ Confirmed: Note was NOT created (as expected when claudit not in PATH)")
	t.Log("✓ This proves our stricter test assertions would catch this failure")

	// Check Claude output for hook errors
	if strings.Contains(string(claudeOutput), "claudit") {
		t.Logf("Claude output mentions claudit (hook may have tried to run): %s", string(claudeOutput))
	}
}

// TestStoreCommandFailureHandling tests that the store command returns errors instead of exiting silently
// This test should FAIL with the current implementation (bug) and PASS after we fix it
func TestStoreCommandFailureHandling(t *testing.T) {
	clauditPath := os.Getenv("CLAUDIT_BINARY")
	if clauditPath == "" {
		clauditPath = filepath.Join(getWorkspaceRoot(), "claudit")
	}
	if _, err := os.Stat(clauditPath); err != nil {
		t.Fatalf("claudit binary not found at %s", clauditPath)
	}

	// Create temporary git repo
	tmpDir, err := os.MkdirTemp("", "claudit-store-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize git repo with a commit
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}
	runGit(t, tmpDir, "add", "test.txt")
	runGit(t, tmpDir, "commit", "-m", "Initial commit")

	t.Run("missing transcript file should fail", func(t *testing.T) {
		hookInput := `{
			"session_id": "test-session",
			"transcript_path": "/nonexistent/transcript.jsonl",
			"tool_name": "Bash",
			"tool_input": {
				"command": "git commit -m 'test'"
			}
		}`

		cmd := exec.Command(clauditPath, "store")
		cmd.Dir = tmpDir
		cmd.Stdin = strings.NewReader(hookInput)
		output, err := cmd.CombinedOutput()

		// BUG: This currently exits with 0 (success) even though it failed
		// After fix: should exit with non-zero code
		if err == nil {
			t.Errorf("FAIL: store command succeeded when it should have failed (missing transcript)\nThis is the bug! It exits with code 0 instead of returning an error.\nOutput: %s", output)
		} else {
			t.Logf("✓ Correctly failed with error: %v", err)
		}

		// Should log a warning
		if !strings.Contains(string(output), "warning") && !strings.Contains(string(output), "failed") {
			t.Errorf("Expected warning message in output, got: %s", output)
		}
	})

	t.Run("invalid commit SHA should fail", func(t *testing.T) {
		// Create a valid transcript file
		transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
		transcriptData := `{"type":"session_start","id":"test"}
{"type":"user","content":"test message"}`
		if err := os.WriteFile(transcriptPath, []byte(transcriptData), 0644); err != nil {
			t.Fatalf("Failed to write transcript: %v", err)
		}

		// But point to a repo with no HEAD (empty repo)
		emptyDir, err := os.MkdirTemp("", "empty-repo-*")
		if err != nil {
			t.Fatalf("Failed to create empty dir: %v", err)
		}
		defer os.RemoveAll(emptyDir)
		runGit(t, emptyDir, "init")

		hookInput := fmt.Sprintf(`{
			"session_id": "test-session",
			"transcript_path": "%s",
			"tool_name": "Bash",
			"tool_input": {
				"command": "git commit -m 'test'"
			}
		}`, transcriptPath)

		cmd := exec.Command(clauditPath, "store")
		cmd.Dir = emptyDir // Empty repo with no HEAD
		cmd.Stdin = strings.NewReader(hookInput)
		output, err := cmd.CombinedOutput()

		// BUG: This currently exits with 0 (success) even though there's no HEAD
		// After fix: should exit with non-zero code
		if err == nil {
			t.Errorf("FAIL: store command succeeded when it should have failed (no HEAD commit)\nThis is the bug! It exits with code 0 instead of returning an error.\nOutput: %s", output)
		} else {
			t.Logf("✓ Correctly failed with error: %v", err)
		}
	})
}

// getWorkspaceRoot finds the workspace root by looking for go.mod
func getWorkspaceRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

// runGit runs a git command in the specified directory
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %v\nOutput: %s", args, err, output)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
