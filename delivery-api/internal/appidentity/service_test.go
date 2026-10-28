package appidentity

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

const (
	exchangeTestToken = "header.payload.signature-SECRET-TOKEN-TEXT"
	exchangeTestKey   = "dak_" + "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
)

var errScripted = errors.New("scripted failure")

// fakeVerifier is a scripted TokenVerifier. It records the recorder it was handed.
type fakeVerifier struct {
	claims   Claims
	err      error
	calls    int
	replay   ReplayRecorder
	rawToken string
}

func (fake *fakeVerifier) Verify(_ context.Context, rawToken string, replay ReplayRecorder) (Claims, error) {
	fake.calls++
	fake.rawToken = rawToken
	fake.replay = replay
	return fake.claims, fake.err
}

// fakeExchangeTx is a scripted ExchangeTx that records every call in order.
type fakeExchangeTx struct {
	calls []string

	recordFresh bool
	recordErr   error
	recordedJTI string
	recordedAt  time.Time

	binding      Binding
	bound        bool
	lockErr      error
	lockSequence []lockStep

	requestID int64
	fileErr   error
	filedAt   time.Time

	issued   IssuedKey
	issueErr error
	issuedAt time.Time
	runID    string
}

func (fake *fakeExchangeTx) RecordJTI(_ context.Context, jti string, expiresAt time.Time) (bool, error) {
	fake.calls = append(fake.calls, "record")
	fake.recordedJTI, fake.recordedAt = jti, expiresAt
	return fake.recordFresh, fake.recordErr
}

func (fake *fakeExchangeTx) LockBinding(context.Context, string) (Binding, bool, error) {
	fake.calls = append(fake.calls, "lock")
	// A scripted sequence answers the first calls in order, then the plain fields answer.
	if len(fake.lockSequence) > 0 {
		step := fake.lockSequence[0]
		fake.lockSequence = fake.lockSequence[1:]
		return step.binding, step.bound, fake.lockErr
	}
	return fake.binding, fake.bound, fake.lockErr
}

// lockStep is one scripted LockBinding answer.
type lockStep struct {
	binding Binding
	bound   bool
}

func (fake *fakeExchangeTx) FilePendingRequest(_ context.Context, _ string, _ Claims, now time.Time) (int64, error) {
	fake.calls = append(fake.calls, "file")
	fake.filedAt = now
	return fake.requestID, fake.fileErr
}

func (fake *fakeExchangeTx) IssueKey(_ context.Context, _ string, githubRunID string, now time.Time) (IssuedKey, error) {
	fake.calls = append(fake.calls, "issue")
	fake.issuedAt, fake.runID = now, githubRunID
	return fake.issued, fake.issueErr
}

// fakeExchangeStore runs the exchange function against fakeExchangeTx and reports whether the
// "transaction" committed (the function returned nil).
type fakeExchangeStore struct {
	tx        *fakeExchangeTx
	runs      int
	committed bool
	beginErr  error
}

func (fake *fakeExchangeStore) Exchange(_ context.Context, fn func(tx ExchangeTx) error) error {
	fake.runs++
	if fake.beginErr != nil {
		return fake.beginErr
	}
	fnErr := fn(fake.tx)
	fake.committed = fnErr == nil
	return fnErr
}

func (fake *fakeExchangeStore) ResolveKey(context.Context, string, time.Time) (ResolvedKey, error) {
	return ResolvedKey{}, errors.New("not used by the exchange")
}

func (fake *fakeExchangeStore) TouchKey(context.Context, int64, time.Time) (bool, error) {
	return false, errors.New("not used by the exchange")
}

func (fake *fakeExchangeStore) DeleteExpiredReplay(context.Context, time.Time) (int64, error) {
	return 0, errors.New("not used by the exchange")
}

type exchangeFixture struct {
	service  *Service
	verifier *fakeVerifier
	store    *fakeExchangeStore
	tx       *fakeExchangeTx
	logs     *syncBuffer
}

func newExchangeFixture() *exchangeFixture {
	claims := testClaimsFor(1001, "")
	tx := &fakeExchangeTx{recordFresh: true, requestID: 7, issued: IssuedKey{ID: 5, Plaintext: exchangeTestKey, Hint: "dak_abcd"}}
	fixture := &exchangeFixture{
		verifier: &fakeVerifier{claims: claims},
		store:    &fakeExchangeStore{tx: tx},
		tx:       tx,
		logs:     &syncBuffer{},
	}
	fixture.service = NewService(ServiceConfig{
		Store:    fixture.store,
		Admin:    nil,
		Verifier: fixture.verifier,
		Logger:   textLogger(fixture.logs),
		Now:      func() time.Time { return testNow },
	})
	return fixture
}

// testClaimsFor returns verified claims for the test workflow.
func testClaimsFor(repositoryID int64, environment string) Claims {
	return Claims{
		Repository: testRepository, RepositoryID: repositoryID, RepositoryOwnerID: 678,
		WorkflowPath: testWorkflow, Ref: "refs/heads/main", Environment: environment,
		RunID: "run-77", JTI: "jti-77", ExpiresAt: testNow.Add(10 * time.Minute),
	}
}

