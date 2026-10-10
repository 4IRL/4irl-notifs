package migrate

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// failureCall records one RecordFailure invocation.
type failureCall struct {
	version int
	message string
}

// fakeStore is a scriptable Store that records the order of calls made to it.
type fakeStore struct {
	calls []string

	lockErr          error
	ensureTablesErr  error
	appliedVersions  map[int]bool
	appliedErr       error
	applyErrByVer    map[int]error
	recordFailureErr error

	applied  []int
	failures []failureCall
	unlocked int
}

func (store *fakeStore) Lock(context.Context) (func(context.Context) error, error) {
	store.calls = append(store.calls, "Lock")
	if store.lockErr != nil {
		return nil, store.lockErr
	}
	return func(context.Context) error {
		store.calls = append(store.calls, "unlock")
		store.unlocked++
		return nil
	}, nil
}

func (store *fakeStore) EnsureTables(context.Context) error {
	store.calls = append(store.calls, "EnsureTables")
	return store.ensureTablesErr
}

func (store *fakeStore) AppliedVersions(context.Context) (map[int]bool, error) {
	store.calls = append(store.calls, "AppliedVersions")
	return store.appliedVersions, store.appliedErr
}

func (store *fakeStore) Apply(_ context.Context, migration Migration) error {
	store.calls = append(store.calls, "Apply")
	if applyErr := store.applyErrByVer[migration.Version]; applyErr != nil {
		return applyErr
	}
	store.applied = append(store.applied, migration.Version)
	return nil
}

func (store *fakeStore) RecordFailure(_ context.Context, version int, message string) error {
	store.calls = append(store.calls, "RecordFailure")
	store.failures = append(store.failures, failureCall{version: version, message: message})
	return store.recordFailureErr
}

func threeMigrations() []Migration {
	return []Migration{
		{Version: 1, Name: "one", SQL: "SELECT 1;"},
		{Version: 2, Name: "two", SQL: "SELECT 2;"},
		{Version: 3, Name: "three", SQL: "SELECT 3;"},
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

func TestRunAppliesOnlyPendingVersionsInOrder(testInstance *testing.T) {
	store := &fakeStore{appliedVersions: map[int]bool{1: true}}

	count, err := Run(context.Background(), store, threeMigrations(), discardLogger())

	if err != nil {
		testInstance.Fatalf("Run error = %v, want nil", err)
	}
	if count != 2 {
		testInstance.Fatalf("applied count = %d, want 2", count)
	}
	if want := []int{2, 3}; !reflect.DeepEqual(store.applied, want) {
		testInstance.Fatalf("applied = %v, want %v", store.applied, want)
	}
	if store.unlocked != 1 {
		testInstance.Fatalf("unlock calls = %d, want 1", store.unlocked)
	}
}

func TestRunWithNothingPendingAppliesNothing(testInstance *testing.T) {
	store := &fakeStore{appliedVersions: map[int]bool{1: true, 2: true, 3: true}}

	count, err := Run(context.Background(), store, threeMigrations(), discardLogger())

	if err != nil {
		testInstance.Fatalf("Run error = %v, want nil", err)
	}
	if count != 0 {
		testInstance.Fatalf("applied count = %d, want 0", count)
	}
	for _, call := range store.calls {
		if call == "Apply" {
			testInstance.Fatalf("calls = %v, want no Apply", store.calls)
		}
	}
	if store.unlocked != 1 {
		testInstance.Fatalf("unlock calls = %d, want 1", store.unlocked)
	}
}

func TestRunApplyFailureStopsRecordsAndUnlocks(testInstance *testing.T) {
	applyFailure := errors.New("syntax error at or near \"BOOM\"")
	store := &fakeStore{
		appliedVersions: map[int]bool{},
		applyErrByVer:   map[int]error{2: applyFailure},
	}

	count, err := Run(context.Background(), store, threeMigrations(), discardLogger())

	if err == nil {
		testInstance.Fatal("Run error = nil, want an error")
	}
	if !errors.Is(err, applyFailure) {
		testInstance.Fatalf("Run error = %v, want it to wrap the apply error", err)
	}
	if count != 1 {
		testInstance.Fatalf("applied count = %d, want 1 (version 1 only)", count)
	}
	if want := []int{1}; !reflect.DeepEqual(store.applied, want) {
		testInstance.Fatalf("applied = %v, want %v (version 3 must not run)", store.applied, want)
	}
	wantFailures := []failureCall{{version: 2, message: applyFailure.Error()}}
	if !reflect.DeepEqual(store.failures, wantFailures) {
		testInstance.Fatalf("failures = %+v, want %+v", store.failures, wantFailures)
	}
	if store.unlocked != 1 {
		testInstance.Fatalf("unlock calls = %d, want 1", store.unlocked)
	}
}

func TestRunLockErrorReturnsBeforeEnsureTables(testInstance *testing.T) {
	lockFailure := errors.New("cannot reach database")
	store := &fakeStore{lockErr: lockFailure}

	count, err := Run(context.Background(), store, threeMigrations(), discardLogger())

	if !errors.Is(err, lockFailure) {
		testInstance.Fatalf("Run error = %v, want it to wrap the lock error", err)
	}
	if count != 0 {
		testInstance.Fatalf("applied count = %d, want 0", count)
	}
	if want := []string{"Lock"}; !reflect.DeepEqual(store.calls, want) {
		testInstance.Fatalf("calls = %v, want %v", store.calls, want)
	}
}

func TestRunEnsuresTablesBeforeReadingAppliedVersions(testInstance *testing.T) {
	store := &fakeStore{appliedVersions: map[int]bool{}}

	if _, err := Run(context.Background(), store, nil, discardLogger()); err != nil {
		testInstance.Fatalf("Run error = %v, want nil", err)
	}

	want := []string{"Lock", "EnsureTables", "AppliedVersions", "unlock"}
	if !reflect.DeepEqual(store.calls, want) {
		testInstance.Fatalf("calls = %v, want %v", store.calls, want)
	}
}

func TestRunEnsureTablesAndAppliedVersionsErrorsAreReturned(testInstance *testing.T) {
	boom := errors.New("boom")
	testCases := []struct {
		name  string
		store *fakeStore
	}{
		{name: "EnsureTables", store: &fakeStore{ensureTablesErr: boom}},
		{name: "AppliedVersions", store: &fakeStore{appliedErr: boom}},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			_, err := Run(context.Background(), testCase.store, threeMigrations(), discardLogger())

			if !errors.Is(err, boom) {
				subTest.Fatalf("Run error = %v, want it to wrap %v", err, boom)
			}
			if len(testCase.store.applied) != 0 {
				subTest.Fatalf("applied = %v, want none", testCase.store.applied)
			}
			if testCase.store.unlocked != 1 {
				subTest.Fatalf("unlock calls = %d, want 1", testCase.store.unlocked)
			}
		})
	}
}

