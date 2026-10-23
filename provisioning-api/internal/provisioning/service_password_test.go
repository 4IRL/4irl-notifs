package provisioning

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/personsvc"
)

const (
	subscriberPassword = "abcd-efgh-ijkm-npqr"
	testTimeout        = 5 * time.Second
)

var provisionRequestForAlice = ProvisionRequest{AppID: "urls4irl", Email: aliceEmail}

func newStatefulPersonClient() *fakePersonClient {
	return &fakePersonClient{configuredValue: true, stateful: true}
}

func newLoggedService(ntfyClient *fakeNtfyClient, personClient *fakePersonClient, logger *slog.Logger) *Service {
	return NewService(ServiceConfig{
		Client:                     ntfyClient,
		GeneratePassword:           func() (string, error) { return "generated-pw", nil },
		GenerateSubscriberPassword: func() (string, error) { return subscriberPassword, nil },
		PersonClient:               personClient,
		Logger:                     logger,
	})
}

func newJSONLogger() (*slog.Logger, *bytes.Buffer) {
	var buffer bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buffer, nil)), &buffer
}

// logRecords decodes every JSON log line in the buffer.
func logRecords(t *testing.T, buffer *bytes.Buffer) []map[string]any {
	t.Helper()
	records := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buffer.String()), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line is not JSON: %q: %v", line, err)
		}
		records = append(records, record)
	}
	return records
}

func recordsWithMessage(records []map[string]any, message string) []map[string]any {
	matching := []map[string]any{}
	for _, record := range records {
		if record["msg"] == message {
			matching = append(matching, record)
		}
	}
	return matching
}

func stampOf(personClient *fakePersonClient) (string, bool) {
	personClient.mutex.Lock()
	defer personClient.mutex.Unlock()
	row, exists := personClient.rows[aliceHash]
	if !exists {
		return "", false
	}
	return row.stamp, true
}

func seedRow(personClient *fakePersonClient, stamp string) {
	if personClient.rows == nil {
		personClient.rows = map[string]*fakePersonRow{}
	}
	personClient.rows[aliceHash] = &fakePersonRow{stamp: stamp}
}

func indexOfInvocation(invocations []string, prefix string) int {
	for index, invocation := range invocations {
		if strings.HasPrefix(invocation, prefix) {
			return index
		}
	}
	return -1
}

func TestProvisionUnsetStampGeneratesAppliesThroughAddUserAndReturnsPassword(t *testing.T) {
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
	personClient := newStatefulPersonClient()
	service := newLoggedService(ntfyClient, personClient, nil)

	result, err := service.Provision(context.Background(), provisionRequestForAlice)
	if err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}
	if result.Password != subscriberPassword {
		t.Fatalf("Password = %q, expected the generated subscriber password", result.Password)
	}
	if got := strings.Join(ntfyClient.invocations, " | "); !strings.Contains(got, fmt.Sprintf("AddUser(%s,pw=%s)", aliceNtfyUser, subscriberPassword)) || strings.Contains(got, "ChangePassword") {
		t.Fatalf("expected AddUser with the subscriber password and no ChangePassword, got: %s", got)
	}
	if stamp, exists := stampOf(personClient); !exists || stamp == "" {
		t.Fatalf("expected a recorded stamp, got %q exists=%v", stamp, exists)
	}
}

func TestProvisionLegacyPersonWithExistingUserIsRotatedViaChangePassword(t *testing.T) {
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true, users: map[string]bool{aliceNtfyUser: true}}
	personClient := newStatefulPersonClient()
	service := newLoggedService(ntfyClient, personClient, nil)

	result, err := service.Provision(context.Background(), provisionRequestForAlice)
	if err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}
	if result.Password != subscriberPassword {
		t.Fatalf("Password = %q, expected the legacy person to be rotated and handed the new password", result.Password)
	}
	if got := strings.Join(ntfyClient.invocations, " | "); !strings.Contains(got, fmt.Sprintf("ChangePassword(%s,pw=%s)", aliceNtfyUser, subscriberPassword)) {
		t.Fatalf("expected the legacy rotation through ChangePassword, got: %s", got)
	}
}

func TestProvisionAlreadySetStampWithExistingUserLeavesPasswordAlone(t *testing.T) {
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true, users: map[string]bool{aliceNtfyUser: true}}
	personClient := newStatefulPersonClient()
	seedRow(personClient, "earlier-stamp")
	service := newLoggedService(ntfyClient, personClient, nil)

	result, err := service.Provision(context.Background(), provisionRequestForAlice)
	if err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}
	if result.Password != "" {
		t.Fatalf("Password = %q, expected none", result.Password)
	}
	if got := strings.Join(ntfyClient.invocations, " | "); strings.Contains(got, "ChangePassword") {
		t.Fatalf("an already-set stamp must not rotate the password, got: %s", got)
	}
	if stamp, _ := stampOf(personClient); stamp != "earlier-stamp" {
		t.Fatalf("stamp = %q, expected it untouched", stamp)
	}
}

func TestProvisionStaleStampSelfHealsClaimBeforeChangePassword(t *testing.T) {
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
	personClient := newStatefulPersonClient()
	personClient.recordTo = &ntfyClient.invocations
	seedRow(personClient, "stale-stamp")
	service := newLoggedService(ntfyClient, personClient, nil)

	result, err := service.Provision(context.Background(), provisionRequestForAlice)
	if err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}
	if result.Password != subscriberPassword {
		t.Fatalf("Password = %q, expected the self-heal to return a fresh password", result.Password)
	}
	invocations := ntfyClient.invocations
	addUserIndex := indexOfInvocation(invocations, "AddUser(")
	alwaysIndex := indexOfInvocation(invocations, fmt.Sprintf("ClaimPassword(%s,alice@example.com,always)", aliceHash))
	changeIndex := indexOfInvocation(invocations, "ChangePassword(")
	if addUserIndex < 0 || alwaysIndex < addUserIndex || changeIndex < alwaysIndex {
		t.Fatalf("expected AddUser, then the always claim, then ChangePassword, got: %s", strings.Join(invocations, " | "))
	}
	if !strings.Contains(invocations[addUserIndex], "pw=generated-pw") {
		t.Fatalf("expected the throwaway password at AddUser, got: %s", invocations[addUserIndex])
	}
	if !strings.Contains(invocations[changeIndex], "pw="+subscriberPassword) {
		t.Fatalf("expected ChangePassword with the subscriber password, got: %s", invocations[changeIndex])
	}
	if stamp, _ := stampOf(personClient); stamp != "stamp-1" {
		t.Fatalf("stamp = %q, expected the always claim's stamp-1", stamp)
	}
}

