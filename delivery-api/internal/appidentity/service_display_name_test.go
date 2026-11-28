package appidentity

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// logMarker differs from the registrar name and the derived app id, so a leak of the value into
// a log line is detectable.
const logMarker = "Zq Display Marker"

func exchangeWithDisplayName(fixture *exchangeFixture, displayName string) (ExchangeResult, error) {
	return fixture.service.Exchange(context.Background(),
		ExchangeInput{Name: "blog", DisplayName: displayName, Token: exchangeTestToken})
}

func TestValidateDisplayName(testInstance *testing.T) {
	for _, testCase := range []struct {
		name    string
		input   string
		want    string
		wantErr error
	}{
		{"plain", "My Blog", "My Blog", nil},
		{"trimmed", "  My Blog\t ", "My Blog", nil},
		{"64 runes", strings.Repeat("a", 64), strings.Repeat("a", 64), nil},
		{"65 runes", strings.Repeat("a", 65), "", ErrInvalidDisplayName},
		{"64 multi-byte runes", strings.Repeat("é", 64), strings.Repeat("é", 64), nil},
		{"65 multi-byte runes", strings.Repeat("é", 65), "", ErrInvalidDisplayName},
		{"zero-width joiner", "My" + string(rune(0x200d)) + "Blog", "", ErrInvalidDisplayName},
		{"inner tab", "My\tBlog", "", ErrInvalidDisplayName},
		{"empty", "", "", ErrInvalidDisplayName},
		{"whitespace only", " \t ", "", ErrInvalidDisplayName},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			got, err := ValidateDisplayName(testCase.input)

			if !errors.Is(err, testCase.wantErr) || got != testCase.want {
				subTest.Errorf("ValidateDisplayName(%q) = %q, %v; want %q, %v", testCase.input, got, err, testCase.want, testCase.wantErr)
			}
		})
	}
}

func TestRegistrarExchangeForwardsTheDisplayNameOnFirstCreate(testInstance *testing.T) {
	for _, testCase := range []struct {
		name        string
		displayName string
		want        string
	}{
		{"a supplied name", "  My Blog ", "My Blog"},
		{"no name uses the app id", "", "hosted_blog"},
		{"a whitespace-only name is not provided", "   ", "hosted_blog"},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newRegistrarFixture()

			if _, err := exchangeWithDisplayName(fixture, testCase.displayName); err != nil {
				subTest.Fatalf("Exchange: %v", err)
			}

			if len(fixture.directory.displayName) != 1 || fixture.directory.displayName[0] != testCase.want {
				subTest.Errorf("CreateApp display names = %v, want [%s]", fixture.directory.displayName, testCase.want)
			}
		})
	}
}

func TestRegistrarExchangeRefusesABadDisplayNameBeforeAnythingElse(testInstance *testing.T) {
	for _, testCase := range []struct {
		name    string
		input   ExchangeInput
		wantErr error
	}{
		{"invalid", ExchangeInput{Name: "blog", DisplayName: strings.Repeat("a", 65), Token: exchangeTestToken}, ErrInvalidDisplayName},
		{"control rune", ExchangeInput{Name: "blog", DisplayName: "a\tb", Token: exchangeTestToken}, ErrInvalidDisplayName},
		{"with app_id", ExchangeInput{AppID: "urls4irl", DisplayName: "Blog", Token: exchangeTestToken}, ErrDisplayNameRequiresName},
		{"app_id with an invalid value reports requires-name", ExchangeInput{AppID: "urls4irl", DisplayName: strings.Repeat("a", 65), Token: exchangeTestToken}, ErrDisplayNameRequiresName},
		{"invalid with an empty token", ExchangeInput{Name: "blog", DisplayName: strings.Repeat("a", 65)}, ErrInvalidDisplayName},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newRegistrarFixture()

			_, err := fixture.service.Exchange(context.Background(), testCase.input)

			if !errors.Is(err, testCase.wantErr) {
				subTest.Fatalf("err = %v, want %v", err, testCase.wantErr)
			}
			if fixture.verifier.calls != 0 || fixture.store.runs != 0 || len(fixture.directory.appIDs) != 0 {
				subTest.Errorf("verifier calls %d, store runs %d, directory calls %v, want none",
					fixture.verifier.calls, fixture.store.runs, fixture.directory.appIDs)
			}
		})
	}
}

func TestExchangeWithAppIDAndAWhitespaceOnlyDisplayNameIsAcceptedAsNotProvided(testInstance *testing.T) {
	fixture := newExchangeFixture()

	_, err := fixture.service.Exchange(context.Background(),
		ExchangeInput{AppID: "urls4irl", DisplayName: "  ", Token: exchangeTestToken})

	if err != nil {
		testInstance.Errorf("err = %v, want none", err)
	}
}

