package engine

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/google/shlex"

	"github.com/Pratham-Mishra04/pulse/internal/log"
)

// Builder runs go build inside a debounced, cancellable queue.
// Only one build runs at a time — a new Enqueue() cancels any in-flight build.
type Builder struct {
	name string
	cfg  ServiceConfig
	log  *log.Logger

	// enqueueCh signals the worker goroutine that a new build is requested.
	// Buffered at 1: if a signal is already pending, duplicate signals are
	// dropped. This prevents a burst of file events from queuing N builds.
	enqueueCh chan struct{}

	// results is where the worker sends completed BuildResults.
	// Engine reads from this channel in its main select loop.
	results chan BuildResult

	// current holds the cancel function of the currently running build.
	// Buffered at 1 — acts as a semaphore. When a new Enqueue() arrives,
	// it drains this channel and calls cancel() to abort the in-flight build.
	// This is the fix for Air issue #784 (concurrent builds corrupting output).
	current chan context.CancelFunc
}

// BuildResult is the outcome of a single build attempt.
type BuildResult struct {
	// BinPath is the path to run the compiled binary (from ServiceConfig.Run).
	BinPath string

	// Output is the combined stdout+stderr from the build command.
	// Non-empty on failure; used to display the compiler error.
	Output string

	// Elapsed is the wall-clock time the build took.
	Elapsed time.Duration

	// Err is non-nil if the build command exited non-zero.
	Err error
}

func NewBuilder(name string, cfg ServiceConfig, l *log.Logger) *Builder {
	return &Builder{
		name:      name,
		cfg:       cfg,
		log:       l,
		enqueueCh: make(chan struct{}, 1),
		results:   make(chan BuildResult, 1),
		current:   make(chan context.CancelFunc, 1),
	}
}

// Enqueue schedules a new build. If a build is currently running, it is
// cancelled immediately. If a build is already pending in the queue, the
// duplicate signal is dropped — one pending build is sufficient.
func (b *Builder) Enqueue() {
	// Drain the semaphore and cancel the in-flight build if one exists.
	select {
	case cancel := <-b.current:
		cancel()
	default:
		// No build running — nothing to cancel.
	}

	// Signal the worker. Non-blocking: if enqueueCh already has a signal
	// pending, this file event is absorbed — the pending build will pick up
	// the latest state anyway.
	select {
	case b.enqueueCh <- struct{}{}:
	default:
	}
}

// Results returns the read-only channel Engine reads build outcomes from.
func (b *Builder) Results() <-chan BuildResult {
	return b.results
}

// Run starts the build worker loop. Blocks until ctx is cancelled.
// Must be called in a goroutine by Engine before the main event loop starts.
func (b *Builder) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return

		case <-b.enqueueCh:
			// Debounce: wait for the quiet period before building.
			// Any rapid file events that arrive during this window are already
			// absorbed by the buffered enqueueCh or dropped as duplicates.
			// Use select so a context cancellation exits immediately.
			select {
			case <-time.After(b.cfg.Debounce):
			case <-ctx.Done():
				return
			}

			// Create a cancellable context for this specific build.
			buildCtx, cancel := context.WithCancel(ctx)

			// Register the cancel func so the next Enqueue() can abort us.
			select {
			case b.current <- cancel:
			default:
				// Shouldn't happen — current was drained in Enqueue() — but
				// defend against it to avoid blocking.
			}

			result := b.Build(buildCtx)
			cancel() // Always release resources, even on success.

			// Drain current: we're done, so remove our cancel from the slot.
			// If Enqueue() already drained it (i.e. we were cancelled mid-build),
			// this is a no-op.
			select {
			case <-b.current:
			default:
			}

			// Send the result to Engine. If ctx was cancelled while we were
			// building, skip the send and exit cleanly.
			select {
			case b.results <- result:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Build executes the build command synchronously and returns the result.
// Exported so Engine can call it directly for the initial build at startup,
// before the worker goroutine and file watcher are running.
func (b *Builder) Build(ctx context.Context) BuildResult {
	start := time.Now()

	// Split the build string into command + args using a shell lexer so that
	// quoted arguments (e.g. -ldflags "-X main.Version=1.2.3 -X main.Build=abc")
	// are preserved as single tokens rather than split on whitespace.
	parts, err := shlex.Split(b.cfg.Build)
	if err != nil {
		return BuildResult{
			BinPath: b.cfg.Run,
			Output:  fmt.Sprintf("invalid build command %q: %v", b.cfg.Build, err),
			Elapsed: time.Since(start),
			Err:     fmt.Errorf("invalid build command: %w", err),
		}
	}
	if len(parts) == 0 {
		return BuildResult{
			BinPath: b.cfg.Run,
			Output:  "build command is empty",
			Elapsed: time.Since(start),
			Err:     fmt.Errorf("build command is empty"),
		}
	}
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Dir = b.cfg.Path

	// Capture stdout and stderr together — Go compiler writes errors to stderr,
	// but we want a single formatted block to display to the user.
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err = cmd.Run()
	return BuildResult{
		BinPath: b.cfg.Run,
		Output:  out.String(),
		Elapsed: time.Since(start),
		Err:     err,
	}
}
