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
type Runner struct {
	name   string
	cfg    ServiceConfig
	log    *log.Logger
	killer platform.Killer // platform-specific kill implementation

	// mu guards cmd and pid — both are read and written from different goroutines.
	mu  sync.Mutex
	cmd *exec.Cmd
	pid int

	// stdinPipe is the write end of the child's stdin pipe.
	// It is replaced on each restart while the forwardStdin goroutine is running.
	// stdinMu guards access to stdinPipe during the swap.
	stdinPipe io.WriteCloser
	stdinMu   sync.Mutex
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
func (r *Runner) Start(result BuildResult) error {
	return r.launch(result)
}

// Restart gracefully stops the current process then starts the new binary.
// This is the core of Pulse's build-first lifecycle: Stop() is only called
// after a successful build — never before.
func (r *Runner) Restart(result BuildResult) error {
	if err := r.Stop(); err != nil {
		return err
	}
	return r.launch(result)
}

// Stop sends SIGTERM to the process group and waits up to KillTimeout for
// the process to exit cleanly. If it hasn't exited by then, SIGKILL is sent.
func (r *Runner) Stop() error {
	r.mu.Lock()
	cmd := r.cmd
	pid := r.pid
	r.mu.Unlock()

	// Nothing running — no-op.
	if cmd == nil || pid == 0 {
		return nil
	}

	// Graceful shutdown: SIGTERM to the entire process group.
	if err := r.killer.Kill(pid); err != nil {
		if strings.Contains(err.Error(), "no such process") || strings.Contains(err.Error(), "process already finished") {
			// Process already exited on its own — reap it and clear state.
			err := cmd.Wait()
			if err != nil {
				return fmt.Errorf("failed to wait for process: %w", err)
			}
			r.mu.Lock()
			r.cmd = nil
			r.pid = 0
			r.mu.Unlock()
			return nil
		}
		return fmt.Errorf("failed to kill process: %w", err)
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

	r.mu.Lock()
	r.cmd = nil
	r.pid = 0
	r.mu.Unlock()
	return nil
}

// Pid returns the pid of the currently running process, or 0 if none.
func (r *Runner) Pid() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pid
}

// launch starts the binary from result.BinPath and records the cmd and pid.
//
// Intentionally uses exec.Command (not exec.CommandContext) so that the
// engine context cancelling does not race-kill the child with SIGKILL before
// runner.Stop() can send SIGTERM. Shutdown is always driven by runner.Stop(),
// which is called explicitly by the engine on ctx.Done() — this preserves the
// graceful SIGTERM → KillTimeout → SIGKILL path and lets the child flush its
// closing logs on both Ctrl-C and restart.
func (r *Runner) launch(result BuildResult) error {
	parts, err := shlex.Split(result.BinPath)
	if err != nil {
		return fmt.Errorf("failed to parse run command: %w", err)
	}
	if len(parts) == 0 {
		return fmt.Errorf("run command is empty")
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	if r.cfg.Path != "" {
		cmd.Dir = r.cfg.Path
	}
	cmd.Stdout = os.Stdout // child output flows directly to the terminal
	cmd.Stderr = os.Stderr
	cmd.Env = buildEnv(r.cfg.Env)

	// Put the child in its own process group so we can kill the entire tree.
	// On Unix this sets Setpgid=true. On Windows this is a no-op (Tier 2).
	platform.SetPgid(cmd)

	if !r.cfg.NoStdin {
		// StdinPipe() must be called before cmd.Start().
		// We swap this pipe pointer (under stdinMu) so the already-running
		// forwardStdin goroutine writes to the new process after restart.
		pipe, err := cmd.StdinPipe()
		if err != nil {
			return err
		}
		r.stdinMu.Lock()
		r.stdinPipe = pipe
		r.stdinMu.Unlock()
	}

	if err := cmd.Start(); err != nil {
		return err
	}

	r.mu.Lock()
	r.cmd = cmd
	r.pid = cmd.Process.Pid
	r.mu.Unlock()

	r.log.Restart(r.name, cmd.Process.Pid)
	return nil
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
func buildEnv(extra map[string]string) []string {
	if len(extra) == 0 {
		return os.Environ()
	}
	// Index parent env by key so we can overwrite duplicates deterministically.
	env := os.Environ()
	index := make(map[string]int, len(env))
	for i, entry := range env {
		if eq := strings.IndexByte(entry, '='); eq >= 0 {
			index[entry[:eq]] = i
		}
	}
	for k, v := range extra {
		if i, exists := index[k]; exists {
			env[i] = k + "=" + v
		} else {
			env = append(env, k+"="+v)
		}
	}
	return env
}
