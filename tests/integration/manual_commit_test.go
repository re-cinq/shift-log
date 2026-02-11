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

// verifyNoteOnHead checks that the HEAD commit has a valid claudit git note
// with all required fields and the expected agent value.
func verifyNoteOnHead(t *testing.T, tmpDir, expectedAgent string) {
	t.Helper()

	// Note MUST exist
	cmd := exec.Command("git", "notes", "--ref=refs/notes/claude-conversations", "list")
	cmd.Dir = tmpDir
	notesOutput, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("FAIL: Commit was made but no git notes exist!\nError: %v\nOutput: %s", err, notesOutput)
	}
	if len(strings.TrimSpace(string(notesOutput))) == 0 {
		t.Fatal("FAIL: Commit was made but git notes list is empty!")
	}

	t.Log("Git note was created by claudit post-commit hook")

	// Read note content
	cmd = exec.Command("git", "notes", "--ref=refs/notes/claude-conversations", "show", "HEAD")
	cmd.Dir = tmpDir
	noteContent, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("FAIL: Note exists but cannot be read: %v", err)
	}

	var noteData map[string]interface{}
	if err := json.Unmarshal(noteContent, &noteData); err != nil {
		t.Fatalf("FAIL: Note content is not valid JSON: %v\nContent: %s", err, noteContent[:min(len(noteContent), 500)])
	}

	// Verify required fields
	requiredFields := []string{"version", "session_id", "project_path", "git_branch", "message_count", "checksum", "transcript", "timestamp"}
	for _, field := range requiredFields {
		if _, ok := noteData[field]; !ok {
			t.Fatalf("FAIL: Note missing required field '%s'\nContent: %v", field, noteData)
		}
	}

	// Verify agent field
	if noteData["agent"] != expectedAgent {
		t.Errorf("Expected agent='%s', got %v", expectedAgent, noteData["agent"])
	}

	t.Log("Note content is valid and contains all required fields")
	t.Logf("Note preview: version=%v, session_id=%v, agent=%v, message_count=%v",
		noteData["version"], noteData["session_id"], noteData["agent"], noteData["message_count"])
}

// setupManualCommitRepo creates a temp git repo, initializes claudit with the
// given agent, and returns the tmpDir and clauditPath. Caller must defer os.RemoveAll(tmpDir).
func setupManualCommitRepo(t *testing.T, agentName string) (tmpDir, clauditPath string) {
	t.Helper()

	clauditPath = os.Getenv("CLAUDIT_BINARY")
	if clauditPath == "" {
		clauditPath = filepath.Join(getWorkspaceRoot(), "claudit")
	}
	if _, err := os.Stat(clauditPath); err != nil {
		t.Fatalf("claudit binary not found at %s - run 'go build'", clauditPath)
	}

	var err error
	tmpDir, err = os.MkdirTemp("", fmt.Sprintf("%s-manual-commit-*", agentName))
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Initialize git repo
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "Test User")

	// Create initial commit
	testFile := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Project\n"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}
	runGit(t, tmpDir, "add", "README.md")
	runGit(t, tmpDir, "commit", "-m", "Initial commit")

	// Initialize claudit with the specified agent
	initArgs := []string{"init"}
	if agentName != "claude" {
		initArgs = append(initArgs, "--agent="+agentName)
	}
	cmd := exec.Command(clauditPath, initArgs...)
	cmd.Dir = tmpDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claudit init failed: %v\nOutput: %s", err, output)
	}

	return tmpDir, clauditPath
}

// manualCommitNewFile creates a new file and commits it manually.
// This ensures there is always something to commit regardless of what the agent did.
func manualCommitNewFile(t *testing.T, tmpDir string) {
	t.Helper()

	manualFile := filepath.Join(tmpDir, "manual-file.txt")
	if err := os.WriteFile(manualFile, []byte("manually created file\n"), 0644); err != nil {
		t.Fatalf("Failed to write manual file: %v", err)
	}
	runGit(t, tmpDir, "add", "manual-file.txt")
	runGit(t, tmpDir, "commit", "-m", "Manual commit after agent session")
}

