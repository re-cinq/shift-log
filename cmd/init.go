package cmd

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/DanielJonesEB/claudit/internal/claude"
	"github.com/DanielJonesEB/claudit/internal/config"
	"github.com/DanielJonesEB/claudit/internal/git"
	"github.com/DanielJonesEB/claudit/internal/storage"
	"github.com/spf13/cobra"
)

var (
	notesRefFlag string
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize claudit in the current repository",
	Long: `Configures the current git repository for conversation capture.

This command:
- Uses refs/notes/commits by default (or --notes-ref for custom location)
- Creates/updates .claude/settings.local.json with PostToolUse hook
- Installs git hooks for automatic note syncing
- Configures git settings for notes visibility

If existing notes are found that were not written by Claudit, initialization
will abort with a warning. Use a different --notes-ref to avoid conflicts.`,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringVar(&notesRefFlag, "notes-ref", "", "Git notes ref to use (default: refs/notes/commits)")
}

func runInit(cmd *cobra.Command, args []string) error {
	// Verify we're in a git repository
	if err := git.RequireGitRepo(); err != nil {
		return err
	}

	repoRoot, err := git.GetRepoRoot()
	if err != nil {
		return fmt.Errorf("failed to get repository root: %w", err)
	}

	// Determine notes ref to use
	notesRef, err := determineNotesRef()
	if err != nil {
		return err
	}

	// Check for existing non-Claudit notes
	if err := checkExistingNotes(notesRef); err != nil {
		return err
	}

	// Store configuration
	cfg := &config.Config{
		NotesRef: notesRef,
	}
	if err := config.Write(cfg); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	fmt.Printf("✓ Configured notes ref: %s\n", notesRef)

	// Configure git settings for notes visibility
	if err := configureGitSettings(notesRef); err != nil {
		return fmt.Errorf("failed to configure git settings: %w", err)
	}

	fmt.Println("✓ Configured git notes settings (displayRef, rewriteRef)")

	// Configure Claude hooks
	claudeDir := filepath.Join(repoRoot, ".claude")
	settings, err := claude.ReadSettings(claudeDir)
	if err != nil {
		return fmt.Errorf("failed to read Claude settings: %w", err)
	}

	claude.AddClauditHook(settings)
	claude.AddSessionHooks(settings)

	if err := claude.WriteSettings(claudeDir, settings); err != nil {
		return fmt.Errorf("failed to write Claude settings: %w", err)
	}

	fmt.Println("✓ Configured Claude hooks (PostToolUse, SessionStart, SessionEnd)")

	// Install git hooks
	gitDir, err := git.EnsureGitDir()
	if err != nil {
		return fmt.Errorf("failed to find git directory: %w", err)
	}

	if err := git.InstallAllHooks(gitDir); err != nil {
		return fmt.Errorf("failed to install git hooks: %w", err)
	}

	fmt.Println("✓ Installed git hooks (pre-push, post-merge, post-checkout, post-commit)")

	// Check if claudit is in PATH
	if _, err := exec.LookPath("claudit"); err != nil {
		fmt.Println()
		fmt.Println("⚠ Warning: 'claudit' is not in your PATH.")
		fmt.Println("  The hook will not work until claudit is installed.")
		fmt.Println("  Install with: go install github.com/DanielJonesEB/claudit@latest")
	}

	fmt.Println()
	fmt.Println("Claudit is now configured! Conversations will be stored")
	fmt.Printf("as git notes on %s when commits are made via Claude Code.\n", notesRef)

	return nil
}

// determineNotesRef determines which git notes ref to use
func determineNotesRef() (string, error) {
	// Check if config file already exists
	if config.Exists() {
		existingCfg, err := config.Read()
		if err == nil && existingCfg.NotesRef != "" {
			// Reuse existing configuration
			return existingCfg.NotesRef, nil
		}
	}

	// If --notes-ref flag provided, use it
	if notesRefFlag != "" {
		if err := validateNotesRef(notesRefFlag); err != nil {
			return "", err
		}
		return notesRefFlag, nil
	}

	// Default to refs/notes/commits
	return config.DefaultNotesRef, nil
}

// validateNotesRef validates that the ref looks like a valid git notes ref
func validateNotesRef(ref string) error {
	if !strings.HasPrefix(ref, "refs/notes/") {
		return fmt.Errorf("invalid notes ref: %s (must start with refs/notes/)", ref)
	}
	return nil
}

// checkExistingNotes checks for existing notes at the given ref that were not written by Claudit
func checkExistingNotes(notesRef string) error {
	// List all notes at this ref
	cmd := exec.Command("git", "notes", "--ref", notesRef, "list")
	output, err := cmd.Output()
	if err != nil {
		// No notes exist yet - this is fine
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil
		}
		// Other error - also fine, ref might not exist
		return nil
	}

	// Parse the notes list
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Format: "note_sha commit_sha"
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		noteSHA := parts[0]

		// Read the note content
		noteCmd := exec.Command("git", "cat-file", "-p", noteSHA)
		noteContent, err := noteCmd.Output()
		if err != nil {
			continue // Skip notes we can't read
		}

		// Try to parse as a Claudit note
		_, err = storage.UnmarshalStoredConversation(noteContent)
		if err != nil {
			// This note is not a valid Claudit note
			return fmt.Errorf("existing notes found at %s that were not written by Claudit.\n"+
				"Use --notes-ref to specify a different location, e.g.:\n"+
				"  claudit init --notes-ref=refs/notes/claude-conversations", notesRef)
		}
	}

	return nil
}

// configureGitSettings configures git settings for notes visibility
func configureGitSettings(notesRef string) error {
	// Configure notes.displayRef so git log shows notes
	cmd := exec.Command("git", "config", "notes.displayRef", notesRef)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set notes.displayRef: %w", err)
	}

	// Configure notes.rewriteRef so notes follow commits during rebase
	cmd = exec.Command("git", "config", "notes.rewriteRef", notesRef)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to set notes.rewriteRef: %w", err)
	}

	return nil
}
