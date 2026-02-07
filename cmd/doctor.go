package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/DanielJonesEB/claudit/internal/git"
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:     "doctor",
	Short:   "Diagnose claudit configuration issues",
	GroupID: "human",
	Long: `Checks the claudit configuration and reports any issues that might
prevent conversations from being stored.

This command checks:
- Git repository status
- Claude Code hook configuration
- Git hooks installation
- PATH configuration`,
	RunE: runDoctor,
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command, args []string) error {
	fmt.Println("Claudit Doctor")
	fmt.Println("==============")
	fmt.Println()

	hasErrors := false

	// Check 1: Git repository
	fmt.Print("Checking git repository... ")
	if !git.IsInsideWorkTree() {
		fmt.Println("FAIL")
		fmt.Println("  Not inside a git repository")
		hasErrors = true
	} else {
		repoRoot, _ := git.GetRepoRoot()
		fmt.Println("OK")
		fmt.Printf("  Repository: %s\n", repoRoot)
	}
	fmt.Println()

	// Check 2: claudit in PATH
	fmt.Print("Checking claudit in PATH... ")
	clauditPath, err := exec.LookPath("claudit")
	if err != nil {
		fmt.Println("FAIL")
		fmt.Println("  'claudit' is not in your PATH")
		fmt.Println("  Claude Code hooks will not be able to find claudit")
		fmt.Println("  Install with: go install github.com/DanielJonesEB/claudit@latest")
		hasErrors = true
	} else {
		fmt.Println("OK")
		fmt.Printf("  Found: %s\n", clauditPath)
	}
	fmt.Println()

	// Check 3: Claude settings file
	fmt.Print("Checking Claude Code hook configuration... ")
	repoRoot, _ := git.GetRepoRoot()
	if repoRoot == "" {
		fmt.Println("SKIP (not in git repo)")
	} else {
		settingsPath := filepath.Join(repoRoot, ".claude", "settings.local.json")
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			fmt.Println("FAIL")
			fmt.Println("  No .claude/settings.local.json found")
			fmt.Println("  Run 'claudit init' to configure")
			hasErrors = true
		} else {
			// Check hook format
			var settings map[string]interface{}
			if err := json.Unmarshal(data, &settings); err != nil {
				fmt.Println("FAIL")
				fmt.Printf("  Invalid JSON in settings file: %v\n", err)
				hasErrors = true
			} else {
				// Check for correct nested structure
				hooks, hasHooks := settings["hooks"].(map[string]interface{})
				if !hasHooks {
					fmt.Println("FAIL")
					fmt.Println("  Missing 'hooks' key in settings")
					fmt.Println("  Run 'claudit init' to fix")
					hasErrors = true
				} else {
					postToolUse, hasPostToolUse := hooks["PostToolUse"]
					if !hasPostToolUse {
						fmt.Println("FAIL")
						fmt.Println("  Missing 'hooks.PostToolUse' configuration")
						fmt.Println("  Run 'claudit init' to fix")
						hasErrors = true
					} else {
						// Check for claudit store command
						foundClaudit := hasClauditCommand(postToolUse, "claudit store")
						if !foundClaudit {
							fmt.Println("FAIL")
							fmt.Println("  'claudit store' hook not found in PostToolUse")
							fmt.Println("  Run 'claudit init' to fix")
							hasErrors = true
						} else {
							fmt.Println("OK")
							fmt.Printf("  Found PostToolUse hook configuration\n")
						}

						// Check for SessionStart hook
						sessionStart, hasSessionStart := hooks["SessionStart"]
						if !hasSessionStart || !hasClauditCommand(sessionStart, "claudit session-start") {
							fmt.Println("  WARN: Missing SessionStart hook (manual commit capture won't work)")
							fmt.Println("        Run 'claudit init' to add")
						} else {
							fmt.Println("  Found SessionStart hook")
						}

						// Check for SessionEnd hook
						sessionEnd, hasSessionEnd := hooks["SessionEnd"]
						if !hasSessionEnd || !hasClauditCommand(sessionEnd, "claudit session-end") {
							fmt.Println("  WARN: Missing SessionEnd hook (manual commit capture won't work)")
							fmt.Println("        Run 'claudit init' to add")
						} else {
							fmt.Println("  Found SessionEnd hook")
						}
					}
				}
			}
		}
	}
	fmt.Println()

	// Check 4: Git hooks
	fmt.Print("Checking git hooks... ")
	if repoRoot == "" {
		fmt.Println("SKIP (not in git repo)")
	} else {
		gitDir, _ := git.EnsureGitDir()
		if gitDir == "" {
			fmt.Println("FAIL")
			fmt.Println("  Could not find .git directory")
			hasErrors = true
		} else {
			missingHooks := []string{}
			for _, hook := range []string{"pre-push", "post-merge", "post-checkout", "post-commit"} {
				hookPath := filepath.Join(gitDir, "hooks", hook)
				data, err := os.ReadFile(hookPath)
				if err != nil {
					missingHooks = append(missingHooks, hook)
				} else if !strings.Contains(string(data), "claudit") {
					missingHooks = append(missingHooks, hook+" (no claudit)")
				}
			}
			if len(missingHooks) > 0 {
				fmt.Println("FAIL")
				fmt.Printf("  Missing or incomplete hooks: %v\n", missingHooks)
				fmt.Println("  Run 'claudit init' to fix")
				hasErrors = true
			} else {
				fmt.Println("OK")
				fmt.Println("  All git hooks installed")
			}
		}
	}
	fmt.Println()

	// Summary
	if hasErrors {
		fmt.Println("Issues found. Run 'claudit init' to fix configuration.")
		return fmt.Errorf("configuration issues detected")
	}

	fmt.Println("All checks passed! Claudit is properly configured.")
	return nil
}

// hasClauditCommand checks if a hook list contains a specific claudit command
func hasClauditCommand(hookConfig interface{}, command string) bool {
	hookList, ok := hookConfig.([]interface{})
	if !ok {
		return false
	}
	for _, h := range hookList {
		hookMap, _ := h.(map[string]interface{})
		hookCmds, _ := hookMap["hooks"].([]interface{})
		for _, hc := range hookCmds {
			hcMap, _ := hc.(map[string]interface{})
			if cmd, ok := hcMap["command"].(string); ok {
				if strings.Contains(cmd, command) {
					return true
				}
			}
		}
	}
	return false
}