func matchingBinding() Binding {
	return Binding{
		AppID: "urls4irl", Repository: testRepository, RepositoryID: 1001, RepositoryOwnerID: 678,
		WorkflowPath: testWorkflow, Ref: "refs/heads/main", ApprovedAt: testNow.Add(-time.Hour),
	}
}

func TestExchangeOfAnUnboundAppFilesAPendingRequestAndIssuesNoKey(testInstance *testing.T) {
	fixture := newExchangeFixture()

	result, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken)
	if err != nil {
		testInstance.Fatalf("Exchange: %v", err)
	}

	if result.Status != ExchangePending || result.RequestID != 7 || result.Key != "" || result.KeyID != 0 {
		testInstance.Errorf("result = %+v, want a pending result for request 7 with no key", result)
	}
	if got := strings.Join(fixture.tx.calls, ","); got != "record,lock,file" {
		testInstance.Errorf("transaction calls = %s, want record,lock,file (and never issue)", got)
	}
	if !fixture.store.committed {
		testInstance.Error("the pending request must commit (the token is consumed)")
	}
	if !fixture.tx.filedAt.Equal(testNow) {
		testInstance.Errorf("request filed at %v, want the service clock %v", fixture.tx.filedAt, testNow)
	}
}

func TestExchangeRecordsTheJTIInsideTheTransactionAfterVerifyingWithoutRecording(testInstance *testing.T) {
	fixture := newExchangeFixture()
	fixture.tx.bound, fixture.tx.binding = true, matchingBinding()

	if _, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken); err != nil {
		testInstance.Fatalf("Exchange: %v", err)
	}

	if fixture.store.runs != 1 || fixture.tx.recordedJTI != "jti-77" {
		testInstance.Errorf("store runs = %d, recorded jti = %q, want one transaction recording jti-77", fixture.store.runs, fixture.tx.recordedJTI)
	}
	wantExpiry := fixture.verifier.claims.ReplayExpiry()
	if !fixture.tx.recordedAt.Equal(wantExpiry) {
		testInstance.Errorf("jti kept until %v, want token expiry + leeway %v", fixture.tx.recordedAt, wantExpiry)
	}
	// The verifier must not consume the jti itself: a recorder that said "replay" or hit the
	// database outside the transaction would break the same-transaction guarantee.
	fresh, recordErr := fixture.verifier.replay.RecordJTI(context.Background(), "anything", testNow)
	if recordErr != nil || !fresh {
		testInstance.Errorf("verification recorder = (%v, %v), want a recorder that never records or refuses", fresh, recordErr)
	}
}

func TestExchangeIssuesAKeyWhenTheClaimsMatchTheBinding(testInstance *testing.T) {
	fixture := newExchangeFixture()
	fixture.tx.bound, fixture.tx.binding = true, matchingBinding()

	result, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken)
	if err != nil {
		testInstance.Fatalf("Exchange: %v", err)
	}

	if result.Status != ExchangeIssued || result.AppID != "urls4irl" || result.Key != exchangeTestKey || result.KeyID != 5 {
		testInstance.Errorf("result = %+v", result)
	}
	if got := strings.Join(fixture.tx.calls, ","); got != "record,lock,issue" {
		testInstance.Errorf("transaction calls = %s, want record,lock,issue (and never file)", got)
	}
	if fixture.tx.runID != "run-77" || !fixture.tx.issuedAt.Equal(testNow) {
		testInstance.Errorf("key issued with run id %q at %v", fixture.tx.runID, fixture.tx.issuedAt)
	}
	if !fixture.store.committed {
		testInstance.Error("the issue must commit")
	}
	if fixture.verifier.rawToken != exchangeTestToken {
		testInstance.Errorf("verifier saw token %q, want the request's token", fixture.verifier.rawToken)
	}
}

func TestExchangeRereadsTheBindingWhenAnApprovalCommitsWhileFiling(testInstance *testing.T) {
	appeared := lockStep{binding: matchingBinding(), bound: true}
	for _, testCase := range []struct {
		name      string
		mutate    func(*Claims)
		wantErr   error
		wantCalls string
	}{
		{"matching claims are issued a key", func(*Claims) {}, nil, "record,lock,file,lock,issue"},
		{"claims differing from the new binding are refused and nothing is filed",
			func(claims *Claims) { claims.Ref = "refs/heads/other" }, ErrBindingMismatch, "record,lock,file,lock"},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newExchangeFixture()
			fixture.tx.lockSequence = []lockStep{{}, appeared}
			fixture.tx.fileErr = ErrBindingAppeared
			testCase.mutate(&fixture.verifier.claims)

			result, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken)

			if !errors.Is(err, testCase.wantErr) {
				subTest.Fatalf("err = %v, want %v", err, testCase.wantErr)
			}
			if got := strings.Join(fixture.tx.calls, ","); got != testCase.wantCalls {
				subTest.Errorf("transaction calls = %s, want %s", got, testCase.wantCalls)
			}
			if testCase.wantErr == nil && (result.Status != ExchangeIssued || result.Key != exchangeTestKey) {
				subTest.Errorf("result = %+v, want an issued key", result)
			}
			if testCase.wantErr != nil && fixture.store.committed {
				subTest.Error("a refused exchange must roll back")
			}
		})
	}
}

