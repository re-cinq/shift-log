package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/re-cinq/claudit/internal/git"
	"github.com/re-cinq/claudit/internal/storage"
)

// testRepo is a temporary git repository for handler tests.
type testRepo struct {
	path string
	t    *testing.T
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	dir, err := os.MkdirTemp("", "claudit-web-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	r := &testRepo{path: dir, t: t}
	r.git("init", "-b", "master")
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test User")
	r.git("config", "commit.gpgsign", "false")
	return r
}

func (r *testRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.path
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *testRepo) writeFile(name, content string) {
	r.t.Helper()
	p := filepath.Join(r.path, name)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *testRepo) commit(message string) string {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "--no-gpg-sign", "-m", message, "--allow-empty")
	return r.git("rev-parse", "HEAD")
}

func (r *testRepo) addConversation(commitSHA, sessionID string, transcriptData []byte, messageCount int) {
	r.t.Helper()
	stored, err := storage.NewStoredConversation(sessionID, r.path, "master", messageCount, transcriptData)
	if err != nil {
		r.t.Fatal(err)
	}
	data, err := stored.Marshal()
	if err != nil {
		r.t.Fatal(err)
	}
	r.git("notes", "--ref", git.NotesRef, "add", "-f", "-m", string(data), commitSHA)
}

// chdir changes CWD to dir for git functions that operate on CWD.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}

// decodeJSON decodes a JSON response body, failing the test on error.
func decodeJSON(t *testing.T, w *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.NewDecoder(w.Body).Decode(v); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

func sampleTranscript() []byte {
	entries := []map[string]interface{}{
		{
			"uuid": "user-1", "type": "user",
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Hello, can you help?"},
				},
			},
		},
		{
			"uuid": "assistant-1", "parentUuid": "user-1", "type": "assistant",
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Of course! What do you need?"},
				},
			},
		},
	}
	return marshalTranscript(entries)
}

func extendedTranscript() []byte {
	entries := []map[string]interface{}{
		{
			"uuid": "user-1", "type": "user",
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Hello, can you help?"},
				},
			},
		},
		{
			"uuid": "assistant-1", "parentUuid": "user-1", "type": "assistant",
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Of course! What do you need?"},
				},
			},
		},
		{
			"uuid": "user-2", "parentUuid": "assistant-1", "type": "user",
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Create a file please"},
				},
			},
		},
		{
			"uuid": "assistant-2", "parentUuid": "user-2", "type": "assistant",
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Done! I created the file."},
					{
						"type": "tool_use", "id": "tool-1", "name": "Bash",
						"input": map[string]interface{}{"command": "echo test > file.txt"},
					},
				},
			},
		},
	}
	return marshalTranscript(entries)
}

func marshalTranscript(entries []map[string]interface{}) []byte {
	var lines []string
	for _, e := range entries {
		data, _ := json.Marshal(e)
		lines = append(lines, string(data))
	}
	return []byte(strings.Join(lines, "\n"))
}

// --- Helper function tests ---

