package integration_test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOpenCodeResumeFlag validates that `opencode --session <id>` is a
// recognized CLI flag, confirming our ResumeCommand() returns a valid invocation.
// Requires: OpenCode CLI in PATH. No API key needed.
func TestOpenCodeResumeFlag(t *testing.T) {
	if os.Getenv("SKIP_OPENCODE_INTEGRATION") == "1" {
		t.Skip("SKIP_OPENCODE_INTEGRATION=1")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("OpenCode CLI not found in PATH")
	}

	cmd := exec.Command("opencode", "--help")
	output, _ := cmd.CombinedOutput()
	helpText := string(output)

	if !strings.Contains(helpText, "--session") {
		t.Fatalf("opencode --help does not list '--session' flag.\n"+
			"Our ResumeCommand() returns 'opencode --session <id>' which may be invalid.\n"+
			"Help output:\n%s", helpText)
	}
	t.Log("Confirmed: --session flag exists in opencode CLI")

	// Verify the shorthand -s also exists
	if !strings.Contains(helpText, "-s,") && !strings.Contains(helpText, "-s ") {
		t.Log("Note: -s shorthand not found in help (optional)")
	}

	// Verify 'run' subcommand exists (used in integration tests)
	if !strings.Contains(helpText, "run") {
		t.Error("opencode --help does not list 'run' subcommand")
	}
}

// TestOpenCodeSessionSubcommand validates that the session management
// subcommand exists, which we may use for session discovery.
// Requires: OpenCode CLI in PATH. No API key needed.
func TestOpenCodeSessionSubcommand(t *testing.T) {
	if os.Getenv("SKIP_OPENCODE_INTEGRATION") == "1" {
		t.Skip("SKIP_OPENCODE_INTEGRATION=1")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("OpenCode CLI not found in PATH")
	}

	cmd := exec.Command("opencode", "session", "--help")
	output, _ := cmd.CombinedOutput()
	helpText := string(output)

	if !strings.Contains(helpText, "list") {
		t.Errorf("opencode session --help does not list 'list' subcommand.\n"+
			"Help output:\n%s", helpText)
	}
	t.Log("Confirmed: 'opencode session list' subcommand exists")
}

