package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/re-cinq/claudit/internal/git"
	"github.com/re-cinq/claudit/internal/storage"
	"github.com/re-cinq/claudit/internal/web"
)

// testRepo is a temporary git repository for browser tests.
type testRepo struct {
	path string
	t    *testing.T
}

func newTestRepo(t *testing.T) *testRepo {
	t.Helper()
	dir, err := os.MkdirTemp("", "claudit-browser-test-*")
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

func marshalTranscript(entries []map[string]interface{}) []byte {
	var lines []string
	for _, e := range entries {
		data, _ := json.Marshal(e)
		lines = append(lines, string(data))
	}
	return []byte(strings.Join(lines, "\n"))
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

// newBrowserContext creates a headless Chrome context for testing.
func newBrowserContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.WSURLReadTimeout(45*time.Second),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)

	ctx, ctxCancel := chromedp.NewContext(allocCtx)

	cancel := func() {
		ctxCancel()
		allocCancel()
	}

	t.Cleanup(cancel)
	return ctx, cancel
}

// setupTestServer creates a test repo with sample data and starts an httptest server.
// Returns the server URL, repo, and the SHAs of created commits.
func setupTestServer(t *testing.T) (string, *testRepo, []string) {
	t.Helper()

	repo := newTestRepo(t)
	chdir(t, repo.path)

	// Commit 1: no conversation
	repo.writeFile("a.txt", "a")
	sha1 := repo.commit("Initial commit")

	// Commit 2: with conversation (user + assistant text messages)
	repo.writeFile("b.txt", "b")
	sha2 := repo.commit("Add feature B")
	transcript2 := marshalTranscript([]map[string]interface{}{
		{
			"uuid": "user-1", "type": "user",
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Hello, can you help me fix the bug?"},
				},
			},
		},
		{
			"uuid": "assistant-1", "parentUuid": "user-1", "type": "assistant",
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Of course! I'll look into the bug now."},
				},
			},
		},
	})
	repo.addConversation(sha2, "session-1", transcript2, 2)

	// Commit 3: with extended conversation including tool use and thinking
	repo.writeFile("c.txt", "c")
	sha3 := repo.commit("Fix bug in module C")
	transcript3 := marshalTranscript([]map[string]interface{}{
		{
			"uuid": "user-10", "type": "user",
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Please create a test file"},
				},
			},
		},
		{
			"uuid": "assistant-10", "parentUuid": "user-10", "type": "assistant",
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{
						"type":     "thinking",
						"thinking": "I need to create a test file.\nLet me think about the structure.\nThis should have proper assertions.\nAnd edge cases too.",
					},
					{"type": "text", "text": "I'll create the test file for you."},
					{
						"type":  "tool_use",
						"id":    "tool-1",
						"name":  "Bash",
						"input": map[string]interface{}{"command": "echo 'test content' > test.txt"},
					},
				},
			},
		},
		{
			"uuid": "user-11", "parentUuid": "assistant-10", "type": "user",
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type":       "tool_result",
						"tool_use_id": "tool-1",
						"content":    "test content",
					},
				},
			},
		},
		{
			"uuid": "assistant-11", "parentUuid": "user-11", "type": "assistant",
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Done! The test file has been created."},
				},
			},
		},
	})
	repo.addConversation(sha3, "session-2", transcript3, 4)

	srv := web.NewServer(0, repo.path)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return ts.URL, repo, []string{sha1, sha2, sha3}
}

func TestPageTitle(t *testing.T) {
	url, _, _ := setupTestServer(t)
	ctx, _ := newBrowserContext(t)

	var title string
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.Title(&title),
	)
	if err != nil {
		t.Fatalf("chromedp: %v", err)
	}
	if title != "Claudit - Conversation History" {
		t.Errorf("title: want %q, got %q", "Claudit - Conversation History", title)
	}
}

