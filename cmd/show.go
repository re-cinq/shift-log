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

var showFull bool

var showCmd = &cobra.Command{
	Use:   "show [ref]",
	Short: "Show conversation history for a commit",
	Long: `Displays the Claude Code conversation history stored for a commit.

By default, shows only the conversation since the last commit (incremental view).
Use --full to see the complete session history.

If no ref is provided, shows the conversation for HEAD.

Examples:
  claudit show           # Show conversation since last commit
  claudit show --full    # Show full session history
  claudit show abc1234   # Show conversation for specific commit
  claudit show HEAD~1    # Show conversation for previous commit`,
	Args: cobra.MaximumNArgs(1),
	RunE: runShow,
}

func init() {
	showCmd.Flags().BoolVarP(&showFull, "full", "f", false, "Show full session history instead of incremental")
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

	// Find parent conversation boundary (unless --full is specified)
	var parentSHA string
	var lastEntryUUID string
	var isIncremental bool

	if !showFull {
		parentSHA, lastEntryUUID = findParentConversationBoundary(fullSHA, stored.SessionID)
		isIncremental = lastEntryUUID != ""
	}

	// Get entries to display
	var entries []claude.TranscriptEntry
	if isIncremental {
		entries = transcript.GetEntriesSince(lastEntryUUID)
	} else {
		entries = transcript.Entries
	}

	// Print header
	message, date, _ := git.GetCommitInfo(fullSHA)
	fmt.Printf("Conversation for %s (%s)\n", fullSHA[:7], date[:10])
	fmt.Printf("Commit: %s\n", message)

	if isIncremental {
		fmt.Printf("Showing: %d entries since %s\n", len(entries), parentSHA[:7])
	} else {
		fmt.Printf("Showing: %d entries (full session)\n", len(entries))
	}

	fmt.Println(strings.Repeat("─", 60))
	fmt.Println()

	// Render the entries
	renderer := claude.NewRenderer(os.Stdout)
	return renderer.RenderEntries(entries)
}

// findParentConversationBoundary finds the most recent parent commit with a conversation
// and returns its SHA and the last entry UUID from that conversation.
// Returns empty strings if no parent conversation is found or session IDs differ.
func findParentConversationBoundary(commitSHA, currentSessionID string) (parentSHA, lastEntryUUID string) {
	parents, err := git.GetParentCommits(commitSHA)
	if err != nil || len(parents) == 0 {
		return "", ""
	}

	// Check each parent for a conversation (use first one found)
	for _, parent := range parents {
		if !git.HasNote(parent) {
			continue
		}

		noteContent, err := git.GetNote(parent)
		if err != nil {
			continue
		}

		stored, err := storage.UnmarshalStoredConversation(noteContent)
		if err != nil {
			continue
		}

		// If session ID differs, treat as new session (show full)
		if stored.SessionID != currentSessionID {
			return "", ""
		}

		transcriptData, err := stored.GetTranscript()
		if err != nil {
			continue
		}

		transcript, err := claude.ParseTranscript(strings.NewReader(string(transcriptData)))
		if err != nil {
			continue
		}

		lastUUID := transcript.GetLastEntryUUID()
		if lastUUID != "" {
			return parent, lastUUID
		}
	}

	return "", ""
}
