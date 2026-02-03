package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadConfig(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "claudit-config-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	t.Run("returns default when config doesn't exist", func(t *testing.T) {
		cfg, err := Read()
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if cfg.NotesRef != DefaultNotesRef {
			t.Errorf("expected %q, got %q", DefaultNotesRef, cfg.NotesRef)
		}
	})

	t.Run("reads existing config", func(t *testing.T) {
		// Create config file
		if err := os.MkdirAll(".claudit", 0755); err != nil {
			t.Fatalf("failed to create .claudit: %v", err)
		}
		configContent := `{"notes_ref":"refs/notes/test"}`
		if err := os.WriteFile(".claudit/config", []byte(configContent), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		cfg, err := Read()
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if cfg.NotesRef != "refs/notes/test" {
			t.Errorf("expected refs/notes/test, got %q", cfg.NotesRef)
		}

		// Cleanup
		os.RemoveAll(".claudit")
	})

	t.Run("returns default on malformed config", func(t *testing.T) {
		// Create malformed config
		if err := os.MkdirAll(".claudit", 0755); err != nil {
			t.Fatalf("failed to create .claudit: %v", err)
		}
		if err := os.WriteFile(".claudit/config", []byte("invalid json"), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		cfg, err := Read()
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if cfg.NotesRef != DefaultNotesRef {
			t.Errorf("expected default ref on malformed config, got %q", cfg.NotesRef)
		}

		// Cleanup
		os.RemoveAll(".claudit")
	})
}

func TestWriteConfig(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "claudit-config-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to temp directory
	oldWd, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(oldWd)

	t.Run("writes config to file", func(t *testing.T) {
		cfg := &Config{
			NotesRef: CustomNotesRef,
		}

		if err := Write(cfg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		// Verify file exists
		configPath := filepath.Join(".claudit", "config")
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			t.Error("config file was not created")
		}

		// Read back and verify
		readCfg, err := Read()
		if err != nil {
			t.Errorf("unexpected error reading config: %v", err)
		}
		if readCfg.NotesRef != CustomNotesRef {
			t.Errorf("expected %q, got %q", CustomNotesRef, readCfg.NotesRef)
		}
	})

	t.Run("creates .claudit directory if needed", func(t *testing.T) {
		// Remove .claudit directory
		os.RemoveAll(".claudit")

		cfg := &Config{
			NotesRef: DefaultNotesRef,
		}

		if err := Write(cfg); err != nil {
			t.Errorf("unexpected error: %v", err)
		}

		// Verify directory was created
		if _, err := os.Stat(".claudit"); os.IsNotExist(err) {
			t.Error(".claudit directory was not created")
		}
	})
}
