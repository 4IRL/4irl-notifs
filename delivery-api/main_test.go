package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/dispatch"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
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

// TestProberShutdownFitsBackgroundStopTimeout pins the prober's worst-case
// shutdown (one probe plus its result write) inside the background stop budget.
// The pin lives here because heartbeat must not import dispatch.
func TestProberShutdownFitsBackgroundStopTimeout(testInstance *testing.T) {
	if heartbeat.MaxProberShutdown > backgroundStopTimeout {
		testInstance.Fatalf("heartbeat.MaxProberShutdown = %s, want at most backgroundStopTimeout %s", heartbeat.MaxProberShutdown, backgroundStopTimeout)
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

func TestBuildMinterNeedsBothOriginAndCredential(testInstance *testing.T) {
	const fakeCredential = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const resolverErrorText = "read secret file for DELIVERY_PROVISIONING_CREDENTIAL: no such file"
	const provisioningOrigin = "http://provisioning-api:8080"
	testCases := []struct {
		name         string
		url          string
		value        string
		resolveErr   error
		wantMinter   bool
		wantWarn     string // the variable the single WARN must name; empty means no log output
		wantWarnText string // extra text the WARN must carry
	}{
		{name: "both set", url: provisioningOrigin, value: fakeCredential, wantMinter: true},
		{name: "padded credential is trimmed", url: provisioningOrigin, value: " " + fakeCredential + "\n", wantMinter: true},
		{name: "credential empty", url: provisioningOrigin, value: "", wantWarn: provisioningCredentialEnv, wantWarnText: "unset or empty"},
		{name: "credential whitespace only", url: provisioningOrigin, value: " \n", wantWarn: provisioningCredentialEnv, wantWarnText: "unset or empty"},
		{
			name: "credential unreadable", url: provisioningOrigin, resolveErr: errors.New(resolverErrorText),
			wantWarn: provisioningCredentialEnv, wantWarnText: resolverErrorText,
		},
		{name: "url unset", url: "", value: fakeCredential, wantWarn: provisioningURLEnv, wantWarnText: "unset"},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			resolver := func(key string) (string, error) {
				if key != provisioningCredentialEnv {
					subTest.Errorf("resolved secret %q, want %q", key, provisioningCredentialEnv)
				}
				return testCase.value, testCase.resolveErr
			}
			var logOutput bytes.Buffer
			minter, minterErr := buildMinter(testCase.url, resolver, slog.New(slog.NewTextHandler(&logOutput, nil)))

			if minterErr != nil {
				subTest.Fatalf("unexpected error: %v", minterErr)
			}
			if (minter != nil) != testCase.wantMinter {
				subTest.Fatalf("minter non-nil = %v, want %v", minter != nil, testCase.wantMinter)
			}
			if testCase.wantWarn == "" {
				if logOutput.Len() != 0 {
					subTest.Errorf("unexpected log output %q", logOutput.String())
				}
			} else if !strings.Contains(logOutput.String(), testCase.wantWarn) ||
				!strings.Contains(logOutput.String(), testCase.wantWarnText) ||
				strings.Count(logOutput.String(), "level=WARN") != 1 {
				subTest.Errorf("want exactly one WARN naming %s with %q, log = %q",
					testCase.wantWarn, testCase.wantWarnText, logOutput.String())
			}
			if strings.Contains(logOutput.String(), fakeCredential) {
				subTest.Error("log output leaks the credential")
			}
		})
	}
}

func TestBuildProvisionClientIsNilWhenDisabled(testInstance *testing.T) {
	const fakeCredential = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	resolver := func(string) (string, error) { return fakeCredential, nil }
	var logOutput bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logOutput, nil))

	disabled, disabledErr := buildProvisionClient("", resolver, logger)
	if disabledErr != nil || disabled != nil {
		testInstance.Fatalf("disabled client = %v, err = %v; want a nil *Client and no error", disabled, disabledErr)
	}
	enabled, enabledErr := buildProvisionClient("http://provisioning-api:8080", resolver, logger)
	if enabledErr != nil || enabled == nil {
		testInstance.Fatalf("enabled client = %v, err = %v; want a client", enabled, enabledErr)
	}
}

func TestBuildRegistrar(testInstance *testing.T) {
	const fakeSecret = "fake-access-secret-value"
	const resolverErrorText = "read secret file for PERSON_SERVICE_ACCESS_CLIENT_SECRET: no such file"
	resolverFor := func(values map[string]string, failName string) func(string) (string, error) {
		return func(name string) (string, error) {
			if name == failName {
				return "", errors.New(resolverErrorText)
			}
			return values[name], nil
		}
	}
	testCases := []struct {
		name       string
		url        string
		values     map[string]string
		failName   string
		wantNil    bool
		wantWarn   string // text the WARN must carry; empty means no log output
		wantNoText string // text the log must not carry
	}{
		{name: "url empty", url: "", wantNil: true, wantWarn: personServiceURLEnv},
		{name: "url blank", url: "  \n", wantNil: true, wantWarn: personServiceURLEnv},
		{name: "http without credentials", url: "http://person-service:8787"},
		{
			name: "https with empty secret", url: "https://people.example.test",
			values:   map[string]string{personServiceAccessIDEnv: "id-1"},
			wantWarn: personServiceAccessSecretEnv, wantNoText: fakeSecret,
		},
		{
			name: "resolver error", url: "https://people.example.test",
			values:   map[string]string{personServiceAccessIDEnv: "id-1", personServiceAccessSecretEnv: fakeSecret},
			failName: personServiceAccessSecretEnv, wantWarn: resolverErrorText, wantNoText: fakeSecret,
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			var logOutput bytes.Buffer
			registrar := buildRegistrar(testCase.url, resolverFor(testCase.values, testCase.failName),
				slog.New(slog.NewTextHandler(&logOutput, nil)))

			if (registrar == nil) != testCase.wantNil {
				subTest.Fatalf("registrar nil = %v, want %v", registrar == nil, testCase.wantNil)
			}
			if testCase.wantWarn == "" {
				if logOutput.Len() != 0 {
					subTest.Errorf("unexpected log output %q", logOutput.String())
				}
			} else if !strings.Contains(logOutput.String(), testCase.wantWarn) || !strings.Contains(logOutput.String(), "level=WARN") {
				subTest.Errorf("want a WARN carrying %q, log = %q", testCase.wantWarn, logOutput.String())
			}
			if testCase.wantNoText != "" && strings.Contains(logOutput.String(), testCase.wantNoText) {
				subTest.Errorf("log leaks %q: %q", testCase.wantNoText, logOutput.String())
			}
		})
	}
}

