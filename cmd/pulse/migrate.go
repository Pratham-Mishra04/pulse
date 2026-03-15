package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Pratham-Mishra04/pulse/engine"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate <path>",
	Short: "Migrate an Air config (.air.toml) to pulse.yaml",
	Long: `pulse migrate reads an existing .air.toml and generates a pulse.yaml.

Every mapped field is logged. Every dropped field is logged with a reason.
Nothing is silently ignored.`,
	Args: cobra.ExactArgs(1),
	RunE: runMigrate,
}

// airConfig mirrors the Air TOML structure. Only fields we need to inspect are
// included — unknown keys are silently tolerated (we do not use KnownFields).
type airConfig struct {
	Root        string      `toml:"root"`
	TmpDir      string      `toml:"tmp_dir"`
	TestDataDir string      `toml:"testdata_dir"`
	EnvFiles    []string    `toml:"env_files"`
	Build       airBuild    `toml:"build"`
	Log         airLog      `toml:"log"`
	Color       interface{} `toml:"color"`   // always dropped
	Misc        airMisc     `toml:"misc"`
	Screen      interface{} `toml:"screen"`  // always dropped
	Proxy       interface{} `toml:"proxy"`   // always dropped
}

// airEntrypoint unmarshals Air's build.entrypoint which can be either a
// string ("./tmp/main") or an array of strings (["./tmp/main", "arg1"]).
type airEntrypoint []string

func (e *airEntrypoint) UnmarshalTOML(v interface{}) error {
	switch val := v.(type) {
	case string:
		*e = []string{val}
	case []interface{}:
		for _, item := range val {
			s, ok := item.(string)
			if !ok {
				return fmt.Errorf("entrypoint array must contain strings, got %T", item)
			}
			*e = append(*e, s)
		}
	default:
		return fmt.Errorf("entrypoint must be a string or array of strings, got %T", v)
	}
	return nil
}

type airBuild struct {
	PreCmd                 []string      `toml:"pre_cmd"`
	Cmd                    string        `toml:"cmd"`
	PostCmd                []string      `toml:"post_cmd"`
	Bin                    string        `toml:"bin"`
	Entrypoint             airEntrypoint `toml:"entrypoint"`
	FullBin                string        `toml:"full_bin"`
	ArgsBin                []string      `toml:"args_bin"`
	Log                    string        `toml:"log"`
	IncludeExt             []string      `toml:"include_ext"`
	ExcludeDir             []string      `toml:"exclude_dir"`
	IncludeDir             []string      `toml:"include_dir"`
	ExcludeFile            []string      `toml:"exclude_file"`
	IncludeFile            []string      `toml:"include_file"`
	ExcludeRegex           []string      `toml:"exclude_regex"`
	ExcludeUnchanged       bool          `toml:"exclude_unchanged"`
	IgnoreDangerousRootDir bool          `toml:"ignore_dangerous_root_dir"`
	FollowSymlink          bool          `toml:"follow_symlink"`
	Poll                   bool          `toml:"poll"`
	PollInterval           int           `toml:"poll_interval"` // milliseconds
	Delay                  int           `toml:"delay"`         // milliseconds
	StopOnError            bool          `toml:"stop_on_error"`
	SendInterrupt          bool          `toml:"send_interrupt"`
	KillDelay              time.Duration `toml:"kill_delay"`
	Rerun                  bool          `toml:"rerun"`
	RerunDelay             int           `toml:"rerun_delay"`
}

type airLog struct {
	AddTime  bool `toml:"time"`
	MainOnly bool `toml:"main_only"`
	Silent   bool `toml:"silent"`
}

type airMisc struct {
	CleanOnExit bool `toml:"clean_on_exit"`
}

