// Package tests contains end-to-end tests for Pulse.
// Run with: make e2e  (or RUN_E2E=1 go test ./tests/ -v)
//
// Tests are skipped unless RUN_E2E=1 is set, so they are excluded from the
// normal `go test ./...` run — they are slow, spawn real processes, and
// require a free port.
package tests

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// pulseBin is the path to the compiled pulse binary.
// Set once in TestMain and reused across all tests.
var pulseBin string

// TestMain skips all tests unless RUN_E2E=1 is set, then builds the pulse
// binary once and reuses it across all test cases.
func TestMain(m *testing.M) {
	if os.Getenv("RUN_E2E") != "1" {
		fmt.Println("skipping e2e tests (run with RUN_E2E=1 or make e2e)")
		os.Exit(0)
	}
	bin, cleanup, err := buildPulse()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to build pulse binary: %v\n", err)
		os.Exit(1)
	}
	defer cleanup()
	pulseBin = bin
	os.Exit(m.Run())
}

// freePort asks the OS for an available port by binding to :0, then releases it.
// The port is immediately available for the test process to use.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("failed to find free port: %v", err)
	}
	port := fmt.Sprintf("%d", l.Addr().(*net.TCPAddr).Port)
	l.Close()
	return port
}

// TestReloadAndKeepAlive is the primary e2e test. It verifies three scenarios
// in sequence using the same running Pulse process:
//
//  1. Normal rebuild — editing a file triggers a rebuild and the new response
//     is served after restart.
//
//  2. Keep-alive on build failure — introducing a syntax error must NOT kill
//     the running server. The old response must still be served.
//
//  3. Recovery — fixing the error triggers a clean rebuild and the new
//     response is served again.
func TestReloadAndKeepAlive(t *testing.T) {
	// Create a temp directory for the test Go project.
	dir := t.TempDir()
	port := freePort(t)

	// Write the initial test app — a minimal HTTP server returning "hello v1".
	writeGoMod(t, dir)
	writeTestApp(t, dir, "hello v1")
	writePulseYAML(t, dir, port)

	// Start Pulse and wait for the server to be ready.
	pulse := startPulse(t, dir)
	defer pulse.stop(t)

	waitForServer(t, port, 20*time.Second)
	assertResponse(t, port, "hello v1")

	// ── Scenario 1: normal rebuild ────────────────────────────────────────
	t.Log("scenario 1: editing file → expect rebuild and new response")
	writeTestApp(t, dir, "hello v2")
	waitForResponse(t, port, "hello v2", 20*time.Second)
	assertResponse(t, port, "hello v2")

	// ── Scenario 2: keep-alive on build failure ───────────────────────────
	t.Log("scenario 2: syntax error → expect old process kept alive")
	writeBrokenApp(t, dir)

	// Give Pulse time to attempt (and fail) the build.
	time.Sleep(2 * time.Second)

	// The old process must still be serving — this is Pulse's core behaviour.
	assertResponse(t, port, "hello v2")

	// ── Scenario 3: recovery ─────────────────────────────────────────────
	t.Log("scenario 3: fixing error → expect rebuild and new response")
	writeTestApp(t, dir, "hello v3")
	waitForResponse(t, port, "hello v3", 20*time.Second)
	assertResponse(t, port, "hello v3")
}

// ── Test app helpers ──────────────────────────────────────────────────────────

// appTemplate is the test HTTP server. PORT is injected via env by pulse.yaml.
const appTemplate = `package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "%s")
	})
	fmt.Printf("listening on :%%s\n", port)
	http.ListenAndServe(":"+port, nil)
}
`

// writeTestApp writes a valid main.go returning the given response string.
func writeTestApp(t *testing.T, dir, response string) {
	t.Helper()
	src := fmt.Sprintf(appTemplate, response)
	writeFile(t, filepath.Join(dir, "main.go"), src)
}

// writeBrokenApp writes a main.go with a syntax error.
func writeBrokenApp(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "main.go"), `package main

func main() {
	// syntax error: missing closing brace
`)
}

