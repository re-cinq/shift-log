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

// TestOpenCodeIntegration runs an end-to-end test with actual OpenCode CLI.
// This test requires:
// - GEMINI_API_KEY or GOOGLE_GENERATIVE_AI_API_KEY environment variable set
// - OpenCode CLI installed and in PATH (`opencode`)
// - claudit binary built
//
// OpenCode uses @ai-sdk/google under the hood, which expects GOOGLE_GENERATIVE_AI_API_KEY.
// We set that env var directly when running OpenCode (mapping from GEMINI_API_KEY if needed).
//
// Opt out with: SKIP_OPENCODE_INTEGRATION=1 go test ./tests/integration/...
func TestOpenCodeIntegration(t *testing.T) {
	t.Parallel()
	if os.Getenv("SKIP_OPENCODE_INTEGRATION") == "1" {
		t.Skip("SKIP_OPENCODE_INTEGRATION=1 is set")
	}

	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	googleGenAIKey := os.Getenv("GOOGLE_GENERATIVE_AI_API_KEY")
	if geminiAPIKey == "" && googleGenAIKey == "" {
		t.Skip("Neither GEMINI_API_KEY nor GOOGLE_GENERATIVE_AI_API_KEY set")
	}

	// Determine which key to use (prefer GOOGLE_GENERATIVE_AI_API_KEY if both set)
	apiKey := googleGenAIKey
	if apiKey == "" {
		apiKey = geminiAPIKey
	}

	// Check OpenCode CLI is available
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("OpenCode CLI not found in PATH")
	}

	// Check claudit binary
	clauditPath := os.Getenv("CLAUDIT_BINARY")
	if clauditPath == "" {
		clauditPath = filepath.Join(getWorkspaceRoot(), "claudit")
	}
	if _, err := os.Stat(clauditPath); err != nil {
		t.Fatalf("claudit binary not found at %s - run 'go build' or use SKIP_OPENCODE_INTEGRATION=1", clauditPath)
	}

	// Create temporary test directory
	tmpDir, err := os.MkdirTemp("", "opencode-integration-*")
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

	// Write opencode.json to configure model and permissions statically.
	// API key is passed via GOOGLE_GENERATIVE_AI_API_KEY env var (not config)
	// because opencode.json {env:} substitution is unreliable.
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

	// Initialize claudit with OpenCode agent
	cmd := exec.Command(clauditPath, "init", "--agent=opencode")
	cmd.Dir = tmpDir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("claudit init --agent=opencode failed: %v\nOutput: %s", err, output)
	}

	// Verify plugin is installed
	pluginPath := filepath.Join(tmpDir, ".opencode", "plugins", "claudit.js")
	if _, err := os.Stat(pluginPath); err != nil {
		t.Fatalf("claudit plugin not installed at %s", pluginPath)
	}

	// No plugin modification needed — claudit init installs the correct plugin

	t.Log("Plugin configuration verified successfully")

	// Create a test file that OpenCode will commit
	todoFile := filepath.Join(tmpDir, "todo.txt")
	if err := os.WriteFile(todoFile, []byte("- Buy milk\n- Walk dog\n"), 0644); err != nil {
		t.Fatalf("Failed to write todo file: %v", err)
	}

	// Run OpenCode CLI with a simple prompt to commit the file.
	// Permissions are set to "allow" in opencode.json (no CLI flag needed).
	opencodeCmd := exec.Command("opencode", "run",
		"Please run: git add todo.txt && git commit -m 'Add todo list'",
	)
	opencodeCmd.Dir = tmpDir
	opencodeCmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(clauditPath)+":"+os.Getenv("PATH"),
		"GOOGLE_GENERATIVE_AI_API_KEY="+apiKey,
	)

	// Set timeout and capture output
	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, err := opencodeCmd.CombinedOutput()
		done <- result{output, err}
	}()

	var opencodeOutput []byte
	select {
	case res := <-done:
		opencodeOutput = res.output
		if res.err != nil {
			t.Logf("OpenCode command finished with error (may be expected): %v", res.err)
		}
	case <-time.After(90 * time.Second):
		opencodeCmd.Process.Kill()
		t.Fatal("OpenCode command timed out after 90 seconds")
	}

	// Give hooks time to run
	time.Sleep(2 * time.Second)

	// Check for claudit warnings
	if strings.Contains(string(opencodeOutput), "claudit: warning:") {
		t.Fatalf("FAIL: claudit logged warnings during execution:\n%s", string(opencodeOutput))
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
		t.Logf("OpenCode output:\n%s", string(opencodeOutput))
		t.Skip("OpenCode did not make the commit - cannot test note storage")
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

	// Verify agent field is "opencode"
	if noteData["agent"] != "opencode" {
		t.Errorf("Expected agent='opencode', got %v", noteData["agent"])
	}

	t.Log("Note content is valid and contains all required fields")
	t.Logf("Note preview: version=%v, session_id=%v, agent=%v, message_count=%v",
		noteData["version"], noteData["session_id"], noteData["agent"], noteData["message_count"])
}
