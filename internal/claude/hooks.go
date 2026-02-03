package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Hook defines a Claude Code hook configuration
type Hook struct {
	Matcher string    `json:"matcher"` // Tool name pattern (e.g., "Bash", "Write", "Edit|Write")
	Hooks   []HookCmd `json:"hooks"`
}

// HookCmd defines a command to run for a hook
type HookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// HooksConfig represents the nested hooks configuration
type HooksConfig struct {
	PostToolUse []Hook `json:"PostToolUse,omitempty"`
}

// Settings represents Claude Code's settings.local.json structure
type Settings struct {
	Hooks HooksConfig            `json:"hooks,omitempty"`
	Other map[string]interface{} `json:"-"` // Preserve other settings
}

// ReadSettings reads the Claude settings file from the given directory
func ReadSettings(claudeDir string) (*Settings, error) {
	path := filepath.Join(claudeDir, "settings.local.json")

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Settings{Other: make(map[string]interface{})}, nil
		}
		return nil, err
	}

	// First unmarshal into a raw map to preserve unknown fields
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	// Extract known fields
	settings := &Settings{Other: make(map[string]interface{})}

	if hooks, ok := raw["hooks"]; ok {
		hookBytes, _ := json.Marshal(hooks)
		json.Unmarshal(hookBytes, &settings.Hooks)
		delete(raw, "hooks")
	}

	// Store remaining fields
	settings.Other = raw

	return settings, nil
}

// WriteSettings writes the settings to the Claude settings file
func WriteSettings(claudeDir string, settings *Settings) error {
	// Merge settings into output map
	output := make(map[string]interface{})
	for k, v := range settings.Other {
		output[k] = v
	}

	// Only include hooks if there are any configured
	if len(settings.Hooks.PostToolUse) > 0 {
		output["hooks"] = settings.Hooks
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}

	// Ensure directory exists
	if err := os.MkdirAll(claudeDir, 0755); err != nil {
		return err
	}

	path := filepath.Join(claudeDir, "settings.local.json")
	return os.WriteFile(path, data, 0644)
}

// AddClauditHook adds or updates the claudit store hook in settings
func AddClauditHook(settings *Settings) {
	clauditHook := Hook{
		Matcher: "Bash",
		Hooks: []HookCmd{
			{
				Type:    "command",
				Command: "claudit store",
				Timeout: 30,
			},
		},
	}

	// Check if claudit hook already exists and update it
	for i, hook := range settings.Hooks.PostToolUse {
		if hook.Matcher == "Bash" && len(hook.Hooks) > 0 {
			for _, h := range hook.Hooks {
				if h.Command == "claudit store" {
					// Already exists, update it
					settings.Hooks.PostToolUse[i] = clauditHook
					return
				}
			}
		}
	}

	// Add new hook
	settings.Hooks.PostToolUse = append(settings.Hooks.PostToolUse, clauditHook)
}