func TestRegistrarExchangeContractRowsWithADisplayName(testInstance *testing.T) {
	testInstance.Run("the 202 pending path stores no name", func(subTest *testing.T) {
		fixture := newRegistrarFixture()
		fixture.store.state = RegistrarExchangeState{}
		fixture.tx.registrar = nil
		fixture.tx.requestID = 11

		result, err := exchangeWithDisplayName(fixture, "My Blog")

		if err != nil || result.Status != ExchangePending {
			subTest.Fatalf("result = %+v, err = %v, want pending", result, err)
		}
		if got := strings.Join(fixture.tx.calls, ","); got != "record,find-registrar,has-normal-binding,file-registrar" {
			subTest.Errorf("transaction calls = %q", got)
		}
		if len(fixture.directory.displayName) != 0 || fixture.store.runs != 1 || !fixture.store.committed {
			subTest.Errorf("directory %v, runs %d, committed %v", fixture.directory.displayName, fixture.store.runs, fixture.store.committed)
		}
	})
	testInstance.Run("a failing directory writes nothing and consumes no jti", func(subTest *testing.T) {
		fixture := newRegistrarFixture()
		fixture.directory.err = errScripted

		_, err := exchangeWithDisplayName(fixture, "My Blog")

		if !errors.Is(err, ErrAppRegistryUnavailable) || len(fixture.directory.appIDs) != 1 || fixture.store.runs != 0 || fixture.store.committed {
			subTest.Errorf("err = %v, directory %v, runs %d, committed %v", err, fixture.directory.appIDs, fixture.store.runs, fixture.store.committed)
		}
	})
}

func TestRegistrarExchangeOfAnOwnedAppNeverCallsTheDirectory(testInstance *testing.T) {
	fixture := newRegistrarFixture()
	fixture.store.state.Derived = DerivedAppOwned
	fixture.tx.bound, fixture.tx.binding = true, ownedBinding(9)

	if _, err := exchangeWithDisplayName(fixture, "Another Name"); err != nil {
		testInstance.Fatalf("Exchange: %v", err)
	}

	if len(fixture.directory.appIDs) != 0 {
		testInstance.Errorf("directory calls = %v, want none", fixture.directory.appIDs)
	}
}

func TestRegistrarExchangeDisplayNameLogging(testInstance *testing.T) {
	testInstance.Run("registrar_created carries display_name_set true and never the value", func(subTest *testing.T) {
		fixture := newRegistrarFixture()
		if _, err := exchangeWithDisplayName(fixture, logMarker); err != nil {
			subTest.Fatalf("Exchange: %v", err)
		}
		logs := fixture.logs.String()
		if !strings.Contains(logs, "outcome=registrar_created") || !strings.Contains(logs, "display_name_set=true") {
			subTest.Errorf("want registrar_created with display_name_set=true:\n%s", logs)
		}
		if strings.Contains(logs, logMarker) {
			subTest.Errorf("log leaks the display name:\n%s", logs)
		}
	})
	testInstance.Run("registrar_created without a name carries false", func(subTest *testing.T) {
		fixture := newRegistrarFixture()
		if _, err := exchangeWithDisplayName(fixture, ""); err != nil {
			subTest.Fatalf("Exchange: %v", err)
		}
		if logs := fixture.logs.String(); !strings.Contains(logs, "outcome=registrar_created") || !strings.Contains(logs, "display_name_set=false") {
			subTest.Errorf("want display_name_set=false:\n%s", logs)
		}
	})
	testInstance.Run("other outcome lines carry no display_name_set", func(subTest *testing.T) {
		for _, testCase := range []struct {
			outcome string
			setup   func(*exchangeFixture)
		}{
			{"outcome=registrar_issued", func(fixture *exchangeFixture) {
				fixture.store.state.Derived = DerivedAppOwned
				fixture.tx.bound, fixture.tx.binding = true, ownedBinding(9)
			}},
			{"outcome=registrar_pending", func(fixture *exchangeFixture) {
				fixture.store.state = RegistrarExchangeState{}
				fixture.tx.registrar = nil
			}},
			{"outcome=registrar_denied", func(fixture *exchangeFixture) { fixture.store.state.Derived = DerivedAppForeign }},
		} {
			fixture := newRegistrarFixture()
			testCase.setup(fixture)
			_, _ = exchangeWithDisplayName(fixture, logMarker)
			logs := fixture.logs.String()
			if !strings.Contains(logs, testCase.outcome) {
				subTest.Errorf("log is missing %q:\n%s", testCase.outcome, logs)
			}
			if strings.Contains(logs, "display_name_set") || strings.Contains(logs, logMarker) {
				subTest.Errorf("unexpected display_name logging for %s:\n%s", testCase.outcome, logs)
			}
		}
	})
	testInstance.Run("the registry-failure WARN carries display_name_set", func(subTest *testing.T) {
		fixture := newRegistrarFixture()
		fixture.directory.err = errScripted
		if _, err := exchangeWithDisplayName(fixture, logMarker); !errors.Is(err, ErrAppRegistryUnavailable) {
			subTest.Fatalf("err = %v, want ErrAppRegistryUnavailable", err)
		}
		logs := fixture.logs.String()
		if !strings.Contains(logs, "registration failed") || !strings.Contains(logs, "display_name_set=true") {
			subTest.Errorf("want the WARN with display_name_set=true:\n%s", logs)
		}
		if strings.Contains(logs, logMarker) {
			subTest.Errorf("log leaks the display name:\n%s", logs)
		}
	})
}
