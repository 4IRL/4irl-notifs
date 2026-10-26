package enforcement

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	testCases := []struct {
		name        string
		value       string
		expected    Mode
		expectError bool
	}{
		{name: "empty means enforce", value: "", expected: Enforce},
		{name: "enforce", value: "enforce", expected: Enforce},
		{name: "log-only", value: "log-only", expected: LogOnly},
		{name: "capitalized enforce is rejected", value: "Enforce", expectError: true},
		{name: "uppercase log-only is rejected", value: "LOG-ONLY", expectError: true},
		{name: "leading space is rejected", value: " log-only", expectError: true},
		{name: "trailing space is rejected", value: "enforce ", expectError: true},
		{name: "whitespace only is rejected", value: " ", expectError: true},
		{name: "underscore spelling is rejected", value: "log_only", expectError: true},
		{name: "logonly is rejected", value: "logonly", expectError: true},
		{name: "off is rejected", value: "off", expectError: true},
		{name: "false is rejected", value: "false", expectError: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			mode, err := Parse("PROVISIONING_APP_GRANT_MODE", testCase.value)
			if testCase.expectError {
				if err == nil {
					t.Fatalf("expected an error for %q, got mode %v", testCase.value, mode)
				}
				if mode != Enforce {
					t.Fatalf("an invalid value must still fail closed to Enforce, got %v", mode)
				}
				for _, want := range []string{"PROVISIONING_APP_GRANT_MODE", "enforce", "log-only"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q should name %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", testCase.value, err)
			}
			if mode != testCase.expected {
				t.Fatalf("mode = %v, expected %v", mode, testCase.expected)
			}
		})
	}
}

func TestParseErrorNamesTheGivenEnvVar(t *testing.T) {
	_, err := Parse("PROVISIONING_INTERNAL_CREDENTIAL_MODE", "nope")
	if err == nil || !strings.Contains(err.Error(), "PROVISIONING_INTERNAL_CREDENTIAL_MODE") {
		t.Fatalf("error should name the env var, got: %v", err)
	}
}

func TestZeroValueIsEnforce(t *testing.T) {
	var mode Mode
	if mode != Enforce {
		t.Fatalf("zero value = %v, expected Enforce", mode)
	}
}

func TestString(t *testing.T) {
	testCases := []struct {
		mode     Mode
		expected string
	}{
		{mode: Enforce, expected: "enforce"},
		{mode: LogOnly, expected: "log-only"},
		{mode: Mode(99), expected: "enforce"},
	}
	for _, testCase := range testCases {
		if got := testCase.mode.String(); got != testCase.expected {
			t.Fatalf("Mode(%d).String() = %q, expected %q", int(testCase.mode), got, testCase.expected)
		}
	}
}
