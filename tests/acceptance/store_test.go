package acceptance_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/re-cinq/claudit/tests/acceptance/testutil"
)

var _ = Describe("Store Command", func() {
	for _, cfg := range testutil.AllAgentConfigs() {
		config := cfg // capture loop variable

		Describe(config.Name+" agent", func() {
			var repo *testutil.GitRepo

			BeforeEach(func() {
				var err error
				repo, err = testutil.NewGitRepo()
				Expect(err).NotTo(HaveOccurred())

				if config.NeedsBinaryPath {
					repo.SetBinaryPath(testutil.BinaryPath())
				}

				// Initialize agent hooks
				_, _, err = testutil.RunClauditInDir(repo.Path, config.InitArgs...)
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

			It("creates a git note with conversation", func() {
				hookParam, err := config.PrepareTranscript(repo.Path, "session-123", config.SampleTranscript())
				Expect(err).NotTo(HaveOccurred())

				head, err := repo.GetHead()
				Expect(err).NotTo(HaveOccurred())

				hookInput := config.SampleHookInput("session-123", hookParam, "git commit -m 'test'")

				_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, hookInput, config.StoreArgs...)
				Expect(err).NotTo(HaveOccurred())
				Expect(stderr).To(ContainSubstring("stored conversation"))

				Expect(repo.HasNote("refs/notes/claude-conversations", head)).To(BeTrue())
			})

			It("stores note with expected metadata", func() {
				hookParam, err := config.PrepareTranscript(repo.Path, "session-456", config.SampleTranscript())
				Expect(err).NotTo(HaveOccurred())

				head, err := repo.GetHead()
				Expect(err).NotTo(HaveOccurred())

				hookInput := config.SampleHookInput("session-456", hookParam, "git commit -m 'test'")
				_, _, err = testutil.RunClauditInDirWithStdin(repo.Path, hookInput, config.StoreArgs...)
				Expect(err).NotTo(HaveOccurred())

				noteContent, err := repo.GetNote("refs/notes/claude-conversations", head)
				Expect(err).NotTo(HaveOccurred())

				var stored map[string]interface{}
				Expect(json.Unmarshal([]byte(noteContent), &stored)).To(Succeed())

				Expect(stored["version"]).To(BeEquivalentTo(1))
				Expect(stored["session_id"]).To(Equal("session-456"))
				Expect(stored["checksum"]).To(HavePrefix("sha256:"))
				Expect(stored["transcript"]).NotTo(BeEmpty())
			})

			It("transcript can be decompressed from note", func() {
				hookParam, err := config.PrepareTranscript(repo.Path, "session-789", config.SampleTranscript())
				Expect(err).NotTo(HaveOccurred())

				head, err := repo.GetHead()
				Expect(err).NotTo(HaveOccurred())

				hookInput := config.SampleHookInput("session-789", hookParam, "git commit -m 'test'")
				_, _, err = testutil.RunClauditInDirWithStdin(repo.Path, hookInput, config.StoreArgs...)
				Expect(err).NotTo(HaveOccurred())

				noteContent, err := repo.GetNote("refs/notes/claude-conversations", head)
				Expect(err).NotTo(HaveOccurred())

				var stored map[string]interface{}
				Expect(json.Unmarshal([]byte(noteContent), &stored)).To(Succeed())

				encoded := stored["transcript"].(string)
				Expect(encoded).NotTo(BeEmpty())
			})

			It("exits silently for non-commit commands", func() {
				hookParam, err := config.PrepareTranscript(repo.Path, "session-123", config.SampleTranscript())
				Expect(err).NotTo(HaveOccurred())

				head, err := repo.GetHead()
				Expect(err).NotTo(HaveOccurred())

				hookInput := config.SampleHookInput("session-123", hookParam, "ls -la")

				_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, hookInput, config.StoreArgs...)
				Expect(err).NotTo(HaveOccurred())
				Expect(stderr).To(BeEmpty())

				Expect(repo.HasNote("refs/notes/claude-conversations", head)).To(BeFalse())
			})

			It("exits silently for non-matching tool", func() {
				head, err := repo.GetHead()
				Expect(err).NotTo(HaveOccurred())

				hookInput := config.SampleNonToolInput("session-123")

				_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, hookInput, config.StoreArgs...)
				Expect(err).NotTo(HaveOccurred())
				Expect(stderr).To(BeEmpty())

				Expect(repo.HasNote("refs/notes/claude-conversations", head)).To(BeFalse())
			})

			It("exits with warning for malformed JSON", func() {
				_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, "not valid json", config.StoreArgs...)
				Expect(err).NotTo(HaveOccurred())
				Expect(stderr).To(ContainSubstring("warning"))
			})

			It("exits with error for missing transcript", func() {
				hookInput := config.SampleHookInput("session-123", "/nonexistent/path", "git commit -m 'test'")

				_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, hookInput, config.StoreArgs...)
				Expect(err).To(HaveOccurred())
				Expect(stderr).To(ContainSubstring("failed to read transcript"))
			})
		})
	}
})
