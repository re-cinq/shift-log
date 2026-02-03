package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/DanielJonesEB/claudit/internal/claude"
	"github.com/DanielJonesEB/claudit/internal/git"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize claudit in the current repository",
	Long: `Configures the current git repository for conversation capture.

This command:
- Creates/updates .claude/settings.local.json with PostToolUse hook
- Installs git hooks for automatic note syncing`,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
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
	fmt.Println()
	fmt.Println("Claudit is now configured! Conversations will be stored")
	fmt.Println("as git notes when commits are made via Claude Code.")

	return nil
}
