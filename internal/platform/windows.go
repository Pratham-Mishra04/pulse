//go:build windows

package platform

import (
	"fmt"
	"os/exec"
	"syscall"
)

// kernel32 APIs loaded once at startup — kernel32.dll is always present on Windows.
var (
	kernel32                 = syscall.MustLoadDLL("kernel32.dll")
	generateConsoleCtrlEvent = kernel32.MustFindProc("GenerateConsoleCtrlEvent")
)

type windowsKiller struct{}

// NewKiller returns the Windows implementation of Killer.
func NewKiller() Killer {
	return &windowsKiller{}
}

// Kill sends CTRL_BREAK_EVENT to the process, which is the closest Windows
// equivalent to SIGTERM. The child process can intercept this signal via
// SetConsoleCtrlHandler. Requires the child to have been started with
// CREATE_NEW_PROCESS_GROUP (see SetPgid).
func (k *windowsKiller) Kill(pid int) error {
	r, _, e := generateConsoleCtrlEvent.Call(syscall.CTRL_BREAK_EVENT, uintptr(pid))
	if r == 0 {
		// ERROR_INVALID_PARAMETER (87 / 0x57) is returned when the pid/group no longer exists.
		if e == syscall.Errno(0x57) {
			return fmt.Errorf("no such process")
		}
		return fmt.Errorf("GenerateConsoleCtrlEvent: %w", e)
	}
	return nil
}

// KillTree forcefully terminates pid and all its child processes using taskkill.
// This is the Windows equivalent of SIGKILL on the whole process group.
func (k *windowsKiller) KillTree(pid int) error {
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", pid))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("taskkill: %w: %s", err, output)
	}
	return nil
}

// SetPgid configures cmd to start in its own console process group on Windows.
// CREATE_NEW_PROCESS_GROUP (0x200) is required so that GenerateConsoleCtrlEvent
// can target this process specifically without affecting the Pulse process itself.
func SetPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x00000200, // CREATE_NEW_PROCESS_GROUP
	}
}
