package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/Pratham-Mishra04/pulse/internal/log"
)

// Engine coordinates the Watcher, Builder, and Runner for a single service.
// It owns no business logic — its only job is to wire the three components
// together and run the top-level event loop:
//
//	file change → Watcher → Builder (debounce + build) → Runner (stop + start)
//
// When proxy is configured, the Engine also manages a ReverseProxy and
// orchestrates zero-downtime restarts: new process warms up behind the proxy
// while the old one keeps serving, then swaps atomically when healthy.
type Engine struct {
	name    string
	cfg     ServiceConfig
	log     *log.Logger
	watcher *Watcher
	builder *Builder
	runner  *Runner
	proxy   *ReverseProxy // nil when proxy mode is not configured

	// proxyCancelFn cancels the in-flight health poll goroutine.
	// Reset on each proxyRestart call so a newer build can supersede a
	// previous warmup without waiting for its health check to complete.
	proxyCancelFn context.CancelFunc
	proxyMu       sync.Mutex // serializes concurrent proxyRestart calls
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

	e := &Engine{
		name:    name,
		cfg:     cfg,
		log:     l,
		watcher: watcher,
		builder: NewBuilder(name, cfg, l),
		runner:  NewRunner(name, cfg, l),
	}

	if cfg.Proxy != nil {
		e.proxy = NewReverseProxy(*cfg.Proxy, l)
	}

	return e, nil
}

// runPostHook executes the post hook after a successful build, before the
// process is swapped. Returns an error if the hook fails and post_strict is
// true — the caller should keep the old process alive without starting the new
// binary. When post_strict is false a failure is logged but the swap proceeds.
func (e *Engine) runPostHook(ctx context.Context) error {
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
			return fmt.Errorf("post hook failed: %w", err)
		}
	}
	return nil
}

// Run starts the watch → build → run loop and blocks until ctx is cancelled.
//
// Startup sequence (proxy mode):
//  1. Bind the public proxy address.
//  2. Build the binary immediately (synchronous).
//  3. Start the process on a dynamic internal port.
//  4. Poll health synchronously — nothing is serving yet, 503s are fine.
//  5. Swap the proxy backend once healthy.
//  6. Start the file watcher and builder worker goroutines.
//  7. Enter the event loop.
//
// Startup sequence (direct mode, no proxy):
//  1. Build the binary immediately.
//  2. Start the process.
//  3. Start the file watcher and builder worker goroutines.
//  4. Enter the event loop.
func (e *Engine) Run(ctx context.Context) error {
	// ── Proxy setup ───────────────────────────────────────────────────────────
	if e.proxy != nil {
		if err := e.proxy.Start(); err != nil {
			return err
		}
		defer e.proxy.Stop(ctx) //nolint:errcheck
		e.log.Info(fmt.Sprintf("proxy listening on %s", e.proxy.Addr()))
	}

	// ── Initial build ─────────────────────────────────────────────────────────
	result := e.builder.Build(ctx)
	if result.Err != nil {
		e.log.Error(fmt.Sprintf("initial build failed:\n%s", result.Output))
		return result.Err
	}
	if err := e.runPostHook(ctx); err != nil {
		e.log.Error(err.Error())
		return err
	}

	// ── Initial start ─────────────────────────────────────────────────────────
	if e.proxy != nil {
		// Proxy mode: pick a free port, start on it, poll health, then swap.
		// This is synchronous on first start — nothing is serving yet.
		if err := e.proxyStart(ctx, result); err != nil {
			return err
		}
	} else {
		if err := e.runner.Start(result, 0); err != nil {
			return err
		}
	}

	// ── File watcher ──────────────────────────────────────────────────────────
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

	// ── Event loop ────────────────────────────────────────────────────────────
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
			// Run the post hook before swapping — if strict and it fails,
			// keep the old process alive and wait for the next file change.
			if err := e.runPostHook(ctx); err != nil {
				e.log.Error(err.Error())
				e.log.Keeping(e.runner.Pid())
				continue
			}
			if e.proxy != nil {
				// Proxy mode: async zero-downtime restart.
				// The event loop is not blocked — new file changes can still
				// arrive while the new process is warming up.
				go e.proxyRestart(ctx, result)
			} else {
				// Direct mode: stop old process, start new one.
				if err := e.runner.Restart(result); err != nil {
					e.log.Error(err.Error())
					continue
				}
			}
		}
	}
}