func TestProvisionWithoutUsableStoreNeverRotatesHealsOrReturnsPassword(t *testing.T) {
	cases := []struct {
		name         string
		personClient func() *fakePersonClient
		userExists   bool
	}{
		{"claim error, new user", func() *fakePersonClient {
			client := newStatefulPersonClient()
			client.claimErr = errors.New("claim boom")
			return client
		}, false},
		{"claim error, existing user", func() *fakePersonClient {
			client := newStatefulPersonClient()
			client.claimErr = errors.New("claim boom")
			return client
		}, true},
		{"unconfigured client", func() *fakePersonClient { return &fakePersonClient{configuredValue: false} }, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
			if testCase.userExists {
				ntfyClient.users = map[string]bool{aliceNtfyUser: true}
			}
			service := newLoggedService(ntfyClient, testCase.personClient(), nil)

			result, err := service.Provision(context.Background(), provisionRequestForAlice)
			if err != nil {
				t.Fatalf("Provision must still succeed, got: %v", err)
			}
			if result.Password != "" || result.Token != "tk_new" {
				t.Fatalf("expected a token and no password, got %#v", result)
			}
			got := strings.Join(ntfyClient.invocations, " | ")
			if strings.Contains(got, "ChangePassword") || strings.Contains(got, subscriberPassword) {
				t.Fatalf("no rotation or self-heal without a usable store, got: %s", got)
			}
			if !testCase.userExists && !strings.Contains(got, "AddUser("+aliceNtfyUser+",pw=generated-pw)") {
				t.Fatalf("expected the throwaway password at AddUser, got: %s", got)
			}
		})
	}
}

func TestProvisionAmbiguousClaimFailureReleasesTheClientStamp(t *testing.T) {
	ambiguousErrors := map[string]error{
		"transport error": errors.New("person-service: request failed: i/o timeout"),
		"timeout":         fmt.Errorf("person-service: request failed: %w", context.DeadlineExceeded),
		"gateway timeout": &personsvc.StatusError{Status: 504, Snippet: "upstream timeout"},
	}
	for name, claimErr := range ambiguousErrors {
		t.Run(name, func(t *testing.T) {
			personClient := newStatefulPersonClient()
			personClient.claimErrByMode = map[personsvc.ClaimMode]error{personsvc.ClaimIfUnset: claimErr}
			ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
			service := newLoggedService(ntfyClient, personClient, nil)

			result, err := service.Provision(context.Background(), provisionRequestForAlice)
			if err != nil {
				t.Fatalf("Provision must still succeed (store unavailable), got: %v", err)
			}
			if result.Password != "" {
				t.Fatalf("no password may be returned, got %q", result.Password)
			}
			if len(personClient.claimStamps) != 1 {
				t.Fatalf("claim stamps = %v, expected exactly one claim", personClient.claimStamps)
			}
			stamp := personClient.claimStamps[0]
			if _, parseErr := time.Parse(personsvc.ClaimStampLayout, stamp); parseErr != nil || stamp == "" {
				t.Fatalf("claim stamp %q is not a canonical ISO timestamp: %v", stamp, parseErr)
			}
			expectedRelease := fmt.Sprintf("ReleasePasswordClaim(%s,%s,)", aliceHash, stamp)
			if !strings.Contains(strings.Join(personClient.invocations, " | "), expectedRelease) {
				t.Fatalf("expected %s with the same stamp the claim sent, got: %v", expectedRelease, personClient.invocations)
			}
		})
	}
}

func TestProvisionDefiniteClaimRejectionDoesNotRelease(t *testing.T) {
	personClient := newStatefulPersonClient()
	personClient.claimErrByMode = map[personsvc.ClaimMode]error{
		personsvc.ClaimIfUnset: &personsvc.StatusError{Status: 400, Snippet: "invalid email"},
	}
	service := newLoggedService(&fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}, personClient, nil)

	if _, err := service.Provision(context.Background(), provisionRequestForAlice); err != nil {
		t.Fatalf("Provision must still succeed, got: %v", err)
	}
	if hasInvocationPrefix(snapshotInvocations(&personClient.invocations), "ReleasePasswordClaim(") {
		t.Fatalf("a definite rejection must not release, got: %v", personClient.invocations)
	}
}

func TestProvisionSuccessfulClaimSendsStampAndDoesNotReleaseOnSuccess(t *testing.T) {
	personClient := newStatefulPersonClient()
	service := newLoggedService(&fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}, personClient, nil)

	if _, err := service.Provision(context.Background(), provisionRequestForAlice); err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}
	if len(personClient.claimStamps) == 0 || personClient.claimStamps[0] == "" {
		t.Fatalf("the if_unset claim must send a client stamp, got %v", personClient.claimStamps)
	}
	if hasInvocationPrefix(snapshotInvocations(&personClient.invocations), "ReleasePasswordClaim(") {
		t.Fatalf("a successful provision must not release, got: %v", personClient.invocations)
	}
}

// failingProvisionStep arms one failing Provision step and returns the function
// that heals it.
type failingProvisionStep struct {
	name string
	fail func(client *fakeNtfyClient) (heal func())
}

