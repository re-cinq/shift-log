package storage

import (
	"fmt"
	"strings"

	"github.com/re-cinq/claudit/internal/agent"
	agentclaude "github.com/re-cinq/claudit/internal/agent/claude"
	"github.com/re-cinq/claudit/internal/git"
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

// ParseTranscript decompresses the stored transcript and parses it into a Transcript.
func (sc *StoredConversation) ParseTranscript() (*agent.Transcript, error) {
	data, err := sc.GetTranscript()
	if err != nil {
		return nil, err
	}
	return agentclaude.ParseJSONLTranscript(strings.NewReader(string(data)))
}

// FindParentConversationBoundary finds the most recent parent commit with a conversation
// and returns its SHA and the last entry UUID from that conversation.
// Returns empty strings if no parent conversation is found or session IDs differ.
func FindParentConversationBoundary(commitSHA, currentSessionID string) (parentSHA, lastEntryUUID string) {
	parents, err := git.GetParentCommits(commitSHA)
	if err != nil || len(parents) == 0 {
		return "", ""
	}

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

		if stored.SessionID != currentSessionID {
			return "", ""
		}

		transcript, err := stored.ParseTranscript()
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