func TestBuildRegistrarTrimsCredentialsAndSendsThemAsHeaders(testInstance *testing.T) {
	headers := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		headers <- request.Header.Clone()
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	resolver := func(name string) (string, error) {
		return map[string]string{
			personServiceAccessIDEnv:     " id-1\n",
			personServiceAccessSecretEnv: "\tsecret-1 \n",
		}[name], nil
	}

	registrar := buildRegistrar(" "+server.URL+" ", resolver, newTestLogger())
	if registrar == nil {
		testInstance.Fatal("registrar is nil, want a client for the trimmed URL")
	}
	if createErr := registrar.CreateApp(context.Background(), "app-1", "app-1"); createErr != nil {
		testInstance.Fatalf("CreateApp: %v", createErr)
	}
	got := <-headers
	if got.Get("CF-Access-Client-Id") != "id-1" || got.Get("CF-Access-Client-Secret") != "secret-1" {
		testInstance.Errorf("access headers = %q / %q, want the trimmed values",
			got.Get("CF-Access-Client-Id"), got.Get("CF-Access-Client-Secret"))
	}
}

func TestBuildMinterRejectsNonInternalProvisioningHosts(testInstance *testing.T) {
	const fakeCredential = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testCases := []struct {
		name       string
		url        string
		wantAccept bool
	}{
		{"compose service name", "http://provisioning-api:8080", true},
		{"compose service name without port", "http://provisioning-api", true},
		{"compose service name with trailing slash", "http://provisioning-api:8080/", true},
		{"compose service name over https", "https://provisioning-api:8080", true},
		{"external host", "http://provisioning.example.com:8080", false},
		{"localhost", "http://localhost:8080", false},
		{"ip address", "http://10.0.0.5:8080", false},
		{"look-alike prefix", "http://provisioning-api.evil.example:8080", false},
		{"look-alike suffix", "http://evil-provisioning-api:8080", false},
		{"userinfo smuggling the real host", "http://provisioning-api@evil.example:8080", false},
		{"userinfo with the real host after it", "http://user:pass@provisioning-api:8080", false},
		{"unsupported scheme", "ftp://provisioning-api:8080", false},
		{"no scheme", "provisioning-api:8080", false},
		{"not a url", "://", false},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			resolver := func(string) (string, error) { return fakeCredential, nil }
			var logOutput bytes.Buffer

			minter, minterErr := buildMinter(testCase.url, resolver, slog.New(slog.NewTextHandler(&logOutput, nil)))

			if testCase.wantAccept {
				if minterErr != nil || minter == nil {
					subTest.Fatalf("minter = %v, err = %v, want an accepted origin", minter, minterErr)
				}
				return
			}
			if minterErr == nil || minter != nil {
				subTest.Fatalf("minter = %v, err = %v, want a startup error and no minter", minter, minterErr)
			}
			if strings.Contains(minterErr.Error(), fakeCredential) || strings.Contains(minterErr.Error(), "pass") {
				subTest.Errorf("error leaks credential or URL userinfo: %v", minterErr)
			}
			if strings.Contains(logOutput.String(), fakeCredential) {
				subTest.Error("log output leaks the credential")
			}
		})
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

// internalCredentialPattern is the shape the deploy validates for the internal credential.
var internalCredentialPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// devInternalCredential is the obviously-fake, public credential the local compose file carries.
// Pinning the exact value means a real credential pasted there fails the test.
const devInternalCredential = "fedcba98765432100123456789abcdeffedcba98765432100123456789abcdef"

