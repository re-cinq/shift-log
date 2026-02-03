package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/DanielJonesEB/claudit/internal/claude"
	"github.com/DanielJonesEB/claudit/internal/git"
	"github.com/DanielJonesEB/claudit/internal/storage"
	"github.com/spf13/cobra"
)

// HookInput represents the PostToolUse hook JSON input from Claude Code
type HookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	ToolName       string `json:"tool_name"`
	ToolInput      struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

var storeCmd = &cobra.Command{
	Use:   "store",
	Short: "Store conversation from Claude Code hook",
	Long: `Reads PostToolUse hook JSON from stdin and stores the conversation
as a Git Note if a git commit was detected.

This command is designed to be called by Claude Code's PostToolUse hook.`,
	RunE: runStore,
}

func init() {
	rootCmd.AddCommand(storeCmd)
}

func runStore(cmd *cobra.Command, args []string) error {
	// Read hook input from stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		logWarning("failed to read stdin: %v", err)
		return nil // Exit silently to not disrupt workflow
	}

	var hook HookInput
	if err := json.Unmarshal(input, &hook); err != nil {
		logWarning("failed to parse hook JSON: %v", err)
		return nil // Exit silently
	}

	// Check if this is a git commit command
	if hook.ToolName != "Bash" || !isGitCommitCommand(hook.ToolInput.Command) {
		return nil // Exit silently for non-commit commands
	}

	// Verify we're in a git repository
	if !git.IsInsideWorkTree() {
		logWarning("not inside a git repository")
		return nil
	}

	// Read and parse transcript
	if hook.TranscriptPath == "" {
		logWarning("no transcript path provided")
		return nil
	}

	transcriptData, err := os.ReadFile(hook.TranscriptPath)
	if err != nil {
		logWarning("failed to read transcript: %v", err)
		return nil
	}

	transcript, err := claude.ParseTranscript(strings.NewReader(string(transcriptData)))
	if err != nil {
		logWarning("failed to parse transcript: %v", err)
		return nil
	}

	// Get git context
	projectPath, _ := git.GetRepoRoot()
	branch, _ := git.GetCurrentBranch()
	headCommit, err := git.GetHeadCommit()
	if err != nil {
		logWarning("failed to get HEAD commit: %v", err)
		return nil
	}

	// Create stored conversation
	stored, err := storage.NewStoredConversation(
		hook.SessionID,
		projectPath,
		branch,
		transcript.MessageCount(),
		transcriptData,
	)
	if err != nil {
		logWarning("failed to create stored conversation: %v", err)
		return nil
	}

	// Marshal and store as git note
	noteContent, err := stored.Marshal()
	if err != nil {
		logWarning("failed to marshal conversation: %v", err)
		return nil
	}

	if err := git.AddNote(headCommit, noteContent); err != nil {
		logWarning("failed to add git note: %v", err)
		return nil
	}

	logInfo("stored conversation for commit %s", headCommit[:8])
	return nil
}

// isGitCommitCommand checks if a command is a git commit
func isGitCommitCommand(command string) bool {
	// Simple heuristic: check if command contains "git commit"
	// This handles: git commit, git commit -m, git commit -am, etc.
	return strings.Contains(command, "git commit") ||
		strings.Contains(command, "git-commit")
}

func logWarning(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "claudit: warning: "+format+"\n", args...)
}

func logInfo(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "claudit: "+format+"\n", args...)
}
