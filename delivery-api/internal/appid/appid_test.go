package appid

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateAppID(testInstance *testing.T) {
	testCases := []struct {
		name  string
		appID string
		valid bool
	}{
		{"single letter", "a", true},
		{"single digit", "7", true},
		{"typical app", "urls4irl", true},
		{"underscore inside", "chores_4irl", true},
		{"leading digit", "4irl", true},
		{"63 characters", strings.Repeat("a", 63), true},
		{"64 characters", strings.Repeat("a", 64), false},
		{"empty", "", false},
		{"leading underscore", "_urls4irl", false},
		{"uppercase", "Urls4irl", false},
		{"hyphen", "urls-4irl", false},
		{"space", "urls 4irl", false},
		{"dot", "urls.4irl", false},
		{"slash", "urls/4irl", false},
		{"trailing newline", "urls4irl\n", false},
		{"non-ASCII letter", "urlsé", false},
		{"wildcard", "*", false},
	}
	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			if got := Valid(testCase.appID); got != testCase.valid {
				subTest.Errorf("Valid(%q) = %v, want %v", testCase.appID, got, testCase.valid)
			}
			err := ValidateAppID(testCase.appID)
			if testCase.valid && err != nil {
				subTest.Errorf("ValidateAppID(%q) = %v, want nil", testCase.appID, err)
			}
			if !testCase.valid && !errors.Is(err, ErrInvalid) {
				subTest.Errorf("ValidateAppID(%q) = %v, want ErrInvalid", testCase.appID, err)
			}
		})
	}
}

func TestErrInvalidMessageIsTheClientSafeText(testInstance *testing.T) {
	if got := ErrInvalid.Error(); got != "invalid app_id" {
		testInstance.Errorf("ErrInvalid message = %q, want %q", got, "invalid app_id")
	}
}