func TestCommitListRenders(t *testing.T) {
	url, _, shas := setupTestServer(t)
	ctx, _ := newBrowserContext(t)

	var commitListHTML string
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		// Wait for commits to load (commit list should have commit items)
		chromedp.WaitVisible(`.commit-item`, chromedp.ByQuery),
		chromedp.OuterHTML(`#commit-list`, &commitListHTML, chromedp.ByID),
	)
	if err != nil {
		t.Fatalf("chromedp: %v", err)
	}

	// Check that SHAs are rendered (short form)
	for _, sha := range shas {
		short := sha[:7]
		if !strings.Contains(commitListHTML, short) {
			t.Errorf("commit list should contain short SHA %s", short)
		}
	}

	// Check commit messages are present
	for _, msg := range []string{"Initial commit", "Add feature B", "Fix bug in module C"} {
		if !strings.Contains(commitListHTML, msg) {
			t.Errorf("commit list should contain message %q", msg)
		}
	}

	// Check dates are rendered (the commit-meta class holds date info)
	if !strings.Contains(commitListHTML, "commit-meta") {
		t.Error("commit list should contain commit-meta elements for dates")
	}
}

func TestConversationBadge(t *testing.T) {
	url, _, _ := setupTestServer(t)
	ctx, _ := newBrowserContext(t)

	var badges []*string
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`.commit-item`, chromedp.ByQuery),
		// Get text of all badges
		chromedp.EvaluateAsDevTools(
			`Array.from(document.querySelectorAll('.badge')).map(b => b.textContent.trim())`,
			&badges,
		),
	)
	if err != nil {
		t.Fatalf("chromedp: %v", err)
	}

	// We have 2 commits with conversations (sha2 with 2 msgs, sha3 with 4 msgs)
	if len(badges) != 2 {
		t.Fatalf("expected 2 badges, got %d", len(badges))
	}

	foundCounts := make(map[string]bool)
	for _, b := range badges {
		if b != nil {
			foundCounts[*b] = true
		}
	}
	if !foundCounts["2 msgs"] {
		t.Error("expected badge with '2 msgs'")
	}
	if !foundCounts["4 msgs"] {
		t.Error("expected badge with '4 msgs'")
	}
}

func TestClickCommitShowsConversation(t *testing.T) {
	url, _, shas := setupTestServer(t)
	ctx, _ := newBrowserContext(t)

	sha2Short := shas[1][:7]
	selector := fmt.Sprintf(`.commit-item[data-sha="%s"]`, shas[1])

	var conversationHTML string
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`.commit-item`, chromedp.ByQuery),
		chromedp.Click(selector, chromedp.ByQuery),
		// Wait for conversation to load — look for a message div
		chromedp.WaitVisible(`.message`, chromedp.ByQuery),
		chromedp.OuterHTML(`#conversation-content`, &conversationHTML, chromedp.ByID),
	)
	if err != nil {
		t.Fatalf("chromedp (clicking commit %s): %v", sha2Short, err)
	}

	if !strings.Contains(conversationHTML, "Hello, can you help me fix the bug?") {
		t.Error("conversation should contain user message text")
	}
	if !strings.Contains(conversationHTML, "Of course!") {
		t.Error("conversation should contain assistant message text")
	}
}

func TestUserMessageRendering(t *testing.T) {
	url, _, shas := setupTestServer(t)
	ctx, _ := newBrowserContext(t)

	selector := fmt.Sprintf(`.commit-item[data-sha="%s"]`, shas[1])

	var userMsgClasses, userMsgRole string
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`.commit-item`, chromedp.ByQuery),
		chromedp.Click(selector, chromedp.ByQuery),
		chromedp.WaitVisible(`.message.user`, chromedp.ByQuery),
		chromedp.AttributeValue(`.message.user`, "class", &userMsgClasses, nil, chromedp.ByQuery),
		chromedp.Text(`.message.user .message-role`, &userMsgRole, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatalf("chromedp: %v", err)
	}

	if !strings.Contains(userMsgClasses, "user") {
		t.Errorf("user message should have 'user' class, got %q", userMsgClasses)
	}
	if strings.TrimSpace(strings.ToLower(userMsgRole)) != "user" {
		t.Errorf("user message role label: want 'user', got %q", userMsgRole)
	}
}

