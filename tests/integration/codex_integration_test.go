package integration_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCodexCLIIntegration runs an end-to-end test with actual Codex CLI.
// This test requires:
// - OPENAI_API_KEY environment variable set
// - Codex CLI installed and in PATH (`codex`)
// - claudit binary built
//
// Opt out with: SKIP_CODEX_INTEGRATION=1 go test ./tests/integration/...
func TestCodexCLIIntegration(t *testing.T) {
	if os.Getenv("SKIP_CODEX_INTEGRATION") == "1" {
		t.Skip("SKIP_CODEX_INTEGRATION=1 is set")
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY not set - set it or use SKIP_CODEX_INTEGRATION=1")
	}

	// Check Codex CLI is available
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatal("Codex CLI not found in PATH - install it or use SKIP_CODEX_INTEGRATION=1")
	}

	// Check claudit binary
	clauditPath := os.Getenv("CLAUDIT_BINARY")
	if clauditPath == "" {
		clauditPath = filepath.Join(getWorkspaceRoot(), "claudit")
	}
	if _, err := os.Stat(clauditPath); err != nil {
		t.Fatalf("claudit binary not found at %s - run 'go build' or use SKIP_CODEX_INTEGRATION=1", clauditPath)
	}

	// Create temporary test directory
	tmpDir, err := os.MkdirTemp("", "codex-integration-*")
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

	// Initialize claudit with Codex agent
	cmd := exec.Command(clauditPath, "init", "--agent=codex")
	cmd.Dir = tmpDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claudit init --agent=codex failed: %v\nOutput: %s", err, output)
	}

	// Verify no agent-specific config files were created (hookless agent)
	for _, path := range []string{".codex/settings.json", ".claude/settings.local.json"} {
		if _, err := os.Stat(filepath.Join(tmpDir, path)); err == nil {
			t.Fatalf("Unexpected config file created: %s", path)
		}
	}

	// Verify git hooks were installed
	hookPath := filepath.Join(tmpDir, ".git", "hooks", "post-commit")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatal("post-commit hook was not installed")
	}

	t.Log("Hookless init verified — git hooks installed, no agent config files")

	// Create a test file that Codex will commit
	todoFile := filepath.Join(tmpDir, "todo.txt")
	if err := os.WriteFile(todoFile, []byte("- Buy milk\n- Walk dog\n"), 0644); err != nil {
		t.Fatalf("Failed to write todo file: %v", err)
	}

	// Run Codex CLI with a simple prompt to commit the file
	codexCmd := exec.Command("codex",
		"--approval-mode", "full-auto",
		"-q", "Please run: git add todo.txt && git commit -m 'Add todo list'",
	)
	codexCmd.Dir = tmpDir
	codexCmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(clauditPath)+":"+os.Getenv("PATH"),
		"OPENAI_API_KEY="+apiKey,
	)

	// Set timeout and capture output
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := codexCmd.CombinedOutput()
		done <- result{output, err}
	}()

	var codexOutput []byte
	select {
	case res := <-done:
		codexOutput = res.output
		if res.err != nil {
			t.Logf("Codex command finished with error (may be expected): %v", res.err)
		}
	case <-time.After(90 * time.Second):
		codexCmd.Process.Kill()
		t.Fatal("Codex command timed out after 90 seconds")
	}

	// Give hooks time to run
	time.Sleep(2 * time.Second)

	// Check for claudit warnings
	if strings.Contains(string(codexOutput), "claudit: warning:") {
		t.Fatalf("FAIL: claudit logged warnings during execution:\n%s", string(codexOutput))
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
		t.Skip("Codex did not make the commit - cannot test note storage")
	}

	t.Log("Commit was created successfully")
	t.Logf("Git log: %s", output)

	// CRITICAL TEST: If commit was made, note MUST exist
	cmd = exec.Command("git", "notes", "--ref=refs/notes/claude-conversations", "list")
	cmd.Dir = tmpDir
	notesOutput, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("FAIL: Commit was made but no git notes exist!\nError: %v\nOutput: %s", err, notesOutput)
	}

	if len(strings.TrimSpace(string(notesOutput))) == 0 {
		t.Fatal("FAIL: Commit was made but git notes list is empty!")
	}

	t.Log("Git note was created by claudit hook")

	// Verify note content
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
			t.Fatalf("FAIL: Note missing required field '%s'", field)
		}
	}

	// Verify agent field is "codex"
	if noteData["agent"] != "codex" {
		t.Errorf("Expected agent='codex', got %v", noteData["agent"])
	}

	t.Log("Note content is valid and contains all required fields")
	t.Logf("Note preview: version=%v, session_id=%v, agent=%v, message_count=%v",
		noteData["version"], noteData["session_id"], noteData["agent"], noteData["message_count"])
}
