package git

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrNotGitRepo is returned when an operation requires a git repository
var ErrNotGitRepo = errors.New("not inside a git repository")

// RunGitCommand executes a git command and returns the trimmed output.
// This is a helper to avoid repeating the exec.Command + TrimSpace pattern.
func RunGitCommand(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// IsInsideWorkTree returns true if the current directory is inside a git repository
func IsInsideWorkTree() bool {
	output, err := RunGitCommand("rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false
	}
	return output == "true"
}

// RequireGitRepo returns ErrNotGitRepo if not inside a git repository.
// Use this at the start of commands that require a git repository.
func RequireGitRepo() error {
	if !IsInsideWorkTree() {
		return ErrNotGitRepo
	}
	return nil
}

// GetRepoRoot returns the root directory of the git repository
func GetRepoRoot() (string, error) {
	return RunGitCommand("rev-parse", "--show-toplevel")
}

// GetCurrentBranch returns the name of the current branch
func GetCurrentBranch() (string, error) {
	return RunGitCommand("rev-parse", "--abbrev-ref", "HEAD")
}

// GetHeadCommit returns the SHA of HEAD
func GetHeadCommit() (string, error) {
	return RunGitCommand("rev-parse", "HEAD")
}

// EnsureGitDir returns the path to the .git directory, handling worktrees
func EnsureGitDir() (string, error) {
	root, err := GetRepoRoot()
	if err != nil {
		return "", err
	}

	gitDir := filepath.Join(root, ".git")
	info, err := os.Stat(gitDir)
	if err != nil {
		return "", err
	}

	// If .git is a file, this is a worktree - read the actual git dir path
	if !info.IsDir() {
		content, err := os.ReadFile(gitDir)
		if err != nil {
			return "", err
		}
		// Format: "gitdir: /path/to/actual/.git/worktrees/name"
		parts := strings.SplitN(string(content), ": ", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[1]), nil
		}
	}

	return gitDir, nil
}

// ResolveRef resolves a git reference (branch, tag, SHA, relative) to a full commit SHA
func ResolveRef(ref string) (string, error) {
	return RunGitCommand("rev-parse", ref)
}

// HasUncommittedChanges returns true if there are uncommitted changes in the working directory
func HasUncommittedChanges() (bool, error) {
	output, err := RunGitCommand("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return len(output) > 0, nil
}

// Checkout checks out a commit or branch
func Checkout(ref string) error {
	cmd := exec.Command("git", "checkout", ref)
	return cmd.Run()
}

// GetParentCommits returns the parent commit SHA(s) for a given commit
func GetParentCommits(commitSHA string) ([]string, error) {
	output, err := RunGitCommand("rev-parse", commitSHA+"^@")
	if err != nil {
		// No parents (initial commit) - not an error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 128 {
			return nil, nil
		}
		return nil, err
	}

	if output == "" {
		return nil, nil
	}

	parents := strings.Split(output, "\n")
	var result []string
	for _, p := range parents {
		if p != "" {
			result = append(result, p)
		}
	}
	return result, nil
}

// GetCommitInfo returns the commit message and author date for a commit
func GetCommitInfo(commitSHA string) (message string, date string, err error) {
	// Get commit message (first line)
	message, err = RunGitCommand("log", "-1", "--format=%s", commitSHA)
	if err != nil {
		return "", "", err
	}

	// Get commit date
	date, err = RunGitCommand("log", "-1", "--format=%ci", commitSHA)
	if err != nil {
		return "", "", err
	}

	return message, date, nil
}
