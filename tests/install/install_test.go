package install_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// findProjectRoot walks up from cwd looking for go.mod.
func findProjectRoot() string {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "."
}

func TestInstallScript(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available, skipping install script test")
	}

	root := findProjectRoot()
	scriptPath := filepath.Join(root, "scripts", "install.sh")

	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("install script not found at %s", scriptPath)
	}

	// Run a clean Debian container that:
	// 1. Installs curl (needed by install script)
	// 2. Runs the install script
	// 3. Verifies shiftlog is installed and reports a version
	cmd := exec.Command("docker", "run", "--rm",
		"-v", scriptPath+":/tmp/install.sh:ro",
		"debian:bookworm-slim",
		"bash", "-c",
		"apt-get update -qq && apt-get install -y -qq curl ca-certificates > /dev/null 2>&1 && bash /tmp/install.sh && shiftlog --version",
	)

	out, err := cmd.CombinedOutput()
	output := string(out)
	t.Logf("container output:\n%s", output)

	if err != nil {
		t.Fatalf("install script failed: %v", err)
	}

	if !strings.Contains(output, "shiftlog version") {
		t.Errorf("expected output to contain 'shiftlog version', got:\n%s", output)
	}

	// The installed binary should report a real version, not "dev"
	lines := strings.Split(strings.TrimSpace(output), "\n")
	lastLine := lines[len(lines)-1]
	if strings.Contains(lastLine, "dev") {
		t.Errorf("expected a release version, but got: %s", lastLine)
	}
}
