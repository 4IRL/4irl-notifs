package appidentity

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// hostedRegistrar is the registrar every name-form test exchanges as.
func hostedRegistrar() *WorkflowRegistrar {
	return &WorkflowRegistrar{
		ID: 9, Prefix: "hosted_", Repository: testRepository, RepositoryID: 1001, RepositoryOwnerID: 678,
		WorkflowPath: testWorkflow, Ref: "refs/heads/main", ApprovedAt: testNow.Add(-time.Hour),
	}
}

// ownedBinding is a binding of hosted_blog created by the hosted_ registrar.
func ownedBinding(registrarID int64) Binding {
	binding := matchingBinding()
	binding.AppID = "hosted_blog"
	binding.RegistrarID = &registrarID
	return binding
}

// newRegistrarFixture is an exchange fixture whose claims are the hosted_ registrar's and whose
// pre-read reports a new name "blog".
func newRegistrarFixture() *exchangeFixture {
	fixture := newExchangeFixture()
	registrar := hostedRegistrar()
	fixture.tx.registrar = registrar
	fixture.store.state = RegistrarExchangeState{
		Registrar: registrar, DerivedAppID: "hosted_blog", Derived: DerivedAppAbsent,
	}
	return fixture
}

func exchangeByName(fixture *exchangeFixture) (ExchangeResult, error) {
	return fixture.service.Exchange(context.Background(), ExchangeInput{Name: "blog", Token: exchangeTestToken})
}

