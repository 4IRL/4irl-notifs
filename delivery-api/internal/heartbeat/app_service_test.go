package heartbeat

import (
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	checkAppA = "app_a"
	checkAppB = "app_b"
)

// appCheckStore is a Store for the app-scoped service methods: it holds checks by id, honours the owner like
// the Postgres store, and records every call. Any other Store method panics on the nil embedded interface.
type appCheckStore struct {
	Store
	checks map[int64]StoredCheck
	calls  []string
	// listErr scripts a ListByOwner failure.
	listErr error
	// errs scripts a failure per method name ("Create", "Get", "PatchOwnedBy", ...), returned before any change.
	errs map[string]error

	created []NewCheck
	// writeTimes is the `now` each write method (Create, PatchOwnedBy, PauseOwnedBy, ResumeOwnedBy) was handed.
	writeTimes map[string]time.Time
}

// recordWrite notes the clock a write method was given and returns the failure scripted for it, if any.
func (store *appCheckStore) recordWrite(method string, now time.Time) error {
	store.calls = append(store.calls, method)
	if store.writeTimes == nil {
		store.writeTimes = map[string]time.Time{}
	}
	store.writeTimes[method] = now
	return store.errs[method]
}

func newAppCheckStore(checks ...StoredCheck) *appCheckStore {
	store := &appCheckStore{checks: map[int64]StoredCheck{}}
	for _, check := range checks {
		store.checks[check.ID] = check
	}
	return store
}

func (store *appCheckStore) Create(_ context.Context, check NewCheck, now time.Time) (StoredCheck, error) {
	if failure := store.recordWrite("Create", now); failure != nil {
		return StoredCheck{}, failure
	}
	store.created = append(store.created, check)
	stored := StoredCheck{
		ID: int64(len(store.checks) + 1), AppID: check.AppID, Name: check.Name,
		PeriodSeconds: check.PeriodSeconds, GraceSeconds: check.GraceSeconds,
		OwnerKind: check.OwnerKind, OwnerAppID: check.OwnerAppID,
	}
	store.checks[stored.ID] = stored
	return stored, nil
}

func (store *appCheckStore) Get(_ context.Context, id int64) (StoredCheck, error) {
	store.calls = append(store.calls, "Get")
	if failure := store.errs["Get"]; failure != nil {
		return StoredCheck{}, failure
	}
	check, found := store.checks[id]
	if !found {
		return StoredCheck{}, ErrCheckNotFound
	}
	return check, nil
}

func (store *appCheckStore) ListByOwner(_ context.Context, appID string) ([]StoredCheck, error) {
	store.calls = append(store.calls, "ListByOwner")
	if store.listErr != nil {
		return nil, store.listErr
	}
	owned := []StoredCheck{}
	for id := int64(1); id <= int64(len(store.checks)); id++ {
		if check, found := store.checks[id]; found && ownedByApp(check, appID) {
			owned = append(owned, check)
		}
	}
	return owned, nil
}

func (store *appCheckStore) owned(id int64, appID string) (StoredCheck, error) {
	check, found := store.checks[id]
	if !found || !ownedByApp(check, appID) {
		return StoredCheck{}, ErrCheckNotFound
	}
	return check, nil
}

func (store *appCheckStore) PatchOwnedBy(
	_ context.Context, id int64, appID string, _ CheckPatch, now time.Time,
) (StoredCheck, error) {
	if failure := store.recordWrite("PatchOwnedBy", now); failure != nil {
		return StoredCheck{}, failure
	}
	return store.owned(id, appID)
}

func (store *appCheckStore) DeleteOwnedBy(_ context.Context, id int64, appID string) (bool, error) {
	store.calls = append(store.calls, "DeleteOwnedBy")
	if failure := store.errs["DeleteOwnedBy"]; failure != nil {
		return false, failure
	}
	if _, ownedErr := store.owned(id, appID); ownedErr != nil {
		return false, nil
	}
	delete(store.checks, id)
	return true, nil
}

func (store *appCheckStore) PauseOwnedBy(_ context.Context, id int64, appID string, now time.Time) (StoredCheck, error) {
	if failure := store.recordWrite("PauseOwnedBy", now); failure != nil {
		return StoredCheck{}, failure
	}
	return store.owned(id, appID)
}

func (store *appCheckStore) ResumeOwnedBy(_ context.Context, id int64, appID string, now time.Time) (StoredCheck, error) {
	if failure := store.recordWrite("ResumeOwnedBy", now); failure != nil {
		return StoredCheck{}, failure
	}
	return store.owned(id, appID)
}