func TestExchangeGivesUpWhenTheBindingStillCannotBeFound(testInstance *testing.T) {
	fixture := newExchangeFixture()
	fixture.tx.fileErr = ErrBindingAppeared

	_, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken)

	if !errors.Is(err, ErrBindingAppeared) {
		testInstance.Fatalf("err = %v, want ErrBindingAppeared (no retry loop)", err)
	}
	if got := strings.Join(fixture.tx.calls, ","); got != "record,lock,file,lock" {
		testInstance.Errorf("transaction calls = %s, want exactly one re-read", got)
	}
}

func TestExchangeReturnsATransactionThatCouldNotStart(testInstance *testing.T) {
	fixture := newExchangeFixture()
	fixture.store.beginErr = errScripted

	_, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken)

	if !errors.Is(err, errScripted) || len(fixture.tx.calls) != 0 {
		testInstance.Errorf("err = %v, transaction calls = %v, want the begin error and no calls", err, fixture.tx.calls)
	}
	if logs := fixture.logs.String(); !strings.Contains(logs, "outcome=error") {
		testInstance.Errorf("a failed begin must be logged as an error outcome:\n%s", logs)
	}
}

func TestExchangeLogsACanceledRequestBelowErrorLevel(testInstance *testing.T) {
	fixture := newExchangeFixture()
	fixture.verifier.err = context.Canceled

	_, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken)

	if !errors.Is(err, context.Canceled) {
		testInstance.Fatalf("err = %v", err)
	}
	if logs := fixture.logs.String(); !strings.Contains(logs, "level=DEBUG") || !strings.Contains(logs, "outcome=canceled") || strings.Contains(logs, "level=ERROR") {
		testInstance.Errorf("a client hang-up must log at debug, not error:\n%s", logs)
	}
}

func TestExchangeRefusesClaimsThatDifferFromTheBindingInAnyOfTheFiveFields(testInstance *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*Claims)
	}{
		{"repository id", func(claims *Claims) { claims.RepositoryID = 2002 }},
		{"owner id", func(claims *Claims) { claims.RepositoryOwnerID = 999 }},
		{"workflow path", func(claims *Claims) { claims.WorkflowPath = ".github/workflows/other.yml" }},
		{"ref", func(claims *Claims) { claims.Ref = "refs/heads/feature" }},
		{"environment", func(claims *Claims) { claims.Environment = "production" }},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newExchangeFixture()
			fixture.tx.bound, fixture.tx.binding = true, matchingBinding()
			testCase.mutate(&fixture.verifier.claims)

			_, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken)

			if !errors.Is(err, ErrBindingMismatch) {
				subTest.Fatalf("err = %v, want ErrBindingMismatch", err)
			}
			if got := strings.Join(fixture.tx.calls, ","); got != "record,lock" {
				subTest.Errorf("transaction calls = %s, want record,lock only: nothing may be filed or issued", got)
			}
			if fixture.store.committed {
				subTest.Error("a mismatch must roll the transaction back")
			}
		})
	}
}

func TestExchangeOfAReplayedTokenIsAnInvalidTokenAndRollsBack(testInstance *testing.T) {
	fixture := newExchangeFixture()
	fixture.tx.recordFresh = false

	_, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken)

	if !errors.Is(err, ErrInvalidToken) || !errors.Is(err, ErrReplayed) {
		testInstance.Fatalf("err = %v, want ErrReplayed (an ErrInvalidToken)", err)
	}
	if got := strings.Join(fixture.tx.calls, ","); got != "record" {
		testInstance.Errorf("transaction calls = %s, want only record: a replay must stop before the binding is read", got)
	}
}

func TestExchangeTokenFailuresNeverReachTheDatabase(testInstance *testing.T) {
	for _, testCase := range []struct {
		name string
		err  error
	}{
		{"invalid token", fmt.Errorf("%w: token signature is invalid", ErrInvalidToken)},
		{"keys unavailable", ErrKeysUnavailable},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newExchangeFixture()
			fixture.verifier.err = testCase.err

			_, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken)

			if !errors.Is(err, testCase.err) {
				subTest.Errorf("err = %v, want %v", err, testCase.err)
			}
			if fixture.store.runs != 0 {
				subTest.Errorf("store ran %d times, want 0: nothing is written for a token that failed verification", fixture.store.runs)
			}
		})
	}
}

