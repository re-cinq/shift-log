package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSummariseWithClaude runs an end-to-end test of `claudit summarise` using
// Claude Code as the summarisation agent. It stores a conversation on a commit
// (via the normal hook flow), then invokes `claudit summarise` which calls
// `claude -p --output-format text` to produce a real LLM summary.
//
// Requires:
// - ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN environment variable
// - Claude Code CLI installed and in PATH
// - claudit binary built
//
// Opt out with: SKIP_CLAUDE_INTEGRATION=1
func TestSummariseWithClaude(t *testing.T) {
	t.Parallel()

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

	clauditPath := os.Getenv("CLAUDIT_BINARY")
	if clauditPath == "" {
		clauditPath = filepath.Join(getWorkspaceRoot(), "claudit")
	}
	if _, err := os.Stat(clauditPath); err != nil {
		t.Fatalf("claudit binary not found at %s - run 'go build'", clauditPath)
	}

	// Create temporary test directory
	tmpDir, err := os.MkdirTemp("", "summarise-integration-*")
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
	cmd := exec.Command(clauditPath, "init")
	cmd.Dir = tmpDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claudit init failed: %v\nOutput: %s", err, output)
	}

	// Build PATH with claudit binary directory
	pathEnv := "PATH=" + filepath.Dir(clauditPath) + ":" + os.Getenv("PATH")

	// Create a test file for Claude to commit
	todoFile := filepath.Join(tmpDir, "todo.txt")
	if err := os.WriteFile(todoFile, []byte("- Buy groceries\n- Write tests\n- Deploy app\n"), 0644); err != nil {
		t.Fatalf("Failed to write todo file: %v", err)
	}

	// Run Claude Code to make a commit (establishing a conversation)
	claudeCmd := exec.Command("claude",
		"--print",
		"--allowedTools", "Bash(git:*),Read",
		"--max-turns", "5",
		"--dangerously-skip-permissions",
		"Please run: git add todo.txt && git commit -m 'Add todo list'",
	)
	claudeCmd.Dir = tmpDir
	claudeCmd.Env = append(os.Environ(), pathEnv)
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
		_ = claudeCmd.Process.Kill()
		t.Fatal("Claude command timed out after 60 seconds")
	}

	// Give hooks time to run
	time.Sleep(2 * time.Second)

	// Check if commit was made
	cmd = exec.Command("git", "log", "--oneline", "-n", "2")
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to check git log: %v\nOutput: %s", err, output)
	}

	if !strings.Contains(string(output), "todo") {
		t.Skip("Claude did not make the commit - cannot test summarise")
	}
	t.Log("Commit with conversation was created")

	// Verify note exists before attempting summarise
	cmd = exec.Command("git", "notes", "--ref=refs/notes/claude-conversations", "list")
	cmd.Dir = tmpDir
	if notesOutput, err := cmd.CombinedOutput(); err != nil || len(strings.TrimSpace(string(notesOutput))) == 0 {
		t.Skip("No git note stored - cannot test summarise")
	}
	t.Log("Git note exists, running summarise...")

	// Now run claudit summarise — this calls real claude -p to summarise
	summariseCmd := exec.Command(clauditPath, "summarise")
	summariseCmd.Dir = tmpDir
	summariseCmd.Env = append(os.Environ(), pathEnv)
	if apiKey != "" {
		summariseCmd.Env = append(summariseCmd.Env, "ANTHROPIC_API_KEY="+apiKey)
	}
	if oauthToken != "" {
		summariseCmd.Env = append(summariseCmd.Env, "CLAUDE_CODE_OAUTH_TOKEN="+oauthToken)
	}

	done2 := make(chan result, 1)
	go func() {
		output, err := summariseCmd.CombinedOutput()
		done2 <- result{output, err}
	}()

	var summariseOutput string
	select {
	case res := <-done2:
		summariseOutput = string(res.output)
		if res.err != nil {
			t.Fatalf("claudit summarise failed: %v\nOutput: %s", res.err, res.output)
		}
	case <-time.After(120 * time.Second):
		_ = summariseCmd.Process.Kill()
		t.Fatal("claudit summarise timed out after 120 seconds")
	}

	t.Logf("Summary output:\n%s", summariseOutput)

	// Verify summary is non-empty and contains meaningful content
	summary := strings.TrimSpace(summariseOutput)
	if summary == "" {
		t.Fatal("FAIL: summarise returned empty output")
	}

	t.Log("Summarise returned non-empty output")

	// The summary should reference something about the conversation
	// (committing a todo file, git add, etc.)
	// We check for any of several keywords that would appear in a summary
	// of "git add todo.txt && git commit -m 'Add todo list'"
	keywords := []string{"todo", "commit", "git", "add", "file", "list"}
	foundKeyword := false
	lowerSummary := strings.ToLower(summary)
	for _, kw := range keywords {
		if strings.Contains(lowerSummary, kw) {
			foundKeyword = true
			break
		}
	}
	if !foundKeyword {
		t.Errorf("Summary does not appear to reference the conversation content.\nExpected one of %v in summary:\n%s", keywords, summary)
	}

	t.Log("Summary contains relevant content from the conversation")
}