func TestRegistrarExchangeContractRows(testInstance *testing.T) {
	for _, testCase := range []struct {
		name         string
		setup        func(*exchangeFixture)
		wantErr      error
		wantStatus   string
		wantCreated  bool
		wantTxCalls  string
		wantRegister int
		wantTxRuns   int
		wantCommit   bool
	}{
		{
			name:        "a new name is created in the directory, bound and keyed",
			setup:       func(*exchangeFixture) {},
			wantStatus:  ExchangeIssued,
			wantCreated: true,
			wantTxCalls: "record,find-registrar,lock,create-registrar-binding,issue", wantRegister: 1, wantTxRuns: 1, wantCommit: true,
		},
		{
			name: "an app the registrar already created is keyed without the directory",
			setup: func(fixture *exchangeFixture) {
				fixture.store.state.Derived = DerivedAppOwned
				fixture.tx.bound, fixture.tx.binding = true, ownedBinding(9)
			},
			wantStatus:  ExchangeIssued,
			wantTxCalls: "record,find-registrar,lock,issue", wantRegister: 0, wantTxRuns: 1, wantCommit: true,
		},
		{
			name:    "an unconfigured directory is refused with nothing written",
			setup:   func(fixture *exchangeFixture) { fixture.service.registrar = nil },
			wantErr: ErrAppRegistryNotConfigured,
		},
		{
			name:         "a failing directory writes nothing and consumes no jti",
			setup:        func(fixture *exchangeFixture) { fixture.directory.err = errScripted },
			wantErr:      ErrAppRegistryUnavailable,
			wantRegister: 1,
		},
		{
			name:    "the app cap is refused before the directory",
			setup:   func(fixture *exchangeFixture) { fixture.store.state.AppCount = RegistrarAppLimit },
			wantErr: ErrRegistrarAppLimit,
		},
		{
			name:    "an overlapping id is refused before the directory",
			setup:   func(fixture *exchangeFixture) { fixture.store.state.Overlaps = true },
			wantErr: ErrAppIDOverlap,
		},
		{
			name:    "an app bound normally or by another registrar is refused before the directory",
			setup:   func(fixture *exchangeFixture) { fixture.store.state.Derived = DerivedAppForeign },
			wantErr: ErrRegistrarOwnership,
		},
		{
			name:    "a replayed jti found by the pre-read is refused before the directory and the transaction",
			setup:   func(fixture *exchangeFixture) { fixture.store.state.JTIRecorded = true },
			wantErr: ErrReplayed,
		},
		{
			name:    "a derived id that is not a valid app id is refused",
			setup:   func(fixture *exchangeFixture) { fixture.store.readErr = ErrInvalidName },
			wantErr: ErrInvalidName,
		},
		{
			name: "claims that match no registrar file a registrar request",
			setup: func(fixture *exchangeFixture) {
				fixture.store.state = RegistrarExchangeState{}
				fixture.tx.registrar = nil
			},
			wantStatus:  ExchangePending,
			wantTxCalls: "record,find-registrar,has-normal-binding,file-registrar", wantTxRuns: 1, wantCommit: true,
		},
		{
			name: "a normal app's identity in name form is a mismatch and files nothing",
			setup: func(fixture *exchangeFixture) {
				fixture.store.state = RegistrarExchangeState{NormalBinding: true}
				fixture.tx.registrar, fixture.tx.normalBinding = nil, true
			},
			wantErr:     ErrBindingMismatch,
			wantTxCalls: "record,find-registrar,has-normal-binding", wantTxRuns: 1,
		},
		{
			name:        "a replayed jti found inside the transaction rolls back",
			setup:       func(fixture *exchangeFixture) { fixture.tx.recordFresh = false },
			wantErr:     ErrReplayed,
			wantTxCalls: "record", wantRegister: 1, wantTxRuns: 1,
		},
		{
			name: "a registrar request still consumes the jti",
			setup: func(fixture *exchangeFixture) {
				fixture.store.state = RegistrarExchangeState{}
				fixture.tx.registrar = nil
				fixture.tx.requestID = 11
			},
			wantStatus:  ExchangePending,
			wantTxCalls: "record,find-registrar,has-normal-binding,file-registrar", wantTxRuns: 1, wantCommit: true,
		},
		{
			name: "a pending cap on the registrar request is surfaced",
			setup: func(fixture *exchangeFixture) {
				fixture.store.state, fixture.tx.registrar, fixture.tx.fileErr = RegistrarExchangeState{}, nil, ErrPendingCap
			},
			wantErr:     ErrPendingCap,
			wantTxCalls: "record,find-registrar,has-normal-binding,file-registrar", wantTxRuns: 1,
		},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newRegistrarFixture()
			testCase.setup(fixture)

			result, err := exchangeByName(fixture)

			if !errors.Is(err, testCase.wantErr) || (testCase.wantErr == nil && err != nil) {
				subTest.Fatalf("err = %v, want %v", err, testCase.wantErr)
			}
			if testCase.wantErr == ErrReplayed && !errors.Is(err, ErrInvalidToken) {
				subTest.Errorf("a replay must read as an invalid token, got %v", err)
			}
			if testCase.wantErr == nil {
				if result.Status != testCase.wantStatus || result.Created != testCase.wantCreated {
					subTest.Errorf("result = %+v, want status %s created %v", result, testCase.wantStatus, testCase.wantCreated)
				}
				if testCase.wantStatus == ExchangeIssued && (result.AppID != "hosted_blog" || result.Key != exchangeTestKey) {
					subTest.Errorf("result = %+v, want the derived app id and the key", result)
				}
			}
			if got := strings.Join(fixture.tx.calls, ","); got != testCase.wantTxCalls {
				subTest.Errorf("transaction calls = %q, want %q", got, testCase.wantTxCalls)
			}
			if len(fixture.directory.appIDs) != testCase.wantRegister {
				subTest.Errorf("directory calls = %v, want %d", fixture.directory.appIDs, testCase.wantRegister)
			}
			if fixture.store.runs != testCase.wantTxRuns {
				subTest.Errorf("transactions = %d, want %d", fixture.store.runs, testCase.wantTxRuns)
			}
			if fixture.store.committed != testCase.wantCommit {
				subTest.Errorf("committed = %v, want %v", fixture.store.committed, testCase.wantCommit)
			}
			if testCase.wantRegister > 0 && fixture.directory.appIDs[0] != "hosted_blog" {
				subTest.Errorf("directory saw %v, want the derived id as both id and name", fixture.directory.appIDs)
			}
			if fixture.store.readName != "blog" {
				subTest.Errorf("pre-read saw name %q, want blog", fixture.store.readName)
			}
		})
	}
}

