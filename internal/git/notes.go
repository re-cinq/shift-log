package git

import (
	"errors"
	"os/exec"
	"strings"
)

// NotesRef is the git notes ref used to store conversation notes.
// A custom ref keeps git log clean and avoids collisions with other notes.
const NotesRef = "refs/notes/claude-conversations"

// NotesTrackingRef is the ref used to hold fetched remote notes before merging.
const NotesTrackingRef = "refs/notes/claude-conversations-remote"

// ErrNonFastForward is returned when a push fails because the remote has diverged.
var ErrNonFastForward = errors.New("non-fast-forward update: remote notes have diverged, run 'claudit sync pull' first")

// AddNote adds a note to a commit.
// Content is piped via stdin (-F -) to avoid ARG_MAX limits on large transcripts.
func AddNote(commitSHA string, content []byte) error {
	cmd := exec.Command("git", "notes", "--ref", NotesRef, "add", "-f", "-F", "-", commitSHA)
	cmd.Stdin = strings.NewReader(string(content))
	return cmd.Run()
}

// GetNote retrieves a note from a commit
func GetNote(commitSHA string) ([]byte, error) {
	cmd := exec.Command("git", "notes", "--ref", NotesRef, "show", commitSHA)
	return cmd.Output()
}

// HasNote checks if a commit has a conversation note
func HasNote(commitSHA string) bool {
	cmd := exec.Command("git", "notes", "--ref", NotesRef, "show", commitSHA)
	return cmd.Run() == nil
}

// ListCommitsWithNotes returns a list of commit SHAs that have conversation notes
// sorted in reverse chronological order (matching git log)
func ListCommitsWithNotes() ([]string, error) {
	cmd := exec.Command("git", "notes", "--ref", NotesRef, "list")
	output, err := cmd.Output()
	if err != nil {
		// No notes exist yet - this is not an error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}

	// Build a set of commits with notes
	commitSet := make(map[string]bool)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Format: "note_sha commit_sha"
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			commitSet[parts[1]] = true
		}
	}

	if len(commitSet) == 0 {
		return nil, nil
	}

	// Use git rev-list to sort commits in reverse chronological order
	// --all ensures we see all branches, --topo-order maintains parent-child relationships
	cmd = exec.Command("git", "rev-list", "--all", "--topo-order")
	output, err = cmd.Output()
	if err != nil {
		return nil, err
	}

	// Filter to only commits that have notes, preserving git's order
	var commits []string
	for _, sha := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if sha == "" {
			continue
		}
		if commitSet[sha] {
			commits = append(commits, sha)
		}
	}

	return commits, nil
}

// PushNotes pushes notes to the remote.
// Returns ErrNonFastForward if the remote has diverged.
func PushNotes(remote string) error {
	// Use --no-verify to prevent pre-push hook from triggering recursively
	cmd := exec.Command("git", "push", "--no-verify", remote, NotesRef)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "non-fast-forward") ||
			strings.Contains(string(output), "rejected") ||
			strings.Contains(string(output), "fetch first") {
			return ErrNonFastForward
		}
		return err
	}
	return nil
}

// FetchNotesToTracking fetches remote notes to the tracking ref without
// touching the local notes ref. This is the first step of the
// fetch-then-merge sync flow.
func FetchNotesToTracking(remote string) error {
	cmd := exec.Command("git", "fetch", remote, NotesRef+":"+NotesTrackingRef)
	return cmd.Run()
}

// MergeNotes merges the tracking ref into the local notes ref using
// git notes merge. The cat_sort_uniq strategy concatenates notes when
// two developers have annotated the same commit SHA.
func MergeNotes() error {
	cmd := exec.Command("git", "notes", "--ref", NotesRef, "merge", "--strategy=cat_sort_uniq", NotesTrackingRef)
	return cmd.Run()
}

// FetchNotes fetches remote notes and merges them into the local ref.
// This is the high-level operation used by sync pull.
func FetchNotes(remote string) error {
	if err := FetchNotesToTracking(remote); err != nil {
		return err
	}
	return MergeNotes()
}
