package engine

import (
	"os"
	"path/filepath"
	"testing"
)

// ── commentStyleFor ───────────────────────────────────────────────────────────

func TestCommentStyleFor_KnownExtensions(t *testing.T) {
	cases := []struct {
		ext      string
		wantLine string
		wantOpen string
	}{
		{".go", "//", "/*"},
		{".ts", "//", "/*"},
		{".rs", "//", "/*"},
		{".java", "//", "/*"},
		{".py", "#", ""},
		{".rb", "#", ""},
		{".yaml", "#", ""},
		{".sh", "#", ""},
		{".sql", "--", "/*"},
		{".css", "", "/*"},
		{".html", "", "<!--"},
		{".svelte", "", "<!--"},
	}
	for _, tc := range cases {
		t.Run(tc.ext, func(t *testing.T) {
			s, ok := commentStyleFor(tc.ext)
			if !ok {
				t.Fatalf("commentStyleFor(%q) = _, false; want recognized", tc.ext)
			}
			if s.line != tc.wantLine {
				t.Errorf("line = %q, want %q", s.line, tc.wantLine)
			}
			if s.blockOpen != tc.wantOpen {
				t.Errorf("blockOpen = %q, want %q", s.blockOpen, tc.wantOpen)
			}
		})
	}
}

func TestCommentStyleFor_UnknownExtension(t *testing.T) {
	unknowns := []string{".xyz", ".unknown", "", ".doc", ".exe"}
	for _, ext := range unknowns {
		if _, ok := commentStyleFor(ext); ok {
			t.Errorf("commentStyleFor(%q) = _, true; want unrecognized", ext)
		}
	}
}

// ── stripComments ─────────────────────────────────────────────────────────────

