package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/DanielJonesEB/claudit/internal/claude"
	"github.com/DanielJonesEB/claudit/internal/cli"
	"github.com/DanielJonesEB/claudit/internal/git"
	"github.com/DanielJonesEB/claudit/internal/session"
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

var manualFlag bool

var storeCmd = &cobra.Command{
	Use:   "store",
	Short: "Store conversation from Claude Code hook",
	Long: `Reads PostToolUse hook JSON from stdin and stores the conversation
as a Git Note if a git commit was detected.

This command is designed to be called by Claude Code's PostToolUse hook.

With --manual flag, discovers the active session and stores its conversation
for the most recent commit. Used by the post-commit git hook.`,
	RunE: runStore,
}

func init() {
	storeCmd.Flags().BoolVar(&manualFlag, "manual", false, "Manual mode: discover session from active session file or recent sessions")
	rootCmd.AddCommand(storeCmd)
}

func runStore(cmd *cobra.Command, args []string) error {
	if manualFlag {
		return runManualStore()
	}
	return runHookStore()
}

// runHookStore handles the PostToolUse hook mode
func runHookStore() error {
	var hook HookInput
	if err := cli.ReadHookInput(&hook); err != nil {
		return nil // Exit silently to not disrupt workflow
	}

	// Check if this is a git commit command
	if hook.ToolName != "Bash" || !isGitCommitCommand(hook.ToolInput.Command) {
		return nil // Exit silently for non-commit commands
	}

	// Verify we're in a git repository
	if !git.IsInsideWorkTree() {
		cli.LogWarning("not inside a git repository")
		return nil
	}

	return storeConversation(hook.SessionID, hook.TranscriptPath)
}

// runManualStore handles the manual (post-commit hook) mode
func runManualStore() error {
	// Verify we're in a git repository
	if !git.IsInsideWorkTree() {
		return nil // Exit silently - not in a git repo
	}

	// Get project path
	projectPath, err := git.GetRepoRoot()
	if err != nil {
		return nil // Exit silently
	}

	// Discover active session
	activeSession, err := session.DiscoverSession(projectPath)
	if err != nil || activeSession == nil {
		// No session found - exit silently (don't disrupt git workflow)
		return nil
	}

	return storeConversation(activeSession.SessionID, activeSession.TranscriptPath)
}

// storeConversation stores a conversation for the HEAD commit with duplicate detection
func storeConversation(sessionID, transcriptPath string) error {
	// Get HEAD commit
	headCommit, err := git.GetHeadCommit()
	if err != nil {
		return fmt.Errorf("failed to get HEAD commit: %w", err)
	}

	// Check for existing note (duplicate detection)
	if git.HasNote(headCommit) {
		existingNote, err := git.GetNote(headCommit)
		if err == nil {
			existing, err := storage.UnmarshalStoredConversation(existingNote)
			if err == nil && existing.SessionID == sessionID {
				// Same session - already stored (idempotent)
				cli.LogInfo("conversation already stored for commit %s", headCommit[:8])
				return nil
			}
			// Different session - will overwrite
		}
	}

	// Read and parse transcript
	if transcriptPath == "" {
		return fmt.Errorf("no transcript path provided")
	}

	transcriptData, err := os.ReadFile(transcriptPath)
	if err != nil {
		return fmt.Errorf("failed to read transcript: %w", err)
	}

	transcript, err := claude.ParseTranscript(strings.NewReader(string(transcriptData)))
	if err != nil {
		return fmt.Errorf("failed to parse transcript: %w", err)
	}

	// Get git context
	projectPath, _ := git.GetRepoRoot()
	branch, _ := git.GetCurrentBranch()

	// Create stored conversation
	stored, err := storage.NewStoredConversation(
		sessionID,
		projectPath,
		branch,
		transcript.MessageCount(),
		transcriptData,
	)
	if err != nil {
		return fmt.Errorf("failed to create stored conversation: %w", err)
	}

	// Marshal and store as git note
	noteContent, err := stored.Marshal()
	if err != nil {
		return fmt.Errorf("failed to marshal conversation: %w", err)
	}

	if err := git.AddNote(headCommit, noteContent); err != nil {
		return fmt.Errorf("failed to add git note: %w", err)
	}

	cli.LogInfo("stored conversation for commit %s", headCommit[:8])
	return nil
}

// isGitCommitCommand checks if a command is a git commit
func isGitCommitCommand(command string) bool {
	// Simple heuristic: check if command contains "git commit"
	// This handles: git commit, git commit -m, git commit -am, etc.
	return strings.Contains(command, "git commit") ||
		strings.Contains(command, "git-commit")
}
