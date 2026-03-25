package engine

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/shlex"

	"github.com/Pratham-Mishra04/pulse/internal/log"
	"github.com/Pratham-Mishra04/pulse/internal/platform"
)

// Runner manages the lifecycle of the running child process.
// It is responsible for:
//   - Launching the binary after a successful build
//   - Gracefully stopping it before a restart (SIGTERM → timeout → SIGKILL)
//   - Forwarding stdin from the parent process to the child on each restart
//
// In proxy mode, Runner also holds a "pending" slot for a newly launched
// process that has not yet been promoted (i.e. health check is still in
// progress). The active process continues serving while the pending one
// warms up. On promotion, pending becomes active and the old active process
// is stopped in the background.
type Runner struct {
	name   string
	cfg    ServiceConfig
	log    *log.Logger
	killer platform.Killer // platform-specific kill implementation

	// mu guards cmd, pid, pendingCmd, and pendingPid.
	mu         sync.Mutex
	cmd        *exec.Cmd
	pid        int
	pendingCmd *exec.Cmd
	pendingPid int

	// stdinPipe is the write end of the active child's stdin pipe.
	// It is replaced on each restart while the forwardStdin goroutine is running.
	// stdinMu guards access to stdinPipe during the swap.
	stdinPipe    io.WriteCloser
	stdinMu      sync.Mutex
	pendingPipe  io.WriteCloser // stdin pipe for the pending process (not yet active)
}

func NewRunner(name string, cfg ServiceConfig, l *log.Logger) *Runner {
	r := &Runner{
		name:   name,
		cfg:    cfg,
		log:    l,
		killer: platform.NewKiller(),
	}
	// Start the stdin forwarding goroutine once for the session lifetime.
	// It runs continuously and writes into whatever stdinPipe is current.
	if !cfg.NoStdin {
		go r.forwardStdin()
	}
	return r
}

// Start launches the process for the first time.
// portOverride, when non-zero, injects PORT=<portOverride> into the process env.
// Used in proxy mode so the process binds on the internal dynamic port.
func (r *Runner) Start(result BuildResult, portOverride int) error {
	return r.launch(result, true, portOverride)
}

// Restart gracefully stops the current process then starts the new binary.
// This is the core of Pulse's build-first lifecycle: Stop() is only called
// after a successful build — never before.
func (r *Runner) Restart(result BuildResult) error {
	if err := r.Stop(); err != nil {
		return err
	}
	return r.launch(result, false, 0)
}

// LaunchNew starts the new binary on internalPort WITHOUT stopping the current
// process. Used in proxy mode: the old process keeps serving live traffic
// while the new one warms up. The new process is stored in the pending slot.
//
// If a previous pending process exists it is stopped before the new one starts.
func (r *Runner) LaunchNew(result BuildResult, internalPort int) error {
	// Clear any leftover pending process from a previous (cancelled) warmup.
	if err := r.StopPending(); err != nil {
		return err
	}
	return r.launchPending(result, internalPort)
}

// PromotePending atomically moves the pending process into the active slot.
// Must only be called after the health check has passed.
// Stdin is switched to the new process immediately.
func (r *Runner) PromotePending() {
	r.mu.Lock()
	r.cmd = r.pendingCmd
	r.pid = r.pendingPid
	r.pendingCmd = nil
	r.pendingPid = 0

	newPipe := r.pendingPipe
	r.pendingPipe = nil
	r.mu.Unlock()

	if newPipe != nil {
		r.stdinMu.Lock()
		r.stdinPipe = newPipe
		r.stdinMu.Unlock()
	}
}

// StopPending kills the pending (warming-up) process without promoting it.
// Used when the health check fails or a newer build supersedes this one.
// No-op if there is no pending process.
func (r *Runner) StopPending() error {
	r.mu.Lock()
	cmd := r.pendingCmd
	pid := r.pendingPid
	r.pendingCmd = nil
	r.pendingPid = 0
	r.pendingPipe = nil
	r.mu.Unlock()

	if cmd == nil || pid == 0 {
		return nil
	}
	return r.stopProcess(cmd, pid)
}

// StopProcess stops an arbitrary cmd/pid pair using the same graceful shutdown
// logic as Stop(). Used to stop the old active process after promotion.
// Intended to be called in a background goroutine so it does not block the swap.
func (r *Runner) StopProcess(cmd *exec.Cmd, pid int) error {
	return r.stopProcess(cmd, pid)
}

// CurrentCmdPid returns the active cmd and pid under the mutex.
// Used by the engine to capture the old process before launching a new one.
func (r *Runner) CurrentCmdPid() (*exec.Cmd, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmd, r.pid
}

// Stop sends SIGTERM to the process group and waits up to KillTimeout for
// the process to exit cleanly. If it hasn't exited by then, SIGKILL is sent.
func (r *Runner) Stop() error {
	r.mu.Lock()
	cmd := r.cmd
	pid := r.pid
	r.mu.Unlock()

	if cmd == nil || pid == 0 {
		return nil
	}

	if err := r.stopProcess(cmd, pid); err != nil {
		return err
	}

	r.mu.Lock()
	r.cmd = nil
	r.pid = 0
	r.mu.Unlock()
	return nil
}

