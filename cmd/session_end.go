package cmd

import (
	"github.com/DanielJonesEB/claudit/internal/cli"
	"github.com/DanielJonesEB/claudit/internal/session"
	"github.com/spf13/cobra"
)

// SessionEndInput represents the SessionEnd hook JSON input from Claude Code
type SessionEndInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Reason         string `json:"reason"`
}

var sessionEndCmd = &cobra.Command{
	Use:   "session-end",
	Short: "Handle Claude Code SessionEnd hook",
	Long: `Reads SessionEnd hook JSON from stdin and clears the active session.

This command is designed to be called by Claude Code's SessionEnd hook.`,
	RunE: runSessionEnd,
}

func init() {
	rootCmd.AddCommand(sessionEndCmd)
}

func runSessionEnd(cmd *cobra.Command, args []string) error {
	var hook SessionEndInput
	if err := cli.ReadHookInput(&hook); err != nil {
		return nil // Exit silently to not disrupt workflow
	}

	// Clear active session file
	if err := session.ClearActiveSession(); err != nil {
		// Log but don't fail - don't disrupt user's workflow
		cli.LogWarning("failed to clear active session: %v", err)
		return nil
	}

	cli.LogInfo("session ended: %s (%s)", hook.SessionID[:8], hook.Reason)
	return nil
}