func ownedCheck(id int64, appID string) StoredCheck {
	owner := appID
	return StoredCheck{ID: id, AppID: appID, Name: "nightly", OwnerKind: registry.OwnerApp, OwnerAppID: &owner}
}

// operatorCheck is an operator-owned check under app A's ping identity.
func operatorCheck(id int64) StoredCheck {
	return StoredCheck{ID: id, AppID: checkAppA, Name: "nightly_op", OwnerKind: registry.OwnerOperator}
}

func newAppCheckService(testInstance *testing.T, store Store) *Service {
	testInstance.Helper()
	service, _ := newLoggedAppCheckService(testInstance, store)
	return service
}

// newLoggedAppCheckService is newAppCheckService that also returns the buffer the service logs into.
func newLoggedAppCheckService(testInstance *testing.T, store Store) (*Service, *lockedBuffer) {
	testInstance.Helper()
	logs := &lockedBuffer{}
	return NewService(ServiceConfig{
		Store:    store,
		Messages: &fakeMessages{},
		Now:      func() time.Time { return serviceNow },
		Logger:   slog.New(slog.NewTextHandler(logs, nil)),
	}), logs
}

func validAppCheckInput() AppCheckInput {
	return AppCheckInput{Name: "nightly", PeriodSeconds: 3600, GraceSeconds: 300, DestinationNames: []string{"ops_discord"}}
}

func TestCreateForAppIsOwnedByAndRegisteredUnderTheCallingApp(testInstance *testing.T) {
	store := newAppCheckStore()
	service := newAppCheckService(testInstance, store)

	created, err := service.CreateForApp(context.Background(), checkAppA, validAppCheckInput())

	if err != nil {
		testInstance.Fatalf("CreateForApp: %v", err)
	}
	if len(store.created) != 1 {
		testInstance.Fatalf("store creates = %d, want 1", len(store.created))
	}
	handed := store.created[0]
	// The row handed to the store names the owner explicitly: the store treats an empty owner kind as the
	// operator, so anything short of 'app' here would be a fail-open write.
	if handed.OwnerKind != registry.OwnerApp || handed.OwnerAppID == nil || *handed.OwnerAppID != checkAppA {
		testInstance.Fatalf("owner = %q/%v, want app/%q: an app-scoped create must never yield an operator-owned check",
			handed.OwnerKind, handed.OwnerAppID, checkAppA)
	}
	if handed.AppID != checkAppA {
		testInstance.Fatalf("app_id = %q, want %q (the ping identity is always the caller's)", handed.AppID, checkAppA)
	}
	if !ownedByApp(created, checkAppA) || created.AppID != checkAppA {
		testInstance.Fatalf("created = %+v, want an app-owned check under %q", created, checkAppA)
	}
	if !reflect.DeepEqual(handed.DestinationNames, []string{"ops_discord"}) {
		testInstance.Fatalf("destinations = %v", handed.DestinationNames)
	}
}

func TestCreateForAppValidatesBeforeTheStore(testInstance *testing.T) {
	for label, test := range map[string]struct {
		appID string
		input func(AppCheckInput) AppCheckInput
	}{
		"malformed caller": {appID: "*", input: func(input AppCheckInput) AppCheckInput { return input }},
		"empty caller":     {appID: "", input: func(input AppCheckInput) AppCheckInput { return input }},
		"bad name": {appID: checkAppA, input: func(input AppCheckInput) AppCheckInput {
			input.Name = "BAD NAME"
			return input
		}},
		"bad period": {appID: checkAppA, input: func(input AppCheckInput) AppCheckInput {
			input.PeriodSeconds = 1
			return input
		}},
		"no destinations": {appID: checkAppA, input: func(input AppCheckInput) AppCheckInput {
			input.DestinationNames = nil
			return input
		}},
	} {
		store := newAppCheckStore()
		service := newAppCheckService(testInstance, store)

		_, err := service.CreateForApp(context.Background(), test.appID, test.input(validAppCheckInput()))

		var validationErr *ValidationError
		if !errors.As(err, &validationErr) {
			testInstance.Fatalf("%s: err = %v, want a validation error", label, err)
		}
		if len(store.calls) != 0 {
			testInstance.Fatalf("%s: the store was called: %v", label, store.calls)
		}
	}
}

