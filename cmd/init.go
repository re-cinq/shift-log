package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/DanielJonesEB/claudit/internal/claude"
	"github.com/DanielJonesEB/claudit/internal/cli"
	"github.com/DanielJonesEB/claudit/internal/git"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:     "init",
	Short:   "Initialize claudit in the current repository",
	GroupID: "human",
	Long: `Configures the current git repository for conversation capture.

This command:
- Uses refs/notes/claude-conversations for note storage
- Creates/updates .claude/settings.local.json with PostToolUse hook
- Installs git hooks for automatic note syncing
- Configures git settings for notes visibility`,
	RunE: runInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
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

	// Configure git settings for notes visibility
	cli.LogDebug("init: configuring git settings for notes ref %s", git.NotesRef)
	if err := configureGitSettings(git.NotesRef); err != nil {
		return fmt.Errorf("failed to configure git settings: %w", err)
	}

	fmt.Printf("✓ Configured notes ref: %s\n", git.NotesRef)
	fmt.Println("✓ Configured git notes settings (displayRef, rewriteRef)")

	// Configure Claude hooks
	cli.LogDebug("init: configuring Claude hooks")
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
	cli.LogDebug("init: installing git hooks")
	gitDir, err := git.EnsureGitDir()
	if err != nil {
		return fmt.Errorf("failed to find git directory: %w", err)
	}

	if err := git.InstallAllHooks(gitDir); err != nil {
		return fmt.Errorf("failed to install git hooks: %w", err)
	}

	fmt.Println("✓ Installed git hooks (pre-push, post-merge, post-checkout, post-commit)")

	// Add .claudit/ to .gitignore
	cli.LogDebug("init: ensuring .claudit/ is in .gitignore")
	if err := ensureGitignoreEntry(repoRoot, ".claudit/"); err != nil {
		return fmt.Errorf("failed to update .gitignore: %w", err)
	}

	fmt.Println("✓ Added .claudit/ to .gitignore")

	// Check if claudit is in PATH
	if _, err := exec.LookPath("claudit"); err != nil {
		fmt.Println()
		fmt.Println("⚠ Warning: 'claudit' is not in your PATH.")
		fmt.Println("  The hook will not work until claudit is installed.")
		fmt.Println("  Install with: go install github.com/DanielJonesEB/claudit@latest")
	}

	fmt.Println()
	fmt.Println("Claudit is now configured! Conversations will be stored")
	fmt.Printf("as git notes on %s when commits are made via Claude Code.\n", git.NotesRef)

	return nil
}

// ensureGitignoreEntry adds an entry to the repo's .gitignore if not already present.
func ensureGitignoreEntry(repoRoot, entry string) error {
	gitignorePath := filepath.Join(repoRoot, ".gitignore")

	// Check if the entry already exists
	if f, err := os.Open(gitignorePath); err == nil {
		defer func() { _ = f.Close() }()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == entry {
				cli.LogDebug("init: .gitignore already contains %s", entry)
				return nil
			}
		}
	}

	// Append the entry
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	// Ensure we start on a new line if the file doesn't end with one
	info, err := f.Stat()
	if err != nil {
		return err
	}
	prefix := ""
	if info.Size() > 0 {
		// Read last byte to check for trailing newline
		buf := make([]byte, 1)
		if rf, err := os.Open(gitignorePath); err == nil {
			if _, err := rf.Seek(-1, 2); err == nil {
				if _, err := rf.Read(buf); err == nil {
					if buf[0] != '\n' {
						prefix = "\n"
					}
				}
			}
			_ = rf.Close()
		}
	}

	_, err = fmt.Fprintf(f, "%s%s\n", prefix, entry)
	return err
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