func TestRegistrarExchangeCallOrderIsVerifyThenReadThenDirectoryThenTransaction(testInstance *testing.T) {
	fixture := newRegistrarFixture()
	var order []string
	fixture.store.order = &order
	fixture.directory.calls = &order
	fixture.store.onRead = func() {
		order = append(order, "read")
		if fixture.verifier.calls != 1 {
			testInstance.Errorf("the pre-read ran with %d verifier calls, want the token verified first", fixture.verifier.calls)
		}
	}

	if _, err := exchangeByName(fixture); err != nil {
		testInstance.Fatalf("Exchange: %v", err)
	}

	if got := strings.Join(order, ","); got != "read,register,tx" {
		testInstance.Errorf("order = %s, want read,register,tx", got)
	}
}

func TestRegistrarExchangeRaces(testInstance *testing.T) {
	otherRegistrarBinding := ownedBinding(10)
	normalBinding := matchingBinding()
	normalBinding.AppID = "hosted_blog"

	for _, testCase := range []struct {
		name         string
		setup        func(*exchangeFixture)
		wantErr      error
		wantStatus   string
		wantTxCalls  string
		wantRegister int
	}{
		{
			name:         "the registrar vanished before the transaction files a registrar request",
			setup:        func(fixture *exchangeFixture) { fixture.tx.registrar = nil },
			wantStatus:   ExchangePending,
			wantTxCalls:  "record,find-registrar,has-normal-binding,file-registrar",
			wantRegister: 1,
		},
		{
			name:         "the registrar vanished before the binding insert files a registrar request",
			setup:        func(fixture *exchangeFixture) { fixture.tx.createErr = ErrRegistrarNotFound },
			wantStatus:   ExchangePending,
			wantTxCalls:  "record,find-registrar,lock,create-registrar-binding,has-normal-binding,file-registrar",
			wantRegister: 1,
		},
		{
			name: "an id unbound after the pre-read never skips the directory",
			setup: func(fixture *exchangeFixture) {
				fixture.store.state.Derived = DerivedAppOwned
			},
			wantErr:     ErrBindingAppeared,
			wantTxCalls: "record,find-registrar,lock",
		},
		{
			name: "a lost create race on this registrar's own app is keyed after one re-lock",
			setup: func(fixture *exchangeFixture) {
				fixture.tx.createErr = ErrBindingAppeared
				fixture.tx.lockSequence = []lockStep{{}, {binding: ownedBinding(9), bound: true}}
			},
			wantStatus:   ExchangeIssued,
			wantTxCalls:  "record,find-registrar,lock,create-registrar-binding,lock,issue",
			wantRegister: 1,
		},
		{
			name: "a lost create race to another registrar is an ownership refusal",
			setup: func(fixture *exchangeFixture) {
				fixture.tx.createErr = ErrBindingAppeared
				fixture.tx.lockSequence = []lockStep{{}, {binding: otherRegistrarBinding, bound: true}}
			},
			wantErr:      ErrRegistrarOwnership,
			wantTxCalls:  "record,find-registrar,lock,create-registrar-binding,lock",
			wantRegister: 1,
		},
		{
			name: "a lost create race to a normal binding is an ownership refusal",
			setup: func(fixture *exchangeFixture) {
				fixture.tx.createErr = ErrBindingAppeared
				fixture.tx.lockSequence = []lockStep{{}, {binding: normalBinding, bound: true}}
			},
			wantErr:      ErrRegistrarOwnership,
			wantTxCalls:  "record,find-registrar,lock,create-registrar-binding,lock",
			wantRegister: 1,
		},
		{
			name: "a lost create race whose binding is gone again is the retryable error after exactly two locks",
			setup: func(fixture *exchangeFixture) {
				fixture.tx.createErr = ErrBindingAppeared
				fixture.tx.lockSequence = []lockStep{{}, {}}
			},
			wantErr:      ErrBindingAppeared,
			wantTxCalls:  "record,find-registrar,lock,create-registrar-binding,lock",
			wantRegister: 1,
		},
		{
			name:         "a store refusal from the binding insert is returned unchanged",
			setup:        func(fixture *exchangeFixture) { fixture.tx.createErr = ErrRegistrarAppLimit },
			wantErr:      ErrRegistrarAppLimit,
			wantTxCalls:  "record,find-registrar,lock,create-registrar-binding",
			wantRegister: 1,
		},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newRegistrarFixture()
			testCase.setup(fixture)

			result, err := exchangeByName(fixture)

			if !errors.Is(err, testCase.wantErr) || (testCase.wantErr == nil && err != nil) {
				subTest.Fatalf("err = %v, want %v", err, testCase.wantErr)
			}
			if testCase.wantErr == nil && result.Status != testCase.wantStatus {
				subTest.Errorf("status = %s, want %s", result.Status, testCase.wantStatus)
			}
			if got := strings.Join(fixture.tx.calls, ","); got != testCase.wantTxCalls {
				subTest.Errorf("transaction calls = %q, want %q", got, testCase.wantTxCalls)
			}
			if len(fixture.directory.appIDs) != testCase.wantRegister {
				subTest.Errorf("directory calls = %v, want %d", fixture.directory.appIDs, testCase.wantRegister)
			}
			if testCase.wantErr != nil && fixture.store.committed {
				subTest.Error("a refused exchange must roll back")
			}
			if testCase.wantStatus == ExchangeIssued && result.Created {
				subTest.Error("an app another request created must not be reported as created")
			}
		})
	}
}