// TestOpenCodeValidation performs deep validation of claudit's OpenCode
// integration by running OpenCode with a capture plugin that records the
// actual data OpenCode provides to plugin hooks.
//
// Validates:
//  1. Plugin hook API: tool.execute.before/after hooks are invoked
//  2. Hook input schema: sessionID, callID, tool are present in input
//  3. Hook output schema: args.command is accessible for shell tools
//  4. Session storage: sessions at data_dir/storage/session/<projectID>/
//  5. Message storage: messages at data_dir/storage/message/<sessionID>/
//  6. Project ID scheme: matches git rev-list --max-parents=0 --all
//  7. Data directory: XDG_DATA_HOME env var is respected
//
// Requires: OpenCode CLI, GEMINI_API_KEY or GOOGLE_GENERATIVE_AI_API_KEY.
// Opt out: SKIP_OPENCODE_INTEGRATION=1
func TestOpenCodeValidation(t *testing.T) {
	if os.Getenv("SKIP_OPENCODE_INTEGRATION") == "1" {
		t.Skip("SKIP_OPENCODE_INTEGRATION=1")
	}

	geminiAPIKey := os.Getenv("GEMINI_API_KEY")
	googleGenAIKey := os.Getenv("GOOGLE_GENERATIVE_AI_API_KEY")
	if geminiAPIKey == "" && googleGenAIKey == "" {
		t.Fatal("Neither GEMINI_API_KEY nor GOOGLE_GENERATIVE_AI_API_KEY set - " +
			"set one of these or use SKIP_OPENCODE_INTEGRATION=1")
	}
	apiKey := googleGenAIKey
	if apiKey == "" {
		apiKey = geminiAPIKey
	}

	if _, err := exec.LookPath("opencode"); err != nil {
		t.Fatal("OpenCode CLI not found in PATH - install it or use SKIP_OPENCODE_INTEGRATION=1")
	}

	clauditPath := os.Getenv("CLAUDIT_BINARY")
	if clauditPath == "" {
		clauditPath = filepath.Join(getWorkspaceRoot(), "claudit")
	}
	if _, err := os.Stat(clauditPath); err != nil {
		t.Fatalf("claudit binary not found at %s - run 'go build'", clauditPath)
	}

	// Create isolated test environment
	tmpDir, err := os.MkdirTemp("", "opencode-validation-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Use custom XDG_DATA_HOME for isolation (no pollution of real OpenCode data).
	// OpenCode respects XDG_DATA_HOME and stores data at $XDG_DATA_HOME/opencode.
	xdgDataHome := filepath.Join(tmpDir, "xdg-data")
	dataDir := filepath.Join(xdgDataHome, "opencode")

	// Capture file for hook data
	captureFile := filepath.Join(tmpDir, "hook-capture.jsonl")

	// Initialize git repo
	runGit(t, tmpDir, "init")
	runGit(t, tmpDir, "config", "user.email", "test@example.com")
	runGit(t, tmpDir, "config", "user.name", "Test User")

	os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Test Project\n"), 0644)
	runGit(t, tmpDir, "add", "README.md")
	runGit(t, tmpDir, "commit", "-m", "Initial commit")

	// Get the expected project ID (root commit hash)
	cmd := exec.Command("git", "rev-list", "--max-parents=0", "--all")
	cmd.Dir = tmpDir
	rootCommitOutput, err := cmd.Output()
	if err != nil {
		t.Fatalf("Failed to get root commit: %v", err)
	}
	expectedProjectID := strings.TrimSpace(string(rootCommitOutput))
	t.Logf("Expected project ID (root commit): %s", expectedProjectID[:12])

	// Write opencode.json
	opencodeConfig := map[string]interface{}{
		"$schema":    "https://opencode.ai/config.json",
		"model":      "google/gemini-2.5-flash",
		"permission": "allow",
	}
	configData, _ := json.MarshalIndent(opencodeConfig, "", "  ")
	os.WriteFile(filepath.Join(tmpDir, "opencode.json"), configData, 0644)

	// Initialize claudit (installs default plugin)
	initCmd := exec.Command(clauditPath, "init", "--agent=opencode")
	initCmd.Dir = tmpDir
	if output, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("claudit init failed: %v\n%s", err, output)
	}

	// Overwrite the default plugin with a capture version that also logs raw hook data
	pluginPath := filepath.Join(tmpDir, ".opencode", "plugins", "claudit.js")
	os.WriteFile(pluginPath, []byte(capturePluginJS), 0644)

	// Create a file for OpenCode to commit
	os.WriteFile(filepath.Join(tmpDir, "todo.txt"), []byte("- Buy milk\n- Walk dog\n"), 0644)

	// Run OpenCode with custom data dir and capture file
	opencodeCmd := exec.Command("opencode", "run",
		"Please run: git add todo.txt && git commit -m 'Add todo list'",
	)
	opencodeCmd.Dir = tmpDir
	opencodeCmd.Env = append(os.Environ(),
		"PATH="+filepath.Dir(clauditPath)+":"+os.Getenv("PATH"),
		"GOOGLE_GENERATIVE_AI_API_KEY="+apiKey,
		"XDG_DATA_HOME="+xdgDataHome,
		"CLAUDIT_HOOK_CAPTURE_FILE="+captureFile,
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

	var opencodeOutput []byte
	select {
	case res := <-done:
		opencodeOutput = res.output
		if res.err != nil {
			t.Logf("OpenCode finished with error (may be expected): %v", res.err)
		}
	case <-time.After(90 * time.Second):
		opencodeCmd.Process.Kill()
		t.Fatal("OpenCode timed out after 90 seconds")
	}

	time.Sleep(2 * time.Second)

	// Check if commit was made (prerequisite for all sub-tests)
	cmd = exec.Command("git", "log", "--oneline", "-n", "2")
	cmd.Dir = tmpDir
	logOutput, _ := cmd.CombinedOutput()
	if !strings.Contains(string(logOutput), "todo") {
		t.Logf("OpenCode output:\n%s", string(opencodeOutput))
		t.Skip("OpenCode did not make the commit - cannot validate integration")
	}
	t.Log("Commit was created successfully")

	// === Sub-tests validating each integration assumption ===

	t.Run("PluginHookAPI", func(t *testing.T) {
		// Core validation: did OpenCode actually call our plugin hooks?
		if _, err := os.Stat(captureFile); os.IsNotExist(err) {
			t.Fatal("Hook capture file was not created.\n" +
				"This means OpenCode did NOT call our plugin hooks.\n" +
				"Possible causes:\n" +
				"  - Plugin file format/export pattern is wrong\n" +
				"  - Hook names (tool.execute.before/after) are not recognized\n" +
				"  - Plugin directory (.opencode/plugins/) is not scanned")
		}

		f, err := os.Open(captureFile)
		if err != nil {
			t.Fatalf("Failed to open capture file: %v", err)
		}
		defer f.Close()

		var beforeEvents, afterEvents []map[string]interface{}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
		for scanner.Scan() {
			var entry map[string]interface{}
			if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
				continue
			}
			switch entry["event"] {
			case "before":
				beforeEvents = append(beforeEvents, entry)
			case "after":
				afterEvents = append(afterEvents, entry)
			}
		}

		if len(beforeEvents) == 0 {
			t.Fatal("No 'tool.execute.before' events captured.\n" +
				"OpenCode may use different hook names than 'tool.execute.before'.")
		}
		if len(afterEvents) == 0 {
			t.Fatal("No 'tool.execute.after' events captured.\n" +
				"OpenCode may use different hook names than 'tool.execute.after'.")
		}

		t.Logf("Captured %d before events and %d after events", len(beforeEvents), len(afterEvents))
	})

	t.Run("HookInputSchema", func(t *testing.T) {
		// Validate that input fields match what ParseHookInput expects
		events := readCaptureEvents(t, captureFile)
		if len(events.Before) == 0 {
			t.Skip("No before events to validate")
		}

		for i, evt := range events.Before {
			// input.sessionID — used to construct transcript path
			if evt.InputSessionID == "" {
				t.Errorf("before[%d]: input.sessionID is empty — "+
					"ParseHookInput needs this to find transcripts", i)
			}

			// input.callID — used to correlate before/after events
			if evt.InputCallID == "" {
				t.Errorf("before[%d]: input.callID is empty — "+
					"needed for before/after correlation", i)
			}

			// input.tool — used as tool_name in hook JSON
			if evt.InputTool == "" {
				t.Errorf("before[%d]: input.tool is empty — "+
					"IsCommitCommand needs the tool name", i)
			}
		}

		// Check that at least one event has args (shell tool)
		hasArgs := false
		for _, evt := range events.Before {
			if evt.HasArgs {
				hasArgs = true
				t.Logf("Confirmed: tool=%q has args, command=%q",
					evt.InputTool, evt.Command)
				break
			}
		}
		if !hasArgs {
			t.Error("No 'before' event had output.args — " +
				"we extract the command from output.args.command")
		}
	})

	t.Run("HookOutputSchema", func(t *testing.T) {
		// Validate that output.args.command contains the git commit command
		events := readCaptureEvents(t, captureFile)

		hasCommitCommand := false
		for _, evt := range events.Before {
			if strings.Contains(evt.Command, "git commit") ||
				strings.Contains(evt.Command, "git add") {
				hasCommitCommand = true
				t.Logf("Confirmed: captured git command in output.args.command: %q", evt.Command)
				break
			}
		}
		if !hasCommitCommand {
			t.Error("No captured event has 'git commit' in output.args.command — " +
				"our IsCommitCommand checks for this string")
		}

		// Verify before/after callID correlation works
		beforeCallIDs := make(map[string]bool)
		for _, evt := range events.Before {
			if evt.InputCallID != "" {
				beforeCallIDs[evt.InputCallID] = true
			}
		}
		matchedAfter := 0
		for _, evt := range events.After {
			if beforeCallIDs[evt.InputCallID] {
				matchedAfter++
			}
		}
		if matchedAfter == 0 && len(events.After) > 0 {
			t.Error("No 'after' event callIDs matched 'before' events — " +
				"our plugin correlates before/after by callID")
		} else {
			t.Logf("Confirmed: %d after events correlated with before events via callID", matchedAfter)
		}
	})

	t.Run("DataDirectory", func(t *testing.T) {
		// Validate XDG_DATA_HOME was respected
		if _, err := os.Stat(dataDir); os.IsNotExist(err) {
			t.Fatalf("Custom data dir at %s was not created.\n"+
				"OpenCode may not respect XDG_DATA_HOME.\n"+
				"Our GetDataDir() uses XDG_DATA_HOME/opencode.", dataDir)
		}

		// Check expected subdirectory structure
		for _, subdir := range []string{"storage", "storage/session", "storage/message"} {
			path := filepath.Join(dataDir, subdir)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Errorf("Expected directory %s not found in data dir.\n"+
					"Our code expects this structure for session/message storage.", subdir)
			}
		}

		t.Logf("Confirmed: XDG_DATA_HOME respected at %s", dataDir)
	})

	t.Run("ProjectIDScheme", func(t *testing.T) {
		// Validate that OpenCode uses root commit hash as project ID
		sessionDir := filepath.Join(dataDir, "storage", "session")
		entries, err := os.ReadDir(sessionDir)
		if err != nil {
			t.Fatalf("Failed to read session dir: %v", err)
		}

		projectSessionDir := filepath.Join(sessionDir, expectedProjectID)
		if _, err := os.Stat(projectSessionDir); os.IsNotExist(err) {
			var found []string
			for _, e := range entries {
				found = append(found, e.Name())
			}
			t.Fatalf("No session directory matching root commit hash.\n"+
				"Expected project ID: %s\n"+
				"Found entries: %v\n"+
				"Our GetProjectID() uses 'git rev-list --max-parents=0 --all'.\n"+
				"OpenCode may use a different project identifier.",
				expectedProjectID[:12], found)
		}

		t.Logf("Confirmed: session stored under root commit hash %s", expectedProjectID[:12])

		// Read session file to verify projectID field
		sessionEntries, err := os.ReadDir(projectSessionDir)
		if err != nil {
			t.Fatalf("Failed to read project session dir: %v", err)
		}

		for _, e := range sessionEntries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(projectSessionDir, e.Name()))
			if err != nil {
				continue
			}
			var session map[string]interface{}
			if err := json.Unmarshal(data, &session); err != nil {
				continue
			}
			if pid, ok := session["projectID"]; ok {
				pidStr, _ := pid.(string)
				if pidStr != expectedProjectID {
					t.Errorf("Session file projectID = %q, expected %q", pidStr, expectedProjectID)
				} else {
					t.Logf("Confirmed: session file projectID matches root commit")
				}
			}
			break
		}
	})

	t.Run("SessionStoragePaths", func(t *testing.T) {
		// Validate session file format matches what our ListSessions/ReadSession expects
		projectSessionDir := filepath.Join(dataDir, "storage", "session", expectedProjectID)
		entries, err := os.ReadDir(projectSessionDir)
		if err != nil {
			t.Skipf("Could not read session dir: %v", err)
		}

		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(projectSessionDir, e.Name()))
			if err != nil {
				continue
			}

			var session map[string]interface{}
			if err := json.Unmarshal(data, &session); err != nil {
				t.Errorf("Session file %s is not valid JSON: %v", e.Name(), err)
				continue
			}

			// Our SessionInfo struct expects these fields
			expectedFields := map[string]bool{
				"id": true,
			}
			for field := range expectedFields {
				if _, ok := session[field]; !ok {
					t.Errorf("Session file missing '%s' field (have: %v)",
						field, mapKeys(session))
				}
			}

			// Log all fields for documentation
			t.Logf("Session file %s fields: %v", e.Name(), mapKeys(session))

			// Verify session ID matches filename
			sessionID, _ := session["id"].(string)
			expectedName := sessionID + ".json"
			if e.Name() != expectedName {
				t.Logf("Note: session filename %q != id+.json %q",
					e.Name(), expectedName)
			}
			break
		}
	})

	t.Run("MessageStoragePaths", func(t *testing.T) {
		// Validate message storage matches what our ReadMessages/ParseTranscriptFile expects
		msgBaseDir := filepath.Join(dataDir, "storage", "message")
		entries, err := os.ReadDir(msgBaseDir)
		if err != nil {
			t.Fatalf("Failed to read message dir: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("No message session directories found")
		}

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sessionMsgDir := filepath.Join(msgBaseDir, e.Name())
			msgEntries, err := os.ReadDir(sessionMsgDir)
			if err != nil {
				continue
			}
			t.Logf("Session %s: %d message files", e.Name(), len(msgEntries))

			// List file types for documentation
			var fileTypes []string
			for _, me := range msgEntries {
				ext := filepath.Ext(me.Name())
				fileTypes = append(fileTypes, me.Name()+" ("+ext+")")
			}
			t.Logf("Message files: %v", fileTypes)

			// Read first JSON message to validate format
			for _, me := range msgEntries {
				if !strings.HasSuffix(me.Name(), ".json") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(sessionMsgDir, me.Name()))
				if err != nil {
					continue
				}
				var msg map[string]interface{}
				if err := json.Unmarshal(data, &msg); err != nil {
					t.Errorf("Message %s is not valid JSON: %v", me.Name(), err)
					continue
				}

				// Our parseOpenCodeEntry expects 'role' or 'type' and 'id'
				hasRole := msg["role"] != nil
				hasType := msg["type"] != nil
				hasID := msg["id"] != nil
				if !hasRole && !hasType {
					t.Errorf("Message %s has neither 'role' nor 'type' field (keys: %v)",
						me.Name(), mapKeys(msg))
				}
				if !hasID {
					t.Logf("Note: message %s has no 'id' field", me.Name())
				}
				t.Logf("Message format fields: %v", mapKeys(msg))
				break
			}
			break
		}
	})
}

