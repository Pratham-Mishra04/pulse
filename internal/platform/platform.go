package platform

// Killer handles cross-platform process tree termination.
// unix.go provides the Unix implementation; windows.go provides the Windows implementation.
type Killer interface {
	// Kill sends a graceful signal (SIGTERM on Unix, CTRL_BREAK on Windows)
	// to the process group / job object.
	Kill(pid int) error

	// KillTree forcefully terminates the entire process tree.
	// Called after kill-timeout expires.
	KillTree(pid int) error
}
