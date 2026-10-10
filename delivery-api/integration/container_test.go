package integration

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	apiContainerEnv = "NOTIFS_DELIVERY_CONTAINER"
	dbContainerEnv  = "NOTIFS_DELIVERY_DB_CONTAINER"
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

func repoRootForTest(testInstance *testing.T) string {
	testInstance.Helper()
	absPath, absErr := filepath.Abs(filepath.Join("..", ".."))
	if absErr != nil {
		testInstance.Fatalf("abs: %v", absErr)
	}
	return absPath
}

func TestResolveServiceContainerEnvWins(testInstance *testing.T) {
	testInstance.Setenv(apiContainerEnv, "abc123")
	var calls []recordedCall

	containerID, resolveErr := resolveServiceContainer(
		stubRunner("ignored\n", nil, &calls), apiContainerEnv, "delivery-api",
	)

	if resolveErr != nil {
		testInstance.Fatalf("unexpected error: %v", resolveErr)
	}
	if containerID != "abc123" {
		testInstance.Fatalf("container = %q, want abc123", containerID)
	}
	if len(calls) != 0 {
		testInstance.Fatalf("runner called %d times, want 0", len(calls))
	}
}

func TestResolveServiceContainerEmptyEnvFallsBackToRunner(testInstance *testing.T) {
	testInstance.Setenv(apiContainerEnv, "")
	testInstance.Setenv("COMPOSE_PROJECT_NAME", "")
	var calls []recordedCall

	containerID, resolveErr := resolveServiceContainer(
		stubRunner("deadbeef\n", nil, &calls), apiContainerEnv, "delivery-api",
	)

	if resolveErr != nil {
		testInstance.Fatalf("unexpected error: %v", resolveErr)
	}
	if containerID != "deadbeef" {
		testInstance.Fatalf("container = %q, want deadbeef", containerID)
	}
	if len(calls) != 1 {
		testInstance.Fatalf("runner called %d times, want 1", len(calls))
	}
}

func TestResolveServiceContainerFallbackArgvWithProject(testInstance *testing.T) {
	testInstance.Setenv(apiContainerEnv, "")
	testInstance.Setenv("COMPOSE_PROJECT_NAME", "4irl-notifs-proof")
	var calls []recordedCall

	containerID, resolveErr := resolveServiceContainer(
		stubRunner("  cid42  \n", nil, &calls), apiContainerEnv, "delivery-api",
	)

	if resolveErr != nil {
		testInstance.Fatalf("unexpected error: %v", resolveErr)
	}
	if containerID != "cid42" {
		testInstance.Fatalf("container = %q, want cid42", containerID)
	}
	root := repoRootForTest(testInstance)
	composeFile := filepath.Join(root, "docker-compose.yml")
	want := []string{
		"compose", "-p", "4irl-notifs-proof",
		"--project-directory", root, "-f", composeFile,
		"ps", "-q", "delivery-api",
	}
	if len(calls) != 1 || calls[0].name != "docker" {
		testInstance.Fatalf("calls = %+v, want one docker call", calls)
	}
	if !reflect.DeepEqual(calls[0].args, want) {
		testInstance.Fatalf("argv = %v, want %v", calls[0].args, want)
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(composeFile) {
		testInstance.Fatalf("paths must be absolute: %s %s", root, composeFile)
	}
	if _, statErr := os.Stat(composeFile); statErr != nil {
		testInstance.Fatalf("compose file must exist: %v", statErr)
	}
}

func TestResolveServiceContainerFallbackArgvWithoutProject(testInstance *testing.T) {
	testInstance.Setenv(apiContainerEnv, "")
	testInstance.Setenv("COMPOSE_PROJECT_NAME", "")
	var calls []recordedCall

	if _, resolveErr := resolveServiceContainer(
		stubRunner("cid\n", nil, &calls), apiContainerEnv, "delivery-api",
	); resolveErr != nil {
		testInstance.Fatalf("unexpected error: %v", resolveErr)
	}
	root := repoRootForTest(testInstance)
	want := []string{
		"compose",
		"--project-directory", root, "-f", filepath.Join(root, "docker-compose.yml"),
		"ps", "-q", "delivery-api",
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].args, want) {
		testInstance.Fatalf("calls = %+v, want args %v", calls, want)
	}
}

func TestResolveServiceContainerDatabaseService(testInstance *testing.T) {
	testInstance.Setenv(dbContainerEnv, "")
	testInstance.Setenv("COMPOSE_PROJECT_NAME", "")
	var calls []recordedCall

	containerID, resolveErr := resolveServiceContainer(
		stubRunner("dbcid\n", nil, &calls), dbContainerEnv, "delivery-postgres",
	)

	if resolveErr != nil {
		testInstance.Fatalf("unexpected error: %v", resolveErr)
	}
	if containerID != "dbcid" {
		testInstance.Fatalf("container = %q, want dbcid", containerID)
	}
	if len(calls) != 1 {
		testInstance.Fatalf("runner called %d times, want 1", len(calls))
	}
	args := calls[0].args
	if got := strings.Join(args[len(args)-3:], " "); got != "ps -q delivery-postgres" {
		testInstance.Fatalf("argv tail = %q, want %q", got, "ps -q delivery-postgres")
	}

	testInstance.Setenv(dbContainerEnv, "envdb")
	envID, envErr := resolveServiceContainer(
		stubRunner("ignored\n", nil, &calls), dbContainerEnv, "delivery-postgres",
	)
	if envErr != nil || envID != "envdb" {
		testInstance.Fatalf("env override = %q, %v; want envdb, nil", envID, envErr)
	}
	if len(calls) != 1 {
		testInstance.Fatalf("runner called %d times after env override, want still 1", len(calls))
	}
}

func TestResolveServiceContainerEmptyOutputErrors(testInstance *testing.T) {
	testInstance.Setenv(apiContainerEnv, "")
	testInstance.Setenv("COMPOSE_PROJECT_NAME", "")
	var calls []recordedCall

	_, resolveErr := resolveServiceContainer(
		stubRunner("  \n", nil, &calls), apiContainerEnv, "delivery-api",
	)

	if resolveErr == nil {
		testInstance.Fatal("expected error for empty runner output")
	}
	if !strings.Contains(resolveErr.Error(), "no delivery-api container found") {
		testInstance.Fatalf("error = %q, want it to name the service", resolveErr)
	}
}

func TestResolveServiceContainerMultipleContainersErrors(testInstance *testing.T) {
	testInstance.Setenv(apiContainerEnv, "")
	testInstance.Setenv("COMPOSE_PROJECT_NAME", "")
	var calls []recordedCall

	_, resolveErr := resolveServiceContainer(
		stubRunner("cid1\ncid2\n", nil, &calls), apiContainerEnv, "delivery-api",
	)

	if resolveErr == nil {
		testInstance.Fatal("expected error when several containers match")
	}
	if !strings.Contains(resolveErr.Error(), "several") {
		testInstance.Fatalf("error = %q, want it to say several containers matched", resolveErr)
	}
}

func TestResolveServiceContainerRunnerErrorPropagates(testInstance *testing.T) {
	testInstance.Setenv(apiContainerEnv, "")
	testInstance.Setenv("COMPOSE_PROJECT_NAME", "")
	var calls []recordedCall
	runnerErr := errors.New("boom")

	_, resolveErr := resolveServiceContainer(
		stubRunner("", runnerErr, &calls), apiContainerEnv, "delivery-api",
	)

	if !errors.Is(resolveErr, runnerErr) {
		testInstance.Fatalf("error = %v, want it to wrap %v", resolveErr, runnerErr)
	}
}