func TestListForAppReturnsOnlyTheCallersOwn(testInstance *testing.T) {
	store := newAppCheckStore(ownedCheck(1, checkAppA), operatorCheck(2), ownedCheck(3, checkAppB))
	service := newAppCheckService(testInstance, store)

	checks, err := service.ListForApp(context.Background(), checkAppA)

	if err != nil || len(checks) != 1 || checks[0].ID != 1 {
		testInstance.Fatalf("listed %+v, %v, want only check 1 (not the operator's with the same app_id, not app B's)", checks, err)
	}
	empty, err := service.ListForApp(context.Background(), "app_c")
	if err != nil || empty == nil || len(empty) != 0 {
		testInstance.Fatalf("an app with no checks lists %#v, %v, want an empty non-nil slice", empty, err)
	}
}

// ownerBlindCheckStore returns every check from ListByOwner, as a store that forgot its owner filter.
type ownerBlindCheckStore struct{ *appCheckStore }

func (store *ownerBlindCheckStore) ListByOwner(context.Context, string) ([]StoredCheck, error) {
	return []StoredCheck{ownedCheck(1, checkAppB), operatorCheck(2)}, nil
}

func TestListForAppDropsARowTheStoreReturnedForAnotherOwner(testInstance *testing.T) {
	service := newAppCheckService(testInstance, &ownerBlindCheckStore{appCheckStore: newAppCheckStore()})

	checks, err := service.ListForApp(context.Background(), checkAppA)

	if err != nil || len(checks) != 0 {
		testInstance.Fatalf("listed %+v, %v, want nothing: no row is app A's", checks, err)
	}
}

// A check the app does not own is indistinguishable from an absent one on every operation, and is never changed.
func TestAppCheckAccessToAnythingNotOwnedIsNotFound(testInstance *testing.T) {
	store := newAppCheckStore(operatorCheck(1), ownedCheck(2, checkAppB))
	service := newAppCheckService(testInstance, store)
	ctx := context.Background()
	period := 600

	for label, id := range map[string]int64{
		"operator check with the app's id": 1, "another app's check": 2, "absent check": 99,
	} {
		_, getErr := service.GetForApp(ctx, checkAppA, id)
		_, patchErr := service.PatchForApp(ctx, checkAppA, id, CheckPatch{PeriodSeconds: &period})
		_, emptyPatchErr := service.PatchForApp(ctx, checkAppA, id, CheckPatch{})
		deleteErr := service.DeleteForApp(ctx, checkAppA, id)
		_, pauseErr := service.PauseForApp(ctx, checkAppA, id)
		_, resumeErr := service.ResumeForApp(ctx, checkAppA, id)

		for op, err := range map[string]error{
			"get": getErr, "patch": patchErr, "empty patch": emptyPatchErr, "delete": deleteErr,
			"pause": pauseErr, "resume": resumeErr,
		} {
			if !errors.Is(err, ErrCheckNotFound) {
				testInstance.Fatalf("%s %s: err = %v, want ErrCheckNotFound", op, label, err)
			}
		}
	}
	if len(store.checks) != 2 {
		testInstance.Fatalf("checks left = %d, want both untouched", len(store.checks))
	}
}

func TestAppCheckWritesCarryTheOwnerToTheStoreInTheirOnlyCall(testInstance *testing.T) {
	store := newAppCheckStore(ownedCheck(1, checkAppA))
	service := newAppCheckService(testInstance, store)
	ctx := context.Background()
	period := 600

	if _, err := service.PatchForApp(ctx, checkAppA, 1, CheckPatch{PeriodSeconds: &period}); err != nil {
		testInstance.Fatalf("PatchForApp: %v", err)
	}
	if _, err := service.PauseForApp(ctx, checkAppA, 1); err != nil {
		testInstance.Fatalf("PauseForApp: %v", err)
	}
	if _, err := service.ResumeForApp(ctx, checkAppA, 1); err != nil {
		testInstance.Fatalf("ResumeForApp: %v", err)
	}
	if err := service.DeleteForApp(ctx, checkAppA, 1); err != nil {
		testInstance.Fatalf("DeleteForApp: %v", err)
	}

	// No unscoped read-then-write: each write is the one owner-scoped store call.
	want := []string{"PatchOwnedBy", "PauseOwnedBy", "ResumeOwnedBy", "DeleteOwnedBy"}
	if !reflect.DeepEqual(store.calls, want) {
		testInstance.Fatalf("store calls = %v, want %v", store.calls, want)
	}
	if err := service.DeleteForApp(ctx, checkAppA, 1); !errors.Is(err, ErrCheckNotFound) {
		testInstance.Fatalf("a second delete: err = %v, want ErrCheckNotFound (not the admin's idempotent success)", err)
	}
}

