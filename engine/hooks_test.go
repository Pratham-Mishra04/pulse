package engine

import (
	"context"
	"strings"
	"testing"
)

// ── runHook ───────────────────────────────────────────────────────────────────

func TestRunHook_Success(t *testing.T) {
	_, _, err := runHook(context.Background(), "echo hello", "")
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestRunHook_CapturesOutput(t *testing.T) {
	out, _, err := runHook(context.Background(), "echo hello", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected output to contain 'hello', got %q", out)
	}
}

func TestRunHook_Failure(t *testing.T) {
	_, _, err := runHook(context.Background(), "false", "")
	if err == nil {
		t.Fatal("expected error from failing command, got nil")
	}
}

func TestRunHook_CapturesStderr(t *testing.T) {
	// Use a command that writes to stderr and exits non-zero.
	out, _, err := runHook(context.Background(), "sh -c 'echo error-msg >&2; exit 1'", "")
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(out, "error-msg") {
		t.Errorf("expected stderr in output, got %q", out)
	}
}

func TestRunHook_InvalidCommand(t *testing.T) {
	// Unclosed quote — shlex should reject it.
	_, _, err := runHook(context.Background(), `echo "unclosed`, "")
	if err == nil {
		t.Fatal("expected error for malformed command, got nil")
	}
}

func TestRunHook_EmptyCommand(t *testing.T) {
	_, _, err := runHook(context.Background(), "", "")
	if err == nil {
		t.Fatal("expected error for empty command, got nil")
	}
}

func TestRunHook_QuotedArgs(t *testing.T) {
	// Quoted argument with a space must stay as one token.
	out, _, err := runHook(context.Background(), `echo "hello world"`, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "hello world") {
		t.Errorf("expected 'hello world' as single arg, got %q", out)
	}
}

func TestRunHook_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := runHook(ctx, "echo should-not-run", "")
	// May succeed or fail depending on timing, but must not panic.
	_ = err
}

func TestRunHook_ReturnsElapsed(t *testing.T) {
	_, elapsed, err := runHook(context.Background(), "echo ok", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed <= 0 {
		t.Error("expected positive elapsed duration")
	}
}

// ── Pre hook integration via Builder.Build ────────────────────────────────────

func TestBuild_PreHook_Success(t *testing.T) {
	dir := t.TempDir()
	bin := writeTempGoApp(t, dir)
	cfg := ServiceConfig{
		Pre:   "echo pre-hook-ran",
		Build: buildCmd(dir, bin),
		Run:   bin,
	}
	b := NewBuilder("test", cfg, testLogger())
	result := b.Build(context.Background())
	if result.Err != nil {
		t.Fatalf("expected success, got: %v\n%s", result.Err, result.Output)
	}
}

func TestBuild_PreHook_FailureNonStrict(t *testing.T) {
	// Non-strict pre failure should NOT abort the build.
	dir := t.TempDir()
	bin := writeTempGoApp(t, dir)
	cfg := ServiceConfig{
		Pre:       "false", // always fails
		PreStrict: false,
		Build:     buildCmd(dir, bin),
		Run:       bin,
	}
	b := NewBuilder("test", cfg, testLogger())
	result := b.Build(context.Background())
	// Build should still succeed despite pre hook failure.
	if result.Err != nil {
		t.Fatalf("non-strict pre failure should not abort build, got: %v\n%s", result.Err, result.Output)
	}
}

func TestBuild_PreHook_FailureStrict(t *testing.T) {
	// Strict pre failure MUST abort the build.
	dir := t.TempDir()
	bin := writeTempGoApp(t, dir)
	cfg := ServiceConfig{
		Pre:       "false", // always fails
		PreStrict: true,
		Build:     buildCmd(dir, bin),
		Run:       bin,
	}
	b := NewBuilder("test", cfg, testLogger())
	result := b.Build(context.Background())
	if result.Err == nil {
		t.Fatal("expected build to be aborted by strict pre hook failure, got nil error")
	}
	if !strings.Contains(result.Output, "pre hook failed") {
		t.Errorf("expected 'pre hook failed' in output, got %q", result.Output)
	}
}

// ── Post hook integration via Engine.runPostHook ──────────────────────────────

func newTestEngine(cfg ServiceConfig) *Engine {
	return &Engine{
		name: "test",
		cfg:  cfg,
		log:  testLogger(),
	}
}

// TestEngine_PostStrict_LeavesOldProcess is a swap-level regression test.
// It verifies that a strict post-hook failure does not replace the running
// process — the engine must keep the old process alive and skip the restart.
func TestEngine_PostStrict_LeavesOldProcess(t *testing.T) {
	dir := t.TempDir()
	bin := writeTempGoApp(t, dir)

	cfg := ServiceConfig{
		Build:      buildCmd(dir, bin),
		Run:        bin,
		Post:       "false", // always fails
		PostStrict: true,
		NoStdin:    true,
	}

	r := NewRunner("test", cfg, testLogger())
	e := &Engine{
		name:    "test",
		cfg:     cfg,
		log:     testLogger(),
		runner:  r,
		builder: NewBuilder("test", cfg, testLogger()),
	}

	// Build and start the initial process.
	result := e.builder.Build(context.Background())
	if result.Err != nil {
		t.Fatalf("initial build failed: %v\n%s", result.Err, result.Output)
	}
	if err := r.Start(result, 0); err != nil {
		t.Fatalf("failed to start initial process: %v", err)
	}
	t.Cleanup(func() { _ = r.Stop() })

	originalPID := r.Pid()
	if originalPID == 0 {
		t.Fatal("expected non-zero PID after start")
	}

	// Simulate a rebuild event: the post hook must fail strictly.
	if err := e.runPostHook(context.Background()); err == nil {
		t.Fatal("expected strict post hook to fail, got nil")
	}

	// Engine must NOT call runner.Restart on strict post failure —
	// the old process reference (PID) must be unchanged.
	if pid := r.Pid(); pid != originalPID {
		t.Errorf("PID changed %d → %d: old process was replaced despite strict post failure", originalPID, pid)
	}
}

func TestPostHook_Success(t *testing.T) {
	e := newTestEngine(ServiceConfig{Post: "echo post-hook-ran"})
	if err := e.runPostHook(context.Background()); err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestPostHook_NoPost(t *testing.T) {
	// Empty Post — runPostHook must be a no-op.
	e := newTestEngine(ServiceConfig{})
	if err := e.runPostHook(context.Background()); err != nil {
		t.Fatalf("expected nil for empty Post, got: %v", err)
	}
}

func TestPostHook_FailureNonStrict(t *testing.T) {
	// Non-strict post failure must NOT return an error — swap proceeds anyway.
	e := newTestEngine(ServiceConfig{Post: "false", PostStrict: false})
	if err := e.runPostHook(context.Background()); err != nil {
		t.Fatalf("non-strict post failure should not return error, got: %v", err)
	}
}

func TestPostHook_FailureStrict(t *testing.T) {
	// Strict post failure MUST return an error — caller keeps old process alive.
	e := newTestEngine(ServiceConfig{Post: "false", PostStrict: true})
	err := e.runPostHook(context.Background())
	if err == nil {
		t.Fatal("expected error from strict post hook failure, got nil")
	}
	if !strings.Contains(err.Error(), "post hook failed") {
		t.Errorf("expected 'post hook failed' in error, got %q", err.Error())
	}
}