func TestStripComments_GoStyle(t *testing.T) {
	style := commentStyle{line: "//", blockOpen: "/*", blockClose: "*/"}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "pure line comment is dropped",
			input: "// this is a comment\n",
			want:  "",
		},
		{
			name:  "code line is kept",
			input: "x := 5\n",
			want:  "x := 5",
		},
		{
			name:  "comment between code lines is dropped",
			input: "x := 5\n// comment\ny := 6\n",
			want:  "x := 5\ny := 6",
		},
		{
			name:  "blank lines are dropped",
			input: "x := 5\n\n\ny := 6\n",
			want:  "x := 5\ny := 6",
		},
		{
			name:  "inline comment is preserved (conservative — avoids false positives)",
			input: "x := 5 // inline comment\n",
			want:  "x := 5 // inline comment",
		},
		{
			name:  "only comments produces empty string",
			input: "// comment 1\n// comment 2\n",
			want:  "",
		},
		{
			name:  "empty input",
			input: "",
			want:  "",
		},
		{
			name:  "single-line block comment on its own line is dropped",
			input: "/* block comment */\nx := 5\n",
			want:  "x := 5",
		},
		{
			name:  "multi-line block comment is dropped",
			input: "/*\n * package docs\n * spanning lines\n */\npackage main\n",
			want:  "package main",
		},
		{
			name:  "code after block close on the same closing line is preserved",
			input: "x := 1\n/*\n comment\n*/\ny := 2\n",
			want:  "x := 1\ny := 2",
		},
		{
			name:  "leading and trailing whitespace on code lines is trimmed",
			input: "   x := 5   \n",
			want:  "x := 5",
		},
		{
			name:  "code after single-line block comment is preserved",
			input: "/* comment */ x := 5\n",
			want:  "x := 5",
		},
		{
			name:  "single-line block comment with no trailing code is dropped",
			input: "/* comment */\n",
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripComments([]byte(tc.input), style)
			if got != tc.want {
				t.Errorf("stripComments:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestStripComments_HashStyle(t *testing.T) {
	// Non-indent-sensitive (e.g. shell): leading whitespace is trimmed.
	style := commentStyle{line: "#"}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "hash comment is dropped",
			input: "# this is a comment\n",
			want:  "",
		},
		{
			name:  "shebang on first line is preserved (interpreter is semantic)",
			input: "#!/usr/bin/env bash\necho hello\n",
			want:  "#!/usr/bin/env bash\necho hello",
		},
		{
			name:  "hash-bang not on first line is treated as a comment",
			input: "echo hello\n#!/not/a/shebang\necho world\n",
			want:  "echo hello\necho world",
		},
		{
			name:  "comment between code lines dropped",
			input: "# config\nPORT=8080\n# another comment\nHOST=localhost\n",
			want:  "PORT=8080\nHOST=localhost",
		},
		{
			name:  "inline hash comment is preserved",
			input: "PORT=8080 # default port\n",
			want:  "PORT=8080 # default port",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripComments([]byte(tc.input), style)
			if got != tc.want {
				t.Errorf("stripComments:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestStripComments_IndentSensitive(t *testing.T) {
	// Python and YAML: leading whitespace must be preserved so that
	// indentation changes are detected as real code changes.
	pyStyle := commentStyle{line: "#", indentSensitive: true}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "comment dropped, indentation preserved on code lines",
			input: "# header\ndef foo():\n    return 1\n",
			want:  "def foo():\n    return 1",
		},
		{
			name:  "indentation difference is visible in output",
			input: "if x:\n        pass\n", // 8 spaces
			want:  "if x:\n        pass",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripComments([]byte(tc.input), pyStyle)
			if got != tc.want {
				t.Errorf("stripComments:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestStrippedHash_ShebangChange_DifferentHash(t *testing.T) {
	// Changing the interpreter in a shebang is a real code change — the hash
	// must differ so the rebuild is not skipped.
	style := commentStyle{line: "#"}

	python3 := []byte("#!/usr/bin/env python3\nprint('hello')\n")
	python2 := []byte("#!/usr/bin/env python\nprint('hello')\n")

	if strippedHash(python3, style) == strippedHash(python2, style) {
		t.Error("shebang change produced equal hashes — rebuild would be incorrectly skipped")
	}
}

func TestStrippedHash_IndentChange_DifferentHash(t *testing.T) {
	// In Python, changing indentation is a semantic code change — the hash
	// must differ so the rebuild is not skipped.
	pyStyle := commentStyle{line: "#", indentSensitive: true}

	correct := []byte("if x:\n    pass\n")    // 4-space indent — inside the if
	broken := []byte("if x:\npass\n")          // 0-space indent — outside the if

	if strippedHash(correct, pyStyle) == strippedHash(broken, pyStyle) {
		t.Error("indentation change in Python produced equal hashes — rebuild would be incorrectly skipped")
	}
}

func TestStrippedHash_CommentChange_IndentSensitive_SameHash(t *testing.T) {
	// A comment-only change in Python must still be detected and skipped.
	pyStyle := commentStyle{line: "#", indentSensitive: true}

	old := []byte("def foo():\n    # old comment\n    return 1\n")
	new := []byte("def foo():\n    # new comment\n    return 1\n")

	if strippedHash(old, pyStyle) != strippedHash(new, pyStyle) {
		t.Error("comment-only change in Python produced different hashes — rebuild would not be skipped")
	}
}

func TestStripComments_HTMLStyle(t *testing.T) {
	style := commentStyle{blockOpen: "<!--", blockClose: "-->"}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "single-line html comment dropped",
			input: "<!-- a comment -->\n<div>hello</div>\n",
			want:  "<div>hello</div>",
		},
		{
			name:  "multi-line html comment dropped",
			input: "<!--\n  todo: remove\n-->\n<p>content</p>\n",
			want:  "<p>content</p>",
		},
		{
			name:  "element without comment kept",
			input: "<html>\n<body>\n</body>\n</html>\n",
			want:  "<html>\n<body>\n</body>\n</html>",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripComments([]byte(tc.input), style)
			if got != tc.want {
				t.Errorf("stripComments:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestStripComments_CSSStyle(t *testing.T) {
	// CSS has only block comments, no line-comment prefix.
	style := commentStyle{blockOpen: "/*", blockClose: "*/"}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "block comment dropped",
			input: "/* reset styles */\nbody { margin: 0; }\n",
			want:  "body { margin: 0; }",
		},
		{
			name:  "multi-line block comment dropped",
			input: "/*\n * Theme: dark\n */\n.bg { color: black; }\n",
			want:  ".bg { color: black; }",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripComments([]byte(tc.input), style)
			if got != tc.want {
				t.Errorf("stripComments:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestStripComments_SQLStyle(t *testing.T) {
	style := commentStyle{line: "--", blockOpen: "/*", blockClose: "*/"}

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "double-dash comment dropped",
			input: "-- select all users\nSELECT * FROM users;\n",
			want:  "SELECT * FROM users;",
		},
		{
			name:  "block comment dropped",
			input: "/* TODO */\nSELECT 1;\n",
			want:  "SELECT 1;",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripComments([]byte(tc.input), style)
			if got != tc.want {
				t.Errorf("stripComments:\ngot:  %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// ── strippedHash ──────────────────────────────────────────────────────────────

func TestStrippedHash_SameContent_SameHash(t *testing.T) {
	style := commentStyle{line: "//", blockOpen: "/*", blockClose: "*/"}
	src := []byte("package main\n\nfunc main() {}\n")
	h1 := strippedHash(src, style)
	h2 := strippedHash(src, style)
	if h1 != h2 {
		t.Error("identical content produced different hashes")
	}
}

func TestStrippedHash_CommentChangeOnly_SameHash(t *testing.T) {
	style := commentStyle{line: "//", blockOpen: "/*", blockClose: "*/"}
	old := []byte("package main\n\n// old doc comment\nfunc main() {}\n")
	new := []byte("package main\n\n// new doc comment\nfunc main() {}\n")
	if strippedHash(old, style) != strippedHash(new, style) {
		t.Error("comment-only change produced different hashes — should be equal")
	}
}

func TestStrippedHash_CommentAdded_SameHash(t *testing.T) {
	style := commentStyle{line: "//", blockOpen: "/*", blockClose: "*/"}
	old := []byte("package main\n\nfunc main() {}\n")
	new := []byte("package main\n\n// added comment\nfunc main() {}\n")
	if strippedHash(old, style) != strippedHash(new, style) {
		t.Error("adding a comment produced different hashes — should be equal")
	}
}

func TestStrippedHash_CommentRemoved_SameHash(t *testing.T) {
	style := commentStyle{line: "//", blockOpen: "/*", blockClose: "*/"}
	old := []byte("package main\n\n// removed comment\nfunc main() {}\n")
	new := []byte("package main\n\nfunc main() {}\n")
	if strippedHash(old, style) != strippedHash(new, style) {
		t.Error("removing a comment produced different hashes — should be equal")
	}
}

func TestStrippedHash_CodeChange_DifferentHash(t *testing.T) {
	style := commentStyle{line: "//", blockOpen: "/*", blockClose: "*/"}
	old := []byte("package main\n\nfunc main() {}\n")
	new := []byte("package main\n\nfunc main() { println(\"hi\") }\n")
	if strippedHash(old, style) == strippedHash(new, style) {
		t.Error("code change produced equal hashes — should differ")
	}
}

func TestStrippedHash_BlockCommentChange_SameHash(t *testing.T) {
	style := commentStyle{line: "//", blockOpen: "/*", blockClose: "*/"}
	old := []byte("package main\n/*\n * old block\n */\nfunc main() {}\n")
	new := []byte("package main\n/*\n * new block\n */\nfunc main() {}\n")
	if strippedHash(old, style) != strippedHash(new, style) {
		t.Error("block comment change produced different hashes — should be equal")
	}
}

// ── isCommentOnlyDiff ─────────────────────────────────────────────────────────

// writeFile is a helper that writes content to a file, failing the test on error.
func writeFile(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("writeFile %q: %v", path, err)
	}
}

func TestIsCommentOnlyDiff_FirstCallAlwaysTriggersRebuild(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(ServiceConfig{Watch: []string{".go"}})

	f := filepath.Join(dir, "main.go")
	writeFile(t, f, []byte("package main\n// comment\n"))

	// No baseline in cache yet — must return false regardless of content.
	if w.isCommentOnlyDiff(f) {
		t.Error("isCommentOnlyDiff = true on first call, want false (no baseline)")
	}
}

func TestIsCommentOnlyDiff_CommentOnlyChange_SkipsRebuild(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(ServiceConfig{Watch: []string{".go"}})

	f := filepath.Join(dir, "handler.go")

	// First write — populates the cache.
	writeFile(t, f, []byte("package main\n\nfunc handle() {}\n// old comment\n"))
	w.isCommentOnlyDiff(f)

	// Change only the comment.
	writeFile(t, f, []byte("package main\n\nfunc handle() {}\n// new comment\n"))
	if !w.isCommentOnlyDiff(f) {
		t.Error("isCommentOnlyDiff = false for comment-only change, want true")
	}
}

func TestIsCommentOnlyDiff_CodeChange_TriggersRebuild(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(ServiceConfig{Watch: []string{".go"}})

	f := filepath.Join(dir, "main.go")

	writeFile(t, f, []byte("package main\n\nfunc main() {}\n"))
	w.isCommentOnlyDiff(f)

	writeFile(t, f, []byte("package main\n\nfunc main() { println(\"hi\") }\n"))
	if w.isCommentOnlyDiff(f) {
		t.Error("isCommentOnlyDiff = true for code change, want false")
	}
}

func TestIsCommentOnlyDiff_CommentAdded_SkipsRebuild(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(ServiceConfig{Watch: []string{".go"}})

	f := filepath.Join(dir, "main.go")

	writeFile(t, f, []byte("package main\n\nfunc main() {}\n"))
	w.isCommentOnlyDiff(f)

	// Add a new comment line — code is unchanged.
	writeFile(t, f, []byte("package main\n\n// entry point\nfunc main() {}\n"))
	if !w.isCommentOnlyDiff(f) {
		t.Error("isCommentOnlyDiff = false after adding comment, want true")
	}
}

func TestIsCommentOnlyDiff_CommentRemoved_SkipsRebuild(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(ServiceConfig{Watch: []string{".go"}})

	f := filepath.Join(dir, "main.go")

	writeFile(t, f, []byte("package main\n\n// entry point\nfunc main() {}\n"))
	w.isCommentOnlyDiff(f)

	// Remove the comment — code is unchanged.
	writeFile(t, f, []byte("package main\n\nfunc main() {}\n"))
	if !w.isCommentOnlyDiff(f) {
		t.Error("isCommentOnlyDiff = false after removing comment, want true")
	}
}

func TestIsCommentOnlyDiff_UnrecognizedExtension_AlwaysTriggersRebuild(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(ServiceConfig{Watch: []string{".xyz"}})

	f := filepath.Join(dir, "data.xyz")
	writeFile(t, f, []byte("some content\n"))
	w.isCommentOnlyDiff(f)

	writeFile(t, f, []byte("some content\n"))
	// No comment style known for .xyz — must never skip.
	if w.isCommentOnlyDiff(f) {
		t.Error("isCommentOnlyDiff = true for unrecognized extension, want false")
	}
}

func TestIsCommentOnlyDiff_LargeFile_AlwaysTriggersRebuild(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(ServiceConfig{Watch: []string{".go"}})

	f := filepath.Join(dir, "big.go")

	// Write a file just over the 1 MB limit.
	content := make([]byte, maxCommentCheckBytes+1)
	for i := range content {
		content[i] = 'a'
	}
	writeFile(t, f, content)
	w.isCommentOnlyDiff(f) // first call — would populate cache for small files

	writeFile(t, f, content) // identical content
	// Should return false — size guard bypasses comment detection entirely.
	if w.isCommentOnlyDiff(f) {
		t.Error("isCommentOnlyDiff = true for large file, want false")
	}
}

func TestIsCommentOnlyDiff_CacheUpdatesAfterCodeChange(t *testing.T) {
	dir := t.TempDir()
	w := newTestWatcher(ServiceConfig{Watch: []string{".go"}})

	f := filepath.Join(dir, "main.go")

	// v1 → v2: code change (triggers rebuild, updates cache to v2)
	writeFile(t, f, []byte("package main\nfunc main() {}\n"))
	w.isCommentOnlyDiff(f)

	writeFile(t, f, []byte("package main\nfunc main() { println(\"v2\") }\n"))
	w.isCommentOnlyDiff(f) // returns false, but MUST update cache to v2

	// v2 → comment added on top of v2: should be detected as comment-only
	writeFile(t, f, []byte("package main\n// added\nfunc main() { println(\"v2\") }\n"))
	if !w.isCommentOnlyDiff(f) {
		t.Error("isCommentOnlyDiff = false after comment added to v2, want true (cache should reflect v2)")
	}
}