func TestExchangeTargetAndNameValidationHappenBeforeVerification(testInstance *testing.T) {
	longName := strings.Repeat("a", 100)
	for _, testCase := range []struct {
		name    string
		input   ExchangeInput
		wantErr error
	}{
		{"both app_id and name", ExchangeInput{AppID: "urls4irl", Name: "blog", Token: exchangeTestToken}, ErrExchangeTarget},
		{"neither app_id nor name", ExchangeInput{Token: exchangeTestToken}, ErrExchangeTarget},
		{"uppercase name", ExchangeInput{Name: "Blog", Token: exchangeTestToken}, ErrInvalidName},
		{"name with a dash", ExchangeInput{Name: "my-blog", Token: exchangeTestToken}, ErrInvalidName},
		{"name starting with an underscore", ExchangeInput{Name: "_blog", Token: exchangeTestToken}, ErrInvalidName},
		{"name with a trailing newline", ExchangeInput{Name: "blog\n", Token: exchangeTestToken}, ErrInvalidName},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newRegistrarFixture()

			_, err := fixture.service.Exchange(context.Background(), testCase.input)

			if !errors.Is(err, testCase.wantErr) {
				subTest.Fatalf("err = %v, want %v", err, testCase.wantErr)
			}
			if fixture.verifier.calls != 0 || fixture.store.runs != 0 || len(fixture.directory.appIDs) != 0 {
				subTest.Errorf("verifier calls %d, store runs %d, directory calls %v, want none", fixture.verifier.calls, fixture.store.runs, fixture.directory.appIDs)
			}
		})
	}

	testInstance.Run("a valid name with a missing token", func(subTest *testing.T) {
		fixture := newRegistrarFixture()
		_, err := fixture.service.Exchange(context.Background(), ExchangeInput{Name: "blog"})
		var validationErr *ValidationError
		if !errors.As(err, &validationErr) || fixture.verifier.calls != 0 {
			subTest.Errorf("err = %v, verifier calls = %d, want a ValidationError and no verification", err, fixture.verifier.calls)
		}
	})

	testInstance.Run("a name whose derived id is too long never reaches the directory", func(subTest *testing.T) {
		fixture := newRegistrarFixture()
		fixture.store.readErr = ErrInvalidName
		_, err := fixture.service.Exchange(context.Background(), ExchangeInput{Name: longName, Token: exchangeTestToken})
		if !errors.Is(err, ErrInvalidName) || len(fixture.directory.appIDs) != 0 || fixture.store.runs != 0 {
			subTest.Errorf("err = %v, directory %v, runs %d", err, fixture.directory.appIDs, fixture.store.runs)
		}
	})
}

func TestDeriveAppIDRejectsAnOverlongDerivedID(testInstance *testing.T) {
	// A valid name whose prefix+name is longer than 63 characters is an invalid name.
	if _, err := DeriveAppID("hosted_", strings.Repeat("a", 60)); !errors.Is(err, ErrInvalidName) {
		testInstance.Errorf("DeriveAppID over 63 chars = %v, want ErrInvalidName", err)
	}
	if derived, err := DeriveAppID("hosted_", "blog"); err != nil || derived != "hosted_blog" {
		testInstance.Errorf("DeriveAppID = %q, %v", derived, err)
	}
}