func TestPatchForAppWithNoFieldIsAnOwnerCheckedRead(testInstance *testing.T) {
	store := newAppCheckStore(ownedCheck(1, checkAppA))
	service := newAppCheckService(testInstance, store)

	check, err := service.PatchForApp(context.Background(), checkAppA, 1, CheckPatch{})

	if err != nil || check.ID != 1 {
		testInstance.Fatalf("PatchForApp = %+v, %v, want the current check", check, err)
	}
	if !reflect.DeepEqual(store.calls, []string{"Get"}) {
		testInstance.Fatalf("store calls = %v, want a single Get", store.calls)
	}
}

func TestAppCheckMethodsRefuseAMalformedCallerAppID(testInstance *testing.T) {
	store := newAppCheckStore(ownedCheck(1, checkAppA))
	service := newAppCheckService(testInstance, store)
	ctx := context.Background()
	period := 600

	_, listErr := service.ListForApp(ctx, "")
	_, getErr := service.GetForApp(ctx, "*", 1)
	_, patchErr := service.PatchForApp(ctx, "", 1, CheckPatch{PeriodSeconds: &period})
	deleteErr := service.DeleteForApp(ctx, "a b", 1)
	_, pauseErr := service.PauseForApp(ctx, "", 1)
	_, resumeErr := service.ResumeForApp(ctx, "", 1)

	for label, err := range map[string]error{
		"list": listErr, "get": getErr, "patch": patchErr, "delete": deleteErr, "pause": pauseErr, "resume": resumeErr,
	} {
		var validationErr *ValidationError
		if !errors.As(err, &validationErr) {
			testInstance.Fatalf("%s: err = %v, want a validation error", label, err)
		}
	}
	if len(store.calls) != 0 {
		testInstance.Fatalf("the store was called for a malformed caller: %v", store.calls)
	}
}

func TestOwnedByAppFailsClosed(testInstance *testing.T) {
	appA := checkAppA
	for label, test := range map[string]struct {
		check StoredCheck
		appID string
		want  bool
	}{
		"own":                {check: ownedCheck(1, checkAppA), appID: checkAppA, want: true},
		"another app's":      {check: ownedCheck(1, checkAppB), appID: checkAppA},
		"operator":           {check: operatorCheck(1), appID: checkAppA},
		"empty caller":       {check: ownedCheck(1, checkAppA), appID: ""},
		"empty owner kind":   {check: StoredCheck{OwnerAppID: &appA}, appID: checkAppA},
		"unknown owner kind": {check: StoredCheck{OwnerKind: "team", OwnerAppID: &appA}, appID: checkAppA},
		"app without owner":  {check: StoredCheck{OwnerKind: registry.OwnerApp}, appID: checkAppA},
		"zero value":         {check: StoredCheck{}, appID: checkAppA},
	} {
		if got := ownedByApp(test.check, test.appID); got != test.want {
			testInstance.Fatalf("%s: ownedByApp = %v, want %v", label, got, test.want)
		}
	}
}

// Every write is stamped with the service's clock: a zero time handed to the store would restart a resumed
// check's deadline in year 1 and page the app on the next tick.
func TestAppCheckWritesStampTheServiceClock(testInstance *testing.T) {
	store := newAppCheckStore()
	service := newAppCheckService(testInstance, store)
	ctx := context.Background()
	period := 600

	if _, err := service.CreateForApp(ctx, checkAppA, validAppCheckInput()); err != nil {
		testInstance.Fatalf("CreateForApp: %v", err)
	}
	if _, err := service.PatchForApp(ctx, checkAppA, 1, CheckPatch{PeriodSeconds: &period}); err != nil {
		testInstance.Fatalf("PatchForApp: %v", err)
	}
	if _, err := service.PauseForApp(ctx, checkAppA, 1); err != nil {
		testInstance.Fatalf("PauseForApp: %v", err)
	}
	if _, err := service.ResumeForApp(ctx, checkAppA, 1); err != nil {
		testInstance.Fatalf("ResumeForApp: %v", err)
	}

	for _, method := range []string{"Create", "PatchOwnedBy", "PauseOwnedBy", "ResumeOwnedBy"} {
		stamped, called := store.writeTimes[method]
		if !called {
			testInstance.Fatalf("%s was never called", method)
		}
		if !stamped.Equal(serviceNow) {
			testInstance.Fatalf("%s was handed %v, want the service clock %v", method, stamped, serviceNow)
		}
	}
}