func failingProvisionSteps() []failingProvisionStep {
	return []failingProvisionStep{
		{"apply (AddUser)", func(client *fakeNtfyClient) func() {
			client.addUserErr = errors.New("add user failed")
			return func() { client.addUserErr = nil }
		}},
		{"scoped GrantAccess", func(client *fakeNtfyClient) func() {
			client.grantAccessErr = errors.New("grant failed")
			return func() { client.grantAccessErr = nil }
		}},
		{"broadcast GrantAccess", func(client *fakeNtfyClient) func() {
			client.grantAccessErrByPattern = map[string]error{"urls4irl-broadcast": errors.New("broadcast grant failed")}
			return func() { client.grantAccessErrByPattern = nil }
		}},
		{"ListTokens", func(client *fakeNtfyClient) func() {
			client.listTokensErr = errors.New("list tokens failed")
			return func() { client.listTokensErr = nil }
		}},
		{"RemoveToken", func(client *fakeNtfyClient) func() {
			client.listTokens = []ntfycli.Token{{Value: "tk_old", Label: "urls4irl"}}
			client.removeTokenErr = errors.New("remove token failed")
			return func() { client.removeTokenErr = nil }
		}},
		{"AddToken", func(client *fakeNtfyClient) func() {
			client.addTokenErr = errors.New("add token failed")
			return func() { client.addTokenErr = nil }
		}},
	}
}

func TestProvisionFailureAfterClaimReleasesToNullAndNextProvisionReturnsPassword(t *testing.T) {
	for _, step := range failingProvisionSteps() {
		t.Run(step.name, func(t *testing.T) {
			ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
			personClient := newStatefulPersonClient()
			service := newLoggedService(ntfyClient, personClient, nil)
			heal := step.fail(ntfyClient)

			if _, err := service.Provision(context.Background(), provisionRequestForAlice); err == nil {
				t.Fatal("expected the failing step's error")
			}
			if stamp, exists := stampOf(personClient); !exists || stamp != "" {
				t.Fatalf("stamp = %q exists=%v, expected the orphan row with a NULL stamp", stamp, exists)
			}
			expectedRelease := fmt.Sprintf("ReleasePasswordClaim(%s,stamp-1,)", aliceHash)
			if indexOfInvocation(personClient.invocations, expectedRelease) < 0 {
				t.Fatalf("expected %s, got: %v", expectedRelease, personClient.invocations)
			}

			heal()
			result, err := service.Provision(context.Background(), provisionRequestForAlice)
			if err != nil {
				t.Fatalf("retry returned unexpected error: %v", err)
			}
			if result.Password != subscriberPassword {
				t.Fatalf("retry Password = %q, expected the next Provision to claim again and return a password", result.Password)
			}
			if len(personClient.rows) != 1 {
				t.Fatalf("expected the orphan row to be reused, got %d rows", len(personClient.rows))
			}
		})
	}
}

func TestProvisionLegacyRotationFailureEndsWithNullStamp(t *testing.T) {
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true, users: map[string]bool{aliceNtfyUser: true}, changePasswordErr: errors.New("change failed")}
	personClient := newStatefulPersonClient()
	service := newLoggedService(ntfyClient, personClient, nil)

	if _, err := service.Provision(context.Background(), provisionRequestForAlice); err == nil {
		t.Fatal("expected the ChangePassword error")
	}
	if stamp, _ := stampOf(personClient); stamp != "" {
		t.Fatalf("stamp = %q, expected NULL", stamp)
	}
	ntfyClient.changePasswordErr = nil
	result, err := service.Provision(context.Background(), provisionRequestForAlice)
	if err != nil || result.Password != subscriberPassword {
		t.Fatalf("retry = %#v, %v, expected a password", result, err)
	}
}

func TestProvisionSelfHealSubStepFailures(t *testing.T) {
	t.Run("always claim error changes nothing and returns no password", func(t *testing.T) {
		ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
		personClient := newStatefulPersonClient()
		personClient.claimErrByMode = map[personsvc.ClaimMode]error{personsvc.ClaimAlways: errors.New("always boom")}
		seedRow(personClient, "stale-stamp")
		service := newLoggedService(ntfyClient, personClient, nil)

		result, err := service.Provision(context.Background(), provisionRequestForAlice)
		if err != nil {
			t.Fatalf("Provision must succeed, got: %v", err)
		}
		if result.Password != "" {
			t.Fatalf("Password = %q, expected none", result.Password)
		}
		if got := strings.Join(ntfyClient.invocations, " | "); strings.Contains(got, "ChangePassword") {
			t.Fatalf("a failed always claim must not change the password, got: %s", got)
		}
	})

	t.Run("ChangePassword failure after the claim releases to NULL not the stale stamp", func(t *testing.T) {
		ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true, changePasswordErr: errors.New("change failed")}
		personClient := newStatefulPersonClient()
		seedRow(personClient, "stale-stamp")
		service := newLoggedService(ntfyClient, personClient, nil)

		if _, err := service.Provision(context.Background(), provisionRequestForAlice); err == nil {
			t.Fatal("expected the ChangePassword error")
		}
		if stamp, _ := stampOf(personClient); stamp != "" {
			t.Fatalf("stamp = %q, expected NULL (not restored to the stale stamp)", stamp)
		}
		ntfyClient.changePasswordErr = nil
		result, err := service.Provision(context.Background(), provisionRequestForAlice)
		if err != nil || result.Password != subscriberPassword {
			t.Fatalf("retry = %#v, %v, expected a password", result, err)
		}
	})
}

