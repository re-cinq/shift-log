package cmd

import (
	"fmt"

	"github.com/DanielJonesEB/claudit/internal/git"
	"github.com/spf13/cobra"
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync conversation notes with remote",
	Long:  `Sync git notes containing conversations with the remote repository.`,
}

var syncPushCmd = &cobra.Command{
	Use:   "push",
	Short: "Push conversation notes to remote",
	RunE:  runSyncPush,
}

var syncPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull conversation notes from remote",
	RunE:  runSyncPull,
}

var syncRemote string

func init() {
	rootCmd.AddCommand(syncCmd)
	syncCmd.AddCommand(syncPushCmd)
	syncCmd.AddCommand(syncPullCmd)

	syncCmd.PersistentFlags().StringVar(&syncRemote, "remote", "origin", "Remote to sync with")
}

func runSyncPush(cmd *cobra.Command, args []string) error {
	if !git.IsInsideWorkTree() {
		return fmt.Errorf("not a git repository")
	}

	if err := git.PushNotes(syncRemote); err != nil {
		// Don't fail if there are no notes to push or remote doesn't exist
		logWarning("could not push notes: %v", err)
		return nil
	}

	fmt.Printf("Pushed conversation notes to %s\n", syncRemote)
	return nil
}

func runSyncPull(cmd *cobra.Command, args []string) error {
	if !git.IsInsideWorkTree() {
		return fmt.Errorf("not a git repository")
	}

	if err := git.FetchNotes(syncRemote); err != nil {
		// Don't fail if there are no notes to fetch or remote doesn't exist
		logWarning("could not fetch notes: %v", err)
		return nil
	}

	fmt.Printf("Fetched conversation notes from %s\n", syncRemote)
	return nil
}