func TestNormalExchangeRefusesRegistrarSpace(testInstance *testing.T) {
	for _, testCase := range []struct {
		name        string
		setup       func(*fakeExchangeTx)
		wantErr     error
		wantTxCalls string
	}{
		{
			name:        "claims equal to a registrar identity are refused with 403 semantics",
			setup:       func(tx *fakeExchangeTx) { tx.registrar = hostedRegistrar() },
			wantErr:     ErrBindingMismatch,
			wantTxCalls: "record,find-registrar",
		},
		{
			name:        "claims equal to a registrar identity win over an in-prefix app id",
			setup:       func(tx *fakeExchangeTx) { tx.registrar, tx.inPrefix = hostedRegistrar(), true },
			wantErr:     ErrBindingMismatch,
			wantTxCalls: "record,find-registrar",
		},
		{
			name:        "an app id inside a registrar prefix is refused with 409 semantics and nothing is filed",
			setup:       func(tx *fakeExchangeTx) { tx.inPrefix = true },
			wantErr:     ErrAppIDInRegistrarPrefix,
			wantTxCalls: "record,find-registrar,in-prefix",
		},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newExchangeFixture()
			testCase.setup(fixture.tx)

			_, err := fixture.service.Exchange(context.Background(), ExchangeInput{AppID: "hosted_blog", Token: exchangeTestToken})

			if !errors.Is(err, testCase.wantErr) {
				subTest.Fatalf("err = %v, want %v", err, testCase.wantErr)
			}
			if got := strings.Join(fixture.tx.calls, ","); got != testCase.wantTxCalls {
				subTest.Errorf("transaction calls = %q, want %q", got, testCase.wantTxCalls)
			}
			if fixture.store.committed || len(fixture.directory.appIDs) != 0 {
				subTest.Error("a refusal must roll back and never reach the directory")
			}
		})
	}
}

func TestRegistrarExchangeLogsItsOutcomeLabelAndNeverTheTokenOrKey(testInstance *testing.T) {
	for _, testCase := range []struct {
		name        string
		setup       func(*exchangeFixture)
		wantOutcome string
		wantFields  []string
	}{
		{"created", func(*exchangeFixture) {}, "outcome=registrar_created",
			[]string{"registrar_prefix=hosted_", "name=blog", "app_id=hosted_blog"}},
		{"issued", func(fixture *exchangeFixture) {
			fixture.store.state.Derived = DerivedAppOwned
			fixture.tx.bound, fixture.tx.binding = true, ownedBinding(9)
		}, "outcome=registrar_issued", []string{"registrar_prefix=hosted_", "name=blog", "app_id=hosted_blog"}},
		{"pending", func(fixture *exchangeFixture) {
			fixture.store.state = RegistrarExchangeState{}
			fixture.tx.registrar = nil
		}, "outcome=registrar_pending", []string{"name=blog"}},
		{"denied", func(fixture *exchangeFixture) { fixture.store.state.Derived = DerivedAppForeign },
			"outcome=registrar_denied", []string{"registrar_prefix=hosted_", "name=blog", "run_id=run-77"}},
		{"cap", func(fixture *exchangeFixture) { fixture.store.state.AppCount = RegistrarAppLimit },
			"outcome=registrar_cap", []string{"registrar_prefix=hosted_", "name=blog"}},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newRegistrarFixture()
			testCase.setup(fixture)

			_, _ = exchangeByName(fixture)

			logs := fixture.logs.String()
			for _, want := range append([]string{testCase.wantOutcome, "app identity exchange", "duration="}, testCase.wantFields...) {
				if !strings.Contains(logs, want) {
					subTest.Errorf("log is missing %q:\n%s", want, logs)
				}
			}
			if strings.Count(logs, "\n") != 1 {
				subTest.Errorf("want exactly one log line, got:\n%s", logs)
			}
			for _, secret := range []string{exchangeTestToken, "SECRET-TOKEN-TEXT", exchangeTestKey, "abcdefghijklmnop"} {
				if strings.Contains(logs, secret) {
					subTest.Errorf("log leaks %q:\n%s", secret, logs)
				}
			}
		})
	}
}