// writePulseYAML writes a pulse.yaml that:
//   - uses a short debounce (200ms) to keep tests fast
//   - injects PORT so the server listens on testPort
//   - uses a short kill_timeout to speed up restarts
func writePulseYAML(t *testing.T, dir, port string) {
	t.Helper()
	content := fmt.Sprintf(`version: 1
services:
  testapp:
    path: .
    build: go build -o ./tmp/testapp .
    run: ./tmp/testapp
    watch: [".go"]
    debounce: 200ms
    kill_timeout: 2s
    env:
      PORT: "%s"
`, port)
	writeFile(t, filepath.Join(dir, "pulse.yaml"), content)
}

func writeGoMod(t *testing.T, dir string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, "go.mod"), "module testapp\n\ngo 1.21\n")
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeFile %s: %v", path, err)
	}
}

// ── Pulse process helpers ─────────────────────────────────────────────────────

type pulseProcess struct {
	cmd    *exec.Cmd
	pipePW *os.File // write end of the output pipe — closed in stop() to unblock Wait()
}

// startPulse starts the Pulse binary in dir and returns a handle to it.
// The process is automatically stopped when the test ends via t.Cleanup.
//
// Output is captured via an explicit os.Pipe rather than using cmd.Stdout
// directly. This is necessary because the child binary (testapp) inherits
// pulse's stdout file descriptors, so closing the pulse process alone is not
// enough to unblock cmd.Wait() — the child keeps the write end open.
// By owning the pipe ourselves we can close the write end after killing pulse,
// which immediately unblocks the read goroutine and lets Wait() return.
func startPulse(t *testing.T, dir string) *pulseProcess {
	t.Helper()

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}

	cmd := exec.Command(pulseBin)
	cmd.Dir = dir
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		t.Fatalf("failed to start pulse: %v", err)
	}

	// Read from the pipe in a goroutine and forward to t.Log.
	// This runs until pr is closed in stop().
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				t.Log("[pulse] " + strings.TrimRight(string(buf[:n]), "\n"))
			}
			if err != nil {
				pr.Close()
				return
			}
		}
	}()

	p := &pulseProcess{cmd: cmd, pipePW: pw}
	t.Cleanup(func() { p.stop(t) })
	return p
}

func (p *pulseProcess) stop(t *testing.T) {
	t.Helper()
	if p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	// Closing the write end of the pipe unblocks the IO copy goroutine inside
	// exec.Cmd, which lets cmd.Wait() return promptly even if the child process
	// (testapp) is still alive and holding the inherited read descriptor.
	_ = p.pipePW.Close()
	_ = p.cmd.Wait()
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

func serverURL(port string) string {
	return "http://localhost:" + port
}

// waitForServer polls until the server responds or the timeout is reached.
func waitForServer(t *testing.T, port string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(serverURL(port))
		if err == nil {
			resp.Body.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("server did not become ready within %s", timeout)
}

// waitForResponse polls until the server returns the expected body or timeout.
func waitForResponse(t *testing.T, port, expected string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		body := getBody(t, port)
		if strings.TrimSpace(body) == strings.TrimSpace(expected) {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("server did not return %q within %s (last body: %q)", expected, timeout, getBody(t, port))
}

// assertResponse checks the current server response matches expected exactly.
func assertResponse(t *testing.T, port, expected string) {
	t.Helper()
	body := strings.TrimSpace(getBody(t, port))
	if body != strings.TrimSpace(expected) {
		t.Errorf("expected response %q, got %q", expected, body)
	}
}

func getBody(t *testing.T, port string) string {
	t.Helper()
	resp, err := http.Get(serverURL(port))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	return string(b)
}

// ── Build helpers ─────────────────────────────────────────────────────────────

// buildPulse compiles the pulse binary into a temp directory.
// Returns the binary path and a cleanup function.
func buildPulse() (string, func(), error) {
	dir, err := os.MkdirTemp("", "pulse-bin-*")
	if err != nil {
		return "", nil, err
	}

	bin := filepath.Join(dir, "pulse")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	// Resolve the module root (two levels up from tests/).
	root, err := findModuleRoot()
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}

	cmd := exec.Command("go", "build", "-o", bin, "./cmd/pulse")
	cmd.Dir = root
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("go build failed: %w", err)
	}

	return bin, func() { os.RemoveAll(dir) }, nil
}

// findModuleRoot walks up from the current directory to find go.mod.
func findModuleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

// ── Utilities ─────────────────────────────────────────────────────────────────
