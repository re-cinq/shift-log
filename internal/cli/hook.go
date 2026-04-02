package cli

import (
	"encoding/json"
	"os"
)

// ReadHookInput reads a single JSON object from stdin and unmarshals it into
// the provided struct. It uses a JSON decoder rather than io.ReadAll so that
// it returns as soon as one complete JSON object has been read, without
// blocking on EOF. This is necessary for agent CLIs (e.g. Gemini CLI v0.29+)
// that do not close stdin after writing the hook payload.
func ReadHookInput(v interface{}) error {
	if err := json.NewDecoder(os.Stdin).Decode(v); err != nil {
		LogWarning("failed to read/parse hook JSON: %v", err)
		return err
	}
	return nil
}
