package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pulse.yaml")
	content := `version: 1
services:
  api:
    path: ./cmd/api
    build: go build -o ./tmp/api ./cmd/api
    run: ./tmp/api
    watch: [".go"]
    kill_timeout: 10s
    debounce: 500ms
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1", cfg.Version)
	}
	svc, ok := cfg.Services["api"]
	if !ok {
		t.Fatal("service 'api' not found")
	}
	if svc.Path != "./cmd/api" {
		t.Errorf("Path = %q, want %q", svc.Path, "./cmd/api")
	}
	if svc.Build != "go build -o ./tmp/api ./cmd/api" {
		t.Errorf("Build = %q", svc.Build)
	}
	if svc.Run != "./tmp/api" {
		t.Errorf("Run = %q", svc.Run)
	}
	if svc.KillTimeout != 10*time.Second {
		t.Errorf("KillTimeout = %s, want 10s", svc.KillTimeout)
	}
	if svc.Debounce != 500*time.Millisecond {
		t.Errorf("Debounce = %s, want 500ms", svc.Debounce)
	}
}

func TestLoadConfig_MultipleServices(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pulse.yaml")
	content := `version: 1
services:
  api:
    build: go build -o ./tmp/api ./cmd/api
    run: ./tmp/api
  worker:
    build: go build -o ./tmp/worker ./cmd/worker
    run: ./tmp/worker
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Services) != 2 {
		t.Errorf("len(Services) = %d, want 2", len(cfg.Services))
	}
	if _, ok := cfg.Services["api"]; !ok {
		t.Error("missing service 'api'")
	}
	if _, ok := cfg.Services["worker"]; !ok {
		t.Error("missing service 'worker'")
	}
}

func TestLoadConfig_EnvVars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pulse.yaml")
	content := `version: 1
services:
  api:
    build: go build -o ./tmp/api .
    run: ./tmp/api
    env:
      PORT: "8080"
      DB_URL: postgres://localhost/dev
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := cfg.Services["api"]
	if svc.Env["PORT"] != "8080" {
		t.Errorf("Env[PORT] = %q, want %q", svc.Env["PORT"], "8080")
	}
	if svc.Env["DB_URL"] != "postgres://localhost/dev" {
		t.Errorf("Env[DB_URL] = %q", svc.Env["DB_URL"])
	}
}

func TestLoadConfig_UnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pulse.yaml")
	content := `version: 1
services:
  api:
    build: go build -o ./tmp/api .
    run: ./tmp/api
    typo_field: oops
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for unknown key, got nil")
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := LoadConfig("/nonexistent/does-not-exist/pulse.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pulse.yaml")
	if err := os.WriteFile(path, []byte(":\t:invalid::yaml"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestLoadConfig_AppliesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pulse.yaml")
	// Minimal config — debounce, kill_timeout, and watch are all unset.
	content := `version: 1
services:
  api:
    build: go build -o ./tmp/api .
    run: ./tmp/api
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc := cfg.Services["api"]
	if svc.Debounce != DefaultDebounce {
		t.Errorf("Debounce = %s, want %s (default)", svc.Debounce, DefaultDebounce)
	}
	if svc.KillTimeout != DefaultKillTimeout {
		t.Errorf("KillTimeout = %s, want %s (default)", svc.KillTimeout, DefaultKillTimeout)
	}
	if len(svc.Watch) == 0 {
		t.Fatal("Watch must be set to defaults when unspecified")
	}
}

// ── applyDefaults ─────────────────────────────────────────────────────────────

func TestApplyDefaults_FillsZeroFields(t *testing.T) {
	svc := ServiceConfig{}
	svc.applyDefaults()
	if svc.Debounce != DefaultDebounce {
		t.Errorf("Debounce = %s, want %s", svc.Debounce, DefaultDebounce)
	}
	if svc.KillTimeout != DefaultKillTimeout {
		t.Errorf("KillTimeout = %s, want %s", svc.KillTimeout, DefaultKillTimeout)
	}
	expected := []string{".go", "go.mod", "go.sum"}
	if len(svc.Watch) != len(expected) {
		t.Fatalf("Watch = %v, want %v", svc.Watch, expected)
	}
	for i, v := range expected {
		if svc.Watch[i] != v {
			t.Errorf("Watch[%d] = %q, want %q", i, svc.Watch[i], v)
		}
	}
}

func TestApplyDefaults_RespectsExistingValues(t *testing.T) {
	svc := ServiceConfig{
		Debounce:    1 * time.Second,
		KillTimeout: 30 * time.Second,
		Watch:       []string{".ts", ".tsx"},
	}
	svc.applyDefaults()
	if svc.Debounce != 1*time.Second {
		t.Errorf("Debounce overwritten: got %s", svc.Debounce)
	}
	if svc.KillTimeout != 30*time.Second {
		t.Errorf("KillTimeout overwritten: got %s", svc.KillTimeout)
	}
	if len(svc.Watch) != 2 || svc.Watch[0] != ".ts" {
		t.Errorf("Watch overwritten: got %v", svc.Watch)
	}
}
