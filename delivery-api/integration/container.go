// Package integration holds the helpers for delivery-api's integration tests,
// which run against a live docker-compose stack (see make
// delivery-integration-test). This untagged file is unit-tested without docker.
package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// commandRunner runs an external command and returns its stdout. It is
// injectable so resolveServiceContainer can be unit-tested without docker.
type commandRunner func(name string, args ...string) ([]byte, error)

// composeBaseArgs returns the `docker compose` arguments that select the
// local stack: -p from COMPOSE_PROJECT_NAME when set, plus absolute paths
// derived from this source file so the working directory does not matter.
func composeBaseArgs() ([]string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("cannot locate repo root via runtime.Caller")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	composeArgs := []string{"compose"}
	if project := os.Getenv("COMPOSE_PROJECT_NAME"); project != "" {
		composeArgs = append(composeArgs, "-p", project)
	}
	composeArgs = append(composeArgs,
		"--project-directory", repoRoot,
		"-f", filepath.Join(repoRoot, "docker-compose.yml"),
	)
	return composeArgs, nil
}

// resolveServiceContainer returns the container id of a compose service. The
// envVar (set by `make delivery-integration-test`) wins; an empty value counts
// as unset. Otherwise it asks `docker compose ps -q <service>`. That fallback
// exists only for running `go test -tags integration` without make.
func resolveServiceContainer(run commandRunner, envVar string, service string) (string, error) {
	if value := os.Getenv(envVar); value != "" {
		return value, nil
	}
	composeArgs, argsErr := composeBaseArgs()
	if argsErr != nil {
		return "", argsErr
	}
	composeArgs = append(composeArgs, "ps", "-q", service)
	output, runErr := run("docker", composeArgs...)
	if runErr != nil {
		return "", fmt.Errorf("docker compose ps -q %s: %w", service, runErr)
	}
	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		return "", fmt.Errorf("no %s container found; is the stack up (make local-up)?", service)
	}
	if strings.ContainsAny(containerID, "\r\n") {
		return "", fmt.Errorf("expected one %s container, got several: %q", service, containerID)
	}
	return containerID, nil
}