// stopProcess is the shared implementation for gracefully stopping any
// cmd/pid pair: SIGTERM → KillTimeout → SIGKILL.
func (r *Runner) stopProcess(cmd *exec.Cmd, pid int) error {
	// Graceful shutdown: SIGTERM to the entire process group.
	if err := r.killer.Kill(pid); err != nil {
		if strings.Contains(err.Error(), "no such process") || strings.Contains(err.Error(), "process already finished") {
			// Process already exited on its own — reap it.
			_ = cmd.Wait()
			return nil
		}
		// Graceful kill failed (e.g. Windows console mismatch where
		// GenerateConsoleCtrlEvent cannot reach the target) — fall back
		// to force-killing the process tree immediately.
		if err := r.killer.KillTree(pid); err != nil {
			return fmt.Errorf("failed to kill process: %w", err)
		}
		_ = cmd.Wait()
		return nil
	}

	// Wait for the process to exit in a goroutine so we can apply a timeout.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
		// Process exited within the timeout — clean shutdown.
	case <-time.After(r.cfg.KillTimeout):
		// Timed out — escalate to SIGKILL on the entire process tree.
		if err := r.killer.KillTree(pid); err != nil {
			return fmt.Errorf("failed to kill process tree: %w", err)
		}
		<-done // wait for Wait() to return after SIGKILL
	}
	return nil
}

// Pid returns the pid of the currently running process, or 0 if none.
func (r *Runner) Pid() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pid
}

// launch starts the binary from result.BinPath, records cmd and pid in the
// active slot, and logs the start/restart event.
//
// portOverride, when non-zero, injects PORT=<portOverride> into the env,
// overriding any PORT set in the service's env: config. Used in proxy mode.
//
// Intentionally uses exec.Command (not exec.CommandContext) so that the
// engine context cancelling does not race-kill the child with SIGKILL before
// runner.Stop() can send SIGTERM. Shutdown is always driven by runner.Stop(),
// which is called explicitly by the engine on ctx.Done() — this preserves the
// graceful SIGTERM → KillTimeout → SIGKILL path and lets the child flush its
// closing logs on both Ctrl-C and restart.
func (r *Runner) launch(result BuildResult, isInitial bool, portOverride int) error {
	cmd, pipe, err := r.buildCmd(result, portOverride)
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		if pipe != nil {
			_ = pipe.Close()
		}
		return err
	}

	if pipe != nil {
		r.stdinMu.Lock()
		r.stdinPipe = pipe
		r.stdinMu.Unlock()
	}

	r.mu.Lock()
	r.cmd = cmd
	r.pid = cmd.Process.Pid
	r.mu.Unlock()

	if isInitial {
		r.log.Start(r.name, cmd.Process.Pid)
	} else {
		r.log.Restart(r.name, cmd.Process.Pid)
	}
	return nil
}

// launchPending starts the binary and stores it in the pending slot without
// touching the active cmd/pid. Used in proxy mode during warmup.
func (r *Runner) launchPending(result BuildResult, portOverride int) error {
	cmd, pipe, err := r.buildCmd(result, portOverride)
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		if pipe != nil {
			_ = pipe.Close()
		}
		return err
	}

	r.mu.Lock()
	r.pendingCmd = cmd
	r.pendingPid = cmd.Process.Pid
	r.pendingPipe = pipe
	r.mu.Unlock()

	r.log.Start(r.name+" (warming up)", cmd.Process.Pid)
	return nil
}

// buildCmd constructs an exec.Cmd for result, applying portOverride if non-zero.
// Returns the cmd and the stdin pipe (nil when NoStdin is set).
func (r *Runner) buildCmd(result BuildResult, portOverride int) (*exec.Cmd, io.WriteCloser, error) {
	parts, err := shlex.Split(result.BinPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse run command: %w", err)
	}
	if len(parts) == 0 {
		return nil, nil, fmt.Errorf("run command is empty")
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	if r.cfg.Path != "" {
		cmd.Dir = r.cfg.Path
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = buildEnv(r.cfg.Env, portOverride)

	// Put the child in its own process group so we can kill the entire tree.
	// On Unix this sets Setpgid=true; on Windows it sets CREATE_NEW_PROCESS_GROUP.
	platform.SetPgid(cmd)

	var pipe io.WriteCloser
	if !r.cfg.NoStdin {
		// StdinPipe() must be called before cmd.Start().
		pipe, err = cmd.StdinPipe()
		if err != nil {
			return nil, nil, err
		}
	}
	return cmd, pipe, nil
}

// forwardStdin runs for the lifetime of the Pulse session (started once in
// NewRunner). It continuously reads from the terminal stdin and writes into
// the current child's stdin pipe. When the child restarts, launch() swaps
// stdinPipe under stdinMu, so this goroutine automatically writes to the
// new process without needing to be restarted itself.
func (r *Runner) forwardStdin() {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			r.stdinMu.Lock()
			if r.stdinPipe != nil {
				_, _ = r.stdinPipe.Write(buf[:n])
			}
			r.stdinMu.Unlock()
		}
		if err != nil {
			// os.Stdin closed (e.g. piped input finished) — stop forwarding.
			return
		}
	}
}

// buildEnv merges the service's env vars on top of the parent process environment.
// Parent env is inherited first so the child has access to PATH, HOME, etc.
// Extra vars are applied last and win over any duplicate keys from the parent.
//
// portOverride, when non-zero, injects PORT=<portOverride> last, taking
// precedence over any PORT value the user set in env:. Used in proxy mode so
// the process binds on the dynamic internal port Pulse picked.
func buildEnv(extra map[string]string, portOverride int) []string {
	env := os.Environ()
	// Index parent env by key so we can overwrite duplicates deterministically.
	index := make(map[string]int, len(env))
	for i, entry := range env {
		if k, _, ok := strings.Cut(entry, "="); ok {
			index[k] = i
		}
	}
	set := func(k, v string) {
		if i, exists := index[k]; exists {
			env[i] = k + "=" + v
		} else {
			index[k] = len(env)
			env = append(env, k+"="+v)
		}
	}
	for k, v := range extra {
		set(k, v)
	}
	if portOverride != 0 {
		set("PORT", fmt.Sprintf("%d", portOverride))
	}
	return env
}
