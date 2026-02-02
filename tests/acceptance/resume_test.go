package acceptance_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/anthropics/claudit/tests/acceptance/testutil"
)

var _ = Describe("Resume Command", func() {
	var repo *testutil.GitRepo
	var claudeEnv *testutil.ClaudeEnv

	BeforeEach(func() {
		var err error
		repo, err = testutil.NewGitRepo()
		Expect(err).NotTo(HaveOccurred())

		claudeEnv, err = testutil.NewClaudeEnv()
		Expect(err).NotTo(HaveOccurred())

		// Create initial commit
		Expect(repo.WriteFile("README.md", "# Test")).To(Succeed())
		Expect(repo.Commit("Initial commit")).To(Succeed())
	})

	AfterEach(func() {
		if repo != nil {
			repo.Cleanup()
		}
		if claudeEnv != nil {
			claudeEnv.Cleanup()
		}
	})

	// Helper to store a conversation on the current commit
	storeConversation := func(sessionID string) string {
		transcriptPath := filepath.Join(repo.Path, "transcript.jsonl")
		Expect(os.WriteFile(transcriptPath, []byte(testutil.SampleTranscript()), 0644)).To(Succeed())

		head, err := repo.GetHead()
		Expect(err).NotTo(HaveOccurred())

		hookInput := testutil.SampleHookInput(sessionID, transcriptPath, "git commit -m 'test'")
		_, _, err = testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store")
		Expect(err).NotTo(HaveOccurred())

		return head
	}

	Describe("resolving commit references", func() {
		It("resolves full SHA", func() {
			commitSHA := storeConversation("session-full-sha")

			stdout, stderr, err := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", commitSHA, "--force",
			)

			// It will fail to launch claude, but that's OK for this test
			// We just want to verify it resolved the SHA correctly
			Expect(stdout).To(ContainSubstring("restored session"))
			_ = stderr
			_ = err
		})

		It("resolves short SHA", func() {
			commitSHA := storeConversation("session-short-sha")
			shortSHA := commitSHA[:7]

			stdout, _, _ := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", shortSHA, "--force",
			)

			Expect(stdout).To(ContainSubstring("restored session"))
		})

		It("resolves HEAD reference", func() {
			storeConversation("session-head-ref")

			stdout, _, _ := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", "HEAD", "--force",
			)

			Expect(stdout).To(ContainSubstring("restored session"))
		})
	})

	Describe("restoring session files", func() {
		It("writes transcript to Claude projects directory", func() {
			commitSHA := storeConversation("session-restore-test")

			testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", commitSHA, "--force",
			)

			// Verify session file was created
			Expect(claudeEnv.SessionFileExists(repo.Path, "session-restore-test")).To(BeTrue())
		})

		It("creates sessions-index.json", func() {
			commitSHA := storeConversation("session-index-test")

			testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", commitSHA, "--force",
			)

			// Verify index was created
			Expect(claudeEnv.SessionsIndexExists(repo.Path)).To(BeTrue())
		})
	})

	Describe("handling missing conversations", func() {
		It("fails when commit has no conversation", func() {
			// Create a commit without a conversation
			Expect(repo.WriteFile("file.txt", "content")).To(Succeed())
			Expect(repo.Commit("Commit without conversation")).To(Succeed())

			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			_, stderr, err := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", head, "--force",
			)

			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("no conversation found"))
		})

		It("fails with invalid commit reference", func() {
			_, stderr, err := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", "invalid-ref", "--force",
			)

			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("could not resolve commit"))
		})
	})

	Describe("handling uncommitted changes", func() {
		It("warns about uncommitted changes without --force", func() {
			commitSHA := storeConversation("session-dirty")

			// Make uncommitted changes
			Expect(repo.WriteFile("uncommitted.txt", "changes")).To(Succeed())

			// Run without stdin to simulate non-interactive
			_, stderr, _ := testutil.RunClauditInDirWithEnvAndStdin(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"n\n", // Respond "no" to prompt
				"resume", commitSHA,
			)

			Expect(stderr).To(ContainSubstring("uncommitted changes"))
		})

		It("proceeds with --force flag despite uncommitted changes", func() {
			commitSHA := storeConversation("session-force")

			// Make uncommitted changes
			Expect(repo.WriteFile("uncommitted.txt", "changes")).To(Succeed())

			stdout, _, _ := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", commitSHA, "--force",
			)

			Expect(stdout).To(ContainSubstring("restored session"))
		})
	})
})