func TestAssistantMessageRendering(t *testing.T) {
	url, _, shas := setupTestServer(t)
	ctx, _ := newBrowserContext(t)

	selector := fmt.Sprintf(`.commit-item[data-sha="%s"]`, shas[1])

	var assistantMsgClasses, assistantMsgRole string
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`.commit-item`, chromedp.ByQuery),
		chromedp.Click(selector, chromedp.ByQuery),
		chromedp.WaitVisible(`.message.assistant`, chromedp.ByQuery),
		chromedp.AttributeValue(`.message.assistant`, "class", &assistantMsgClasses, nil, chromedp.ByQuery),
		chromedp.Text(`.message.assistant .message-role`, &assistantMsgRole, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatalf("chromedp: %v", err)
	}

	if !strings.Contains(assistantMsgClasses, "assistant") {
		t.Errorf("assistant message should have 'assistant' class, got %q", assistantMsgClasses)
	}
	if strings.TrimSpace(strings.ToLower(assistantMsgRole)) != "assistant" {
		t.Errorf("assistant message role label: want 'assistant', got %q", assistantMsgRole)
	}
}

func TestToolUseRendering(t *testing.T) {
	url, _, shas := setupTestServer(t)
	ctx, _ := newBrowserContext(t)

	// Click commit 3 which has tool use
	selector := fmt.Sprintf(`.commit-item[data-sha="%s"]`, shas[2])

	var toolName string
	var toolContentVisible bool
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`.commit-item`, chromedp.ByQuery),
		chromedp.Click(selector, chromedp.ByQuery),
		chromedp.WaitVisible(`.tool-use`, chromedp.ByQuery),
		// Check tool name is displayed
		chromedp.Text(`.tool-name`, &toolName, chromedp.ByQuery),
		// Check tool content is initially hidden
		chromedp.EvaluateAsDevTools(
			`document.querySelector('.tool-content').classList.contains('expanded')`,
			&toolContentVisible,
		),
	)
	if err != nil {
		t.Fatalf("chromedp: %v", err)
	}

	if !strings.Contains(toolName, "Bash") {
		t.Errorf("tool name should contain 'Bash', got %q", toolName)
	}
	if toolContentVisible {
		t.Error("tool content should be hidden initially")
	}

	// Click tool header to expand
	var toolContentVisibleAfter bool
	err = chromedp.Run(ctx,
		chromedp.Click(`.tool-header`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.EvaluateAsDevTools(
			`document.querySelector('.tool-content').classList.contains('expanded')`,
			&toolContentVisibleAfter,
		),
	)
	if err != nil {
		t.Fatalf("chromedp click tool header: %v", err)
	}

	if !toolContentVisibleAfter {
		t.Error("tool content should be expanded after clicking header")
	}
}

