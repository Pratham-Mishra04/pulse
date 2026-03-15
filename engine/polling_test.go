package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPollingWatcher_DetectsNewFile(t *testing.T) {
	dir := t.TempDir()
	cfg := ServiceConfig{
		Path:         dir,
		Watch:        []string{".go"},
		Polling:      "on",
		PollInterval: 50 * time.Millisecond,
	}
	w, err := NewWatcher(cfg, testLogger())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if w.pollInterval == 0 {
		t.Fatal("expected polling mode to be active")
	}

	ctx, cancel := newTimedContext(t, 5*time.Second)
	defer cancel()

	events, err := w.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the watcher time to take its initial snapshot.
	time.Sleep(100 * time.Millisecond)

	// Write a new .go file — should be detected on the next poll tick.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case got, ok := <-events:
		if !ok {
			t.Fatal("events channel closed unexpectedly")
		}
		if filepath.Base(got) != "main.go" {
			t.Errorf("got event for %q, want main.go", got)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for polling event")
	}
}

func TestPollingWatcher_DetectsModifiedFile(t *testing.T) {
	dir := t.TempDir()

	// Create the file before starting the watcher so it's in the snapshot.
	goFile := filepath.Join(dir, "handler.go")
	if err := os.WriteFile(goFile, []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := ServiceConfig{
		Path:  dir,
		Watch: []string{".go"},
		Polling: "on", PollInterval: 50 * time.Millisecond,
	}
	w, err := NewWatcher(cfg, testLogger())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := newTimedContext(t, 5*time.Second)
	defer cancel()

	events, err := w.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait for the initial snapshot to be taken.
	time.Sleep(100 * time.Millisecond)

	// Modify the file — mtime must advance.
	time.Sleep(10 * time.Millisecond) // ensure mtime changes
	if err := os.WriteFile(goFile, []byte("package main\n// changed"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case got, ok := <-events:
		if !ok {
			t.Fatal("events channel closed unexpectedly")
		}
		if filepath.Base(got) != "handler.go" {
			t.Errorf("got event for %q, want handler.go", got)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for modified file event")
	}
}

func TestPollingWatcher_IgnoresNonWatchedExtension(t *testing.T) {
	dir := t.TempDir()
	cfg := ServiceConfig{
		Path:  dir,
		Watch: []string{".go"},
		Polling: "on", PollInterval: 50 * time.Millisecond,
	}
	w, err := NewWatcher(cfg, testLogger())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := newTimedContext(t, 2*time.Second)
	defer cancel()

	events, err := w.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Write a .css file — should NOT trigger an event.
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body{}"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case path := <-events:
		t.Errorf("unexpected event for %q (should have been filtered)", path)
	case <-time.After(200 * time.Millisecond):
		// No event — correct.
	}
}

func TestPollingWatcher_ClosesChannelOnCancel(t *testing.T) {
	dir := t.TempDir()
	cfg := ServiceConfig{
		Path:  dir,
		Watch: []string{".go"},
		Polling: "on", PollInterval: 50 * time.Millisecond,
	}
	w, err := NewWatcher(cfg, testLogger())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := newTimedContext(t, 5*time.Second)

	events, err := w.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	cancel()

	select {
	case _, ok := <-events:
		if ok {
			t.Error("expected events channel to be closed after cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("events channel not closed after context cancellation")
	}
}

// TestNewWatcher_PollingOn verifies polling: "on" activates polling with the
// configured interval.
func TestNewWatcher_PollingOn(t *testing.T) {
	cfg := ServiceConfig{
		Watch:        []string{".go"},
		Polling:      "on",
		PollInterval: 200 * time.Millisecond,
	}
	w, err := NewWatcher(cfg, testLogger())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if w.pollInterval != 200*time.Millisecond {
		t.Errorf("pollInterval = %s, want 200ms", w.pollInterval)
	}
}

// TestNewWatcher_PollingOff verifies polling: "off" disables polling even if
// auto-detection would otherwise enable it.
func TestNewWatcher_PollingOff(t *testing.T) {
	cfg := ServiceConfig{
		Watch:   []string{".go"},
		Polling: "off",
	}
	w, err := NewWatcher(cfg, testLogger())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if w.pollInterval != 0 {
		t.Errorf("expected pollInterval=0 for polling:off, got %s", w.pollInterval)
	}
}

// TestNewWatcher_PollingAutoDefault verifies that the default ("auto") does not
// enable polling on a normal host machine.
func TestNewWatcher_PollingAutoDefault(t *testing.T) {
	if isInsideContainer() {
		t.Skip("running inside a container — auto would enable polling")
	}
	cfg := ServiceConfig{
		Watch:   []string{".go"},
		Polling: "auto",
	}
	w, err := NewWatcher(cfg, testLogger())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	if w.pollInterval != 0 {
		t.Errorf("expected pollInterval=0 on host with polling:auto, got %s", w.pollInterval)
	}
}
