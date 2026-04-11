package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/Pratham-Mishra04/pulse/internal/gitignore"
	"github.com/Pratham-Mishra04/pulse/internal/log"
)

const defaultPollInterval = 500 * time.Millisecond

// hardIgnored are directories always excluded from watching, regardless of
// user config. These are never meaningful sources of Go code changes.
var hardIgnored = []string{
	".git",
	"vendor",
	"node_modules",
	"tmp",      // Pulse's own build output directory
	"testdata", // Go test fixture convention
}

// Watcher wraps fsnotify and applies extension filters, hardIgnored patterns,
// and .gitignore rules. It emits the paths of changed files on the events channel.
type Watcher struct {
	cfg          ServiceConfig
	log          *log.Logger
	gitign       *gitignore.GitIgnore // nil if no .gitignore found
	pollInterval time.Duration        // 0 = use fsnotify, >0 = use polling loop
	watchRoots   []string             // directories to watch: primary + any go.work extras

	// contentCache maps absolute file path → last-seen content.
	// Used by isCommentOnlyDiff to detect when a change is comment-only
	// and the rebuild can be safely skipped.
	contentCache sync.Map
}

// NewWatcher creates a Watcher for cfg. extraRoots contains any additional
// directories to watch beyond cfg.Path (e.g. external go.work modules).
func NewWatcher(cfg ServiceConfig, extraRoots []string, l *log.Logger) (*Watcher, error) {
	// .gitignore is optional — if it doesn't exist, gitign stays nil and
	// MatchesPath is never called. Only return an error if the file exists
	// but cannot be parsed.
	var gi *gitignore.GitIgnore
	if _, err := os.Stat(".gitignore"); err == nil {
		parsed, err := gitignore.CompileIgnoreFile(".gitignore")
		if err != nil {
			return nil, fmt.Errorf("failed to compile .gitignore: %w", err)
		}
		gi = parsed
	}

	// Resolve poll interval from the Polling field.
	var pollInterval time.Duration
	switch cfg.Polling {
	case "on":
		pollInterval = cfg.PollInterval
	case "off":
		pollInterval = 0
	case "", "auto":
		if isInsideContainer() {
			pollInterval = cfg.PollInterval
			l.Info(fmt.Sprintf("container detected → polling mode (%s)", pollInterval))
		}
	default:
		return nil, fmt.Errorf("invalid polling mode %q (expected auto|on|off)", cfg.Polling)
	}

	// Build the ordered list of watch roots: primary dir first, then extras.
	primary := cfg.Path
	if primary == "" {
		primary = "."
	}
	watchRoots := make([]string, 0, 1+len(extraRoots))
	watchRoots = append(watchRoots, primary)
	watchRoots = append(watchRoots, extraRoots...)

	return &Watcher{cfg: cfg, log: l, gitign: gi, pollInterval: pollInterval, watchRoots: watchRoots}, nil
}

// Start begins watching for file changes and returns a read-only channel of
// changed file paths. The channel is closed when ctx is cancelled.
//
// When pollInterval > 0, a stat-based polling loop is used instead of fsnotify.
// This is necessary inside Docker/containers where inotify does not fire for
// bind-mount changes from the host.
func (w *Watcher) Start(ctx context.Context) (<-chan string, error) {
	if w.pollInterval > 0 {
		return w.startPolling(ctx)
	}
	return w.startFSNotify(ctx)
}

// startFSNotify is the default inotify/FSEvents/kqueue-based watcher.
func (w *Watcher) startFSNotify(ctx context.Context) (<-chan string, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	// Register every watch root (primary dir + any go.work external modules).
	// fsnotify requires each directory to be registered explicitly — it does
	// not recurse automatically.
	for _, root := range w.watchRoots {
		if err := addDirsRecursive(fsw, root); err != nil {
			fsw.Close()
			return nil, err
		}
	}

	// Buffer of 64 absorbs bursts of rapid events (e.g. IDE atomic writes
	// that emit multiple events per save). The builder's debounce coalesces
	// these into a single build trigger.
	events := make(chan string, 64)

	go func() {
		defer close(events)
		defer fsw.Close()

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-fsw.Events:
				if !ok {
					// fsnotify closed its events channel — shut down.
					return
				}
				w.handleFSEvent(fsw, ev, events)
			case err, ok := <-fsw.Errors:
				if !ok {
					return
				}
				w.log.Error("watcher: " + err.Error())
			}
		}
	}()

	return events, nil
}

