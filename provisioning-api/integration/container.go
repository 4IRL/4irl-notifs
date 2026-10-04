package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// commandRunner runs an external command and returns its stdout. It is
// injectable so resolveAPIContainer can be unit-tested without docker.
type commandRunner func(name string, args ...string) ([]byte, error)

// resolveAPIContainer returns the container id of the provisioning-api service.
// NOTIFS_API_CONTAINER (set by `make go-integration-test`) wins; an empty value
// counts as unset. Otherwise it asks `docker compose ps -q`, using absolute
// paths derived from this source file so the working directory does not matter,
// and -p from COMPOSE_PROJECT_NAME when set. That fallback exists only for
// running `go test -tags integration` without make.
func resolveAPIContainer(run commandRunner) (string, error) {
	if value := os.Getenv("NOTIFS_API_CONTAINER"); value != "" {
		return value, nil
	}
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot locate repo root via runtime.Caller")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	composeArgs := []string{"compose"}
	if project := os.Getenv("COMPOSE_PROJECT_NAME"); project != "" {
		composeArgs = append(composeArgs, "-p", project)
	}
	composeArgs = append(composeArgs,
		"--project-directory", repoRoot,
		"-f", filepath.Join(repoRoot, "docker-compose.yml"),
		"ps", "-q", "provisioning-api",
	)
	output, runErr := run("docker", composeArgs...)
	if runErr != nil {
		return "", fmt.Errorf("docker compose ps -q provisioning-api: %w", runErr)
	}
	containerID := strings.TrimSpace(string(output))
	if containerID == "" {
		return "", fmt.Errorf("no provisioning-api container found; is the stack up (make local-up)?")
	}
	return containerID, nil
}
