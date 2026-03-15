package engine

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/google/shlex"
)

// runHook executes a single hook command in workDir and returns its combined
// output and any error. The command string is parsed with shlex so quoted
// arguments are handled correctly (same as the build command).
// Pass workDir = "" to inherit the process working directory.
func runHook(ctx context.Context, command, workDir string) (output string, elapsed time.Duration, err error) {
	start := time.Now()

	parts, parseErr := shlex.Split(command)
	if parseErr != nil {
		return "", 0, fmt.Errorf("invalid hook command %q: %w", command, parseErr)
	}
	if len(parts) == 0 {
		return "", 0, fmt.Errorf("hook command %q is empty", command)
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Dir = workDir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	err = cmd.Run()
	return buf.String(), time.Since(start), err
}