func TestProvisionNewerClaimIsNotUndoneByRelease(t *testing.T) {
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true, grantAccessErr: errors.New("grant failed")}
	personClient := newStatefulPersonClient()
	ntfyClient.addUserHook = func() {
		// A newer claim lands while this Provision is applying.
		if _, err := personClient.ClaimPassword(context.Background(), aliceHash, aliceEmail, personsvc.ClaimAlways); err != nil {
			t.Errorf("newer claim failed: %v", err)
		}
	}
	service := newLoggedService(ntfyClient, personClient, nil)

	if _, err := service.Provision(context.Background(), provisionRequestForAlice); err == nil {
		t.Fatal("expected the grant error")
	}
	if stamp, _ := stampOf(personClient); stamp != "stamp-2" {
		t.Fatalf("stamp = %q, expected the newer stamp-2 to survive the release", stamp)
	}
}

func TestProvisionLaterStepFailureAfterHealOrRotationReleasesToNull(t *testing.T) {
	setups := map[string]func(ntfyClient *fakeNtfyClient, personClient *fakePersonClient){
		"self-heal (stale stamp, no user)": func(_ *fakeNtfyClient, personClient *fakePersonClient) {
			seedRow(personClient, "stale-stamp")
		},
		"legacy rotation (no row, existing user)": func(ntfyClient *fakeNtfyClient, _ *fakePersonClient) {
			ntfyClient.users = map[string]bool{aliceNtfyUser: true}
		},
	}
	for setupName, setup := range setups {
		for _, step := range failingProvisionSteps() {
			if step.name == "apply (AddUser)" {
				continue
			}
			t.Run(setupName+"/"+step.name, func(t *testing.T) {
				ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
				personClient := newStatefulPersonClient()
				setup(ntfyClient, personClient)
				service := newLoggedService(ntfyClient, personClient, nil)
				step.fail(ntfyClient)

				if _, err := service.Provision(context.Background(), provisionRequestForAlice); err == nil {
					t.Fatal("expected the failing step's error")
				}
				if stamp, _ := stampOf(personClient); stamp != "" {
					t.Fatalf("stamp = %q, expected NULL (never the stale stamp)", stamp)
				}
			})
		}
	}
}

func TestProvisionNewerClaimSurvivesFailedChangePassword(t *testing.T) {
	setups := map[string]func(ntfyClient *fakeNtfyClient, personClient *fakePersonClient){
		"self-heal": func(_ *fakeNtfyClient, personClient *fakePersonClient) { seedRow(personClient, "stale-stamp") },
		"legacy rotation": func(ntfyClient *fakeNtfyClient, _ *fakePersonClient) {
			ntfyClient.users = map[string]bool{aliceNtfyUser: true}
		},
	}
	for name, setup := range setups {
		t.Run(name, func(t *testing.T) {
			ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
			personClient := newStatefulPersonClient()
			setup(ntfyClient, personClient)
			ntfyClient.changePasswordHook = func() error {
				if _, err := personClient.ClaimPassword(context.Background(), aliceHash, aliceEmail, personsvc.ClaimAlways); err != nil {
					t.Errorf("newer claim failed: %v", err)
				}
				return errors.New("change failed")
			}
			service := newLoggedService(ntfyClient, personClient, nil)

			if _, err := service.Provision(context.Background(), provisionRequestForAlice); err == nil {
				t.Fatal("expected the ChangePassword error")
			}
			if stamp, _ := stampOf(personClient); stamp != "stamp-2" {
				t.Fatalf("stamp = %q, expected the newer stamp-2 to survive", stamp)
			}
		})
	}
}

func TestProvisionSecondAppGetsNoPassword(t *testing.T) {
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
	service := newLoggedService(ntfyClient, newStatefulPersonClient(), nil)

	first, err := service.Provision(context.Background(), provisionRequestForAlice)
	if err != nil || first.Password == "" {
		t.Fatalf("first provision = %#v, %v, expected a password", first, err)
	}
	second, err := service.Provision(context.Background(), ProvisionRequest{AppID: "chores4irl", Email: aliceEmail})
	if err != nil {
		t.Fatalf("second provision returned unexpected error: %v", err)
	}
	if second.Password != "" {
		t.Fatalf("second app Password = %q, expected none", second.Password)
	}
}

func TestConcurrentProvisionsForOnePersonGenerateOnce(t *testing.T) {
	var generatorCalls atomic.Int32
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
	service := NewService(ServiceConfig{
		Client:           ntfyClient,
		GeneratePassword: func() (string, error) { return "generated-pw", nil },
		GenerateSubscriberPassword: func() (string, error) {
			generatorCalls.Add(1)
			return subscriberPassword, nil
		},
		PersonClient: newStatefulPersonClient(),
	})

	const callers = 8
	// Hold the claimer inside its ntfy apply until every other caller is queued
	// on the person lock, so only the lock keeps them from claiming/healing.
	var gateOnce sync.Once
	ntfyClient.addUserHook = func() {
		gateOnce.Do(func() { waitForLockWaiters(t, service, callers) })
	}
	results := make(chan ProvisionResult, callers)
	var waitGroup sync.WaitGroup
	for range callers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := service.Provision(context.Background(), provisionRequestForAlice)
			if err != nil {
				t.Errorf("Provision returned unexpected error: %v", err)
			}
			results <- result
		}()
	}
	waitGroup.Wait()
	close(results)

	passwords := 0
	for result := range results {
		if result.Password != "" {
			passwords++
		}
	}
	if passwords != 1 || generatorCalls.Load() != 1 {
		t.Fatalf("passwords returned = %d, generator calls = %d, expected exactly one of each", passwords, generatorCalls.Load())
	}
}

func TestProvisionClaimErrorIsLoggedAtErrorLevelWithoutEmailOrPassword(t *testing.T) {
	logger, buffer := newJSONLogger()
	personClient := newStatefulPersonClient()
	personClient.claimErr = errors.New("claim boom")
	service := newLoggedService(&fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}, personClient, logger)

	if _, err := service.Provision(context.Background(), provisionRequestForAlice); err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}
	failures := recordsWithMessage(logRecords(t, buffer), "password claim failed")
	if len(failures) != 1 || failures[0]["level"] != "ERROR" || failures[0]["person_hash"] != aliceHash || !strings.Contains(fmt.Sprint(failures[0]["error"]), "claim boom") {
		t.Fatalf("expected one Error-level claim failure with person_hash and error text, got %v", failures)
	}
	for _, secret := range []string{aliceEmail, subscriberPassword, "generated-pw"} {
		if strings.Contains(buffer.String(), secret) {
			t.Fatalf("log output leaked %q: %s", secret, buffer.String())
		}
	}
}

