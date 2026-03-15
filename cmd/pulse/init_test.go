package main

import (
	"os"
	"strings"
	"testing"

	"github.com/Pratham-Mishra04/pulse/internal/log"
)

// chdir changes to dir for the duration of the test and restores the original
// working directory on cleanup. Required because runInit and handleGitignore
// check for go.mod and pulse.yaml in the current directory.
func chdir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
}

func quietLogger() *log.Logger {
	return log.New(log.LogLevelQuiet, true)
}

// ── handleGitignore ───────────────────────────────────────────────────────────

func TestHandleGitignore_CreatesGitignoreWithTmpEntry(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	// No .gitignore exists — handleGitignore should create one.
	// We simulate "Y" input by using a pipe on stdin.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })

	// Write "y\n" so the prompt accepts the default.
	w.WriteString("y\n")
	w.Close()

	l := quietLogger()
	if err := handleGitignore(l); err != nil {
		t.Fatalf("handleGitignore: %v", err)
	}

	data, err := os.ReadFile(".gitignore")
	if err != nil {
		t.Fatalf("expected .gitignore to be created: %v", err)
	}
	if !strings.Contains(string(data), "tmp/") {
		t.Errorf(".gitignore content = %q, want it to contain \"tmp/\"", string(data))
	}
}

func TestHandleGitignore_SkipsIfAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	existing := "tmp/\n.DS_Store\n"
	if err := os.WriteFile(".gitignore", []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	l := quietLogger()
	if err := handleGitignore(l); err != nil {
		t.Fatalf("handleGitignore: %v", err)
	}

	data, err := os.ReadFile(".gitignore")
	if err != nil {
		t.Fatal(err)
	}
	// Content must not have been duplicated.
	count := strings.Count(string(data), "tmp/")
	if count != 1 {
		t.Errorf("expected exactly one 'tmp/' entry, got %d in %q", count, string(data))
	}
}

func TestHandleGitignore_AppendsToExisting(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	// .gitignore exists but does not contain tmp/.
	existing := ".DS_Store\n*.log\n"
	if err := os.WriteFile(".gitignore", []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	// Feed "y" to the prompt.
	r, w, _ := os.Pipe()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })
	w.WriteString("y\n")
	w.Close()

	l := quietLogger()
	if err := handleGitignore(l); err != nil {
		t.Fatalf("handleGitignore: %v", err)
	}

	data, err := os.ReadFile(".gitignore")
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, ".DS_Store") {
		t.Error("expected existing .gitignore content to be preserved")
	}
	if !strings.Contains(content, "tmp/") {
		t.Error("expected 'tmp/' to be appended")
	}
}

func TestHandleGitignore_RejectsOnN(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	// Feed "n" to the prompt — should not write .gitignore.
	r, w, _ := os.Pipe()
	origStdin := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin })
	w.WriteString("n\n")
	w.Close()

	l := quietLogger()
	if err := handleGitignore(l); err != nil {
		t.Fatalf("handleGitignore: %v", err)
	}

	if _, err := os.Stat(".gitignore"); err == nil {
		t.Error("expected .gitignore NOT to be created when user answers 'n'")
	}
}

// ── runInit ───────────────────────────────────────────────────────────────────

func TestRunInit_ErrorsIfPulseYAMLAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	// Pre-create pulse.yaml.
	if err := os.WriteFile("pulse.yaml", []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("go.mod", []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Create the entrypoint directory.
	if err := os.MkdirAll("cmd/api", 0755); err != nil {
		t.Fatal(err)
	}

	flagConfig = "pulse.yaml"
	err := runInit(nil, []string{"cmd/api"})
	if err == nil {
		t.Fatal("expected error when pulse.yaml already exists, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got %q", err.Error())
	}
}

func TestRunInit_ErrorsIfEntrypointMissing(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	if err := os.WriteFile("go.mod", []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}

	flagConfig = "pulse.yaml"
	err := runInit(nil, []string{"./cmd/does-not-exist"})
	if err == nil {
		t.Fatal("expected error for missing entrypoint, got nil")
	}
}

func TestRunInit_ErrorsIfGoModMissing(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	// Create the entrypoint but not go.mod.
	if err := os.MkdirAll("cmd/api", 0755); err != nil {
		t.Fatal(err)
	}

	flagConfig = "pulse.yaml"
	err := runInit(nil, []string{"cmd/api"})
	if err == nil {
		t.Fatal("expected error when go.mod is missing, got nil")
	}
	if !strings.Contains(err.Error(), "go.mod") {
		t.Errorf("expected 'go.mod' in error, got %q", err.Error())
	}
}

func TestRunInit_CreatesPulseYAML(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	if err := os.WriteFile("go.mod", []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("cmd/api", 0755); err != nil {
		t.Fatal(err)
	}

	// Suppress the .gitignore prompt by pre-creating it with tmp/ already present.
	if err := os.WriteFile(".gitignore", []byte("tmp/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	flagConfig = "pulse.yaml"
	if err := runInit(nil, []string{"cmd/api"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	data, err := os.ReadFile("pulse.yaml")
	if err != nil {
		t.Fatalf("pulse.yaml not created: %v", err)
	}
	content := string(data)

	// Verify key fields appear in the generated YAML.
	checks := []string{
		"cmd/api",   // path / build target
		"tmp/api",   // binary output path
		"version:",
	}
	for _, check := range checks {
		if !strings.Contains(content, check) {
			t.Errorf("expected %q in pulse.yaml, got:\n%s", check, content)
		}
	}
}

func TestRunInit_DerivesServiceNameFromPath(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	if err := os.WriteFile("go.mod", []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("cmd/myservice", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".gitignore", []byte("tmp/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	flagConfig = "pulse.yaml"
	if err := runInit(nil, []string{"cmd/myservice"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	data, err := os.ReadFile("pulse.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// Service name is derived from the last path component ("myservice").
	if !strings.Contains(string(data), "myservice") {
		t.Errorf("expected 'myservice' in pulse.yaml, got:\n%s", string(data))
	}
	// Binary path should be ./tmp/myservice.
	if !strings.Contains(string(data), "tmp/myservice") {
		t.Errorf("expected 'tmp/myservice' in pulse.yaml, got:\n%s", string(data))
	}
}

func TestRunInit_DefaultWatchList(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	if err := os.WriteFile("go.mod", []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("cmd/api", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".gitignore", []byte("tmp/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	flagConfig = "pulse.yaml"
	if err := runInit(nil, []string{"cmd/api"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	data, err := os.ReadFile("pulse.yaml")
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, ext := range []string{".go", "go.mod", "go.sum"} {
		if !strings.Contains(content, ext) {
			t.Errorf("expected %q in watch list in pulse.yaml, got:\n%s", ext, content)
		}
	}
}

func TestRunInit_BinaryPathMatchesBuildAndRun(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)

	if err := os.WriteFile("go.mod", []byte("module test\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll("cmd/api", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".gitignore", []byte("tmp/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	flagConfig = "pulse.yaml"
	if err := runInit(nil, []string{"cmd/api"}); err != nil {
		t.Fatalf("runInit: %v", err)
	}

	data, err := os.ReadFile("pulse.yaml")
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// Both build and run should reference the same binary path.
	if strings.Count(content, "tmp/api") < 2 {
		t.Errorf("expected binary path 'tmp/api' in both build and run fields:\n%s", content)
	}
}
