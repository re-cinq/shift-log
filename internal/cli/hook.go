package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// stdinReadTimeout is the maximum time to wait for stdin data.
// Prevents hook commands from hanging when the agent CLI opens a pipe for stdin
// but never closes the write end (observed in some agent CLI versions).
const stdinReadTimeout = 10 * time.Second

// ReadStdin reads all data from stdin without blocking indefinitely.
// Returns nil bytes (no error) when stdin is a character device (terminal or /dev/null),
// or when the pipe is not closed within stdinReadTimeout.
func ReadStdin() ([]byte, error) {
	fi, err := os.Stdin.Stat()
	if err == nil && (fi.Mode()&os.ModeCharDevice) != 0 {
		// stdin is a terminal or character device like /dev/null — not a pipe
		return nil, nil
	}

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
		return res.data, res.err
	case <-time.After(stdinReadTimeout):
		return nil, nil
	}
}

// ReadHookInput reads JSON from stdin and unmarshals it into the provided struct.
// Returns an error if stdin has no data or contains invalid JSON.
// Callers should treat all errors as non-fatal to avoid disrupting the agent workflow.
func ReadHookInput(v interface{}) error {
	input, err := ReadStdin()
	if err != nil {
		LogWarning("failed to read stdin: %v", err)
		return err
	}

	if len(input) == 0 {
		return fmt.Errorf("no hook data on stdin")
	}

	if err := json.Unmarshal(input, v); err != nil {
		LogWarning("failed to parse hook JSON: %v", err)
		return err
	}

	return nil
}
