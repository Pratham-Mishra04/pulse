package engine

import (
	"hash/fnv"
	"strings"
)

// maxCommentCheckBytes is the upper bound on file size for comment-only
// detection. Files larger than this skip the check and always trigger a
// rebuild — reading and hashing very large files is not worth the cost for a
// feature intended for normal source edits.
const maxCommentCheckBytes = 1 << 20 // 1 MB

// commentStyle describes the comment syntax for a source file language.
// A zero value (all fields empty) means the language is unrecognized —
// callers should treat that as "cannot determine, do not skip".
type commentStyle struct {
	line            string // single-line comment prefix, e.g. "//" or "#"
	blockOpen       string // block comment opener, e.g. "/*" — empty if none
	blockClose      string // block comment closer, e.g. "*/" — empty if none
	indentSensitive bool   // true for languages where indentation is semantic (Python, YAML)
}

// commentStyles maps file extensions to their comment syntax.
var commentStyles = map[string]commentStyle{
	// C-style / curly-brace languages
	".go":    {line: "//", blockOpen: "/*", blockClose: "*/"},
	".js":    {line: "//", blockOpen: "/*", blockClose: "*/"},
	".jsx":   {line: "//", blockOpen: "/*", blockClose: "*/"},
	".ts":    {line: "//", blockOpen: "/*", blockClose: "*/"},
	".tsx":   {line: "//", blockOpen: "/*", blockClose: "*/"},
	".mjs":   {line: "//", blockOpen: "/*", blockClose: "*/"},
	".cjs":   {line: "//", blockOpen: "/*", blockClose: "*/"},
	".java":  {line: "//", blockOpen: "/*", blockClose: "*/"},
	".kt":    {line: "//", blockOpen: "/*", blockClose: "*/"},
	".kts":   {line: "//", blockOpen: "/*", blockClose: "*/"},
	".rs":    {line: "//", blockOpen: "/*", blockClose: "*/"},
	".c":     {line: "//", blockOpen: "/*", blockClose: "*/"},
	".cc":    {line: "//", blockOpen: "/*", blockClose: "*/"},
	".cpp":   {line: "//", blockOpen: "/*", blockClose: "*/"},
	".cxx":   {line: "//", blockOpen: "/*", blockClose: "*/"},
	".h":     {line: "//", blockOpen: "/*", blockClose: "*/"},
	".hpp":   {line: "//", blockOpen: "/*", blockClose: "*/"},
	".cs":    {line: "//", blockOpen: "/*", blockClose: "*/"},
	".swift": {line: "//", blockOpen: "/*", blockClose: "*/"},
	".php":   {line: "//", blockOpen: "/*", blockClose: "*/"},
	".scala": {line: "//", blockOpen: "/*", blockClose: "*/"},
	".dart":  {line: "//", blockOpen: "/*", blockClose: "*/"},

	// Hash-comment languages.
	// Python and YAML are indentSensitive: indentation is part of the syntax
	// (block scope in Python, nesting in YAML), so an indentation-only change
	// must be treated as a code change and not skipped.
	".py":   {line: "#", indentSensitive: true},
	".rb":   {line: "#"},
	".sh":   {line: "#"},
	".bash": {line: "#"},
	".zsh":  {line: "#"},
	".fish": {line: "#"},
	".yaml": {line: "#", indentSensitive: true},
	".yml":  {line: "#", indentSensitive: true},
	".toml": {line: "#"},
	".r":    {line: "#"},
	".pl":   {line: "#"},
	".pm":   {line: "#"},

	// CSS family
	".css":  {blockOpen: "/*", blockClose: "*/"},
	".scss": {blockOpen: "/*", blockClose: "*/"},
	".sass": {blockOpen: "/*", blockClose: "*/"},
	".less": {blockOpen: "/*", blockClose: "*/"},

	// SQL / Lua
	".sql": {line: "--", blockOpen: "/*", blockClose: "*/"},
	".lua": {line: "--"},

	// Markup / template languages
	".html":   {blockOpen: "<!--", blockClose: "-->"},
	".htm":    {blockOpen: "<!--", blockClose: "-->"},
	".xml":    {blockOpen: "<!--", blockClose: "-->"},
	".svelte": {blockOpen: "<!--", blockClose: "-->"},
	".vue":    {blockOpen: "<!--", blockClose: "-->"},
}

