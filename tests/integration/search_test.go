package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Search", func() {
	It("should find conversations by text content", func() {
		clauditPath := getClauditPath()
		tmpDir := initGitRepo("search-integration")
		DeferCleanup(os.RemoveAll, tmpDir)

		// Store a conversation with known content
		transcriptData := `{"uuid":"u1","type":"user","message":{"role":"user","content":[{"type":"text","text":"Help me implement authentication"}]}}
{"uuid":"a1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Sure, let me set up JWT tokens for you."}]}}`
		transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
		Expect(os.WriteFile(transcriptPath, []byte(transcriptData), 0644)).To(Succeed())

		hookInput := `{"session_id":"search-test","transcript_path":"` + transcriptPath + `","tool_name":"Bash","tool_input":{"command":"git commit -m 'test'"}}`
		storeCmd := exec.Command(clauditPath, "store")
		storeCmd.Dir = tmpDir
		storeCmd.Stdin = strings.NewReader(hookInput)
		storeCmd.Env = append(os.Environ(), "PATH="+filepath.Dir(clauditPath)+":"+os.Getenv("PATH"))
		storeOutput, err := storeCmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "claudit store failed:\n%s", storeOutput)

		By("Conversation stored, now searching...")

		// Search for content that exists
		searchCmd := exec.Command(clauditPath, "search", "authentication")
		searchCmd.Dir = tmpDir
		searchCmd.Env = append(os.Environ(), "NO_COLOR=1")
		searchOutput, err := searchCmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "claudit search failed:\n%s", searchOutput)

		output := string(searchOutput)
		Expect(output).To(ContainSubstring("Initial commit"))
		Expect(output).To(ContainSubstring("authentication"))
		Expect(output).To(ContainSubstring("[user]"))

		By("Text search found the conversation")

		// Search for content that doesn't exist
		searchCmd = exec.Command(clauditPath, "search", "xyznonexistent99")
		searchCmd.Dir = tmpDir
		searchCmd.Env = append(os.Environ(), "NO_COLOR=1")
		searchOutput, err = searchCmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred())
		Expect(string(searchOutput)).To(ContainSubstring("no matching conversations found"))

		By("No-match case handled cleanly")
	})
})
