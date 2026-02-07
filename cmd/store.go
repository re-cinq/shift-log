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

// PostToolUseHookInput represents the JSON payload sent by Claude Code's PostToolUse hook
type PostToolUseHookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	ToolName       string `json:"tool_name"`
	ToolInput      struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

var manualFlag bool

var storeCmd = &cobra.Command{
	Use:     "store",
	Short:   "Store conversation from Claude Code hook",
	GroupID: "hooks",
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
	cli.LogDebug("store: reading hook input from stdin")
	var hook PostToolUseHookInput
	if err := cli.ReadHookInput(&hook); err != nil {
		cli.LogDebug("store: failed to read hook input: %v", err)
		return nil // Exit silently to not disrupt workflow
	}

	cli.LogDebug("store: tool=%s command=%q session=%s", hook.ToolName, hook.ToolInput.Command, hook.SessionID)

	// Check if this is a git commit command
	if hook.ToolName != "Bash" || !isGitCommitCommand(hook.ToolInput.Command) {
		cli.LogDebug("store: not a git commit command, skipping")
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
	cli.LogDebug("store: manual mode")

	// Verify we're in a git repository
	if !git.IsInsideWorkTree() {
		cli.LogDebug("store: not inside a git repository, skipping")
		return nil // Exit silently - not in a git repo
	}

	// Get project path
	projectPath, err := git.GetRepoRoot()
	if err != nil {
		cli.LogDebug("store: failed to get repo root: %v", err)
		return nil // Exit silently
	}

	cli.LogDebug("store: discovering active session in %s", projectPath)

	// Discover active session
	activeSession, err := session.DiscoverSession(projectPath)
	if err != nil || activeSession == nil {
		cli.LogDebug("store: no active session found (err=%v)", err)
		// No session found - exit silently (don't disrupt git workflow)
		return nil
	}

	cli.LogDebug("store: found session %s", activeSession.SessionID)
	return storeConversation(activeSession.SessionID, activeSession.TranscriptPath)
}

// storeConversation stores a conversation for the HEAD commit with duplicate detection
func storeConversation(sessionID, transcriptPath string) error {
	// Get HEAD commit
	headCommit, err := git.GetHeadCommit()
	if err != nil {
		return fmt.Errorf("failed to get HEAD commit: %w", err)
	}

	cli.LogDebug("store: HEAD commit is %s", headCommit[:8])

	// Check for existing note (duplicate detection)
	if git.HasNote(headCommit) {
		cli.LogDebug("store: existing note found for %s, checking for duplicate", headCommit[:8])
		existingNote, err := git.GetNote(headCommit)
		if err == nil {
			existing, err := storage.UnmarshalStoredConversation(existingNote)
			if err == nil && existing.SessionID == sessionID {
				// Same session - already stored (idempotent)
				cli.LogInfo("conversation already stored for commit %s", headCommit[:8])
				return nil
			}
			cli.LogDebug("store: different session, will overwrite existing note")
			// Different session - will overwrite
		}
	}

	// Read and parse transcript
	if transcriptPath == "" {
		return fmt.Errorf("no transcript path provided")
	}

	cli.LogDebug("store: reading transcript from %s", transcriptPath)

	transcriptData, err := os.ReadFile(transcriptPath)
	if err != nil {
		return fmt.Errorf("failed to read transcript: %w", err)
	}

	cli.LogDebug("store: transcript size is %d bytes", len(transcriptData))

	transcript, err := claude.ParseTranscript(strings.NewReader(string(transcriptData)))
	if err != nil {
		return fmt.Errorf("failed to parse transcript: %w", err)
	}

	// Get git context
	projectPath, _ := git.GetRepoRoot()
	branch, _ := git.GetCurrentBranch()

	cli.LogDebug("store: project=%s branch=%s messages=%d", projectPath, branch, transcript.MessageCount())

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

	cli.LogDebug("store: note size is %d bytes", len(noteContent))

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
