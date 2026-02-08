package main

import (
	"os"

	"github.com/re-cinq/claudit/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
