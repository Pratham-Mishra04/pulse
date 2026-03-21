package engine

import (
	"os"
	"testing"
)

// ── buildEnv ─────────────────────────────────────────────────────────────────

func TestBuildEnv_MergesExtraVars(t *testing.T) {
	extra := map[string]string{
		"PORT":   "8080",
		"DB_URL": "postgres://localhost/dev",
	}
	env := buildEnv(extra, 0)

	hasPort := false
	hasDB := false
	for _, e := range env {
		if e == "PORT=8080" {
			hasPort = true
		}
		if e == "DB_URL=postgres://localhost/dev" {
			hasDB = true
		}
	}
	if !hasPort {
		t.Error("PORT=8080 not found in env")
	}
	if !hasDB {
		t.Error("DB_URL=postgres://localhost/dev not found in env")
	}
}

func TestBuildEnv_NilExtraReturnsParent(t *testing.T) {
	parent := os.Environ()
	result := buildEnv(nil, 0)
	if len(result) != len(parent) {
		t.Errorf("buildEnv(nil) len = %d, want %d (parent env)", len(result), len(parent))
	}
}

func TestBuildEnv_EmptyExtraReturnsParent(t *testing.T) {
	parent := os.Environ()
	result := buildEnv(map[string]string{}, 0)
	if len(result) != len(parent) {
		t.Errorf("buildEnv({}) len = %d, want %d (parent env)", len(result), len(parent))
	}
}

// ── Runner.Pid ────────────────────────────────────────────────────────────────

func TestRunner_Pid_ZeroWhenNoProcess(t *testing.T) {
	cfg := ServiceConfig{NoStdin: true}
	r := NewRunner("test", cfg, testLogger())
	if pid := r.Pid(); pid != 0 {
		t.Errorf("Pid() = %d, want 0 (no process started)", pid)
	}
}

// ── Runner.Stop ───────────────────────────────────────────────────────────────

func TestRunner_Stop_IdempotentWhenNoProcess(t *testing.T) {
	cfg := ServiceConfig{NoStdin: true}
	r := NewRunner("test", cfg, testLogger())
	// Stop on a runner with no running process must be a no-op.
	if err := r.Stop(); err != nil {
		t.Errorf("Stop() on idle runner returned error: %v", err)
	}
	// Calling twice must also be safe.
	if err := r.Stop(); err != nil {
		t.Errorf("second Stop() on idle runner returned error: %v", err)
	}
}
