package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/dispatch"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/httpapi"
)

// resultTimeout bounds how long a test waits on serve or a request before
// failing instead of hanging the suite.
const resultTimeout = 2 * time.Second

// newTestLogger returns a logger that discards everything, keeping test
// output clean.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestClient returns an HTTP client that never reuses connections, so a
// finished request cannot leave an idle keep-alive connection behind that
// would blur the shutdown assertions.
func newTestClient() *http.Client {
	return &http.Client{
		Timeout:   resultTimeout,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
}

// startServe runs serve in a goroutine and returns the channel its result is
// delivered on.
func startServe(
	ctx context.Context,
	listener net.Listener,
	handler http.Handler,
	timeout time.Duration,
) <-chan error {
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- serve(ctx, newTestLogger(), listener, handler, timeout)
	}()
	return serveResult
}

func TestRunRejectsUnknownCommand(testInstance *testing.T) {
	err := run(context.Background(), newTestLogger(), []string{"bogus"})
	if err == nil {
		testInstance.Fatal("run(bogus) returned nil, want an error")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		testInstance.Fatalf("run(bogus) error = %q, want it to contain %q", err, "unknown command")
	}
}

// TestRunFailsFastWithoutDBPassword verifies both the migrate and serve
// branches reject a missing DELIVERY_DB_PASSWORD, and that serve does so before
// binding its listen address.
func TestRunFailsFastWithoutDBPassword(testInstance *testing.T) {
	testCases := []struct {
		name string
		args []string
	}{
		{name: "migrate", args: []string{"migrate"}},
		{name: "serve", args: []string{}},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			subTest.Setenv("DELIVERY_DB_HOST", "delivery-postgres")
			subTest.Setenv("DELIVERY_DB_USER", "delivery")
			subTest.Setenv("DELIVERY_DB_NAME", "delivery")
			subTest.Setenv("DELIVERY_DB_PASSWORD", "")
			subTest.Setenv("DELIVERY_DB_PASSWORD_FILE", "")

			address := reserveFreeAddress(subTest)
			subTest.Setenv("LISTEN_ADDRESS", address)

			ctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
			defer cancel()
			err := run(ctx, newTestLogger(), testCase.args)

			if err == nil {
				subTest.Fatal("run returned nil, want an error")
			}
			if !strings.Contains(err.Error(), "DELIVERY_DB_PASSWORD") {
				subTest.Fatalf("run error = %q, want it to mention DELIVERY_DB_PASSWORD", err)
			}

			connection, dialErr := net.Dial("tcp", address)
			if dialErr == nil {
				_ = connection.Close()
				subTest.Fatalf("dial %s succeeded, want connection refused (run must not bind before config)", address)
			}
		})
	}
}

// setDatabaseEnv sets a complete DELIVERY_DB_* config pointing at a loopback
// port nothing listens on, so a connection attempt is refused immediately
// without any DNS lookup. The pool connects lazily, so no database is needed to
// get past openDatabase.
func setDatabaseEnv(testInstance *testing.T) {
	testInstance.Helper()
	databaseHost, databasePort, splitErr := net.SplitHostPort(reserveFreeAddress(testInstance))
	if splitErr != nil {
		testInstance.Fatalf("split reserved address: %v", splitErr)
	}
	testInstance.Setenv("DELIVERY_DB_HOST", databaseHost)
	testInstance.Setenv("DELIVERY_DB_PORT", databasePort)
	testInstance.Setenv("DELIVERY_DB_USER", "delivery")
	testInstance.Setenv("DELIVERY_DB_NAME", "delivery")
	testInstance.Setenv("DELIVERY_DB_PASSWORD", "test-password")
	testInstance.Setenv("DELIVERY_DB_PASSWORD_FILE", "")
}

// reserveFreeAddress returns a loopback address that was free a moment ago and
// is released again, so a Dial after run can only succeed if run wrongly bound
// it.
func reserveFreeAddress(testInstance *testing.T) string {
	testInstance.Helper()
	reserved, reserveErr := net.Listen("tcp", "127.0.0.1:0")
	if reserveErr != nil {
		testInstance.Fatalf("reserve port: %v", reserveErr)
	}
	address := reserved.Addr().String()
	if closeErr := reserved.Close(); closeErr != nil {
		testInstance.Fatalf("release port: %v", closeErr)
	}
	return address
}

