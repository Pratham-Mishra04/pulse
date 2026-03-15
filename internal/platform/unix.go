//go:build !windows

package platform

import (
	"os/exec"
	"syscall"
)

type unixKiller struct{}

// NewKiller returns the Unix implementation of Killer.
func NewKiller() Killer {
	return &unixKiller{}
}

// Kill sends SIGTERM to the process group.
func (k *unixKiller) Kill(pid int) error {
	return syscall.Kill(-pid, syscall.SIGTERM)
}

// KillTree sends SIGKILL to the process group.
func (k *unixKiller) KillTree(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// SetPgid configures cmd to run in its own process group.
// Must be called before cmd.Start().
func SetPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
