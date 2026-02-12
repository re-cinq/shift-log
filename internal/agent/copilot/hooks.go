package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// HooksFile represents the Copilot CLI hooks.json structure.
// Format: {"version": 1, "hooks": {"postToolUse": [{"type":"bash","bash":"...","timeoutSec":30}]}}
type HooksFile struct {
	Version int                    `json:"version"`
	Hooks   map[string][]HookEntry `json:"hooks"`
	Other   map[string]interface{} `json:"-"`
}

// HookEntry represents a single hook entry in hooks.json.
type HookEntry struct {
	Type       string `json:"type"`
	Bash       string `json:"bash"`
	TimeoutSec int    `json:"timeoutSec,omitempty"`
}

// hooksFilePath returns the path to hooks.json at the repo root.
func hooksFilePath(repoRoot string) string {
	return filepath.Join(repoRoot, "hooks.json")
}

// ReadHooksFile reads the Copilot hooks file from the repo root.
func ReadHooksFile(repoRoot string) (*HooksFile, error) {
	path := hooksFilePath(repoRoot)

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &HooksFile{
				Version: 1,
				Hooks:   make(map[string][]HookEntry),
				Other:   make(map[string]interface{}),
			}, nil
		}
		return nil, err
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}

	hf := &HooksFile{
		Version: 1,
		Hooks:   make(map[string][]HookEntry),
		Other:   make(map[string]interface{}),
	}

	if v, ok := raw["version"].(float64); ok {
		hf.Version = int(v)
	}

	if hooks, ok := raw["hooks"]; ok {
		hookBytes, _ := json.Marshal(hooks)
		_ = json.Unmarshal(hookBytes, &hf.Hooks)
		delete(raw, "hooks")
	}

	delete(raw, "version")
	hf.Other = raw

	return hf, nil
}

// WriteHooksFile writes the hooks file to the repo root.
func WriteHooksFile(repoRoot string, hf *HooksFile) error {
	output := make(map[string]interface{})
	for k, v := range hf.Other {
		output[k] = v
	}

	output["version"] = hf.Version
	if len(hf.Hooks) > 0 {
		output["hooks"] = hf.Hooks
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}

	path := hooksFilePath(repoRoot)
	return os.WriteFile(path, data, 0644)
}

// AddClauditHooks adds the claudit store postToolUse hook entry.
func AddClauditHooks(hf *HooksFile) {
	entry := HookEntry{
		Type:       "bash",
		Bash:       "claudit store --agent=copilot",
		TimeoutSec: 30,
	}

	hf.Hooks["postToolUse"] = addOrUpdateHookEntry(
		hf.Hooks["postToolUse"], entry, "claudit store",
	)
}

// addOrUpdateHookEntry adds or updates a hook entry matching by bash command prefix.
func addOrUpdateHookEntry(entries []HookEntry, newEntry HookEntry, prefix string) []HookEntry {
	for i, e := range entries {
		if strings.Contains(e.Bash, prefix) {
			entries[i] = newEntry
			return entries
		}
	}
	return append(entries, newEntry)
}