// TestRunServeFailsFastWithoutMasterKey verifies serve rejects a missing or
// malformed DELIVERY_MASTER_KEY before binding its listen address, and that an
// invalid value never appears in the error.
func TestRunServeFailsFastWithoutMasterKey(testInstance *testing.T) {
	const invalidKey = "not-a-valid-key-zzzz"

	testCases := []struct {
		name        string
		keyValue    string
		wantMessage string
	}{
		{name: "missing", keyValue: "", wantMessage: "DELIVERY_MASTER_KEY is required"},
		{name: "invalid", keyValue: invalidKey, wantMessage: "must be 64 lowercase hex characters"},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			setDatabaseEnv(subTest)
			subTest.Setenv("DELIVERY_MASTER_KEY", testCase.keyValue)
			subTest.Setenv("DELIVERY_MASTER_KEY_FILE", "")
			address := reserveFreeAddress(subTest)
			subTest.Setenv("LISTEN_ADDRESS", address)

			ctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
			defer cancel()
			err := run(ctx, newTestLogger(), nil)

			if err == nil {
				subTest.Fatal("run returned nil, want an error")
			}
			if !strings.Contains(err.Error(), testCase.wantMessage) {
				subTest.Fatalf("run error = %q, want it to contain %q", err, testCase.wantMessage)
			}
			if strings.Contains(err.Error(), invalidKey) {
				subTest.Fatalf("run error = %q, must not contain the key value", err)
			}

			connection, dialErr := net.Dial("tcp", address)
			if dialErr == nil {
				_ = connection.Close()
				subTest.Fatalf("dial %s succeeded, want connection refused (run must not bind before the key check)", address)
			}
		})
	}
}

// TestRunServeFailsFastOnInvalidRetention verifies serve rejects an invalid
// DELIVERY_RETENTION_DAYS (with a valid database and master key) before binding
// its listen address, and that the error never echoes the value.
func TestRunServeFailsFastOnInvalidRetention(testInstance *testing.T) {
	testCases := []struct {
		name  string
		value string
	}{
		{name: "zero", value: "0"},
		{name: "negative", value: "-5"},
		{name: "not a number", value: "secret-looking-value"},
		{name: "above the maximum", value: "3651"},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			setDatabaseEnv(subTest)
			subTest.Setenv("DELIVERY_MASTER_KEY", strings.Repeat("ab", 32))
			subTest.Setenv("DELIVERY_MASTER_KEY_FILE", "")
			subTest.Setenv("DELIVERY_RETENTION_DAYS", testCase.value)
			address := reserveFreeAddress(subTest)
			subTest.Setenv("LISTEN_ADDRESS", address)

			ctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
			defer cancel()
			err := run(ctx, newTestLogger(), nil)

			if err == nil {
				subTest.Fatal("run returned nil, want an error")
			}
			if !strings.Contains(err.Error(), "DELIVERY_RETENTION_DAYS") {
				subTest.Fatalf("run error = %q, want it to mention DELIVERY_RETENTION_DAYS", err)
			}
			if strings.Contains(err.Error(), testCase.value) {
				subTest.Fatalf("run error = %q, must not contain the value", err)
			}

			connection, dialErr := net.Dial("tcp", address)
			if dialErr == nil {
				_ = connection.Close()
				subTest.Fatalf("dial %s succeeded, want connection refused (run must not bind before the retention check)", address)
			}
		})
	}
}