func TestRegistrarExchangeTruncatesALoggedName(testInstance *testing.T) {
	fixture := newRegistrarFixture()
	longName := strings.Repeat("a", 200)
	fixture.store.readErr = ErrInvalidName

	_, _ = fixture.service.Exchange(context.Background(), ExchangeInput{Name: longName, Token: exchangeTestToken})

	if logs := fixture.logs.String(); strings.Contains(logs, longName) || !strings.Contains(logs, "name=") {
		testInstance.Errorf("the logged name must be bounded:\n%s", logs)
	}
}

func TestApproveRegistrarRequestRefusesBadPrefixesWithoutAnyCall(testInstance *testing.T) {
	for _, prefix := range []string{"platform_", "platform_eu_", "everyone_", "", "x", "Hosted_", "hosted", "_hosted_", "has-dash_"} {
		testInstance.Run(prefix, func(subTest *testing.T) {
			var calls []string
			admin := &fakeAdminStore{calls: &calls}
			directory := &fakeRegistrar{calls: &calls}
			service := NewService(ServiceConfig{Admin: admin, Registrar: directory, Logger: textLogger(&syncBuffer{})})

			_, err := service.ApproveRegistrarRequest(context.Background(), 4, prefix)

			if !errors.Is(err, ErrInvalidPrefix) {
				subTest.Errorf("err = %v, want ErrInvalidPrefix", err)
			}
			if len(calls) != 0 {
				subTest.Errorf("calls = %v, want no store and no person-service call", calls)
			}
		})
	}
}

