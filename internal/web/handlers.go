package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"strconv"
	"strings"

	"github.com/DanielJonesEB/claudit/internal/claude"
	"github.com/DanielJonesEB/claudit/internal/git"
	"github.com/DanielJonesEB/claudit/internal/storage"
)

// CommitInfo represents commit data for the API
type CommitInfo struct {
	SHA             string `json:"sha"`
	Message         string `json:"message"`
	Author          string `json:"author"`
	Date            string `json:"date"`
	HasConversation bool   `json:"has_conversation"`
	MessageCount    int    `json:"message_count,omitempty"`
}

// ConversationResponse represents the full conversation data
type ConversationResponse struct {
	SHA              string                   `json:"sha"`
	SessionID        string                   `json:"session_id"`
	Timestamp        string                   `json:"timestamp"`
	MessageCount     int                      `json:"message_count"`
	Transcript       []claude.TranscriptEntry `json:"transcript"`
	IsIncremental    bool                     `json:"is_incremental"`
	ParentCommitSHA  string                   `json:"parent_commit_sha,omitempty"`
	IncrementalCount int                      `json:"incremental_count,omitempty"`
}

// GraphNode represents a node in the commit graph
type GraphNode struct {
	SHA             string   `json:"sha"`
	Parents         []string `json:"parents"`
	HasConversation bool     `json:"has_conversation"`
	Message         string   `json:"message"`
}

// writeJSONError writes a JSON error response with the given status code.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// buildNoteSet returns a set of commit SHAs that have conversation notes.
func buildNoteSet() (map[string]bool, error) {
	commitsWithNotes, err := git.ListCommitsWithNotes()
	if err != nil {
		return nil, err
	}
	noteSet := make(map[string]bool, len(commitsWithNotes))
	for _, sha := range commitsWithNotes {
		noteSet[sha] = true
	}
	return noteSet, nil
}

// getStoredOrWriteError retrieves a stored conversation for the given SHA,
// writing an appropriate JSON error response and returning nil if not found or on error.
func getStoredOrWriteError(w http.ResponseWriter, commitSHA string) *storage.StoredConversation {
	stored, err := storage.GetStoredConversation(commitSHA)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to read conversation")
		return nil
	}
	if stored == nil {
		writeJSONError(w, http.StatusNotFound, "no conversation found")
		return nil
	}
	return stored
}

