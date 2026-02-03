package acceptance_test

import (
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/DanielJonesEB/claudit/tests/acceptance/testutil"
)

var _ = Describe("Notes Ref Selection", func() {
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

	Describe("with --notes-ref flag", func() {
		It("uses default ref when specified", func() {
			stdout, _, err := testutil.RunClauditInDir(repo.Path, "init", "--notes-ref=refs/notes/commits")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("Configured notes ref: refs/notes/commits"))

			// Verify config was created
			config, err := repo.ReadFile(".claudit/config")
			Expect(err).NotTo(HaveOccurred())
			Expect(config).To(ContainSubstring(`"notes_ref": "refs/notes/commits"`))
		})

		It("uses custom ref when specified", func() {
			stdout, _, err := testutil.RunClauditInDir(repo.Path, "init", "--notes-ref=refs/notes/claude-conversations")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("Configured notes ref: refs/notes/claude-conversations"))

			// Verify config was created
			config, err := repo.ReadFile(".claudit/config")
			Expect(err).NotTo(HaveOccurred())
			Expect(config).To(ContainSubstring(`"notes_ref": "refs/notes/claude-conversations"`))
		})

		It("rejects invalid ref", func() {
			_, stderr, err := testutil.RunClauditInDir(repo.Path, "init", "--notes-ref=refs/notes/invalid")
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("invalid notes ref"))
		})
	})

	Describe("git configuration", func() {
		It("configures notes.displayRef for chosen ref", func() {
			_, _, err := testutil.RunClauditInDir(repo.Path, "init", "--notes-ref=refs/notes/commits")
			Expect(err).NotTo(HaveOccurred())

			// Check git config
			cmd := exec.Command("git", "config", "notes.displayRef")
			cmd.Dir = repo.Path
			output, err := cmd.Output()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("refs/notes/commits"))
		})

		It("configures notes.rewriteRef for chosen ref", func() {
			_, _, err := testutil.RunClauditInDir(repo.Path, "init", "--notes-ref=refs/notes/commits")
			Expect(err).NotTo(HaveOccurred())

			// Check git config
			cmd := exec.Command("git", "config", "notes.rewriteRef")
			cmd.Dir = repo.Path
			output, err := cmd.Output()
			Expect(err).NotTo(HaveOccurred())
			Expect(string(output)).To(ContainSubstring("refs/notes/commits"))
		})
	})

	Describe("config persistence", func() {
		It("reuses existing config on subsequent init", func() {
			// First init with custom ref
			_, _, err := testutil.RunClauditInDir(repo.Path, "init", "--notes-ref=refs/notes/claude-conversations")
			Expect(err).NotTo(HaveOccurred())

			// Second init should reuse config (no flag needed)
			stdout, _, err := testutil.RunClauditInDir(repo.Path, "init")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("refs/notes/claude-conversations"))

			// Config should still have custom ref
			config, err := repo.ReadFile(".claudit/config")
			Expect(err).NotTo(HaveOccurred())
			Expect(config).To(ContainSubstring(`"notes_ref": "refs/notes/claude-conversations"`))
		})
	})

	Describe("note storage with dynamic ref", func() {
		It("stores notes on configured ref", func() {
			// Init with default ref
			_, _, err := testutil.RunClauditInDir(repo.Path, "init", "--notes-ref=refs/notes/commits")
			Expect(err).NotTo(HaveOccurred())

			// Create a commit
			Expect(repo.WriteFile("test.txt", "content")).To(Succeed())
			Expect(repo.Commit("Test commit")).To(Succeed())

			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			// Simulate store command
			transcriptPath := repo.Path + "/transcript.jsonl"
			Expect(repo.WriteFile("transcript.jsonl", testutil.SampleTranscript())).To(Succeed())

			hookInput := testutil.SampleHookInput("test-session", transcriptPath, "git commit -m 'test'")
			_, _, err = testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store")
			Expect(err).NotTo(HaveOccurred())

			// Verify note was created on default ref
			Expect(repo.HasNote("refs/notes/commits", head)).To(BeTrue())
			// Should NOT be on custom ref
			Expect(repo.HasNote("refs/notes/claude-conversations", head)).To(BeFalse())
		})

		It("stores notes on custom ref when configured", func() {
			// Init with custom ref
			_, _, err := testutil.RunClauditInDir(repo.Path, "init", "--notes-ref=refs/notes/claude-conversations")
			Expect(err).NotTo(HaveOccurred())

			// Create a commit
			Expect(repo.WriteFile("test.txt", "content")).To(Succeed())
			Expect(repo.Commit("Test commit")).To(Succeed())

			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			// Simulate store command
			transcriptPath := repo.Path + "/transcript.jsonl"
			Expect(repo.WriteFile("transcript.jsonl", testutil.SampleTranscript())).To(Succeed())

			hookInput := testutil.SampleHookInput("test-session", transcriptPath, "git commit -m 'test'")
			_, _, err = testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store")
			Expect(err).NotTo(HaveOccurred())

			// Verify note was created on custom ref
			Expect(repo.HasNote("refs/notes/claude-conversations", head)).To(BeTrue())
			// Should NOT be on default ref
			Expect(repo.HasNote("refs/notes/commits", head)).To(BeFalse())
		})
	})
})
