package integration_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestPrerequisites runs first (alphabetically) and prints a clear summary
// of which integration test prerequisites are available. This makes CI
// failures immediately obvious when secrets or CLIs are missing.
func TestPrerequisites(t *testing.T) {
	type prereq struct {
		agent   string
		envVars []string // at least one must be set
		binary  string
		skipEnv string
	}

	prereqs := []prereq{
		{
			agent:   "Claude",
			envVars: []string{"CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY"},
			binary:  "claude",
			skipEnv: "SKIP_CLAUDE_INTEGRATION",
		},
		{
			agent:   "Codex",
			envVars: []string{"OPENAI_API_KEY"},
			binary:  "codex",
			skipEnv: "SKIP_CODEX_INTEGRATION",
		},
		{
			agent:   "Gemini",
			envVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
			binary:  "gemini",
			skipEnv: "SKIP_GEMINI_INTEGRATION",
		},
		{
			agent:   "OpenCode",
			envVars: []string{"GEMINI_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"},
			binary:  "opencode",
			skipEnv: "SKIP_OPENCODE_INTEGRATION",
		},
	}

	var summary []string
	var missing []string

	for _, p := range prereqs {
		if os.Getenv(p.skipEnv) == "1" {
			summary = append(summary, fmt.Sprintf("  %-10s SKIPPED (%s=1)", p.agent, p.skipEnv))
			continue
		}

		hasKey := false
		for _, env := range p.envVars {
			if os.Getenv(env) != "" {
				hasKey = true
				break
			}
		}

		_, hasbin := exec.LookPath(p.binary)

		keyStatus := "OK"
		if !hasKey {
			keyStatus = "MISSING (" + strings.Join(p.envVars, " or ") + ")"
			missing = append(missing, fmt.Sprintf("%s: missing API key (%s)", p.agent, strings.Join(p.envVars, " or ")))
		}

		binStatus := "OK"
		if hasbin != nil {
			binStatus = fmt.Sprintf("MISSING (%s not in PATH)", p.binary)
			missing = append(missing, fmt.Sprintf("%s: %s not in PATH", p.agent, p.binary))
		}

		summary = append(summary, fmt.Sprintf("  %-10s key=%-8s binary=%s", p.agent, keyStatus, binStatus))
	}

	t.Logf("\n=== Integration Test Prerequisites ===\n%s\n", strings.Join(summary, "\n"))

	if len(missing) > 0 {
		t.Fatalf("Missing prerequisites — these integration tests will fail:\n  %s\n\nSet the required secrets/env vars or skip with SKIP_<AGENT>_INTEGRATION=1",
			strings.Join(missing, "\n  "))
	}
}