// TestClaudeManualCommit tests the manual commit flow with Claude Code CLI.
// The agent runs a simple command (establishing a session), then we manually
// commit a new file and verify the post-commit hook discovers the session.
//
// Requires: ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN, claude CLI in PATH
// Opt out: SKIP_CLAUDE_INTEGRATION=1
func TestClaudeManualCommit(t *testing.T) {
	if os.Getenv("SKIP_CLAUDE_INTEGRATION") == "1" {
		t.Skip("SKIP_CLAUDE_INTEGRATION=1 is set")
	}

	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	oauthToken := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if apiKey == "" && oauthToken == "" {
		t.Skip("Neither ANTHROPIC_API_KEY nor CLAUDE_CODE_OAUTH_TOKEN set")
	}

	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("Claude Code CLI not found in PATH")
	}

	tmpDir, clauditPath := setupManualCommitRepo(t, "claude")
	defer os.RemoveAll(tmpDir)

	// Run Claude with a simple command to establish a session.
	// We only need session files created — the commit is ours.
	claudeCmd := exec.Command("claude",
		"--print",
		"--allowedTools", "Bash",
		"--max-turns", "3",
		"--dangerously-skip-permissions",
		"Run: echo 'hello world'",
	)
	claudeCmd.Dir = tmpDir
	claudeCmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(clauditPath)+":"+os.Getenv("PATH"),
	)
	if apiKey != "" {
		claudeCmd.Env = append(claudeCmd.Env, "ANTHROPIC_API_KEY="+apiKey)
	}
	if oauthToken != "" {
		claudeCmd.Env = append(claudeCmd.Env, "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
	}

	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := claudeCmd.CombinedOutput()
		done <- result{output, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Logf("Claude command finished with error (may be expected): %v", res.err)
		}
		t.Logf("Claude output: %s", res.output)
	case <-time.After(60 * time.Second):
		claudeCmd.Process.Kill()
		t.Fatal("Claude command timed out after 60 seconds")
	}

	t.Log("Agent session established, making manual commit...")

	// Create a new file and commit — this triggers the post-commit hook
	manualCommitNewFile(t, tmpDir)

	// Give post-commit hook time to complete
	time.Sleep(2 * time.Second)

	t.Log("Manual commit created, verifying note...")
	verifyNoteOnHead(t, tmpDir, "claude")
}

// TestCodexManualCommit tests the manual commit flow with Codex CLI.
//
// Requires: OPENAI_API_KEY, codex CLI in PATH
// Opt out: SKIP_CODEX_INTEGRATION=1
func TestCodexManualCommit(t *testing.T) {
	if os.Getenv("SKIP_CODEX_INTEGRATION") == "1" {
		t.Skip("SKIP_CODEX_INTEGRATION=1 is set")
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("Codex CLI not found in PATH")
	}

	tmpDir, clauditPath := setupManualCommitRepo(t, "codex")
	defer os.RemoveAll(tmpDir)

	// Login Codex CLI
	loginCmd := exec.Command("bash", "-c",
		fmt.Sprintf("echo %q | codex login --with-api-key", apiKey))
	loginCmd.Dir = tmpDir
	if loginOutput, err := loginCmd.CombinedOutput(); err != nil {
		t.Fatalf("codex login failed: %v\nOutput: %s", err, loginOutput)
	}

	// Run Codex with a simple command to establish a session
	codexCmd := exec.Command("codex", "exec",
		"--dangerously-bypass-approvals-and-sandbox",
		"Run: echo 'hello world'",
	)
	codexCmd.Dir = tmpDir
	codexCmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(clauditPath)+":"+os.Getenv("PATH"),
		"OPENAI_API_KEY="+apiKey,
	)

	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := codexCmd.CombinedOutput()
		done <- result{output, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Logf("Codex command finished with error (may be expected): %v", res.err)
		}
		t.Logf("Codex output: %s", res.output)
	case <-time.After(90 * time.Second):
		codexCmd.Process.Kill()
		t.Fatal("Codex command timed out after 90 seconds")
	}

	t.Log("Agent session established, making manual commit...")

	// Create a new file and commit — triggers post-commit hook
	manualCommitNewFile(t, tmpDir)

	time.Sleep(2 * time.Second)

	t.Log("Manual commit created, verifying note...")
	verifyNoteOnHead(t, tmpDir, "codex")
}

