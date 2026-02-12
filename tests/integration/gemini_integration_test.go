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

// TestGeminiCLIIntegration runs an end-to-end test with actual Gemini CLI.
// This test requires:
// - GEMINI_API_KEY or GOOGLE_API_KEY environment variable set
// - Gemini CLI installed and in PATH (`gemini`)
// - claudit binary built
//
// Opt out with: SKIP_GEMINI_INTEGRATION=1 go test ./tests/integration/...
func TestGeminiCLIIntegration(t *testing.T) {
	t.Parallel()
	if os.Getenv("SKIP_GEMINI_INTEGRATION") == "1" {
		t.Skip("SKIP_GEMINI_INTEGRATION=1 is set")
	}

	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	googleAPIKey := os.Getenv("GOOGLE_API_KEY")
	if geminiAPIKey == "" && googleAPIKey == "" {
		t.Skip("Neither GEMINI_API_KEY nor GOOGLE_API_KEY set")
	}

	// Check Gemini CLI is available
	if _, err := exec.LookPath("gemini"); err != nil {
		t.Skip("Gemini CLI not found in PATH")
	}

	// Check claudit binary
	clauditPath := os.Getenv("CLAUDIT_BINARY")
	if clauditPath == "" {
		clauditPath = filepath.Join(getWorkspaceRoot(), "claudit")
	}
	if _, err := os.Stat(clauditPath); err != nil {
		t.Fatalf("claudit binary not found at %s - run 'go build' or use SKIP_GEMINI_INTEGRATION=1", clauditPath)
	}

	// Create temporary test directory
	tmpDir, err := os.MkdirTemp("", "gemini-integration-*")
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

	// Initialize claudit with Gemini agent
	cmd := exec.Command(clauditPath, "init", "--agent=gemini")
	cmd.Dir = tmpDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claudit init --agent=gemini failed: %v\nOutput: %s", err, output)
	}

	// Verify hooks are configured
	settingsPath := filepath.Join(tmpDir, ".gemini", "settings.json")
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("Failed to read settings: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(settingsData, &settings); err != nil {
		t.Fatalf("Failed to parse settings: %v", err)
	}

	hooks, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected hooks object in settings, got: %v", settings)
	}
	afterTool, ok := hooks["AfterTool"].([]interface{})
	if !ok {
		t.Fatalf("Expected AfterTool array in hooks, got: %v", hooks)
	}
	if len(afterTool) == 0 {
		t.Fatal("AfterTool hooks array is empty")
	}

	t.Log("Hook configuration verified successfully")

	// Create a test file that Gemini will commit
	todoFile := filepath.Join(tmpDir, "todo.txt")
	if err := os.WriteFile(todoFile, []byte("- Buy milk\n- Walk dog\n"), 0644); err != nil {
		t.Fatalf("Failed to write todo file: %v", err)
	}

	// Run Gemini CLI with a simple prompt to commit the file
	geminiCmd := exec.Command("gemini",
		"-p", "Please run: git add todo.txt && git commit -m 'Add todo list'",
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

	// Set timeout and capture output
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := geminiCmd.CombinedOutput()
		done <- result{output, err}
	}()

	var geminiOutput []byte
	select {
	case res := <-done:
		geminiOutput = res.output
		if res.err != nil {
			t.Logf("Gemini command finished with error (may be expected): %v", res.err)
		}
	case <-time.After(90 * time.Second):
		geminiCmd.Process.Kill()
		t.Fatal("Gemini command timed out after 90 seconds")
	}

	// Give hooks time to run
	time.Sleep(2 * time.Second)

	// Check for claudit warnings
	if strings.Contains(string(geminiOutput), "claudit: warning:") {
		t.Fatalf("FAIL: claudit logged warnings during execution:\n%s", string(geminiOutput))
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
		t.Skip("Gemini did not make the commit - cannot test note storage")
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

	// Verify agent field is "gemini"
	if noteData["agent"] != "gemini" {
		t.Errorf("Expected agent='gemini', got %v", noteData["agent"])
	}

	t.Log("Note content is valid and contains all required fields")
	t.Logf("Note preview: version=%v, session_id=%v, agent=%v, message_count=%v",
		noteData["version"], noteData["session_id"], noteData["agent"], noteData["message_count"])
}
