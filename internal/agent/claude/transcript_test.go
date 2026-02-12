package claude

import (
	"strings"
	"testing"
)

func TestGetLastEntryUUID(t *testing.T) {
	tests := []struct {
		name     string
		jsonl    string
		expected string
	}{
		{
			name:     "empty transcript",
			jsonl:    "",
			expected: "",
		},
		{
			name:     "single entry",
			jsonl:    `{"uuid":"abc123","type":"user"}`,
			expected: "abc123",
		},
		{
			name:     "multiple entries",
			jsonl:    `{"uuid":"first","type":"user"}` + "\n" + `{"uuid":"second","type":"assistant"}` + "\n" + `{"uuid":"third","type":"user"}`,
			expected: "third",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			transcript, err := ParseJSONLTranscript(strings.NewReader(tc.jsonl))
			if err != nil {
				t.Fatalf("ParseJSONLTranscript failed: %v", err)
			}
			result := transcript.GetLastEntryUUID()
			if result != tc.expected {
				t.Errorf("GetLastEntryUUID() = %q, expected %q", result, tc.expected)
			}
		})
	}
}

func TestFindEntryIndex(t *testing.T) {
	jsonl := `{"uuid":"first","type":"user"}` + "\n" + `{"uuid":"second","type":"assistant"}` + "\n" + `{"uuid":"third","type":"user"}`
	transcript, err := ParseJSONLTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ParseJSONLTranscript failed: %v", err)
	}

	tests := []struct {
		uuid     string
		expected int
	}{
		{"first", 0},
		{"second", 1},
		{"third", 2},
		{"nonexistent", -1},
		{"", -1},
	}

	for _, tc := range tests {
		t.Run(tc.uuid, func(t *testing.T) {
			result := transcript.FindEntryIndex(tc.uuid)
			if result != tc.expected {
				t.Errorf("FindEntryIndex(%q) = %d, expected %d", tc.uuid, result, tc.expected)
			}
		})
	}
}

func TestGetEntriesSince(t *testing.T) {
	jsonl := `{"uuid":"first","type":"user"}` + "\n" + `{"uuid":"second","type":"assistant"}` + "\n" + `{"uuid":"third","type":"user"}` + "\n" + `{"uuid":"fourth","type":"assistant"}`
	transcript, err := ParseJSONLTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ParseJSONLTranscript failed: %v", err)
	}

	tests := []struct {
		name          string
		lastUUID      string
		expectedUUIDs []string
	}{
		{
			name:          "empty uuid returns all",
			lastUUID:      "",
			expectedUUIDs: []string{"first", "second", "third", "fourth"},
		},
		{
			name:          "from first entry",
			lastUUID:      "first",
			expectedUUIDs: []string{"second", "third", "fourth"},
		},
		{
			name:          "from middle entry",
			lastUUID:      "second",
			expectedUUIDs: []string{"third", "fourth"},
		},
		{
			name:          "from last entry",
			lastUUID:      "fourth",
			expectedUUIDs: []string{},
		},
		{
			name:          "nonexistent uuid returns all",
			lastUUID:      "nonexistent",
			expectedUUIDs: []string{"first", "second", "third", "fourth"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entries := transcript.GetEntriesSince(tc.lastUUID)

			if len(entries) != len(tc.expectedUUIDs) {
				t.Errorf("GetEntriesSince(%q) returned %d entries, expected %d", tc.lastUUID, len(entries), len(tc.expectedUUIDs))
				return
			}

			for i, entry := range entries {
				if entry.UUID != tc.expectedUUIDs[i] {
					t.Errorf("Entry %d UUID = %q, expected %q", i, entry.UUID, tc.expectedUUIDs[i])
				}
			}
		})
	}
}

func TestGetEntriesSinceWithEmptyTranscript(t *testing.T) {
	transcript, err := ParseJSONLTranscript(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ParseJSONLTranscript failed: %v", err)
	}

	entries := transcript.GetEntriesSince("")
	if len(entries) != 0 {
		t.Errorf("Expected 0 entries from empty transcript, got %d", len(entries))
	}

	entries = transcript.GetEntriesSince("some-uuid")
	if len(entries) != 0 {
		t.Errorf("Expected 0 entries from empty transcript with uuid, got %d", len(entries))
	}
}

func TestParseJSONLTranscriptExtractsModel(t *testing.T) {
	jsonl := `{"uuid":"user-1","type":"user","message":{"role":"user","content":[{"type":"text","text":"Hello"}]}}
{"uuid":"assistant-1","type":"assistant","model":"claude-sonnet-4-5-20250514","message":{"role":"assistant","content":[{"type":"text","text":"Hi!"}]}}
{"uuid":"user-2","type":"user","message":{"role":"user","content":[{"type":"text","text":"Thanks"}]}}`

	transcript, err := ParseJSONLTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ParseJSONLTranscript failed: %v", err)
	}

	if transcript.Model != "claude-sonnet-4-5-20250514" {
		t.Errorf("Model = %q, want %q", transcript.Model, "claude-sonnet-4-5-20250514")
	}
}

func TestParseJSONLTranscriptNoModel(t *testing.T) {
	jsonl := `{"uuid":"user-1","type":"user","message":{"role":"user","content":[{"type":"text","text":"Hello"}]}}
{"uuid":"assistant-1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Hi!"}]}}`

	transcript, err := ParseJSONLTranscript(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ParseJSONLTranscript failed: %v", err)
	}

	if transcript.Model != "" {
		t.Errorf("Model = %q, want empty", transcript.Model)
	}
}
