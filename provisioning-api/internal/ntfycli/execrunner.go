package ntfycli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
)

// ExecRunner is the production Runner: it invokes a real ntfy binary as a
// subprocess, forwarding extra environment entries (e.g. NTFY_PASSWORD) on top
// of the process environment.
type ExecRunner struct {
	// BinaryPath is the path to the ntfy binary (e.g. "/usr/bin/ntfy").
	BinaryPath string
}

// Run executes one ntfy invocation and returns its captured stdout and stderr
// along with any process error.
func (runner ExecRunner) Run(ctx context.Context, args []string, extraEnv []string) (string, string, error) {
	command := exec.CommandContext(ctx, runner.BinaryPath, args...)
	command.Env = append(os.Environ(), extraEnv...)

	var stdoutBuffer, stderrBuffer bytes.Buffer
	command.Stdout = &stdoutBuffer
	command.Stderr = &stderrBuffer

	runErr := command.Run()
	return stdoutBuffer.String(), stderrBuffer.String(), runErr
}
