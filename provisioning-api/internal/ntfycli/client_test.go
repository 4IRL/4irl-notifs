package ntfycli

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recordedCall captures a single invocation observed by the fake runner.
type recordedCall struct {
	args     []string
	extraEnv []string
}

// scriptedResult is one canned response the fake runner plays back.
type scriptedResult struct {
	stdout string
	stderr string
	err    error
}

// fakeRunner is a scriptable, concurrency-observing Runner test double.
type fakeRunner struct {
	mutex     sync.Mutex
	calls     []recordedCall
	results   []scriptedResult
	callIndex int
}

func (runner *fakeRunner) Run(_ context.Context, args []string, extraEnv []string) (string, string, error) {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()

	runner.calls = append(runner.calls, recordedCall{args: args, extraEnv: extraEnv})
	if runner.callIndex >= len(runner.results) {
		return "", "", nil
	}
	result := runner.results[runner.callIndex]
	runner.callIndex++
	return result.stdout, result.stderr, result.err
}

func newTestClient(runner Runner) *Client {
	return NewClient(ClientConfig{Runner: runner, Sleep: func(_ time.Duration) {}})
}

func TestAddUserInvokesCLIWithPasswordEnv(t *testing.T) {
	runner := &fakeRunner{}
	client := newTestClient(runner)

	if err := client.AddUser(context.Background(), "alice", "sekrit-pw"); err != nil {
		t.Fatalf("AddUser returned unexpected error: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 CLI call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if got := strings.Join(call.args, " "); got != "user add alice" {
		t.Fatalf("unexpected args: %q", got)
	}
	foundPassword := false
	for _, envEntry := range call.extraEnv {
		if envEntry == "NTFY_PASSWORD=sekrit-pw" {
			foundPassword = true
		}
	}
	if !foundPassword {
		t.Fatalf("NTFY_PASSWORD env entry missing from %v", call.extraEnv)
	}
}

func TestChangePasswordInvokesCLIWithPasswordEnv(t *testing.T) {
	runner := &fakeRunner{}
	client := newTestClient(runner)

	if err := client.ChangePassword(context.Background(), "alice", "sekrit-pw"); err != nil {
		t.Fatalf("ChangePassword returned unexpected error: %v", err)
	}

	if len(runner.calls) != 1 {
		t.Fatalf("expected 1 CLI call, got %d", len(runner.calls))
	}
	call := runner.calls[0]
	if got := strings.Join(call.args, " "); got != "user change-pass alice" {
		t.Fatalf("unexpected args: %q", got)
	}
	foundPassword := false
	for _, envEntry := range call.extraEnv {
		if envEntry == "NTFY_PASSWORD=sekrit-pw" {
			foundPassword = true
		}
	}
	if !foundPassword {
		t.Fatalf("NTFY_PASSWORD env entry missing from %v", call.extraEnv)
	}
}

// overlapDetectingRunner records the maximum number of concurrently in-flight
// Run calls, using atomics only (no internal locking that would mask missing
// serialization in the client under test).
type overlapDetectingRunner struct {
	inFlight       atomic.Int32
	maxObserved    atomic.Int32
	completedCalls atomic.Int32
}

func (runner *overlapDetectingRunner) Run(_ context.Context, _ []string, _ []string) (string, string, error) {
	current := runner.inFlight.Add(1)
	for {
		observed := runner.maxObserved.Load()
		if current <= observed || runner.maxObserved.CompareAndSwap(observed, current) {
			break
		}
	}
	time.Sleep(2 * time.Millisecond)
	runner.inFlight.Add(-1)
	runner.completedCalls.Add(1)
	return "", "", nil
}

func TestConcurrentInvocationsAreSerialized(t *testing.T) {
	runner := &overlapDetectingRunner{}
	client := newTestClient(runner)

	const goroutineCount = 8
	var waitGroup sync.WaitGroup
	for workerIndex := 0; workerIndex < goroutineCount; workerIndex++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if err := client.AddUser(context.Background(), "alice", "pw"); err != nil {
				t.Errorf("AddUser failed: %v", err)
			}
		}()
	}
	waitGroup.Wait()

	if completed := runner.completedCalls.Load(); completed != goroutineCount {
		t.Fatalf("expected %d completed calls, got %d", goroutineCount, completed)
	}
	if maxConcurrent := runner.maxObserved.Load(); maxConcurrent != 1 {
		t.Fatalf("CLI invocations overlapped: max concurrency %d, expected 1", maxConcurrent)
	}
}