// TestRunServeFailsFastOnInvalidMaxScheduled verifies serve rejects an invalid
// DELIVERY_MAX_SCHEDULED_PER_APP or DELIVERY_MAX_SCHEDULED_TOTAL (with a valid
// database and master key) before binding its listen address, and that the error
// never echoes the value.
func TestRunServeFailsFastOnInvalidMaxScheduled(testInstance *testing.T) {
	testCases := []struct {
		name   string
		envVar string
		value  string
	}{
		{name: "per app zero", envVar: "DELIVERY_MAX_SCHEDULED_PER_APP", value: "0"},
		{name: "per app negative", envVar: "DELIVERY_MAX_SCHEDULED_PER_APP", value: "-5"},
		{name: "per app not a number", envVar: "DELIVERY_MAX_SCHEDULED_PER_APP", value: "secret-looking-value"},
		{name: "per app above the maximum", envVar: "DELIVERY_MAX_SCHEDULED_PER_APP", value: "100001"},
		{name: "total zero", envVar: "DELIVERY_MAX_SCHEDULED_TOTAL", value: "0"},
		{name: "total negative", envVar: "DELIVERY_MAX_SCHEDULED_TOTAL", value: "-5"},
		{name: "total not a number", envVar: "DELIVERY_MAX_SCHEDULED_TOTAL", value: "secret-looking-value"},
		{name: "total above the maximum", envVar: "DELIVERY_MAX_SCHEDULED_TOTAL", value: "10000001"},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			setDatabaseEnv(subTest)
			subTest.Setenv("DELIVERY_MASTER_KEY", strings.Repeat("ab", 32))
			subTest.Setenv("DELIVERY_MASTER_KEY_FILE", "")
			subTest.Setenv(testCase.envVar, testCase.value)
			address := reserveFreeAddress(subTest)
			subTest.Setenv("LISTEN_ADDRESS", address)

			ctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
			defer cancel()
			err := run(ctx, newTestLogger(), nil)

			if err == nil {
				subTest.Fatal("run returned nil, want an error")
			}
			if !strings.Contains(err.Error(), testCase.envVar) {
				subTest.Fatalf("run error = %q, want it to mention %s", err, testCase.envVar)
			}
			if strings.Contains(err.Error(), testCase.value) {
				subTest.Fatalf("run error = %q, must not contain the value", err)
			}

			connection, dialErr := net.Dial("tcp", address)
			if dialErr == nil {
				_ = connection.Close()
				subTest.Fatalf("dial %s succeeded, want connection refused (run must not bind before the cap check)", address)
			}
		})
	}
}

// TestShutdownBudgetCoversWorkerAndWaitingRequests pins the shutdown constants
// to the delivery worker's worst case: a SIGTERM must leave an in-flight attempt
// time to be recorded and a ?wait request time to finish.
func TestShutdownBudgetCoversWorkerAndWaitingRequests(testInstance *testing.T) {
	if want := dispatch.MaxInFlightShutdown + httpapi.MaxWait; shutdownTimeout < want {
		testInstance.Fatalf("shutdownTimeout = %s, want at least %s", shutdownTimeout, want)
	}
	if backgroundStopTimeout < dispatch.MaxInFlightShutdown {
		testInstance.Fatalf("backgroundStopTimeout = %s, want at least %s", backgroundStopTimeout, dispatch.MaxInFlightShutdown)
	}
}

