package cli

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
)

var helpCmd = &cobra.Command{
	Use:   "help",
	Short: "Show all commands and pulse.yaml configuration options",
	Run: func(cmd *cobra.Command, args []string) {
		printHelp()
	},
}

func printHelp() {
	// ── Styles ────────────────────────────────────────────────────────────────
	var (
		sectionStyle  lipgloss.Style
		dividerStyle  lipgloss.Style
		keyStyle      lipgloss.Style
		typeStyle     lipgloss.Style
		requiredStyle lipgloss.Style
		descStyle     lipgloss.Style
		defaultStyle  lipgloss.Style
		cmdStyle      lipgloss.Style
		flagStyle     lipgloss.Style
		dimStyle      lipgloss.Style
	)
	if flagNoColor {
		sectionStyle  = lipgloss.NewStyle()
		dividerStyle  = lipgloss.NewStyle()
		keyStyle      = lipgloss.NewStyle()
		typeStyle     = lipgloss.NewStyle()
		requiredStyle = lipgloss.NewStyle()
		descStyle     = lipgloss.NewStyle()
		defaultStyle  = lipgloss.NewStyle()
		cmdStyle      = lipgloss.NewStyle()
		flagStyle     = lipgloss.NewStyle()
		dimStyle      = lipgloss.NewStyle()
	} else {
		sectionStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true)
		dividerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))
		keyStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
		typeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		requiredStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
		descStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		defaultStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Italic(true)
		cmdStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true)
		flagStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
		dimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	}

	div := func(label string) string {
		line := "─────────────────────────────────────────────────"
		if label == "" {
			return dividerStyle.Render(line)
		}
		return dividerStyle.Render("── ") + sectionStyle.Render(label) + " " + dividerStyle.Render(line[:len(line)-len(label)-3])
	}

	row := func(key, typ, def, desc string) {
		keyCol  := fmt.Sprintf("  %-16s", key)
		typeCol := fmt.Sprintf("%-10s", typ)
		req := ""
		if def == "required" {
			req = " " + requiredStyle.Render("required")
			def = ""
		}
		fmt.Printf("%s%s%s%s\n",
			keyStyle.Render(keyCol),
			typeStyle.Render(typeCol),
			descStyle.Render(desc),
			req,
		)
		if def != "" {
			fmt.Printf("  %-26s%s\n", "", defaultStyle.Render("default: "+def))
		}
	}

	fmt.Println()

	// ── Commands ──────────────────────────────────────────────────────────────
	fmt.Println("  " + sectionStyle.Render("COMMANDS"))
	fmt.Println()
	fmt.Printf("  %s  %s\n", cmdStyle.Render("pulse"),                   dimStyle.Render("Start watching and rebuilding (default)"))
	fmt.Printf("  %s  %s\n", cmdStyle.Render("pulse init <path>"),       dimStyle.Render("Create pulse.yaml for your project"))
	fmt.Printf("  %s  %s\n", cmdStyle.Render("pulse version"),           dimStyle.Render("Print version and runtime info"))
	fmt.Printf("  %s  %s\n", cmdStyle.Render("pulse migrate <path>"),    dimStyle.Render("Migrate an Air .air.toml to pulse.yaml"))
	fmt.Printf("  %s  %s\n", cmdStyle.Render("pulse help"),              dimStyle.Render("Show this reference"))
	fmt.Println()

	// ── Global flags ──────────────────────────────────────────────────────────
	fmt.Println("  " + sectionStyle.Render("GLOBAL FLAGS"))
	fmt.Println()
	fmt.Printf("  %s  %s\n", flagStyle.Render("--config <file>"), dimStyle.Render("Path to config file  (default: pulse.yaml)"))
	fmt.Printf("  %s      %s\n", flagStyle.Render("--quiet,   -q"), dimStyle.Render("Only show errors and restarts"))
	fmt.Printf("  %s    %s\n", flagStyle.Render("--verbose, -v"), dimStyle.Render("Show all file events including ignored ones"))
	fmt.Printf("  %s    %s\n", flagStyle.Render("--no-color    "), dimStyle.Render("Disable ANSI colour output"))
	fmt.Println()

	// ── pulse.yaml reference ──────────────────────────────────────────────────
	fmt.Println("  " + sectionStyle.Render("PULSE.YAML REFERENCE"))
	fmt.Println()

	var sampleKey, sampleVal, sampleCmt lipgloss.Style
	if flagNoColor {
		sampleKey = lipgloss.NewStyle()
		sampleVal = lipgloss.NewStyle()
		sampleCmt = lipgloss.NewStyle()
	} else {
		sampleKey = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
		sampleVal = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
		sampleCmt = lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Italic(true)
	}

	line := func(indent, key, val, comment string) {
		k := sampleKey.Render(indent + key + ":")
		v := ""
		if val != "" {
			v = " " + sampleVal.Render(val)
		}
		c := ""
		if comment != "" {
			c = "  " + sampleCmt.Render("# "+comment)
		}
		fmt.Println("  " + k + v + c)
	}

	line("", "version", "1", "")
	line("", "services", "", "")
	line("  ", "api", "", "")
	line("    ", "path", ".", "working directory")
	line("    ", "build", "go build -o ./tmp/api ./cmd/api", "omit for plain-process services")
	line("    ", "run", "./tmp/api --port 8080", "required")
	line("    ", "watch", "[.go, go.mod, go.sum]", "extensions or filenames that trigger a rebuild")
	line("    ", "ignore", "[mock_*.go]", "glob patterns to exclude")
	line("    ", "env", "", "")
	line("      ", "PORT", "\"8080\"", "injected into the process environment")
	line("    ", "pre", "go generate ./...", "runs before each build")
	line("    ", "pre_strict", "false", "abort build on pre failure")
	line("    ", "post", "curl -sf http://localhost:8080/healthz", "runs after build, before swap")
	line("    ", "post_strict", "false", "abort swap on post failure")
	line("    ", "debounce", "300ms", "quiet period before a build fires")
	line("    ", "kill_timeout", "5s", "SIGTERM wait before SIGKILL")
	line("    ", "no_stdin", "false", "disable stdin forwarding")
	line("    ", "polling", "auto", "auto | on | off")
	line("    ", "poll_interval", "500ms", "tick rate when polling")
	line("    ", "no_workspace", "false", "disable go.work detection")
	fmt.Println()

	fmt.Println("  " + div("Core"))
	fmt.Println()
	row("path",  "string",   ".",        "Working directory for the build command")
	row("build", "string",   "",         "Compile command (omit for plain-process services)")
	row("run",   "string",   "required", "Start command")
	fmt.Println()

	fmt.Println("  " + div("Watching"))
	fmt.Println()
	row("watch",    "[]string", ".go  go.mod  go.sum", "File extensions or names that trigger a rebuild")
	row("ignore",   "[]string", "",                    "Extra glob patterns to exclude")
	fmt.Printf("  %-26s%s\n", "", defaultStyle.Render("always ignored: .git  vendor  tmp  node_modules  testdata  *_gen.go  *.pb.go"))
	row("debounce", "duration", "300ms",               "Quiet period after last file event before building")
	fmt.Println()

	fmt.Println("  " + div("Hooks"))
	fmt.Println()
	row("pre",         "string", "", "Command run before each build")
	row("pre_strict",  "bool",   "false", "Abort build on pre failure — old process stays alive")
	fmt.Println()
	row("post",        "string", "", "Command run after build, before the process is swapped")
	row("post_strict", "bool",   "false", "Abort swap on post failure — old process stays alive")
	fmt.Println()

	fmt.Println("  " + div("Process"))
	fmt.Println()
	row("env",          "map[string]string", "", "Environment variables injected into the process")
	row("kill_timeout", "duration", "5s",    "Wait for SIGTERM before escalating to SIGKILL")
	row("no_stdin",     "bool",     "false", "Disable stdin forwarding (useful in CI)")
	fmt.Println()

	fmt.Println("  " + div("Polling"))
	fmt.Println()
	row("polling",       "string",   "auto",  "File-watching strategy: auto | on | off")
	fmt.Printf("  %-26s%s\n", "", defaultStyle.Render("auto = inotify normally; poll when a container is detected"))
	row("poll_interval", "duration", "500ms", "Tick rate when polling is active")
	fmt.Println()

	fmt.Println("  " + div("Workspace"))
	fmt.Println()
	row("no_workspace", "bool", "false", "Disable go.work detection and external module watching")
	fmt.Println()
}