func TestExchangeMapsStoreOutcomes(testInstance *testing.T) {
	for _, testCase := range []struct {
		name  string
		setup func(*fakeExchangeTx)
		want  error
	}{
		{"pending cap", func(tx *fakeExchangeTx) { tx.fileErr = ErrPendingCap }, ErrPendingCap},
		{"record failure", func(tx *fakeExchangeTx) { tx.recordErr = errScripted }, errScripted},
		{"lock failure", func(tx *fakeExchangeTx) { tx.lockErr = errScripted }, errScripted},
		{"issue failure", func(tx *fakeExchangeTx) {
			tx.bound, tx.binding, tx.issueErr = true, matchingBinding(), errScripted
		}, errScripted},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newExchangeFixture()
			testCase.setup(fixture.tx)

			_, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken)

			if !errors.Is(err, testCase.want) {
				subTest.Errorf("err = %v, want %v", err, testCase.want)
			}
			if fixture.store.committed {
				subTest.Error("a failed exchange must roll back")
			}
		})
	}
}

func TestExchangeRejectsAMalformedAppIDOrMissingTokenBeforeVerifying(testInstance *testing.T) {
	for _, testCase := range []struct {
		name    string
		appID   string
		token   string
		message string
	}{
		{"empty app id", "", exchangeTestToken, "invalid app_id"},
		{"uppercase app id", "Urls4irl", exchangeTestToken, "invalid app_id"},
		{"app id with a dash", "urls-4irl", exchangeTestToken, "invalid app_id"},
		{"overlong app id", "a" + strings.Repeat("b", 63), exchangeTestToken, "invalid app_id"},
		{"app id with a trailing newline", "urls4irl\n", exchangeTestToken, "invalid app_id"},
		{"missing token", "urls4irl", "", "token is required"},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newExchangeFixture()

			_, err := fixture.service.Exchange(context.Background(), testCase.appID, testCase.token)

			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || validationErr.Message != testCase.message {
				subTest.Fatalf("err = %v, want a ValidationError %q", err, testCase.message)
			}
			if fixture.verifier.calls != 0 || fixture.store.runs != 0 {
				subTest.Errorf("verifier calls = %d, store runs = %d, want 0 and 0", fixture.verifier.calls, fixture.store.runs)
			}
		})
	}
}

func TestExchangeLogsOneLinePerExchangeWithoutTheTokenOrKey(testInstance *testing.T) {
	fixture := newExchangeFixture()
	fixture.tx.bound, fixture.tx.binding = true, matchingBinding()

	if _, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken); err != nil {
		testInstance.Fatalf("Exchange: %v", err)
	}

	logs := fixture.logs.String()
	for _, want := range []string{
		"app identity exchange", "app_id=urls4irl", "repository=4IRL/urls4irl", "workflow_path=.github/workflows/prod-deploy.yml",
		"ref=refs/heads/main", "run_id=run-77", "outcome=issued", "duration=",
	} {
		if !strings.Contains(logs, want) {
			testInstance.Errorf("log is missing %q:\n%s", want, logs)
		}
	}
	if strings.Count(logs, "\n") != 1 {
		testInstance.Errorf("want exactly one log line per exchange, got:\n%s", logs)
	}
	for _, secret := range []string{exchangeTestToken, "SECRET-TOKEN-TEXT", exchangeTestKey, "abcdefghijklmnop"} {
		if strings.Contains(logs, secret) {
			testInstance.Errorf("log leaks %q:\n%s", secret, logs)
		}
	}
}

func TestExchangeLogsEachFailureOutcomeWithItsReason(testInstance *testing.T) {
	for _, testCase := range []struct {
		name        string
		setup       func(*exchangeFixture)
		wantOutcome string
		wantReason  string
		wantClaims  bool
	}{
		{"invalid token", func(fixture *exchangeFixture) {
			fixture.verifier.err = fmt.Errorf("%w: token signature is invalid", ErrInvalidToken)
		}, "outcome=invalid_token", "token signature is invalid", false},
		{"keys unavailable", func(fixture *exchangeFixture) { fixture.verifier.err = ErrKeysUnavailable },
			"outcome=keys_unavailable", "signing keys unavailable", false},
		{"replay", func(fixture *exchangeFixture) { fixture.tx.recordFresh = false },
			"outcome=invalid_token", "jti already used", true},
		{"mismatch", func(fixture *exchangeFixture) {
			fixture.tx.bound, fixture.tx.binding = true, matchingBinding()
			fixture.verifier.claims.Ref = "refs/heads/other"
		}, "outcome=mismatch", "does not match", true},
		{"cap", func(fixture *exchangeFixture) { fixture.tx.fileErr = ErrPendingCap },
			"outcome=pending_cap", "too many pending", true},
		{"store failure", func(fixture *exchangeFixture) { fixture.tx.lockErr = errScripted },
			"outcome=error", "scripted failure", true},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			fixture := newExchangeFixture()
			testCase.setup(fixture)

			_, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken)
			if err == nil {
				subTest.Fatal("want an error")
			}

			logs := fixture.logs.String()
			for _, want := range []string{"app_id=urls4irl", testCase.wantOutcome, testCase.wantReason, "duration="} {
				if !strings.Contains(logs, want) {
					subTest.Errorf("log is missing %q:\n%s", want, logs)
				}
			}
			if testCase.wantClaims && !strings.Contains(logs, "run_id=run-77") {
				subTest.Errorf("a failure after verification must log the verified claims:\n%s", logs)
			}
			if strings.Contains(logs, exchangeTestToken) {
				subTest.Errorf("log leaks the token:\n%s", logs)
			}
		})
	}
}

