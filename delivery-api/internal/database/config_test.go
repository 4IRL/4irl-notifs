package database

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// envGetter returns a getenv func backed by a map, so tests never touch the
// process environment.
func envGetter(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// fixedSecret returns a resolveSecret func yielding value and err for any key.
func fixedSecret(value string, err error) func(string) (string, error) {
	return func(string) (string, error) { return value, err }
}

func fullEnv() map[string]string {
	return map[string]string{
		"DELIVERY_DB_HOST": "delivery-postgres",
		"DELIVERY_DB_PORT": "5432",
		"DELIVERY_DB_USER": "delivery",
		"DELIVERY_DB_NAME": "delivery",
	}
}

func TestConfigFromEnv(testInstance *testing.T) {
	resolverFailure := errors.New("secret backend exploded")

	testCases := []struct {
		name         string
		mutateEnv    func(map[string]string)
		password     string
		resolveErr   error
		wantConfig   Config
		wantErrParts []string
	}{
		{
			name:     "all set",
			password: "s3cret",
			wantConfig: Config{
				Host: "delivery-postgres", Port: "5432", User: "delivery", Name: "delivery", Password: "s3cret",
			},
		},
		{
			name:      "port unset defaults to 5432",
			mutateEnv: func(env map[string]string) { delete(env, "DELIVERY_DB_PORT") },
			password:  "s3cret",
			wantConfig: Config{
				Host: "delivery-postgres", Port: "5432", User: "delivery", Name: "delivery", Password: "s3cret",
			},
		},
		{
			name:         "empty host",
			mutateEnv:    func(env map[string]string) { env["DELIVERY_DB_HOST"] = "" },
			password:     "s3cret",
			wantErrParts: []string{"DELIVERY_DB_HOST"},
		},
		{
			name:         "empty user",
			mutateEnv:    func(env map[string]string) { env["DELIVERY_DB_USER"] = "" },
			password:     "s3cret",
			wantErrParts: []string{"DELIVERY_DB_USER"},
		},
		{
			name:         "empty name",
			mutateEnv:    func(env map[string]string) { env["DELIVERY_DB_NAME"] = "" },
			password:     "s3cret",
			wantErrParts: []string{"DELIVERY_DB_NAME"},
		},
		{
			name:         "empty password",
			password:     "",
			wantErrParts: []string{"DELIVERY_DB_PASSWORD is required"},
		},
		{
			name:         "resolver error is wrapped",
			resolveErr:   resolverFailure,
			wantErrParts: []string{"DELIVERY_DB_PASSWORD", "secret backend exploded"},
		},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			env := fullEnv()
			if testCase.mutateEnv != nil {
				testCase.mutateEnv(env)
			}

			config, err := ConfigFromEnv(envGetter(env), fixedSecret(testCase.password, testCase.resolveErr))

			if len(testCase.wantErrParts) > 0 {
				if err == nil {
					subTest.Fatalf("ConfigFromEnv error = nil, want one containing %v", testCase.wantErrParts)
				}
				for _, part := range testCase.wantErrParts {
					if !strings.Contains(err.Error(), part) {
						subTest.Fatalf("ConfigFromEnv error = %q, want it to contain %q", err, part)
					}
				}
				if testCase.resolveErr != nil && !errors.Is(err, testCase.resolveErr) {
					subTest.Fatalf("ConfigFromEnv error = %v, want it to wrap the resolver error", err)
				}
				return
			}
			if err != nil {
				subTest.Fatalf("ConfigFromEnv error = %v, want nil", err)
			}
			if config != testCase.wantConfig {
				subTest.Fatalf("ConfigFromEnv = %+v, want %+v", config, testCase.wantConfig)
			}
		})
	}
}

func TestConfigFromEnvAsksResolverForPasswordKey(testInstance *testing.T) {
	var gotKey string
	resolver := func(key string) (string, error) {
		gotKey = key
		return "s3cret", nil
	}

	if _, err := ConfigFromEnv(envGetter(fullEnv()), resolver); err != nil {
		testInstance.Fatalf("ConfigFromEnv error = %v, want nil", err)
	}
	if gotKey != "DELIVERY_DB_PASSWORD" {
		testInstance.Fatalf("resolver key = %q, want %q", gotKey, "DELIVERY_DB_PASSWORD")
	}
}

func TestConfigRedactsPasswordWhenFormatted(testInstance *testing.T) {
	config := Config{Host: "delivery-postgres", Port: "5432", User: "delivery", Name: "delivery", Password: "hunter2"}

	var logBuffer bytes.Buffer
	slog.New(slog.NewTextHandler(&logBuffer, nil)).Info("config", "database", config)

	for label, output := range map[string]string{
		"%v":   fmt.Sprintf("%v", config),
		"%+v":  fmt.Sprintf("%+v", config),
		"slog": logBuffer.String(),
	} {
		if strings.Contains(output, "hunter2") {
			testInstance.Fatalf("%s output leaked the password: %s", label, output)
		}
	}
}

func TestConnString(testInstance *testing.T) {
	config := Config{Host: "delivery-postgres", Port: "5432", User: "delivery", Name: "delivery", Password: "p@ss"}

	const want = "postgres://delivery:p%40ss@delivery-postgres:5432/delivery?sslmode=disable"
	if got := config.ConnString(); got != want {
		testInstance.Fatalf("ConnString() = %q, want %q", got, want)
	}
}
