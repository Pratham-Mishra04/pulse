package engine

import (
	"bytes"
	"os"
)

// isInsideContainer returns true when Pulse is running inside a Docker
// container or similar OCI runtime (Kubernetes, containerd, Podman).
//
// Used to auto-enable polling mode: inotify does not fire for bind-mount
// changes from the host in most container runtimes, so fsnotify silently
// misses every file edit made outside the container.
//
// Safe to call on macOS and Windows — the file checks simply return false
// on those platforms.
// dockerEnvPath and cgroupPath are package-level vars so tests can override them.
var dockerEnvPath = "/.dockerenv"
var cgroupPath = "/proc/1/cgroup"

func isInsideContainer() bool {
	// Docker and most OCI runtimes place this sentinel file in every container.
	if _, err := os.Stat(dockerEnvPath); err == nil {
		return true
	}

	// Fallback: inspect the cgroup v1 hierarchy. Linux only; ReadFile returns
	// an error on macOS/Windows, so this branch is a no-op there.
	data, err := os.ReadFile(cgroupPath)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte("docker")) ||
		bytes.Contains(data, []byte("kubepods")) ||
		bytes.Contains(data, []byte("containerd")) ||
		bytes.Contains(data, []byte("libpod")) ||
		bytes.Contains(data, []byte("podman"))
}