// Air default values — used to detect whether a field was explicitly set by
// the user so we only log mappings for fields that differ from the defaults.
var airDefaults = airBuild{
	Cmd:          "go build -o ./tmp/main .",
	Bin:          "./tmp/main",
	IncludeExt:   []string{"go", "tpl", "tmpl", "html"},
	ExcludeDir:   []string{"assets", "tmp", "vendor", "testdata"},
	ExcludeRegex: []string{"_test.go"},
	Delay:        1000,
	RerunDelay:   500,
}

// shellQuoteArg wraps s in double quotes if it contains whitespace or shell
// metacharacters so that Pulse's shlex parser can round-trip the value.
func shellQuoteArg(s string) string {
	if strings.ContainsAny(s, " \t\"'\\") {
		return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
	}
	return s
}

// joinArgv shell-quotes each element and joins them with a single space.
func joinArgv(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuoteArg(a)
	}
	return strings.Join(quoted, " ")
}

func runMigrate(cmd *cobra.Command, args []string) error {
	l := newLogger()
	src := args[0]

	// When flagConfig has no directory component, anchor it next to the source
	// .air.toml so that `pulse migrate path/to/.air.toml` writes pulse.yaml
	// into the same directory rather than into the caller's CWD.
	outConfig := flagConfig
	if filepath.Dir(outConfig) == "." {
		outConfig = filepath.Join(filepath.Dir(src), outConfig)
	}

	// Refuse to overwrite an existing pulse.yaml.
	if _, err := os.Stat(outConfig); err == nil {
		return fmt.Errorf("pulse.yaml already exists — remove it first")
	}

	// Parse the Air TOML file. Capture metadata so we can distinguish
	// explicit zero values (e.g. delay = 0) from absent fields.
	var air airConfig
	meta, err := toml.DecodeFile(src, &air)
	if err != nil {
		return fmt.Errorf("failed to parse %s: %w", src, err)
	}

	l.Info(fmt.Sprintf("reading %s...", src))

	var notes []string // collected drop/note messages printed at the end

	mapped := func(airField, pulseField, value string) {
		l.Info(fmt.Sprintf("mapped    %-30s → %s: %s", airField, pulseField, value))
	}
	dropped := func(airField, reason string) {
		msg := fmt.Sprintf("ignored   %-30s  (%s)", airField, reason)
		l.Info(msg)
		notes = append(notes, msg)
	}
	noted := func(airField, reason string) {
		msg := fmt.Sprintf("note      %-30s  %s", airField, reason)
		l.Info(msg)
		notes = append(notes, msg)
	}

	b := air.Build
	svc := engine.ServiceConfig{}

	// ── build.cmd → build ─────────────────────────────────────────────────────
	if b.Cmd != "" {
		svc.Build = b.Cmd
		mapped("build.cmd", "build", b.Cmd)
	}

	// ── run command: priority order is full_bin > entrypoint > bin ────────────
	//
	// full_bin: arbitrary shell command (may include env vars, wrappers).
	// entrypoint: [binary, arg1, arg2, ...] — new preferred form.
	// bin: deprecated plain binary path.
	// args_bin: extra arguments appended to whichever run form is active.
	switch {
	case b.FullBin != "":
		run := b.FullBin
		if len(b.ArgsBin) > 0 {
			run = run + " " + joinArgv(b.ArgsBin)
		}
		svc.Run = run
		mapped("build.full_bin", "run", run)
		if len(b.ArgsBin) > 0 {
			mapped("build.args_bin", "run (appended)", joinArgv(b.ArgsBin))
		}

	case len(b.Entrypoint) > 0:
		parts := make([]string, 0, len(b.Entrypoint)+len(b.ArgsBin))
		parts = append(parts, b.Entrypoint...)
		parts = append(parts, b.ArgsBin...)
		run := joinArgv(parts)
		svc.Run = run
		mapped("build.entrypoint", "run", run)
		if len(b.ArgsBin) > 0 {
			mapped("build.args_bin", "run (appended)", joinArgv(b.ArgsBin))
		}

	case b.Bin != "":
		run := shellQuoteArg(b.Bin)
		if len(b.ArgsBin) > 0 {
			run = run + " " + joinArgv(b.ArgsBin)
		}
		svc.Run = run
		mapped("build.bin (deprecated)", "run", run)
		if len(b.ArgsBin) > 0 {
			mapped("build.args_bin", "run (appended)", joinArgv(b.ArgsBin))
		}
	}

	// ── root → path ───────────────────────────────────────────────────────────
	if air.Root != "" && air.Root != "." {
		svc.Path = filepath.ToSlash(air.Root)
		mapped("root", "path", svc.Path)
	}

	// ── build.include_ext → watch ─────────────────────────────────────────────
	// Air stores extensions WITHOUT the leading dot ("go", "html").
	// Pulse requires the leading dot (".go", ".html").
	if len(b.IncludeExt) > 0 {
		watch := make([]string, 0, len(b.IncludeExt))
		for _, ext := range b.IncludeExt {
			if !strings.HasPrefix(ext, ".") {
				ext = "." + ext
			}
			watch = append(watch, ext)
		}
		svc.Watch = watch
		mapped("build.include_ext", "watch", strings.Join(watch, ", "))
	}

	// ── build.include_file → watch (exact filenames) ──────────────────────────
	if len(b.IncludeFile) > 0 {
		svc.Watch = append(svc.Watch, b.IncludeFile...)
		mapped("build.include_file", "watch (exact filenames)", strings.Join(b.IncludeFile, ", "))
	}

	// ── build.exclude_dir + exclude_file → ignore ─────────────────────────────
	// Pulse's ignore list matches against filepath.Base(path) using glob syntax.
	// Air's exclude_dir entries are directory names matched against the full path.
	// We add them as-is and leave a note about the semantic difference.
	var ignorePatterns []string
	if len(b.ExcludeDir) > 0 {
		ignorePatterns = append(ignorePatterns, b.ExcludeDir...)
		mapped("build.exclude_dir", "ignore", strings.Join(b.ExcludeDir, ", "))
		noted("build.exclude_dir", "Pulse matches ignore patterns against the base filename, not the full path. Directories already excluded by default: .git, vendor, node_modules, tmp, testdata")
	}
	if len(b.ExcludeFile) > 0 {
		ignorePatterns = append(ignorePatterns, b.ExcludeFile...)
		mapped("build.exclude_file", "ignore", strings.Join(b.ExcludeFile, ", "))
	}
	if len(ignorePatterns) > 0 {
		svc.Ignore = ignorePatterns
	}

	// ── build.include_dir ─────────────────────────────────────────────────────
	// Pulse watches the project root recursively and go.work external modules.
	// There is no direct per-service include_dir equivalent.
	if len(b.IncludeDir) > 0 {
		noted("build.include_dir", fmt.Sprintf("no direct equivalent — Pulse watches the project root recursively. Dirs: %s", strings.Join(b.IncludeDir, ", ")))
	}

	// ── build.exclude_regex ───────────────────────────────────────────────────
	// Pulse uses glob patterns, not regular expressions.
	if len(b.ExcludeRegex) > 0 {
		dropped("build.exclude_regex", "Pulse uses glob patterns (ignore:), not regexes. Convert manually if needed. Regexes were: "+strings.Join(b.ExcludeRegex, ", "))
	}

	// ── pre_cmd → pre ─────────────────────────────────────────────────────────
	// Air supports multiple pre commands (array). Pulse supports one shell
	// command string. Join with && so all must succeed.
	if len(b.PreCmd) > 0 {
		svc.Pre = strings.Join(b.PreCmd, " && ")
		mapped("build.pre_cmd", "pre", svc.Pre)
		if len(b.PreCmd) > 1 {
			noted("build.pre_cmd", "multiple commands joined with && into a single pre command")
		}
	}

	// ── post_cmd → post ───────────────────────────────────────────────────────
	// Air runs post_cmd on ^C (shutdown), not after each restart.
	// Pulse's post runs after each successful restart — semantics differ.
	if len(b.PostCmd) > 0 {
		svc.Post = strings.Join(b.PostCmd, " && ")
		mapped("build.post_cmd", "post", svc.Post)
		noted("build.post_cmd", "Air runs post_cmd on ^C (shutdown); Pulse runs post after each successful restart — verify this matches your intent")
	}

	// ── build.delay → debounce ────────────────────────────────────────────────
	// Air default is 1000ms. Pulse default is 300ms.
	// Use metadata to detect explicit delay = 0 (meaning "no debounce") vs absent.
	if b.Delay > 0 || meta.IsDefined("build", "delay") {
		svc.Debounce = time.Duration(b.Delay) * time.Millisecond
		mapped("build.delay", "debounce", svc.Debounce.String())
		if b.Delay == airDefaults.Delay {
			noted("build.delay", "Air default (1000ms) mapped; Pulse default is 300ms — consider lowering")
		}
		if b.Delay == 0 {
			noted("build.delay", "delay = 0 mapped; Pulse will apply its 300ms default unless debounce is set explicitly")
		}
	}

	// ── build.kill_delay → kill_timeout ──────────────────────────────────────
	// Air's kill_delay is the wait after SIGTERM before SIGKILL.
	// Pulse's kill_timeout is the same concept.
	// TOML integers are parsed by Go as nanoseconds into time.Duration. Since
	// users typically write values like 500 or 5000 meaning milliseconds (as
	// shown in Air's own example config), values under 1ms are scaled up by
	// multiplying by time.Millisecond. Example: 5000 (ns) → 5000ms = 5s.
	if b.KillDelay > 0 {
		kt := b.KillDelay
		if kt < time.Millisecond {
			kt = kt * time.Millisecond
		}
		svc.KillTimeout = kt
		mapped("build.kill_delay", "kill_timeout", svc.KillTimeout.String())
	}

	// ── build.poll / poll_interval → polling / poll_interval ─────────────────
	if b.Poll {
		svc.Polling = "on"
		mapped("build.poll", "polling", "on")
	}
	if b.PollInterval > 0 {
		svc.PollInterval = time.Duration(b.PollInterval) * time.Millisecond
		mapped("build.poll_interval", "poll_interval", svc.PollInterval.String())
	}

	// ── DROPPED FIELDS ────────────────────────────────────────────────────────

	// build.log: Air writes build errors to a log file in tmp_dir.
	// Pulse prints them to stdout — no file logging.
	if b.Log != "" && b.Log != "build-errors.log" {
		dropped("build.log", "Pulse logs build errors to stdout; no file logging")
	}

	// build.stop_on_error: Air kills the process when a build fails.
	// Pulse always keeps the old process alive on build failure — this is the
	// default, non-configurable behaviour (the core Pulse differentiator).
	if b.StopOnError {
		dropped("build.stop_on_error", "Pulse always keeps the old process alive on build failure; stop_on_error is not supported")
	}

	// build.send_interrupt: Air can optionally send SIGINT before killing.
	// Pulse always sends SIGTERM (configurable via kill_timeout, not signal type).
	if b.SendInterrupt {
		dropped("build.send_interrupt", "Pulse always uses SIGTERM for graceful shutdown")
	}

	// build.rerun / rerun_delay: re-runs the binary on exit without a file change.
	// Pulse does not support auto-rerun.
	if b.Rerun {
		dropped("build.rerun", "not supported in Pulse")
	}
	if b.RerunDelay > 0 && b.RerunDelay != airDefaults.RerunDelay {
		dropped("build.rerun_delay", "not supported in Pulse (rerun is not supported)")
	}

	// build.exclude_unchanged: content-hash deduplication — Pulse does not implement this.
	if b.ExcludeUnchanged {
		dropped("build.exclude_unchanged", "not supported in Pulse; mtime-based change detection is used instead")
	}

	// build.follow_symlink: Pulse does not follow symlinks during directory walking.
	if b.FollowSymlink {
		dropped("build.follow_symlink", "not supported in Pulse")
	}

	// build.ignore_dangerous_root_dir: safety guard specific to Air's walking logic.
	if b.IgnoreDangerousRootDir {
		dropped("build.ignore_dangerous_root_dir", "not applicable — Pulse uses hardcoded safe ignore list")
	}

	// root-level fields
	if air.TmpDir != "" && air.TmpDir != "tmp" {
		dropped("tmp_dir", "Pulse always uses ./tmp/; custom tmp_dir is not supported")
	}
	if air.TestDataDir != "" && air.TestDataDir != "testdata" {
		dropped("testdata_dir", "Pulse always excludes testdata/ from watching")
	}
	if len(air.EnvFiles) > 0 {
		dropped("env_files", "not supported — use shell dotenv loading (e.g. direnv) or add vars under env: in pulse.yaml")
	}

	// log section
	if air.Log.Silent {
		noted("log.silent", "use --quiet flag when running pulse")
	} else if air.Log.MainOnly {
		noted("log.main_only", "use --quiet flag when running pulse for reduced output")
	}
	if air.Log.AddTime {
		dropped("log.time", "Pulse always shows timestamps")
	}

	// misc.clean_on_exit: Air can delete tmp/ on exit. Pulse does not.
	if air.Misc.CleanOnExit {
		dropped("misc.clean_on_exit", "not supported in Pulse; delete tmp/ manually if needed")
	}

	// color / screen / proxy sections — dropped only if present in the source config
	if meta.IsDefined("color") {
		dropped("[color]", "Pulse auto-detects terminal color support; color is not configurable")
	}
	if meta.IsDefined("screen") {
		dropped("[screen]", "not supported in Pulse")
	}
	if meta.IsDefined("proxy") {
		dropped("[proxy]", "browser reload proxy is not supported in Pulse (Tier 3 roadmap)")
	}

	// ── Derive service name ───────────────────────────────────────────────────
	serviceName := "app"
	if svc.Build != "" {
		// Try to infer a name from the build output path.
		// Handles both "-o ./tmp/api" (space-separated) and "-o=./tmp/api" (equals) forms.
		parts := strings.Fields(svc.Build)
		for i, p := range parts {
			var outPath string
			if p == "-o" && i+1 < len(parts) {
				outPath = parts[i+1]
			} else if strings.HasPrefix(p, "-o=") {
				outPath = strings.TrimPrefix(p, "-o=")
			}
			if outPath != "" {
				base := filepath.Base(outPath)
				// Strip .exe suffix on Windows-targeted builds.
				base = strings.TrimSuffix(base, ".exe")
				if base != "" && base != "." {
					serviceName = base
				}
				break
			}
		}
	}

	// ── Write pulse.yaml ──────────────────────────────────────────────────────
	cfg := engine.Config{
		Version:  1,
		Services: map[string]engine.ServiceConfig{serviceName: svc},
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to generate pulse.yaml: %w", err)
	}
	if err := os.WriteFile(outConfig, data, 0644); err != nil {
		return fmt.Errorf("failed to write pulse.yaml: %w", err)
	}

	// ── .gitignore prompt ─────────────────────────────────────────────────────
	if err := handleGitignore(l); err != nil {
		l.Error(fmt.Sprintf("could not update .gitignore: %s", err))
	}

	fmt.Println()
	l.Info(fmt.Sprintf("created %s — run `pulse` to start", outConfig))

	if len(notes) > 0 {
		fmt.Println()
		l.Info("review the following before running:")
		for _, n := range notes {
			fmt.Printf("  %s\n", n)
		}
	}

	return nil
}