// captureEvent represents a parsed hook capture event.
type captureEvent struct {
	Event          string
	InputSessionID string
	InputCallID    string
	InputTool      string
	InputKeys      []string
	OutputKeys     []string
	HasArgs        bool
	ArgsKeys       []string
	Command        string
}

// captureEvents holds parsed before/after events.
type captureEvents struct {
	Before []captureEvent
	After  []captureEvent
}

// readCaptureEvents reads and parses the hook capture file.
func readCaptureEvents(t *testing.T, captureFile string) captureEvents {
	t.Helper()

	f, err := os.Open(captureFile)
	if err != nil {
		t.Skipf("Could not open capture file: %v", err)
		return captureEvents{}
	}
	defer f.Close()

	var events captureEvents
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 256*1024)
	for scanner.Scan() {
		var raw map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}

		evt := captureEvent{
			Event:          stringField(raw, "event"),
			InputSessionID: stringField(raw, "input_sessionID"),
			InputCallID:    stringField(raw, "input_callID"),
			InputTool:      stringField(raw, "input_tool"),
			Command:        stringField(raw, "command"),
		}

		if v, ok := raw["has_args"]; ok {
			evt.HasArgs, _ = v.(bool)
		}
		if v, ok := raw["input_keys"]; ok {
			evt.InputKeys = toStringSlice(v)
		}
		if v, ok := raw["output_keys"]; ok {
			evt.OutputKeys = toStringSlice(v)
		}
		if v, ok := raw["args_keys"]; ok {
			evt.ArgsKeys = toStringSlice(v)
		}

		switch evt.Event {
		case "before":
			events.Before = append(events.Before, evt)
		case "after":
			events.After = append(events.After, evt)
		}
	}

	return events
}

