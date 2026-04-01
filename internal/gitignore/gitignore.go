// Package gitignore parses .gitignore files and matches paths against them.
// The parsing rules follow the Git documentation:
// http://git-scm.com/docs/gitignore
package gitignore

import (
	"os"
	"regexp"
	"strings"
)

// Precompiled regexps used by getPatternFromLine. Compiling once at init time
// avoids re-compiling the same patterns for every line in every .gitignore file.
var (
	reEscapedHashBang  = regexp.MustCompile(`^(\#|\!)`)
	reSubdirGlob       = regexp.MustCompile(`([^\/+])/.*\*\.`)
	reEscapeDot        = regexp.MustCompile(`\.`)
	reDoubleStar       = regexp.MustCompile(`/\*\*/`)
	reLeadingDoubleStar = regexp.MustCompile(`\*\*/`)
	reTrailingDoubleStar = regexp.MustCompile(`/\*\*`)
	reEscapedStar      = regexp.MustCompile(`\\\*`)
	reStar             = regexp.MustCompile(`\*`)
)

// IgnoreParser is an interface with MatchesPath.
type IgnoreParser interface {
	MatchesPath(f string) bool
	MatchesPathHow(f string) (bool, *IgnorePattern)
}

// getPatternFromLine converts a single .gitignore line into a compiled regexp
// and a flag indicating whether it is a negation pattern.
func getPatternFromLine(line string) (*regexp.Regexp, bool) {
	// Trim OS-specific carriage returns.
	line = strings.TrimRight(line, "\r")

	// Strip comments [Rule 2]
	if strings.HasPrefix(line, `#`) {
		return nil, false
	}

	// Trim string [Rule 3]. Note: spaces escaped with a trailing backslash
	// (e.g. "foo\ ") are also stripped — literal trailing spaces in patterns
	// are not supported.
	line = strings.Trim(line, " ")

	if line == "" {
		return nil, false
	}

	negatePattern := false
	if line[0] == '!' {
		negatePattern = true
		line = line[1:]
	}

	// Handle [Rule 2, 4], when # or ! is escaped with a backslash.
	if reEscapedHashBang.MatchString(line) {
		line = line[1:]
	}

	// If we encounter a foo/*.blah in a folder, prepend the / char.
	if reSubdirGlob.MatchString(line) && line[0] != '/' {
		line = "/" + line
	}

	// Handle escaping the "." char.
	line = reEscapeDot.ReplaceAllString(line, `\.`)

	magicStar := "#$~"

	// Handle "/**/" usage.
	if strings.HasPrefix(line, "/**/") {
		line = line[1:]
	}
	line = reDoubleStar.ReplaceAllString(line, `(/|/.+/)`)
	line = reLeadingDoubleStar.ReplaceAllString(line, `(|.`+magicStar+`/)`)
	line = reTrailingDoubleStar.ReplaceAllString(line, `(|/.`+magicStar+`)`)

	// Handle escaping the "*" char.
	line = reEscapedStar.ReplaceAllString(line, `\`+magicStar)
	line = reStar.ReplaceAllString(line, `([^/]*)`)

	// Handle the "?" wildcard — matches any single non-slash character [gitignore spec].
	line = strings.ReplaceAll(line, "?", `([^/])`)

	line = strings.ReplaceAll(line, magicStar, "*")

	var expr string
	if strings.HasSuffix(line, "/") {
		expr = line + "(|.*)$"
	} else {
		expr = line + "(|/.*)$"
	}
	if strings.HasPrefix(expr, "/") {
		expr = "^(|/)" + expr[1:]
	} else {
		expr = "^(|.*/)" + expr
	}
	pattern, err := regexp.Compile(expr)
	if err != nil {
		return nil, false
	}

	return pattern, negatePattern
}

// IgnorePattern encapsulates a compiled pattern and whether it is negated.
type IgnorePattern struct {
	Pattern *regexp.Regexp
	Negate  bool
	LineNo  int
	Line    string
}

// GitIgnore holds a list of compiled ignore patterns parsed from a .gitignore file.
type GitIgnore struct {
	patterns []*IgnorePattern
}

// CompileIgnoreLines accepts a variadic set of strings and returns a GitIgnore
// instance with the lines compiled into regexp patterns.
func CompileIgnoreLines(lines ...string) *GitIgnore {
	gi := &GitIgnore{}
	for i, line := range lines {
		pattern, negatePattern := getPatternFromLine(line)
		if pattern != nil {
			ip := &IgnorePattern{pattern, negatePattern, i + 1, line}
			gi.patterns = append(gi.patterns, ip)
		}
	}
	return gi
}

// CompileIgnoreFile parses the .gitignore file at fpath and returns a GitIgnore.
func CompileIgnoreFile(fpath string) (*GitIgnore, error) {
	bs, err := os.ReadFile(fpath)
	if err != nil {
		return nil, err
	}
	return CompileIgnoreLines(strings.Split(string(bs), "\n")...), nil
}

// MatchesPath returns true if the given path is matched by any pattern in gi.
func (gi *GitIgnore) MatchesPath(f string) bool {
	matched, _ := gi.MatchesPathHow(f)
	return matched
}

// MatchesPathHow returns whether the path is matched and which pattern matched it.
func (gi *GitIgnore) MatchesPathHow(f string) (bool, *IgnorePattern) {
	f = strings.ReplaceAll(f, string(os.PathSeparator), "/")

	matchesPath := false
	var mip *IgnorePattern
	for _, ip := range gi.patterns {
		if ip.Pattern.MatchString(f) {
			if !ip.Negate {
				matchesPath = true
				mip = ip
			} else if matchesPath {
				matchesPath = false
				mip = nil
			}
		}
	}
	return matchesPath, mip
}