// composeStopGracePeriod returns the stop_grace_period of the delivery-api
// service in a compose file, or -1 when it sets none (Docker then kills the
// container after 10 s). It scans lines rather than parsing YAML so the module
// keeps its standard-library-only dependency set.
func composeStopGracePeriod(testInstance *testing.T, composePath string) time.Duration {
	testInstance.Helper()
	contents, readErr := os.ReadFile(composePath)
	if readErr != nil {
		testInstance.Fatalf("read %s: %v", composePath, readErr)
	}
	servicePattern := regexp.MustCompile(`^  delivery-api:\s*$`)
	gracePattern := regexp.MustCompile(`^    stop_grace_period:\s*(\d+)s\s*$`)
	inService := false
	for _, line := range strings.Split(string(contents), "\n") {
		switch {
		case servicePattern.MatchString(line):
			inService = true
		case inService && line != "" && !strings.HasPrefix(line, "    "):
			// A line indented less than a service property ends the delivery-api block.
			return -1
		case inService:
			if match := gracePattern.FindStringSubmatch(line); match != nil {
				seconds, parseErr := strconv.Atoi(match[1])
				if parseErr != nil {
					testInstance.Fatalf("parse stop_grace_period %q: %v", match[1], parseErr)
				}
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return -1
}

// TestComposeStopGracePeriodCoversShutdown pins both compose files to the
// process's worst-case graceful shutdown: with Docker's default 10 s, a SIGTERM
// would be followed by SIGKILL while a delivery attempt is still being recorded.
func TestComposeStopGracePeriodCoversShutdown(testInstance *testing.T) {
	worstCase := shutdownTimeout + backgroundStopTimeout
	for _, composeFile := range []string{"docker-compose.yml", "docker-compose.prod.yml"} {
		testInstance.Run(composeFile, func(subTest *testing.T) {
			grace := composeStopGracePeriod(subTest, filepath.Join("..", composeFile))
			if grace < worstCase {
				subTest.Fatalf("delivery-api stop_grace_period = %s, want at least the worst-case shutdown %s", grace, worstCase)
			}
		})
	}
}

func TestBuildSinksAddsNtfyOnlyWhenConfigured(testInstance *testing.T) {
	guardedClient := http.DefaultClient

	configured, configuredTypes := buildSinks(guardedClient, "http://ntfy:80", newTestLogger())
	for _, destinationType := range []string{"webhook", "discord", "ntfy"} {
		if configured[destinationType] == nil {
			testInstance.Errorf("configured: no sink for %q", destinationType)
		}
		if !configuredTypes[destinationType] {
			testInstance.Errorf("configured: %q not accepted at submit", destinationType)
		}
	}

	var logOutput bytes.Buffer
	unconfigured, unconfiguredTypes := buildSinks(guardedClient, "", slog.New(slog.NewTextHandler(&logOutput, nil)))
	if unconfigured["ntfy"] != nil || unconfiguredTypes["ntfy"] {
		testInstance.Error("unconfigured: ntfy must be left out of both the sinks and the supported types")
	}
	for _, destinationType := range []string{"webhook", "discord"} {
		if unconfigured[destinationType] == nil || !unconfiguredTypes[destinationType] {
			testInstance.Errorf("unconfigured: %q missing", destinationType)
		}
	}
	if len(unconfigured) != len(unconfiguredTypes) {
		testInstance.Errorf("sinks (%d) and supported types (%d) diverged", len(unconfigured), len(unconfiguredTypes))
	}
	if !strings.Contains(logOutput.String(), "ntfy sink disabled: DELIVERY_NTFY_URL unset") {
		testInstance.Errorf("no warn line for the disabled ntfy sink, log = %q", logOutput.String())
	}
}

// TestComposeSetsDeliveryNtfyURL pins both compose files to the internal ntfy
// and provisioning-api origins, so a stack started from either enables the ntfy
// sink and publisher minting (an unset DELIVERY_PROVISIONING_URL silently
// disables minting, which makes every recipient submit fail).
func TestComposeSetsDeliveryNtfyURL(testInstance *testing.T) {
	wantLines := []string{
		"- DELIVERY_NTFY_URL=http://ntfy:80",
		"- DELIVERY_PROVISIONING_URL=http://provisioning-api:8080",
	}
	for _, composeFile := range []string{"docker-compose.yml", "docker-compose.prod.yml"} {
		contents, readErr := os.ReadFile(filepath.Join("..", composeFile))
		if readErr != nil {
			testInstance.Fatalf("read %s: %v", composeFile, readErr)
		}
		for _, wantLine := range wantLines {
			if !strings.Contains(string(contents), wantLine) {
				testInstance.Errorf("%s does not set %s", composeFile, strings.TrimPrefix(wantLine, "- "))
			}
		}
	}
}

// TestRunServeFailsFastOnInvalidOIDCConfig verifies serve refuses to start, before binding its
// listen address, on an OIDC configuration that could trust a key set production must not: a
// static JWKS file without the dev flag, an unrecognised dev flag, or an invalid audience; it
// also refuses an invalid pending-request TTL. The error names the variable and never echoes
// the value.
func TestRunServeFailsFastOnInvalidOIDCConfig(testInstance *testing.T) {
	testCases := []struct {
		name        string
		env         map[string]string
		wantMessage string
		secret      string
	}{
		{
			name:        "jwks file without the dev flag",
			env:         map[string]string{"DELIVERY_OIDC_JWKS_FILE": "/secret-looking/jwks.json"},
			wantMessage: "DELIVERY_OIDC_JWKS_FILE requires DELIVERY_OIDC_DEV_MODE=1",
			secret:      "/secret-looking/jwks.json",
		},
		{
			name:        "jwks file with the dev flag explicitly off",
			env:         map[string]string{"DELIVERY_OIDC_JWKS_FILE": "/secret-looking/jwks.json", "DELIVERY_OIDC_DEV_MODE": "0"},
			wantMessage: "DELIVERY_OIDC_JWKS_FILE requires DELIVERY_OIDC_DEV_MODE=1",
			secret:      "/secret-looking/jwks.json",
		},
		{
			name:        "unrecognised dev flag",
			env:         map[string]string{"DELIVERY_OIDC_DEV_MODE": "secret-looking-value"},
			wantMessage: "DELIVERY_OIDC_DEV_MODE",
			secret:      "secret-looking-value",
		},
		{
			name:        "invalid audience",
			env:         map[string]string{"DELIVERY_OIDC_AUDIENCE": "secret looking value"},
			wantMessage: "DELIVERY_OIDC_AUDIENCE",
			secret:      "secret looking value",
		},
		{
			name:        "invalid pending request ttl",
			env:         map[string]string{"DELIVERY_PENDING_IDENTITY_REQUEST_DAYS": "secret-looking-days"},
			wantMessage: "DELIVERY_PENDING_IDENTITY_REQUEST_DAYS",
			secret:      "secret-looking-days",
		},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			setDatabaseEnv(subTest)
			subTest.Setenv("DELIVERY_MASTER_KEY", strings.Repeat("ab", 32))
			subTest.Setenv("DELIVERY_MASTER_KEY_FILE", "")
			subTest.Setenv("DELIVERY_PENDING_IDENTITY_REQUEST_DAYS", "")
			subTest.Setenv("DELIVERY_OIDC_AUDIENCE", "")
			subTest.Setenv("DELIVERY_OIDC_JWKS_FILE", "")
			subTest.Setenv("DELIVERY_OIDC_DEV_MODE", "")
			for name, value := range testCase.env {
				subTest.Setenv(name, value)
			}
			address := reserveFreeAddress(subTest)
			subTest.Setenv("LISTEN_ADDRESS", address)

			ctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
			defer cancel()
			err := run(ctx, newTestLogger(), nil)

			if err == nil {
				subTest.Fatal("run returned nil, want an error")
			}
			if !strings.Contains(err.Error(), testCase.wantMessage) {
				subTest.Fatalf("run error = %q, want it to contain %q", err, testCase.wantMessage)
			}
			if strings.Contains(err.Error(), testCase.secret) {
				subTest.Fatalf("run error = %q, must not contain the value", err)
			}

			connection, dialErr := net.Dial("tcp", address)
			if dialErr == nil {
				_ = connection.Close()
				subTest.Fatalf("dial %s succeeded, want connection refused (run must not bind before the OIDC check)", address)
			}
		})
	}
}

// TestProdComposeSetsNeitherOIDCDevVariable pins the production stack to GitHub's real signing
// keys: the dev-only JWKS file and dev-mode variables must not appear in docker-compose.prod.yml
// at all (not even in a comment), so no edit can quietly turn them on there.
func TestProdComposeSetsNeitherOIDCDevVariable(testInstance *testing.T) {
	contents, readErr := os.ReadFile(filepath.Join("..", "docker-compose.prod.yml"))
	if readErr != nil {
		testInstance.Fatalf("read docker-compose.prod.yml: %v", readErr)
	}
	for _, variable := range []string{"DELIVERY_OIDC_JWKS_FILE", "DELIVERY_OIDC_DEV_MODE"} {
		if strings.Contains(string(contents), variable) {
			testInstance.Errorf("docker-compose.prod.yml mentions %s: the dev-only OIDC variables belong in docker-compose.yml only", variable)
		}
	}
	if strings.Contains(string(contents), "dev-oidc") {
		testInstance.Error("docker-compose.prod.yml mounts or references dev-oidc material")
	}
}

// TestLocalComposeWiresTheDevOIDCKeySet pins the local stack to the throwaway JWKS that
// scripts/dev-oidc-keys.mjs generates: the dev variables on delivery-api, and the file mounted
// read-only where DELIVERY_OIDC_JWKS_FILE points.
func TestLocalComposeWiresTheDevOIDCKeySet(testInstance *testing.T) {
	contents, readErr := os.ReadFile(filepath.Join("..", "docker-compose.yml"))
	if readErr != nil {
		testInstance.Fatalf("read docker-compose.yml: %v", readErr)
	}
	// Whole trimmed lines, so a commented-out line does not satisfy the check.
	lines := map[string]bool{}
	for _, line := range strings.Split(string(contents), "\n") {
		lines[strings.TrimSpace(line)] = true
	}
	for _, wantLine := range []string{
		"- DELIVERY_OIDC_DEV_MODE=1",
		"- DELIVERY_OIDC_JWKS_FILE=/run/dev-oidc/jwks.json",
		"source: ./.local/dev-oidc/jwks.json",
		"target: /run/dev-oidc/jwks.json",
		"read_only: true",
		// A missing source must fail the up, not become a directory.
		"create_host_path: false",
	} {
		if !lines[wantLine] {
			testInstance.Errorf("docker-compose.yml has no line %q", wantLine)
		}
	}
}

// TestBackgroundStopCancelsAndWaitsForEveryRunner verifies stop cancels the
// context every runner shares and returns only after all of them have exited.
func TestBackgroundStopCancelsAndWaitsForEveryRunner(testInstance *testing.T) {
	const runnerCount = 3
	var exited atomic.Int32
	started := make(chan struct{}, runnerCount)
	runner := func(runnerCtx context.Context) {
		started <- struct{}{}
		<-runnerCtx.Done()
		exited.Add(1)
	}

	background := startBackground(context.Background(), runner, runner, runner)
	for range runnerCount {
		select {
		case <-started:
		case <-time.After(resultTimeout):
			testInstance.Fatal("a runner never started")
		}
	}

	background.stop(newTestLogger(), resultTimeout)

	if got := exited.Load(); got != runnerCount {
		testInstance.Fatalf("runners exited = %d, want %d (stop must wait for all of them)", got, runnerCount)
	}
}

// TestBackgroundStopStopsWhenParentContextIsCanceled verifies a signal (the
// parent context) stops the runners without stop having to cancel them.
func TestBackgroundStopStopsWhenParentContextIsCanceled(testInstance *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	exited := make(chan struct{})
	background := startBackground(parent, func(runnerCtx context.Context) {
		<-runnerCtx.Done()
		close(exited)
	})

	cancelParent()

	select {
	case <-exited:
	case <-time.After(resultTimeout):
		testInstance.Fatal("runner did not observe the parent cancellation")
	}
	background.stop(newTestLogger(), resultTimeout)
}

// TestBackgroundStopGivesUpOnARunnerThatIgnoresCancellation verifies stop is
// bounded: a stuck runner is logged, not waited on forever.
func TestBackgroundStopGivesUpOnARunnerThatIgnoresCancellation(testInstance *testing.T) {
	release := make(chan struct{})
	testInstance.Cleanup(func() { close(release) })
	background := startBackground(context.Background(), func(context.Context) { <-release })

	var logOutput bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logOutput, nil))
	begin := time.Now()
	background.stop(logger, 50*time.Millisecond)

	if elapsed := time.Since(begin); elapsed > resultTimeout {
		testInstance.Fatalf("stop took %s, want it bounded by its timeout", elapsed)
	}
	if !strings.Contains(logOutput.String(), "background workers did not stop in time") {
		testInstance.Fatalf("log = %q, want the timeout message", logOutput.String())
	}
}