func TestLoggedReasonTruncatesOnARuneBoundary(testInstance *testing.T) {
	// 3-byte runes: byte 300 falls inside one for any offset that is not a multiple of 3.
	for _, prefix := range []string{"", "a", "ab"} {
		reason := loggedReason(fmt.Errorf("%s%s", prefix, strings.Repeat("€", 200)))

		if !utf8.ValidString(reason) {
			testInstance.Errorf("prefix %q: truncated reason is not valid UTF-8", prefix)
		}
		if !strings.HasSuffix(reason, "...") || len(reason) > maxLoggedReasonChars+len("...") {
			testInstance.Errorf("prefix %q: reason length %d, want a bounded value ending in ...", prefix, len(reason))
		}
	}
	if short := loggedReason(errScripted); short != errScripted.Error() {
		testInstance.Errorf("a short reason must pass through unchanged, got %q", short)
	}
}

func TestExchangeBoundsALoggedReason(testInstance *testing.T) {
	fixture := newExchangeFixture()
	fixture.verifier.err = fmt.Errorf("%w: unknown kid %s", ErrInvalidToken, strings.Repeat("k", 5000))

	if _, err := fixture.service.Exchange(context.Background(), "urls4irl", exchangeTestToken); err == nil {
		testInstance.Fatal("want an error")
	}

	if length := len(fixture.logs.String()); length > 1000 {
		testInstance.Errorf("log line is %d bytes: a hostile kid must not control log volume", length)
	}
}

func TestValidateAppID(testInstance *testing.T) {
	for _, valid := range []string{"a", "urls4irl", "chores_4irl", "x_", strings.Repeat("a", 63)} {
		if err := ValidateAppID(valid); err != nil {
			testInstance.Errorf("ValidateAppID(%q) = %v, want nil", valid, err)
		}
	}
	for _, invalid := range []string{"", "_leading", "Upper", "has-dash", "has space", "x\n", strings.Repeat("a", 64), "é"} {
		if err := ValidateAppID(invalid); err == nil {
			testInstance.Errorf("ValidateAppID(%q) = nil, want an error", invalid)
		}
	}
}

func TestStoredKeyDerivesItsOperatorView(testInstance *testing.T) {
	hourAgo := testNow.Add(-time.Hour)
	dayAndHourAgo := testNow.Add(-SupersededGrace - time.Hour)
	operator := RevokedReasonOperator

	for _, testCase := range []struct {
		name          string
		stored        storedKey
		wantState     KeyState
		wantReason    string
		wantRevokedAt *time.Time
	}{
		{"active", storedKey{}, KeyActive, "", nil},
		{"superseded inside the grace window", storedKey{SupersededAt: &hourAgo}, KeySuperseded, "", nil},
		{"superseded past the grace window reads as revoked with reason superseded", storedKey{SupersededAt: &dayAndHourAgo},
			KeyRevoked, RevokedReasonSuperseded, timePointer(dayAndHourAgo.Add(SupersededGrace))},
		{"revoked by the operator", storedKey{RevokedAt: &hourAgo, RevokedReason: &operator},
			KeyRevoked, RevokedReasonOperator, &hourAgo},
		{"revoked with no stored reason defaults to operator", storedKey{RevokedAt: &hourAgo},
			KeyRevoked, RevokedReasonOperator, &hourAgo},
		{"revoked and superseded keeps the revoke", storedKey{SupersededAt: &dayAndHourAgo, RevokedAt: &hourAgo, RevokedReason: &operator},
			KeyRevoked, RevokedReasonOperator, &hourAgo},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			key := testCase.stored.toKey(testNow)

			if key.State != testCase.wantState || key.RevokedReason != testCase.wantReason {
				subTest.Errorf("state = %q reason = %q, want %q and %q", key.State, key.RevokedReason, testCase.wantState, testCase.wantReason)
			}
			switch {
			case testCase.wantRevokedAt == nil && key.RevokedAt != nil:
				subTest.Errorf("RevokedAt = %v, want nil", key.RevokedAt)
			case testCase.wantRevokedAt != nil && (key.RevokedAt == nil || !key.RevokedAt.Equal(*testCase.wantRevokedAt)):
				subTest.Errorf("RevokedAt = %v, want %v", key.RevokedAt, testCase.wantRevokedAt)
			}
		})
	}
}

func timePointer(value time.Time) *time.Time { return &value }

