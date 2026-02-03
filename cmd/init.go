package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/DanielJonesEB/claudit/internal/claude"
	"github.com/DanielJonesEB/claudit/internal/config"
	"github.com/DanielJonesEB/claudit/internal/git"
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
- Prompts for git notes ref choice (or uses --notes-ref flag)
- Creates/updates .claude/settings.local.json with PostToolUse hook
- Installs git hooks for automatic note syncing
- Configures git settings for notes visibility`,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringVar(&notesRefFlag, "notes-ref", "", "Git notes ref to use (refs/notes/commits or refs/notes/claude-conversations)")
}

func runInit(cmd *cobra.Command, args []string) error {
	// Verify we're in a git repository
	if !git.IsInsideWorkTree() {
		return fmt.Errorf("not a git repository")
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

	if err := claude.WriteSettings(claudeDir, settings); err != nil {
		return fmt.Errorf("failed to write Claude settings: %w", err)
	}

	fmt.Println("✓ Configured Claude PostToolUse hook")

	// Install git hooks
	gitDir, err := git.EnsureGitDir()
	if err != nil {
		return fmt.Errorf("failed to find git directory: %w", err)
	}

	if err := git.InstallAllHooks(gitDir); err != nil {
		return fmt.Errorf("failed to install git hooks: %w", err)
	}

	fmt.Println("✓ Installed git hooks (pre-push, post-merge, post-checkout)")

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

	// Interactive prompt
	return promptForNotesRef()
}

// validateNotesRef validates that the ref is one of the allowed values
func validateNotesRef(ref string) error {
	if ref != config.DefaultNotesRef && ref != config.CustomNotesRef {
		return fmt.Errorf("invalid notes ref: %s (must be %s or %s)",
			ref, config.DefaultNotesRef, config.CustomNotesRef)
	}
	return nil
}

// promptForNotesRef prompts the user to choose a git notes ref
func promptForNotesRef() (string, error) {
	fmt.Println()
	fmt.Println("Which git notes ref should claudit use?")
	fmt.Println()
	fmt.Printf("  1. %s (default - works with standard git commands)\n", config.DefaultNotesRef)
	fmt.Printf("  2. %s (custom - separate namespace)\n", config.CustomNotesRef)
	fmt.Println()
	fmt.Print("Enter choice [1]: ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read input: %w", err)
	}

	choice := strings.TrimSpace(input)
	if choice == "" || choice == "1" {
		return config.DefaultNotesRef, nil
	} else if choice == "2" {
		return config.CustomNotesRef, nil
	}

	return "", fmt.Errorf("invalid choice: %s (must be 1 or 2)", choice)
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
