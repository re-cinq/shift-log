package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/re-cinq/claudit/internal/claude"
	"github.com/re-cinq/claudit/internal/cli"
	"github.com/re-cinq/claudit/internal/git"
	"github.com/re-cinq/claudit/internal/storage"
	"github.com/spf13/cobra"
)

var (
	resumeForce bool
)

var resumeCmd = &cobra.Command{
	Use:     "resume <commit>",
	Short:   "Resume a Claude session from a commit",
	GroupID: "human",
	Long: `Restores a Claude Code session from a commit with a stored conversation,
checks out the commit, and launches Claude Code with the restored session.

Accepts various git references:
  - Full or short SHA: abc123def456
  - Branch name: feature-branch
  - Relative reference: HEAD~2

Examples:
  claudit resume abc123
  claudit resume feature-branch
  claudit resume HEAD~1`,
	Args: cobra.ExactArgs(1),
	RunE: runResume,
}

func init() {
	rootCmd.AddCommand(resumeCmd)
	resumeCmd.Flags().BoolVarP(&resumeForce, "force", "f", false, "Skip confirmation for uncommitted changes")
}

func runResume(cmd *cobra.Command, args []string) error {
	ref := args[0]

	cli.LogDebug("resume: resolving ref %s", ref)

	// Verify we're in a git repository
	if err := git.RequireGitRepo(); err != nil {
		return err
	}

	// Resolve the commit reference
	commitSHA, err := git.ResolveRef(ref)
	if err != nil {
		return fmt.Errorf("could not resolve commit: %s", ref)
	}

	cli.LogDebug("resume: resolved to commit %s", commitSHA[:8])

	// Read the stored conversation
	stored, err := storage.GetStoredConversation(commitSHA)
	if err != nil {
		return fmt.Errorf("could not read conversation: %w", err)
	}
	if stored == nil {
		return fmt.Errorf("no conversation found for commit %s", commitSHA[:8])
	}

	cli.LogDebug("resume: session=%s branch=%s messages=%d", stored.SessionID, stored.GitBranch, stored.MessageCount)

	// Verify integrity
	valid, err := stored.VerifyIntegrity()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not verify transcript integrity: %v\n", err)
	} else if !valid {
		fmt.Fprintf(os.Stderr, "warning: transcript checksum mismatch, conversation may be corrupted\n")
	}

	// Decompress transcript
	transcriptData, err := stored.GetTranscript()
	if err != nil {
		return fmt.Errorf("could not decompress transcript: %w", err)
	}

	// Check for uncommitted changes
	hasChanges, err := git.HasUncommittedChanges()
	if err != nil {
		return fmt.Errorf("could not check working directory status: %w", err)
	}

	if hasChanges && !resumeForce {
		fmt.Fprintf(os.Stderr, "warning: you have uncommitted changes\n")
		fmt.Fprintf(os.Stderr, "checkout will discard or conflict with these changes.\n")
		fmt.Fprint(os.Stderr, "continue? [y/N] ")

		reader := bufio.NewReader(os.Stdin)
		response, _ := reader.ReadString('\n')
		response = strings.TrimSpace(strings.ToLower(response))
		if response != "y" && response != "yes" {
			return fmt.Errorf("aborted")
		}
	}

	// Get project path for restoring session
	projectPath, err := git.GetRepoRoot()
	if err != nil {
		return fmt.Errorf("could not determine project path: %w", err)
	}

	// Restore the session files
	err = claude.RestoreSession(
		projectPath,
		stored.SessionID,
		stored.GitBranch,
		transcriptData,
		stored.MessageCount,
		"Restored session",
	)
	if err != nil {
		return fmt.Errorf("could not restore session: %w", err)
	}

	fmt.Printf("restored session %s (%d messages)\n", stored.SessionID, stored.MessageCount)

	// Checkout the commit
	if err := git.Checkout(commitSHA); err != nil {
		return fmt.Errorf("could not checkout commit: %w", err)
	}

	fmt.Printf("checked out %s\n", commitSHA[:8])

	// Launch Claude with the session
	fmt.Printf("launching claude --resume %s\n", stored.SessionID)

	claudeCmd := exec.Command("claude", "--resume", stored.SessionID)
	claudeCmd.Stdin = os.Stdin
	claudeCmd.Stdout = os.Stdout
	claudeCmd.Stderr = os.Stderr

	return claudeCmd.Run()
}