func TestThinkingBlockRendering(t *testing.T) {
	url, _, shas := setupTestServer(t)
	ctx, _ := newBrowserContext(t)

	// Click commit 3 which has thinking
	selector := fmt.Sprintf(`.commit-item[data-sha="%s"]`, shas[2])

	var previewText string
	var fullVisible bool
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`.commit-item`, chromedp.ByQuery),
		chromedp.Click(selector, chromedp.ByQuery),
		chromedp.WaitVisible(`.thinking-block`, chromedp.ByQuery),
		// Check preview is shown
		chromedp.Text(`.thinking-preview`, &previewText, chromedp.ByQuery),
		// Check full text is initially hidden
		chromedp.EvaluateAsDevTools(
			`window.getComputedStyle(document.querySelector('.thinking-full')).display === 'none'`,
			&fullVisible,
		),
	)
	if err != nil {
		t.Fatalf("chromedp: %v", err)
	}

	if !strings.Contains(previewText, "I need to create a test file") {
		t.Errorf("thinking preview should contain initial text, got %q", previewText)
	}
	if !fullVisible {
		t.Error("thinking full text should be hidden initially (display: none)")
	}

	// Click thinking header to expand
	var expandedAfterClick bool
	err = chromedp.Run(ctx,
		chromedp.Click(`.thinking-header`, chromedp.ByQuery),
		chromedp.Sleep(200*time.Millisecond),
		chromedp.EvaluateAsDevTools(
			`document.querySelector('.thinking-block').classList.contains('expanded')`,
			&expandedAfterClick,
		),
	)
	if err != nil {
		t.Fatalf("chromedp click thinking: %v", err)
	}

	if !expandedAfterClick {
		t.Error("thinking block should be expanded after clicking header")
	}
}

func TestHTMLEscaping(t *testing.T) {
	repo := newTestRepo(t)
	chdir(t, repo.path)

	repo.writeFile("a.txt", "a")
	sha := repo.commit("XSS test commit")

	// Create conversation with HTML/script injection attempt
	transcript := marshalTranscript([]map[string]interface{}{
		{
			"uuid": "user-xss", "type": "user",
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": `<script>alert("xss")</script><img src=x onerror=alert(1)>`},
				},
			},
		},
		{
			"uuid": "assistant-xss", "parentUuid": "user-xss", "type": "assistant",
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": `Here is some <b>bold</b> text and a <a href="javascript:alert(1)">link</a>`},
				},
			},
		},
	})
	repo.addConversation(sha, "session-xss", transcript, 2)

	srv := web.NewServer(0, repo.path)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx, _ := newBrowserContext(t)

	selector := fmt.Sprintf(`.commit-item[data-sha="%s"]`, sha)

	var conversationHTML string
	var scriptCount int
	err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL),
		chromedp.WaitVisible(`.commit-item`, chromedp.ByQuery),
		chromedp.Click(selector, chromedp.ByQuery),
		chromedp.WaitVisible(`.message`, chromedp.ByQuery),
		chromedp.OuterHTML(`#conversation-content`, &conversationHTML, chromedp.ByID),
		// Verify no script tags were injected into the DOM
		chromedp.EvaluateAsDevTools(
			`document.querySelectorAll('#conversation-content script').length`,
			&scriptCount,
		),
	)
	if err != nil {
		t.Fatalf("chromedp: %v", err)
	}

	if scriptCount != 0 {
		t.Errorf("expected no script elements in conversation, found %d", scriptCount)
	}

	// The HTML entities should be escaped in the rendered output
	if strings.Contains(conversationHTML, `<script>`) {
		t.Error("raw <script> tag should be escaped in conversation HTML")
	}
	if strings.Contains(conversationHTML, `<img `) {
		t.Error("raw <img> tag should be escaped in conversation HTML")
	}
	// Escaped versions should be present
	if !strings.Contains(conversationHTML, `&lt;script&gt;`) {
		t.Error("script tag should appear as escaped HTML entities")
	}
	if !strings.Contains(conversationHTML, `&lt;img`) {
		t.Error("img tag should appear as escaped HTML entities")
	}
}

