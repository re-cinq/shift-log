package storage

import (
	"fmt"
	"strings"

	"github.com/DanielJonesEB/claudit/internal/claude"
	"github.com/DanielJonesEB/claudit/internal/git"
)

// GetStoredConversation retrieves and parses a stored conversation from a commit's git note.
// Returns nil, nil if no note exists for the commit.
func GetStoredConversation(commitSHA string) (*StoredConversation, error) {
	if !git.HasNote(commitSHA) {
		return nil, nil
	}

	noteContent, err := git.GetNote(commitSHA)
	if err != nil {
		return nil, fmt.Errorf("could not read conversation: %w", err)
	}

	stored, err := UnmarshalStoredConversation(noteContent)
	if err != nil {
		return nil, fmt.Errorf("could not parse conversation: %w", err)
	}

	return stored, nil
}

// FindParentConversationBoundary finds the most recent parent commit with a conversation
// and returns its SHA and the last entry UUID from that conversation.
// Returns empty strings if no parent conversation is found or session IDs differ.
func FindParentConversationBoundary(commitSHA, currentSessionID string) (parentSHA, lastEntryUUID string) {
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

		stored, err := UnmarshalStoredConversation(noteContent)
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
