package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// HookType represents the type of git hook
type HookType string

const (
	HookPrePush      HookType = "pre-push"
	HookPostMerge    HookType = "post-merge"
	HookPostCheckout HookType = "post-checkout"
	HookPostCommit   HookType = "post-commit"
)

// clauditMarker identifies claudit-managed hook sections
const clauditMarker = "# claudit-managed"

// InstallHook installs or updates a git hook with claudit commands
func InstallHook(gitDir string, hookType HookType, command string) error {
	hooksDir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return err
	}

	hookPath := filepath.Join(hooksDir, string(hookType))

	// Read existing hook content if any
	var existingContent string
	if data, err := os.ReadFile(hookPath); err == nil {
		existingContent = string(data)
	}

	// Build the claudit section
	clauditSection := fmt.Sprintf(`
%s start
%s
%s end
`, clauditMarker, command, clauditMarker)

	var newContent string

	if existingContent == "" {
		// New hook file
		newContent = "#!/bin/sh\n" + clauditSection
	} else if strings.Contains(existingContent, clauditMarker) {
		// Update existing claudit section
		newContent = replaceClauditSection(existingContent, clauditSection)
	} else {
		// Append to existing hook
		newContent = existingContent + "\n" + clauditSection
	}

	return os.WriteFile(hookPath, []byte(newContent), 0755)
}

// replaceClauditSection replaces the claudit-managed section in hook content
func replaceClauditSection(content, newSection string) string {
	startMarker := clauditMarker + " start"
	endMarker := clauditMarker + " end"

	startIdx := strings.Index(content, startMarker)
	endIdx := strings.Index(content, endMarker)

	if startIdx == -1 || endIdx == -1 {
		return content + "\n" + newSection
	}

	// Find the newline before start marker (if any)
	lineStart := startIdx
	if lineStart > 0 {
		lineStart = strings.LastIndex(content[:startIdx], "\n")
		if lineStart == -1 {
			lineStart = 0
		}
	}

	// Find the newline after end marker
	lineEnd := endIdx + len(endMarker)
	if lineEnd < len(content) && content[lineEnd] == '\n' {
		lineEnd++
	}

	return content[:lineStart] + newSection + content[lineEnd:]
}

// InstallAllHooks installs all claudit git hooks.
// It resolves the absolute path to the running claudit binary so hooks work
// even when the shell environment strips PATH (e.g. Codex CLI sandbox).
func InstallAllHooks(gitDir string) error {
	bin, err := resolveClauditBinary()
	if err != nil {
		return fmt.Errorf("failed to resolve claudit binary path: %w", err)
	}

	hooks := map[HookType]string{
		HookPrePush:      bin + " sync push",
		HookPostMerge:    bin + " sync pull\n" + bin + " remap",
		HookPostCheckout: bin + " sync pull",
		HookPostCommit:   bin + " store --manual",
	}

	for hookType, command := range hooks {
		if err := InstallHook(gitDir, hookType, command); err != nil {
			return fmt.Errorf("failed to install %s hook: %w", hookType, err)
		}
	}

	return nil
}

// resolveClauditBinary returns the absolute path to the running claudit binary.
func resolveClauditBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}