// composeServiceLines returns the non-comment lines of one service block in a compose file, each
// trimmed with any trailing " #" comment removed, so a commented-out line never satisfies a check.
func composeServiceLines(testInstance *testing.T, composeFile string, service string) []string {
	testInstance.Helper()
	contents, readErr := os.ReadFile(filepath.Join("..", composeFile))
	if readErr != nil {
		testInstance.Fatalf("read %s: %v", composeFile, readErr)
	}
	serviceHeader := "  " + service + ":"
	nextEntry := regexp.MustCompile(`^(  [A-Za-z0-9_-]+:|[A-Za-z0-9_-]+:)`)
	var serviceLines []string
	inService := false
	for _, line := range strings.Split(string(contents), "\n") {
		if strings.TrimRight(line, " ") == serviceHeader {
			inService = true
			continue
		}
		if inService && nextEntry.MatchString(line) {
			break
		}
		if !inService {
			continue
		}
		if commentStart := strings.Index(line, " #"); commentStart >= 0 {
			line = line[:commentStart]
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		serviceLines = append(serviceLines, trimmed)
	}
	if !inService {
		testInstance.Fatalf("%s has no service %q", composeFile, service)
	}
	return serviceLines
}

// composeEnvValue returns the value of "- NAME=value" among the lines, and whether it is present.
func composeEnvValue(lines []string, name string) (string, bool) {
	prefix := "- " + name + "="
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix), true
		}
	}
	return "", false
}

// composeListsSecret reports whether the lines hold "- secretName" under the service's `secrets:` key.
func composeListsSecret(lines []string, secretName string) bool {
	inSecrets := false
	for _, line := range lines {
		if line == "secrets:" {
			inSecrets = true
			continue
		}
		if !inSecrets {
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			return false
		}
		if line == "- "+secretName {
			return true
		}
	}
	return false
}

// TestLocalComposeWiresDeliveryAPIToPersonService pins the local wiring that operator approval needs:
// approval registers the app in person-service (blocking), so delivery-api must know its origin and
// wait for it to be healthy, or every integration test that approves a binding fails.
func TestLocalComposeWiresDeliveryAPIToPersonService(testInstance *testing.T) {
	deliveryLines := composeServiceLines(testInstance, "docker-compose.yml", "delivery-api")

	gotURL, present := composeEnvValue(deliveryLines, "PERSON_SERVICE_URL")
	if !present || gotURL != "http://person-service:8787" {
		testInstance.Errorf("docker-compose.yml delivery-api: PERSON_SERVICE_URL = %q (present=%v), want http://person-service:8787", gotURL, present)
	}
	// Lines are trimmed, so track having passed the depends_on: key; the entry must come after it.
	underDependsOn := false
	dependsOnPersonServiceHealthy := false
	for index, line := range deliveryLines {
		if line == "depends_on:" {
			underDependsOn = true
		}
		if underDependsOn && line == "person-service:" && index+1 < len(deliveryLines) &&
			deliveryLines[index+1] == "condition: service_healthy" {
			dependsOnPersonServiceHealthy = true
			break
		}
	}
	if !dependsOnPersonServiceHealthy {
		testInstance.Error("docker-compose.yml delivery-api must depend_on person-service with condition: service_healthy")
	}
}

// TestLocalComposeWiresTheInternalCredentialAndModes pins the local stack: provisioning-api and
// delivery-api carry the same obviously-fake dev credential, the credential check is log-only and
// the grant check is enforce (so the integration suite exercises the real grant check).
func TestLocalComposeWiresTheInternalCredentialAndModes(testInstance *testing.T) {
	provisioningLines := composeServiceLines(testInstance, "docker-compose.yml", "provisioning-api")
	deliveryLines := composeServiceLines(testInstance, "docker-compose.yml", "delivery-api")

	provisioningCredential, provisioningHasCredential := composeEnvValue(provisioningLines, "PROVISIONING_INTERNAL_CREDENTIAL")
	if !provisioningHasCredential {
		testInstance.Fatal("docker-compose.yml provisioning-api does not set PROVISIONING_INTERNAL_CREDENTIAL")
	}
	deliveryCredential, deliveryHasCredential := composeEnvValue(deliveryLines, "DELIVERY_PROVISIONING_CREDENTIAL")
	if !deliveryHasCredential {
		testInstance.Fatal("docker-compose.yml delivery-api does not set DELIVERY_PROVISIONING_CREDENTIAL")
	}
	if provisioningCredential != deliveryCredential {
		testInstance.Error("docker-compose.yml: the two services must carry the same dev credential value")
	}
	if !internalCredentialPattern.MatchString(provisioningCredential) {
		testInstance.Error("docker-compose.yml: the dev credential must be 64 lowercase hex characters")
	}
	if provisioningCredential != devInternalCredential {
		testInstance.Error("docker-compose.yml: the dev credential must be the public fake value; never put a real credential in the local compose file")
	}

	wantModes := map[string]string{
		"PROVISIONING_INTERNAL_CREDENTIAL_MODE": "log-only",
		"PROVISIONING_APP_GRANT_MODE":           "enforce",
	}
	for name, wantValue := range wantModes {
		gotValue, present := composeEnvValue(provisioningLines, name)
		if !present || gotValue != wantValue {
			testInstance.Errorf("docker-compose.yml provisioning-api: %s = %q (present=%v), want %q", name, gotValue, present, wantValue)
		}
	}
}

