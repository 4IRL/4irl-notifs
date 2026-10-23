package appidentity

import (
	"strings"
	"testing"
	"time"
)

// envFrom returns a getenv over a fixed map.
func envFrom(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestConfigFromEnv(testInstance *testing.T) {
	testCases := []struct {
		label        string
		env          map[string]string
		wantAudience string
		wantFile     string
		wantDev      bool
		wantErr      string
	}{
		{label: "everything unset", env: map[string]string{}, wantAudience: "notifs-delivery"},
		{
			label:        "custom audience",
			env:          map[string]string{"DELIVERY_OIDC_AUDIENCE": "my-audience"},
			wantAudience: "my-audience",
		},
		{
			label:        "dev mode without a file is allowed",
			env:          map[string]string{"DELIVERY_OIDC_DEV_MODE": "1"},
			wantAudience: "notifs-delivery", wantDev: true,
		},
		{
			label:        "dev mode zero is off",
			env:          map[string]string{"DELIVERY_OIDC_DEV_MODE": "0"},
			wantAudience: "notifs-delivery",
		},
		{
			label: "file with dev mode",
			env: map[string]string{
				"DELIVERY_OIDC_JWKS_FILE": "/run/dev-oidc/jwks.json", "DELIVERY_OIDC_DEV_MODE": "1",
			},
			wantAudience: "notifs-delivery", wantFile: "/run/dev-oidc/jwks.json", wantDev: true,
		},
		{
			label:   "file without the dev flag refuses to start",
			env:     map[string]string{"DELIVERY_OIDC_JWKS_FILE": "/run/dev-oidc/jwks.json"},
			wantErr: "DELIVERY_OIDC_JWKS_FILE requires DELIVERY_OIDC_DEV_MODE=1",
		},
		{
			label: "file with the dev flag explicitly zero refuses to start",
			env: map[string]string{
				"DELIVERY_OIDC_JWKS_FILE": "/run/dev-oidc/jwks.json", "DELIVERY_OIDC_DEV_MODE": "0",
			},
			wantErr: "DELIVERY_OIDC_JWKS_FILE requires DELIVERY_OIDC_DEV_MODE=1",
		},
		{
			label:   "dev mode with an unrecognised value refuses to start",
			env:     map[string]string{"DELIVERY_OIDC_DEV_MODE": "true"},
			wantErr: "invalid DELIVERY_OIDC_DEV_MODE",
		},
		{
			label:   "audience with a space is invalid",
			env:     map[string]string{"DELIVERY_OIDC_AUDIENCE": "two words"},
			wantErr: "invalid DELIVERY_OIDC_AUDIENCE",
		},
		{
			label:   "audience over the length cap is invalid",
			env:     map[string]string{"DELIVERY_OIDC_AUDIENCE": strings.Repeat("a", 201)},
			wantErr: "invalid DELIVERY_OIDC_AUDIENCE",
		},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			config, err := ConfigFromEnv(envFrom(testCase.env))
			if testCase.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
					subTest.Fatalf("err = %v, want it to contain %q", err, testCase.wantErr)
				}
				for _, value := range testCase.env {
					if len(value) > 5 && strings.Contains(err.Error(), value) {
						subTest.Errorf("the error echoes the value %q", value)
					}
				}
				return
			}
			if err != nil {
				subTest.Fatalf("unexpected error: %v", err)
			}
			if config.Audience != testCase.wantAudience || config.JWKSFile != testCase.wantFile || config.DevMode != testCase.wantDev {
				subTest.Errorf("config = %+v, want audience %q file %q dev %v",
					config, testCase.wantAudience, testCase.wantFile, testCase.wantDev)
			}
		})
	}
}

func TestConfigFromEnvPendingRequestTTL(testInstance *testing.T) {
	const day = 24 * time.Hour
	testCases := []struct {
		label   string
		value   string
		wantTTL time.Duration
		wantErr bool
	}{
		{label: "unset defaults to seven days", value: "", wantTTL: 7 * day},
		{label: "one day is the smallest accepted value", value: "1", wantTTL: day},
		{label: "a custom value", value: "14", wantTTL: 14 * day},
		{label: "the largest accepted value", value: "365", wantTTL: 365 * day},
		{label: "zero is refused, not read as no expiry", value: "0", wantErr: true},
		{label: "negative is refused", value: "-3", wantErr: true},
		{label: "over the maximum is refused", value: "366", wantErr: true},
		{label: "not an integer is refused", value: "seven", wantErr: true},
		{label: "a duration string is refused", value: "168h", wantErr: true},
		{label: "a fraction is refused", value: "1.5", wantErr: true},
		{label: "whitespace is refused", value: " 7", wantErr: true},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.label, func(subTest *testing.T) {
			config, err := ConfigFromEnv(envFrom(map[string]string{PendingRequestDaysEnvVar: testCase.value}))
			if testCase.wantErr {
				if err == nil || err.Error() != "invalid DELIVERY_PENDING_IDENTITY_REQUEST_DAYS" {
					subTest.Fatalf("err = %v, want exactly %q", err, "invalid DELIVERY_PENDING_IDENTITY_REQUEST_DAYS")
				}
				if config.PendingRequestTTL != 0 {
					subTest.Errorf("an invalid value returned ttl %v, want the zero config", config.PendingRequestTTL)
				}
				return
			}
			if err != nil {
				subTest.Fatalf("unexpected error: %v", err)
			}
			if config.PendingRequestTTL != testCase.wantTTL {
				subTest.Errorf("PendingRequestTTL = %v, want %v", config.PendingRequestTTL, testCase.wantTTL)
			}
		})
	}
}
