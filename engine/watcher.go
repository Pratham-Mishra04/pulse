package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
	ignore "github.com/sabhiram/go-gitignore"

	"github.com/Pratham-Mishra04/pulse/internal/log"
)

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
	cfg    ServiceConfig
	log    *log.Logger
	gitign *ignore.GitIgnore // nil if no .gitignore found
}

func NewWatcher(cfg ServiceConfig, l *log.Logger) (*Watcher, error) {
	// .gitignore is optional — if it doesn't exist, gitign stays nil and
	// MatchesPath is never called. Only return an error if the file exists
	// but cannot be parsed.
	var gi *ignore.GitIgnore
	if _, err := os.Stat(".gitignore"); err == nil {
		parsed, err := ignore.CompileIgnoreFile(".gitignore")
		if err != nil {
			return nil, fmt.Errorf("failed to compile .gitignore: %w", err)
		}
		gi = parsed
	}
	return &Watcher{cfg: cfg, log: l, gitign: gi}, nil
}

// Start registers the project root with fsnotify and returns a read-only
// channel of changed file paths. The channel is closed when ctx is cancelled.
func (w *Watcher) Start(ctx context.Context) (<-chan string, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	root := w.cfg.Path
	if root == "" {
		root = "."
	}

	// Recursively add all non-ignored subdirectories to fsnotify.
	// fsnotify requires each directory to be registered explicitly — it does
	// not watch subdirectories automatically.
	if err := addDirsRecursive(fsw, root); err != nil {
		fsw.Close()
		return nil, err
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
					continue
				}
				if !w.shouldReload(ev.Name) {
					w.log.Verbose("ignored: " + ev.Name)
					continue
				}
				// Non-blocking send: if the buffer is full the event is dropped.
				// This is safe because the debounce window in Builder will coalesce
				// the remaining events into a single build anyway.
				select {
				case events <- ev.Name:
				default:
				}

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

	// 4. Generated file conventions — these change frequently but should
	// never be the source of a rebuild trigger.
	base := filepath.Base(path)
	if matchesSuffix(base, "_gen.go", ".pb.go") {
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
