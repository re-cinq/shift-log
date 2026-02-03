package git

import (
	"os/exec"
	"strings"

	"github.com/DanielJonesEB/claudit/internal/config"
)

// GetNotesRef returns the configured git notes ref
func GetNotesRef() string {
	cfg, err := config.Read()
	if err != nil {
		// Fallback to default ref on error
		return config.DefaultNotesRef
	}
	return cfg.NotesRef
}

// AddNote adds a note to a commit
func AddNote(commitSHA string, content []byte) error {
	ref := GetNotesRef()
	cmd := exec.Command("git", "notes", "--ref", ref, "add", "-f", "-m", string(content), commitSHA)
	return cmd.Run()
}

// GetNote retrieves a note from a commit
func GetNote(commitSHA string) ([]byte, error) {
	ref := GetNotesRef()
	cmd := exec.Command("git", "notes", "--ref", ref, "show", commitSHA)
	return cmd.Output()
}

// HasNote checks if a commit has a conversation note
func HasNote(commitSHA string) bool {
	ref := GetNotesRef()
	cmd := exec.Command("git", "notes", "--ref", ref, "show", commitSHA)
	return cmd.Run() == nil
}

// ListCommitsWithNotes returns a list of commit SHAs that have conversation notes
func ListCommitsWithNotes() ([]string, error) {
	ref := GetNotesRef()
	cmd := exec.Command("git", "notes", "--ref", ref, "list")
	output, err := cmd.Output()
	if err != nil {
		// No notes exist yet - this is not an error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}

	var commits []string
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		// Format: "note_sha commit_sha"
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			commits = append(commits, parts[1])
		}
	}

	return commits, nil
}

// PushNotes pushes notes to the remote
func PushNotes(remote string) error {
	ref := GetNotesRef()
	// Use --no-verify to prevent pre-push hook from triggering recursively
	cmd := exec.Command("git", "push", "--no-verify", remote, ref)
	return cmd.Run()
}

// FetchNotes fetches notes from the remote
func FetchNotes(remote string) error {
	ref := GetNotesRef()
	cmd := exec.Command("git", "fetch", remote, ref+":"+ref)
	return cmd.Run()
}
