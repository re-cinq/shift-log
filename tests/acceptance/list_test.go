package acceptance_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/DanielJonesEB/claudit/tests/acceptance/testutil"
)

var _ = Describe("List Command", func() {
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

	Describe("with conversations", func() {
		It("lists commits with conversations", func() {
			commitSHA := storeConversation("session-list-1")

			stdout, _, err := testutil.RunClauditInDir(repo.Path, "list")
			Expect(err).NotTo(HaveOccurred())

			// Should contain SHA
			Expect(stdout).To(ContainSubstring(commitSHA[:7]))
		})

		It("shows message count", func() {
			storeConversation("session-list-2")

			stdout, _, err := testutil.RunClauditInDir(repo.Path, "list")
			Expect(err).NotTo(HaveOccurred())

			// Should show message count
			Expect(stdout).To(MatchRegexp(`\d+ messages`))
		})

		It("lists multiple conversations", func() {
			// First commit with conversation
			storeConversation("session-multi-1")

			// Second commit with conversation
			Expect(repo.WriteFile("file2.txt", "content")).To(Succeed())
			Expect(repo.Commit("Second commit")).To(Succeed())
			storeConversation("session-multi-2")

			stdout, _, err := testutil.RunClauditInDir(repo.Path, "list")
			Expect(err).NotTo(HaveOccurred())

			// Should show both commits
			lines := countLines(stdout)
			Expect(lines).To(BeNumerically(">=", 2))
		})
	})

	Describe("without conversations", func() {
		It("shows 'no conversations found' message", func() {
			stdout, _, err := testutil.RunClauditInDir(repo.Path, "list")
			Expect(err).NotTo(HaveOccurred())

			Expect(stdout).To(ContainSubstring("no conversations found"))
		})
	})

	Describe("output format", func() {
		It("includes commit date", func() {
			storeConversation("session-date-test")

			stdout, _, err := testutil.RunClauditInDir(repo.Path, "list")
			Expect(err).NotTo(HaveOccurred())

			// Date format: YYYY-MM-DD
			Expect(stdout).To(MatchRegexp(`\d{4}-\d{2}-\d{2}`))
		})

		It("includes commit message", func() {
			storeConversation("session-msg-test")

			stdout, _, err := testutil.RunClauditInDir(repo.Path, "list")
			Expect(err).NotTo(HaveOccurred())

			// Should contain "Initial commit" from our test setup
			Expect(stdout).To(ContainSubstring("Initial commit"))
		})
	})

	Describe("outside git repository", func() {
		It("fails with error", func() {
			// Create a temp directory that's not a git repo
			tmpDir, err := os.MkdirTemp("", "claudit-no-git-*")
			Expect(err).NotTo(HaveOccurred())
			defer os.RemoveAll(tmpDir)

			_, stderr, err := testutil.RunClauditInDir(tmpDir, "list")
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("not inside a git repository"))
		})
	})
})

func countLines(s string) int {
	if s == "" {
		return 0
	}
	count := 1
	for _, c := range s {
		if c == '\n' {
			count++
		}
	}
	return count
}
