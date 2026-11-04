package integration

import (
	"fmt"
	"io"
	"net/http"
	"os"
)

const (
	// credentialEnvName is the one env name every harness that calls
	// provisioning-api directly reads the internal credential from.
	credentialEnvName = "NOTIFS_INTERNAL_CREDENTIAL"
	// credentialHeader is the header provisioning-api's internal credential
	// middleware checks on every route except GET /healthz.
	credentialHeader = "X-Notifs-Internal-Credential"
)

// internalCredential returns the credential from NOTIFS_INTERNAL_CREDENTIAL.
// An empty or unset variable is an error that names the variable (fail closed:
// no default, never an empty header).
func internalCredential() (string, error) {
	value := os.Getenv(credentialEnvName)
	if value == "" {
		return "", fmt.Errorf("%s is empty or unset; run via make go-integration-test (needs make local-up first)", credentialEnvName)
	}
	return value, nil
}

// newProvisioningRequest builds a request to provisioning-api carrying the
// internal credential. Every provisioning-api request in this suite, except the
// exempt GET /healthz, goes through it.
func newProvisioningRequest(method string, url string, body io.Reader) (*http.Request, error) {
	credential, credentialErr := internalCredential()
	if credentialErr != nil {
		return nil, credentialErr
	}
	request, buildErr := http.NewRequest(method, url, body)
	if buildErr != nil {
		return nil, buildErr
	}
	request.Header.Set(credentialHeader, credential)
	return request, nil
}
