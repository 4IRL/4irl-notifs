package httpapi

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
)

func TestProbePatchNeverEchoesOrLogsTheCredentials(testInstance *testing.T) {
	const (
		clientID     = "SECRET-PATCH-CLIENT-ID-4d8"
		clientSecret = "SECRET-PATCH-CLIENT-SECRET-2b6"
	)
	replace := `{"probe":{"access_token":{"client_id":"` + clientID + `","client_secret":"` + clientSecret + `"}}}`
	invalidURL := `{"probe":{"url":"http://example.com","access_token":{"client_id":"` + clientID + `","client_secret":"` + clientSecret + `"}}}`
	testCases := []struct {
		name       string
		fake       *fakeHeartbeats
		body       string
		wantStatus int
	}{
		{
			name: "replaced", fake: &fakeHeartbeats{getResult: sampleProbeCheck(), patchResult: sampleProbeCheck()},
			body: replace, wantStatus: http.StatusOK,
		},
		{
			name: "bad url", fake: &fakeHeartbeats{getResult: sampleProbeCheck(), patchResult: sampleProbeCheck()},
			body: invalidURL, wantStatus: http.StatusBadRequest,
		},
		{name: "not found", fake: failingHeartbeats(heartbeat.ErrCheckNotFound), body: replace, wantStatus: http.StatusNotFound},
		{name: "internal error", fake: failingHeartbeats(errors.New("boom")), body: replace, wantStatus: http.StatusInternalServerError},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			var logBuffer bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuffer, nil))

			recorder := serveHeartbeats(testCase.fake, logger, http.MethodPatch, "/v1/admin/heartbeats/9", testCase.body)

			if recorder.Code != testCase.wantStatus {
				subTest.Fatalf("status = %d, want %d (body %s)", recorder.Code, testCase.wantStatus, recorder.Body.String())
			}
			for _, secret := range []string{clientID, clientSecret} {
				if strings.Contains(recorder.Body.String(), secret) {
					subTest.Fatalf("response leaked %q: %s", secret, recorder.Body.String())
				}
				if strings.Contains(logBuffer.String(), secret) {
					subTest.Fatalf("log leaked %q: %s", secret, logBuffer.String())
				}
			}
		})
	}
}
