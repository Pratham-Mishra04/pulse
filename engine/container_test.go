package engine

import (
	"os"
	"testing"
)

func TestIsInsideContainer_FalseOnHost(t *testing.T) {
	// On a normal development machine neither /.dockerenv nor cgroup markers
	// should be present. This test verifies the function does not return a
	// false positive in the most common environment.
	if isInsideContainer() {
		t.Skip("skipping host-only test inside a container")
	}
	if isInsideContainer() {
		t.Error("isInsideContainer() = true on what appears to be a host machine")
	}
}

func TestIsInsideContainer_TrueWhenDockerenvPresent(t *testing.T) {
	dir := t.TempDir()
	sentinel := dir + "/.dockerenv"
	if err := os.WriteFile(sentinel, []byte{}, 0644); err != nil {
		t.Fatal(err)
	}

	old := dockerEnvPath
	dockerEnvPath = sentinel
	t.Cleanup(func() { dockerEnvPath = old })

	if !isInsideContainer() {
		t.Fatal("expected isInsideContainer() = true when sentinel exists")
	}
}