func TestProvisionOutcomeLogging(t *testing.T) {
	cases := []struct {
		name    string
		outcome string
		setup   func(ntfyClient *fakeNtfyClient, personClient *fakePersonClient)
	}{
		{"fresh claim", "generated", func(*fakeNtfyClient, *fakePersonClient) {}},
		{"legacy rotation", "generated", func(ntfyClient *fakeNtfyClient, _ *fakePersonClient) {
			ntfyClient.users = map[string]bool{aliceNtfyUser: true}
		}},
		{"already set", "already_set", func(ntfyClient *fakeNtfyClient, personClient *fakePersonClient) {
			ntfyClient.users = map[string]bool{aliceNtfyUser: true}
			seedRow(personClient, "earlier-stamp")
		}},
		{"claim error", "store_unavailable", func(_ *fakeNtfyClient, personClient *fakePersonClient) {
			personClient.claimErr = errors.New("claim boom")
		}},
		{"unconfigured client", "store_unavailable", func(_ *fakeNtfyClient, personClient *fakePersonClient) {
			personClient.configuredValue = false
		}},
		{"always claim error", "store_unavailable", func(_ *fakeNtfyClient, personClient *fakePersonClient) {
			personClient.claimErrByMode = map[personsvc.ClaimMode]error{personsvc.ClaimAlways: errors.New("always boom")}
			seedRow(personClient, "stale-stamp")
		}},
		{"self-heal", "healed", func(_ *fakeNtfyClient, personClient *fakePersonClient) {
			seedRow(personClient, "stale-stamp")
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logger, buffer := newJSONLogger()
			ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
			personClient := newStatefulPersonClient()
			testCase.setup(ntfyClient, personClient)
			service := newLoggedService(ntfyClient, personClient, logger)

			if _, err := service.Provision(context.Background(), provisionRequestForAlice); err != nil {
				t.Fatalf("Provision returned unexpected error: %v", err)
			}
			decisions := recordsWithMessage(logRecords(t, buffer), "subscriber password decision")
			if len(decisions) != 1 || decisions[0]["outcome"] != testCase.outcome || decisions[0]["person_hash"] != aliceHash {
				t.Fatalf("expected one decision line with outcome %q, got %v", testCase.outcome, decisions)
			}
		})
	}
}

func TestProvisionFailedReleaseIsLoggedAtErrorLevel(t *testing.T) {
	steps := append(failingProvisionSteps(), failingProvisionStep{"self-heal ChangePassword", func(client *fakeNtfyClient) func() {
		client.changePasswordErr = errors.New("change failed")
		return func() {}
	}})
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			logger, buffer := newJSONLogger()
			ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
			personClient := newStatefulPersonClient()
			personClient.releaseClaimErr = errors.New("release boom")
			if step.name == "self-heal ChangePassword" {
				seedRow(personClient, "stale-stamp")
			}
			service := newLoggedService(ntfyClient, personClient, logger)
			step.fail(ntfyClient)

			if _, err := service.Provision(context.Background(), provisionRequestForAlice); err == nil {
				t.Fatal("expected the failing step's error")
			}
			failures := recordsWithMessage(logRecords(t, buffer), "password claim release failed")
			if len(failures) != 1 || failures[0]["level"] != "ERROR" || failures[0]["person_hash"] != aliceHash || !strings.Contains(fmt.Sprint(failures[0]["error"]), "release boom") {
				t.Fatalf("expected one Error-level release failure with person_hash and error text, got %v", failures)
			}
		})
	}
}

func TestSetPasswordRecordsStampBeforeChangingPassword(t *testing.T) {
	ntfyClient := &fakeNtfyClient{}
	service, personClient := newSetPasswordService(ntfyClient)
	personClient.recordTo = &ntfyClient.invocations

	if _, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "tasktracker", Email: aliceEmail, Password: "sekrit-pw"}); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}
	expected := fmt.Sprintf("ClaimPassword(%s,alice@example.com,update_only) | ChangePassword(%s,pw=sekrit-pw)", aliceHash, aliceNtfyUser)
	if got := strings.Join(ntfyClient.invocations, " | "); got != expected {
		t.Fatalf("invocations = %s, expected %s", got, expected)
	}
}

func TestSetPasswordRestoresPriorStampWhenChangePasswordFails(t *testing.T) {
	ntfyClient := &fakeNtfyClient{changePasswordErr: errors.New("change failed")}
	service, personClient := newSetPasswordService(ntfyClient)

	if _, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "tasktracker", Email: aliceEmail, Password: "sekrit-pw"}); err == nil {
		t.Fatal("expected the ChangePassword error")
	}
	if stamp, _ := stampOf(personClient); stamp != "earlier-stamp" {
		t.Fatalf("stamp = %q, expected the prior stamp restored", stamp)
	}
}

func TestSetPasswordReleaseDoesNotUndoANewerStamp(t *testing.T) {
	ntfyClient := &fakeNtfyClient{}
	service, personClient := newSetPasswordService(ntfyClient)
	ntfyClient.changePasswordHook = func() error {
		if _, err := personClient.ClaimPassword(context.Background(), aliceHash, aliceEmail, personsvc.ClaimAlways); err != nil {
			t.Errorf("newer claim failed: %v", err)
		}
		return errors.New("change failed")
	}

	if _, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "tasktracker", Email: aliceEmail, Password: "sekrit-pw"}); err == nil {
		t.Fatal("expected the ChangePassword error")
	}
	if stamp, _ := stampOf(personClient); stamp != "stamp-2" {
		t.Fatalf("stamp = %q, expected the newer stamp-2 to survive", stamp)
	}
}

