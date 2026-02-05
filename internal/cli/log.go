package cli

import (
	"fmt"
	"os"
)

// LogWarning prints a warning message to stderr with the claudit prefix
func LogWarning(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "claudit: warning: "+format+"\n", args...)
}

// LogInfo prints an info message to stderr with the claudit prefix
func LogInfo(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "claudit: "+format+"\n", args...)
}
