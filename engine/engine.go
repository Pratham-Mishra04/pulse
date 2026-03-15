package engine

import (
	"context"
	"fmt"

	"github.com/Pratham-Mishra04/pulse/internal/log"
)

// Engine coordinates the Watcher, Builder, and Runner for a single service.
// It owns no business logic — its only job is to wire the three components
// together and run the top-level event loop:
//
//	file change → Watcher → Builder (debounce + build) → Runner (stop + start)
type Engine struct {
	name    string
	cfg     ServiceConfig
	log     *log.Logger
	watcher *Watcher
	builder *Builder
	runner  *Runner
}

// New creates an Engine for the named service. Returns an error if the
// Watcher cannot be initialised (e.g. malformed .gitignore).
func New(name string, cfg ServiceConfig, l *log.Logger) (*Engine, error) {
	// Detect go.work and collect any external "use" directories as extra
	// watch roots, unless the user has opted out via no_workspace: true.
	var extraRoots []string
	if !cfg.NoWorkspace {
		projectRoot := cfg.Path
		if projectRoot == "" {
			projectRoot = "."
		}
		if goWorkPath, found := detectGoWork(projectRoot); found {
			roots, err := externalWorkspaceDirs(goWorkPath, projectRoot)
			if err != nil {
				l.Verbose(fmt.Sprintf("workspace: failed to parse go.work: %v", err))
			} else if len(roots) > 0 {
				l.Info(fmt.Sprintf("workspace: watching %d external module(s) from go.work", len(roots)))
				extraRoots = roots
			}
		}
	}

	watcher, err := NewWatcher(cfg, extraRoots, l)
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %w", err)
	}
	return &Engine{
		name:    name,
		cfg:     cfg,
		log:     l,
		watcher: watcher,
		builder: NewBuilder(name, cfg, l),
		runner:  NewRunner(name, cfg, l),
	}, nil
}

// runPostHook executes the configured post hook and handles logging.
// When isInitial is true (first startup) and PostStrict is set, a failure
// returns an error so Run() can exit. On subsequent restarts (isInitial=false)
// the running process is never killed due to a post hook failure — any error
// is only logged.
func (e *Engine) runPostHook(ctx context.Context, isInitial bool) error {
	if e.cfg.Post == "" {
		return nil
	}
	out, elapsed, err := runHook(ctx, e.cfg.Post, e.cfg.Path)
	e.log.Hook("post", e.name, elapsed, err == nil)
	if err != nil {
		if out != "" {
			e.log.Error(out)
		}
		if e.cfg.PostStrict {
			if isInitial {
				return fmt.Errorf("post hook failed: %w", err)
			}
			e.log.Error(fmt.Sprintf("post hook failed: %v", err))
		}
		// Either way, the process is already running — we never kill it due
		// to a post hook failure (F-09 spec).
	}
	return nil
}

// Run starts the watch → build → run loop and blocks until ctx is cancelled.
//
// Startup sequence:
//  1. Build the binary immediately (synchronous, before any file watching).
//  2. Start the process.
//  3. Start the file watcher and builder worker goroutines.
//  4. Enter the event loop.
func (e *Engine) Run(ctx context.Context) error {
	// Step 1 & 2: initial build and start. If the initial build fails (or the
	// pre hook aborts it with pre_strict), Pulse exits — there is no previous
	// process to keep alive on first run.
	result := e.builder.Build(ctx)
	if result.Err != nil {
		e.log.Error(fmt.Sprintf("initial build failed:\n%s", result.Output))
		return result.Err
	}
	if err := e.runner.Start(result); err != nil {
		return err
	}
	// Run the post hook after the initial successful start.
	if err := e.runPostHook(ctx, true); err != nil {
		return err
	}

	// Step 3: start the file watcher.
	events, err := e.watcher.Start(ctx)
	if err != nil {
		runnerErr := e.runner.Stop()
		if runnerErr != nil {
			e.log.Error("failed to stop runner: " + runnerErr.Error())
		}
		return err
	}

	// Start the builder worker in the background. It blocks on enqueueCh,
	// runs the build after the debounce window, and sends results back here.
	go e.builder.Run(ctx)

	// Step 4: event loop.
	for {
		select {
		case <-ctx.Done():
			// Context cancelled (Ctrl+C) — stop the running process and exit.
			return e.runner.Stop()

		case file, ok := <-events:
			if !ok {
				// Watcher closed its channel — shut down cleanly.
				return e.runner.Stop()
			}
			e.log.Watch(file)
			// Signal the builder. Enqueue() cancels any in-flight build so
			// there is never more than one build running at a time.
			e.builder.Enqueue()

		case result, ok := <-e.builder.Results():
			if !ok {
				// Builder goroutine exited unexpectedly — shut down cleanly.
				return e.runner.Stop()
			}
			// Only log a build line when a compile step actually ran.
			// Plain-process services (no build command) skip straight to restart.
			if !result.NoBuild {
				e.log.Build(e.name, result.Elapsed, result.Err == nil)
			}
			if result.Err != nil {
				// Build failed (or pre hook aborted it) — log the compiler
				// output and keep the old process running.
				e.log.Error(result.Output)
				e.log.Keeping(e.runner.Pid())
				continue
			}
			// Build succeeded — stop the old process, start the new binary.
			if err := e.runner.Restart(result); err != nil {
				e.log.Error(err.Error())
				continue
			}
			// Run the post hook after a successful restart.
			if err := e.runPostHook(ctx, false); err != nil {
				e.log.Error(err.Error())
			}
		}
	}
}
