package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
)

// MessageType represents the type of a transcript entry
type MessageType string

const (
	MessageTypeUser      MessageType = "user"
	MessageTypeAssistant MessageType = "assistant"
	MessageTypeSystem    MessageType = "system"
)

// ContentBlock represents a content block in a message
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

// TranscriptEntry represents a single entry in the JSONL transcript
type TranscriptEntry struct {
	UUID                    string          `json:"uuid"`
	ParentUUID              string          `json:"parentUuid,omitempty"`
	Type                    MessageType     `json:"type"`
	Timestamp               string          `json:"timestamp,omitempty"`
	Message                 *Message        `json:"message,omitempty"`
	SourceToolAssistantUUID string          `json:"sourceToolAssistantUUID,omitempty"`
	Raw                     json.RawMessage `json:"-"`
}

// Message represents a message content structure
type Message struct {
	Role    string         `json:"role,omitempty"`
	Content []ContentBlock `json:"-"` // Custom unmarshal handles string or array
	RawContent json.RawMessage `json:"content,omitempty"`
}

// UnmarshalJSON handles content being either a string or array of blocks
func (m *Message) UnmarshalJSON(data []byte) error {
	// Use an alias to avoid recursion
	type Alias Message
	aux := &struct {
		*Alias
	}{
		Alias: (*Alias)(m),
	}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}

	// Try to unmarshal content as array of blocks
	if len(m.RawContent) > 0 {
		if err := json.Unmarshal(m.RawContent, &m.Content); err != nil {
			// If that fails, try as a string
			var text string
			if err := json.Unmarshal(m.RawContent, &text); err == nil {
				m.Content = []ContentBlock{{Type: "text", Text: text}}
			}
		}
	}
	return nil
}

// Transcript represents a parsed Claude Code transcript
type Transcript struct {
	Entries []TranscriptEntry
}

// ParseTranscript parses a JSONL transcript from a reader
func ParseTranscript(r io.Reader) (*Transcript, error) {
	scanner := bufio.NewScanner(r)
	// Increase buffer size for large lines
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 10*1024*1024)

	var entries []TranscriptEntry

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry TranscriptEntry
		_ = json.Unmarshal(line, &entry) // Skip malformed lines
		entry.Raw = json.RawMessage(line)
		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return &Transcript{Entries: entries}, nil
}

// ParseTranscriptFile parses a JSONL transcript from a file path
func ParseTranscriptFile(path string) (*Transcript, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return ParseTranscript(f)
}

// ToJSONL converts the transcript back to JSONL format
func (t *Transcript) ToJSONL() ([]byte, error) {
	var result []byte
	for i, entry := range t.Entries {
		if i > 0 {
			result = append(result, '\n')
		}
		result = append(result, entry.Raw...)
	}
	return result, nil
}

// MessageCount returns the number of entries in the transcript
func (t *Transcript) MessageCount() int {
	return len(t.Entries)
}

// GetLastEntryUUID returns the UUID of the last entry in the transcript
func (t *Transcript) GetLastEntryUUID() string {
	if len(t.Entries) == 0 {
		return ""
	}
	return t.Entries[len(t.Entries)-1].UUID
}

// FindEntryIndex finds the index of an entry by UUID, returns -1 if not found
func (t *Transcript) FindEntryIndex(uuid string) int {
	for i, entry := range t.Entries {
		if entry.UUID == uuid {
			return i
		}
	}
	return -1
}

// GetEntriesSince returns entries that come after the given UUID
// If uuid is empty, returns all entries (handles initial commit case)
func (t *Transcript) GetEntriesSince(lastUUID string) []TranscriptEntry {
	if lastUUID == "" {
		return t.Entries
	}

	idx := t.FindEntryIndex(lastUUID)
	if idx == -1 {
		// UUID not found - return all entries (different session or data mismatch)
		return t.Entries
	}

	// Return entries after the found UUID
	if idx+1 >= len(t.Entries) {
		return nil // No new entries
	}
	return t.Entries[idx+1:]
}
