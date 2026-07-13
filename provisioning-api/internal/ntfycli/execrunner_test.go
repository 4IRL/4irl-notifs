package ntfycli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeStubBinary writes an executable shell stub that echoes its args and the
// NTFY_PASSWORD env var to stdout, a line to stderr, and exits with exitCode.
func writeStubBinary(t *testing.T, exitCode int) string {
	t.Helper()
	stubPath := filepath.Join(t.TempDir(), "ntfy-stub.sh")
	script := "#!/bin/sh\n" +
		"echo \"args:$*\"\n" +
		"echo \"pw:$NTFY_PASSWORD\"\n" +
		">&2 echo \"stub-stderr\"\n" +
		"exit " + itoa(exitCode) + "\n"
	if writeErr := os.WriteFile(stubPath, []byte(script), 0o755); writeErr != nil {
		t.Fatalf("writing stub: %v", writeErr)
	}
	return stubPath
}

// itoa avoids importing strconv purely for one small conversion in the test.
func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	return string(rune('0' + value))
}

func TestExecRunnerRunsBinaryWithArgsAndEnv(t *testing.T) {
	runner := ExecRunner{BinaryPath: writeStubBinary(t, 0)}

	stdout, stderr, runErr := runner.Run(context.Background(), []string{"user", "add", "alice"}, []string{"NTFY_PASSWORD=sekrit"})
	if runErr != nil {
		t.Fatalf("Run returned unexpected error: %v", runErr)
	}
	if !strings.Contains(stdout, "args:user add alice") {
		t.Fatalf("stdout missing args, got %q", stdout)
	}
	if !strings.Contains(stdout, "pw:sekrit") {
		t.Fatalf("stdout missing forwarded NTFY_PASSWORD env, got %q", stdout)
	}
	if !strings.Contains(stderr, "stub-stderr") {
		t.Fatalf("stderr not captured, got %q", stderr)
	}
}

func TestExecRunnerReturnsErrorAndStderrOnNonZeroExit(t *testing.T) {
	runner := ExecRunner{BinaryPath: writeStubBinary(t, 1)}

	_, stderr, runErr := runner.Run(context.Background(), []string{"user", "add", "alice"}, nil)
	if runErr == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
	if !strings.Contains(stderr, "stub-stderr") {
		t.Fatalf("stderr should still be captured on failure, got %q", stderr)
	}
}
