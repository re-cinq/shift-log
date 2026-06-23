package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// ReadHookInput reads JSON from stdin and unmarshals it into the provided struct.
// Uses a short timeout so that agents which do not close the hook command's stdin
// (e.g. Gemini CLI ≥ v0.29.7) do not block indefinitely.
func ReadHookInput(v interface{}) error {
	input, err := readStdinWithTimeout(3 * time.Second)
	if err != nil {
		LogDebug("hook stdin did not close within timeout: %v", err)
		return err
	}

	if err := json.Unmarshal(input, v); err != nil {
		LogWarning("failed to parse hook JSON: %v", err)
		return err
	}

	return nil
}

// readStdinWithTimeout reads all of stdin, returning an error if stdin does not
// reach EOF within d. This prevents shiftlog hook sub-commands from blocking
// forever when the parent agent keeps the write-end of the pipe open.
func readStdinWithTimeout(d time.Duration) ([]byte, error) {
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(os.Stdin)
		ch <- result{data, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			return nil, fmt.Errorf("failed to read stdin: %w", res.err)
		}
		return res.data, nil
	case <-time.After(d):
		return nil, fmt.Errorf("stdin read timeout after %v", d)
	}
}