func TestAppCheckStoreErrorsPassThrough(testInstance *testing.T) {
	boom := errors.New("boom")
	period := 600
	for _, test := range []struct {
		method string
		call   func(service *Service) error
	}{
		{method: "Create", call: func(service *Service) error {
			_, err := service.CreateForApp(context.Background(), checkAppA, validAppCheckInput())
			return err
		}},
		{method: "Get", call: func(service *Service) error {
			_, err := service.GetForApp(context.Background(), checkAppA, 1)
			return err
		}},
		{method: "PatchOwnedBy", call: func(service *Service) error {
			_, err := service.PatchForApp(context.Background(), checkAppA, 1, CheckPatch{PeriodSeconds: &period})
			return err
		}},
		{method: "DeleteOwnedBy", call: func(service *Service) error {
			return service.DeleteForApp(context.Background(), checkAppA, 1)
		}},
		{method: "PauseOwnedBy", call: func(service *Service) error {
			_, err := service.PauseForApp(context.Background(), checkAppA, 1)
			return err
		}},
		{method: "ResumeOwnedBy", call: func(service *Service) error {
			_, err := service.ResumeForApp(context.Background(), checkAppA, 1)
			return err
		}},
	} {
		store := newAppCheckStore(ownedCheck(1, checkAppA))
		store.errs = map[string]error{test.method: boom}
		service, logs := newLoggedAppCheckService(testInstance, store)

		err := test.call(service)

		if !errors.Is(err, boom) {
			testInstance.Fatalf("%s: err = %v, want the store error", test.method, err)
		}
		// A failed call must not be reported as a success in the log.
		for _, successLine := range []string{"created", "updated", "deleted", "paused", "resumed"} {
			if strings.Contains(logs.String(), successLine) {
				testInstance.Fatalf("%s failed but the log has %q:\n%s", test.method, successLine, logs.String())
			}
		}
	}
}

func TestAppCheckWritesLogTheAppAndTheCheck(testInstance *testing.T) {
	store := newAppCheckStore()
	service, logs := newLoggedAppCheckService(testInstance, store)
	ctx := context.Background()
	period := 600

	created, createErr := service.CreateForApp(ctx, checkAppA, validAppCheckInput())
	_, patchErr := service.PatchForApp(ctx, checkAppA, created.ID, CheckPatch{PeriodSeconds: &period})
	_, pauseErr := service.PauseForApp(ctx, checkAppA, created.ID)
	_, resumeErr := service.ResumeForApp(ctx, checkAppA, created.ID)
	deleteErr := service.DeleteForApp(ctx, checkAppA, created.ID)

	for label, err := range map[string]error{
		"create": createErr, "patch": patchErr, "pause": pauseErr, "resume": resumeErr, "delete": deleteErr,
	} {
		if err != nil {
			testInstance.Fatalf("%s: %v", label, err)
		}
	}
	for _, line := range []string{
		"app heartbeat check created", "app heartbeat check updated", "app heartbeat check paused",
		"app heartbeat check resumed", "app heartbeat check deleted",
	} {
		requireLogLine(testInstance, logs.String(), line, "app_id="+checkAppA, "check_id="+strconv.FormatInt(created.ID, 10))
	}
}

// A refused read answers exactly like an absent check; the Warn line is the only place an operator can see an
// app probing ids that belong to someone else.
func TestARefusedForeignGetForAppIsLogged(testInstance *testing.T) {
	store := newAppCheckStore(ownedCheck(7, checkAppB))
	service, logs := newLoggedAppCheckService(testInstance, store)

	_, err := service.GetForApp(context.Background(), checkAppA, 7)

	if !errors.Is(err, ErrCheckNotFound) {
		testInstance.Fatalf("err = %v, want ErrCheckNotFound", err)
	}
	requireLogLine(testInstance, logs.String(), "level=WARN", "app heartbeat check read refused",
		"app_id="+checkAppA, "check_id=7")
}

// requireLogLine requires one line of logged that contains every fragment, so a fragment cannot be satisfied by a
// different line.
func requireLogLine(testInstance *testing.T, logged string, fragments ...string) {
	testInstance.Helper()
	for _, line := range strings.Split(logged, "\n") {
		matches := true
		for _, fragment := range fragments {
			matches = matches && strings.Contains(line, fragment)
		}
		if matches {
			return
		}
	}
	testInstance.Fatalf("no log line contains all of %q in:\n%s", fragments, logged)
}

func TestAppCheckListErrorsPassThrough(testInstance *testing.T) {
	boom := errors.New("boom")
	store := newAppCheckStore()
	store.listErr = boom
	service := newAppCheckService(testInstance, store)

	if _, err := service.ListForApp(context.Background(), checkAppA); !errors.Is(err, boom) {
		testInstance.Fatalf("err = %v, want the store error", err)
	}
}
