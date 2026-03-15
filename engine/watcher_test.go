package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── containsSegment ───────────────────────────────────────────────────────────

func TestContainsSegment(t *testing.T) {
	cases := []struct {
		path, segment string
		want          bool
	}{
		// Hard-ignored directories at various nesting depths.
		{"./vendor/foo/bar.go", "vendor", true},
		{"./internal/vendor/foo.go", "vendor", true},
		{".git/hooks/pre-commit", ".git", true},
		{"./tmp/api", "tmp", true},
		{"./node_modules/react/index.js", "node_modules", true},
		{"./testdata/fixture.go", "testdata", true},

		// Should not match unrelated paths.
		{"./internal/auth/handler.go", "vendor", false},
		{"./cmd/api/main.go", "tmp", false},

		// Exact filename match.
		{"./foo/bar/baz.go", "baz.go", true},

		// Directory segment match.
		{"./foo/bar/baz.go", "bar", true},
		{"./foo/bar/baz.go", "foo", true},

		// Absolute path.
		{"/home/user/project/vendor/foo.go", "vendor", true},
		{"/home/user/project/internal/foo.go", "vendor", false},

		// Should not match partial segment names.
		{"./myvendor/foo.go", "vendor", false},
		{"./vendor_custom/foo.go", "vendor", false},
	}
	for _, tc := range cases {
		got := containsSegment(tc.path, tc.segment)
		if got != tc.want {
			t.Errorf("containsSegment(%q, %q) = %v, want %v", tc.path, tc.segment, got, tc.want)
		}
	}
}

// ── matchesSuffix ─────────────────────────────────────────────────────────────

func TestMatchesSuffix(t *testing.T) {
	cases := []struct {
		name     string
		suffixes []string
		want     bool
	}{
		{"foo_gen.go", []string{"_gen.go", ".pb.go"}, true},
		{"foo.pb.go", []string{"_gen.go", ".pb.go"}, true},
		{"foo.go", []string{"_gen.go", ".pb.go"}, false},
		{"handler.go", []string{"_gen.go"}, false},
		{"mock_gen.go", []string{"_gen.go"}, true},
		{"wire_gen.go", []string{"_gen.go"}, true},
		// Edge cases.
		{"", []string{"_gen.go"}, false},
		{"a", []string{"a"}, true},
		{"a", []string{"ab"}, false},
		// Exact suffix length.
		{"_gen.go", []string{"_gen.go"}, true},
	}
	for _, tc := range cases {
		got := matchesSuffix(tc.name, tc.suffixes...)
		if got != tc.want {
			t.Errorf("matchesSuffix(%q, %v) = %v, want %v", tc.name, tc.suffixes, got, tc.want)
		}
	}
}

// ── isHardIgnored ─────────────────────────────────────────────────────────────