// proxyStart handles the initial process launch in proxy mode.
// It is synchronous — the proxy returns 503 until this returns, which is
// acceptable on startup since no client expects the service to be up yet.
func (e *Engine) proxyStart(ctx context.Context, result BuildResult) error {
	port, err := freePort()
	if err != nil {
		return err
	}

	if err := e.runner.Start(result, port); err != nil {
		return err
	}

	e.log.Info(fmt.Sprintf("waiting for %s to be healthy on :%d...", e.name, port))

	poller := NewHealthPoller(port, *e.cfg.HealthCheck, e.log)
	if err := poller.Poll(ctx); err != nil {
		_ = e.runner.Stop()
		return fmt.Errorf("initial health check failed: %w", err)
	}

	e.proxy.SwapBackend(port)
	e.log.Info(fmt.Sprintf("%s is healthy — proxy now forwarding %s → :%d", e.name, e.proxy.Addr(), port))
	return nil
}

// proxyRestart performs a zero-downtime restart in proxy mode:
//  1. Cancels any in-flight warmup from a previous build.
//  2. Picks a free internal port and launches the new process there.
//  3. Polls health on that port asynchronously.
//  4. On success: atomically swaps the proxy backend, promotes pending,
//     and stops the old process in the background.
//  5. On failure: stops the pending process, old process keeps serving.
func (e *Engine) proxyRestart(ctx context.Context, result BuildResult) {
	e.proxyMu.Lock()
	defer e.proxyMu.Unlock()

	// Cancel any previous warmup and kill its candidate process.
	if e.proxyCancelFn != nil {
		e.proxyCancelFn()
		if err := e.runner.StopPending(); err != nil {
			e.log.Error("failed to stop previous candidate: " + err.Error())
		}
	}

	pollCtx, cancel := context.WithCancel(ctx)
	e.proxyCancelFn = cancel

	port, err := freePort()
	if err != nil {
		cancel()
		e.log.Error("failed to find free port: " + err.Error())
		return
	}

	oldCmd, oldPid := e.runner.CurrentCmdPid()

	if err := e.runner.LaunchNew(result, port); err != nil {
		cancel()
		e.log.Error("failed to launch new process: " + err.Error())
		return
	}

	e.log.Info(fmt.Sprintf("waiting for %s to be healthy on :%d...", e.name, port))

	go func() {
		poller := NewHealthPoller(port, *e.cfg.HealthCheck, e.log)
		if err := poller.Poll(pollCtx); err != nil {
			// Acquire the mutex before touching the pending slot. If our context
			// was cancelled it means a newer build already took over — don't stop
			// its candidate. Only stop if we are still the active warmup.
			e.proxyMu.Lock()
			select {
			case <-pollCtx.Done():
				e.proxyMu.Unlock()
				return
			default:
			}
			e.log.Error(fmt.Sprintf("health check failed — keeping old process: %v", err))
			_ = e.runner.StopPending()
			e.proxyMu.Unlock()
			cancel()
			return
		}

		// Acquire the proxy mutex to serialise the swap with any concurrent
		// proxyRestart call. Re-check cancellation under the lock — a newer
		// build may have cancelled us between Poll returning and here.
		e.proxyMu.Lock()
		select {
		case <-pollCtx.Done():
			e.proxyMu.Unlock()
			_ = e.runner.StopPending()
			return
		default:
		}

		e.proxy.SwapBackend(port)
		e.runner.PromotePending()
		e.log.Info(fmt.Sprintf("%s is healthy — proxy now forwarding %s → :%d", e.name, e.proxy.Addr(), port))
		e.proxyMu.Unlock()

		// Stop the old process in the background so we don't block the swap.
		go func() {
			e.log.Info(fmt.Sprintf("shutting down old %s process (pid %d)", e.name, oldPid))
			if err := e.runner.StopProcess(oldCmd, oldPid); err != nil {
				e.log.Error("failed to stop old process: " + err.Error())
			}
		}()

		cancel()
	}()
}