func TestAddUserReturnsCLIError(t *testing.T) {
	runner := &fakeRunner{results: []scriptedResult{
		{stderr: "something broke", err: errors.New("exit status 1")},
	}}
	client := newTestClient(runner)

	err := client.AddUser(context.Background(), "alice", "sekrit-pw")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "something broke") {
		t.Fatalf("error should include CLI stderr, got: %v", err)
	}
}

func TestBusyWriteIsRetriedWithExponentialBackoff(t *testing.T) {
	busyResult := scriptedResult{stderr: "cannot open auth db: database is locked", err: errors.New("exit status 1")}
	runner := &fakeRunner{results: []scriptedResult{busyResult, busyResult, {stdout: "user alice added with role user"}}}

	var recordedSleeps []time.Duration
	client := NewClient(ClientConfig{
		Runner: runner,
		Sleep:  func(duration time.Duration) { recordedSleeps = append(recordedSleeps, duration) },
	})

	if err := client.AddUser(context.Background(), "alice", "pw"); err != nil {
		t.Fatalf("expected retry to eventually succeed, got: %v", err)
	}

	if len(runner.calls) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(runner.calls))
	}
	expectedSleeps := []time.Duration{50 * time.Millisecond, 100 * time.Millisecond}
	if len(recordedSleeps) != len(expectedSleeps) {
		t.Fatalf("expected %d sleeps, got %v", len(expectedSleeps), recordedSleeps)
	}
	for sleepIndex, expectedSleep := range expectedSleeps {
		if recordedSleeps[sleepIndex] != expectedSleep {
			t.Fatalf("sleep %d = %v, expected %v", sleepIndex, recordedSleeps[sleepIndex], expectedSleep)
		}
	}
}

func TestBusyRetriesAreExhaustedAfterMaxAttempts(t *testing.T) {
	busyResult := scriptedResult{stderr: "SQLITE_BUSY: database is locked", err: errors.New("exit status 1")}
	runner := &fakeRunner{results: []scriptedResult{busyResult, busyResult, busyResult, busyResult, busyResult, busyResult, busyResult}}
	client := newTestClient(runner)

	err := client.AddUser(context.Background(), "alice", "pw")
	if err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
	if !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("error should surface the busy stderr, got: %v", err)
	}
	if len(runner.calls) != 5 {
		t.Fatalf("expected exactly 5 attempts (default max), got %d", len(runner.calls))
	}
}

func TestCLIErrorsAreClassifiedAsSentinels(t *testing.T) {
	testCases := []struct {
		name             string
		stderr           string
		expectedSentinel error
	}{
		{
			name:             "duplicate user maps to ErrAlreadyExists",
			stderr:           "user alice already exists",
			expectedSentinel: ErrAlreadyExists,
		},
		{
			name:             "missing user maps to ErrNotFound",
			stderr:           "user alice does not exist",
			expectedSentinel: ErrNotFound,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &fakeRunner{results: []scriptedResult{
				{stderr: testCase.stderr, err: errors.New("exit status 1")},
			}}
			client := newTestClient(runner)

			err := client.AddUser(context.Background(), "alice", "pw")
			if !errors.Is(err, testCase.expectedSentinel) {
				t.Fatalf("expected errors.Is(err, sentinel) for stderr %q, got: %v", testCase.stderr, err)
			}
		})
	}
}