func TestSetPasswordWithoutRecordableStampSkipsChangePasswordAndErrors(t *testing.T) {
	t.Run("claim failure", func(t *testing.T) {
		ntfyClient := &fakeNtfyClient{}
		service, personClient := newSetPasswordService(ntfyClient)
		personClient.claimErr = errors.New("claim boom")

		if _, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "tasktracker", Email: aliceEmail, Password: "sekrit-pw"}); err == nil {
			t.Fatal("expected an error")
		}
		if len(ntfyClient.invocations) != 0 {
			t.Fatalf("ChangePassword must not run, got: %v", ntfyClient.invocations)
		}
	})
	t.Run("unconfigured client", func(t *testing.T) {
		ntfyClient := &fakeNtfyClient{}
		service := newLoggedService(ntfyClient, &fakePersonClient{configuredValue: false}, nil)

		_, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "tasktracker", Email: aliceEmail, Password: "sekrit-pw"})
		if !errors.Is(err, ErrPersonStoreUnavailable) {
			t.Fatalf("err = %v, expected ErrPersonStoreUnavailable", err)
		}
		if len(ntfyClient.invocations) != 0 {
			t.Fatalf("ChangePassword must not run, got: %v", ntfyClient.invocations)
		}
	})
	t.Run("nil client", func(t *testing.T) {
		ntfyClient := &fakeNtfyClient{}
		service := newTestService(ntfyClient)

		if _, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "tasktracker", Email: aliceEmail, Password: "sekrit-pw"}); err == nil {
			t.Fatal("expected an error")
		}
		if len(ntfyClient.invocations) != 0 {
			t.Fatalf("ChangePassword must not run, got: %v", ntfyClient.invocations)
		}
	})
}

func TestSetPasswordForUserWithoutRowRecordsAfterwardsAndUnknownUserCreatesNoRow(t *testing.T) {
	t.Run("row-less existing user", func(t *testing.T) {
		ntfyClient := &fakeNtfyClient{}
		personClient := newStatefulPersonClient()
		personClient.recordTo = &ntfyClient.invocations
		service := newLoggedService(ntfyClient, personClient, nil)

		if _, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "tasktracker", Email: aliceEmail, Password: "sekrit-pw"}); err != nil {
			t.Fatalf("SetPassword returned unexpected error: %v", err)
		}
		expected := fmt.Sprintf("ClaimPassword(%s,alice@example.com,update_only) | ChangePassword(%s,pw=sekrit-pw) | ClaimPassword(%s,alice@example.com,always)", aliceHash, aliceNtfyUser, aliceHash)
		if got := strings.Join(ntfyClient.invocations, " | "); got != expected {
			t.Fatalf("invocations = %s, expected %s", got, expected)
		}
		if stamp, exists := stampOf(personClient); !exists || stamp == "" {
			t.Fatalf("expected a row stamped after the change, got %q exists=%v", stamp, exists)
		}
	})
	t.Run("unknown ntfy user", func(t *testing.T) {
		ntfyClient := &fakeNtfyClient{changePasswordErr: fmt.Errorf("ntfy user: %w: no such user", ntfycli.ErrNotFound)}
		personClient := newStatefulPersonClient()
		service := newLoggedService(ntfyClient, personClient, nil)

		_, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "tasktracker", Email: aliceEmail, Password: "sekrit-pw"})
		if !errors.Is(err, ntfycli.ErrNotFound) {
			t.Fatalf("err = %v, expected ErrNotFound", err)
		}
		if len(personClient.rows) != 0 {
			t.Fatalf("no person row may be created for an unknown user, got %d", len(personClient.rows))
		}
	})
	t.Run("record after change fails but the call succeeds", func(t *testing.T) {
		logger, buffer := newJSONLogger()
		personClient := newStatefulPersonClient()
		personClient.claimErrByMode = map[personsvc.ClaimMode]error{personsvc.ClaimAlways: errors.New("always boom")}
		service := newLoggedService(&fakeNtfyClient{}, personClient, logger)

		if _, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "tasktracker", Email: aliceEmail, Password: "sekrit-pw"}); err != nil {
			t.Fatalf("the applied change must still succeed, got: %v", err)
		}
		failures := recordsWithMessage(logRecords(t, buffer), "password stamp record after change failed")
		if len(failures) != 1 || failures[0]["level"] != "ERROR" || failures[0]["person_hash"] != aliceHash {
			t.Fatalf("expected one Error-level record failure, got %v", failures)
		}
	})
}

func lockedKeyCount(service *Service) int {
	service.personLocks.guard.Lock()
	defer service.personLocks.guard.Unlock()
	return len(service.personLocks.entries)
}

func lockWaiters(service *Service, key string) int {
	service.personLocks.guard.Lock()
	defer service.personLocks.guard.Unlock()
	if entry := service.personLocks.entries[key]; entry != nil {
		return entry.waiters
	}
	return 0
}

// waitForLockWaiters spins (no sleeps) until want goroutines hold or wait for
// the key's lock.
func waitForLockWaiters(t *testing.T, service *Service, want int) {
	t.Helper()
	deadline := time.Now().Add(testTimeout)
	for lockWaiters(service, aliceHash) < want {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d lock holders/waiters", want)
		}
		runtime.Gosched()
	}
}

func waitForEntered(t *testing.T, entered <-chan struct{}) {
	t.Helper()
	select {
	case <-entered:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for the gated call to start")
	}
}

func waitForResult(t *testing.T, done <-chan error, what string) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s returned unexpected error: %v", what, err)
		}
	case <-time.After(testTimeout):
		t.Fatalf("%s timed out", what)
	}
}