// commentStyleFor returns the comment syntax for ext (e.g. ".go").
// Returns (style, true) when recognized, (zero, false) otherwise.
func commentStyleFor(ext string) (commentStyle, bool) {
	s, ok := commentStyles[ext]
	return s, ok
}

// stripComments removes comment-only content from src and returns the remaining
// non-comment, non-blank lines joined by newlines.
//
// Stripping rules:
//   - Lines whose trimmed content starts with style.line are dropped.
//   - Lines that fall inside a blockOpen…blockClose span are dropped.
//   - A block open/close that occupies its own line is dropped.
//   - Inline comments that follow real code (e.g. "x = 1 // note") are NOT
//     stripped — this avoids false positives from comment markers inside
//     string literals or URLs.
//   - Blank lines are dropped so that whitespace-only edits adjacent to
//     comments are not treated as code changes.
//   - For indent-sensitive languages (Python, YAML) leading whitespace on code
//     lines is preserved, because indentation is part of the syntax. For all
//     other languages code lines are trimmed before hashing.
func stripComments(src []byte, style commentStyle) string {
	raw := strings.TrimRight(string(src), "\r\n")
	if raw == "" {
		return ""
	}
	lines := strings.Split(raw, "\n")
	result := make([]string, 0, len(lines))
	inBlock := false

	for i, line := range lines {
		// Strip Windows CR so \r\n files are handled identically to \n files.
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)

		// ── Inside a block comment ────────────────────────────────────────────
		if inBlock {
			if style.blockClose != "" {
				if idx := strings.Index(trimmed, style.blockClose); idx >= 0 {
					inBlock = false
					// Preserve any code that follows the closing marker.
					if after := strings.TrimSpace(trimmed[idx+len(style.blockClose):]); after != "" {
						result = append(result, after)
					}
				}
			}
			continue
		}

		// ── Block comment that opens on this line ─────────────────────────────
		if style.blockOpen != "" && strings.HasPrefix(trimmed, style.blockOpen) {
			rest := trimmed[len(style.blockOpen):]
			if style.blockClose != "" {
				if closeIdx := strings.Index(rest, style.blockClose); closeIdx >= 0 {
					// Block comment contained on one line — preserve any code
					// that follows the closing marker (e.g. "/* note */ x := 5").
					after := strings.TrimSpace(rest[closeIdx+len(style.blockClose):])
					if after != "" {
						result = append(result, after)
					}
					continue
				}
			}
			// Multi-line block comment starts here.
			inBlock = true
			continue
		}

		// ── Full-line single-line comment ─────────────────────────────────────
		if style.line != "" && strings.HasPrefix(trimmed, style.line) {
			// Shebang on the first line specifies the interpreter — treat it as
			// code so that changing e.g. python3 → python is not misclassified
			// as a comment-only edit and silently skips the rebuild.
			if i == 0 && strings.HasPrefix(trimmed, "#!") {
				// fall through to the append below
			} else {
				continue
			}
		}

		// ── Blank line ────────────────────────────────────────────────────────
		if trimmed == "" {
			continue
		}

		// For indent-sensitive languages preserve the original line so that
		// an indentation change is hashed as a real code difference.
		// For all other languages trim whitespace — it is not semantic.
		if style.indentSensitive {
			result = append(result, line)
		} else {
			result = append(result, trimmed)
		}
	}

	return strings.Join(result, "\n")
}

// strippedHash returns a fast non-cryptographic FNV-64a hash of the
// comment-stripped and blank-line-stripped content of src. Two versions of a
// file with the same strippedHash have identical non-comment code (with very
// high probability). Storing a hash instead of the raw content keeps the
// per-file cache entry at 8 bytes regardless of file size.
func strippedHash(src []byte, style commentStyle) uint64 {
	h := fnv.New64a()
	h.Write([]byte(stripComments(src, style))) //nolint:errcheck — fnv never errors
	return h.Sum64()
}