// TestProdComposeMountsTheInternalCredentialSecret pins production: both services read the one
// persistent file secret, the credential check is log-only until the admin UI is re-homed while the
// grant check enforces, and no credential value is written into the compose file.
func TestProdComposeMountsTheInternalCredentialSecret(testInstance *testing.T) {
	const secretPath = "/run/secrets/PROVISIONING_INTERNAL_CREDENTIAL"
	provisioningLines := composeServiceLines(testInstance, "docker-compose.prod.yml", "provisioning-api")
	deliveryLines := composeServiceLines(testInstance, "docker-compose.prod.yml", "delivery-api")

	credentialServices := []struct {
		service  string
		lines    []string
		fileVar  string
		plainVar string
	}{
		{"provisioning-api", provisioningLines, "PROVISIONING_INTERNAL_CREDENTIAL_FILE", "PROVISIONING_INTERNAL_CREDENTIAL"},
		{"delivery-api", deliveryLines, "DELIVERY_PROVISIONING_CREDENTIAL_FILE", "DELIVERY_PROVISIONING_CREDENTIAL"},
	}
	for _, credentialService := range credentialServices {
		gotValue, present := composeEnvValue(credentialService.lines, credentialService.fileVar)
		if !present || gotValue != secretPath {
			testInstance.Errorf("docker-compose.prod.yml %s: %s = %q (present=%v), want %q", credentialService.service, credentialService.fileVar, gotValue, present, secretPath)
		}
		if !composeListsSecret(credentialService.lines, "PROVISIONING_INTERNAL_CREDENTIAL") {
			testInstance.Errorf("docker-compose.prod.yml %s does not list the PROVISIONING_INTERNAL_CREDENTIAL secret", credentialService.service)
		}
		if _, plainPresent := composeEnvValue(credentialService.lines, credentialService.plainVar); plainPresent {
			testInstance.Errorf("docker-compose.prod.yml %s sets a plain %s value; use the _FILE secret", credentialService.service, credentialService.plainVar)
		}
	}
	wantModes := map[string]string{
		"PROVISIONING_INTERNAL_CREDENTIAL_MODE": "log-only",
		"PROVISIONING_APP_GRANT_MODE":           "enforce",
	}
	for name, wantValue := range wantModes {
		gotValue, present := composeEnvValue(provisioningLines, name)
		if !present || gotValue != wantValue {
			testInstance.Errorf("docker-compose.prod.yml provisioning-api: %s = %q (present=%v), want %q", name, gotValue, present, wantValue)
		}
	}

	contents, readErr := os.ReadFile(filepath.Join("..", "docker-compose.prod.yml"))
	if readErr != nil {
		testInstance.Fatalf("read docker-compose.prod.yml: %v", readErr)
	}

	// The top-level secrets map points at the persistent (never deleted) directory.
	secretMapping := regexp.MustCompile(`(?m)^  PROVISIONING_INTERNAL_CREDENTIAL:\n    file: \./secrets-persistent/PROVISIONING_INTERNAL_CREDENTIAL$`)
	if !secretMapping.Match(contents) {
		testInstance.Error("docker-compose.prod.yml does not map PROVISIONING_INTERNAL_CREDENTIAL to ./secrets-persistent/PROVISIONING_INTERNAL_CREDENTIAL")
	}

	// File-level: no plain value in any form or service (list, map or quoted env entry), and no
	// 64-hex literal anywhere in the file.
	plainValue := regexp.MustCompile(`(?m)(^|[\s"'-])(PROVISIONING_INTERNAL_CREDENTIAL|DELIVERY_PROVISIONING_CREDENTIAL)[ \t]*[=:][ \t]*\S`)
	if plainValue.Match(contents) {
		testInstance.Error("docker-compose.prod.yml sets a plain credential value; production reads only the _FILE secret")
	}
	if regexp.MustCompile(`[0-9a-f]{64}`).Match(contents) {
		testInstance.Error("docker-compose.prod.yml contains a 64-hex literal; no credential value may be committed there")
	}
}

// TestProdComposeWiresDeliveryAPIToPersonServiceWithPersistentSecrets pins production: approval
// registers the app in person-service (blocking), so delivery-api needs the URL and the Access
// service-token files, and those files must live in ./secrets-persistent/ (not the ./secrets/ that
// the deploy deletes right after `up`).
func TestProdComposeWiresDeliveryAPIToPersonServiceWithPersistentSecrets(testInstance *testing.T) {
	deliveryLines := composeServiceLines(testInstance, "docker-compose.prod.yml", "delivery-api")
	provisioningLines := composeServiceLines(testInstance, "docker-compose.prod.yml", "provisioning-api")

	gotURL, present := composeEnvValue(deliveryLines, "PERSON_SERVICE_URL")
	if !present || gotURL != "${PERSON_SERVICE_URL:-https://notifs-people.4irl.app}" {
		testInstance.Errorf("docker-compose.prod.yml delivery-api: PERSON_SERVICE_URL = %q (present=%v)", gotURL, present)
	}
	for _, secretName := range []string{"PERSON_SERVICE_ACCESS_CLIENT_ID", "PERSON_SERVICE_ACCESS_CLIENT_SECRET"} {
		wantPath := "/run/secrets/" + secretName
		gotPath, filePresent := composeEnvValue(deliveryLines, secretName+"_FILE")
		if !filePresent || gotPath != wantPath {
			testInstance.Errorf("docker-compose.prod.yml delivery-api: %s_FILE = %q (present=%v), want %q", secretName, gotPath, filePresent, wantPath)
		}
		for service, lines := range map[string][]string{"delivery-api": deliveryLines, "provisioning-api": provisioningLines} {
			if !composeListsSecret(lines, secretName) {
				testInstance.Errorf("docker-compose.prod.yml %s does not list the %s secret", service, secretName)
			}
		}
	}

	contents, readErr := os.ReadFile(filepath.Join("..", "docker-compose.prod.yml"))
	if readErr != nil {
		testInstance.Fatalf("read docker-compose.prod.yml: %v", readErr)
	}
	for _, secretName := range []string{"PERSON_SERVICE_ACCESS_CLIENT_ID", "PERSON_SERVICE_ACCESS_CLIENT_SECRET"} {
		secretMapping := regexp.MustCompile(`(?m)^  ` + secretName + `:\n    file: \./secrets-persistent/` + secretName + `$`)
		if !secretMapping.Match(contents) {
			testInstance.Errorf("docker-compose.prod.yml does not map %s to ./secrets-persistent/%s", secretName, secretName)
		}
	}
	if strings.Contains(string(contents), "file: ./secrets/") {
		testInstance.Error("docker-compose.prod.yml still maps a secret to the deleted ./secrets/ directory")
	}

	deployContents, deployErr := os.ReadFile(filepath.Join("..", ".github", "workflows", "prod-deploy.yml"))
	if deployErr != nil {
		testInstance.Fatalf("read prod-deploy.yml: %v", deployErr)
	}
	deploy := string(deployContents)
	for _, secretName := range []string{"PERSON_SERVICE_ACCESS_CLIENT_ID", "PERSON_SERVICE_ACCESS_CLIENT_SECRET"} {
		for _, wantLine := range []string{
			`printf "%s" "${` + secretName + `_VALUE}" > ./secrets-persistent/` + secretName + `;`,
			`chmod 644 ./secrets-persistent/` + secretName + `;`,
		} {
			if !strings.Contains(deploy, wantLine) {
				testInstance.Errorf("prod-deploy.yml does not contain %q", wantLine)
			}
		}
		if strings.Contains(deploy, "./secrets/"+secretName) {
			testInstance.Errorf("prod-deploy.yml still writes %s into the deleted ./secrets/ directory", secretName)
		}
		wantValueLine := secretName + `_VALUE="${{ secrets.` + secretName + ` }}";`
		if !strings.Contains(deploy, wantValueLine) {
			testInstance.Errorf("prod-deploy.yml does not contain %q", wantValueLine)
		}
	}
	if !strings.Contains(deploy, "rm -rf ./secrets/;") {
		testInstance.Error("prod-deploy.yml does not clear leftovers with `rm -rf ./secrets/;`")
	}
}

