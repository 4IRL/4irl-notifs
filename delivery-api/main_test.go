package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
