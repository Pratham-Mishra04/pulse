package log

import (
	"fmt"
	"os"
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
	fmt.Printf("%s%s %s\n", l.ts(), styleInfo.Render("[pulse]"), msg)
}

// Watch prints a [watch] line for a changed file. Suppressed in quiet mode.
func (l *Logger) Watch(file string) {
	if l.level == LogLevelQuiet {
		return
	}
	fmt.Printf("%s%s %s\n",
		l.ts(),
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
	fmt.Printf("%s%s %s %s %s\n",
		l.ts(),
		styleBuild.Render("[build]"),
		name,
		styleMs.Render(fmt.Sprintf("· %dms", elapsed.Milliseconds())),
		mark,
	)
}

// Restart prints a [restart] line with the new pid.
func (l *Logger) Restart(name string, pid int) {
	fmt.Printf("%s%s %s %s %s\n",
		l.ts(),
		styleRestart.Render("[restart]"),
		name,
		styleDim.Render("→"),
		stylePid.Render(fmt.Sprintf("pid %d", pid)),
	)
}

// Keeping prints a [run] keeping line when a build fails and the old process stays alive.
func (l *Logger) Keeping(pid int) {
	fmt.Printf("%s%s keeping previous process %s\n",
		l.ts(),
		styleRun.Render("[run]"),
		stylePid.Render(fmt.Sprintf("(pid %d)", pid)),
	)
}

// Error prints a [error] prefixed message.
func (l *Logger) Error(msg string) {
	fmt.Printf("%s%s %s\n",
		l.ts(),
		styleError.Render("[error]"),
		msg,
	)
}

// Skip prints a [skip] line when a service rebuild is skipped.
func (l *Logger) Skip(name, reason string) {
	if l.level == LogLevelQuiet {
		return
	}
	fmt.Printf("%s%s %s %s\n",
		l.ts(),
		styleSkip.Render("[skip]"),
		name,
		styleDim.Render("("+reason+")"),
	)
}

// Verbose prints only at LogLevelVerbose.
func (l *Logger) Verbose(msg string) {
	if l.level != LogLevelVerbose {
		return
	}
	fmt.Printf("%s%s %s\n",
		l.ts(),
		styleVerbose.Render("[verbose]"),
		styleVerbose.Render(msg),
	)
}