// TestProdDeployWorkflowValidatesTheInternalCredentialFirst pins the deploy gate: the credential
// is a required workflow_call secret, a validation step with the 64-hex shape runs before the SSH
// key is installed (so nothing reaches the VPS on a bad value), the build-and-deploy workflow
// passes it through, and the only 64-hex literal in either workflow is the public dev value the
// validation rejects.
func TestProdDeployWorkflowValidatesTheInternalCredentialFirst(testInstance *testing.T) {
	deployContents, deployErr := os.ReadFile(filepath.Join("..", ".github", "workflows", "prod-deploy.yml"))
	if deployErr != nil {
		testInstance.Fatalf("read prod-deploy.yml: %v", deployErr)
	}
	buildContents, buildErr := os.ReadFile(filepath.Join("..", ".github", "workflows", "prod-build-and-deploy.yml"))
	if buildErr != nil {
		testInstance.Fatalf("read prod-build-and-deploy.yml: %v", buildErr)
	}
	deploy := string(deployContents)

	declared := regexp.MustCompile(`(?m)^      PROVISIONING_INTERNAL_CREDENTIAL:\n        required: true$`)
	if !declared.MatchString(deploy) {
		testInstance.Error("prod-deploy.yml does not declare PROVISIONING_INTERNAL_CREDENTIAL as a required workflow_call secret")
	}
	validateAt := strings.Index(deploy, "- name: Validate PROVISIONING_INTERNAL_CREDENTIAL secret")
	installKeyAt := strings.Index(deploy, "- name: Install SSH key")
	if validateAt < 0 || installKeyAt < 0 || validateAt > installKeyAt {
		testInstance.Fatal("prod-deploy.yml must validate PROVISIONING_INTERNAL_CREDENTIAL before the Install SSH key step")
	}
	validationStep := deploy[validateAt:installKeyAt]
	if !strings.Contains(validationStep, `"${PROVISIONING_INTERNAL_CREDENTIAL}" =~ ^[0-9a-f]{64}$`) || !strings.Contains(validationStep, "exit 1") {
		testInstance.Error("the PROVISIONING_INTERNAL_CREDENTIAL validation step does not fail on a value outside ^[0-9a-f]{64}$")
	}
	if !strings.Contains(validationStep, `"${PROVISIONING_INTERNAL_CREDENTIAL}" == "`+devInternalCredential+`"`) {
		testInstance.Error("the PROVISIONING_INTERNAL_CREDENTIAL validation step does not reject the public local-dev value")
	}
	for _, wantLine := range []string{
		`printf "%s" "${PROVISIONING_INTERNAL_CREDENTIAL_VALUE}" > ./secrets-persistent/PROVISIONING_INTERNAL_CREDENTIAL;`,
		`chmod 644 ./secrets-persistent/PROVISIONING_INTERNAL_CREDENTIAL;`,
		`[ -s ./secrets-persistent/PROVISIONING_INTERNAL_CREDENTIAL ]`,
	} {
		if !strings.Contains(deploy, wantLine) {
			testInstance.Errorf("prod-deploy.yml does not contain %q", wantLine)
		}
	}
	if !strings.Contains(string(buildContents), "PROVISIONING_INTERNAL_CREDENTIAL: ${{ secrets.PROVISIONING_INTERNAL_CREDENTIAL }}") {
		testInstance.Error("prod-build-and-deploy.yml does not pass PROVISIONING_INTERNAL_CREDENTIAL through")
	}

	hexLiteral := regexp.MustCompile(`[0-9a-f]{64}`)
	for name, contents := range map[string]string{"prod-deploy.yml": deploy, "prod-build-and-deploy.yml": string(buildContents)} {
		for _, literal := range hexLiteral.FindAllString(contents, -1) {
			if literal != devInternalCredential {
				testInstance.Errorf("%s contains a 64-hex literal that is not the public dev value", name)
			}
		}
	}
}