func TestGetCommitList(t *testing.T) {
	repo := newTestRepo(t)

	repo.writeFile("a.txt", "a")
	sha1 := repo.commit("First commit")

	repo.writeFile("b.txt", "b")
	sha2 := repo.commit("Second commit")

	commits, err := getCommitList(10, repo.path)
	if err != nil {
		t.Fatalf("getCommitList: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(commits))
	}
	// Most recent first
	if commits[0].SHA != sha2 {
		t.Errorf("first commit SHA: want %s, got %s", sha2, commits[0].SHA)
	}
	if commits[1].SHA != sha1 {
		t.Errorf("second commit SHA: want %s, got %s", sha1, commits[1].SHA)
	}
	if commits[0].Message != "Second commit" {
		t.Errorf("message: want %q, got %q", "Second commit", commits[0].Message)
	}
	if commits[0].Author != "Test User" {
		t.Errorf("author: want %q, got %q", "Test User", commits[0].Author)
	}
	if commits[0].Date == "" {
		t.Error("date should not be empty")
	}
}

func TestGetCommitListLimit(t *testing.T) {
	repo := newTestRepo(t)

	repo.writeFile("a.txt", "a")
	repo.commit("First")
	repo.writeFile("b.txt", "b")
	repo.commit("Second")
	repo.writeFile("c.txt", "c")
	repo.commit("Third")

	commits, err := getCommitList(2, repo.path)
	if err != nil {
		t.Fatalf("getCommitList: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits with limit, got %d", len(commits))
	}
	if commits[0].Message != "Third" {
		t.Errorf("expected newest commit first, got %q", commits[0].Message)
	}
}

func TestGetGraphData(t *testing.T) {
	repo := newTestRepo(t)

	repo.writeFile("a.txt", "a")
	sha1 := repo.commit("First commit")

	repo.writeFile("b.txt", "b")
	sha2 := repo.commit("Second commit")

	nodes, err := getGraphData(10, repo.path)
	if err != nil {
		t.Fatalf("getGraphData: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}
	if nodes[0].SHA != sha2 {
		t.Errorf("first node SHA: want %s, got %s", sha2, nodes[0].SHA)
	}
	if nodes[0].Message != "Second commit" {
		t.Errorf("message: want %q, got %q", "Second commit", nodes[0].Message)
	}
	if len(nodes[0].Parents) != 1 || nodes[0].Parents[0] != sha1 {
		t.Errorf("second commit parents: want [%s], got %v", sha1, nodes[0].Parents)
	}
	if len(nodes[1].Parents) != 0 {
		t.Errorf("first commit should have no parents, got %v", nodes[1].Parents)
	}
}

// --- Handler tests ---

func TestHandleCommits(t *testing.T) {
	repo := newTestRepo(t)
	chdir(t, repo.path)

	repo.writeFile("a.txt", "a")
	repo.commit("First commit")

	repo.writeFile("b.txt", "b")
	sha2 := repo.commit("Second commit")
	repo.addConversation(sha2, "session-1", sampleTranscript(), 2)

	srv := NewServer(0, repo.path)

	t.Run("lists all commits", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200, got %d", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type: want application/json, got %q", ct)
		}

		var commits []CommitInfo
		decodeJSON(t, w, &commits)
		if len(commits) != 2 {
			t.Fatalf("expected 2 commits, got %d", len(commits))
		}
	})

	t.Run("includes conversation metadata", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		var commits []CommitInfo
		decodeJSON(t, w, &commits)

		var withConv *CommitInfo
		for i, c := range commits {
			if c.SHA == sha2 {
				withConv = &commits[i]
				break
			}
		}
		if withConv == nil {
			t.Fatal("commit with conversation not found")
		}
		if !withConv.HasConversation {
			t.Error("expected HasConversation=true")
		}
		if withConv.MessageCount != 2 {
			t.Errorf("MessageCount: want 2, got %d", withConv.MessageCount)
		}
	})

	t.Run("filters by has_conversation", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits?has_conversation=true", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		var commits []CommitInfo
		decodeJSON(t, w, &commits)

		if len(commits) != 1 {
			t.Fatalf("expected 1 commit with filter, got %d", len(commits))
		}
		if commits[0].SHA != sha2 {
			t.Errorf("filtered commit SHA: want %s, got %s", sha2, commits[0].SHA)
		}
	})

	t.Run("pagination limit", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits?limit=1", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		var commits []CommitInfo
		decodeJSON(t, w, &commits)

		if len(commits) != 1 {
			t.Fatalf("expected 1 commit with limit=1, got %d", len(commits))
		}
	})

	t.Run("pagination offset", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits?limit=1&offset=1", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		var commits []CommitInfo
		decodeJSON(t, w, &commits)

		if len(commits) != 1 {
			t.Fatalf("expected 1 commit with offset=1, got %d", len(commits))
		}
	})

	t.Run("offset beyond range returns empty", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits?offset=100", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		var commits []CommitInfo
		decodeJSON(t, w, &commits)

		if len(commits) != 0 {
			t.Errorf("expected 0 commits, got %d", len(commits))
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/commits", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status: want 405, got %d", w.Code)
		}
	})
}

