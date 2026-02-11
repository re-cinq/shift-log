package testutil

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
)

var binaryPath string

// BuildBinary builds the claudit binary for testing
func BuildBinary() error {
	// Build to a temp location
	tmpDir, err := os.MkdirTemp("", "claudit-test-*")
	if err != nil {
		return err
	}

	binaryPath = filepath.Join(tmpDir, "claudit")

	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir = findProjectRoot()
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")

	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	// Install mock agent binaries so resume tests don't launch real TUIs.
	// The binary dir is first in PATH, so these shadow any system-installed agents.
	for _, name := range []string{"claude", "opencode", "gemini"} {
		mockPath := filepath.Join(tmpDir, name)
		_ = os.WriteFile(mockPath, []byte("#!/bin/sh\nexit 0\n"), 0755)
	}

	return nil
}

// BinaryPath returns the path to the built binary
func BinaryPath() string {
	return binaryPath
}

// CleanupBinary removes the built binary
func CleanupBinary() {
	if binaryPath != "" {
		_ = os.RemoveAll(filepath.Dir(binaryPath))
	}
}

// RunClaudit runs the claudit binary with the given arguments
func RunClaudit(args ...string) (string, string, error) {
	return RunClauditWithStdin("", args...)
}

// RunClauditWithStdin runs claudit with stdin input
func RunClauditWithStdin(stdin string, args ...string) (string, string, error) {
	cmd := exec.Command(binaryPath, args...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if stdin != "" {
		cmd.Stdin = bytes.NewBufferString(stdin)
	}

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// RunClauditInDir runs claudit in a specific directory
func RunClauditInDir(dir string, args ...string) (string, string, error) {
	return RunClauditInDirWithStdin(dir, "", args...)
}

// RunClauditInDirWithStdin runs claudit in a specific directory with stdin
func RunClauditInDirWithStdin(dir, stdin string, args ...string) (string, string, error) {
	cmd := exec.Command(binaryPath, args...)
	cmd.Dir = dir
	// Add binary directory to PATH so hooks can find claudit
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(binaryPath)+":"+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if stdin != "" {
		cmd.Stdin = bytes.NewBufferString(stdin)
	}

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// RunClauditInDirWithEnv runs claudit in a specific directory with custom env vars
func RunClauditInDirWithEnv(dir string, extraEnv []string, args ...string) (string, string, error) {
	return RunClauditInDirWithEnvAndStdin(dir, extraEnv, "", args...)
}

// RunClauditInDirWithEnvAndStdin runs claudit with custom env vars and stdin
func RunClauditInDirWithEnvAndStdin(dir string, extraEnv []string, stdin string, args ...string) (string, string, error) {
	cmd := exec.Command(binaryPath, args...)
	cmd.Dir = dir

	// Start with basic env
	env := append(os.Environ(), "PATH="+filepath.Dir(binaryPath)+":"+os.Getenv("PATH"))
	// Add extra env vars
	env = append(env, extraEnv...)
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if stdin != "" {
		cmd.Stdin = bytes.NewBufferString(stdin)
	}

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func findProjectRoot() string {
	// Walk up from current directory looking for go.mod
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}