// TestProdComposeMountsThePreviousMasterKeySecret pins production: only delivery-api reads the
// optional previous master key, through a _FILE secret mapped to the persistent directory, never a
// plain value, and delivery-migrate carries no master key at all.
func TestProdComposeMountsThePreviousMasterKeySecret(testInstance *testing.T) {
	const secretPath = "/run/secrets/DELIVERY_MASTER_KEY_PREVIOUS"
	deliveryLines := composeServiceLines(testInstance, "docker-compose.prod.yml", "delivery-api")
	migrateLines := composeServiceLines(testInstance, "docker-compose.prod.yml", "delivery-migrate")

	gotValue, present := composeEnvValue(deliveryLines, "DELIVERY_MASTER_KEY_PREVIOUS_FILE")
	if !present || gotValue != secretPath {
		testInstance.Errorf("docker-compose.prod.yml delivery-api: DELIVERY_MASTER_KEY_PREVIOUS_FILE = %q (present=%v), want %q", gotValue, present, secretPath)
	}
	if !composeListsSecret(deliveryLines, "DELIVERY_MASTER_KEY_PREVIOUS") {
		testInstance.Error("docker-compose.prod.yml delivery-api does not list the DELIVERY_MASTER_KEY_PREVIOUS secret")
	}
	if _, plainPresent := composeEnvValue(deliveryLines, "DELIVERY_MASTER_KEY_PREVIOUS"); plainPresent {
		testInstance.Error("docker-compose.prod.yml delivery-api sets a plain DELIVERY_MASTER_KEY_PREVIOUS value; use the _FILE secret")
	}
	for _, migrateLine := range migrateLines {
		if strings.Contains(migrateLine, "DELIVERY_MASTER_KEY") {
			testInstance.Errorf("docker-compose.prod.yml delivery-migrate must not carry a master key, found %q", migrateLine)
		}
	}

	contents, readErr := os.ReadFile(filepath.Join("..", "docker-compose.prod.yml"))
	if readErr != nil {
		testInstance.Fatalf("read docker-compose.prod.yml: %v", readErr)
	}
	secretMapping := regexp.MustCompile(`(?m)^  DELIVERY_MASTER_KEY_PREVIOUS:\n    file: \./secrets-persistent/DELIVERY_MASTER_KEY_PREVIOUS$`)
	if !secretMapping.Match(contents) {
		testInstance.Error("docker-compose.prod.yml does not map DELIVERY_MASTER_KEY_PREVIOUS to ./secrets-persistent/DELIVERY_MASTER_KEY_PREVIOUS")
	}
	plainValue := regexp.MustCompile(`(?m)(^|[\s"'-])DELIVERY_MASTER_KEY_PREVIOUS[ \t]*[=:][ \t]*[^\s/]`)
	if plainValue.Match(contents) {
		testInstance.Error("docker-compose.prod.yml sets a plain DELIVERY_MASTER_KEY_PREVIOUS value; production reads only the _FILE secret")
	}
}

