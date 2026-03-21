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

	// Post is a command run after a successful build but before the process is
	// swapped. On success (or non-strict failure) the old process is stopped
	// and the new binary is started. On failure with post_strict: true the old
	// process is kept alive — same semantics as pre_strict for the build step.
	Post string `yaml:"post"`

	// PostStrict makes a Post failure abort the swap entirely.
	// The old process stays alive, identical to a build failure.
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

	// Proxy enables zero-downtime restarts. Pulse binds the public address
	// and reverse-proxies to the process on a dynamic internal port.
	// When set, HealthCheck must also be configured.
	// Pulse injects PORT into the process environment with the internal port —
	// any PORT value in env: is ignored when proxy is active.
	Proxy *ProxyConfig `yaml:"proxy,omitempty"`

	// HealthCheck configures how Pulse polls the new process for readiness
	// before atomically swapping it behind the proxy.
	// Required when proxy is set.
	HealthCheck *HealthCheckConfig `yaml:"healthcheck,omitempty"`
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
	DefaultDebounce             = 300 * time.Millisecond
	DefaultKillTimeout          = 5 * time.Second
	DefaultHealthCheckInterval  = 1 * time.Second
	DefaultHealthCheckTimeout   = 60 * time.Second
	DefaultHealthCheckThreshold = 1

	DefaultProxyReadTimeout  = 30 * time.Second
	DefaultProxyWriteTimeout = 60 * time.Second
	DefaultProxyIdleTimeout  = 120 * time.Second
)

// ProxyConfig enables zero-downtime restarts by having Pulse own the public
// port and reverse-proxy to the actual process on a dynamic internal port.
// When set, healthcheck must also be configured.
type ProxyConfig struct {
	// Addr is the public address Pulse listens on, e.g. ":8080".
	Addr string `yaml:"addr"`

	// ReadTimeout is the maximum duration for reading the entire request.
	// Defaults to 30s.
	ReadTimeout time.Duration `yaml:"read_timeout"`

	// WriteTimeout is the maximum duration before timing out writes of the
	// response. Defaults to 60s.
	WriteTimeout time.Duration `yaml:"write_timeout"`

	// IdleTimeout is the maximum amount of time to wait for the next request
	// when keep-alives are enabled. Defaults to 120s.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
}

// HealthCheckConfig controls how Pulse polls the new process before promoting
// it behind the proxy. Only meaningful when proxy is set.
type HealthCheckConfig struct {
	// Path is the HTTP endpoint to poll, e.g. "/health". Required.
	Path string `yaml:"path"`

	// Interval is how often to poll. Defaults to 1s.
	Interval time.Duration `yaml:"interval"`

	// Timeout is the total budget for the health check. If the process does
	// not become healthy within this window, the swap is aborted and the old
	// process is kept alive. Defaults to 60s.
	Timeout time.Duration `yaml:"timeout"`

	// Threshold is the number of consecutive 200 responses required before
	// the process is promoted. Defaults to 1.
	Threshold int `yaml:"threshold"`
}

// expandEnv replaces ${VAR} and $VAR references in command and path fields
// with their values from the environment. This lets pulse.yaml stay free of
// hardcoded values — callers can export HOST=localhost etc. before running
// pulse and the substitution happens at load time.
//
// NOTE: Run is intentionally excluded. The run command is executed inside a
// child shell at runtime, so env vars like $PORT are expanded there — after
// Pulse has injected any dynamic values (e.g. the internal proxy port) into
// the child's environment. Expanding Run at load time would bake in the
// parent's PORT value and prevent proxy port injection from taking effect.
func (s *ServiceConfig) expandEnv() {
	s.Path = os.ExpandEnv(s.Path)
	s.Build = os.ExpandEnv(s.Build)
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
	if s.Proxy != nil {
		if s.Proxy.ReadTimeout == 0 {
			s.Proxy.ReadTimeout = DefaultProxyReadTimeout
		}
		if s.Proxy.WriteTimeout == 0 {
			s.Proxy.WriteTimeout = DefaultProxyWriteTimeout
		}
		if s.Proxy.IdleTimeout == 0 {
			s.Proxy.IdleTimeout = DefaultProxyIdleTimeout
		}
	}
	if s.HealthCheck != nil {
		if s.HealthCheck.Interval == 0 {
			s.HealthCheck.Interval = DefaultHealthCheckInterval
		}
		if s.HealthCheck.Timeout == 0 {
			s.HealthCheck.Timeout = DefaultHealthCheckTimeout
		}
		if s.HealthCheck.Threshold == 0 {
			s.HealthCheck.Threshold = DefaultHealthCheckThreshold
		}
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
		if svc.Run == "" {
			return Config{}, fmt.Errorf("service %q: run command is required", name)
		}
		if svc.Proxy != nil {
			if svc.Proxy.Addr == "" {
				return Config{}, fmt.Errorf("service %q: proxy.addr is required", name)
			}
			if svc.HealthCheck == nil {
				return Config{}, fmt.Errorf("service %q: healthcheck is required when proxy is set", name)
			}
			if svc.HealthCheck.Path == "" {
				return Config{}, fmt.Errorf("service %q: healthcheck.path is required", name)
			}
			if svc.Proxy.ReadTimeout < 0 || svc.Proxy.WriteTimeout < 0 || svc.Proxy.IdleTimeout < 0 {
				return Config{}, fmt.Errorf("service %q: proxy timeouts must be >= 0", name)
			}
			if svc.HealthCheck.Interval < 0 || svc.HealthCheck.Timeout < 0 {
				return Config{}, fmt.Errorf("service %q: healthcheck interval and timeout must be >= 0", name)
			}
			if svc.HealthCheck.Threshold < 0 {
				return Config{}, fmt.Errorf("service %q: healthcheck.threshold must be >= 0", name)
			}
			if _, hasPort := svc.Env["PORT"]; hasPort {
				fmt.Printf("warning: service %q: PORT in env is ignored when proxy is set — Pulse injects PORT automatically\n", name)
			}
		}
		if svc.HealthCheck != nil && svc.Proxy == nil {
			fmt.Printf("warning: service %q: healthcheck is ignored when proxy is not set\n", name)
		}
		cfg.Services[name] = svc
	}

	return cfg, nil
}