func TestAddTokenParsesTokenFromStdout(t *testing.T) {
	testCases := []struct {
		name          string
		stdout        string
		expectedToken string
		expectError   bool
	}{
		{
			name:          "parses token value",
			stdout:        "token tk_kdn7wq2tli5x1y5te0fltika2plnj created for user alice, never expires",
			expectedToken: "tk_kdn7wq2tli5x1y5te0fltika2plnj",
		},
		{
			name:        "unparseable stdout is an error",
			stdout:      "something unexpected",
			expectError: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &fakeRunner{results: []scriptedResult{{stdout: testCase.stdout}}}
			client := newTestClient(runner)

			token, err := client.AddToken(context.Background(), "alice", "urls4irl")
			if testCase.expectError {
				if err == nil {
					t.Fatal("expected parse error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("AddToken returned unexpected error: %v", err)
			}
			if token != testCase.expectedToken {
				t.Fatalf("token = %q, expected %q", token, testCase.expectedToken)
			}
			if got := strings.Join(runner.calls[0].args, " "); got != "token add --label urls4irl alice" {
				t.Fatalf("unexpected args: %q", got)
			}

		})
	}
}

func TestListTokensParsesLabeledAndUnlabeledTokens(t *testing.T) {
	listStdout := "user alice\n" +
		"- tk_kdn7wq2tli5x1y5te0fltika2plnj (urls4irl), never expires, accessed from 0.0.0.0 at 13 Jul 26 03:46 UTC\n" +
		"- tk_zzz9wq2tli5x1y5te0fltika2plaa, expires 14 Jul 26, accessed from 0.0.0.0 at 13 Jul 26 03:46 UTC\n"
	runner := &fakeRunner{results: []scriptedResult{{stdout: listStdout}}}
	client := newTestClient(runner)

	tokens, err := client.ListTokens(context.Background(), "alice")
	if err != nil {
		t.Fatalf("ListTokens returned unexpected error: %v", err)
	}

	expected := []Token{
		{Value: "tk_kdn7wq2tli5x1y5te0fltika2plnj", Label: "urls4irl"},
		{Value: "tk_zzz9wq2tli5x1y5te0fltika2plaa", Label: ""},
	}
	if !reflect.DeepEqual(tokens, expected) {
		t.Fatalf("tokens = %#v, expected %#v", tokens, expected)
	}
	if got := strings.Join(runner.calls[0].args, " "); got != "token list alice" {
		t.Fatalf("unexpected args: %q", got)
	}
}

func TestListUsersParsesUserBlocksAndSkipsAnonymous(t *testing.T) {
	listStdout := "user alice (role: user, tier: none)\n" +
		"- read-write access to topic urls4irl-*\n" +
		"- read-write access to topic chores4irl-*\n" +
		"user bob (role: user, tier: none)\n" +
		"- no topic-specific permissions\n" +
		"user * (role: anonymous, tier: none)\n" +
		"- no topic-specific permissions\n" +
		"- no access to any (other) topics (server config)\n"
	runner := &fakeRunner{results: []scriptedResult{{stdout: listStdout}}}
	client := newTestClient(runner)

	users, err := client.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers returned unexpected error: %v", err)
	}

	expected := []User{
		{Name: "alice", TopicPatterns: []string{"urls4irl-*", "chores4irl-*"}},
		{Name: "bob", TopicPatterns: nil},
	}
	if !reflect.DeepEqual(users, expected) {
		t.Fatalf("users = %#v, expected %#v", users, expected)
	}
	if got := strings.Join(runner.calls[0].args, " "); got != "user list" {
		t.Fatalf("unexpected args: %q", got)
	}
}

// Captured from the pinned ntfy image (binwiederhier/ntfy:v2.26.0) running
// `ntfy access <user>` against a throwaway auth db.
const (
	capturedAccessScopedAndBroadcast = "user u_scoped (role: user, tier: none)\n" +
		"- read-only access to topic app1-broadcast\n" +
		"- read-only access to topic app1-hash1-*\n"
	capturedAccessNoGrants       = "user u_none (role: user, tier: none)\n- no topic-specific permissions\n"
	capturedAccessUnknownStderr  = "user u_unknown does not exist\n"
	capturedAccessAnonymousBlock = "user * (role: anonymous, tier: none)\n" +
		"- no topic-specific permissions\n" +
		"- no access to any (other) topics (server config)\n"
)

func TestGetUserReturnsScopedAndBroadcastPatterns(t *testing.T) {
	runner := &fakeRunner{results: []scriptedResult{{stdout: capturedAccessScopedAndBroadcast}}}
	client := newTestClient(runner)

	user, err := client.GetUser(context.Background(), "u_scoped")
	if err != nil {
		t.Fatalf("GetUser returned unexpected error: %v", err)
	}

	expected := User{Name: "u_scoped", TopicPatterns: []string{"app1-broadcast", "app1-hash1-*"}}
	if !reflect.DeepEqual(user, expected) {
		t.Fatalf("user = %#v, expected %#v", user, expected)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("GetUser must issue exactly one CLI call, got %d", len(runner.calls))
	}
	if got := strings.Join(runner.calls[0].args, " "); got != "access u_scoped" {
		t.Fatalf("args = %q, expected %q", got, "access u_scoped")
	}
	if len(runner.calls[0].extraEnv) != 0 {
		t.Fatalf("GetUser must not pass extra env, got %v", runner.calls[0].extraEnv)
	}
}

func TestGetUserWithNoGrantsReturnsEmptyPatternsAndNilError(t *testing.T) {
	runner := &fakeRunner{results: []scriptedResult{{stdout: capturedAccessNoGrants}}}
	client := newTestClient(runner)

	user, err := client.GetUser(context.Background(), "u_none")
	if err != nil {
		t.Fatalf("GetUser returned unexpected error: %v", err)
	}
	if user.Name != "u_none" {
		t.Fatalf("user name = %q, expected u_none", user.Name)
	}
	if len(user.TopicPatterns) != 0 {
		t.Fatalf("expected no topic patterns, got %v", user.TopicPatterns)
	}
}