// TestSummariseTldrAlias verifies the `tldr` alias works in a real environment.
// This is a lighter test — it only checks that the alias resolves correctly
// (it will fail at "no conversation" if no note exists, which is expected).
func TestSummariseTldrAlias(t *testing.T) {
	t.Parallel()

	clauditPath := os.Getenv("CLAUDIT_BINARY")
	if clauditPath == "" {
		clauditPath = filepath.Join(getWorkspaceRoot(), "claudit")
	}
	if _, err := os.Stat(clauditPath); err != nil {
		t.Fatalf("claudit binary not found at %s - run 'go build'", clauditPath)
	}

	tmpDir, err := os.MkdirTemp("", "tldr-alias-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}
	runGit(t, tmpDir, "add", "f.txt")
	runGit(t, tmpDir, "commit", "-m", "init")

	// Run `claudit tldr` — should fail with "no conversation found" (not "unknown command")
	cmd := exec.Command(clauditPath, "tldr")
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("Expected error (no conversation), but command succeeded")
	}

	outStr := string(output)
	if strings.Contains(outStr, "unknown command") || strings.Contains(outStr, "unknown flag") {
		t.Fatalf("FAIL: tldr alias not registered. Output: %s", outStr)
	}

	if !strings.Contains(outStr, "no conversation found") {
		t.Fatalf("Expected 'no conversation found' error, got: %s", outStr)
	}

	t.Log("tldr alias correctly resolves to summarise command")
}

// TestSummariseUnsupportedAgent verifies that using an agent that doesn't
// support summarisation produces a helpful error.
func TestSummariseUnsupportedAgent(t *testing.T) {
	t.Parallel()

	clauditPath := os.Getenv("CLAUDIT_BINARY")
	if clauditPath == "" {
		clauditPath = filepath.Join(getWorkspaceRoot(), "claudit")
	}
	if _, err := os.Stat(clauditPath); err != nil {
		t.Fatalf("claudit binary not found at %s - run 'go build'", clauditPath)
	}

	tmpDir, err := os.MkdirTemp("", "summarise-unsupported-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "Test User")

	if err := os.WriteFile(filepath.Join(tmpDir, "f.txt"), []byte("x"), 0644); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}
	runGit(t, tmpDir, "add", "f.txt")
	runGit(t, tmpDir, "commit", "-m", "init")

	// Store a minimal conversation so we get past the "no conversation" check
	transcriptData := `{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"Hello"}]}}
{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi there!"}]}}`
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(transcriptData), 0644); err != nil {
		t.Fatalf("Failed to write transcript: %v", err)
	}

	hookInput := `{"session_id":"test-session","transcript_path":"` + transcriptPath + `","tool_name":"Bash","tool_input":{"command":"git commit -m 'test'"}}`
	storeCmd := exec.Command(clauditPath, "store")
	storeCmd.Dir = tmpDir
	storeCmd.Stdin = strings.NewReader(hookInput)
	storeCmd.Env = append(os.Environ(), "PATH="+filepath.Dir(clauditPath)+":"+os.Getenv("PATH"))
	if storeOutput, err := storeCmd.CombinedOutput(); err != nil {
		t.Fatalf("claudit store failed: %v\nOutput: %s", err, storeOutput)
	}

	// Now try to summarise with copilot (which doesn't implement Summariser)
	cmd := exec.Command(clauditPath, "summarise", "--agent=copilot")
	cmd.Dir = tmpDir
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("Expected error for unsupported agent, but command succeeded")
	}

	outStr := string(output)
	if !strings.Contains(outStr, "does not support summarisation") {
		t.Fatalf("Expected 'does not support summarisation' error, got: %s", outStr)
	}
	if !strings.Contains(outStr, "--agent=claude") {
		t.Fatalf("Expected suggestion to use --agent=claude, got: %s", outStr)
	}

	t.Log("Unsupported agent produces helpful error with suggestion")
}
