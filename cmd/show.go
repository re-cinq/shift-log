package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/DanielJonesEB/claudit/internal/claude"
	"github.com/DanielJonesEB/claudit/internal/git"
	"github.com/DanielJonesEB/claudit/internal/storage"
	"github.com/spf13/cobra"
)

var showCmd = &cobra.Command{
	Use:   "show [ref]",
	Short: "Show conversation history for a commit",
	Long: `Displays the Claude Code conversation history stored for a commit.

If no ref is provided, shows the conversation for HEAD.

Examples:
  claudit show           # Show conversation for HEAD
  claudit show abc1234   # Show conversation for specific commit
  claudit show HEAD~1    # Show conversation for previous commit
  claudit show main      # Show conversation for branch tip`,
	Args: cobra.MaximumNArgs(1),
	RunE: runShow,
}

func init() {
	rootCmd.AddCommand(showCmd)
}

func runShow(cmd *cobra.Command, args []string) error {
	// Verify we're in a git repository
	if !git.IsInsideWorkTree() {
		return fmt.Errorf("not inside a git repository")
	}

	// Determine the ref to show
	ref := "HEAD"
	if len(args) > 0 {
		ref = args[0]
	}

	// Resolve the reference to a full SHA
	fullSHA, err := git.ResolveRef(ref)
	if err != nil {
		return fmt.Errorf("could not resolve reference '%s': not a valid commit", ref)
	}

	// Check if the commit has a conversation
	if !git.HasNote(fullSHA) {
		return fmt.Errorf("no conversation found for commit %s", fullSHA[:7])
	}

	// Get the note content
	noteContent, err := git.GetNote(fullSHA)
	if err != nil {
		return fmt.Errorf("could not read conversation: %w", err)
	}

	// Parse the stored conversation
	stored, err := storage.UnmarshalStoredConversation(noteContent)
	if err != nil {
		return fmt.Errorf("could not parse conversation: %w", err)
	}

	// Decompress the transcript
	transcriptData, err := stored.GetTranscript()
	if err != nil {
		return fmt.Errorf("could not decompress conversation: %w", err)
	}

	// Parse the transcript
	transcript, err := claude.ParseTranscript(strings.NewReader(string(transcriptData)))
	if err != nil {
		return fmt.Errorf("could not parse transcript: %w", err)
	}

	// Print header
	message, date, _ := git.GetCommitInfo(fullSHA)
	fmt.Printf("Conversation for %s (%s)\n", fullSHA[:7], date[:10])
	fmt.Printf("Commit: %s\n", message)
	fmt.Printf("Messages: %d\n", stored.MessageCount)
	fmt.Println(strings.Repeat("─", 60))
	fmt.Println()

	// Render the transcript
	renderer := claude.NewRenderer(os.Stdout)
	return renderer.RenderTranscript(transcript)
}
