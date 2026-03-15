package engine

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// detectGoWork searches for a go.work file starting from dir and walking up
// the directory tree — the same lookup strategy used by the Go toolchain.
// Returns the absolute path to go.work and true if found.
func detectGoWork(dir string) (string, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", false
	}
	for {
		candidate := filepath.Join(abs, "go.work")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			// Reached the filesystem root without finding go.work.
			return "", false
		}
		abs = parent
	}
}

// externalWorkspaceDirs parses a go.work file and returns the absolute paths
// of any "use" directories that fall outside projectRoot.
//
// Entries inside projectRoot are already covered by the normal watch root —
// only external ones need to be added as extra watch roots.
//
// Both single-line and block forms are handled:
//
//	use ./internal           ← single-line
//	use (                    ← block form
//	    ../shared-libs
//	    ../auth
//	)
func externalWorkspaceDirs(goWorkPath, projectRoot string) ([]string, error) {
	f, err := os.Open(goWorkPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	workDir := filepath.Dir(goWorkPath)

	projectAbs, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, err
	}

	var extra []string
	inUseBlock := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		// Strip inline comments.
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}

		switch {
		case line == "use (":
			inUseBlock = true
			continue
		case inUseBlock && line == ")":
			inUseBlock = false
			continue
		}

		var usePath string
		if inUseBlock {
			usePath = line
		} else if strings.HasPrefix(line, "use ") {
			usePath = strings.TrimSpace(strings.TrimPrefix(line, "use "))
		} else {
			continue
		}

		// Resolve to an absolute path relative to go.work's directory.
		abs, err := filepath.Abs(filepath.Join(workDir, usePath))
		if err != nil {
			continue
		}

		// Skip entries that are inside (or equal to) the project root —
		// they're already being watched.
		rel, err := filepath.Rel(projectAbs, abs)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(rel, "..") {
			continue
		}

		extra = append(extra, abs)
	}

	return extra, scanner.Err()
}
