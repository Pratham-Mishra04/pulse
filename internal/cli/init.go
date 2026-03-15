package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/Pratham-Mishra04/pulse/engine"
	"github.com/Pratham-Mishra04/pulse/internal/log"
)

var initCmd = &cobra.Command{
	Use:   "init <path>",
	Short: "Initialise Pulse for this project — creates pulse.yaml",
	Long: `pulse init creates a pulse.yaml for your project.
		It requires the path to your main package (e.g. ./cmd/api).
		It is a one-time operation — edit pulse.yaml directly for any changes.`,
	Args: cobra.ExactArgs(1),
	RunE: runInit,
}

func runInit(cmd *cobra.Command, args []string) error {
	l := newLogger()
	entrypoint := filepath.Clean(args[0])

	// Refuse to overwrite an existing pulse.yaml.
	if _, err := os.Stat(flagConfig); err == nil {
		return fmt.Errorf("pulse.yaml already exists — edit it directly to make changes")
	}

	// Reject file paths — entrypoint must be a directory.
	if strings.HasSuffix(args[0], ".go") {
		return fmt.Errorf("entrypoint must be a directory (e.g. ./cmd/api), not a .go file")
	}

	// Validate that the path exists and contains a Go package.
	if _, err := os.Stat(entrypoint); err != nil {
		return fmt.Errorf("path %q does not exist", entrypoint)
	}
	if _, err := os.Stat("go.mod"); err != nil {
		return fmt.Errorf("no go.mod found in current directory — is this a Go project?")
	}

	// Derive the service name from the directory name.
	// When the arg is "." use the working directory name instead of the
	// literal "." character (filepath.Base(".") == ".").
	name := filepath.Base(entrypoint)
	if name == "." {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("could not determine working directory: %w", err)
		}
		name = filepath.Base(wd)
	}

	// Derive binary output path and normalise to forward slashes so the
	// generated Build/Run strings are valid on all platforms (shlex parses them
	// as shell tokens; Windows backslashes would be misread as escape sequences).
	// e.g. name="api" → ./tmp/api
	binPath := filepath.ToSlash(filepath.Join(".", "tmp", name))
	// Ensure the entrypoint starts with "./" so go build treats it as a local
	// package path rather than resolving it against the stdlib or module cache.
	if entrypoint != "." && !strings.HasPrefix(entrypoint, "./") {
		entrypoint = "./" + entrypoint
	}
	entrypointSlash := filepath.ToSlash(entrypoint)

	// Build the ServiceConfig with explicit values — no hidden defaults in
	// the generated file so users can see exactly what Pulse will do.
	svc := engine.ServiceConfig{
		Path:        ".",
		Build:       fmt.Sprintf("go build -o %s %s", binPath, entrypointSlash),
		Run:         binPath,
		Watch:       []string{".go", "go.mod", "go.sum"},
		KillTimeout: engine.DefaultKillTimeout,
		Debounce:    engine.DefaultDebounce,
	}

	cfg := engine.Config{
		Version:  1,
		Services: map[string]engine.ServiceConfig{name: svc},
	}

	// Marshal to YAML and write pulse.yaml.
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to generate pulse.yaml: %w", err)
	}
	if err := os.WriteFile(flagConfig, data, 0644); err != nil {
		return fmt.Errorf("failed to write pulse.yaml: %w", err)
	}

	l.Info(fmt.Sprintf("main package: %s", entrypointSlash))
	l.Info(fmt.Sprintf("build: go build -o %s %s", binPath, entrypointSlash))
	l.Info(fmt.Sprintf("run:   %s", binPath))

	// Handle .gitignore — check if tmp/ is already present.
	if err := handleGitignore(l); err != nil {
		// Non-fatal — pulse.yaml was already written.
		l.Error(fmt.Sprintf("could not update .gitignore: %s", err))
	}

	l.Info(fmt.Sprintf("created %s — run `pulse` to start", flagConfig))
	return nil
}

// handleGitignore checks whether tmp/ is in .gitignore and either skips
// (already present), adds it after prompting, or creates the file.
func handleGitignore(l *log.Logger) error {
	const entry = "tmp/"
	const gitignorePath = ".gitignore"

	// Read existing .gitignore if it exists.
	existing, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Check if tmp/ is already present — skip prompt if so.
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == entry {
			l.Info("tmp/ already in .gitignore")
			return nil
		}
	}

	// Prompt the user. Only append on an explicit affirmative — EOF or
	// non-interactive stdin (CI, pipes) must not be treated as consent.
	fmt.Print("\n  Add tmp/ to .gitignore? [Y/n]: ")
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		// stdin closed or non-interactive — skip silently.
		return nil
	}
	answer = strings.TrimSpace(strings.ToLower(answer))

	// Accept only explicit yes (empty input = default yes; any other string = no).
	if answer != "" && answer != "y" && answer != "yes" {
		return nil
	}

	// Append tmp/ — preserve whatever was already there.
	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	// Add a newline before the entry if the file is non-empty and doesn't
	// already end with one.
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		fmt.Fprintln(f)
	}
	fmt.Fprintln(f, entry)

	l.Info("added tmp/ to .gitignore")
	return nil
}