// handleFSEvent processes a single fsnotify event: registering new directories
// and forwarding relevant file changes onto the events channel.
func (w *Watcher) handleFSEvent(fsw *fsnotify.Watcher, ev fsnotify.Event, events chan<- string) {
	// When a new directory is created, register it with fsnotify so
	// files created inside it are also watched.
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			if !isHardIgnored(ev.Name) {
				if err := addDirsRecursive(fsw, ev.Name); err != nil {
					w.log.Verbose("watcher: failed to watch new dir " + ev.Name + ": " + err.Error())
				}
			}
		}
	}
	// Only care about writes and newly created files.
	// Rename/chmod/remove do not need a rebuild.
	if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
		return
	}
	if !w.shouldReload(w.relPathForFilter(ev.Name)) {
		w.log.Verbose("ignored: " + ev.Name)
		return
	}
	if w.isCommentOnlyDiff(ev.Name) {
		w.log.Skip(ev.Name, "comment-only change")
		return
	}
	// Non-blocking send: if the buffer is full the event is dropped.
	// This is safe because the debounce window in Builder will coalesce
	// the remaining events into a single build anyway.
	select {
	case events <- ev.Name:
	default:
	}
}

// startPolling is a stat-based polling watcher. On every tick it walks all
// watch roots, compares file modification times against a snapshot, and emits
// paths for any file that changed. Used inside containers where inotify does
// not fire for bind-mount changes from the host.
func (w *Watcher) startPolling(ctx context.Context) (<-chan string, error) {
	// Build the initial snapshot before returning so the first tick only
	// reports files that changed after Start() was called.
	snapshot, err := w.buildSnapshot()
	if err != nil {
		return nil, fmt.Errorf("polling snapshot init failed: %w", err)
	}

	events := make(chan string, 64)

	go func() {
		defer close(events)

		ticker := time.NewTicker(w.pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return

			case <-ticker.C:
				current, err := w.buildSnapshot()
				if err != nil {
					w.log.Error("polling snapshot failed: " + err.Error())
					continue
				}

				for path, mtime := range current {
					prev, seen := snapshot[path]
					// Emit if file is new or its mtime advanced.
					if !seen || mtime.After(prev) {
						if w.isCommentOnlyDiff(path) {
							w.log.Skip(path, "comment-only change")
							continue
						}
						select {
						case events <- path:
						default:
						}
					}
				}

				snapshot = current
			}
		}
	}()

	return events, nil
}

// buildSnapshot walks all watch roots and records the modification time of
// every file that passes shouldReload. Used by the polling watcher.
func (w *Watcher) buildSnapshot() (map[string]time.Time, error) {
	snapshot := make(map[string]time.Time)
	for _, root := range w.watchRoots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if isHardIgnored(path) && path != root {
					return filepath.SkipDir
				}
				return nil
			}
			// Use a root-relative path for filtering so that hard-ignored
			// segment checks don't fire on absolute path components that
			// belong to the root itself (e.g. /home/user/vendor/mylib).
			relPath, relErr := filepath.Rel(root, path)
			if relErr != nil {
				relPath = path
			}
			if !w.shouldReload(relPath) {
				return nil
			}
			info, err := d.Info()
			if err == nil {
				snapshot[path] = info.ModTime()
			}
			return nil
		})
		if err != nil {
			return snapshot, err
		}
	}
	return snapshot, nil
}

// isCommentOnlyDiff reads the file at path, compares a hash of its
// comment-stripped content against the cached hash from the previous version,
// updates the cache, and returns true when the change is comment-only.
//
// The cache stores an FNV-64a hash of the stripped content (8 bytes per file)
// rather than the raw bytes, so memory usage is O(number of watched files)
// regardless of file size.
//
// Returns false (do not skip) when:
//   - The file extension is not recognized (safe fallback).
//   - The file cannot be read.
//   - The file exceeds maxCommentCheckBytes (logged at info level).
//   - No hash is cached yet (first time the file is seen).
//   - Non-comment code differs between the two versions.
func (w *Watcher) isCommentOnlyDiff(path string) bool {
	ext := filepath.Ext(path)
	style, ok := commentStyleFor(ext)
	if !ok {
		return false
	}

	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}

	// Guard: stat first so large files are never read into memory at all.
	info, err := os.Stat(absPath)
	if err != nil {
		return false
	}
	if info.Size() > maxCommentCheckBytes {
		w.log.Info(fmt.Sprintf(
			"comment-check skipped for %s (%d KB exceeds limit) — rebuild triggered",
			filepath.Base(path), info.Size()/1024,
		))
		return false
	}

	newContent, err := os.ReadFile(absPath)
	if err != nil {
		return false
	}

	newHash := strippedHash(newContent, style)

	var (
		oldHash   uint64
		hasCached bool
	)
	if cached, ok := w.contentCache.Load(absPath); ok {
		oldHash = cached.(uint64)
		hasCached = true
	}

	// Always update so the next diff has the latest baseline.
	w.contentCache.Store(absPath, newHash)

	if !hasCached {
		// First sight of this file — no baseline to compare against.
		return false
	}

	return oldHash == newHash
}