func TestRunRecordFailureErrorIsLoggedNotReturned(testInstance *testing.T) {
	const recordFailureText = "could not write failure row 10.0.0.5"
	applyFailure := errors.New("relation already exists")
	store := &fakeStore{
		appliedVersions:  map[int]bool{},
		applyErrByVer:    map[int]error{1: applyFailure},
		recordFailureErr: errors.New(recordFailureText),
	}
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))

	_, err := Run(context.Background(), store, threeMigrations(), logger)

	if !errors.Is(err, applyFailure) {
		testInstance.Fatalf("Run error = %v, want it to wrap the apply error", err)
	}
	if strings.Contains(err.Error(), recordFailureText) {
		testInstance.Fatalf("Run error = %q, must not carry the RecordFailure error", err)
	}
	if !strings.Contains(logBuffer.String(), recordFailureText) {
		testInstance.Fatalf("log output = %q, want it to contain %q", logBuffer.String(), recordFailureText)
	}
}

func TestRunDoesNotRecordFailureWhenContextCanceled(testInstance *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := &fakeStore{
		appliedVersions: map[int]bool{},
		applyErrByVer:   map[int]error{1: context.Canceled},
	}
	// Cancel while Apply is "running": the fake returns the cancellation error.
	cancel()

	_, err := Run(ctx, store, threeMigrations(), discardLogger())

	if !errors.Is(err, context.Canceled) {
		testInstance.Fatalf("Run error = %v, want it to wrap context.Canceled", err)
	}
	if len(store.failures) != 0 {
		testInstance.Fatalf("failures = %+v, want none for an interrupted migration", store.failures)
	}
	if store.unlocked != 1 {
		testInstance.Fatalf("unlock calls = %d, want 1", store.unlocked)
	}
}

func TestRunTruncatesRecordedFailureToFiveHundredRunes(testInstance *testing.T) {
	testCases := []struct {
		name        string
		message     string
		wantRunes   int
		wantMessage string
	}{
		{name: "ascii over limit", message: strings.Repeat("x", 600), wantRunes: 500},
		{name: "multibyte over limit", message: strings.Repeat("é", 600), wantRunes: 500},
		{name: "exactly at limit", message: strings.Repeat("y", 500), wantRunes: 500},
		{name: "under limit is untouched", message: "short", wantRunes: 5, wantMessage: "short"},
	}

	for _, testCase := range testCases {
		testInstance.Run(testCase.name, func(subTest *testing.T) {
			store := &fakeStore{
				appliedVersions: map[int]bool{},
				applyErrByVer:   map[int]error{1: errors.New(testCase.message)},
			}

			_, _ = Run(context.Background(), store, threeMigrations(), discardLogger())

			if len(store.failures) != 1 {
				subTest.Fatalf("failures = %+v, want exactly one", store.failures)
			}
			recorded := store.failures[0].message
			if gotRunes := len([]rune(recorded)); gotRunes != testCase.wantRunes {
				subTest.Fatalf("recorded message has %d runes, want %d", gotRunes, testCase.wantRunes)
			}
			if testCase.wantMessage != "" && recorded != testCase.wantMessage {
				subTest.Fatalf("recorded message = %q, want %q", recorded, testCase.wantMessage)
			}
		})
	}
}