func TestHandleCommitDetail(t *testing.T) {
	repo := newTestRepo(t)
	chdir(t, repo.path)

	repo.writeFile("a.txt", "a")
	sha1 := repo.commit("First commit")

	repo.writeFile("b.txt", "b")
	sha2 := repo.commit("Second commit")
	repo.addConversation(sha2, "session-1", sampleTranscript(), 2)

	srv := NewServer(0, repo.path)

	t.Run("returns conversation", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits/"+sha2, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200, got %d: %s", w.Code, w.Body.String())
		}
		if ct := w.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type: want application/json, got %q", ct)
		}

		var resp ConversationResponse
		decodeJSON(t, w, &resp)
		if resp.SHA != sha2 {
			t.Errorf("SHA: want %s, got %s", sha2, resp.SHA)
		}
		if resp.SessionID != "session-1" {
			t.Errorf("SessionID: want %q, got %q", "session-1", resp.SessionID)
		}
		if len(resp.Transcript) != 2 {
			t.Errorf("transcript entries: want 2, got %d", len(resp.Transcript))
		}
		if resp.MessageCount != 2 {
			t.Errorf("MessageCount: want 2, got %d", resp.MessageCount)
		}
	})

	t.Run("short SHA resolves", func(t *testing.T) {
		shortSHA := sha2[:7]
		req := httptest.NewRequest("GET", "/api/commits/"+shortSHA, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp ConversationResponse
		decodeJSON(t, w, &resp)
		if resp.SHA != sha2 {
			t.Errorf("resolved SHA: want %s, got %s", sha2, resp.SHA)
		}
	})

	t.Run("transcript entry types", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits/"+sha2, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		var resp ConversationResponse
		decodeJSON(t, w, &resp)

		if string(resp.Transcript[0].Type) != "user" {
			t.Errorf("first entry type: want user, got %s", resp.Transcript[0].Type)
		}
		if string(resp.Transcript[1].Type) != "assistant" {
			t.Errorf("second entry type: want assistant, got %s", resp.Transcript[1].Type)
		}
	})

	t.Run("no conversation returns 404", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits/"+sha1, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("status: want 404, got %d", w.Code)
		}
	})

	t.Run("missing SHA returns 400", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits/", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("status: want 400, got %d", w.Code)
		}
	})

	t.Run("invalid SHA returns 400", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits/not-a-valid-ref", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("status: want 400, got %d", w.Code)
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/commits/"+sha2, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status: want 405, got %d", w.Code)
		}
	})
}

func TestHandleCommitDetailIncremental(t *testing.T) {
	repo := newTestRepo(t)
	chdir(t, repo.path)

	// First commit with short conversation
	repo.writeFile("a.txt", "a")
	sha1 := repo.commit("First commit")
	repo.addConversation(sha1, "session-1", sampleTranscript(), 2)

	// Second commit with extended conversation (same session)
	repo.writeFile("b.txt", "b")
	sha2 := repo.commit("Second commit")
	repo.addConversation(sha2, "session-1", extendedTranscript(), 4)

	srv := NewServer(0, repo.path)

	t.Run("incremental returns only new entries", func(t *testing.T) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/commits/%s?incremental=true", sha2), nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200, got %d: %s", w.Code, w.Body.String())
		}

		var resp ConversationResponse
		decodeJSON(t, w, &resp)

		if !resp.IsIncremental {
			t.Error("expected IsIncremental=true")
		}
		if resp.ParentCommitSHA != sha1 {
			t.Errorf("ParentCommitSHA: want %s, got %s", sha1, resp.ParentCommitSHA)
		}
		// Should only have user-2 and assistant-2
		if len(resp.Transcript) != 2 {
			t.Errorf("incremental entries: want 2, got %d", len(resp.Transcript))
		}
		if resp.IncrementalCount != 2 {
			t.Errorf("IncrementalCount: want 2, got %d", resp.IncrementalCount)
		}
	})

	t.Run("full mode returns all entries", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/commits/"+sha2, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		var resp ConversationResponse
		decodeJSON(t, w, &resp)

		if resp.IsIncremental {
			t.Error("expected IsIncremental=false for full mode")
		}
		if len(resp.Transcript) != 4 {
			t.Errorf("full transcript entries: want 4, got %d", len(resp.Transcript))
		}
	})

	t.Run("incremental with no parent conversation returns all", func(t *testing.T) {
		// Create a commit with a different session ID (no matching parent)
		repo.writeFile("c.txt", "c")
		sha3 := repo.commit("Third commit")
		repo.addConversation(sha3, "different-session", sampleTranscript(), 2)

		req := httptest.NewRequest("GET", fmt.Sprintf("/api/commits/%s?incremental=true", sha3), nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		var resp ConversationResponse
		decodeJSON(t, w, &resp)

		// Different session from parent, so no incremental boundary found
		if resp.IsIncremental {
			t.Error("expected IsIncremental=false when parent has different session")
		}
		if len(resp.Transcript) != 2 {
			t.Errorf("entries: want 2 (full), got %d", len(resp.Transcript))
		}
	})
}

