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

// TestCopilotCLIIntegration runs an end-to-end test with actual GitHub Copilot CLI.
// This test requires:
// - COPILOT_GITHUB_TOKEN environment variable set (for Copilot CLI authentication)
// - Copilot CLI installed and in PATH (`copilot`)
// - claudit binary built
//
// Opt out with: SKIP_COPILOT_INTEGRATION=1 go test ./tests/integration/...
func TestCopilotCLIIntegration(t *testing.T) {
	t.Parallel()
	if os.Getenv("SKIP_COPILOT_INTEGRATION") == "1" {
		t.Skip("SKIP_COPILOT_INTEGRATION=1 is set")
	}

	githubToken := os.Getenv("COPILOT_GITHUB_TOKEN")
	if githubToken == "" {
		t.Skip("COPILOT_GITHUB_TOKEN not set")
	}

	// Check Copilot CLI is available
	if _, err := exec.LookPath("copilot"); err != nil {
		t.Skip("Copilot CLI not found in PATH")
	}

	// Check claudit binary
	clauditPath := os.Getenv("CLAUDIT_BINARY")
	if clauditPath == "" {
		clauditPath = filepath.Join(getWorkspaceRoot(), "claudit")
	}
	if _, err := os.Stat(clauditPath); err != nil {
		t.Fatalf("claudit binary not found at %s - run 'go build' or use SKIP_COPILOT_INTEGRATION=1", clauditPath)
	}

	// Create temporary test directory
	tmpDir, err := os.MkdirTemp("", "copilot-integration-*")
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

	// Initialize claudit with Copilot agent
	cmd := exec.Command(clauditPath, "init", "--agent=copilot")
	cmd.Dir = tmpDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claudit init --agent=copilot failed: %v\nOutput: %s", err, output)
	}

	// Verify hooks are configured at .github/hooks/claudit.json
	hooksPath := filepath.Join(tmpDir, ".github", "hooks", "claudit.json")
	hooksData, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("Failed to read hooks file: %v", err)
	}

	var hooksFile map[string]interface{}
	if err := json.Unmarshal(hooksData, &hooksFile); err != nil {
		t.Fatalf("Failed to parse hooks file: %v", err)
	}

	hooks, ok := hooksFile["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected hooks object in claudit.json, got: %v", hooksFile)
	}
	postToolUse, ok := hooks["postToolUse"].([]interface{})
	if !ok {
		t.Fatalf("Expected postToolUse array in hooks, got: %v", hooks)
	}
	if len(postToolUse) == 0 {
		t.Fatal("postToolUse hooks array is empty")
	}

	// Verify hook entry uses "command" type (not "bash")
	hookEntry, ok := postToolUse[0].(map[string]interface{})
	if !ok {
		t.Fatal("Expected hook entry to be an object")
	}
	if hookEntry["type"] != "command" {
		t.Fatalf("Expected hook type 'command', got %v", hookEntry["type"])
	}
	if !strings.Contains(hookEntry["command"].(string), "claudit store") {
		t.Fatalf("Expected hook command to contain 'claudit store', got %v", hookEntry["command"])
	}

	t.Log("Hook configuration verified successfully")

	// Create a test file that Copilot will commit
	todoFile := filepath.Join(tmpDir, "todo.txt")
	if err := os.WriteFile(todoFile, []byte("- Buy milk\n- Walk dog\n"), 0644); err != nil {
		t.Fatalf("Failed to write todo file: %v", err)
	}

	// Run Copilot CLI with a simple prompt to commit the file
	// --yolo enables all permissions (tools, paths, URLs) without confirmation
	copilotCmd := exec.Command("copilot",
		"-p", "Run this exact command: git add todo.txt && git commit -m 'Add todo list'",
		"--yolo",
	)
	copilotCmd.Dir = tmpDir
	copilotCmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(clauditPath)+":"+os.Getenv("PATH"),
		"COPILOT_GITHUB_TOKEN="+githubToken,
	)

	// Set timeout and capture output
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := copilotCmd.CombinedOutput()
		done <- result{output, err}
	}()

	var copilotOutput []byte
	select {
	case res := <-done:
		copilotOutput = res.output
		if res.err != nil {
			t.Logf("Copilot command finished with error (may be expected): %v", res.err)
		}
	case <-time.After(90 * time.Second):
		copilotCmd.Process.Kill()
		t.Fatal("Copilot command timed out after 90 seconds")
	}

	// Give hooks time to run
	time.Sleep(2 * time.Second)

	// Check for claudit warnings
	if strings.Contains(string(copilotOutput), "claudit: warning:") {
		t.Fatalf("FAIL: claudit logged warnings during execution:\n%s", string(copilotOutput))
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
		t.Skip("Copilot did not make the commit - cannot test note storage")
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

	// Verify agent field is "copilot"
	if noteData["agent"] != "copilot" {
		t.Errorf("Expected agent='copilot', got %v", noteData["agent"])
	}

	t.Log("Note content is valid and contains all required fields")
	t.Logf("Note preview: version=%v, session_id=%v, agent=%v, message_count=%v",
		noteData["version"], noteData["session_id"], noteData["agent"], noteData["message_count"])
}
