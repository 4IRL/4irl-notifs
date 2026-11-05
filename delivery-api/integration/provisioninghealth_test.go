//go:build integration

package integration

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProvisioningHealthReportsOK(testInstance *testing.T) {
	waitForReady(testInstance)

	response, getErr := readyzClient.Get(deliveryURL() + "/healthz/provisioning")
	if getErr != nil {
		testInstance.Fatalf("GET /healthz/provisioning: %v", getErr)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			testInstance.Errorf("closing body: %v", closeErr)
		}
	}()
	bodyBytes, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		testInstance.Fatalf("reading body: %v", readErr)
	}
	if response.StatusCode != http.StatusOK {
		testInstance.Fatalf("status = %d, want 200; body = %s", response.StatusCode, bodyBytes)
	}
	if got := strings.TrimSpace(string(bodyBytes)); got != `{"status":"ok"}` {
		testInstance.Errorf("body = %q, want {\"status\":\"ok\"}", got)
	}
}