func TestDeleteUserReturnsPersonStoreFailureForPersonIds(t *testing.T) {
	ntfyClient := &fakeNtfyClient{}
	personClient := newStatefulPersonClient()
	personClient.deletePersonErr = errors.New("person delete failed")
	service := newLoggedService(ntfyClient, personClient, nil)

	if err := service.DeleteUser(context.Background(), aliceNtfyUser); !errors.Is(err, personClient.deletePersonErr) {
		t.Fatalf("err = %v, expected the person-store delete error", err)
	}
}

func TestDeleteUserSucceedsWhenPersonStoreUnconfigured(t *testing.T) {
	for name, personClient := range map[string]PersonServiceClient{
		"unconfigured": &fakePersonClient{configuredValue: false, deletePersonErr: errors.New("must not be called")},
	} {
		t.Run(name, func(t *testing.T) {
			service := NewService(ServiceConfig{Client: &fakeNtfyClient{}, PersonClient: personClient})
			if err := service.DeleteUser(context.Background(), aliceNtfyUser); err != nil {
				t.Fatalf("DeleteUser returned unexpected error: %v", err)
			}
		})
	}
	service := NewService(ServiceConfig{Client: &fakeNtfyClient{}})
	if err := service.DeleteUser(context.Background(), aliceNtfyUser); err != nil {
		t.Fatalf("DeleteUser with no client returned unexpected error: %v", err)
	}
}

func TestDeleteUserForNonHashIdsOnlyDeletesNtfyUserWithoutLock(t *testing.T) {
	for _, userID := range []string{"u_test", "urls4irl-publisher", "u_" + strings.ToUpper(aliceHash), "u_" + aliceHash + "x"} {
		t.Run(userID, func(t *testing.T) {
			personClient := newStatefulPersonClient()
			personClient.deletePersonErr = errors.New("must not be called")
			ntfyClient := &fakeNtfyClient{}
			service := newLoggedService(ntfyClient, personClient, nil)
			lockedDuringDelete := -1
			ntfyClient.deleteUserHook = func() { lockedDuringDelete = lockedKeyCount(service) }

			if err := service.DeleteUser(context.Background(), userID); err != nil {
				t.Fatalf("DeleteUser returned unexpected error: %v", err)
			}
			if lockedDuringDelete != 0 {
				t.Fatalf("held %d person locks during the ntfy delete, expected none", lockedDuringDelete)
			}
			if indexOfInvocation(personClient.invocations, "DeletePerson(") >= 0 {
				t.Fatalf("DeletePerson must not be called, got: %v", personClient.invocations)
			}
		})
	}
}

func TestDeleteUserForPersonIdHoldsPersonLock(t *testing.T) {
	ntfyClient := &fakeNtfyClient{}
	service := newLoggedService(ntfyClient, newStatefulPersonClient(), nil)
	lockedDuringDelete := -1
	ntfyClient.deleteUserHook = func() { lockedDuringDelete = lockedKeyCount(service) }

	if err := service.DeleteUser(context.Background(), aliceNtfyUser); err != nil {
		t.Fatalf("DeleteUser returned unexpected error: %v", err)
	}
	if lockedDuringDelete != 1 {
		t.Fatalf("held %d person locks during the ntfy delete, expected 1", lockedDuringDelete)
	}
	if lockedKeyCount(service) != 0 {
		t.Fatal("lock entry leaked after DeleteUser returned")
	}
}

func TestDeprovisionLastAppTeardownStaysBestEffortWhenPersonDeleteFails(t *testing.T) {
	logger, buffer := newJSONLogger()
	ntfyClient := &fakeNtfyClient{listUsers: []ntfycli.User{{Name: aliceNtfyUser, TopicPatterns: nil}}}
	personClient := newStatefulPersonClient()
	personClient.deletePersonErr = errors.New("person delete failed")
	service := newLoggedService(ntfyClient, personClient, logger)

	if err := service.Deprovision(context.Background(), DeprovisionRequest{AppID: "urls4irl", NtfyUserID: aliceNtfyUser}); err != nil {
		t.Fatalf("Deprovision must still succeed, got: %v", err)
	}
	warnings := recordsWithMessage(logRecords(t, buffer), "person-service dual-delete (person) failed")
	if len(warnings) != 1 || warnings[0]["level"] != "WARN" {
		t.Fatalf("expected one Warn line for the swallowed person delete, got %v", warnings)
	}
}

func TestDeleteUserWaitsForProvisionOfSamePerson(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var gateOnce sync.Once
	var deleteRan atomic.Bool
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
	ntfyClient.addUserHook = func() {
		gateOnce.Do(func() {
			close(entered)
			<-release
		})
	}
	ntfyClient.deleteUserHook = func() { deleteRan.Store(true) }
	service := newLoggedService(ntfyClient, newStatefulPersonClient(), nil)

	provisionDone := make(chan error, 1)
	go func() {
		_, err := service.Provision(context.Background(), provisionRequestForAlice)
		provisionDone <- err
	}()
	waitForEntered(t, entered)

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- service.DeleteUser(context.Background(), aliceNtfyUser) }()
	waitForLockWaiters(t, service, 2)
	if deleteRan.Load() {
		t.Fatal("DeleteUser ran its ntfy delete while Provision held the person lock")
	}

	close(release)
	waitForResult(t, provisionDone, "Provision")
	waitForResult(t, deleteDone, "DeleteUser")
	invocations := ntfyClient.invocations
	if indexOfInvocation(invocations, "AddToken(") < 0 || indexOfInvocation(invocations, "AddToken(") > indexOfInvocation(invocations, "DeleteUser(") {
		t.Fatalf("expected Provision to finish before the delete, got: %s", strings.Join(invocations, " | "))
	}
}

