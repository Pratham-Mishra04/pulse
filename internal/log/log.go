package log

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Package-level styles are initialised once at program start and never mutated.
// lipgloss.Style is a value type, so concurrent reads from multiple goroutines
// (e.g. parallel service loggers) are safe without additional locking.
var (
	styleWatch   = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Bold(false)
	styleBuild   = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
	styleRestart = lipgloss.NewStyle().Foreground(lipgloss.Color("78")).Bold(true)
	styleRun     = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	styleError   = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Bold(true)
	styleSkip    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleInfo    = lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true)
	styleVerbose = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true)

	styleDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleSuccess  = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))
	styleFail     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	styleTs       = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	stylePid      = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Italic(true)
	styleFilePath = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleMs       = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	stylePrefix   = lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Bold(true)
)

// LogLevel controls how much output the logger emits.
type LogLevel string

const (
	LogLevelQuiet   LogLevel = "quiet"   // only errors and restarts
	LogLevelInfo    LogLevel = "info"    // default
	LogLevelVerbose LogLevel = "verbose" // everything including ignored file events
)

type Logger struct {
	level   LogLevel
	noColor bool
	prefix  string // service name shown before every line in multi-service mode
}

func New(level LogLevel, noColor bool) *Logger {
	l := &Logger{
		level:   level,
		noColor: noColor || !isTTY(),
	}
	if l.noColor {
		lipgloss.SetColorProfile(0)
	}
	return l
}

// WithPrefix returns a copy of the logger that prepends the service name to
// every log line. Used in multi-service mode so each service's output is
// distinguishable at a glance.
func (l *Logger) WithPrefix(name string) *Logger {
	dup := *l
	dup.prefix = name
	return &dup
}

// pfx returns the rendered service name prefix, or an empty string when running
// in single-service mode (prefix not set).
func (l *Logger) pfx() string {
	if l.prefix == "" {
		return ""
	}
	return stylePrefix.Render(l.prefix) + " "
}