func TestGetUserUnknownUserIsErrNotFound(t *testing.T) {
	runner := &fakeRunner{results: []scriptedResult{
		{stderr: capturedAccessUnknownStderr, err: errors.New("exit status 1")},
	}}
	client := newTestClient(runner)

	_, err := client.GetUser(context.Background(), "u_unknown")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for the real unknown-user stderr, got: %v", err)
	}
}

func TestGetUserOutputWithoutMatchingBlockIsErrNotFound(t *testing.T) {
	testCases := []struct {
		name   string
		stdout string
	}{
		{name: "empty stdout", stdout: ""},
		{name: "whitespace-only stdout", stdout: "\n  \n"},
		{name: "block for a different user", stdout: capturedAccessScopedAndBroadcast},
		{name: "anonymous block only", stdout: capturedAccessAnonymousBlock},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &fakeRunner{results: []scriptedResult{{stdout: testCase.stdout}}}
			client := newTestClient(runner)

			user, err := client.GetUser(context.Background(), "u_wanted")
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("expected ErrNotFound, got user %#v err %v", user, err)
			}
			if user.Name != "" || len(user.TopicPatterns) != 0 {
				t.Fatalf("expected the zero User alongside the error, got %#v", user)
			}
		})
	}
}

func TestGetUserRetriesWhenAuthDatabaseIsBusy(t *testing.T) {
	busyResult := scriptedResult{stderr: "cannot open auth db: database is locked", err: errors.New("exit status 1")}
	runner := &fakeRunner{results: []scriptedResult{busyResult, {stdout: capturedAccessScopedAndBroadcast}}}
	client := newTestClient(runner)

	user, err := client.GetUser(context.Background(), "u_scoped")
	if err != nil {
		t.Fatalf("expected busy retry to succeed, got: %v", err)
	}
	if user.Name != "u_scoped" {
		t.Fatalf("user name = %q, expected u_scoped", user.Name)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(runner.calls))
	}
}

func TestGetUserSurfacesOtherCLIErrors(t *testing.T) {
	runner := &fakeRunner{results: []scriptedResult{
		{stderr: "permission denied", err: errors.New("exit status 1")},
	}}
	client := newTestClient(runner)

	_, err := client.GetUser(context.Background(), "u_scoped")
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("an unrelated CLI failure must not look like ErrNotFound: %v", err)
	}
}

func TestPassThroughMethodsBuildExpectedInvocations(t *testing.T) {
	testCases := []struct {
		name         string
		invoke       func(client *Client) error
		expectedArgs string
	}{
		{
			name:         "DeleteUser",
			invoke:       func(client *Client) error { return client.DeleteUser(context.Background(), "alice") },
			expectedArgs: "user del alice",
		},
		{
			name: "GrantAccess",
			invoke: func(client *Client) error {
				return client.GrantAccess(context.Background(), "alice", "urls4irl-76gzqgp4byjl6dje-*", PermissionReadOnly)
			},
			expectedArgs: "access alice urls4irl-76gzqgp4byjl6dje-* ro",
		},
		{
			name: "ResetAccess",
			invoke: func(client *Client) error {
				return client.ResetAccess(context.Background(), "alice", "urls4irl-76gzqgp4byjl6dje-*")
			},
			expectedArgs: "access --reset alice urls4irl-76gzqgp4byjl6dje-*",
		},
		{
			name: "RemoveToken",
			invoke: func(client *Client) error {
				return client.RemoveToken(context.Background(), "alice", "tk_abc123")
			},
			expectedArgs: "token remove alice tk_abc123",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			runner := &fakeRunner{}
			client := newTestClient(runner)

			if err := testCase.invoke(client); err != nil {
				t.Fatalf("%s returned unexpected error: %v", testCase.name, err)
			}
			if got := strings.Join(runner.calls[0].args, " "); got != testCase.expectedArgs {
				t.Fatalf("args = %q, expected %q", got, testCase.expectedArgs)
			}
			if len(runner.calls[0].extraEnv) != 0 {
				t.Fatalf("%s must not pass extra env, got %v", testCase.name, runner.calls[0].extraEnv)
			}
		})
	}
}

func TestNonBusyErrorIsNotRetried(t *testing.T) {
	runner := &fakeRunner{results: []scriptedResult{
		{stderr: "user alice already exists", err: errors.New("exit status 1")},
		{stdout: "should never be reached"},
	}}
	client := newTestClient(runner)

	err := client.AddUser(context.Background(), "alice", "pw")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if len(runner.calls) != 1 {
		t.Fatalf("non-busy failure must not retry: got %d attempts", len(runner.calls))
	}
}
