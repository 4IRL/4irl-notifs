package personsvc

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

func TestCreateAppRequestShape(t *testing.T) {
	var gotMethod, gotPath, gotContentType, gotID, gotSecret, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotMethod = request.Method
		gotPath = request.URL.Path
		gotContentType = request.Header.Get("Content-Type")
		gotID = request.Header.Get("CF-Access-Client-Id")
		gotSecret = request.Header.Get("CF-Access-Client-Secret")
		raw, _ := io.ReadAll(request.Body)
		gotBody = string(raw)
		writer.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	client := NewClient(Config{
		BaseURL: server.URL + "/", AccessClientID: testClientID, AccessClientSecret: testClientSecret,
	})
	if createErr := client.CreateApp(context.Background(), "my-app", "My App"); createErr != nil {
		t.Fatalf("CreateApp: %v", createErr)
	}
	if gotMethod != http.MethodPost || gotPath != "/apps" {
		t.Fatalf("got %s %s", gotMethod, gotPath)
	}
	if gotContentType != "application/json" {
		t.Fatalf("content type %q", gotContentType)
	}
	if gotID != testClientID || gotSecret != testClientSecret {
		t.Fatalf("access headers %q %q", gotID, gotSecret)
	}
	if gotBody != `{"app_id":"my-app","display_name":"My App"}` {
		t.Fatalf("body %q", gotBody)
	}
}

func TestCreateAppAccessHeadersOnlyWhenConfigured(t *testing.T) {
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
		t.Run(testCase.name, func(t *testing.T) {
			var header http.Header
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				header = request.Header.Clone()
				writer.WriteHeader(http.StatusCreated)
			}))
			defer server.Close()
			client := NewClient(Config{
				BaseURL: server.URL, AccessClientID: testCase.id, AccessClientSecret: testCase.secret,
			})
			if createErr := client.CreateApp(context.Background(), "a", "A"); createErr != nil {
				t.Fatal(createErr)
			}
			_, hasID := header["Cf-Access-Client-Id"]
			_, hasSecret := header["Cf-Access-Client-Secret"]
			if hasID != testCase.wantID || hasSecret != testCase.wantSec {
				t.Fatalf("hasID=%v hasSecret=%v", hasID, hasSecret)
			}
		})
	}
}

func TestCreateAppStatuses(t *testing.T) {
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
		t.Run(http.StatusText(testCase.status), func(t *testing.T) {
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
				t.Fatalf("err=%v wantErr=%v", createErr, testCase.wantErr)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls=%d", calls.Load())
			}
			if createErr != nil {
				text := createErr.Error()
				if strings.Contains(text, "BODYLEAK") || strings.Contains(text, testClientSecret) {
					t.Fatalf("error leaks: %q", text)
				}
			}
		})
	}
}

func TestCreateAppTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))
	defer server.Close()
	defer close(release)
	client := NewClient(Config{BaseURL: server.URL, Timeout: 50 * time.Millisecond})
	if createErr := client.CreateApp(context.Background(), "a", "A"); createErr == nil {
		t.Fatal("want timeout error")
	}
}

func TestCreateAppRedirectNotFollowed(t *testing.T) {
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
		t.Fatal("want error on 307")
	}
	if targetHits.Load() != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestCreateAppUnconfiguredNeverDials(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client := NewClient(Config{})
	if client.Configured() {
		t.Fatal("empty config must not be configured")
	}
	if createErr := client.CreateApp(context.Background(), "a", "A"); !errors.Is(createErr, ErrNotConfigured) {
		t.Fatalf("err=%v", createErr)
	}
	if calls.Load() != 0 {
		t.Fatal("dialed")
	}
	if !NewClient(Config{BaseURL: server.URL}).Configured() {
		t.Fatal("BaseURL set must be configured")
	}
}

func TestCreateAppLogsStartAndEnd(t *testing.T) {
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
		t.Run(testCase.name, func(t *testing.T) {
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
				t.Fatalf("want start and end lines, got %q", buffer.String())
			}
			if !strings.Contains(lines[0], "start") {
				t.Fatalf("first line not start: %q", lines[0])
			}
			end := lines[len(lines)-1]
			for _, want := range []string{"end", "app_id=log-app", "outcome=" + testCase.wantOutcome, "duration"} {
				if !strings.Contains(end, want) {
					t.Fatalf("end line missing %q: %q", want, end)
				}
			}
			if !testCase.hang && !strings.Contains(end, "status=") {
				t.Fatalf("end line missing status: %q", end)
			}
			if strings.Contains(buffer.String(), testClientSecret) || strings.Contains(buffer.String(), "BODYLEAK") {
				t.Fatalf("log leaks secret or body: %q", buffer.String())
			}
		})
	}
}