// TestGeminiManualCommit tests the manual commit flow with Gemini CLI.
//
// Requires: GEMINI_API_KEY or GOOGLE_API_KEY, gemini CLI in PATH
// Opt out: SKIP_GEMINI_INTEGRATION=1
func TestGeminiManualCommit(t *testing.T) {
	if os.Getenv("SKIP_GEMINI_INTEGRATION") == "1" {
		t.Skip("SKIP_GEMINI_INTEGRATION=1 is set")
	}

	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	googleAPIKey := os.Getenv("GOOGLE_API_KEY")
	if geminiAPIKey == "" && googleAPIKey == "" {
		t.Skip("Neither GEMINI_API_KEY nor GOOGLE_API_KEY set")
	}

	if _, err := exec.LookPath("gemini"); err != nil {
		t.Skip("Gemini CLI not found in PATH")
	}

	tmpDir, clauditPath := setupManualCommitRepo(t, "gemini")
	defer os.RemoveAll(tmpDir)

	// Run Gemini with a simple command to establish a session
	geminiCmd := exec.Command("gemini",
		"-p", "Run: echo 'hello world'",
		"--approval-mode", "yolo",
	)
	geminiCmd.Dir = tmpDir
	geminiCmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(clauditPath)+":"+os.Getenv("PATH"),
	)
	if geminiAPIKey != "" {
		geminiCmd.Env = append(geminiCmd.Env, "GEMINI_API_KEY="+geminiAPIKey)
	}
	if googleAPIKey != "" {
		geminiCmd.Env = append(geminiCmd.Env, "GOOGLE_API_KEY="+googleAPIKey)
	}

	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := geminiCmd.CombinedOutput()
		done <- result{output, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Logf("Gemini command finished with error (may be expected): %v", res.err)
		}
		t.Logf("Gemini output: %s", res.output)
	case <-time.After(90 * time.Second):
		geminiCmd.Process.Kill()
		t.Fatal("Gemini command timed out after 90 seconds")
	}

	t.Log("Agent session established, making manual commit...")

	// Create a new file and commit — triggers post-commit hook
	manualCommitNewFile(t, tmpDir)

	time.Sleep(2 * time.Second)

	t.Log("Manual commit created, verifying note...")
	verifyNoteOnHead(t, tmpDir, "gemini")
}

// TestOpenCodeManualCommit tests the manual commit flow with OpenCode CLI.
//
// Requires: GEMINI_API_KEY or GOOGLE_GENERATIVE_AI_API_KEY, opencode CLI in PATH
// Opt out: SKIP_OPENCODE_INTEGRATION=1
func TestOpenCodeManualCommit(t *testing.T) {
	if os.Getenv("SKIP_OPENCODE_INTEGRATION") == "1" {
		t.Skip("SKIP_OPENCODE_INTEGRATION=1 is set")
	}

	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	googleGenAIKey := os.Getenv("GOOGLE_GENERATIVE_AI_API_KEY")
	if geminiAPIKey == "" && googleGenAIKey == "" {
		t.Skip("Neither GEMINI_API_KEY nor GOOGLE_GENERATIVE_AI_API_KEY set")
	}

	apiKey := googleGenAIKey
	if apiKey == "" {
		apiKey = geminiAPIKey
	}

	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("OpenCode CLI not found in PATH")
	}

	tmpDir, clauditPath := setupManualCommitRepo(t, "opencode")
	defer os.RemoveAll(tmpDir)

	// Write opencode.json config
	opencodeConfig := map[string]interface{}{
		"$schema":    "https://opencode.ai/config.json",
		"model":      "google/gemini-2.5-flash",
		"permission": "allow",
	}
	configData, err := json.MarshalIndent(opencodeConfig, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal opencode config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "opencode.json"), configData, 0644); err != nil {
		t.Fatalf("Failed to write opencode.json: %v", err)
	}

	// Run OpenCode with a simple command to establish a session
	opencodeCmd := exec.Command("opencode", "run",
		"Run: echo 'hello world'",
	)
	opencodeCmd.Dir = tmpDir
	opencodeCmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(clauditPath)+":"+os.Getenv("PATH"),
		"GOOGLE_GENERATIVE_AI_API_KEY="+apiKey,
	)

	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := opencodeCmd.CombinedOutput()
		done <- result{output, err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			t.Logf("OpenCode command finished with error (may be expected): %v", res.err)
		}
		t.Logf("OpenCode output: %s", res.output)
	case <-time.After(90 * time.Second):
		opencodeCmd.Process.Kill()
		t.Fatal("OpenCode command timed out after 90 seconds")
	}

	t.Log("Agent session established, making manual commit...")

	// Create a new file and commit — triggers post-commit hook
	manualCommitNewFile(t, tmpDir)

	time.Sleep(2 * time.Second)

	t.Log("Manual commit created, verifying note...")
	verifyNoteOnHead(t, tmpDir, "opencode")
}
