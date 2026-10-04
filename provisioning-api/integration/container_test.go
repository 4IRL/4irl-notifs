package integration

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type recordedCall struct {
	name string
	args []string
}

func stubRunner(output string, runErr error, calls *[]recordedCall) commandRunner {
	return func(name string, args ...string) ([]byte, error) {
		*calls = append(*calls, recordedCall{name: name, args: args})
		return []byte(output), runErr
	}
}

func TestResolveAPIContainerEnvWins(t *testing.T) {
	t.Setenv("NOTIFS_API_CONTAINER", "abc123")
	var calls []recordedCall

	containerID, resolveErr := resolveAPIContainer(stubRunner("ignored\n", nil, &calls))

	if resolveErr != nil {
		t.Fatalf("unexpected error: %v", resolveErr)
	}
	if containerID != "abc123" {
		t.Fatalf("container = %q, want abc123", containerID)
	}
	if len(calls) != 0 {
		t.Fatalf("runner called %d times, want 0", len(calls))
	}
}

func TestResolveAPIContainerEmptyEnvFallsBackToRunner(t *testing.T) {
	t.Setenv("NOTIFS_API_CONTAINER", "")
	t.Setenv("COMPOSE_PROJECT_NAME", "")
	var calls []recordedCall

	containerID, resolveErr := resolveAPIContainer(stubRunner("deadbeef\n", nil, &calls))

	if resolveErr != nil {
		t.Fatalf("unexpected error: %v", resolveErr)
	}
	if containerID != "deadbeef" {
		t.Fatalf("container = %q, want deadbeef", containerID)
	}
	if len(calls) != 1 {
		t.Fatalf("runner called %d times, want 1", len(calls))
	}
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	absPath, absErr := filepath.Abs(filepath.Join("..", ".."))
	if absErr != nil {
		t.Fatalf("abs: %v", absErr)
	}
	return absPath
}

func TestResolveAPIContainerFallbackArgvWithProject(t *testing.T) {
	t.Setenv("NOTIFS_API_CONTAINER", "")
	t.Setenv("COMPOSE_PROJECT_NAME", "4irl-notifs-proof")
	var calls []recordedCall

	containerID, resolveErr := resolveAPIContainer(stubRunner("  cid42  \n", nil, &calls))

	if resolveErr != nil {
		t.Fatalf("unexpected error: %v", resolveErr)
	}
	if containerID != "cid42" {
		t.Fatalf("container = %q, want cid42", containerID)
	}
	root := repoRootForTest(t)
	composeFile := filepath.Join(root, "docker-compose.yml")
	want := []string{
		"compose", "-p", "4irl-notifs-proof",
		"--project-directory", root, "-f", composeFile,
		"ps", "-q", "provisioning-api",
	}
	if len(calls) != 1 || calls[0].name != "docker" {
		t.Fatalf("calls = %+v, want one docker call", calls)
	}
	if !reflect.DeepEqual(calls[0].args, want) {
		t.Fatalf("argv = %v, want %v", calls[0].args, want)
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(composeFile) {
		t.Fatalf("paths must be absolute: %s %s", root, composeFile)
	}
	if _, statErr := os.Stat(composeFile); statErr != nil {
		t.Fatalf("compose file must exist: %v", statErr)
	}
}

func TestResolveAPIContainerFallbackArgvWithoutProject(t *testing.T) {
	t.Setenv("NOTIFS_API_CONTAINER", "")
	t.Setenv("COMPOSE_PROJECT_NAME", "")
	var calls []recordedCall

	if _, resolveErr := resolveAPIContainer(stubRunner("cid\n", nil, &calls)); resolveErr != nil {
		t.Fatalf("unexpected error: %v", resolveErr)
	}
	root := repoRootForTest(t)
	want := []string{
		"compose",
		"--project-directory", root, "-f", filepath.Join(root, "docker-compose.yml"),
		"ps", "-q", "provisioning-api",
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].args, want) {
		t.Fatalf("calls = %+v, want args %v", calls, want)
	}
}

func TestResolveAPIContainerEmptyOutputErrors(t *testing.T) {
	t.Setenv("NOTIFS_API_CONTAINER", "")
	t.Setenv("COMPOSE_PROJECT_NAME", "")
	var calls []recordedCall

	if _, resolveErr := resolveAPIContainer(stubRunner("  \n", nil, &calls)); resolveErr == nil {
		t.Fatal("expected error for empty runner output")
	}
}

func TestResolveAPIContainerRunnerErrorPropagates(t *testing.T) {
	t.Setenv("NOTIFS_API_CONTAINER", "")
	t.Setenv("COMPOSE_PROJECT_NAME", "")
	var calls []recordedCall

	if _, resolveErr := resolveAPIContainer(stubRunner("", errors.New("boom"), &calls)); resolveErr == nil {
		t.Fatal("expected runner error to propagate")
	}
}