func TestHandleGraph(t *testing.T) {
	repo := newTestRepo(t)
	chdir(t, repo.path)

	repo.writeFile("a.txt", "a")
	repo.commit("First commit")

	repo.writeFile("b.txt", "b")
	sha2 := repo.commit("Second commit")
	repo.addConversation(sha2, "session-1", sampleTranscript(), 2)

	srv := NewServer(0, repo.path)

	t.Run("returns graph nodes", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/graph", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200, got %d", w.Code)
		}

		var nodes []GraphNode
		decodeJSON(t, w, &nodes)

		if len(nodes) != 2 {
			t.Fatalf("expected 2 nodes, got %d", len(nodes))
		}
	})

	t.Run("marks conversations on nodes", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/graph", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		var nodes []GraphNode
		decodeJSON(t, w, &nodes)

		var found bool
		for _, n := range nodes {
			if n.SHA == sha2 {
				found = true
				if !n.HasConversation {
					t.Error("expected HasConversation=true on second commit")
				}
			} else {
				if n.HasConversation {
					t.Error("expected HasConversation=false on first commit")
				}
			}
		}
		if !found {
			t.Error("second commit not in graph nodes")
		}
	})

	t.Run("nodes include parent references", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/graph", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		var nodes []GraphNode
		decodeJSON(t, w, &nodes)

		// Find the second commit and check its parent
		for _, n := range nodes {
			if n.SHA == sha2 && len(n.Parents) == 0 {
				t.Error("second commit should have a parent")
			}
		}
	})

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/graph", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status: want 405, got %d", w.Code)
		}
	})
}

func TestHandleResume(t *testing.T) {
	repo := newTestRepo(t)
	chdir(t, repo.path)

	repo.writeFile("a.txt", "a")
	sha1 := repo.commit("First commit")

	repo.writeFile("b.txt", "b")
	sha2 := repo.commit("Second commit")
	repo.addConversation(sha2, "session-1", sampleTranscript(), 2)

	srv := NewServer(0, repo.path)

	t.Run("method not allowed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/resume/"+sha1, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("status: want 405, got %d", w.Code)
		}
	})

	t.Run("missing SHA returns 400", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/resume/", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("status: want 400, got %d", w.Code)
		}
	})

	t.Run("no conversation returns 404", func(t *testing.T) {
		req := httptest.NewRequest("POST", "/api/resume/"+sha1, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("status: want 404, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("uncommitted changes returns 409", func(t *testing.T) {
		// Create an untracked file to dirty the working tree
		dirtyFile := filepath.Join(repo.path, "dirty.txt")
		if err := os.WriteFile(dirtyFile, []byte("dirty"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(dirtyFile)

		req := httptest.NewRequest("POST", "/api/resume/"+sha2, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusConflict {
			t.Errorf("status: want 409, got %d: %s", w.Code, w.Body.String())
		}
	})
}

// --- Static file / embedded HTML tests ---

func TestStaticFileServing(t *testing.T) {
	repo := newTestRepo(t)
	srv := NewServer(0, repo.path)

	t.Run("serves index.html at root", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("status: want 200, got %d", w.Code)
		}
		if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("Content-Type: want text/html, got %q", ct)
		}
	})

	t.Run("HTML contains page title", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		body := w.Body.String()
		if !strings.Contains(body, "Claudit - Conversation History") {
			t.Error("page should contain title 'Claudit - Conversation History'")
		}
	})

	t.Run("HTML contains key rendering functions", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		body := w.Body.String()
		functions := []string{
			"function escapeHtml(",
			"function formatContent(",
			"function renderConversation(",
			"function renderUserMessage(",
			"function renderAssistantMessage(",
			"function renderSystemMessage(",
			"function renderThinking(",
			"function renderToolUse(",
			"function renderToolResult(",
			"function formatToolInput(",
			"function countDisplayedMessages(",
			"function formatDate(",
			"function showStatus(",
			"function setViewMode(",
			"function updateViewToggle(",
			"function renderCommits(",
		}
		for _, fn := range functions {
			if !strings.Contains(body, fn) {
				t.Errorf("index.html missing function: %s", fn)
			}
		}
	})

	t.Run("HTML contains expected DOM elements", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		body := w.Body.String()
		elements := []string{
			`id="commit-list"`,
			`id="conversation-content"`,
			`id="resume-btn"`,
			`id="view-toggle"`,
			`id="conversation-title"`,
			`id="incremental-info"`,
			`class="commit-panel"`,
			`class="conversation-panel"`,
		}
		for _, elem := range elements {
			if !strings.Contains(body, elem) {
				t.Errorf("index.html missing DOM element: %s", elem)
			}
		}
	})

	t.Run("HTML contains CSS variables for theming", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)

		body := w.Body.String()
		cssVars := []string{
			"--bg-primary",
			"--text-primary",
			"--accent",
			"--user-bg",
			"--assistant-bg",
		}
		for _, v := range cssVars {
			if !strings.Contains(body, v) {
				t.Errorf("index.html missing CSS variable: %s", v)
			}
		}
	})
}
