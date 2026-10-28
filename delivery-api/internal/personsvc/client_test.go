package personsvc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testClientID     = "client-id-1"
	testClientSecret = "super-secret-value"
)

func newBufferLogger(buffer *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestCreateAppRequestShape(testInstance *testing.T) {
	type capturedRequest struct{ method, path, contentType, id, secret, body string }
	// Buffered so the handler goroutine hands the data over with a happens-before edge.
	captured := make(chan capturedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		captured <- capturedRequest{
			method:      request.Method,
			path:        request.URL.Path,
			contentType: request.Header.Get("Content-Type"),
			id:          request.Header.Get("CF-Access-Client-Id"),
			secret:      request.Header.Get("CF-Access-Client-Secret"),
			body:        string(raw),
		}
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL + "/", AccessClientID: testClientID, AccessClientSecret: testClientSecret,
	})
	if createErr := client.CreateApp(context.Background(), "my-app", "My App"); createErr != nil {
		testInstance.Fatalf("CreateApp: %v", createErr)
	}
	got := <-captured
	gotMethod, gotPath, gotContentType, gotID, gotSecret, gotBody :=
		got.method, got.path, got.contentType, got.id, got.secret, got.body
	if gotMethod != http.MethodPost || gotPath != "/apps" {
		testInstance.Fatalf("got %s %s", gotMethod, gotPath)
	}
	if gotContentType != "application/json" {
		testInstance.Fatalf("content type %q", gotContentType)
	}
	if gotID != testClientID || gotSecret != testClientSecret {
		testInstance.Fatalf("access headers %q %q", gotID, gotSecret)
	}
	if gotBody != `{"app_id":"my-app","display_name":"My App"}` {
		testInstance.Fatalf("body %q", gotBody)
	}
}

func TestCreateAppAccessHeadersOnlyWhenConfigured(testInstance *testing.T) {
	tests := []struct {
		name, id, secret string
		wantID, wantSec  bool
	}{
		{"none", "", "", false, false},
		{"id only", "x", "", true, false},
		{"secret only", "", "y", false, true},
		{"both", "x", "y", true, true},
	}
	for _, testCase := range tests {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			headers := make(chan http.Header, 1)
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				headers <- request.Header.Clone()
				writer.WriteHeader(http.StatusCreated)
			}))
			defer server.Close()
			client := NewClient(Config{
				BaseURL: server.URL, AccessClientID: testCase.id, AccessClientSecret: testCase.secret,
			})
			if createErr := client.CreateApp(context.Background(), "a", "A"); createErr != nil {
				subTest.Fatal(createErr)
			}
			header := <-headers
			_, hasID := header["Cf-Access-Client-Id"]
			_, hasSecret := header["Cf-Access-Client-Secret"]
			if hasID != testCase.wantID || hasSecret != testCase.wantSec {
				subTest.Fatalf("hasID=%v hasSecret=%v", hasID, hasSecret)
			}
		})
	}
}

func TestCreateAppStatuses(testInstance *testing.T) {
	tests := []struct {
		status  int
		wantErr bool
	}{
		{http.StatusCreated, false},
		{http.StatusOK, false},
		{http.StatusConflict, false},
		{http.StatusBadRequest, true},
		{http.StatusNotFound, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
	}
	for _, testCase := range tests {
		testInstance.Run(http.StatusText(testCase.status), func(subTest *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte("BODYLEAK " + testClientSecret))
			}))
			defer server.Close()
			client := NewClient(Config{BaseURL: server.URL, AccessClientID: "i", AccessClientSecret: testClientSecret})
			createErr := client.CreateApp(context.Background(), "a", "A")
			if (createErr != nil) != testCase.wantErr {
				subTest.Fatalf("err=%v wantErr=%v", createErr, testCase.wantErr)
			}
			if calls.Load() != 1 {
				subTest.Fatalf("calls=%d", calls.Load())
			}
			if createErr != nil {
				text := createErr.Error()
				if strings.Contains(text, "BODYLEAK") || strings.Contains(text, testClientSecret) {
					subTest.Fatalf("error leaks: %q", text)
				}
			}
		})
	}
}

func TestCreateAppTimeout(testInstance *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		<-release
	}))
	defer server.Close()
	defer close(release)
	client := NewClient(Config{BaseURL: server.URL, Timeout: 50 * time.Millisecond})
	createErr := client.CreateApp(context.Background(), "a", "A")
	if createErr == nil {
		testInstance.Fatal("want timeout error")
	}
	if !errors.Is(createErr, context.DeadlineExceeded) && !os.IsTimeout(createErr) {
		testInstance.Fatalf("want a timeout error, got %v", createErr)
	}
	if calls.Load() != 1 {
		testInstance.Fatalf("calls=%d", calls.Load())
	}
}

func TestCreateAppRedirectNotFollowed(testInstance *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		writer.WriteHeader(http.StatusCreated)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/apps", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	client := NewClient(Config{BaseURL: server.URL})
	if createErr := client.CreateApp(context.Background(), "a", "A"); createErr == nil {
		testInstance.Fatal("want error on 307")
	}
	if targetHits.Load() != 0 {
		testInstance.Fatal("redirect was followed")
	}
}

func TestCreateAppUnconfiguredNeverDials(testInstance *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client := NewClient(Config{})
	if client.Configured() {
		testInstance.Fatal("empty config must not be configured")
	}
	if createErr := client.CreateApp(context.Background(), "a", "A"); !errors.Is(createErr, ErrNotConfigured) {
		testInstance.Fatalf("err=%v", createErr)
	}
	if calls.Load() != 0 {
		testInstance.Fatal("dialed")
	}
	if !NewClient(Config{BaseURL: server.URL}).Configured() {
		testInstance.Fatal("BaseURL set must be configured")
	}
}

func TestCreateAppLogsStartAndEnd(testInstance *testing.T) {
	tests := []struct {
		name        string
		status      int
		hang        bool
		wantOutcome string
	}{
		{"created", http.StatusCreated, false, "created"},
		{"exists", http.StatusConflict, false, "exists"},
		{"server error", http.StatusInternalServerError, false, "failed"},
		{"timeout", 0, true, "failed"},
	}
	for _, testCase := range tests {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				if testCase.hang {
					<-release
					return
				}
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte("BODYLEAK"))
			}))
			defer server.Close()
			defer close(release)

			var buffer bytes.Buffer
			client := NewClient(Config{
				BaseURL: server.URL, AccessClientID: "i", AccessClientSecret: testClientSecret,
				Timeout: 50 * time.Millisecond, Logger: newBufferLogger(&buffer),
			})
			_ = client.CreateApp(context.Background(), "log-app", "Log App")

			lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
			if len(lines) < 2 {
				subTest.Fatalf("want start and end lines, got %q", buffer.String())
			}
			if !strings.Contains(lines[0], "start") {
				subTest.Fatalf("first line not start: %q", lines[0])
			}
			end := lines[len(lines)-1]
			for _, want := range []string{"end", "app_id=log-app", "outcome=" + testCase.wantOutcome, "duration"} {
				if !strings.Contains(end, want) {
					subTest.Fatalf("end line missing %q: %q", want, end)
				}
			}
			if !testCase.hang && !strings.Contains(end, "status=") {
				subTest.Fatalf("end line missing status: %q", end)
			}
			if strings.Contains(buffer.String(), testClientSecret) || strings.Contains(buffer.String(), "BODYLEAK") {
				subTest.Fatalf("log leaks secret or body: %q", buffer.String())
			}
		})
	}
}
