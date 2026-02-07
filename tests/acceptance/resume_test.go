package acceptance_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/DanielJonesEB/claudit/internal/storage"
	"github.com/DanielJonesEB/claudit/tests/acceptance/testutil"
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

	Describe("handling corrupt conversations", func() {
		It("fails when note contains invalid JSON", func() {
			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			// Write garbage directly as a git note
			repo.AddNote("refs/notes/claude-conversations", head, "not valid json at all")

			_, stderr, err := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", head, "--force",
			)

			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("could not parse"))
		})

		It("warns on checksum mismatch but still restores", func() {
			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			// Create a valid stored conversation, then tamper with the checksum
			transcript := []byte(testutil.SampleTranscript())
			sc, err := storage.NewStoredConversation("session-tampered", repo.Path, "master", 4, transcript)
			Expect(err).NotTo(HaveOccurred())

			sc.Checksum = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

			noteData, err := sc.Marshal()
			Expect(err).NotTo(HaveOccurred())
			repo.AddNote("refs/notes/claude-conversations", head, string(noteData))

			stdout, stderr, _ := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", head, "--force",
			)

			// Should warn about checksum mismatch
			Expect(stderr).To(ContainSubstring("checksum mismatch"))
			// But should still restore the session
			Expect(stdout).To(ContainSubstring("restored session"))
		})

		It("fails when transcript cannot be decompressed", func() {
			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			// Create a note with valid JSON structure but invalid transcript data
			note := map[string]interface{}{
				"version":       1,
				"session_id":    "session-corrupt",
				"timestamp":     "2025-01-01T00:00:00Z",
				"project_path":  repo.Path,
				"git_branch":    "master",
				"message_count": 1,
				"checksum":      "sha256:abcd",
				"transcript":    "dGhpcyBpcyBub3QgZ3ppcCBkYXRh", // base64("this is not gzip data")
			}
			noteData, err := json.Marshal(note)
			Expect(err).NotTo(HaveOccurred())
			repo.AddNote("refs/notes/claude-conversations", head, string(noteData))

			_, stderr, err := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", head, "--force",
			)

			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("could not decompress"))
		})

		It("fails when transcript field is not valid base64", func() {
			head, err := repo.GetHead()
			Expect(err).NotTo(HaveOccurred())

			note := map[string]interface{}{
				"version":       1,
				"session_id":    "session-bad-b64",
				"timestamp":     "2025-01-01T00:00:00Z",
				"project_path":  repo.Path,
				"git_branch":    "master",
				"message_count": 1,
				"checksum":      "sha256:abcd",
				"transcript":    "!!!not-base64!!!",
			}
			noteData, err := json.Marshal(note)
			Expect(err).NotTo(HaveOccurred())
			repo.AddNote("refs/notes/claude-conversations", head, string(noteData))

			_, stderr, err := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", head, "--force",
			)

			Expect(err).To(HaveOccurred())
			// Integrity check fails first (decode error), then decompress fails
			Expect(stderr).To(SatisfyAny(
				ContainSubstring("could not verify transcript integrity"),
				ContainSubstring("could not decompress"),
			))
		})
	})

	Describe("resolving relative references", func() {
		It("resolves HEAD~1 to parent commit", func() {
			// Store conversation on first commit
			storeConversation("session-parent")

			// Create a second commit (no conversation)
			Expect(repo.WriteFile("second.txt", "content")).To(Succeed())
			Expect(repo.Commit("Second commit")).To(Succeed())

			// Resume HEAD~1 should find the first commit's conversation
			stdout, _, _ := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", "HEAD~1", "--force",
			)

			Expect(stdout).To(ContainSubstring("restored session"))
			Expect(stdout).To(ContainSubstring("session-parent"))
		})
	})

	Describe("session content verification", func() {
		It("restores the original transcript content", func() {
			originalTranscript := testutil.SampleTranscript()
			commitSHA := storeConversation("session-content-verify")

			testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", commitSHA, "--force",
			)

			// Read back the restored session file and verify content matches
			content, err := claudeEnv.ReadSessionFile(repo.Path, "session-content-verify")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(content)).To(Equal(originalTranscript))
		})

		It("populates sessions-index.json with correct metadata", func() {
			commitSHA := storeConversation("session-meta-check")

			testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume", commitSHA, "--force",
			)

			// Read and parse the sessions index
			indexPath := claudeEnv.GetSessionsIndexPath(repo.Path)
			indexData, err := os.ReadFile(indexPath)
			Expect(err).NotTo(HaveOccurred())

			var index map[string]interface{}
			Expect(json.Unmarshal(indexData, &index)).To(Succeed())

			entries := index["entries"].([]interface{})
			Expect(entries).To(HaveLen(1))

			entry := entries[0].(map[string]interface{})
			Expect(entry["sessionId"]).To(Equal("session-meta-check"))
			Expect(entry["firstPrompt"]).To(Equal("Hello, can you help me with a task?"))
			Expect(entry["messageCount"]).To(BeEquivalentTo(4))
		})
	})

	Describe("requires arguments", func() {
		It("fails when no commit argument is provided", func() {
			_, stderr, err := testutil.RunClauditInDirWithEnv(
				repo.Path,
				claudeEnv.GetEnvVars(),
				"resume",
			)

			Expect(err).To(HaveOccurred())
			Expect(stderr).To(ContainSubstring("accepts 1 arg"))
		})
	})
})
