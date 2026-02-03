package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultNotesRef is the default git notes ref (standard git behavior)
const DefaultNotesRef = "refs/notes/commits"

// CustomNotesRef is the custom ref for namespace separation
const CustomNotesRef = "refs/notes/claude-conversations"

// Config represents claudit's configuration
type Config struct {
	NotesRef string `json:"notes_ref"`
}

// Read loads configuration from .claudit/config
// Returns default config if file doesn't exist
func Read() (*Config, error) {
	configPath, err := getConfigPath()
	if err != nil {
		return defaultConfig(), nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultConfig(), nil
		}
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		// Return default on malformed config
		return defaultConfig(), nil
	}

	// Validate notes ref
	if cfg.NotesRef == "" {
		cfg.NotesRef = DefaultNotesRef
	}

	return &cfg, nil
}

// Exists checks if the config file exists
func Exists() bool {
	configPath, err := getConfigPath()
	if err != nil {
		return false
	}

	_, err = os.Stat(configPath)
	return err == nil
}

// Write saves configuration to .claudit/config
func Write(cfg *Config) error {
	configPath, err := getConfigPath()
	if err != nil {
		return err
	}

	// Ensure .claudit directory exists
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal config to JSON
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write config file
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	return nil
}

// defaultConfig returns the default configuration
func defaultConfig() *Config {
	return &Config{
		NotesRef: DefaultNotesRef,
	}
}

// getConfigPath returns the path to .claudit/config
func getConfigPath() (string, error) {
	// Get git repository root
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		// Fall back to current directory if not in a git repo
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		return filepath.Join(cwd, ".claudit", "config"), nil
	}

	repoRoot := strings.TrimSpace(string(output))
	return filepath.Join(repoRoot, ".claudit", "config"), nil
}
