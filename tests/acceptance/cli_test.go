package acceptance_test

import (
	"encoding/json"
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/re-cinq/claudit/tests/acceptance/testutil"
)

var _ = Describe("CLI Foundation", func() {
	Describe("claudit with no arguments", func() {
		It("displays help text", func() {
			stdout, _, err := testutil.RunClaudit()
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("Usage:"))
			Expect(stdout).To(ContainSubstring("Commands for humans:"))
		})
	})

	Describe("claudit --version", func() {
		It("displays the version number", func() {
			stdout, _, err := testutil.RunClaudit("--version")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("claudit version"))
		})
	})

	Describe("claudit --help", func() {
		It("displays help text", func() {
			stdout, _, err := testutil.RunClaudit("--help")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("Usage:"))
			Expect(stdout).To(ContainSubstring("init"))
			Expect(stdout).To(ContainSubstring("store"))
			Expect(stdout).To(ContainSubstring("sync"))
		})
	})
})

var _ = Describe("Init Command", func() {
	var repo *testutil.GitRepo

	BeforeEach(func() {
		var err error
		repo, err = testutil.NewGitRepo()
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if repo != nil {
			repo.Cleanup()
		}
	})

	Describe("claudit init in a git repository", func() {
		It("creates .claude/settings.local.json", func() {
			stdout, _, err := testutil.RunClauditInDir(repo.Path, "init")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("Configured Claude hooks"))

			Expect(repo.FileExists(".claude/settings.local.json")).To(BeTrue())
		})

		It("configures PostToolUse hook correctly", func() {
			_, _, err := testutil.RunClauditInDir(repo.Path, "init")
			Expect(err).NotTo(HaveOccurred())

			content, err := repo.ReadFile(".claude/settings.local.json")
			Expect(err).NotTo(HaveOccurred())
			// Verify correct Claude Code hook format: nested under "hooks.PostToolUse"
			Expect(content).To(ContainSubstring(`"hooks"`))
			Expect(content).To(ContainSubstring(`"PostToolUse"`))
			Expect(content).To(ContainSubstring(`"matcher": "Bash"`))
			Expect(content).To(ContainSubstring("claudit store"))
		})

		It("produces valid Claude Code hook JSON structure", func() {
			_, _, err := testutil.RunClauditInDir(repo.Path, "init")
			Expect(err).NotTo(HaveOccurred())

			content, err := repo.ReadFile(".claude/settings.local.json")
			Expect(err).NotTo(HaveOccurred())

			// Parse and validate the exact structure expected by Claude Code
			var settings struct {
				Hooks struct {
					PostToolUse []struct {
						Matcher string `json:"matcher"`
						Hooks   []struct {
							Type    string `json:"type"`
							Command string `json:"command"`
							Timeout int    `json:"timeout"`
						} `json:"hooks"`
					} `json:"PostToolUse"`
				} `json:"hooks"`
			}
			Expect(json.Unmarshal([]byte(content), &settings)).To(Succeed())

			// Verify structure matches Claude Code's expected format
			Expect(settings.Hooks.PostToolUse).To(HaveLen(1))
			hook := settings.Hooks.PostToolUse[0]
			Expect(hook.Matcher).To(Equal("Bash"))
			Expect(hook.Hooks).To(HaveLen(1))
			Expect(hook.Hooks[0].Type).To(Equal("command"))
			Expect(hook.Hooks[0].Command).To(Equal("claudit store"))
			Expect(hook.Hooks[0].Timeout).To(Equal(30))
		})

		It("installs git hooks", func() {
			stdout, _, err := testutil.RunClauditInDir(repo.Path, "init")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("Installed git hooks"))

			// Check that hooks exist and are executable
			for _, hook := range []string{"pre-push", "post-merge", "post-checkout"} {
				hookPath := repo.Path + "/.git/hooks/" + hook
				info, err := os.Stat(hookPath)
				Expect(err).NotTo(HaveOccurred())
				Expect(info.Mode() & 0111).NotTo(BeZero()) // executable
			}
		})

		It("preserves existing settings", func() {
			// Create existing settings
			Expect(os.MkdirAll(repo.Path+"/.claude", 0755)).To(Succeed())
			Expect(repo.WriteFile(".claude/settings.local.json", `{"existingKey": "existingValue"}`)).To(Succeed())

			_, _, err := testutil.RunClauditInDir(repo.Path, "init")
			Expect(err).NotTo(HaveOccurred())

			content, err := repo.ReadFile(".claude/settings.local.json")
			Expect(err).NotTo(HaveOccurred())
			Expect(content).To(ContainSubstring("existingKey"))
			Expect(content).To(ContainSubstring("existingValue"))
			Expect(content).To(ContainSubstring("claudit store"))
		})
	})

	Describe("claudit init outside a git repository", func() {
		It("fails with error", func() {
			tmpDir, err := os.MkdirTemp("", "not-a-repo-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(tmpDir)

			_, stderr, err := testutil.RunClauditInDir(tmpDir, "init")
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("not inside a git repository"))
		})
	})
})