// TestRunServeReadsMasterKeyFromFile verifies serve accepts a valid key supplied
// through DELIVERY_MASTER_KEY_FILE: it gets past the key check and fails only on
// the already-occupied listen address.
func TestRunServeReadsMasterKeyFromFile(testInstance *testing.T) {
	setDatabaseEnv(testInstance)
	keyPath := filepath.Join(testInstance.TempDir(), "master-key")
	keyContents := strings.Repeat("ab", 32) + "\n"
	if writeErr := os.WriteFile(keyPath, []byte(keyContents), 0o600); writeErr != nil {
		testInstance.Fatalf("write key file: %v", writeErr)
	}
	testInstance.Setenv("DELIVERY_MASTER_KEY", "")
	testInstance.Setenv("DELIVERY_MASTER_KEY_FILE", keyPath)

	occupied, occupyErr := net.Listen("tcp", "127.0.0.1:0")
	if occupyErr != nil {
		testInstance.Fatalf("occupy port: %v", occupyErr)
	}
	defer func() { _ = occupied.Close() }()
	testInstance.Setenv("LISTEN_ADDRESS", occupied.Addr().String())

	ctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
	defer cancel()
	err := run(ctx, newTestLogger(), nil)

	if err == nil {
		testInstance.Fatal("run returned nil, want a listen error")
	}
	if strings.Contains(err.Error(), "DELIVERY_MASTER_KEY") {
		testInstance.Fatalf("run error = %q, want the key file accepted", err)
	}
	if !strings.Contains(err.Error(), "listen on") {
		testInstance.Fatalf("run error = %q, want it to contain %q", err, "listen on")
	}
}