func TestIsHardIgnored(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"./vendor/foo.go", true},
		{".git/config", true},
		{"./node_modules/react/index.js", true},
		{"./tmp/api", true},
		{"./testdata/fixtures.go", true},
		// Nested inside a hard-ignored directory.
		{"./internal/vendor/foo.go", true},
		// Normal project paths.
		{"./internal/auth/handler.go", false},
		{"./cmd/api/main.go", false},
		{"./pkg/util/strings.go", false},
	}
	for _, tc := range cases {
		got := isHardIgnored(tc.path)
		if got != tc.want {
			t.Errorf("isHardIgnored(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// ── shouldReload helpers ──────────────────────────────────────────────────────

// newTestWatcher creates a Watcher with no gitignore for unit testing.
func newTestWatcher(cfg ServiceConfig) *Watcher {
	return &Watcher{cfg: cfg, log: testLogger(), gitign: nil}
}

// ── shouldReload ─────────────────────────────────────────────────────────────

func TestShouldReload_HardIgnoredDirectories(t *testing.T) {
	w := newTestWatcher(ServiceConfig{Watch: []string{".go"}})
	paths := []string{
		"./vendor/foo/bar.go",
		".git/hooks/commit-msg",
		"./node_modules/react/index.js",
		"./tmp/binary",
		"./testdata/fixture.go",
	}
	for _, path := range paths {
		if w.shouldReload(path) {
			t.Errorf("shouldReload(%q) = true, want false (hard ignored)", path)
		}
	}
}

func TestShouldReload_ExtensionAllowlist(t *testing.T) {
	w := newTestWatcher(ServiceConfig{Watch: []string{".go", "go.mod"}})
	cases := []struct {
		path string
		want bool
	}{
		{"./internal/auth/handler.go", true},
		{"go.mod", true},
		{"./cmd/api/main.go", true},
		{"./style.css", false},
		{"./README.md", false},
		{"go.sum", false}, // not in the watch list
		{"./config.yaml", false},
	}
	for _, tc := range cases {
		got := w.shouldReload(tc.path)
		if got != tc.want {
			t.Errorf("shouldReload(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestShouldReload_ExactFilename(t *testing.T) {
	w := newTestWatcher(ServiceConfig{Watch: []string{".go", "go.mod", "go.sum"}})
	exactFiles := []string{"go.mod", "go.sum"}
	for _, f := range exactFiles {
		if !w.shouldReload(f) {
			t.Errorf("shouldReload(%q) = false, want true (exact filename)", f)
		}
	}
}

func TestShouldReload_GeneratedFiles(t *testing.T) {
	w := newTestWatcher(ServiceConfig{Watch: []string{".go"}})
	cases := []struct {
		path string
		want bool
	}{
		{"./internal/wire_gen.go", false},  // _gen.go suffix
		{"./proto/user.pb.go", false},      // .pb.go suffix
		{"./internal/handler.go", true},    // normal .go file
		{"./cmd/api/main.go", true},        // normal .go file
		{"./gen/generated_gen.go", false},  // both gen + _gen.go
	}
	for _, tc := range cases {
		got := w.shouldReload(tc.path)
		if got != tc.want {
			t.Errorf("shouldReload(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestShouldReload_UserIgnorePatterns(t *testing.T) {
	w := newTestWatcher(ServiceConfig{
		Watch:  []string{".go"},
		Ignore: []string{"*.pb.go"},
	})
	// filepath.Match is applied to the base name, so "user.pb.go" matches "*.pb.go".
	if w.shouldReload("./internal/proto/user.pb.go") {
		t.Error("shouldReload('./internal/proto/user.pb.go') = true, want false (user ignore)")
	}
	// Normal .go file must still pass through.
	if !w.shouldReload("./internal/handler.go") {
		t.Error("shouldReload('./internal/handler.go') = false, want true")
	}
}

// ── addDirsRecursive ─────────────────────────────────────────────────────────

func TestAddDirsRecursive_SkipsHardIgnored(t *testing.T) {
	// Build a tree:
	//   root/
	//     internal/
	//     vendor/       ← hard ignored
	//       foo/        ← should NOT be registered
	//     .git/         ← hard ignored
	root := t.TempDir()
	dirs := []string{
		filepath.Join(root, "internal"),
		filepath.Join(root, "vendor", "foo"),
		filepath.Join(root, ".git"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			t.Fatal(err)
		}
	}

	registered := map[string]bool{}
	// Use a fake walk to collect what would be registered (without real fsnotify).
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if isHardIgnored(path) && path != root {
			return filepath.SkipDir
		}
		registered[path] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if !registered[root] {
		t.Error("root should be registered")
	}
	internalPath := filepath.Join(root, "internal")
	if !registered[internalPath] {
		t.Error("internal/ should be registered")
	}
	vendorPath := filepath.Join(root, "vendor")
	if registered[vendorPath] {
		t.Error("vendor/ should NOT be registered (hard ignored)")
	}
	vendorFooPath := filepath.Join(root, "vendor", "foo")
	if registered[vendorFooPath] {
		t.Error("vendor/foo/ should NOT be registered (inside hard ignored)")
	}
}

// ── Start: integration smoke test ─────────────────────────────────────────────

func TestWatcher_Start_EmitsEventOnFileChange(t *testing.T) {
	dir := t.TempDir()

	cfg := ServiceConfig{
		Path:  dir,
		Watch: []string{".go"},
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

	// Give fsnotify time to register the directory.
	time.Sleep(50 * time.Millisecond)

	// Write a .go file — should trigger an event.
	goFile := filepath.Join(dir, "main.go")
	if err := os.WriteFile(goFile, []byte("package main"), 0644); err != nil {
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
		t.Fatal("timed out waiting for file event")
	}
}

func TestWatcher_Start_IgnoresNonWatchedExtension(t *testing.T) {
	dir := t.TempDir()

	cfg := ServiceConfig{
		Path:  dir,
		Watch: []string{".go"},
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

	time.Sleep(50 * time.Millisecond)

	// Write a .css file — should NOT trigger an event.
	if err := os.WriteFile(filepath.Join(dir, "style.css"), []byte("body{}"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case path := <-events:
		t.Errorf("unexpected event for %q (should have been filtered)", path)
	case <-time.After(300 * time.Millisecond):
		// No event — correct behaviour.
	}
}

func TestWatcher_Start_ClosesChannelOnCancel(t *testing.T) {
	dir := t.TempDir()
	cfg := ServiceConfig{Path: dir, Watch: []string{".go"}}
	w, err := NewWatcher(cfg, testLogger())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	events, err := w.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	cancel()

	select {
	case _, ok := <-events:
		if ok {
			t.Error("expected channel to be closed after context cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("events channel not closed after context cancellation")
	}
}

// newTimedContext returns a context that cancels after the given duration.
func newTimedContext(t *testing.T, d time.Duration) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), d)
}