// TestProdDeployWorkflowHandlesThePreviousMasterKey pins the deploy wiring of the optional previous
// key: an optional workflow_call secret, a validation step before the SSH key is installed that
// checks the shape only when set and rejects a value equal to the current key without echoing it,
// a set-branch write and an unset-branch truncate, and the build-and-deploy passthrough.
func TestProdDeployWorkflowHandlesThePreviousMasterKey(testInstance *testing.T) {
	deployContents, deployErr := os.ReadFile(filepath.Join("..", ".github", "workflows", "prod-deploy.yml"))
	if deployErr != nil {
		testInstance.Fatalf("read prod-deploy.yml: %v", deployErr)
	}
	buildContents, buildErr := os.ReadFile(filepath.Join("..", ".github", "workflows", "prod-build-and-deploy.yml"))
	if buildErr != nil {
		testInstance.Fatalf("read prod-build-and-deploy.yml: %v", buildErr)
	}
	deploy := string(deployContents)

	declared := regexp.MustCompile(`(?m)^      DELIVERY_MASTER_KEY_PREVIOUS:\n        required: false$`)
	if !declared.MatchString(deploy) {
		testInstance.Error("prod-deploy.yml does not declare DELIVERY_MASTER_KEY_PREVIOUS as an optional (required: false) workflow_call secret")
	}
	validateAt := strings.Index(deploy, "- name: Validate DELIVERY_MASTER_KEY_PREVIOUS secret")
	installKeyAt := strings.Index(deploy, "- name: Install SSH key")
	if validateAt < 0 || installKeyAt < 0 || validateAt > installKeyAt {
		testInstance.Fatal("prod-deploy.yml must validate DELIVERY_MASTER_KEY_PREVIOUS before the Install SSH key step")
	}
	// Bound the step at the next step header so another step's text cannot satisfy these checks.
	validationStep := deploy[validateAt:installKeyAt]
	if nextStepAt := strings.Index(deploy[validateAt+1:], "\n      - name:"); nextStepAt >= 0 {
		validationStep = deploy[validateAt : validateAt+1+nextStepAt]
	}
	if !strings.Contains(validationStep, `-n "${DELIVERY_MASTER_KEY_PREVIOUS}"`) {
		testInstance.Error("the DELIVERY_MASTER_KEY_PREVIOUS validation step must only check a non-empty value")
	}
	if !strings.Contains(validationStep, `"${DELIVERY_MASTER_KEY_PREVIOUS}" =~ ^[0-9a-f]{64}$`) || !strings.Contains(validationStep, "exit 1") {
		testInstance.Error("the DELIVERY_MASTER_KEY_PREVIOUS validation step does not fail on a value outside ^[0-9a-f]{64}$")
	}
	if !strings.Contains(validationStep, `"${DELIVERY_MASTER_KEY_PREVIOUS}" == "${DELIVERY_MASTER_KEY}"`) {
		testInstance.Error("the DELIVERY_MASTER_KEY_PREVIOUS validation step does not reject a value equal to DELIVERY_MASTER_KEY")
	}
	if !strings.Contains(validationStep, "nothing was copied") {
		testInstance.Error("the DELIVERY_MASTER_KEY_PREVIOUS validation messages must say nothing was copied")
	}
	echoesKey := regexp.MustCompile(`echo.*(\$\{?DELIVERY_MASTER_KEY|\$\{\{)`)
	for _, line := range strings.Split(validationStep, "\n") {
		if echoesKey.MatchString(line) {
			testInstance.Errorf("the validation step must never echo a key value: %q", strings.TrimSpace(line))
		}
	}
	// The set branch writes the value, the else branch truncates to empty, then the mode is set.
	writeBranches := regexp.MustCompile(`if \[ -n "\$\{DELIVERY_MASTER_KEY_PREVIOUS_VALUE\}" \]; then\s+` +
		`printf "%s" "\$\{DELIVERY_MASTER_KEY_PREVIOUS_VALUE\}" > \./secrets-persistent/DELIVERY_MASTER_KEY_PREVIOUS;\s+` +
		`else\s+: > \./secrets-persistent/DELIVERY_MASTER_KEY_PREVIOUS;\s+fi;\s+` +
		`chmod 644 \./secrets-persistent/DELIVERY_MASTER_KEY_PREVIOUS;`)
	if !writeBranches.MatchString(deploy) {
		testInstance.Error("prod-deploy.yml must write the previous key when set, truncate its file when unset, then chmod 644")
	}
	if !strings.Contains(string(buildContents), "DELIVERY_MASTER_KEY_PREVIOUS: ${{ secrets.DELIVERY_MASTER_KEY_PREVIOUS }}") {
		testInstance.Error("prod-build-and-deploy.yml does not pass DELIVERY_MASTER_KEY_PREVIOUS through")
	}
}

