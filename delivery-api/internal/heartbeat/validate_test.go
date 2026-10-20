package heartbeat

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func assertValidationMessage(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("expected error %q, got nil", want)
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected *ValidationError, got %T (%v)", err, err)
	}
	if validationErr.Message != want || err.Error() != want {
		t.Fatalf("message = %q / Error() = %q, want %q", validationErr.Message, err.Error(), want)
	}
}

func TestValidateAppIDAndName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{"simple", "nightly_job", true},
		{"single char", "a", true},
		{"digit first", "1abc", true},
		{"63 chars", strings.Repeat("a", 63), true},
		{"empty", "", false},
		{"uppercase", "Nightly", false},
		{"hyphen", "a-b", false},
		{"leading underscore", "_x", false},
		{"64 chars", strings.Repeat("a", 64), false},
		{"trailing newline", "job\n", false},
		{"space", "my job", false},
		{"non ascii", "café", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wantName, wantApp := "", ""
			if !test.valid {
				wantName, wantApp = "invalid name", "invalid app_id"
			}
			assertValidationMessage(t, ValidateName(test.input), wantName)
			assertValidationMessage(t, ValidateAppID(test.input), wantApp)
		})
	}
}

func TestValidatePeriodAndGraceBounds(t *testing.T) {
	periodTests := []struct {
		value int
		valid bool
	}{
		{MinPeriodSeconds - 1, false}, {MinPeriodSeconds, true}, {MaxPeriodSeconds, true},
		{MaxPeriodSeconds + 1, false}, {0, false}, {-1, false},
	}
	for _, test := range periodTests {
		want := ""
		if !test.valid {
			want = "invalid period_seconds"
		}
		assertValidationMessage(t, ValidatePeriodSeconds(test.value), want)
	}
	graceTests := []struct {
		value int
		valid bool
	}{
		{MinGraceSeconds - 1, false}, {MinGraceSeconds, true}, {MaxGraceSeconds, true},
		{MaxGraceSeconds + 1, false}, {0, false}, {-1, false},
	}
	for _, test := range graceTests {
		want := ""
		if !test.valid {
			want = "invalid grace_seconds"
		}
		assertValidationMessage(t, ValidateGraceSeconds(test.value), want)
	}
}

func TestValidateDestinations(t *testing.T) {
	twenty := make([]string, 0, MaxDestinations)
	for index := range MaxDestinations {
		twenty = append(twenty, fmt.Sprintf("dest_%d", index))
	}
	tests := []struct {
		name  string
		input []string
		valid bool
	}{
		{"one", []string{"ops"}, true},
		{"twenty", twenty, true},
		{"twenty one", append(append([]string{}, twenty...), "extra"), false},
		{"nil", nil, false},
		{"empty", []string{}, false},
		{"duplicate", []string{"ops", "ops"}, false},
		{"bad name", []string{"ops", "Bad Name"}, false},
		{"empty name", []string{""}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want := ""
			if !test.valid {
				want = "invalid destinations"
			}
			assertValidationMessage(t, ValidateDestinations(test.input), want)
		})
	}
}

func TestValidatePingKind(t *testing.T) {
	assertValidationMessage(t, ValidatePingKind(PingSuccess), "")
	assertValidationMessage(t, ValidatePingKind(PingFail), "")
	assertValidationMessage(t, ValidatePingKind(""), "invalid kind")
	assertValidationMessage(t, ValidatePingKind("start"), "invalid kind")
}

func TestValidateNewCheckReportsFirstFailingField(t *testing.T) {
	valid := NewCheck{
		AppID: "billing", Name: "nightly", PeriodSeconds: 3600, GraceSeconds: 600,
		DestinationNames: []string{"ops"},
	}
	assertValidationMessage(t, ValidateNewCheck(valid), "")

	tests := []struct {
		name   string
		mutate func(*NewCheck)
		want   string
	}{
		{"app_id", func(check *NewCheck) { check.AppID = "Bad" }, "invalid app_id"},
		{"name", func(check *NewCheck) { check.Name = "" }, "invalid name"},
		{"period", func(check *NewCheck) { check.PeriodSeconds = 59 }, "invalid period_seconds"},
		{"grace", func(check *NewCheck) { check.GraceSeconds = 59 }, "invalid grace_seconds"},
		{"destinations", func(check *NewCheck) { check.DestinationNames = nil }, "invalid destinations"},
		{"app_id before name", func(check *NewCheck) { check.AppID, check.Name = "Bad", "Bad" }, "invalid app_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			check := valid
			test.mutate(&check)
			assertValidationMessage(t, ValidateNewCheck(check), test.want)
		})
	}
}

func TestValidateCheckUpdate(t *testing.T) {
	assertValidationMessage(t, ValidateCheckUpdate(CheckUpdate{
		PeriodSeconds: 60, GraceSeconds: 60, DestinationNames: []string{"ops"},
	}), "")
	assertValidationMessage(t, ValidateCheckUpdate(CheckUpdate{
		PeriodSeconds: 60, GraceSeconds: 60,
	}), "invalid destinations")
}
