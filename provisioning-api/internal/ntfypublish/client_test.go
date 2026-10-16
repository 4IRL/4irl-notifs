package ntfypublish

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublishHappyPathSendsExpectedRequestAndReturnsMessageID(t *testing.T) {
	var capturedMethod string
	var capturedPath string
	var capturedAuthorization string
	var capturedBody string

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedMethod = request.Method
		capturedPath = request.URL.Path
		capturedAuthorization = request.Header.Get("Authorization")
		bodyBytes, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Fatalf("failed to read request body: %v", readErr)
		}
		capturedBody = string(bodyBytes)
		responseWriter.WriteHeader(http.StatusOK)
		if _, writeErr := responseWriter.Write([]byte(`{"id":"VkT2p9wQ"}`)); writeErr != nil {
			t.Fatalf("failed to write response body: %v", writeErr)
		}
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL})

	messageID, err := client.Publish(context.Background(), Message{Topic: "urls4irl-76gzqgp4byjl6dje-all", Token: "tk_ephemeral", Body: "hello world"})
	if err != nil {
		t.Fatalf("Publish returned unexpected error: %v", err)
	}

	if capturedMethod != http.MethodPost {
		t.Fatalf("method = %s, expected POST", capturedMethod)
	}
	if capturedPath != "/urls4irl-76gzqgp4byjl6dje-all" {
		t.Fatalf("path = %s, expected /urls4irl-76gzqgp4byjl6dje-all", capturedPath)
	}
	if capturedAuthorization != "Bearer tk_ephemeral" {
		t.Fatalf("Authorization = %q, expected %q", capturedAuthorization, "Bearer tk_ephemeral")
	}
	if capturedBody != "hello world" {
		t.Fatalf("body = %q, expected %q", capturedBody, "hello world")
	}
	if messageID != "VkT2p9wQ" {
		t.Fatalf("messageID = %q, expected %q", messageID, "VkT2p9wQ")
	}
}

func TestPublishSetsOptionalHeaders(t *testing.T) {
	testCases := []struct {
		name            string
		message         Message
		expectedHeaders map[string]string
	}{
		{
			name:            "title only",
			message:         Message{Title: "Chore Reaper test"},
			expectedHeaders: map[string]string{"Title": "Chore Reaper test"},
		},
		{
			name:    "title+tags",
			message: Message{Title: "Chore Reaper test", Tags: []string{"test_tube", "bell"}},
			expectedHeaders: map[string]string{
				"Title": "Chore Reaper test",
				"Tags":  "test_tube,bell",
			},
		},
		{
			name:            "click",
			message:         Message{Click: "https://example.com/x"},
			expectedHeaders: map[string]string{"Click": "https://example.com/x"},
		},
		{
			name:            "empty",
			message:         Message{},
			expectedHeaders: map[string]string{},
		},
		{
			name:            "non-ASCII title",
			message:         Message{Title: "Café test"},
			expectedHeaders: map[string]string{"Title": "=?utf-8?q?Caf=C3=A9_test?="},
		},
		{
			name:            "blank tags are skipped",
			message:         Message{Tags: []string{"bell", "", "  ", "test_tube"}},
			expectedHeaders: map[string]string{"Tags": "bell,test_tube"},
		},
		{
			name:            "only blank tags sends no Tags header",
			message:         Message{Tags: []string{"", " "}},
			expectedHeaders: map[string]string{},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var capturedHeader http.Header

			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
				capturedHeader = request.Header.Clone()
				responseWriter.WriteHeader(http.StatusOK)
				if _, writeErr := responseWriter.Write([]byte(`{"id":"abc"}`)); writeErr != nil {
					t.Fatalf("failed to write response body: %v", writeErr)
				}
			}))
			defer server.Close()

			client := NewClient(Config{BaseURL: server.URL})

			message := testCase.message
			message.Topic = "urls4irl-76gzqgp4byjl6dje-all"
			message.Token = "tk_ephemeral"
			message.Body = "hello"
			if _, err := client.Publish(context.Background(), message); err != nil {
				t.Fatalf("Publish returned unexpected error: %v", err)
			}

			for _, headerName := range []string{"Title", "Tags", "Click"} {
				expectedValue, expected := testCase.expectedHeaders[headerName]
				actualValues, present := capturedHeader[headerName]
				if !expected {
					if present {
						t.Fatalf("header %s = %q, expected it to be absent", headerName, actualValues)
					}
					continue
				}
				if !present || len(actualValues) != 1 || actualValues[0] != expectedValue {
					t.Fatalf("header %s = %q, expected %q", headerName, actualValues, expectedValue)
				}
			}
		})
	}
}

func TestEncodeHeaderValueRoundTripsThroughRFC2047Decoding(t *testing.T) {
	testCases := []struct {
		name          string
		originalTitle string
	}{
		{name: "accented", originalTitle: "Café test"},
		{name: "emoji", originalTitle: "Chore Reaper 🧹 test ☕"},
		// 125 x 2-byte runes (250 bytes) forces encode-word folding.
		{name: "long non-ASCII near the 256-byte limit", originalTitle: strings.Repeat("é", 125)},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			encodedTitle := encodeHeaderValue(testCase.originalTitle)
			if strings.ContainsAny(encodedTitle, "\r\n") {
				t.Fatalf("encoded title contains CR/LF: %q", encodedTitle)
			}

			decodedTitle, decodeErr := new(mime.WordDecoder).DecodeHeader(encodedTitle)
			if decodeErr != nil {
				t.Fatalf("DecodeHeader returned unexpected error: %v", decodeErr)
			}
			if decodedTitle != testCase.originalTitle {
				t.Fatalf("decoded title = %q, expected %q", decodedTitle, testCase.originalTitle)
			}
		})
	}
}