func TestProvisionWaitsForDeleteUserOfSamePerson(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var gateOnce sync.Once
	var addUserRan atomic.Bool
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
	ntfyClient.deleteUserHook = func() {
		gateOnce.Do(func() {
			close(entered)
			<-release
		})
	}
	ntfyClient.addUserHook = func() { addUserRan.Store(true) }
	service := newLoggedService(ntfyClient, newStatefulPersonClient(), nil)

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- service.DeleteUser(context.Background(), aliceNtfyUser) }()
	waitForEntered(t, entered)

	provisionDone := make(chan error, 1)
	go func() {
		_, err := service.Provision(context.Background(), provisionRequestForAlice)
		provisionDone <- err
	}()
	waitForLockWaiters(t, service, 2)
	if addUserRan.Load() {
		t.Fatal("Provision applied while DeleteUser held the person lock")
	}

	close(release)
	waitForResult(t, deleteDone, "DeleteUser")
	waitForResult(t, provisionDone, "Provision")
}

func TestDeprovisionLastAppTeardownWaitsForProvisionOfSamePerson(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var gateOnce sync.Once
	var teardownDeleted atomic.Bool
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true, listUsers: []ntfycli.User{{Name: aliceNtfyUser}}}
	ntfyClient.addUserHook = func() {
		gateOnce.Do(func() {
			close(entered)
			<-release
		})
	}
	ntfyClient.deleteUserHook = func() { teardownDeleted.Store(true) }
	service := newLoggedService(ntfyClient, newStatefulPersonClient(), nil)

	provisionDone := make(chan error, 1)
	go func() {
		_, err := service.Provision(context.Background(), provisionRequestForAlice)
		provisionDone <- err
	}()
	waitForEntered(t, entered)

	deprovisionDone := make(chan error, 1)
	go func() {
		deprovisionDone <- service.Deprovision(context.Background(), DeprovisionRequest{AppID: "urls4irl", NtfyUserID: aliceNtfyUser})
	}()
	waitForLockWaiters(t, service, 2)
	if teardownDeleted.Load() {
		t.Fatal("last-app teardown deleted the user while Provision held the person lock")
	}

	close(release)
	waitForResult(t, provisionDone, "Provision")
	waitForResult(t, deprovisionDone, "Deprovision")
}

func TestDeprovisionAppWithLastAppTeardownsDoesNotDeadlock(t *testing.T) {
	const (
		hashTwo   = "abcdefghijklmnop"
		hashThree = "qrstuvwxyzabcdef"
	)
	subscribers := []string{aliceNtfyUser, "u_" + hashTwo, "u_" + hashThree}
	var listCalls atomic.Int32
	ntfyClient := &fakeNtfyClient{}
	ntfyClient.listUsersFunc = func() []ntfycli.User {
		users := []ntfycli.User{}
		callNumber := listCalls.Add(1)
		for _, subscriber := range subscribers {
			patterns := []string{"myapp-" + strings.TrimPrefix(subscriber, "u_") + "-*"}
			if callNumber > 1 {
				// Every per-subscriber Deprovision then finds the user left with
				// no grants: the last-app teardown.
				patterns = nil
			}
			users = append(users, ntfycli.User{Name: subscriber, TopicPatterns: patterns})
		}
		return users
	}
	personClient := newStatefulPersonClient()
	service := newLoggedService(ntfyClient, personClient, nil)

	done := make(chan error, 1)
	go func() { done <- service.DeprovisionApp(context.Background(), DeprovisionAppRequest{AppID: "myapp"}) }()
	waitForResult(t, done, "DeprovisionApp")

	for _, subscriber := range subscribers {
		if indexOfInvocation(ntfyClient.invocations, fmt.Sprintf("DeleteUser(%s)", subscriber)) < 0 {
			t.Fatalf("expected a last-app teardown for %s, got: %s", subscriber, strings.Join(ntfyClient.invocations, " | "))
		}
	}
}

func TestNoFlowLogsAPasswordOrEmail(t *testing.T) {
	logger, buffer := newJSONLogger()

	// Provision: success, claim error, release failure, self-heal.
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new", statefulUsers: true}
	personClient := newStatefulPersonClient()
	service := newLoggedService(ntfyClient, personClient, logger)
	if _, err := service.Provision(context.Background(), provisionRequestForAlice); err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}
	personClient.claimErr = errors.New("claim boom")
	if _, err := service.Provision(context.Background(), ProvisionRequest{AppID: "chores4irl", Email: aliceEmail}); err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}
	personClient.claimErr = nil
	personClient.releaseClaimErr = errors.New("release boom")
	ntfyClient.grantAccessErr = errors.New("grant failed")
	if _, err := service.Provision(context.Background(), ProvisionRequest{AppID: "urls4irl", Email: "bob@example.com"}); err == nil {
		t.Fatal("expected the grant error")
	}
	ntfyClient.grantAccessErr = nil
	seedRow(personClient, "stale-stamp")
	ntfyClient.users = map[string]bool{}
	if _, err := service.Provision(context.Background(), provisionRequestForAlice); err != nil {
		t.Fatalf("self-heal returned unexpected error: %v", err)
	}

	// SetPassword and DeleteUser, including failures.
	personClient.releaseClaimErr = nil
	if _, err := service.SetPassword(context.Background(), SetPasswordRequest{AppID: "x", Email: aliceEmail, Password: "sekrit-pw"}); err != nil {
		t.Fatalf("SetPassword returned unexpected error: %v", err)
	}
	ntfyClient.changePasswordErr = errors.New("change failed")
	_, _ = service.SetPassword(context.Background(), SetPasswordRequest{AppID: "x", Email: aliceEmail, Password: "sekrit-pw"})
	personClient.claimErr = errors.New("claim boom")
	_, _ = service.SetPassword(context.Background(), SetPasswordRequest{AppID: "x", Email: aliceEmail, Password: "sekrit-pw"})
	personClient.deletePersonErr = errors.New("person delete failed")
	_ = service.DeleteUser(context.Background(), aliceNtfyUser)

	for _, secret := range []string{subscriberPassword, "generated-pw", "sekrit-pw", aliceEmail, "bob@example.com"} {
		if strings.Contains(buffer.String(), secret) {
			t.Fatalf("log output leaked %q: %s", secret, buffer.String())
		}
	}
}