// handleCommits returns a list of commits with conversation metadata
func (s *Server) handleCommits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse query parameters
	limit := 100
	offset := 0
	hasConversationFilter := false

	if l := r.URL.Query().Get("limit"); l != "" {
		if val, err := strconv.Atoi(l); err == nil && val > 0 {
			limit = val
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if val, err := strconv.Atoi(o); err == nil && val >= 0 {
			offset = val
		}
	}
	if hc := r.URL.Query().Get("has_conversation"); hc == "true" {
		hasConversationFilter = true
	}

	noteSet, err := buildNoteSet()
	if err != nil {
		http.Error(w, "Failed to list conversations", http.StatusInternalServerError)
		return
	}

	// Get all commits
	commits, err := getCommitList(limit+offset, s.repoDir)
	if err != nil {
		http.Error(w, "Failed to get commits", http.StatusInternalServerError)
		return
	}

	var result []CommitInfo

	for _, commit := range commits {
		hasConv := noteSet[commit.SHA]

		if hasConversationFilter && !hasConv {
			continue
		}

		info := CommitInfo{
			SHA:             commit.SHA,
			Message:         commit.Message,
			Author:          commit.Author,
			Date:            commit.Date,
			HasConversation: hasConv,
		}

		// Get message count if has conversation
		if hasConv {
			if stored, err := storage.GetStoredConversation(commit.SHA); err == nil && stored != nil {
				info.MessageCount = stored.MessageCount
			}
		}

		result = append(result, info)
	}

	// Apply pagination
	if offset > len(result) {
		result = []CommitInfo{}
	} else if offset+limit > len(result) {
		result = result[offset:]
	} else {
		result = result[offset : offset+limit]
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// handleCommitDetail returns the full conversation for a specific commit
func (s *Server) handleCommitDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract SHA from path
	path := strings.TrimPrefix(r.URL.Path, "/api/commits/")
	sha := strings.TrimSuffix(path, "/")

	if sha == "" {
		http.Error(w, "Commit SHA required", http.StatusBadRequest)
		return
	}

	// Check if incremental mode is requested
	incremental := r.URL.Query().Get("incremental") == "true"

	// Resolve the reference
	fullSHA, err := git.ResolveRef(sha)
	if err != nil {
		http.Error(w, "Invalid commit reference", http.StatusBadRequest)
		return
	}

	stored := getStoredOrWriteError(w, fullSHA)
	if stored == nil {
		return
	}

	// Parse transcript
	transcript, err := stored.ParseTranscript()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to parse transcript")
		return
	}

	// Determine which entries to return
	var entries []claude.TranscriptEntry
	var parentSHA string
	var isIncremental bool

	if incremental {
		var lastEntryUUID string
		parentSHA, lastEntryUUID = storage.FindParentConversationBoundary(fullSHA, stored.SessionID)
		if lastEntryUUID != "" {
			entries = transcript.GetEntriesSince(lastEntryUUID)
			isIncremental = true
		} else {
			entries = transcript.Entries
		}
	} else {
		entries = transcript.Entries
	}

	response := ConversationResponse{
		SHA:              fullSHA,
		SessionID:        stored.SessionID,
		Timestamp:        stored.Timestamp,
		MessageCount:     stored.MessageCount,
		Transcript:       entries,
		IsIncremental:    isIncremental,
		ParentCommitSHA:  parentSHA,
		IncrementalCount: len(entries),
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// handleGraph returns the commit graph data
func (s *Server) handleGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	noteSet, err := buildNoteSet()
	if err != nil {
		http.Error(w, "Failed to list conversations", http.StatusInternalServerError)
		return
	}

	// Get graph data
	nodes, err := getGraphData(50, s.repoDir)
	if err != nil {
		http.Error(w, "Failed to get graph data", http.StatusInternalServerError)
		return
	}

	for i := range nodes {
		nodes[i].HasConversation = noteSet[nodes[i].SHA]
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(nodes)
}

// handleResume triggers a session resume
func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract SHA from path
	path := strings.TrimPrefix(r.URL.Path, "/api/resume/")
	sha := strings.TrimSuffix(path, "/")

	if sha == "" {
		http.Error(w, "Commit SHA required", http.StatusBadRequest)
		return
	}

	// Check for uncommitted changes
	hasChanges, err := git.HasUncommittedChanges()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to check working directory")
		return
	}

	if hasChanges {
		writeJSONError(w, http.StatusConflict, "uncommitted changes in working directory")
		return
	}

	// Resolve the reference
	fullSHA, err := git.ResolveRef(sha)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid commit reference")
		return
	}

	stored := getStoredOrWriteError(w, fullSHA)
	if stored == nil {
		return
	}

	// Decompress transcript for restore
	transcriptData, err := stored.GetTranscript()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to decompress transcript")
		return
	}

	// Restore session
	err = claude.RestoreSession(
		s.repoDir,
		stored.SessionID,
		stored.GitBranch,
		transcriptData,
		stored.MessageCount,
		"Restored from web UI",
	)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to restore session: %v", err))
		return
	}

	// Checkout commit
	if err := git.Checkout(fullSHA); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to checkout: %v", err))
		return
	}

	// Launch Claude in background
	claudeCmd := exec.Command("claude", "--resume", stored.SessionID)
	claudeCmd.Dir = s.repoDir
	if err := claudeCmd.Start(); err != nil {
		writeJSONError(w, http.StatusInternalServerError, fmt.Sprintf("failed to launch claude: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":     "success",
		"session_id": stored.SessionID,
	})
}

// CommitData holds basic commit information
type CommitData struct {
	SHA     string
	Message string
	Author  string
	Date    string
}

// fieldSep is the delimiter used to split git log output.
// We use %x00 in git --format strings to emit a null byte, which avoids
// collisions with commit messages that may contain pipes or other punctuation.
const fieldSep = "\x00"

// getCommitList returns a list of commits
func getCommitList(limit int, repoDir string) ([]CommitData, error) {
	cmd := exec.Command("git", "log", fmt.Sprintf("--max-count=%d", limit),
		"--format=%H%x00%s%x00%an%x00%ci")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var commits []CommitData
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, fieldSep, 4)
		if len(parts) < 4 {
			continue
		}
		commits = append(commits, CommitData{
			SHA:     parts[0],
			Message: parts[1],
			Author:  parts[2],
			Date:    parts[3],
		})
	}

	return commits, nil
}

// getGraphData returns commit graph data
func getGraphData(limit int, repoDir string) ([]GraphNode, error) {
	cmd := exec.Command("git", "log", fmt.Sprintf("--max-count=%d", limit),
		"--format=%H%x00%P%x00%s")
	cmd.Dir = repoDir
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var nodes []GraphNode
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, fieldSep, 3)
		if len(parts) < 3 {
			continue
		}

		var parents []string
		if parts[1] != "" {
			parents = strings.Split(parts[1], " ")
		}

		nodes = append(nodes, GraphNode{
			SHA:     parts[0],
			Parents: parents,
			Message: parts[2],
		})
	}

	return nodes, nil
}