// fakeAdminStore is a scripted AdminStore that records the time each call was made at.
type fakeAdminStore struct {
	now     time.Time
	err     error
	binding Binding
	request Request
	key     Key
	deleted bool
	unbound string

	// getErr is what GetRequest returns; approveErr is what ApproveRequest returns (err when nil).
	getErr     error
	approveErr error
	// calls records the order of GetRequest and ApproveRequest; approvedAppID is the
	// expectedAppID the store was asked to approve under.
	calls         *[]string
	approvedAppID string
}

func (fake *fakeAdminStore) record(call string) {
	if fake.calls != nil {
		*fake.calls = append(*fake.calls, call)
	}
}

func (fake *fakeAdminStore) GetRequest(_ context.Context, _ int64) (Request, error) {
	fake.record("get")
	if fake.getErr != nil {
		return Request{}, fake.getErr
	}
	request := fake.request
	if request.Status == "" {
		request.Status = RequestPending
	}
	return request, nil
}

// fakeRegistrar is a scripted AppRegistrar that records each call.
type fakeRegistrar struct {
	err         error
	appIDs      []string
	displayName []string
	calls       *[]string
	// deadline and hasDeadline are the deadline of the context the last CreateApp received.
	deadline    time.Time
	hasDeadline bool
}

func (fake *fakeRegistrar) CreateApp(ctx context.Context, appID string, displayName string) error {
	if fake.calls != nil {
		*fake.calls = append(*fake.calls, "register")
	}
	fake.deadline, fake.hasDeadline = ctx.Deadline()
	fake.appIDs = append(fake.appIDs, appID)
	fake.displayName = append(fake.displayName, displayName)
	return fake.err
}

func (fake *fakeAdminStore) ListRequests(_ context.Context, now time.Time) ([]Request, error) {
	fake.now = now
	return []Request{fake.request}, fake.err
}

func (fake *fakeAdminStore) ApproveRequest(_ context.Context, _ int64, expectedAppID string, now time.Time) (Binding, error) {
	fake.record("approve")
	fake.now = now
	fake.approvedAppID = expectedAppID
	if fake.approveErr != nil {
		return fake.binding, fake.approveErr
	}
	return fake.binding, fake.err
}

func (fake *fakeAdminStore) RejectRequest(_ context.Context, _ int64, now time.Time) (Request, error) {
	fake.now = now
	return fake.request, fake.err
}

func (fake *fakeAdminStore) ListBindings(_ context.Context, now time.Time) ([]BindingWithKeys, error) {
	fake.now = now
	return []BindingWithKeys{{Binding: fake.binding}}, fake.err
}

func (fake *fakeAdminStore) RevokeKey(_ context.Context, _ int64, now time.Time) (Key, error) {
	fake.now = now
	return fake.key, fake.err
}

func (fake *fakeAdminStore) Unbind(_ context.Context, appID string) (bool, error) {
	fake.unbound = appID
	return fake.deleted, fake.err
}

func TestAdminUseCasesPassTheServiceClockAndLogTheDecision(testInstance *testing.T) {
	logs := &syncBuffer{}
	admin := &fakeAdminStore{
		binding: matchingBinding(), request: Request{ID: 3, AppID: "urls4irl"},
		key: Key{ID: 4, AppID: "urls4irl", Hint: "dak_abcd", State: KeyRevoked}, deleted: true,
	}
	service := NewService(ServiceConfig{
		Admin: admin, Registrar: &fakeRegistrar{}, Logger: textLogger(logs), Now: func() time.Time { return testNow },
	})
	ctx := context.Background()

	// The fake keeps one shared "now", so each call's time is checked and cleared before the next.
	requireClock := func(method string) {
		testInstance.Helper()
		if !admin.now.Equal(testNow) {
			testInstance.Errorf("%s passed store time %v, want the service clock %v", method, admin.now, testNow)
		}
		admin.now = time.Time{}
	}
	if _, err := service.ListRequests(ctx); err != nil {
		testInstance.Errorf("ListRequests: %v", err)
	}
	requireClock("ListRequests")
	if _, err := service.ListBindings(ctx); err != nil {
		testInstance.Errorf("ListBindings: %v", err)
	}
	requireClock("ListBindings")
	if binding, err := service.ApproveRequest(ctx, 3); err != nil || binding.AppID != "urls4irl" {
		testInstance.Errorf("ApproveRequest = %+v, %v", binding, err)
	}
	requireClock("ApproveRequest")
	if request, err := service.RejectRequest(ctx, 3); err != nil || request.ID != 3 {
		testInstance.Errorf("RejectRequest = %+v, %v", request, err)
	}
	requireClock("RejectRequest")
	if key, err := service.RevokeKey(ctx, 4); err != nil || key.ID != 4 {
		testInstance.Errorf("RevokeKey = %+v, %v", key, err)
	}
	requireClock("RevokeKey")
	if deleted, err := service.Unbind(ctx, "urls4irl"); err != nil || !deleted || admin.unbound != "urls4irl" {
		testInstance.Errorf("Unbind = %v, %v (store saw %q)", deleted, err, admin.unbound)
	}

	for _, want := range []string{"app identity request approved", "app identity request rejected", "app key revoked", "app unbound", "key_hint=dak_abcd"} {
		if !strings.Contains(logs.String(), want) {
			testInstance.Errorf("log is missing %q:\n%s", want, logs.String())
		}
	}
}