// TestRunMigrateDoesNotNeedMasterKey verifies the migrate subcommand never
// consults DELIVERY_MASTER_KEY: with the key unset it still fails, but only on
// database connectivity.
func TestRunMigrateDoesNotNeedMasterKey(testInstance *testing.T) {
	setDatabaseEnv(testInstance)
	testInstance.Setenv("DELIVERY_MASTER_KEY", "")
	testInstance.Setenv("DELIVERY_MASTER_KEY_FILE", "")

	ctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
	defer cancel()
	err := run(ctx, newTestLogger(), []string{"migrate"})

	if err == nil {
		testInstance.Fatal("run(migrate) returned nil against an unreachable database, want an error")
	}
	// The error must come from the database step (the first thing migrate.Run
	// does), proving migrate got past config loading without the key.
	if !strings.Contains(err.Error(), "acquire migration lock") {
		testInstance.Fatalf("run(migrate) error = %q, want a database connectivity error (acquire migration lock)", err)
	}
	if strings.Contains(err.Error(), "DELIVERY_MASTER_KEY") {
		testInstance.Fatalf("run(migrate) error = %q, must not mention DELIVERY_MASTER_KEY", err)
	}
}

func TestServeShutsDownOnContextCancel(testInstance *testing.T) {
	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		testInstance.Fatalf("listen: %v", listenErr)
	}
	handler := httpapi.NewServer(httpapi.ServerConfig{}).Handler()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveResult := startServe(ctx, listener, handler, shutdownTimeout)

	// A synchronous request before cancel proves the server is serving, so
	// shutdown is sequenced strictly after the server is up (no race).
	response, getErr := newTestClient().Get("http://" + listener.Addr().String() + "/healthz")
	if getErr != nil {
		testInstance.Fatalf("GET /healthz before shutdown: %v", getErr)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		testInstance.Fatalf("GET /healthz status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	cancel()

	select {
	case serveErr := <-serveResult:
		if serveErr != nil {
			testInstance.Fatalf("serve returned %v after context cancel, want nil", serveErr)
		}
	case <-time.After(2 * shutdownTimeout):
		testInstance.Fatal("serve did not return after context cancel")
	}

	connection, dialErr := net.Dial("tcp", listener.Addr().String())
	if dialErr == nil {
		_ = connection.Close()
		testInstance.Fatal("dial succeeded after shutdown, want the listener closed")
	}
}

func TestServeReturnsErrorWhenShutdownTimesOut(testInstance *testing.T) {
	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		testInstance.Fatalf("listen: %v", listenErr)
	}

	handlerEntered := make(chan struct{})
	releaseHandler := make(chan struct{})
	testInstance.Cleanup(func() { close(releaseHandler) })
	handler := http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		close(handlerEntered)
		<-releaseHandler
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveResult := startServe(ctx, listener, handler, 50*time.Millisecond)

	// Keep one request in flight so Shutdown cannot drain before its deadline.
	go func() {
		response, getErr := newTestClient().Get("http://" + listener.Addr().String() + "/")
		if getErr == nil {
			_ = response.Body.Close()
		}
	}()

	select {
	case <-handlerEntered:
	case <-time.After(resultTimeout):
		testInstance.Fatal("handler was never entered; the request is not in flight")
	}

	cancel()

	select {
	case serveErr := <-serveResult:
		if !errors.Is(serveErr, context.DeadlineExceeded) {
			testInstance.Fatalf("serve error = %v, want one wrapping context.DeadlineExceeded", serveErr)
		}
	case <-time.After(resultTimeout):
		testInstance.Fatal("serve did not return after the shutdown deadline")
	}
}

func TestServeReturnsListenerError(testInstance *testing.T) {
	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		testInstance.Fatalf("listen: %v", listenErr)
	}
	if closeErr := listener.Close(); closeErr != nil {
		testInstance.Fatalf("close listener: %v", closeErr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := httpapi.NewServer(httpapi.ServerConfig{}).Handler()
	serveResult := startServe(ctx, listener, handler, shutdownTimeout)

	select {
	case serveErr := <-serveResult:
		if serveErr == nil {
			testInstance.Fatal("serve returned nil for a closed listener, want an error")
		}
		if errors.Is(serveErr, http.ErrServerClosed) {
			testInstance.Fatalf("serve error = %v, want a listener error, not ErrServerClosed", serveErr)
		}
	case <-time.After(resultTimeout):
		testInstance.Fatal("serve did not return for a closed listener")
	}
}
