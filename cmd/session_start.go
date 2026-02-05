package cmd

import (
	"time"

	"github.com/DanielJonesEB/claudit/internal/cli"
	"github.com/DanielJonesEB/claudit/internal/session"
	"github.com/spf13/cobra"
)

// SessionStartInput represents the SessionStart hook JSON input from Claude Code
type SessionStartInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
}

var sessionStartCmd = &cobra.Command{
	Use:   "session-start",
	Short: "Handle Claude Code SessionStart hook",
	Long: `Reads SessionStart hook JSON from stdin and records the active session.

This command is designed to be called by Claude Code's SessionStart hook.`,
	RunE: runSessionStart,
}

func init() {
	rootCmd.AddCommand(sessionStartCmd)
}

func runSessionStart(cmd *cobra.Command, args []string) error {
	var hook SessionStartInput
	if err := cli.ReadHookInput(&hook); err != nil {
		return nil // Exit silently to not disrupt workflow
	}

	// Validate required fields
	if hook.SessionID == "" || hook.TranscriptPath == "" {
		cli.LogWarning("missing required fields in hook data")
		return nil
	}

	// Create active session record
	activeSession := &session.ActiveSession{
		SessionID:      hook.SessionID,
		TranscriptPath: hook.TranscriptPath,
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		ProjectPath:    hook.Cwd,
	}

	// Write active session file
	if err := session.WriteActiveSession(activeSession); err != nil {
		// Log but don't fail - don't disrupt user's workflow
		cli.LogWarning("failed to write active session: %v", err)
		return nil
	}

	cli.LogInfo("session started: %s", hook.SessionID[:8])
	return nil
}
