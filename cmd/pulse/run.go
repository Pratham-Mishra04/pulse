package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/Pratham-Mishra04/pulse/engine"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Start watching and rebuilding (default command)",
	// Hidden so `pulse run` works but isn't shown in help — users just type `pulse`.
	Hidden: true,
	RunE:   runPulse,
}

func runPulse(cmd *cobra.Command, args []string) error {
	l := newLogger()

	// Hard error if pulse.yaml doesn't exist — we need the entrypoint.
	if _, err := os.Stat(flagConfig); os.IsNotExist(err) {
		return fmt.Errorf("no pulse.yaml found\n         run `pulse init <path>` to get started")
	}

	cfg, err := engine.LoadConfig(flagConfig)
	if err != nil {
		return fmt.Errorf("failed to load %s: %w", flagConfig, err)
	}
	if len(cfg.Services) == 0 {
		return fmt.Errorf("no services defined in %s", flagConfig)
	}

	// Two-stage Ctrl+C:
	//   First signal  → cancel context → graceful shutdown (SIGTERM to child)
	//   Second signal → os.Exit(1)     → hard exit if child is stuck
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigs)

	go func() {
		select {
		case <-sigs:
		case <-ctx.Done():
			return // program exited normally — no signal received
		}
		l.Info("shutting down...")
		cancel() // first signal: graceful

		select {
		case <-sigs:
			l.Info("force exit")
			os.Exit(1) // second signal: hard exit
		case <-ctx.Done():
			return
		}
	}()

	// Single service — start directly.
	// Multi-service orchestration is Tier 2 (F-10).
	if len(cfg.Services) == 1 {
		for name, svc := range cfg.Services {
			e, err := engine.New(name, svc, l)
			if err != nil {
				return err
			}
			return e.Run(ctx)
		}
	}

	// Multi-service: run each engine in its own goroutine and wait for all.
	// Each engine gets a logger prefixed with its service name so interleaved
	// output is immediately attributable: "api [build] 230ms ✓"
	errs := make(chan error, len(cfg.Services))
	for name, svc := range cfg.Services {
		e, err := engine.New(name, svc, l.WithPrefix(name))
		if err != nil {
			return err
		}
		eng := e // capture loop variable before the goroutine closes over it
		go func() {
			errs <- eng.Run(ctx)
		}()
	}

	// Collect errors — return the first non-nil one.
	var firstErr error
	for range cfg.Services {
		if err := <-errs; err != nil && firstErr == nil {
			firstErr = err
			cancel() // cancel remaining services on first failure
		}
	}
	return firstErr
}