func TestCodeBlockRendering(t *testing.T) {
	repo := newTestRepo(t)
	chdir(t, repo.path)

	repo.writeFile("a.txt", "a")
	sha := repo.commit("Code block test")

	transcript := marshalTranscript([]map[string]interface{}{
		{
			"uuid": "user-code", "type": "user",
			"message": map[string]interface{}{
				"role": "user",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Show me some code"},
				},
			},
		},
		{
			"uuid": "assistant-code", "parentUuid": "user-code", "type": "assistant",
			"message": map[string]interface{}{
				"role": "assistant",
				"content": []map[string]interface{}{
					{"type": "text", "text": "Here is a code block:\n```go\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n```"},
				},
			},
		},
	})
	repo.addConversation(sha, "session-code", transcript, 2)

	srv := web.NewServer(0, repo.path)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ctx, _ := newBrowserContext(t)

	selector := fmt.Sprintf(`.commit-item[data-sha="%s"]`, sha)

	var preCount int
	var codeCount int
	err := chromedp.Run(ctx,
		chromedp.Navigate(ts.URL),
		chromedp.WaitVisible(`.commit-item`, chromedp.ByQuery),
		chromedp.Click(selector, chromedp.ByQuery),
		chromedp.WaitVisible(`.message`, chromedp.ByQuery),
		chromedp.EvaluateAsDevTools(
			`document.querySelectorAll('#conversation-content pre').length`,
			&preCount,
		),
		chromedp.EvaluateAsDevTools(
			`document.querySelectorAll('#conversation-content pre code').length`,
			&codeCount,
		),
	)
	if err != nil {
		t.Fatalf("chromedp: %v", err)
	}

	if preCount == 0 {
		t.Error("expected at least one <pre> element for code block")
	}
	if codeCount == 0 {
		t.Error("expected at least one <pre><code> element for code block")
	}
}

func TestEmptyState(t *testing.T) {
	url, _, _ := setupTestServer(t)
	ctx, _ := newBrowserContext(t)

	var emptyStateText string
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`.empty-state`, chromedp.ByQuery),
		chromedp.Text(`.empty-state`, &emptyStateText, chromedp.ByQuery),
	)
	if err != nil {
		t.Fatalf("chromedp: %v", err)
	}

	if !strings.Contains(emptyStateText, "Select a commit") {
		t.Errorf("empty state should say 'Select a commit', got %q", emptyStateText)
	}
}

func TestResumeButtonDisabledUntilConversationSelected(t *testing.T) {
	url, _, shas := setupTestServer(t)
	ctx, _ := newBrowserContext(t)

	var disabledBefore bool
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitVisible(`#resume-btn`, chromedp.ByID),
		// Check button is disabled initially
		chromedp.EvaluateAsDevTools(
			`document.getElementById('resume-btn').disabled`,
			&disabledBefore,
		),
	)
	if err != nil {
		t.Fatalf("chromedp: %v", err)
	}

	if !disabledBefore {
		t.Error("resume button should be disabled before selecting a commit")
	}

	// Click a commit WITHOUT conversation (sha1 = first commit)
	selectorNoConv := fmt.Sprintf(`.commit-item[data-sha="%s"]`, shas[0])
	var disabledNoConv bool
	err = chromedp.Run(ctx,
		chromedp.WaitVisible(`.commit-item`, chromedp.ByQuery),
		chromedp.Click(selectorNoConv, chromedp.ByQuery),
		chromedp.Sleep(300*time.Millisecond),
		chromedp.EvaluateAsDevTools(
			`document.getElementById('resume-btn').disabled`,
			&disabledNoConv,
		),
	)
	if err != nil {
		t.Fatalf("chromedp click no-conv commit: %v", err)
	}

	if !disabledNoConv {
		t.Error("resume button should be disabled when a commit without conversation is selected")
	}

	// Click a commit WITH conversation (sha2 = second commit)
	selectorWithConv := fmt.Sprintf(`.commit-item[data-sha="%s"]`, shas[1])
	var disabledWithConv bool
	err = chromedp.Run(ctx,
		chromedp.Click(selectorWithConv, chromedp.ByQuery),
		chromedp.WaitVisible(`.message`, chromedp.ByQuery),
		chromedp.EvaluateAsDevTools(
			`document.getElementById('resume-btn').disabled`,
			&disabledWithConv,
		),
	)
	if err != nil {
		t.Fatalf("chromedp click conv commit: %v", err)
	}

	if disabledWithConv {
		t.Error("resume button should be enabled when a commit with conversation is selected")
	}
}
