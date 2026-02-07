package cmd

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is set at build time via ldflags.
// If not set (e.g. go install), we fall back to the module version
// embedded by the Go toolchain.
var version = "dev"

var rootCmd = &cobra.Command{
	Use:   "claudit",
	Short: "Store and resume Claude Code conversations as Git Notes",
	Long: `Claudit captures Claude Code conversation history and stores it as Git Notes
attached to commits. This enables teams to preserve AI-assisted development
context alongside their code and resume interrupted sessions.`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	if version == "dev" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
	}
	rootCmd.Version = version
	rootCmd.SetVersionTemplate(fmt.Sprintf("claudit version %s\n", version))

	// Add command groups
	rootCmd.AddGroup(
		&cobra.Group{ID: "human", Title: "Commands for humans:"},
		&cobra.Group{ID: "hooks", Title: "Commands mostly used by hooks:"},
	)
}
