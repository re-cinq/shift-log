package cmd

import (
	"fmt"

	"github.com/DanielJonesEB/claudit/internal/config"
	"github.com/spf13/cobra"
)

var (
	debugOn     bool
	debugOff    bool
	debugToggle bool
)

var debugCmd = &cobra.Command{
	Use:     "debug",
	Short:   "Toggle debug logging for claudit",
	GroupID: "human",
	Long: `Controls debug logging output for claudit commands.

When debug logging is enabled, claudit writes detailed diagnostic
information to stderr during all operations.

Examples:
  claudit debug          Show current debug state
  claudit debug --on     Enable debug logging
  claudit debug --off    Disable debug logging
  claudit debug --toggle Toggle debug logging`,
	RunE: runDebug,
}

func init() {
	debugCmd.Flags().BoolVar(&debugOn, "on", false, "Enable debug logging")
	debugCmd.Flags().BoolVar(&debugOff, "off", false, "Disable debug logging")
	debugCmd.Flags().BoolVar(&debugToggle, "toggle", false, "Toggle debug logging")
	debugCmd.MarkFlagsMutuallyExclusive("on", "off", "toggle")
	rootCmd.AddCommand(debugCmd)
}

func runDebug(cmd *cobra.Command, args []string) error {
	exists, err := config.DirExists()
	if err != nil {
		return fmt.Errorf("failed to check claudit directory: %w", err)
	}
	if !exists {
		return fmt.Errorf("claudit is not initialized in this repository (run 'claudit init' first)")
	}

	cfg, err := config.Read()
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	// If no flags, just show current state
	if !debugOn && !debugOff && !debugToggle {
		if cfg.Debug {
			fmt.Println("debug logging is on")
		} else {
			fmt.Println("debug logging is off")
		}
		return nil
	}

	switch {
	case debugOn:
		cfg.Debug = true
	case debugOff:
		cfg.Debug = false
	case debugToggle:
		cfg.Debug = !cfg.Debug
	}

	if err := config.Write(cfg); err != nil {
		return fmt.Errorf("failed to write config: %w", err)
	}

	if cfg.Debug {
		fmt.Println("debug logging is on")
	} else {
		fmt.Println("debug logging is off")
	}

	return nil
}
