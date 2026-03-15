package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Pratham-Mishra04/pulse/engine"
)

// migrateSetup creates a temp dir, writes an .air.toml with the given content,
// pre-creates a .gitignore so handleGitignore skips the prompt, chdirs into the
// dir, and resets flagConfig. Returns the path to the .air.toml file.
func migrateSetup(t *testing.T, tomlContent string) string {
	t.Helper()
	dir := t.TempDir()
	chdir(t, dir)

	// Pre-create .gitignore so handleGitignore doesn't prompt.
	if err := os.WriteFile(".gitignore", []byte("tmp/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	airPath := ".air.toml"
	if err := os.WriteFile(airPath, []byte(tomlContent), 0644); err != nil {
		t.Fatal(err)
	}

	origConfig := flagConfig
	flagConfig = "pulse.yaml"
	flagQuiet = true
	flagNoColor = true
	t.Cleanup(func() {
		flagConfig = origConfig
		flagQuiet = false
		flagNoColor = false
	})
	return airPath
}

// loadGeneratedConfig reads the pulse.yaml written by runMigrate and returns
// the first service config it finds (there is always exactly one).
func loadGeneratedConfig(t *testing.T) (string, engine.ServiceConfig) {
	t.Helper()
	data, err := os.ReadFile("pulse.yaml")
	if err != nil {
		t.Fatalf("pulse.yaml not written: %v", err)
	}
	var cfg engine.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("failed to parse generated pulse.yaml: %v\nContent:\n%s", err, data)
	}
	// migrate always generates exactly one service; assert that assumption.
	if len(cfg.Services) != 1 {
		t.Fatalf("expected 1 service in generated pulse.yaml, got %d", len(cfg.Services))
	}
	for name, svc := range cfg.Services {
		return name, svc
	}
	t.Fatal("generated pulse.yaml has no services")
	return "", engine.ServiceConfig{}
}

// ── error cases ───────────────────────────────────────────────────────────────

func TestRunMigrate_ErrorsIfPulseYAMLAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	chdir(t, dir)
	if err := os.WriteFile("pulse.yaml", []byte("version: 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".air.toml", []byte(""), 0644); err != nil {
		t.Fatal(err)
	}
	origConfig := flagConfig
	flagConfig = "pulse.yaml"
	t.Cleanup(func() { flagConfig = origConfig })

	err := runMigrate(nil, []string{".air.toml"})
	if err == nil {
		t.Fatal("expected error when pulse.yaml already exists, got nil")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("expected 'already exists' in error, got %q", err.Error())
	}
}

func TestRunMigrate_ErrorsOnInvalidTOML(t *testing.T) {
	airPath := migrateSetup(t, "this is not valid toml {{{{")

	err := runMigrate(nil, []string{airPath})
	if err == nil {
		t.Fatal("expected error for invalid TOML, got nil")
	}
	if !strings.Contains(err.Error(), "failed to parse") {
		t.Errorf("expected 'failed to parse' in error, got %q", err.Error())
	}
}

// ── build.cmd → build ─────────────────────────────────────────────────────────

func TestRunMigrate_BuildCmdMapped(t *testing.T) {
	airPath := migrateSetup(t, `
[build]
cmd = "go build -o ./tmp/api ./cmd/api"
bin = "./tmp/api"
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.Build != "go build -o ./tmp/api ./cmd/api" {
		t.Errorf("build = %q, want %q", svc.Build, "go build -o ./tmp/api ./cmd/api")
	}
}

// ── run command priority ───────────────────────────────────────────────────────

func TestRunMigrate_RunPriority_FullBin(t *testing.T) {
	// full_bin should win over entrypoint and bin.
	airPath := migrateSetup(t, `
[build]
cmd     = "go build -o ./tmp/api ./cmd/api"
full_bin = "./tmp/api --port 8080"
entrypoint = ["./tmp/api"]
bin     = "./tmp/old"
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.Run != "./tmp/api --port 8080" {
		t.Errorf("run = %q, want full_bin value", svc.Run)
	}
}

func TestRunMigrate_RunPriority_Entrypoint(t *testing.T) {
	// entrypoint should win over bin when full_bin is absent.
	airPath := migrateSetup(t, `
[build]
cmd        = "go build -o ./tmp/api ./cmd/api"
entrypoint = ["./tmp/api", "--port", "8080"]
bin        = "./tmp/old"
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.Run != "./tmp/api --port 8080" {
		t.Errorf("run = %q, want entrypoint joined", svc.Run)
	}
}

func TestRunMigrate_RunPriority_EntrypointScalar(t *testing.T) {
	// Air allows entrypoint as a plain string, not just an array.
	airPath := migrateSetup(t, `
[build]
cmd        = "go build -o ./tmp/api ./cmd/api"
entrypoint = "./tmp/main"
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.Run != "./tmp/main" {
		t.Errorf("run = %q, want %q", svc.Run, "./tmp/main")
	}
}

func TestRunMigrate_RunPriority_Bin(t *testing.T) {
	// bin is the fallback when neither full_bin nor entrypoint are set.
	airPath := migrateSetup(t, `
[build]
cmd = "go build -o ./tmp/api ./cmd/api"
bin = "./tmp/api"
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.Run != "./tmp/api" {
		t.Errorf("run = %q, want bin value", svc.Run)
	}
}

func TestRunMigrate_ArgsBinAppended(t *testing.T) {
	airPath := migrateSetup(t, `
[build]
cmd      = "go build -o ./tmp/api ./cmd/api"
bin      = "./tmp/api"
args_bin = ["--port", "8080"]
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.Run != "./tmp/api --port 8080" {
		t.Errorf("run = %q, want args appended", svc.Run)
	}
}

// ── watch extensions ──────────────────────────────────────────────────────────

func TestRunMigrate_ExtensionDotNormalization(t *testing.T) {
	// Air stores extensions without the leading dot; Pulse requires one.
	airPath := migrateSetup(t, `
[build]
cmd         = "go build -o ./tmp/api ./cmd/api"
bin         = "./tmp/api"
include_ext = ["go", "html", ".tmpl"]
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)

	want := map[string]bool{".go": true, ".html": true, ".tmpl": true}
	for _, ext := range svc.Watch {
		if !strings.HasPrefix(ext, ".") {
			t.Errorf("watch extension %q is missing leading dot", ext)
		}
		delete(want, ext)
	}
	for ext := range want {
		t.Errorf("watch list missing expected extension %q, got %v", ext, svc.Watch)
	}
}

func TestRunMigrate_IncludeFilesAppendedToWatch(t *testing.T) {
	airPath := migrateSetup(t, `
[build]
cmd          = "go build -o ./tmp/api ./cmd/api"
bin          = "./tmp/api"
include_ext  = ["go"]
include_file = ["go.mod", "go.sum", ".env"]
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)

	watchStr := strings.Join(svc.Watch, " ")
	for _, f := range []string{"go.mod", "go.sum", ".env"} {
		if !strings.Contains(watchStr, f) {
			t.Errorf("watch list missing %q, got %v", f, svc.Watch)
		}
	}
}

// ── ignore patterns ───────────────────────────────────────────────────────────

func TestRunMigrate_ExcludeDirAndFileToIgnore(t *testing.T) {
	airPath := migrateSetup(t, `
[build]
cmd          = "go build -o ./tmp/api ./cmd/api"
bin          = "./tmp/api"
exclude_dir  = ["assets", "mocks"]
exclude_file = ["*.pb.go"]
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)

	ignoreStr := strings.Join(svc.Ignore, " ")
	for _, p := range []string{"assets", "mocks", "*.pb.go"} {
		if !strings.Contains(ignoreStr, p) {
			t.Errorf("ignore list missing %q, got %v", p, svc.Ignore)
		}
	}
}

// ── hooks ─────────────────────────────────────────────────────────────────────

func TestRunMigrate_PreCmdJoinedWithAnd(t *testing.T) {
	airPath := migrateSetup(t, `
[build]
cmd     = "go build -o ./tmp/api ./cmd/api"
bin     = "./tmp/api"
pre_cmd = ["go generate ./...", "go vet ./..."]
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	want := "go generate ./... && go vet ./..."
	if svc.Pre != want {
		t.Errorf("pre = %q, want %q", svc.Pre, want)
	}
}

func TestRunMigrate_PostCmdJoinedWithAnd(t *testing.T) {
	airPath := migrateSetup(t, `
[build]
cmd      = "go build -o ./tmp/api ./cmd/api"
bin      = "./tmp/api"
post_cmd = ["echo done", "notify-send rebuilt"]
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	want := "echo done && notify-send rebuilt"
	if svc.Post != want {
		t.Errorf("post = %q, want %q", svc.Post, want)
	}
}

// ── timing fields ─────────────────────────────────────────────────────────────

func TestRunMigrate_DelayToDebounce(t *testing.T) {
	airPath := migrateSetup(t, `
[build]
cmd   = "go build -o ./tmp/api ./cmd/api"
bin   = "./tmp/api"
delay = 500
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.Debounce.Milliseconds() != 500 {
		t.Errorf("debounce = %v, want 500ms", svc.Debounce)
	}
}

func TestRunMigrate_DelayZero(t *testing.T) {
	// delay = 0 is an explicit Air config value meaning "no debounce".
	// It must be preserved in the output (not silently dropped).
	airPath := migrateSetup(t, `
[build]
cmd   = "go build -o ./tmp/api ./cmd/api"
bin   = "./tmp/api"
delay = 0
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.Debounce != 0 {
		t.Errorf("debounce = %v, want 0 (explicit delay=0 should be preserved)", svc.Debounce)
	}
}

func TestRunMigrate_KillDelayToKillTimeout(t *testing.T) {
	// Air's kill_delay is an integer in nanoseconds; the migration scales values
	// < 1ms by multiplying by time.Millisecond. 5000ns → 5000ms = 5s.
	airPath := migrateSetup(t, `
[build]
cmd        = "go build -o ./tmp/api ./cmd/api"
bin        = "./tmp/api"
kill_delay = 5000
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.KillTimeout != 5*time.Second {
		t.Errorf("kill_timeout = %v, want 5s", svc.KillTimeout)
	}
}

func TestRunMigrate_PollFields(t *testing.T) {
	airPath := migrateSetup(t, `
[build]
cmd           = "go build -o ./tmp/api ./cmd/api"
bin           = "./tmp/api"
poll          = true
poll_interval = 250
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.Polling != "on" {
		t.Errorf("polling = %q, want %q", svc.Polling, "on")
	}
	if svc.PollInterval.Milliseconds() != 250 {
		t.Errorf("poll_interval = %v, want 250ms", svc.PollInterval)
	}
}

// ── root → path ───────────────────────────────────────────────────────────────

func TestRunMigrate_RootMappedToPath(t *testing.T) {
	airPath := migrateSetup(t, `
root = "./services/api"

[build]
cmd = "go build -o ./tmp/api ./cmd/api"
bin = "./tmp/api"
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.Path != "./services/api" {
		t.Errorf("path = %q, want %q", svc.Path, "./services/api")
	}
}

func TestRunMigrate_RootDotNotMapped(t *testing.T) {
	// root = "." means "current dir" — not meaningful in pulse.yaml, skip it.
	airPath := migrateSetup(t, `
root = "."

[build]
cmd = "go build -o ./tmp/api ./cmd/api"
bin = "./tmp/api"
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if svc.Path != "" {
		t.Errorf("path = %q, want empty (root=. should be ignored)", svc.Path)
	}
}

// ── service name derivation ───────────────────────────────────────────────────

func TestRunMigrate_ServiceNameFromBuildOutput(t *testing.T) {
	airPath := migrateSetup(t, `
[build]
cmd = "go build -o ./tmp/worker ./cmd/worker"
bin = "./tmp/worker"
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	name, _ := loadGeneratedConfig(t)
	if name != "worker" {
		t.Errorf("service name = %q, want %q", name, "worker")
	}
}

func TestRunMigrate_ServiceNameDefaultsToApp(t *testing.T) {
	// No -o flag in build cmd — service name should fall back to "app".
	airPath := migrateSetup(t, `
[build]
cmd = "go build ."
bin = "./tmp/main"
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	name, _ := loadGeneratedConfig(t)
	if name != "app" {
		t.Errorf("service name = %q, want %q", name, "app")
	}
}

// ── cross-directory migration ─────────────────────────────────────────────────

func TestRunMigrate_CrossDirectory(t *testing.T) {
	// Arrange: .air.toml lives in a subdirectory; the caller's CWD is the parent.
	parent := t.TempDir()
	subdir := parent + "/services/api"
	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatal(err)
	}
	airPath := subdir + "/.air.toml"
	if err := os.WriteFile(airPath, []byte(`
[build]
cmd = "go build -o ./tmp/api ./cmd/api"
bin = "./tmp/api"
`), 0644); err != nil {
		t.Fatal(err)
	}
	// Pre-create .gitignore in subdir so handleGitignore doesn't prompt.
	if err := os.WriteFile(subdir+"/.gitignore", []byte("tmp/\n"), 0644); err != nil {
		t.Fatal(err)
	}

	chdir(t, parent) // CWD is parent, NOT subdir
	origConfig := flagConfig
	flagConfig = "pulse.yaml"
	flagQuiet = true
	flagNoColor = true
	t.Cleanup(func() {
		flagConfig = origConfig
		flagQuiet = false
		flagNoColor = false
	})

	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}

	// pulse.yaml must land next to .air.toml, not in the caller's CWD.
	if _, err := os.Stat(subdir + "/pulse.yaml"); err != nil {
		t.Errorf("expected pulse.yaml next to .air.toml (%s), not found: %v", subdir, err)
	}
	if _, err := os.Stat(parent + "/pulse.yaml"); err == nil {
		t.Errorf("pulse.yaml must not be written into the caller's CWD (%s)", parent)
	}
}

// ── args with spaces ──────────────────────────────────────────────────────────

func TestRunMigrate_ArgsBinWithSpaces(t *testing.T) {
	// An args_bin value containing a space must be shell-quoted so that Pulse's
	// shlex parser can round-trip it back to the original two-token form.
	airPath := migrateSetup(t, `
[build]
cmd      = "go build -o ./tmp/api ./cmd/api"
bin      = "./tmp/api"
args_bin = ["--greeting", "hello world"]
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	// The generated run string must contain the quoted form so shlex sees two
	// arguments ("--greeting" and "hello world"), not three.
	if !strings.Contains(svc.Run, `"hello world"`) {
		t.Errorf("run = %q — space-containing arg must be double-quoted, got %q", svc.Run, svc.Run)
	}
}

func TestRunMigrate_EntrypointWithSpaces(t *testing.T) {
	// Same guarantee for entrypoint array elements containing spaces.
	airPath := migrateSetup(t, `
[build]
cmd        = "go build -o ./tmp/api ./cmd/api"
entrypoint = ["./tmp/api", "--msg", "hello world"]
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}
	_, svc := loadGeneratedConfig(t)
	if !strings.Contains(svc.Run, `"hello world"`) {
		t.Errorf("run = %q — space-containing entrypoint arg must be double-quoted", svc.Run)
	}
}

// ── generated config is valid ─────────────────────────────────────────────────

func TestRunMigrate_GeneratedConfigLoadsCleanly(t *testing.T) {
	// Verify the output is a valid pulse.yaml that engine.LoadConfig accepts.
	airPath := migrateSetup(t, `
root = "."

[build]
cmd         = "go build -o ./tmp/api ./cmd/api"
bin         = "./tmp/api"
include_ext = ["go", "html"]
exclude_dir = ["mocks"]
pre_cmd     = ["go generate ./..."]
delay       = 300
`)
	if err := runMigrate(nil, []string{airPath}); err != nil {
		t.Fatalf("runMigrate: %v", err)
	}

	cfg, err := engine.LoadConfig("pulse.yaml")
	if err != nil {
		t.Fatalf("engine.LoadConfig rejected generated pulse.yaml: %v", err)
	}
	if len(cfg.Services) == 0 {
		t.Error("generated config has no services")
	}
}