// shouldReload returns true if a change to path should trigger a rebuild.
// Filters are applied in order from cheapest to most specific.
func (w *Watcher) shouldReload(path string) bool {
	// 1. Hard-ignored directories — checked first, cheapest filter.
	for _, d := range hardIgnored {
		if containsSegment(path, d) {
			return false
		}
	}

	// 2. User-configured ignore patterns from pulse.yaml.
	for _, pat := range w.cfg.Ignore {
		if matched, _ := filepath.Match(pat, filepath.Base(path)); matched {
			return false
		}
	}

	// 3. .gitignore rules — respects whatever the project already ignores.
	if w.gitign != nil && w.gitign.MatchesPath(path) {
		return false
	}

	// 4. Generated and test-only file conventions — these are never compiled
	// into the production binary so there is no point triggering a rebuild
	// when they change.
	base := filepath.Base(path)
	if matchesSuffix(base, "_gen.go", ".pb.go", "_test.go") {
		return false
	}

	// 5. Extension allowlist — only files matching a watched extension or
	// exact filename (e.g. "go.mod") pass through.
	ext := filepath.Ext(path)
	for _, allowed := range w.cfg.Watch {
		if ext == allowed || filepath.Base(path) == allowed {
			return true
		}
	}

	// Nothing matched — ignore.
	return false
}

// relPathForFilter returns path relative to whichever watch root it falls under.
// This ensures shouldReload's segment-based checks don't fire on path components
// that are part of the root prefix (e.g. /home/user/vendor/mylib/foo.go should
// not be excluded just because "vendor" appears in the absolute root path).
// If no root matches, path is returned unchanged.
func (w *Watcher) relPathForFilter(path string) string {
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	for _, root := range w.watchRoots {
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rootAbs, pathAbs)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return path
}

// containsSegment returns true if any path segment equals the given segment.
// e.g. containsSegment("./internal/vendor/foo.go", "vendor") → true
//
// filepath.SplitList splits on the OS path-list separator (: on Unix, ; on
// Windows), not the path separator. We use strings.Split on the slash-normalised
// path instead so every directory component is checked correctly.
func containsSegment(path, segment string) bool {
	clean := filepath.ToSlash(filepath.Clean(path))
	for _, part := range strings.Split(clean, "/") {
		if part == segment {
			return true
		}
	}
	return false
}

// isHardIgnored returns true if the path contains any hard-ignored directory segment.
func isHardIgnored(path string) bool {
	for _, d := range hardIgnored {
		if containsSegment(path, d) {
			return true
		}
	}
	return false
}

// addDirsRecursive walks root and registers every non-ignored subdirectory with fsw.
// This is required because fsnotify does not recurse automatically.
// fsw.Add failures are collected and returned as a combined error rather than
// stopping the walk — a single unregisterable directory should not prevent the
// rest of the tree from being watched.
func addDirsRecursive(fsw *fsnotify.Watcher, root string) error {
	var failures []string
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Skip paths we can't stat (e.g. permission errors, race with deletion).
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		// Skip hard-ignored directories and don't descend into them.
		if isHardIgnored(path) && path != root {
			return filepath.SkipDir
		}
		if err := fsw.Add(path); err != nil {
			failures = append(failures, path+": "+err.Error())
		}
		return nil
	})
	if len(failures) > 0 {
		return fmt.Errorf("failed to watch %d director(ies): %s", len(failures), strings.Join(failures, "; "))
	}
	return nil
}

// matchesSuffix returns true if name ends with any of the given suffixes.
func matchesSuffix(name string, suffixes ...string) bool {
	for _, s := range suffixes {
		if len(name) >= len(s) && name[len(name)-len(s):] == s {
			return true
		}
	}
	return false
}
