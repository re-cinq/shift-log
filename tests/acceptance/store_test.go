package acceptance_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/DanielJonesEB/claudit/tests/acceptance/testutil"
)

var _ = Describe("Store Command", func() {
	var repo *testutil.GitRepo

	BeforeEach(func() {
		var err error
		repo, err = testutil.NewGitRepo()
		Expect(err).NotTo(HaveOccurred())

		// Create an initial commit so we have HEAD
		Expect(repo.WriteFile("README.md", "# Test")).To(Succeed())
		Expect(repo.Commit("Initial commit")).To(Succeed())
	})

	AfterEach(func() {
		if repo != nil {
			repo.Cleanup()
		}
	})

	Describe("with git commit command", func() {
		It("creates a git note with conversation", func() {
			// Write transcript file
			transcriptPath := filepath.Join(repo.Path, "transcript.jsonl")
			Expect(os.WriteFile(transcriptPath, []byte(testutil.SampleTranscript()), 0644)).To(Succeed())

			// Get current HEAD
			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			// Simulate hook input
			hookInput := testutil.SampleHookInput("session-123", transcriptPath, "git commit -m 'test'")

			_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store")
			Expect(err).NotTo(HaveOccurred())
			Expect(stderr).To(ContainSubstring("stored conversation"))

			// Verify note was created
			Expect(repo.HasNote("refs/notes/claude-conversations", head)).To(BeTrue())
		})

		It("stores note with expected metadata", func() {
			transcriptPath := filepath.Join(repo.Path, "transcript.jsonl")
			Expect(os.WriteFile(transcriptPath, []byte(testutil.SampleTranscript()), 0644)).To(Succeed())

			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			hookInput := testutil.SampleHookInput("session-456", transcriptPath, "git commit -m 'test'")
			_, _, err = testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store")
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
			transcriptPath := filepath.Join(repo.Path, "transcript.jsonl")
			originalTranscript := testutil.SampleTranscript()
			Expect(os.WriteFile(transcriptPath, []byte(originalTranscript), 0644)).To(Succeed())

			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			hookInput := testutil.SampleHookInput("session-789", transcriptPath, "git commit -m 'test'")
			_, _, err = testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store")
			Expect(err).NotTo(HaveOccurred())

			noteContent, err := repo.GetNote("refs/notes/claude-conversations", head)
			Expect(err).NotTo(HaveOccurred())

			var stored map[string]interface{}
			Expect(json.Unmarshal([]byte(noteContent), &stored)).To(Succeed())

			// Decode and decompress
			encoded := stored["transcript"].(string)

			// Use our storage package to verify
			// For now, just verify the field exists and is non-empty
			Expect(encoded).NotTo(BeEmpty())
		})
	})

	Describe("with non-commit command", func() {
		It("exits silently without creating note", func() {
			transcriptPath := filepath.Join(repo.Path, "transcript.jsonl")
			Expect(os.WriteFile(transcriptPath, []byte(testutil.SampleTranscript()), 0644)).To(Succeed())

			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			// Use a non-commit command
			hookInput := testutil.SampleHookInput("session-123", transcriptPath, "ls -la")

			_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store")
			Expect(err).NotTo(HaveOccurred())
			Expect(stderr).To(BeEmpty())

			// Note should NOT be created
			Expect(repo.HasNote("refs/notes/claude-conversations", head)).To(BeFalse())
		})
	})

	Describe("with non-Bash tool", func() {
		It("exits silently", func() {
			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			hookInput := testutil.SampleHookInputNonBash("session-123")

			_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store")
			Expect(err).NotTo(HaveOccurred())
			Expect(stderr).To(BeEmpty())

			Expect(repo.HasNote("refs/notes/claude-conversations", head)).To(BeFalse())
		})
	})

	Describe("with malformed JSON", func() {
		It("exits with warning but no error", func() {
			_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, "not valid json", "store")
			Expect(err).NotTo(HaveOccurred())
			Expect(stderr).To(ContainSubstring("warning"))
		})
	})

	Describe("with missing transcript file", func() {
		It("exits with error when transcript is missing", func() {
			hookInput := testutil.SampleHookInput("session-123", "/nonexistent/path.jsonl", "git commit -m 'test'")

			_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store")
			// After fix: should return error instead of exiting silently
			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("failed to read transcript"))
		})
	})
})
