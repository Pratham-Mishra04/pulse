package engine

import (
	"bytes"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// ServiceConfig holds the configuration for a single watched service.
// Each entry under `services:` in pulse.yaml maps to one ServiceConfig.
type ServiceConfig struct {
	// Path is the directory containing the Go package to build.
	// Used as the working directory for the build command.
	Path string `yaml:"path"`

	// Build is the full shell command used to compile the service.
	// e.g. "go build -o ./tmp/api ./cmd/api"
	Build string `yaml:"build"`

	// Run is the command used to start the compiled binary.
	// e.g. "./tmp/api --port 8080"
	Run string `yaml:"run"`

	// Watch is the list of file extensions (e.g. ".go", ".tmpl") or exact
	// filenames (e.g. "go.mod") that trigger a rebuild when changed.
	// Defaults to [".go", "go.mod", "go.sum"] if not set.
	Watch []string `yaml:"watch"`

	// Ignore is a list of additional glob patterns to exclude from watching.
	// Hard-ignored dirs (.git, vendor, tmp, etc.) are always excluded regardless.
	Ignore []string `yaml:"ignore"`

	// Env is a map of environment variables injected into the running process.
	// These are merged on top of the parent process environment.
	Env map[string]string `yaml:"env"`

	// Pre is a command run before each build. Failure is logged but does not
	// abort the build unless PreStrict is true.
	Pre string `yaml:"pre"`

	// PreStrict makes a Pre command failure abort the build entirely.
	// The old process is kept alive (same semantics as a build failure).
	PreStrict bool `yaml:"pre_strict"`

	// Post is a command run after each successful restart. Failure is logged
	// but does not affect the running process.
	Post string `yaml:"post"`

	// PostStrict makes a Post command failure log a hard error.
	// The newly started process is NOT killed — it is already running.
	PostStrict bool `yaml:"post_strict"`

	// KillTimeout is how long Pulse waits for the process to exit after SIGTERM
	// before sending SIGKILL. Defaults to 5s.
	KillTimeout time.Duration `yaml:"kill_timeout"`

	// Debounce is the quiet period after the last file event before a build
	// is triggered. Rapid saves within the window are coalesced into one build.
	// Defaults to 300ms.
	Debounce time.Duration `yaml:"debounce"`

	// NoStdin disables forwarding of the parent stdin to the child process.
	// Useful in non-interactive environments like CI.
	NoStdin bool `yaml:"no_stdin"`

	// Polling controls the file-watching strategy:
	//   "auto" (default) — use fsnotify normally; switch to polling automatically
	//                      when a Docker/container environment is detected.
	//   "on"             — always use polling (useful when auto-detection fails).
	//   "off"            — always use fsnotify, never poll (trust inotify even
	//                      inside a container).
	// Empty string is treated as "auto".
	Polling string `yaml:"polling"`

	// PollInterval is the tick rate used when polling is active.
	// Defaults to 500ms. Only meaningful when Polling is "on" or "auto" and a
	// container is detected.
	PollInterval time.Duration `yaml:"poll_interval"`

	// NoWorkspace disables automatic go.work detection and watching.
	// By default Pulse finds go.work (walking up from CWD) and adds any
	// "use" directories outside the project root as extra watch roots so that
	// changes to shared workspace modules trigger a rebuild.
	// Set to true to opt out of this behaviour.
	NoWorkspace bool `yaml:"no_workspace"`
}

// Config is the top-level structure of pulse.yaml.
type Config struct {
	// Version is the pulse.yaml schema version. Currently always 1.
	// Reserved for future backwards-compatible migrations.
	Version int `yaml:"version"`

	// Services is a map of service name → config.
	// Single-service projects have one entry; multi-service projects have many.
	Services map[string]ServiceConfig `yaml:"services"`
}

// Default values applied when the corresponding field is zero / unset.
const (
	DefaultDebounce    = 300 * time.Millisecond
	DefaultKillTimeout = 5 * time.Second
)

// expandEnv replaces ${VAR} and $VAR references in command and path fields
// with their values from the environment. This lets pulse.yaml stay free of
// hardcoded values — callers can export HOST=localhost PORT=8080 etc. before
// running pulse and the substitution happens at load time.
func (s *ServiceConfig) expandEnv() {
	s.Path = os.ExpandEnv(s.Path)
	s.Build = os.ExpandEnv(s.Build)
	s.Run = os.ExpandEnv(s.Run)
	s.Pre = os.ExpandEnv(s.Pre)
	s.Post = os.ExpandEnv(s.Post)
}

// applyDefaults fills in zero-value fields with sensible defaults.
// Called for every service after loading pulse.yaml.
func (s *ServiceConfig) applyDefaults() {
	if s.Debounce == 0 {
		s.Debounce = DefaultDebounce
	}
	if s.KillTimeout == 0 {
		s.KillTimeout = DefaultKillTimeout
	}
	// Watch defaults to Go source files only.
	if len(s.Watch) == 0 {
		s.Watch = []string{".go", "go.mod", "go.sum"}
	}
	if s.Polling == "" {
		s.Polling = "auto"
	}
	if s.PollInterval == 0 {
		s.PollInterval = defaultPollInterval
	}
}

// LoadConfig reads and parses pulse.yaml from path.
// Unknown YAML keys are treated as a hard error — Pulse will never silently
// ignore a misspelled or deprecated config key.
func LoadConfig(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // unknown keys → hard error, not silent ignore

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, err
	}

	// Apply defaults to every service after parsing.
	for name, svc := range cfg.Services {
		switch svc.Polling {
		case "", "auto", "on", "off":
		default:
			return Config{}, fmt.Errorf("service %q: invalid polling mode %q (expected auto|on|off)", name, svc.Polling)
		}
		if svc.PollInterval < 0 {
			return Config{}, fmt.Errorf("service %q: poll_interval must be >= 0", name)
		}
		svc.expandEnv()
		svc.applyDefaults()
		if svc.Build == "" {
			return Config{}, fmt.Errorf("service %q: build command is required", name)
		}
		if svc.Run == "" {
			return Config{}, fmt.Errorf("service %q: run command is required", name)
		}
		cfg.Services[name] = svc
	}

	return cfg, nil
}
