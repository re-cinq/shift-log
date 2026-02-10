package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentclaude "github.com/re-cinq/claudit/internal/agent/claude"
)

func TestWriteAndReadActiveSession(t *testing.T) {
	// Create temp directory to act as git repo root
	tmpDir, err := os.MkdirTemp("", "session-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize as git repo
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Change to temp directory for the test
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Create session to write
	session := &ActiveSession{
		SessionID:      "test-session-123",
		TranscriptPath: "/home/user/.claude/projects/-test/test-session-123.jsonl",
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		ProjectPath:    tmpDir,
	}

	// Write session
	err = WriteActiveSession(session)
	if err != nil {
		t.Fatalf("WriteActiveSession failed: %v", err)
	}

	// Verify file was created
	sessionPath := filepath.Join(tmpDir, ".claudit", "active-session.json")
	if _, err := os.Stat(sessionPath); os.IsNotExist(err) {
		t.Error("active-session.json was not created")
	}

	// Read session back
	readSession, err := ReadActiveSession()
	if err != nil {
		t.Fatalf("ReadActiveSession failed: %v", err)
	}

	if readSession == nil {
		t.Fatal("ReadActiveSession returned nil")
	}

	if readSession.SessionID != session.SessionID {
		t.Errorf("SessionID mismatch: got %s, want %s", readSession.SessionID, session.SessionID)
	}

	if readSession.TranscriptPath != session.TranscriptPath {
		t.Errorf("TranscriptPath mismatch: got %s, want %s", readSession.TranscriptPath, session.TranscriptPath)
	}

	if readSession.ProjectPath != session.ProjectPath {
		t.Errorf("ProjectPath mismatch: got %s, want %s", readSession.ProjectPath, session.ProjectPath)
	}
}

func TestReadActiveSessionNotExists(t *testing.T) {
	// Create temp directory without active session file
	tmpDir, err := os.MkdirTemp("", "session-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize as git repo
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Change to temp directory for the test
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Read should return nil when file doesn't exist
	session, err := ReadActiveSession()
	if err != nil {
		t.Fatalf("ReadActiveSession failed: %v", err)
	}

	if session != nil {
		t.Error("Expected nil session when file doesn't exist")
	}
}

func TestClearActiveSession(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "session-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize as git repo
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Change to temp directory for the test
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Create session file
	session := &ActiveSession{
		SessionID:      "test-session-456",
		TranscriptPath: "/home/user/.claude/projects/-test/test-session-456.jsonl",
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		ProjectPath:    tmpDir,
	}

	err = WriteActiveSession(session)
	if err != nil {
		t.Fatalf("WriteActiveSession failed: %v", err)
	}

	// Clear session
	err = ClearActiveSession()
	if err != nil {
		t.Fatalf("ClearActiveSession failed: %v", err)
	}

	// Verify file was removed
	sessionPath := filepath.Join(tmpDir, ".claudit", "active-session.json")
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Error("active-session.json was not removed")
	}

	// Read should return nil now
	readSession, err := ReadActiveSession()
	if err != nil {
		t.Fatalf("ReadActiveSession failed: %v", err)
	}

	if readSession != nil {
		t.Error("Expected nil session after clear")
	}
}

func TestClearActiveSessionNotExists(t *testing.T) {
	// Create temp directory without session file
	tmpDir, err := os.MkdirTemp("", "session-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize as git repo
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Change to temp directory for the test
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Clear should not error when file doesn't exist
	err = ClearActiveSession()
	if err != nil {
		t.Fatalf("ClearActiveSession failed when file doesn't exist: %v", err)
	}
}

func TestIsSessionActive(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "session-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create transcript file
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	err = os.WriteFile(transcriptPath, []byte("{}"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Test with recent transcript (should be active)
	session := &ActiveSession{
		SessionID:      "test-session",
		TranscriptPath: transcriptPath,
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		ProjectPath:    tmpDir,
	}

	if !IsSessionActive(session) {
		t.Error("Expected recent transcript to be active")
	}

	// Test with nil session
	if IsSessionActive(nil) {
		t.Error("Expected nil session to be inactive")
	}

	// Test with empty transcript path
	emptySession := &ActiveSession{
		SessionID:      "test-session",
		TranscriptPath: "",
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		ProjectPath:    tmpDir,
	}

	if IsSessionActive(emptySession) {
		t.Error("Expected session with empty transcript path to be inactive")
	}

	// Test with non-existent transcript file
	missingSession := &ActiveSession{
		SessionID:      "test-session",
		TranscriptPath: filepath.Join(tmpDir, "nonexistent.jsonl"),
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		ProjectPath:    tmpDir,
	}

	if IsSessionActive(missingSession) {
		t.Error("Expected session with missing transcript to be inactive")
	}
}

func TestActiveSessionJSONFormat(t *testing.T) {
	session := &ActiveSession{
		SessionID:      "abc123",
		TranscriptPath: "/home/user/.claude/projects/-path/abc123.jsonl",
		StartedAt:      "2024-01-15T10:30:00Z",
		ProjectPath:    "/path/to/repo",
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		t.Fatal(err)
	}

	// Verify JSON keys match design spec
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}

	expectedKeys := []string{"session_id", "transcript_path", "started_at", "project_path"}
	for _, key := range expectedKeys {
		if _, ok := raw[key]; !ok {
			t.Errorf("Expected JSON key %q not found", key)
		}
	}
}

func TestDiscoverSessionWithActiveSession(t *testing.T) {
	// Create temp directory to act as git repo root
	tmpDir, err := os.MkdirTemp("", "session-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize as git repo
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Change to temp directory for the test
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Create a transcript file
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create active session file
	session := &ActiveSession{
		SessionID:      "discover-test-session",
		TranscriptPath: transcriptPath,
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		ProjectPath:    tmpDir,
	}
	if err := WriteActiveSession(session); err != nil {
		t.Fatal(err)
	}

	// Discover should find the active session
	discovered, err := DiscoverSession(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverSession failed: %v", err)
	}

	if discovered == nil {
		t.Fatal("DiscoverSession returned nil, expected session")
	}

	if discovered.SessionID != "discover-test-session" {
		t.Errorf("SessionID mismatch: got %s, want discover-test-session", discovered.SessionID)
	}
}

func TestDiscoverSessionWithProjectPathMismatch(t *testing.T) {
	// Create temp directory to act as git repo root
	tmpDir, err := os.MkdirTemp("", "session-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize as git repo
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Change to temp directory for the test
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Create a transcript file
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create active session file with DIFFERENT project path
	session := &ActiveSession{
		SessionID:      "wrong-project-session",
		TranscriptPath: transcriptPath,
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		ProjectPath:    "/some/other/path",
	}
	if err := WriteActiveSession(session); err != nil {
		t.Fatal(err)
	}

	// Discover should NOT find the session (project path mismatch)
	discovered, err := DiscoverSession(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverSession failed: %v", err)
	}

	// Should return nil because project path doesn't match
	if discovered != nil {
		t.Error("DiscoverSession should return nil when project path doesn't match")
	}
}

func TestDiscoverSessionWithStaleSession(t *testing.T) {
	// Create temp directory to act as git repo root
	tmpDir, err := os.MkdirTemp("", "session-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize as git repo
	gitDir := filepath.Join(tmpDir, ".git")
	if err := os.MkdirAll(gitDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Change to temp directory for the test
	origDir, _ := os.Getwd()
	os.Chdir(tmpDir)
	defer os.Chdir(origDir)

	// Create a transcript file with OLD mtime
	transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")
	if err := os.WriteFile(transcriptPath, []byte(`{"type":"user"}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Set mtime to 20 minutes ago (beyond staleSessionTimeout)
	oldTime := time.Now().Add(-20 * time.Minute)
	os.Chtimes(transcriptPath, oldTime, oldTime)

	// Create active session file
	session := &ActiveSession{
		SessionID:      "stale-session",
		TranscriptPath: transcriptPath,
		StartedAt:      time.Now().UTC().Format(time.RFC3339),
		ProjectPath:    tmpDir,
	}
	if err := WriteActiveSession(session); err != nil {
		t.Fatal(err)
	}

	// Discover should NOT find the session (stale transcript)
	discovered, err := DiscoverSession(tmpDir)
	if err != nil {
		t.Fatalf("DiscoverSession failed: %v", err)
	}

	// Should return nil because transcript is stale
	if discovered != nil {
		t.Error("DiscoverSession should return nil when transcript is stale")
	}
}

func TestScanForRecentSession(t *testing.T) {
	// Create a temp HOME directory
	tmpHome, err := os.MkdirTemp("", "session-test-home-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpHome)

	// Set HOME to our temp directory
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create project path
	projectPath := "/test/project"

	// Create Claude session directory structure
	// Path encoding: /test/project -> -test-project
	sessionDir := filepath.Join(tmpHome, ".claude", "projects", "-test-project")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a recent session file
	sessionFile := filepath.Join(sessionDir, "recent-session-abc.jsonl")
	if err := os.WriteFile(sessionFile, []byte(`{"type":"user"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Scan should find the session
	discovered, err := scanForRecentSession(projectPath)
	if err != nil {
		t.Fatalf("scanForRecentSession failed: %v", err)
	}

	if discovered == nil {
		t.Fatal("scanForRecentSession returned nil, expected session")
	}

	if discovered.SessionID != "recent-session-abc" {
		t.Errorf("SessionID mismatch: got %s, want recent-session-abc", discovered.SessionID)
	}

	if discovered.ProjectPath != projectPath {
		t.Errorf("ProjectPath mismatch: got %s, want %s", discovered.ProjectPath, projectPath)
	}
}

func TestScanForRecentSessionWithOldFile(t *testing.T) {
	// Create a temp HOME directory
	tmpHome, err := os.MkdirTemp("", "session-test-home-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpHome)

	// Set HOME to our temp directory
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create project path
	projectPath := "/test/project"

	// Create Claude session directory structure
	sessionDir := filepath.Join(tmpHome, ".claude", "projects", "-test-project")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create an OLD session file (beyond recentSessionTimeout)
	sessionFile := filepath.Join(sessionDir, "old-session.jsonl")
	if err := os.WriteFile(sessionFile, []byte(`{"type":"user"}`), 0644); err != nil {
		t.Fatal(err)
	}
	// Set mtime to 10 minutes ago (beyond 5 minute recentSessionTimeout)
	oldTime := time.Now().Add(-10 * time.Minute)
	os.Chtimes(sessionFile, oldTime, oldTime)

	// Scan should NOT find the session (too old)
	discovered, err := scanForRecentSession(projectPath)
	if err != nil {
		t.Fatalf("scanForRecentSession failed: %v", err)
	}

	if discovered != nil {
		t.Error("scanForRecentSession should return nil for old session files")
	}
}

func TestScanForRecentSessionPicksMostRecent(t *testing.T) {
	// Create a temp HOME directory
	tmpHome, err := os.MkdirTemp("", "session-test-home-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpHome)

	// Set HOME to our temp directory
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create project path
	projectPath := "/test/project"

	// Create Claude session directory structure
	sessionDir := filepath.Join(tmpHome, ".claude", "projects", "-test-project")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create older session file (2 minutes ago)
	olderFile := filepath.Join(sessionDir, "older-session.jsonl")
	if err := os.WriteFile(olderFile, []byte(`{"type":"user"}`), 0644); err != nil {
		t.Fatal(err)
	}
	olderTime := time.Now().Add(-2 * time.Minute)
	os.Chtimes(olderFile, olderTime, olderTime)

	// Create newer session file (1 minute ago)
	newerFile := filepath.Join(sessionDir, "newer-session.jsonl")
	if err := os.WriteFile(newerFile, []byte(`{"type":"user"}`), 0644); err != nil {
		t.Fatal(err)
	}
	newerTime := time.Now().Add(-1 * time.Minute)
	os.Chtimes(newerFile, newerTime, newerTime)

	// Scan should find the NEWER session
	discovered, err := scanForRecentSession(projectPath)
	if err != nil {
		t.Fatalf("scanForRecentSession failed: %v", err)
	}

	if discovered == nil {
		t.Fatal("scanForRecentSession returned nil, expected session")
	}

	if discovered.SessionID != "newer-session" {
		t.Errorf("SessionID mismatch: got %s, want newer-session", discovered.SessionID)
	}
}

func TestScanForRecentSessionIgnoresNonJsonl(t *testing.T) {
	// Create a temp HOME directory
	tmpHome, err := os.MkdirTemp("", "session-test-home-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpHome)

	// Set HOME to our temp directory
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create project path
	projectPath := "/test/project"

	// Create Claude session directory structure
	sessionDir := filepath.Join(tmpHome, ".claude", "projects", "-test-project")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create non-.jsonl files
	if err := os.WriteFile(filepath.Join(sessionDir, "config.json"), []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "notes.txt"), []byte(`notes`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sessionDir, "subdir"), 0755); err != nil {
		t.Fatal(err)
	}

	// Scan should NOT find any session
	discovered, err := scanForRecentSession(projectPath)
	if err != nil {
		t.Fatalf("scanForRecentSession failed: %v", err)
	}

	if discovered != nil {
		t.Errorf("scanForRecentSession should return nil when no .jsonl files exist, got session %s", discovered.SessionID)
	}
}

func TestScanForRecentSessionNoDirectory(t *testing.T) {
	// Create a temp HOME directory with NO Claude session directory
	tmpHome, err := os.MkdirTemp("", "session-test-home-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpHome)

	// Set HOME to our temp directory
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Scan for a project that has no session directory
	discovered, err := scanForRecentSession("/nonexistent/project")
	if err != nil {
		t.Fatalf("scanForRecentSession failed: %v", err)
	}

	if discovered != nil {
		t.Error("scanForRecentSession should return nil when session directory doesn't exist")
	}
}

func TestFindRecentSessionFromIndex(t *testing.T) {
	projectPath := "/test/project"
	now := time.Now()

	// Create a sessions index with a recent entry
	index := &agentclaude.SessionsIndex{
		Version: 1,
		Entries: []agentclaude.SessionEntry{
			{
				SessionID:    "recent-indexed-session",
				FullPath:     "/home/user/.claude/projects/-test-project/recent-indexed-session.jsonl",
				MessageCount: 10,
				Created:      now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
				Modified:     now.Add(-1 * time.Minute).Format(time.RFC3339Nano), // Modified 1 min ago
				GitBranch:    "main",
				ProjectPath:  projectPath,
			},
		},
	}

	// Should find the session
	discovered := findRecentSessionFromIndex(index, projectPath)
	if discovered == nil {
		t.Fatal("findRecentSessionFromIndex returned nil, expected session")
	}

	if discovered.SessionID != "recent-indexed-session" {
		t.Errorf("SessionID mismatch: got %s, want recent-indexed-session", discovered.SessionID)
	}
}

func TestFindRecentSessionFromIndexOldEntry(t *testing.T) {
	projectPath := "/test/project"
	now := time.Now()

	// Create a sessions index with an OLD entry (beyond recentSessionTimeout)
	index := &agentclaude.SessionsIndex{
		Version: 1,
		Entries: []agentclaude.SessionEntry{
			{
				SessionID:    "old-indexed-session",
				FullPath:     "/home/user/.claude/projects/-test-project/old-indexed-session.jsonl",
				MessageCount: 10,
				Created:      now.Add(-1 * time.Hour).Format(time.RFC3339Nano),
				Modified:     now.Add(-10 * time.Minute).Format(time.RFC3339Nano), // Modified 10 min ago
				GitBranch:    "main",
				ProjectPath:  projectPath,
			},
		},
	}

	// Should NOT find the session (too old)
	discovered := findRecentSessionFromIndex(index, projectPath)
	if discovered != nil {
		t.Errorf("findRecentSessionFromIndex should return nil for old entries, got %s", discovered.SessionID)
	}
}

func TestFindRecentSessionFromIndexWrongProject(t *testing.T) {
	now := time.Now()

	// Create a sessions index with entry for a DIFFERENT project
	index := &agentclaude.SessionsIndex{
		Version: 1,
		Entries: []agentclaude.SessionEntry{
			{
				SessionID:    "other-project-session",
				FullPath:     "/home/user/.claude/projects/-other-project/session.jsonl",
				MessageCount: 10,
				Created:      now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
				Modified:     now.Add(-1 * time.Minute).Format(time.RFC3339Nano),
				GitBranch:    "main",
				ProjectPath:  "/other/project",
			},
		},
	}

	// Should NOT find the session (wrong project)
	discovered := findRecentSessionFromIndex(index, "/test/project")
	if discovered != nil {
		t.Errorf("findRecentSessionFromIndex should return nil for wrong project, got %s", discovered.SessionID)
	}
}

func TestFindRecentSessionFromIndexPicksMostRecent(t *testing.T) {
	projectPath := "/test/project"
	now := time.Now()

	// Create a sessions index with multiple entries, one older and one newer
	index := &agentclaude.SessionsIndex{
		Version: 1,
		Entries: []agentclaude.SessionEntry{
			{
				SessionID:    "older-session",
				FullPath:     "/home/user/.claude/projects/-test-project/older-session.jsonl",
				MessageCount: 5,
				Created:      now.Add(-30 * time.Minute).Format(time.RFC3339Nano),
				Modified:     now.Add(-3 * time.Minute).Format(time.RFC3339Nano), // 3 min ago
				GitBranch:    "main",
				ProjectPath:  projectPath,
			},
			{
				SessionID:    "newer-session",
				FullPath:     "/home/user/.claude/projects/-test-project/newer-session.jsonl",
				MessageCount: 10,
				Created:      now.Add(-20 * time.Minute).Format(time.RFC3339Nano),
				Modified:     now.Add(-1 * time.Minute).Format(time.RFC3339Nano), // 1 min ago
				GitBranch:    "main",
				ProjectPath:  projectPath,
			},
		},
	}

	// Should find the NEWER session
	discovered := findRecentSessionFromIndex(index, projectPath)
	if discovered == nil {
		t.Fatal("findRecentSessionFromIndex returned nil, expected session")
	}

	if discovered.SessionID != "newer-session" {
		t.Errorf("SessionID mismatch: got %s, want newer-session", discovered.SessionID)
	}
}

func TestFindRecentSessionFromIndexEmptyIndex(t *testing.T) {
	// Empty index
	index := &agentclaude.SessionsIndex{
		Version: 1,
		Entries: []agentclaude.SessionEntry{},
	}

	// Should return nil
	discovered := findRecentSessionFromIndex(index, "/test/project")
	if discovered != nil {
		t.Error("findRecentSessionFromIndex should return nil for empty index")
	}
}

func TestDiscoverRecentSessionFallbackToScan(t *testing.T) {
	// This test verifies the fallback from sessions-index.json to file scanning
	// Create a temp HOME directory
	tmpHome, err := os.MkdirTemp("", "session-test-home-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpHome)

	// Set HOME to our temp directory
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Create project path
	projectPath := "/test/fallback-project"

	// Create Claude session directory structure WITHOUT sessions-index.json
	sessionDir := filepath.Join(tmpHome, ".claude", "projects", "-test-fallback-project")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a recent session file
	sessionFile := filepath.Join(sessionDir, "fallback-session.jsonl")
	if err := os.WriteFile(sessionFile, []byte(`{"type":"user"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// discoverRecentSession should fall back to scanning and find the session
	discovered, err := discoverRecentSession(projectPath)
	if err != nil {
		t.Fatalf("discoverRecentSession failed: %v", err)
	}

	if discovered == nil {
		t.Fatal("discoverRecentSession returned nil, expected fallback to scan")
	}

	if discovered.SessionID != "fallback-session" {
		t.Errorf("SessionID mismatch: got %s, want fallback-session", discovered.SessionID)
	}
}

func TestDiscoverRecentSessionWithIndexAndScan(t *testing.T) {
	// Test that sessions-index.json is preferred over file scanning
	tmpHome, err := os.MkdirTemp("", "session-test-home-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpHome)

	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	projectPath := "/test/index-project"
	now := time.Now()

	// Create Claude session directory
	sessionDir := filepath.Join(tmpHome, ".claude", "projects", "-test-index-project")
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a session file (for scanning fallback)
	sessionFile := filepath.Join(sessionDir, "scan-session.jsonl")
	if err := os.WriteFile(sessionFile, []byte(`{"type":"user"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create sessions-index.json with a recent entry
	index := &agentclaude.SessionsIndex{
		Version: 1,
		Entries: []agentclaude.SessionEntry{
			{
				SessionID:    "index-session",
				FullPath:     filepath.Join(sessionDir, "index-session.jsonl"),
				MessageCount: 10,
				Created:      now.Add(-10 * time.Minute).Format(time.RFC3339Nano),
				Modified:     now.Add(-1 * time.Minute).Format(time.RFC3339Nano),
				GitBranch:    "main",
				ProjectPath:  projectPath,
			},
		},
	}
	indexData, _ := json.MarshalIndent(index, "", "  ")
	if err := os.WriteFile(filepath.Join(sessionDir, "sessions-index.json"), indexData, 0644); err != nil {
		t.Fatal(err)
	}

	// discoverRecentSession should find the session from the index (not scanning)
	discovered, err := discoverRecentSession(projectPath)
	if err != nil {
		t.Fatalf("discoverRecentSession failed: %v", err)
	}

	if discovered == nil {
		t.Fatal("discoverRecentSession returned nil, expected session from index")
	}

	// Should be from index, not from scanning
	if discovered.SessionID != "index-session" {
		t.Errorf("SessionID mismatch: got %s, want index-session (should prefer index over scan)", discovered.SessionID)
	}
}
