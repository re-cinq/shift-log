package acceptance_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/re-cinq/claudit/tests/acceptance/testutil"
)

var _ = Describe("Gemini Store", func() {
	var repo *testutil.GitRepo

	BeforeEach(func() {
		var err error
		repo, err = testutil.NewGitRepo()
		Expect(err).NotTo(HaveOccurred())

		// CRITICAL: Set binary path so git hooks can find claudit
		repo.SetBinaryPath(testutil.BinaryPath())

		// Initialize claudit with gemini agent
		_, _, err = testutil.RunClauditInDir(repo.Path, "init", "--agent=gemini")
		Expect(err).NotTo(HaveOccurred())

		// Create initial commit
		Expect(repo.WriteFile("README.md", "# Test")).To(Succeed())
		Expect(repo.Commit("Initial commit")).To(Succeed())
	})

	AfterEach(func() {
		repo.Cleanup()
	})

	It("stores a git note when run_shell_command with git commit is detected", func() {
		// Create a Gemini session transcript file
		transcriptPath := filepath.Join(os.TempDir(), "gemini-test-transcript.json")
		Expect(os.WriteFile(transcriptPath, []byte(testutil.SampleGeminiTranscript()), 0644)).To(Succeed())
		defer os.Remove(transcriptPath)

		// Make a new commit
		Expect(repo.WriteFile("test.txt", "hello")).To(Succeed())
		Expect(repo.Commit("Add test file")).To(Succeed())

		// Build hook input simulating a git commit via run_shell_command
		hookInput := testutil.SampleGeminiHookInput("gemini-session-123", transcriptPath, "git commit -m 'Add test file'")

		// Run store with gemini agent
		_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store", "--agent=gemini")
		Expect(err).NotTo(HaveOccurred())
		Expect(stderr).To(ContainSubstring("stored conversation"))

		// Verify note was created
		noteOutput, err := repo.GetNote("refs/notes/claude-conversations", "HEAD")
		Expect(err).NotTo(HaveOccurred())
		Expect(noteOutput).NotTo(BeEmpty())

		// Parse and verify note content
		var noteData map[string]interface{}
		Expect(json.Unmarshal([]byte(noteOutput), &noteData)).To(Succeed())
		Expect(noteData["agent"]).To(Equal("gemini"))
		Expect(noteData["session_id"]).To(Equal("gemini-session-123"))
		Expect(noteData["checksum"]).NotTo(BeEmpty())
		Expect(noteData["transcript"]).NotTo(BeEmpty())
	})

	It("exits silently for non-commit commands", func() {
		// Create transcript
		transcriptPath := filepath.Join(os.TempDir(), "gemini-noncommit-transcript.json")
		Expect(os.WriteFile(transcriptPath, []byte(testutil.SampleGeminiTranscript()), 0644)).To(Succeed())
		defer os.Remove(transcriptPath)

		// Make a commit so HEAD exists
		Expect(repo.WriteFile("test.txt", "hello")).To(Succeed())
		Expect(repo.Commit("Add test file")).To(Succeed())

		// Hook input with a non-commit command
		hookInput := testutil.SampleGeminiHookInput("gemini-session-456", transcriptPath, "ls -la")

		_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store", "--agent=gemini")
		Expect(err).NotTo(HaveOccurred())
		Expect(stderr).NotTo(ContainSubstring("stored"))

		// Verify no note was created
		Expect(repo.HasNote("refs/notes/claude-conversations", "HEAD")).To(BeFalse())
	})

	It("exits silently for non-shell tools", func() {
		// Make a commit so HEAD exists
		Expect(repo.WriteFile("test.txt", "hello")).To(Succeed())
		Expect(repo.Commit("Add test file")).To(Succeed())

		// Hook input with a non-shell tool (read_file)
		hookInput := testutil.SampleGeminiHookInputNonShell("gemini-session-789")

		_, stderr, err := testutil.RunClauditInDirWithStdin(repo.Path, hookInput, "store", "--agent=gemini")
		Expect(err).NotTo(HaveOccurred())
		Expect(stderr).NotTo(ContainSubstring("stored"))

		// Verify no note was created
		Expect(repo.HasNote("refs/notes/claude-conversations", "HEAD")).To(BeFalse())
	})
})