func TestRegistrarAdminUseCasesLogTheirDecision(testInstance *testing.T) {
	registrarRow := *hostedRegistrar()
	newService := func(admin *fakeAdminStore) (*Service, *syncBuffer) {
		logs := &syncBuffer{}
		return NewService(ServiceConfig{Admin: admin, Logger: textLogger(logs), Now: func() time.Time { return testNow }}), logs
	}
	requireLog := func(subTest *testing.T, logs *syncBuffer, wants ...string) {
		subTest.Helper()
		for _, want := range wants {
			if !strings.Contains(logs.String(), want) {
				subTest.Errorf("log is missing %q:\n%s", want, logs.String())
			}
		}
	}

	testInstance.Run("approve registrar", func(subTest *testing.T) {
		admin := &fakeAdminStore{registrarRow: registrarRow}
		service, logs := newService(admin)
		registrar, err := service.ApproveRegistrarRequest(context.Background(), 4, "hosted_")
		if err != nil || registrar.Prefix != "hosted_" || admin.approvedPrefix != "hosted_" {
			subTest.Fatalf("ApproveRegistrarRequest = %+v, %v (store saw %q)", registrar, err, admin.approvedPrefix)
		}
		requireLog(subTest, logs, "app identity registrar approved", "prefix=hosted_", "repository="+testRepository, "request_id=4")
	})

	testInstance.Run("approve registrar store error is not logged as a success", func(subTest *testing.T) {
		service, logs := newService(&fakeAdminStore{err: ErrPrefixTaken})
		if _, err := service.ApproveRegistrarRequest(context.Background(), 4, "hosted_"); !errors.Is(err, ErrPrefixTaken) {
			subTest.Errorf("err = %v, want ErrPrefixTaken", err)
		}
		if logs.String() != "" {
			subTest.Errorf("a refused approval must not log:\n%s", logs.String())
		}
	})

	testInstance.Run("list registrars", func(subTest *testing.T) {
		service, _ := newService(&fakeAdminStore{registrarRow: registrarRow})
		registrars, err := service.ListRegistrars(context.Background())
		if err != nil || len(registrars) != 1 {
			subTest.Errorf("ListRegistrars = %v, %v", registrars, err)
		}
	})

	testInstance.Run("delete registrar", func(subTest *testing.T) {
		service, logs := newService(&fakeAdminStore{registrarRow: registrarRow, registrarDeleted: true, unboundApps: 3})
		deleted, unbound, err := service.DeleteRegistrar(context.Background(), 9)
		if err != nil || !deleted || unbound != 3 {
			subTest.Fatalf("DeleteRegistrar = %v, %d, %v", deleted, unbound, err)
		}
		requireLog(subTest, logs, "app identity registrar deleted", "prefix=hosted_", "repository="+testRepository, "unbound_apps=3")
	})

	testInstance.Run("delete unknown registrar is idempotent and not logged", func(subTest *testing.T) {
		service, logs := newService(&fakeAdminStore{})
		deleted, unbound, err := service.DeleteRegistrar(context.Background(), 9)
		if err != nil || deleted || unbound != 0 {
			subTest.Fatalf("DeleteRegistrar = %v, %d, %v, want false, 0, nil", deleted, unbound, err)
		}
		if logs.String() != "" {
			subTest.Errorf("an unknown id must not log:\n%s", logs.String())
		}
	})

	testInstance.Run("acknowledge binding", func(subTest *testing.T) {
		admin := &fakeAdminStore{}
		service, logs := newService(admin)
		if _, err := service.AcknowledgeBinding(context.Background(), "hosted_blog"); err != nil {
			subTest.Fatalf("AcknowledgeBinding: %v", err)
		}
		requireLog(subTest, logs, "binding acknowledged", "app_id=hosted_blog")
		if !slices.Equal(admin.acknowledgedApps, []string{"hosted_blog"}) {
			subTest.Errorf("store saw %v", admin.acknowledgedApps)
		}
		var validationErr *ValidationError
		if _, err := service.AcknowledgeBinding(context.Background(), "Not Valid"); !errors.As(err, &validationErr) {
			subTest.Errorf("err = %v, want a ValidationError", err)
		}
	})

	testInstance.Run("acknowledge all drops duplicates and counts requested and changed", func(subTest *testing.T) {
		admin := &fakeAdminStore{acknowledgedCount: 1}
		service, logs := newService(admin)
		changed, err := service.AcknowledgeAllBindings(context.Background(), []string{"hosted_a", "hosted_b", "hosted_a"})
		if err != nil || changed != 1 {
			subTest.Fatalf("AcknowledgeAllBindings = %d, %v", changed, err)
		}
		if !slices.Equal(admin.acknowledgedApps, []string{"hosted_a", "hosted_b"}) {
			subTest.Errorf("store saw %v, want the de-duplicated list", admin.acknowledgedApps)
		}
		requireLog(subTest, logs, "bindings acknowledged all", "requested=2", "changed=1")
	})

	testInstance.Run("acknowledge all validates the list", func(subTest *testing.T) {
		tooMany := make([]string, MaxAcknowledgeApps+1)
		for index := range tooMany {
			tooMany[index] = "hosted_a"
		}
		exactlyMax := make([]string, MaxAcknowledgeApps)
		for index := range exactlyMax {
			exactlyMax[index] = "hosted_a"
		}
		for _, testCase := range []struct {
			name    string
			appIDs  []string
			message string
			wantOK  bool
		}{
			{"empty", nil, "app_ids must list 1 to 200 apps", false},
			{"more than 200 raw entries even when duplicates", tooMany, "app_ids must list 1 to 200 apps", false},
			{"exactly 200 duplicates is allowed", exactlyMax, "", true},
			{"invalid id", []string{"hosted_a", "Bad Id"}, "", false},
		} {
			admin := &fakeAdminStore{}
			service, _ := newService(admin)
			_, err := service.AcknowledgeAllBindings(context.Background(), testCase.appIDs)
			var validationErr *ValidationError
			switch {
			case testCase.wantOK:
				if err != nil {
					subTest.Errorf("%s: err = %v", testCase.name, err)
				}
			case !errors.As(err, &validationErr) || (testCase.message != "" && validationErr.Message != testCase.message):
				subTest.Errorf("%s: err = %v, want a ValidationError %q", testCase.name, err, testCase.message)
			case admin.acknowledgedApps != nil:
				subTest.Errorf("%s: the store was called for an invalid list", testCase.name)
			}
		}
	})
}
