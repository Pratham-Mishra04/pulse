package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Pratham-Mishra04/pulse/internal/log"
)

// Version is set at build time via -ldflags.
var Version = "dev"

// Global flags shared across commands.
var (
	flagQuiet   bool
	flagVerbose bool
	flagNoColor bool
	flagConfig  string
)

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:   "pulse",
	Short: "Pulse — live reload for Go. Your server stays alive when a build fails.",
	// Running `pulse` with no subcommand is the same as `pulse run`.
	RunE: runCmd.RunE,
}

func init() {
	// Persistent flags are available to every subcommand.
	rootCmd.PersistentFlags().BoolVarP(&flagQuiet, "quiet", "q", false, "only show errors and restarts")
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "show all file events including ignored ones")
	rootCmd.PersistentFlags().BoolVar(&flagNoColor, "no-color", false, "disable ANSI color output")
	rootCmd.PersistentFlags().StringVar(&flagConfig, "config", "pulse.yaml", "path to pulse.yaml")

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(versionCmd)

	// Suppress the default cobra completion command — not needed.
	rootCmd.CompletionOptions.DisableDefaultCmd = true

	// Don't print usage on every error — the error message is sufficient.
	rootCmd.SilenceUsage = true
}

// newLogger builds a Logger from the global flags.
func newLogger() *log.Logger {
	level := log.LogLevelInfo
	if flagQuiet {
		level = log.LogLevelQuiet
	} else if flagVerbose {
		level = log.LogLevelVerbose
	}
	return log.New(level, flagNoColor)
}