func TestAdminUseCasesReturnStoreErrorsAndLogNothing(testInstance *testing.T) {
	logs := &syncBuffer{}
	// getErr makes ApproveRequest fail at GetRequest, before the registrar and the store's
	// ApproveRequest; the store-level ErrRequestNotFound path has its own "vanished under lock" row.
	admin := &fakeAdminStore{err: ErrRequestNotFound, getErr: ErrRequestNotFound}
	service := NewService(ServiceConfig{
		Admin: admin, Registrar: &fakeRegistrar{}, Logger: textLogger(logs), Now: func() time.Time { return testNow },
	})
	ctx := context.Background()

	if _, err := service.ApproveRequest(ctx, 1); !errors.Is(err, ErrRequestNotFound) {
		testInstance.Errorf("ApproveRequest err = %v", err)
	}
	if _, err := service.RejectRequest(ctx, 1); !errors.Is(err, ErrRequestNotFound) {
		testInstance.Errorf("RejectRequest err = %v", err)
	}
	if _, err := service.RevokeKey(ctx, 1); !errors.Is(err, ErrRequestNotFound) {
		testInstance.Errorf("RevokeKey err = %v", err)
	}
	if _, err := service.Unbind(ctx, "urls4irl"); !errors.Is(err, ErrRequestNotFound) {
		testInstance.Errorf("Unbind err = %v", err)
	}
	if logs.String() != "" {
		testInstance.Errorf("a failed decision must not be logged as a success:\n%s", logs.String())
	}
}

func TestApproveRequestRegistersTheStoredAppBeforeTheStoreApproves(testInstance *testing.T) {
	logs := &syncBuffer{}
	var calls []string
	admin := &fakeAdminStore{
		binding: matchingBinding(), request: Request{ID: 3, AppID: "urls4irl"}, calls: &calls,
	}
	registrar := &fakeRegistrar{calls: &calls}
	service := NewService(ServiceConfig{
		Admin: admin, Registrar: registrar, Logger: textLogger(logs), Now: func() time.Time { return testNow },
	})

	binding, err := service.ApproveRequest(context.Background(), 3)

	if err != nil || binding.AppID != "urls4irl" {
		testInstance.Fatalf("ApproveRequest = %+v, %v", binding, err)
	}
	if got := strings.Join(calls, ","); got != "get,register,approve" {
		testInstance.Errorf("call order = %s, want get,register,approve", got)
	}
	if len(registrar.appIDs) != 1 || registrar.appIDs[0] != "urls4irl" || registrar.displayName[0] != "urls4irl" {
		testInstance.Errorf("registrar saw %v / %v, want the stored app_id as id and display name", registrar.appIDs, registrar.displayName)
	}
	if admin.approvedAppID != "urls4irl" {
		testInstance.Errorf("store approved under %q, want the stored app_id", admin.approvedAppID)
	}
	if !strings.Contains(logs.String(), "app identity request approved") {
		testInstance.Errorf("log is missing the approval:\n%s", logs.String())
	}
}