func isTTY() bool {
	stat, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

func (l *Logger) ts() string {
	return styleTs.Render(time.Now().Format("15:04:05")) + " "
}

// Info prints a [pulse] prefixed general message.
func (l *Logger) Info(msg string) {
	fmt.Printf("%s%s%s %s\n", l.ts(), l.pfx(), styleInfo.Render("[pulse]"), msg)
}

// Watch prints a [watch] line for a changed file. Suppressed in quiet mode.
func (l *Logger) Watch(file string) {
	if l.level == LogLevelQuiet {
		return
	}
	fmt.Printf("%s%s%s %s\n",
		l.ts(),
		l.pfx(),
		styleWatch.Render("[watch]"),
		styleFilePath.Render(file),
	)
}

// Build prints a [build] result line. Successful builds suppressed in quiet mode.
func (l *Logger) Build(name string, elapsed time.Duration, ok bool) {
	if l.level == LogLevelQuiet && ok {
		return
	}
	mark := styleSuccess.Render("✓")
	if !ok {
		mark = styleFail.Render("✗")
	}
	fmt.Printf("%s%s%s %s %s %s\n",
		l.ts(),
		l.pfx(),
		styleBuild.Render("[build]"),
		name,
		styleMs.Render(fmt.Sprintf("· %dms", elapsed.Milliseconds())),
		mark,
	)
}

// Restart prints a [restart] line with the new pid.
func (l *Logger) Restart(name string, pid int) {
	fmt.Printf("%s%s%s %s %s %s\n",
		l.ts(),
		l.pfx(),
		styleRestart.Render("[restart]"),
		name,
		styleDim.Render("→"),
		stylePid.Render(fmt.Sprintf("pid %d", pid)),
	)
}

// Keeping prints a [run] keeping line when a build fails and the old process stays alive.
func (l *Logger) Keeping(pid int) {
	fmt.Printf("%s%s%s keeping previous process %s\n",
		l.ts(),
		l.pfx(),
		styleRun.Render("[run]"),
		stylePid.Render(fmt.Sprintf("(pid %d)", pid)),
	)
}

// Error prints a [error] prefixed message.
func (l *Logger) Error(msg string) {
	fmt.Printf("%s%s%s %s\n",
		l.ts(),
		l.pfx(),
		styleError.Render("[error]"),
		msg,
	)
}

// Hook prints a [pre] or [post] hook result line. Suppressed in quiet mode on success.
func (l *Logger) Hook(kind, name string, elapsed time.Duration, ok bool) {
	if l.level == LogLevelQuiet && ok {
		return
	}
	mark := styleSuccess.Render("✓")
	if !ok {
		mark = styleFail.Render("✗")
	}
	label := fmt.Sprintf("[%s]", kind)
	fmt.Printf("%s%s%s %s %s %s\n",
		l.ts(),
		l.pfx(),
		styleSkip.Render(label),
		name,
		styleMs.Render(fmt.Sprintf("· %dms", elapsed.Milliseconds())),
		mark,
	)
}

// Skip prints a [skip] line when a service rebuild is skipped.
func (l *Logger) Skip(name, reason string) {
	if l.level == LogLevelQuiet {
		return
	}
	fmt.Printf("%s%s%s %s %s\n",
		l.ts(),
		l.pfx(),
		styleSkip.Render("[skip]"),
		name,
		styleDim.Render("("+reason+")"),
	)
}

// ServiceInfo is a minimal descriptor passed to Banner so the log package
// does not need to import the engine package.
type ServiceInfo struct {
	Name     string
	Path     string
	Watch    []string
	Debounce string
}

// Banner prints the startup widget once before the engine begins watching.
// Suppressed in quiet mode.
func (l *Logger) Banner(version, goVersion, configPath string, services []ServiceInfo) {
	if l.level == LogLevelQuiet {
		return
	}

	styleArt     := lipgloss.NewStyle().Foreground(lipgloss.Color("99"))
	styleTag     := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleSvcName := lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
	stylePath    := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleDot     := lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
	styleWatch   := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	// ANSI-shadow figlet art for "PULSE".
	// S column taken from BIFROST brand art.
	const (
		art0 = `██████╗ ██╗   ██╗██╗     ███████╗███████╗`
		art1 = `██╔══██╗██║   ██║██║     ██╔════╝██╔════╝`
		art2 = `██████╔╝██║   ██║██║     ███████╗█████╗  `
		art3 = `██╔═══╝ ██║   ██║██║     ╚════██║██╔══╝  `
		art4 = `██║     ╚██████╔╝███████╗███████║███████╗`
		art5 = `╚═╝      ╚═════╝ ╚══════╝╚══════╝╚══════╝`
	)

	fmt.Println()
	fmt.Println(styleArt.Render(art0))
	fmt.Println(styleArt.Render(art1))
	fmt.Println(styleArt.Render(art2))
	fmt.Println(styleArt.Render(art3))
	fmt.Println(styleArt.Render(art4))
	fmt.Printf("%s  %s\n\n",
		styleArt.Render(art5),
		styleTag.Render("v"+version+" · "+goVersion),
	)

	for _, svc := range services {
		path := svc.Path
		if path == "" {
			path = "."
		}
		exts := strings.Join(svc.Watch, "  ")
		fmt.Printf("  %s  %s  %s  %s\n",
			styleSvcName.Render(svc.Name),
			stylePath.Render(path),
			styleDot.Render("·"),
			styleWatch.Render(exts),
		)
	}
	fmt.Println()
}

// Verbose prints only at LogLevelVerbose.
func (l *Logger) Verbose(msg string) {
	if l.level != LogLevelVerbose {
		return
	}
	fmt.Printf("%s%s%s %s\n",
		l.ts(),
		l.pfx(),
		styleVerbose.Render("[verbose]"),
		styleVerbose.Render(msg),
	)
}
