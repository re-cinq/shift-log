package storage

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNewStoredConversation(t *testing.T) {
	transcript := []byte(`{"uuid":"1","type":"user"}`)

	sc, err := NewStoredConversation("session-1", "/test/project", "main", 5, transcript)
	if err != nil {
		t.Fatalf("NewStoredConversation() error: %v", err)
	}

	if sc.Version != 1 {
		t.Errorf("Version = %d, want 1", sc.Version)
	}
	if sc.SessionID != "session-1" {
		t.Errorf("SessionID = %q, want %q", sc.SessionID, "session-1")
	}
	if sc.ProjectPath != "/test/project" {
		t.Errorf("ProjectPath = %q, want %q", sc.ProjectPath, "/test/project")
	}
	if sc.GitBranch != "main" {
		t.Errorf("GitBranch = %q, want %q", sc.GitBranch, "main")
	}
	if sc.MessageCount != 5 {
		t.Errorf("MessageCount = %d, want 5", sc.MessageCount)
	}
	if !strings.HasPrefix(sc.Checksum, "sha256:") {
		t.Errorf("Checksum = %q, want sha256: prefix", sc.Checksum)
	}
	if sc.Transcript == "" {
		t.Error("Transcript should not be empty")
	}
	if sc.Timestamp == "" {
		t.Error("Timestamp should not be empty")
	}
}

func TestStoredConversationGetTranscript(t *testing.T) {
	original := []byte(`{"uuid":"1","type":"user","message":{"content":[{"type":"text","text":"Hello"}]}}`)

	sc, err := NewStoredConversation("session-1", "/test", "main", 1, original)
	if err != nil {
		t.Fatalf("NewStoredConversation() error: %v", err)
	}

	got, err := sc.GetTranscript()
	if err != nil {
		t.Fatalf("GetTranscript() error: %v", err)
	}

	if string(got) != string(original) {
		t.Errorf("GetTranscript() returned different data: got %d bytes, want %d bytes", len(got), len(original))
	}
}

func TestStoredConversationVerifyIntegrity(t *testing.T) {
	transcript := []byte(`{"uuid":"1","type":"user"}`)

	sc, err := NewStoredConversation("session-1", "/test", "main", 1, transcript)
	if err != nil {
		t.Fatalf("NewStoredConversation() error: %v", err)
	}

	valid, err := sc.VerifyIntegrity()
	if err != nil {
		t.Fatalf("VerifyIntegrity() error: %v", err)
	}
	if !valid {
		t.Error("VerifyIntegrity() returned false for valid conversation")
	}
}

func TestStoredConversationVerifyIntegrityTampered(t *testing.T) {
	transcript := []byte(`{"uuid":"1","type":"user"}`)

	sc, err := NewStoredConversation("session-1", "/test", "main", 1, transcript)
	if err != nil {
		t.Fatalf("NewStoredConversation() error: %v", err)
	}

	// Tamper with the checksum
	sc.Checksum = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	valid, err := sc.VerifyIntegrity()
	if err != nil {
		t.Fatalf("VerifyIntegrity() error: %v", err)
	}
	if valid {
		t.Error("VerifyIntegrity() returned true for tampered checksum")
	}
}

func TestStoredConversationVerifyIntegrityCorruptTranscript(t *testing.T) {
	sc := &StoredConversation{
		Version:    1,
		SessionID:  "session-1",
		Checksum:   "sha256:abcd",
		Transcript: "not valid base64!!!",
	}

	_, err := sc.VerifyIntegrity()
	if err == nil {
		t.Error("VerifyIntegrity() should fail on corrupt transcript")
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	transcript := []byte(`{"uuid":"1","type":"user"}`)

	original, err := NewStoredConversation("session-1", "/test", "main", 1, transcript)
	if err != nil {
		t.Fatalf("NewStoredConversation() error: %v", err)
	}

	data, err := original.Marshal()
	if err != nil {
		t.Fatalf("Marshal() error: %v", err)
	}

	// Verify it's valid JSON
	if !json.Valid(data) {
		t.Fatal("Marshal() produced invalid JSON")
	}

	restored, err := UnmarshalStoredConversation(data)
	if err != nil {
		t.Fatalf("UnmarshalStoredConversation() error: %v", err)
	}

	if restored.SessionID != original.SessionID {
		t.Errorf("SessionID mismatch after round-trip")
	}
	if restored.Checksum != original.Checksum {
		t.Errorf("Checksum mismatch after round-trip")
	}
	if restored.Transcript != original.Transcript {
		t.Errorf("Transcript mismatch after round-trip")
	}

	// Verify the restored conversation still passes integrity check
	valid, err := restored.VerifyIntegrity()
	if err != nil {
		t.Fatalf("VerifyIntegrity() error after round-trip: %v", err)
	}
	if !valid {
		t.Error("VerifyIntegrity() failed after marshal/unmarshal round-trip")
	}
}

func TestUnmarshalInvalidJSON(t *testing.T) {
	_, err := UnmarshalStoredConversation([]byte("not json"))
	if err == nil {
		t.Error("UnmarshalStoredConversation() should fail on invalid JSON")
	}
}
