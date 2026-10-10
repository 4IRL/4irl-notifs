package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
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
