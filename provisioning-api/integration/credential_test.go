package integration

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestEveryRequestCarriesTheInternalCredential(t *testing.T) {
	t.Setenv(credentialEnvName, "test-credential-value")
	var mutex sync.Mutex
	seen := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mutex.Lock()
		seen[request.Method] = request.Header.Get(credentialHeader)
		mutex.Unlock()
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{}`)
		}
		request, buildErr := newProvisioningRequest(method, server.URL+"/v1/x", body)
		if buildErr != nil {
			t.Fatalf("%s build: %v", method, buildErr)
		}
		response, doErr := http.DefaultClient.Do(request)
		if doErr != nil {
			t.Fatalf("%s do: %v", method, doErr)
		}
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Fatalf("%s close: %v", method, closeErr)
		}
	}

	mutex.Lock()
	defer mutex.Unlock()
	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		if seen[method] != "test-credential-value" {
			t.Errorf("%s header %s = %q, expected the env value", method, credentialHeader, seen[method])
		}
	}
}

func TestMissingCredentialNamesTheVariable(t *testing.T) {
	t.Setenv(credentialEnvName, "")
	_, credentialErr := internalCredential()
	if credentialErr == nil || !strings.Contains(credentialErr.Error(), "NOTIFS_INTERNAL_CREDENTIAL") {
		t.Fatalf("internalCredential error = %v, expected one naming NOTIFS_INTERNAL_CREDENTIAL", credentialErr)
	}
	request, buildErr := newProvisioningRequest(http.MethodGet, "http://127.0.0.1:1/v1/users", nil)
	if buildErr == nil || request != nil {
		t.Fatalf("request built without a credential: %v", request)
	}
	if !strings.Contains(buildErr.Error(), "NOTIFS_INTERNAL_CREDENTIAL") {
		t.Fatalf("build error %v does not name the variable", buildErr)
	}
}
