package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Pratham-Mishra04/pulse/internal/log"
)

func testLogger() *log.Logger {
	return log.New(log.LogLevelQuiet, true)
}

// writeTempGoApp writes a compilable Go program into dir and returns the path
// to use as the build output binary.
func writeTempGoApp(t *testing.T, dir string) (binPath string) {
	t.Helper()
	src := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testbuild\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "out")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	return bin
}

// buildCmd returns a build command string that uses "go build -C <dir>" so Go
// builds the package inside dir's own module without requiring a cwd change.
// The -C flag was added in Go 1.21, which is this project's minimum version.
func buildCmd(dir, bin string) string {
	return "go build -C " + dir + " -o " + bin + " ."
}

// ── Build ────────────────────────────────────────────────────────────────────

func TestBuild_Success(t *testing.T) {
	dir := t.TempDir()
	bin := writeTempGoApp(t, dir)

	cfg := ServiceConfig{
		Build: buildCmd(dir, bin),
		Run:   bin,
	}
	b := NewBuilder("test", cfg, testLogger())
	result := b.Build(context.Background())
	if result.Err != nil {
		t.Fatalf("expected success, got: %v\noutput: %s", result.Err, result.Output)
	}
	if result.BinPath != bin {
		t.Errorf("BinPath = %q, want %q", result.BinPath, bin)
	}
	if result.Elapsed <= 0 {
		t.Error("expected positive Elapsed duration")
	}
	if _, err := os.Stat(bin); err != nil {
		t.Errorf("binary not produced at %q: %v", bin, err)
	}
}

func TestBuild_Failure(t *testing.T) {
	dir := t.TempDir()
	// Syntax error — missing closing brace.
	src := "package main\n\nfunc main() {\n"
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testbuild\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "out")
	cfg := ServiceConfig{
		Build: buildCmd(dir, bin),
		Run:   bin,
	}
	b := NewBuilder("test", cfg, testLogger())
	result := b.Build(context.Background())
	if result.Err == nil {
		t.Fatal("expected build failure, got nil error")
	}
	if result.Output == "" {
		t.Error("expected non-empty Output (compiler error message) on failure")
	}
	if result.Elapsed <= 0 {
		t.Error("expected positive Elapsed even on failure")
	}
}

func TestBuild_QuotedLDFlags(t *testing.T) {
	dir := t.TempDir()
	// Program that reads the injected variable so the linker doesn't strip it.
	src := `package main

import "fmt"

var Version string

func main() { fmt.Println(Version) }
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testbuild\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "out")

	// The quoted value contains a space — strings.Fields would have broken this
	// into three tokens. shlex must keep it as one argument to -ldflags.
	cfg := ServiceConfig{
		Build: `go build -C ` + dir + ` -ldflags "-X main.Version=1.2.3" -o ` + bin + ` .`,
		Run:   bin,
	}
	b := NewBuilder("test", cfg, testLogger())
	result := b.Build(context.Background())
	if result.Err != nil {
		t.Fatalf("expected success with quoted ldflags, got: %v\noutput: %s", result.Err, result.Output)
	}
}

func TestBuild_InvalidCommand(t *testing.T) {
	cfg := ServiceConfig{
		// Unclosed quote — shlex will return an error.
		Build: `go build -ldflags "unclosed`,
		Run:   "./tmp/out",
	}
	b := NewBuilder("test", cfg, testLogger())
	result := b.Build(context.Background())
	if result.Err == nil {
		t.Fatal("expected error for malformed build command, got nil")
	}
}

func TestBuild_CancelledContext(t *testing.T) {
	dir := t.TempDir()
	bin := writeTempGoApp(t, dir)
	cfg := ServiceConfig{
		Build: buildCmd(dir, bin),
		Run:   bin,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before the build starts
	b := NewBuilder("test", cfg, testLogger())
	result := b.Build(ctx)
	// exec.CommandContext cancels the process when the context is done.
	// The build may or may not complete depending on timing, but there should
	// be no panic and the result should be returned.
	_ = result
}

func TestBuild_BinPathMatchesRunConfig(t *testing.T) {
	dir := t.TempDir()
	bin := writeTempGoApp(t, dir)
	cfg := ServiceConfig{
		Build: buildCmd(dir, bin),
		Run:   bin,
	}
	b := NewBuilder("test", cfg, testLogger())
	result := b.Build(context.Background())
	if result.BinPath != cfg.Run {
		t.Errorf("BinPath = %q, want cfg.Run = %q", result.BinPath, cfg.Run)
	}
}

// ── Enqueue ───────────────────────────────────────────────────────────────────

func TestEnqueue_DropsDuplicates(t *testing.T) {
	cfg := ServiceConfig{Debounce: 50 * time.Millisecond}
	b := NewBuilder("test", cfg, testLogger())

	// Three rapid Enqueue() calls must not block and must produce at most one
	// pending signal in the channel.
	b.Enqueue()
	b.Enqueue()
	b.Enqueue()

	if len(b.enqueueCh) != 1 {
		t.Errorf("enqueueCh len = %d, want 1 (duplicate signals dropped)", len(b.enqueueCh))
	}
}

func TestEnqueue_CancelsInFlightBuild(t *testing.T) {
	dir := t.TempDir()
	bin := writeTempGoApp(t, dir)
	cfg := ServiceConfig{
		Build:    buildCmd(dir, bin),
		Run:      bin,
		Debounce: 1 * time.Millisecond,
	}
	b := NewBuilder("test", cfg, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the worker.
	go b.Run(ctx)

	// Enqueue and immediately enqueue again — second should cancel the first.
	b.Enqueue()
	time.Sleep(50 * time.Millisecond) // let debounce fire and build start
	b.Enqueue()                       // cancel the in-flight build

	// The worker must produce a result eventually without deadlocking.
	select {
	case <-b.Results():
		// Got a result — worker is alive.
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for build result after Enqueue cancellation")
	}
}

// ── Run ───────────────────────────────────────────────────────────────────────

func TestRun_ExitsOnContextCancellation(t *testing.T) {
	cfg := ServiceConfig{
		Build:    "echo ok",
		Debounce: 1 * time.Millisecond,
	}
	b := NewBuilder("test", cfg, testLogger())
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		b.Run(ctx)
		close(done)
	}()

	cancel()

	select {
	case <-done:
		// Run exited cleanly.
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after context cancellation")
	}
}

func TestRun_SendsResultToChannel(t *testing.T) {
	dir := t.TempDir()
	bin := writeTempGoApp(t, dir)
	cfg := ServiceConfig{
		Build:    buildCmd(dir, bin),
		Run:      bin,
		Debounce: 1 * time.Millisecond,
	}
	b := NewBuilder("test", cfg, testLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	go b.Run(ctx)
	b.Enqueue()

	select {
	case result := <-b.Results():
		if result.Err != nil {
			t.Errorf("expected success, got: %v\n%s", result.Err, result.Output)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for build result")
	}
}
