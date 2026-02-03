package acceptance_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/DanielJonesEB/claudit/tests/acceptance/testutil"
)

var _ = Describe("Show Command", func() {
	var repo *testutil.GitRepo

	BeforeEach(func() {
		var err error
		repo, err = testutil.NewGitRepo()
		Expect(err).NotTo(HaveOccurred())

		// Create initial commit
		Expect(repo.WriteFile("README.md", "# Test")).To(Succeed())
		Expect(repo.Commit("Initial commit")).To(Succeed())
	})

	AfterEach(func() {
		if repo != nil {
			repo.Cleanup()
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

	Describe("with conversation", func() {
		It("shows conversation for commit SHA", func() {
			commitSHA := storeConversation("session-show-1")

			stdout, _, err := testutil.RunClauditInDir(repo.Path, "show", commitSHA)
			Expect(err).NotTo(HaveOccurred())

			// Should contain conversation header
			Expect(stdout).To(ContainSubstring(commitSHA[:7]))
			Expect(stdout).To(ContainSubstring("Messages:"))
		})

		It("shows conversation for HEAD when no ref provided", func() {
			storeConversation("session-show-2")

			stdout, _, err := testutil.RunClauditInDir(repo.Path, "show")
			Expect(err).NotTo(HaveOccurred())

			// Should show conversation
			Expect(stdout).To(ContainSubstring("Conversation for"))
		})

		It("displays User: prefix for user messages", func() {
			storeConversation("session-show-3")

			stdout, _, err := testutil.RunClauditInDir(repo.Path, "show")
			Expect(err).NotTo(HaveOccurred())

			Expect(stdout).To(ContainSubstring("User:"))
		})

		It("displays Assistant: prefix for assistant messages", func() {
			storeConversation("session-show-4")

			stdout, _, err := testutil.RunClauditInDir(repo.Path, "show")
			Expect(err).NotTo(HaveOccurred())

			Expect(stdout).To(ContainSubstring("Assistant:"))
		})

		It("displays message content", func() {
			storeConversation("session-show-5")

			stdout, _, err := testutil.RunClauditInDir(repo.Path, "show")
			Expect(err).NotTo(HaveOccurred())

			// Should contain the sample transcript content
			Expect(stdout).To(ContainSubstring("Hello, can you help me with a task?"))
			Expect(stdout).To(ContainSubstring("Of course! What would you like help with?"))
		})
	})

	Describe("without conversation", func() {
		It("shows error when commit has no conversation", func() {
			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			_, stderr, err := testutil.RunClauditInDir(repo.Path, "show", head)
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("no conversation found"))
		})

		It("shows error when HEAD has no conversation", func() {
			_, stderr, err := testutil.RunClauditInDir(repo.Path, "show")
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("no conversation found"))
		})
	})

	Describe("with invalid reference", func() {
		It("shows error for invalid commit reference", func() {
			_, stderr, err := testutil.RunClauditInDir(repo.Path, "show", "invalid-ref-xyz")
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("could not resolve reference"))
		})
	})

	Describe("relative references", func() {
		It("supports HEAD~1 syntax", func() {
			// Create first commit with conversation
			storeConversation("session-relative-1")

			// Create second commit without conversation
			Expect(repo.WriteFile("file2.txt", "content")).To(Succeed())
			Expect(repo.Commit("Second commit")).To(Succeed())

			// HEAD~1 should show the previous commit's conversation
			stdout, _, err := testutil.RunClauditInDir(repo.Path, "show", "HEAD~1")
			Expect(err).NotTo(HaveOccurred())
			Expect(stdout).To(ContainSubstring("User:"))
		})
	})

	Describe("outside git repository", func() {
		It("fails with error", func() {
			tmpDir, err := os.MkdirTemp("", "claudit-no-git-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(tmpDir)

			_, stderr, err := testutil.RunClauditInDir(tmpDir, "show")
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("not inside a git repository"))
		})
	})
})
