package acceptance_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/re-cinq/claudit/tests/acceptance/testutil"
)

var _ = Describe("Gemini Init", func() {
	var repo *testutil.GitRepo

	BeforeEach(func() {
		var err error
		repo, err = testutil.NewGitRepo()
		Expect(err).NotTo(HaveOccurred())

		// CRITICAL: Set binary path so git hooks can find claudit
		repo.SetBinaryPath(testutil.BinaryPath())
	})

	AfterEach(func() {
		repo.Cleanup()
	})

	It("creates .gemini/settings.json with correct hooks", func() {
		_, _, err := testutil.RunClauditInDir(repo.Path, "init", "--agent=gemini")
		Expect(err).NotTo(HaveOccurred())

		settingsPath := filepath.Join(repo.Path, ".gemini", "settings.json")
		Expect(settingsPath).To(BeAnExistingFile())

		data, err := os.ReadFile(settingsPath)
		Expect(err).NotTo(HaveOccurred())

		var raw map[string]interface{}
		Expect(json.Unmarshal(data, &raw)).To(Succeed())

		hooks, ok := raw["hooks"].(map[string]interface{})
		Expect(ok).To(BeTrue(), "Expected hooks key in settings")

		afterTool, ok := hooks["AfterTool"].([]interface{})
		Expect(ok).To(BeTrue(), "Expected AfterTool array")
		Expect(afterTool).NotTo(BeEmpty())

		// Verify matcher is run_shell_command
		hookObj := afterTool[0].(map[string]interface{})
		Expect(hookObj["matcher"]).To(Equal("run_shell_command"))

		// Verify hook command
		hookCmds := hookObj["hooks"].([]interface{})
		hookCmd := hookCmds[0].(map[string]interface{})
		Expect(hookCmd["command"]).To(Equal("claudit store --agent=gemini"))

		// Verify timeout is 30000 (milliseconds)
		Expect(hookCmd["timeout"]).To(BeNumerically("==", 30000))
	})

	It("is idempotent - running init twice does not duplicate hooks", func() {
		// Run init twice
		_, _, err := testutil.RunClauditInDir(repo.Path, "init", "--agent=gemini")
		Expect(err).NotTo(HaveOccurred())
		_, _, err = testutil.RunClauditInDir(repo.Path, "init", "--agent=gemini")
		Expect(err).NotTo(HaveOccurred())

		settingsPath := filepath.Join(repo.Path, ".gemini", "settings.json")
		data, err := os.ReadFile(settingsPath)
		Expect(err).NotTo(HaveOccurred())

		var raw map[string]interface{}
		Expect(json.Unmarshal(data, &raw)).To(Succeed())

		hooks := raw["hooks"].(map[string]interface{})
		afterTool := hooks["AfterTool"].([]interface{})

		// Should have exactly one AfterTool hook, not two
		Expect(afterTool).To(HaveLen(1))
	})

	It("preserves existing settings", func() {
		// Create pre-existing settings
		geminiDir := filepath.Join(repo.Path, ".gemini")
		Expect(os.MkdirAll(geminiDir, 0755)).To(Succeed())

		existingSettings := map[string]interface{}{
			"theme":    "dark",
			"maxTurns": 10,
		}
		data, _ := json.MarshalIndent(existingSettings, "", "  ")
		Expect(os.WriteFile(filepath.Join(geminiDir, "settings.json"), data, 0644)).To(Succeed())

		// Run init
		_, _, err := testutil.RunClauditInDir(repo.Path, "init", "--agent=gemini")
		Expect(err).NotTo(HaveOccurred())

		// Read updated settings
		updatedData, err := os.ReadFile(filepath.Join(geminiDir, "settings.json"))
		Expect(err).NotTo(HaveOccurred())

		var raw map[string]interface{}
		Expect(json.Unmarshal(updatedData, &raw)).To(Succeed())

		// Original settings should still be present
		Expect(raw["theme"]).To(Equal("dark"))
		Expect(raw["maxTurns"]).To(BeNumerically("==", 10))

		// Hooks should also be present
		hooks, ok := raw["hooks"].(map[string]interface{})
		Expect(ok).To(BeTrue())
		Expect(hooks["AfterTool"]).NotTo(BeNil())
	})

	It("creates session hooks with correct timeouts", func() {
		_, _, err := testutil.RunClauditInDir(repo.Path, "init", "--agent=gemini")
		Expect(err).NotTo(HaveOccurred())

		settingsPath := filepath.Join(repo.Path, ".gemini", "settings.json")
		data, err := os.ReadFile(settingsPath)
		Expect(err).NotTo(HaveOccurred())

		var raw map[string]interface{}
		Expect(json.Unmarshal(data, &raw)).To(Succeed())

		hooks := raw["hooks"].(map[string]interface{})

		// Check SessionStart hook
		sessionStart := hooks["SessionStart"].([]interface{})
		Expect(sessionStart).NotTo(BeEmpty())
		startHook := sessionStart[0].(map[string]interface{})
		startCmds := startHook["hooks"].([]interface{})
		startCmd := startCmds[0].(map[string]interface{})
		Expect(startCmd["timeout"]).To(BeNumerically("==", 5000))

		// Check SessionEnd hook
		sessionEnd := hooks["SessionEnd"].([]interface{})
		Expect(sessionEnd).NotTo(BeEmpty())
		endHook := sessionEnd[0].(map[string]interface{})
		endCmds := endHook["hooks"].([]interface{})
		endCmd := endCmds[0].(map[string]interface{})
		Expect(endCmd["timeout"]).To(BeNumerically("==", 5000))
	})
})