// TestLocalComposeLeavesThePreviousMasterKeyUnset pins the local stack: the previous key is an
// empty-by-default passthrough, so the local delivery-api runs single-key.
func TestLocalComposeLeavesThePreviousMasterKeyUnset(testInstance *testing.T) {
	deliveryLines := composeServiceLines(testInstance, "docker-compose.yml", "delivery-api")
	gotValue, present := composeEnvValue(deliveryLines, "DELIVERY_MASTER_KEY_PREVIOUS")
	if !present || gotValue != "${DELIVERY_MASTER_KEY_PREVIOUS:-}" {
		testInstance.Errorf("docker-compose.yml delivery-api: DELIVERY_MASTER_KEY_PREVIOUS = %q (present=%v), want ${DELIVERY_MASTER_KEY_PREVIOUS:-}", gotValue, present)
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

func TestNewProberWiringUsesItsOwnFreshnessAndProductionDefaults(testInstance *testing.T) {
	client := newTestClient()
	holdOffUntil := func() time.Time { return time.Time{} }
	wiring := newProberWiring(nil, holdOffUntil, client, nil, newTestLogger())
	other := newProberWiring(nil, holdOffUntil, client, nil, newTestLogger())

	if wiring.freshness == nil {
		testInstance.Fatal("freshness is nil, want a prober tracker for /readyz")
	}
	if wiring.config.Freshness != wiring.freshness {
		testInstance.Error("config.Freshness is not the tracker handed to HeartbeatProber")
	}
	if wiring.freshness == other.freshness {
		testInstance.Error("two wirings share a tracker, want one fresh tracker per wiring (never the checker's)")
	}
	if wiring.config.Interval != heartbeat.DefaultProberInterval {
		testInstance.Errorf("Interval = %v, want %v", wiring.config.Interval, heartbeat.DefaultProberInterval)
	}
	if wiring.config.Concurrency != heartbeat.DefaultProberConcurrency {
		testInstance.Errorf("Concurrency = %d, want %d", wiring.config.Concurrency, heartbeat.DefaultProberConcurrency)
	}
	if wiring.config.Client != client {
		testInstance.Error("config.Client is not the guarded client passed in")
	}
	if wiring.config.HoldOffUntil == nil {
		testInstance.Error("config.HoldOffUntil is nil")
	}
	if wiring.run == nil {
		testInstance.Fatal("run is nil, want the RunProber background entry")
	}
}

var keyRotationSubcommands = []string{"rotate-keys", "verify-keys"}

// TestRunKeySubcommandsRejectArguments verifies no key can be passed on the command line: any argument is
// rejected before key or database work, and the error never echoes it.
func TestRunKeySubcommandsRejectArguments(testInstance *testing.T) {
	keyValue := strings.Repeat("cd", 32)
	for _, subcommand := range keyRotationSubcommands {
		for _, argument := range []string{keyValue, "--new-key=" + keyValue} {
			testInstance.Run(subcommand+" "+argument[:6], func(subTest *testing.T) {
				err := run(context.Background(), newTestLogger(), []string{subcommand, argument})
				if err == nil {
					subTest.Fatal("run returned nil, want the no-arguments error")
				}
				if err.Error() != subcommand+" takes no arguments" {
					subTest.Fatalf("run error = %q, want %q", err, subcommand+" takes no arguments")
				}
				if strings.Contains(err.Error(), keyValue) {
					subTest.Fatalf("run error = %q, must not contain the argument", err)
				}
			})
		}
	}
}

// TestRunKeySubcommandsResolveKeysBeforeTheDatabase verifies a missing or malformed master key fails with
// the key error even though the database is unreachable, so key resolution precedes any database work.
func TestRunKeySubcommandsResolveKeysBeforeTheDatabase(testInstance *testing.T) {
	testCases := []struct {
		name  string
		value string
	}{
		{name: "missing", value: ""},
		{name: "malformed", value: "not-hex"},
	}
	for _, subcommand := range keyRotationSubcommands {
		for _, testCase := range testCases {
			testInstance.Run(subcommand+" "+testCase.name, func(subTest *testing.T) {
				setDatabaseEnv(subTest)
				subTest.Setenv("DELIVERY_MASTER_KEY", testCase.value)
				subTest.Setenv("DELIVERY_MASTER_KEY_FILE", "")
				subTest.Setenv("DELIVERY_MASTER_KEY_PREVIOUS", "")
				subTest.Setenv("DELIVERY_MASTER_KEY_PREVIOUS_FILE", "")

				ctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
				defer cancel()
				err := run(ctx, newTestLogger(), []string{subcommand})

				if err == nil {
					subTest.Fatal("run returned nil, want a key error")
				}
				if !strings.Contains(err.Error(), "DELIVERY_MASTER_KEY") {
					subTest.Fatalf("run error = %q, want the master key error", err)
				}
				if testCase.value != "" && strings.Contains(err.Error(), testCase.value) {
					subTest.Fatalf("run error = %q, must not contain the key value", err)
				}
			})
		}
	}
}

func TestRunRotateKeysRequiresPreviousKey(testInstance *testing.T) {
	setDatabaseEnv(testInstance)
	testInstance.Setenv("DELIVERY_MASTER_KEY", strings.Repeat("ab", 32))
	testInstance.Setenv("DELIVERY_MASTER_KEY_FILE", "")
	testInstance.Setenv("DELIVERY_MASTER_KEY_PREVIOUS", "")
	testInstance.Setenv("DELIVERY_MASTER_KEY_PREVIOUS_FILE", "")

	ctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
	defer cancel()
	err := run(ctx, newTestLogger(), []string{"rotate-keys"})

	want := "rotate-keys requires DELIVERY_MASTER_KEY_PREVIOUS (the old key)"
	if err == nil || err.Error() != want {
		testInstance.Fatalf("run error = %v, want %q", err, want)
	}
}

// TestRunKeySubcommandsAcceptKeyFiles verifies keys supplied through the _FILE variables are accepted: the run
// gets past key resolution and fails only on the unreachable database.
func TestRunKeySubcommandsAcceptKeyFiles(testInstance *testing.T) {
	for _, subcommand := range keyRotationSubcommands {
		testInstance.Run(subcommand, func(subTest *testing.T) {
			setDatabaseEnv(subTest)
			keyDirectory := subTest.TempDir()
			primaryPath := filepath.Join(keyDirectory, "master-key")
			previousPath := filepath.Join(keyDirectory, "master-key-previous")
			if writeErr := os.WriteFile(primaryPath, []byte(strings.Repeat("ab", 32)+"\n"), 0o600); writeErr != nil {
				subTest.Fatalf("write primary key file: %v", writeErr)
			}
			if writeErr := os.WriteFile(previousPath, []byte(strings.Repeat("cd", 32)+"\n"), 0o600); writeErr != nil {
				subTest.Fatalf("write previous key file: %v", writeErr)
			}
			subTest.Setenv("DELIVERY_MASTER_KEY", "")
			subTest.Setenv("DELIVERY_MASTER_KEY_FILE", primaryPath)
			subTest.Setenv("DELIVERY_MASTER_KEY_PREVIOUS", "")
			subTest.Setenv("DELIVERY_MASTER_KEY_PREVIOUS_FILE", previousPath)

			ctx, cancel := context.WithTimeout(context.Background(), resultTimeout)
			defer cancel()
			err := run(ctx, newTestLogger(), []string{subcommand})

			if err == nil {
				subTest.Fatal("run returned nil against an unreachable database, want an error")
			}
			if strings.Contains(err.Error(), "DELIVERY_MASTER_KEY") || strings.Contains(err.Error(), "requires") {
				subTest.Fatalf("run error = %q, want the key files accepted", err)
			}
			if !strings.Contains(err.Error(), "read ") {
				subTest.Fatalf("run error = %q, want the database read error from the unreachable port", err)
			}
		})
	}
}
