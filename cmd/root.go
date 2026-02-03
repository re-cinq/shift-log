package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var version = "0.3.0"

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
	rootCmd.Version = version
	rootCmd.SetVersionTemplate(fmt.Sprintf("claudit version %s\n", version))
}