func stringField(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

func toStringSlice(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	var result []string
	for _, item := range arr {
		s, _ := item.(string)
		result = append(result, s)
	}
	return result
}

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// capturePluginJS is a modified version of the claudit plugin that also
// captures the raw data OpenCode provides to plugin hooks for validation.
// It reads the capture file path from CLAUDIT_HOOK_CAPTURE_FILE env var.
const capturePluginJS = `// Capture plugin for claudit integration testing
// Logs raw hook data to validate OpenCode's plugin API
export const ClauditPlugin = async ({ directory }) => {
  const fs = await import("fs");
  const captureFile = process.env.CLAUDIT_HOOK_CAPTURE_FILE;
  const pendingCommits = new Map();

  const capture = (data) => {
    if (!captureFile) return;
    try { fs.appendFileSync(captureFile, JSON.stringify(data) + "\n"); } catch(e) {}
  };

  return {
    "tool.execute.before": async (input, output) => {
      capture({
        event: "before",
        input_keys: Object.keys(input || {}),
        input_sessionID: input?.sessionID || "",
        input_callID: input?.callID || "",
        input_tool: input?.tool || "",
        output_keys: Object.keys(output || {}),
        has_args: !!(output?.args),
        args_keys: output?.args ? Object.keys(output.args) : [],
        command: output?.args?.command || output?.args?.cmd || "",
      });

      const command = output?.args?.command || output?.args?.cmd || "";
      if (command.includes("git commit") || command.includes("git-commit")) {
        pendingCommits.set(input.callID, {
          command,
          tool: input.tool,
          sessionID: input.sessionID,
        });
      }
    },

    "tool.execute.after": async (input, output) => {
      capture({
        event: "after",
        input_keys: Object.keys(input || {}),
        input_sessionID: input?.sessionID || "",
        input_callID: input?.callID || "",
        input_tool: input?.tool || "",
        output_keys: Object.keys(output || {}),
      });

      const pending = pendingCommits.get(input.callID);
      if (!pending) return;
      pendingCommits.delete(input.callID);

      const dataDir = process.platform === "darwin"
          ? process.env.HOME + "/Library/Application Support/opencode"
          : (process.env.XDG_DATA_HOME || process.env.HOME + "/.local/share") + "/opencode";

      const hookData = JSON.stringify({
        session_id: pending.sessionID || "",
        data_dir: dataDir,
        project_dir: directory,
        tool_name: pending.tool || "",
        tool_input: { command: pending.command },
      });

      try {
        const { execSync } = await import("child_process");
        execSync("claudit store --agent=opencode", {
          input: hookData,
          cwd: directory,
          timeout: 30000,
          stdio: ["pipe", "pipe", "pipe"],
        });
      } catch (e) {}
    },
  };
};
`