func TestApproveRequestRefusals(testInstance *testing.T) {
	deadline := fmt.Errorf("post: %w", context.DeadlineExceeded)
	for _, testCase := range []struct {
		name         string
		admin        *fakeAdminStore
		registrarErr error
		noRegistrar  bool
		canceled     bool
		wantErr      error
		wantCalls    []string
	}{
		{"registrar fails", &fakeAdminStore{request: Request{AppID: "urls4irl"}}, errors.New("status 500"), false, false,
			ErrAppRegistryUnavailable, []string{"get", "register"}},
		{"registrar times out", &fakeAdminStore{request: Request{AppID: "urls4irl"}}, deadline, false, false,
			ErrAppRegistryUnavailable, []string{"get", "register"}},
		{"caller canceled", &fakeAdminStore{request: Request{AppID: "urls4irl"}}, context.Canceled, false, true,
			context.Canceled, []string{"get", "register"}},
		{"reserved app id", &fakeAdminStore{request: Request{AppID: "everyone"}}, nil, false, false,
			ErrAppIDReserved, []string{"get"}},
		{"unknown request", &fakeAdminStore{getErr: ErrRequestNotFound}, nil, false, false,
			ErrRequestNotFound, []string{"get"}},
		{"rejected request", &fakeAdminStore{request: Request{AppID: "urls4irl", Status: RequestRejected}}, nil, false, false,
			ErrRequestNotPending, []string{"get"}},
		{"no registrar", &fakeAdminStore{request: Request{AppID: "urls4irl"}}, nil, true, false,
			ErrAppRegistryNotConfigured, nil},
		{"overlap after registering", &fakeAdminStore{request: Request{AppID: "urls4irl"}, approveErr: ErrAppIDOverlap}, nil, false, false,
			ErrAppIDOverlap, []string{"get", "register", "approve"}},
		{"request changed under lock", &fakeAdminStore{request: Request{AppID: "urls4irl"}, approveErr: ErrRequestChanged}, nil, false, false,
			ErrRequestChanged, []string{"get", "register", "approve"}},
		{"request vanished under lock", &fakeAdminStore{request: Request{AppID: "urls4irl"}, approveErr: ErrRequestNotFound}, nil, false, false,
			ErrRequestNotFound, []string{"get", "register", "approve"}},
		{"request decided under lock", &fakeAdminStore{request: Request{AppID: "urls4irl"}, approveErr: ErrRequestNotPending}, nil, false, false,
			ErrRequestNotPending, []string{"get", "register", "approve"}},
	} {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			logs := &syncBuffer{}
			var calls []string
			testCase.admin.calls = &calls
			registrar := &fakeRegistrar{err: testCase.registrarErr, calls: &calls}
			config := ServiceConfig{Admin: testCase.admin, Logger: textLogger(logs), Now: func() time.Time { return testNow }}
			if !testCase.noRegistrar {
				config.Registrar = registrar
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if testCase.canceled {
				cancel()
			}

			_, err := NewService(config).ApproveRequest(ctx, 1)

			if !errors.Is(err, testCase.wantErr) {
				subTest.Errorf("err = %v, want %v", err, testCase.wantErr)
			}
			if !slices.Equal(calls, testCase.wantCalls) {
				subTest.Errorf("calls = %v, want %v", calls, testCase.wantCalls)
			}
			if strings.Contains(logs.String(), "app identity request approved") {
				subTest.Errorf("a refused approval must not be logged as a success:\n%s", logs.String())
			}
			if testCase.wantErr == ErrAppRegistryUnavailable {
				for _, want := range []string{"request_id=1", "app_id=urls4irl", "duration=", testCase.registrarErr.Error()} {
					if !strings.Contains(logs.String(), want) {
						subTest.Errorf("registration failure WARN is missing %q:\n%s", want, logs.String())
					}
				}
				if !registrar.hasDeadline || time.Until(registrar.deadline) > registerTimeout {
					subTest.Errorf("registrar deadline set=%v remaining=%v, want set and <= %v",
						registrar.hasDeadline, time.Until(registrar.deadline), registerTimeout)
				}
			}
			if testCase.wantErr == ErrAppRegistryUnavailable && testCase.registrarErr == deadline &&
				!errors.Is(err, context.DeadlineExceeded) {
				subTest.Errorf("err = %v, want it to also wrap context.DeadlineExceeded", err)
			}
			if testCase.canceled && strings.Contains(logs.String(), "registration failed") {
				subTest.Errorf("a canceled caller must not log a registry WARN:\n%s", logs.String())
			}
		})
	}
}

func TestExchangeRejectsTheReservedAppIDBeforeAnyStoreCall(testInstance *testing.T) {
	fixture := newExchangeFixture()

	_, err := fixture.service.Exchange(context.Background(), "everyone", "token")

	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || validationErr.Message != "invalid app_id" {
		testInstance.Fatalf("err = %v, want the invalid app_id ValidationError", err)
	}
	if fixture.store.runs != 0 || fixture.verifier.calls != 0 || len(fixture.tx.calls) != 0 {
		testInstance.Errorf("the reserved id reached the verifier or store (runs %d, calls %v)", fixture.store.runs, fixture.tx.calls)
	}
}

func TestReservedAppIDRuleAppliesToTheExchangeOnly(testInstance *testing.T) {
	if validateExchangeAppID("everyone") == nil {
		testInstance.Errorf("validateExchangeAppID(everyone) = nil, want an error")
	}
	if err := validateExchangeAppID("urls4irl"); err != nil {
		testInstance.Errorf("validateExchangeAppID(urls4irl) = %v", err)
	}
	if err := ValidateAppID("everyone"); err != nil {
		testInstance.Errorf("ValidateAppID(everyone) = %v, want nil so an existing binding stays removable", err)
	}
	admin := &fakeAdminStore{deleted: true}
	service := NewService(ServiceConfig{Admin: admin, Logger: textLogger(&syncBuffer{})})
	if deleted, err := service.Unbind(context.Background(), "everyone"); err != nil || !deleted || admin.unbound != "everyone" {
		testInstance.Errorf("Unbind(everyone) = %v, %v (store saw %q), want it to reach the store", deleted, err, admin.unbound)
	}
}

func TestUnbindRejectsAMalformedAppIDBeforeTheStore(testInstance *testing.T) {
	admin := &fakeAdminStore{}
	service := NewService(ServiceConfig{Admin: admin, Logger: textLogger(&syncBuffer{})})

	_, err := service.Unbind(context.Background(), "Not Valid")

	var validationErr *ValidationError
	if !errors.As(err, &validationErr) || admin.unbound != "" {
		testInstance.Errorf("err = %v, store saw %q, want a ValidationError and no store call", err, admin.unbound)
	}
}