func TestPublishRejectsControlCharactersInHeadersWithoutSendingRequest(t *testing.T) {
	testCases := []struct {
		name    string
		message Message
	}{
		{name: "newline in title", message: Message{Title: "a\r\nX-Evil: 1"}},
		{name: "newline in click", message: Message{Click: "https://x/\r\nX: y"}},
		{name: "newline in tags", message: Message{Tags: []string{"a\r\nX: y"}}},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			requestReceived := false

			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
				requestReceived = true
				responseWriter.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewClient(Config{BaseURL: server.URL})

			message := testCase.message
			message.Topic = "urls4irl-76gzqgp4byjl6dje-all"
			message.Token = "tk_ephemeral"
			message.Body = "hello"
			messageID, err := client.Publish(context.Background(), message)
			if err == nil {
				t.Fatal("expected an error for a header value containing CR/LF")
			}
			if messageID != "" {
				t.Fatalf("messageID = %q, expected empty on failure", messageID)
			}
			if requestReceived {
				t.Fatal("request must not be sent when a header value contains CR/LF")
			}
		})
	}
}

func TestPublishTrailingSlashBaseURLAvoidsDoubleSlash(t *testing.T) {
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		capturedPath = request.URL.Path
		responseWriter.WriteHeader(http.StatusOK)
		if _, writeErr := responseWriter.Write([]byte(`{"id":"abc"}`)); writeErr != nil {
			t.Fatalf("failed to write response body: %v", writeErr)
		}
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL + "/"})

	if _, err := client.Publish(context.Background(), Message{Topic: "urls4irl-76gzqgp4byjl6dje-all", Token: "tk_ephemeral", Body: "hello"}); err != nil {
		t.Fatalf("Publish returned unexpected error: %v", err)
	}

	if capturedPath != "/urls4irl-76gzqgp4byjl6dje-all" {
		t.Fatalf("path = %s, expected /urls4irl-76gzqgp4byjl6dje-all (no double slash)", capturedPath)
	}
}

func TestPublishNonSuccessStatusReturnsErrorMentioningStatus(t *testing.T) {
	testCases := []struct {
		name       string
		statusCode int
	}{
		{name: "service unavailable", statusCode: http.StatusServiceUnavailable},
		{name: "internal server error", statusCode: http.StatusInternalServerError},
		{name: "bad request", statusCode: http.StatusBadRequest},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
				responseWriter.WriteHeader(testCase.statusCode)
				if _, writeErr := responseWriter.Write([]byte("boom")); writeErr != nil {
					t.Fatalf("failed to write response body: %v", writeErr)
				}
			}))
			defer server.Close()

			client := NewClient(Config{BaseURL: server.URL})

			messageID, err := client.Publish(context.Background(), Message{Topic: "urls4irl-76gzqgp4byjl6dje-all", Token: "tk_ephemeral", Body: "hello"})
			if err == nil {
				t.Fatal("expected a non-nil error for a non-2xx response")
			}
			if messageID != "" {
				t.Fatalf("messageID = %q, expected empty on failure", messageID)
			}
			expectedStatusMarker := http.StatusText(testCase.statusCode)
			if !strings.Contains(err.Error(), expectedStatusMarker) {
				t.Fatalf("error %q does not mention status %q", err.Error(), expectedStatusMarker)
			}
		})
	}
}

func TestPublishMalformedJSONResponseReturnsDecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		if _, writeErr := responseWriter.Write([]byte("not json at all")); writeErr != nil {
			t.Fatalf("failed to write response body: %v", writeErr)
		}
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL})

	messageID, err := client.Publish(context.Background(), Message{Topic: "urls4irl-76gzqgp4byjl6dje-all", Token: "tk_ephemeral", Body: "hello"})
	if err == nil {
		t.Fatal("expected a non-nil error for a malformed JSON response")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Fatalf("error %q does not mention %q", err.Error(), "decode response")
	}
	if messageID != "" {
		t.Fatalf("messageID = %q, expected empty on decode failure", messageID)
	}
}

func TestConfiguredReflectsWhetherBaseURLIsSet(t *testing.T) {
	if (NewClient(Config{})).Configured() {
		t.Fatal("Configured() must be false for an empty BaseURL")
	}
	if !(NewClient(Config{BaseURL: "http://ntfy:80"})).Configured() {
		t.Fatal("Configured() must be true for a non-empty BaseURL")
	}
}

func TestPublishPropagatesContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.WriteHeader(http.StatusOK)
		if _, writeErr := responseWriter.Write([]byte(`{"id":"abc"}`)); writeErr != nil {
			t.Fatalf("failed to write response body: %v", writeErr)
		}
	}))
	defer server.Close()

	client := NewClient(Config{BaseURL: server.URL})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := client.Publish(ctx, Message{Topic: "urls4irl-76gzqgp4byjl6dje-all", Token: "tk_ephemeral", Body: "hello"}); err == nil {
		t.Fatal("expected an error when the context is already cancelled")
	}
}
