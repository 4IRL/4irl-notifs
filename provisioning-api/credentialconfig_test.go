package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/enforcement"
)

const testStartupCredential = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestResolveCredentialMode(testInstance *testing.T) {
	testCases := []struct {
		name           string
		modeValue      string
		credential     string
		wantMode       enforcement.Mode
		wantCredential string
		wantErr        bool
		wantWarnings   int
	}{
		{"unset mode with credential is enforce", "", testStartupCredential, enforcement.Enforce, testStartupCredential, false, 0},
		{"explicit enforce with credential", "enforce", testStartupCredential, enforcement.Enforce, testStartupCredential, false, 0},
		{"log-only with credential", "log-only", testStartupCredential, enforcement.LogOnly, testStartupCredential, false, 0},
		{"unset mode without credential refuses to start", "", "", enforcement.Enforce, "", true, 0},
		{"enforce without credential refuses to start", "enforce", "", enforcement.Enforce, "", true, 0},
		{"log-only without credential warns once", "log-only", "", enforcement.LogOnly, "", false, 1},
		{"invalid mode refuses to start", "warn", testStartupCredential, enforcement.Enforce, "", true, 0},
		{"wrong case refuses to start", "Log-Only", testStartupCredential, enforcement.Enforce, "", true, 0},
		{"invalid mode without credential refuses to start", "off", "", enforcement.Enforce, "", true, 0},
		{"padded credential is trimmed", "enforce", " " + testStartupCredential + "\n", enforcement.Enforce, testStartupCredential, false, 0},
		{"whitespace-only credential in enforce refuses to start", "enforce", " \n\t", enforcement.Enforce, "", true, 0},
		{"whitespace-only credential in log-only warns once", "log-only", " \n\t", enforcement.LogOnly, "", false, 1},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			var logBuffer bytes.Buffer
			logger := slog.New(slog.NewTextHandler(&logBuffer, nil))

			mode, credential, err := resolveCredentialMode(logger, testCase.modeValue, testCase.credential)

			if (err != nil) != testCase.wantErr {
				testInstance.Fatalf("err = %v, wantErr = %v", err, testCase.wantErr)
			}
			if mode != testCase.wantMode {
				testInstance.Fatalf("mode = %v, want %v", mode, testCase.wantMode)
			}
			if credential != testCase.wantCredential {
				testInstance.Fatalf("credential = %q, want %q", credential, testCase.wantCredential)
			}
			if warnings := strings.Count(logBuffer.String(), "level=WARN"); warnings != testCase.wantWarnings {
				testInstance.Fatalf("warnings = %d, want %d: %s", warnings, testCase.wantWarnings, logBuffer.String())
			}
			if testCase.credential != "" {
				if strings.Contains(logBuffer.String(), testCase.credential) {
					testInstance.Fatalf("log leaked the credential: %s", logBuffer.String())
				}
				if err != nil && strings.Contains(err.Error(), testCase.credential) {
					testInstance.Fatalf("error leaked the credential: %v", err)
				}
			}
		})
	}
}

func TestResolveGrantMode(testInstance *testing.T) {
	testCases := []struct {
		name      string
		modeValue string
		wantMode  enforcement.Mode
		wantErr   bool
	}{
		{"unset is enforce", "", enforcement.Enforce, false},
		{"explicit enforce", "enforce", enforcement.Enforce, false},
		{"log-only", "log-only", enforcement.LogOnly, false},
		{"invalid mode refuses to start", "warn", enforcement.Enforce, true},
		{"wrong case refuses to start", "Log-Only", enforcement.Enforce, true},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			mode, err := resolveGrantMode(testCase.modeValue)

			if (err != nil) != testCase.wantErr {
				testInstance.Fatalf("err = %v, wantErr = %v", err, testCase.wantErr)
			}
			if mode != testCase.wantMode {
				testInstance.Fatalf("mode = %v, want %v", mode, testCase.wantMode)
			}
		})
	}
}

func TestResolveCredentialModeEnforceWithoutCredentialIsTheNamedError(testInstance *testing.T) {
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))

	_, _, err := resolveCredentialMode(logger, "enforce", "")

	if !errors.Is(err, errEnforceWithoutCredential) {
		testInstance.Fatalf("err = %v, want errEnforceWithoutCredential", err)
	}
}
