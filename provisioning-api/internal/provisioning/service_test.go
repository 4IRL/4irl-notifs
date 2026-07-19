package provisioning

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
)

// fakeNtfyClient records invocations and plays back scripted behavior for the
// subset of the ntfy CLI client the service depends on.
type fakeNtfyClient struct {
	invocations []string

	addUserErr     error
	grantAccessErr error
	resetAccessErr error
	deleteUserErr  error
	addTokenValue  string
	addTokenErr    error
	listTokens     []ntfycli.Token
	listTokensErr  error
	removeTokenErr error
	listUsers      []ntfycli.User
	listUsersErr   error
}

func (client *fakeNtfyClient) record(format string, values ...any) {
	client.invocations = append(client.invocations, fmt.Sprintf(format, values...))
}

func (client *fakeNtfyClient) AddUser(_ context.Context, userID string, password string) error {
	client.record("AddUser(%s,pw=%s)", userID, password)
	return client.addUserErr
}

func (client *fakeNtfyClient) DeleteUser(_ context.Context, userID string) error {
	client.record("DeleteUser(%s)", userID)
	return client.deleteUserErr
}

func (client *fakeNtfyClient) GrantAccess(_ context.Context, userID string, topicPattern string, permission ntfycli.Permission) error {
	client.record("GrantAccess(%s,%s,%s)", userID, topicPattern, permission)
	return client.grantAccessErr
}

func (client *fakeNtfyClient) ResetAccess(_ context.Context, userID string, topicPattern string) error {
	client.record("ResetAccess(%s,%s)", userID, topicPattern)
	return client.resetAccessErr
}

func (client *fakeNtfyClient) AddToken(_ context.Context, userID string, label string) (string, error) {
	client.record("AddToken(%s,%s)", userID, label)
	return client.addTokenValue, client.addTokenErr
}

func (client *fakeNtfyClient) ListTokens(_ context.Context, userID string) ([]ntfycli.Token, error) {
	client.record("ListTokens(%s)", userID)
	return client.listTokens, client.listTokensErr
}

func (client *fakeNtfyClient) RemoveToken(_ context.Context, userID string, token string) error {
	client.record("RemoveToken(%s,%s)", userID, token)
	return client.removeTokenErr
}

func (client *fakeNtfyClient) ListUsers(_ context.Context) ([]ntfycli.User, error) {
	client.record("ListUsers()")
	return client.listUsers, client.listUsersErr
}

func newTestService(client *fakeNtfyClient) *Service {
	return NewService(ServiceConfig{
		Client:           client,
		GeneratePassword: func() (string, error) { return "generated-pw", nil },
	})
}

// fakePersonClient records invocations and plays back scripted behavior for
// the subset of the person-service dual-write client (internal/personsvc)
// the service depends on. When recordTo is set, invocations are appended to
// that shared slice instead of the client's own — used to assert dual-write
// ordering relative to a fakeNtfyClient's invocations in the same test.
type fakePersonClient struct {
	recordTo    *[]string
	invocations []string

	configuredValue bool
	upsertErr       error
}

func (client *fakePersonClient) record(format string, values ...any) {
	entry := fmt.Sprintf(format, values...)
	if client.recordTo != nil {
		*client.recordTo = append(*client.recordTo, entry)
		return
	}
	client.invocations = append(client.invocations, entry)
}

func (client *fakePersonClient) Configured() bool {
	return client.configuredValue
}

func (client *fakePersonClient) UpsertPerson(_ context.Context, personHash string, email string) error {
	client.record("UpsertPerson(%s,%s)", personHash, email)
	return client.upsertErr
}

// aliceEmail/aliceNtfyUser/aliceHash are the golden-vector-derived identity
// used across these tests (see personhash.Hash("alice@example.com")).
const (
	aliceEmail    = "alice@example.com"
	aliceHash     = "76gzqgp4byjl6dje"
	aliceNtfyUser = "u_76gzqgp4byjl6dje"
)

func TestProvisionHappyPathCreatesUserGrantsAccessAndIssuesToken(t *testing.T) {
	client := &fakeNtfyClient{addTokenValue: "tk_new_token"}
	service := newTestService(client)

	result, err := service.Provision(context.Background(), ProvisionRequest{AppID: "urls4irl", Email: aliceEmail})
	if err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}

	expectedResult := ProvisionResult{
		UserID:       aliceNtfyUser,
		AppID:        "urls4irl",
		PersonHash:   aliceHash,
		TopicPattern: "urls4irl-" + aliceHash + "-*",
		Token:        "tk_new_token",
	}
	if result != expectedResult {
		t.Fatalf("result = %#v, expected %#v", result, expectedResult)
	}

	expectedInvocations := strings.Join([]string{
		fmt.Sprintf("AddUser(%s,pw=generated-pw)", aliceNtfyUser),
		fmt.Sprintf("GrantAccess(%s,urls4irl-%s-*,ro)", aliceNtfyUser, aliceHash),
		fmt.Sprintf("ListTokens(%s)", aliceNtfyUser),
		fmt.Sprintf("AddToken(%s,urls4irl)", aliceNtfyUser),
	}, " | ")
	if got := strings.Join(client.invocations, " | "); got != expectedInvocations {
		t.Fatalf("invocations = %s, expected %s", got, expectedInvocations)
	}
}

func TestProvisionRemovesOnlyStaleTokensForSameApp(t *testing.T) {
	client := &fakeNtfyClient{
		addTokenValue: "tk_fresh",
		listTokens: []ntfycli.Token{
			{Value: "tk_stale_urls4irl", Label: "urls4irl"},
			{Value: "tk_other_app", Label: "chores4irl"},
			{Value: "tk_unlabeled", Label: ""},
		},
	}
	service := newTestService(client)

	if _, err := service.Provision(context.Background(), ProvisionRequest{AppID: "urls4irl", Email: aliceEmail}); err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}

	joinedInvocations := strings.Join(client.invocations, " | ")
	if !strings.Contains(joinedInvocations, fmt.Sprintf("RemoveToken(%s,tk_stale_urls4irl)", aliceNtfyUser)) {
		t.Fatalf("stale same-app token not removed: %s", joinedInvocations)
	}
	if strings.Contains(joinedInvocations, "RemoveToken("+aliceNtfyUser+",tk_other_app)") {
		t.Fatalf("other app's token must not be removed: %s", joinedInvocations)
	}
	if strings.Contains(joinedInvocations, "RemoveToken("+aliceNtfyUser+",tk_unlabeled)") {
		t.Fatalf("unlabeled token must not be removed: %s", joinedInvocations)
	}
}

func TestProvisionToleratesExistingUser(t *testing.T) {
	client := &fakeNtfyClient{
		addUserErr:    fmt.Errorf("ntfy user: %w: user %s already exists", ntfycli.ErrAlreadyExists, aliceNtfyUser),
		addTokenValue: "tk_second_app",
	}
	service := newTestService(client)

	result, err := service.Provision(context.Background(), ProvisionRequest{AppID: "chores4irl", Email: aliceEmail})
	if err != nil {
		t.Fatalf("Provision must tolerate an existing user, got: %v", err)
	}
	if result.Token != "tk_second_app" {
		t.Fatalf("token = %q, expected tk_second_app", result.Token)
	}
	if got := strings.Join(client.invocations, " | "); !strings.Contains(got, fmt.Sprintf("GrantAccess(%s,chores4irl-%s-*,ro)", aliceNtfyUser, aliceHash)) {
		t.Fatalf("provisioning did not continue past existing user: %s", got)
	}
}

func TestProvisionDualWritesToPersonServiceAfterTokenIssuedWhenConfigured(t *testing.T) {
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new_token"}
	personClient := &fakePersonClient{configuredValue: true, recordTo: &ntfyClient.invocations}
	service := NewService(ServiceConfig{
		Client:           ntfyClient,
		GeneratePassword: func() (string, error) { return "generated-pw", nil },
		PersonClient:     personClient,
	})

	result, err := service.Provision(context.Background(), ProvisionRequest{AppID: "urls4irl", Email: "  Alice@Example.COM "})
	if err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}
	if result.PersonHash != aliceHash {
		t.Fatalf("PersonHash = %s, expected %s", result.PersonHash, aliceHash)
	}

	expectedInvocations := strings.Join([]string{
		fmt.Sprintf("AddUser(%s,pw=generated-pw)", aliceNtfyUser),
		fmt.Sprintf("GrantAccess(%s,urls4irl-%s-*,ro)", aliceNtfyUser, aliceHash),
		fmt.Sprintf("ListTokens(%s)", aliceNtfyUser),
		fmt.Sprintf("AddToken(%s,urls4irl)", aliceNtfyUser),
		fmt.Sprintf("UpsertPerson(%s,alice@example.com)", aliceHash),
	}, " | ")
	if got := strings.Join(ntfyClient.invocations, " | "); got != expectedInvocations {
		t.Fatalf("invocations = %s, expected %s (dual-write must happen exactly once, after AddToken, with a normalized email)", got, expectedInvocations)
	}
}

func TestProvisionSucceedsWhenPersonServiceDualWriteFails(t *testing.T) {
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new_token"}
	personClient := &fakePersonClient{configuredValue: true, upsertErr: errors.New("person-service unreachable")}
	service := NewService(ServiceConfig{
		Client:           ntfyClient,
		GeneratePassword: func() (string, error) { return "generated-pw", nil },
		PersonClient:     personClient,
	})

	result, err := service.Provision(context.Background(), ProvisionRequest{AppID: "urls4irl", Email: aliceEmail})
	if err != nil {
		t.Fatalf("Provision must succeed even when the person-service dual-write fails, got: %v", err)
	}

	expectedResult := ProvisionResult{
		UserID:       aliceNtfyUser,
		AppID:        "urls4irl",
		PersonHash:   aliceHash,
		TopicPattern: "urls4irl-" + aliceHash + "-*",
		Token:        "tk_new_token",
	}
	if result != expectedResult {
		t.Fatalf("result = %#v, expected %#v", result, expectedResult)
	}
}

func TestProvisionSkipsDualWriteWhenPersonServiceUnconfigured(t *testing.T) {
	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new_token"}
	personClient := &fakePersonClient{configuredValue: false}
	service := NewService(ServiceConfig{
		Client:           ntfyClient,
		GeneratePassword: func() (string, error) { return "generated-pw", nil },
		PersonClient:     personClient,
	})

	if _, err := service.Provision(context.Background(), ProvisionRequest{AppID: "urls4irl", Email: aliceEmail}); err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}
	if len(personClient.invocations) != 0 {
		t.Fatalf("UpsertPerson must not be called when the person-service client is unconfigured, got: %v", personClient.invocations)
	}
}

func TestProvisionSkipsDualWriteWhenNtfyProvisioningFailsBeforeTokenMint(t *testing.T) {
	ntfyClient := &fakeNtfyClient{grantAccessErr: errors.New("grant access failed")}
	personClient := &fakePersonClient{configuredValue: true}
	service := NewService(ServiceConfig{
		Client:           ntfyClient,
		GeneratePassword: func() (string, error) { return "generated-pw", nil },
		PersonClient:     personClient,
	})

	if _, err := service.Provision(context.Background(), ProvisionRequest{AppID: "urls4irl", Email: aliceEmail}); err == nil {
		t.Fatal("expected GrantAccess error to propagate")
	}
	if len(personClient.invocations) != 0 {
		t.Fatalf("UpsertPerson must not be called when ntfy provisioning fails before the token is minted, got: %v", personClient.invocations)
	}
}

func TestProvisionLogsWarnWhenPersonServiceDualWriteFails(t *testing.T) {
	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))

	ntfyClient := &fakeNtfyClient{addTokenValue: "tk_new_token"}
	personClient := &fakePersonClient{configuredValue: true, upsertErr: errors.New("person-service unreachable")}
	service := NewService(ServiceConfig{
		Client:           ntfyClient,
		GeneratePassword: func() (string, error) { return "generated-pw", nil },
		PersonClient:     personClient,
		Logger:           logger,
	})

	if _, err := service.Provision(context.Background(), ProvisionRequest{AppID: "urls4irl", Email: aliceEmail}); err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}

	logOutput := logBuffer.String()
	if !strings.Contains(logOutput, "person-service dual-write failed") {
		t.Fatalf("expected Warn log to mention dual-write failure, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "level=WARN") {
		t.Fatalf("expected log level to be WARN, got: %s", logOutput)
	}
}

func TestDeprovisionResetsAccessAndRemovesAppTokens(t *testing.T) {
	client := &fakeNtfyClient{
		listUsers: []ntfycli.User{
			{Name: aliceNtfyUser, TopicPatterns: []string{"chores4irl-" + aliceHash + "-*"}},
		},
		listTokens: []ntfycli.Token{
			{Value: "tk_urls4irl_token", Label: "urls4irl"},
			{Value: "tk_other_app", Label: "chores4irl"},
		},
	}
	service := newTestService(client)

	if err := service.Deprovision(context.Background(), DeprovisionRequest{AppID: "urls4irl", NtfyUserID: aliceNtfyUser}); err != nil {
		t.Fatalf("Deprovision returned unexpected error: %v", err)
	}

	expectedInvocations := strings.Join([]string{
		fmt.Sprintf("ResetAccess(%s,urls4irl-%s-*)", aliceNtfyUser, aliceHash),
		fmt.Sprintf("ListTokens(%s)", aliceNtfyUser),
		fmt.Sprintf("RemoveToken(%s,tk_urls4irl_token)", aliceNtfyUser),
		"ListUsers()",
	}, " | ")
	if got := strings.Join(client.invocations, " | "); got != expectedInvocations {
		t.Fatalf("invocations = %s, expected %s", got, expectedInvocations)
	}
}

func TestDeprovisionDeletesUserWhenNoTopicPatternsRemain(t *testing.T) {
	client := &fakeNtfyClient{
		listUsers: []ntfycli.User{
			{Name: aliceNtfyUser, TopicPatterns: nil},
		},
	}
	service := newTestService(client)

	if err := service.Deprovision(context.Background(), DeprovisionRequest{AppID: "urls4irl", NtfyUserID: aliceNtfyUser}); err != nil {
		t.Fatalf("Deprovision returned unexpected error: %v", err)
	}

	joinedInvocations := strings.Join(client.invocations, " | ")
	if !strings.Contains(joinedInvocations, fmt.Sprintf("DeleteUser(%s)", aliceNtfyUser)) {
		t.Fatalf("expected DeleteUser to be called when no topic patterns remain: %s", joinedInvocations)
	}
}

func TestDeprovisionKeepsUserWhenTopicPatternsRemain(t *testing.T) {
	client := &fakeNtfyClient{
		listUsers: []ntfycli.User{
			{Name: aliceNtfyUser, TopicPatterns: []string{"chores4irl-" + aliceHash + "-*"}},
		},
	}
	service := newTestService(client)

	if err := service.Deprovision(context.Background(), DeprovisionRequest{AppID: "urls4irl", NtfyUserID: aliceNtfyUser}); err != nil {
		t.Fatalf("Deprovision returned unexpected error: %v", err)
	}

	joinedInvocations := strings.Join(client.invocations, " | ")
	if strings.Contains(joinedInvocations, "DeleteUser(") {
		t.Fatalf("must not delete user while topic patterns remain: %s", joinedInvocations)
	}
}

func TestDeprovisionDoesNothingWhenUserAbsentFromList(t *testing.T) {
	client := &fakeNtfyClient{listUsers: []ntfycli.User{}}
	service := newTestService(client)

	if err := service.Deprovision(context.Background(), DeprovisionRequest{AppID: "urls4irl", NtfyUserID: aliceNtfyUser}); err != nil {
		t.Fatalf("Deprovision returned unexpected error: %v", err)
	}

	joinedInvocations := strings.Join(client.invocations, " | ")
	if strings.Contains(joinedInvocations, "DeleteUser(") {
		t.Fatalf("must treat an already-absent user as already gone, not call DeleteUser: %s", joinedInvocations)
	}
}

func TestDeprovisionPropagatesUnknownUser(t *testing.T) {
	client := &fakeNtfyClient{
		resetAccessErr: fmt.Errorf("ntfy access: %w: user ghost does not exist", ntfycli.ErrNotFound),
	}
	service := newTestService(client)

	err := service.Deprovision(context.Background(), DeprovisionRequest{AppID: "urls4irl", NtfyUserID: "u_ghost0000000000"})
	if !errors.Is(err, ntfycli.ErrNotFound) {
		t.Fatalf("expected ErrNotFound to propagate, got: %v", err)
	}
}

func TestListUsersDerivesAppsFromScopedAndWildcardPatterns(t *testing.T) {
	client := &fakeNtfyClient{
		listUsers: []ntfycli.User{
			{Name: aliceNtfyUser, TopicPatterns: []string{"urls4irl-" + aliceHash + "-*", "chores4irl-" + aliceHash + "-*", "custom-topic"}},
			{Name: "legacy-app-wide-user", TopicPatterns: []string{"urls4irl-*"}},
			{Name: "bob", TopicPatterns: nil},
		},
	}
	service := newTestService(client)

	users, err := service.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers returned unexpected error: %v", err)
	}

	expected := []UserSummary{
		{UserID: aliceNtfyUser, Apps: []string{"urls4irl", "chores4irl"}, TopicPatterns: []string{"urls4irl-" + aliceHash + "-*", "chores4irl-" + aliceHash + "-*", "custom-topic"}},
		{UserID: "legacy-app-wide-user", Apps: []string{"urls4irl"}, TopicPatterns: []string{"urls4irl-*"}},
		{UserID: "bob", Apps: []string{}, TopicPatterns: nil},
	}
	if !reflect.DeepEqual(users, expected) {
		t.Fatalf("users = %#v, expected %#v", users, expected)
	}
}

func TestProvisionAppHappyPathCreatesPublisherGrantsWriteAccessAndIssuesToken(t *testing.T) {
	client := &fakeNtfyClient{addTokenValue: "tk_publisher_token"}
	service := newTestService(client)

	result, err := service.ProvisionApp(context.Background(), ProvisionAppRequest{AppID: "urls4irl"})
	if err != nil {
		t.Fatalf("ProvisionApp returned unexpected error: %v", err)
	}

	expectedResult := ProvisionAppResult{
		AppID:           "urls4irl",
		PublisherUserID: "urls4irl-publisher",
		TopicPattern:    "urls4irl-*",
		Token:           "tk_publisher_token",
	}
	if result != expectedResult {
		t.Fatalf("result = %#v, expected %#v", result, expectedResult)
	}

	expectedInvocations := strings.Join([]string{
		"AddUser(urls4irl-publisher,pw=generated-pw)",
		"GrantAccess(urls4irl-publisher,urls4irl-*,wo)",
		"AddToken(urls4irl-publisher,publisher)",
	}, " | ")
	if got := strings.Join(client.invocations, " | "); got != expectedInvocations {
		t.Fatalf("invocations = %s, expected %s", got, expectedInvocations)
	}
}

func TestProvisionAppToleratesExistingPublisherUser(t *testing.T) {
	client := &fakeNtfyClient{
		addUserErr:    fmt.Errorf("ntfy user: %w: user urls4irl-publisher already exists", ntfycli.ErrAlreadyExists),
		addTokenValue: "tk_second_publisher_token",
	}
	service := newTestService(client)

	result, err := service.ProvisionApp(context.Background(), ProvisionAppRequest{AppID: "urls4irl"})
	if err != nil {
		t.Fatalf("ProvisionApp must tolerate an existing publisher user, got: %v", err)
	}
	if result.Token != "tk_second_publisher_token" {
		t.Fatalf("token = %q, expected tk_second_publisher_token", result.Token)
	}
	if got := strings.Join(client.invocations, " | "); !strings.Contains(got, "GrantAccess(urls4irl-publisher,urls4irl-*,wo)") {
		t.Fatalf("provisioning did not continue past existing publisher user: %s", got)
	}
}

func TestProvisionAppRepeatCallMintsAdditionalTokenWithoutTouchingExisting(t *testing.T) {
	client := &fakeNtfyClient{addTokenValue: "tk_another_token"}
	service := newTestService(client)

	if _, err := service.ProvisionApp(context.Background(), ProvisionAppRequest{AppID: "urls4irl"}); err != nil {
		t.Fatalf("ProvisionApp returned unexpected error: %v", err)
	}

	joinedInvocations := strings.Join(client.invocations, " | ")
	if strings.Contains(joinedInvocations, "ListTokens(") {
		t.Fatalf("ProvisionApp must never call ListTokens: %s", joinedInvocations)
	}
	if strings.Contains(joinedInvocations, "RemoveToken(") {
		t.Fatalf("ProvisionApp must never call RemoveToken (repeat calls mint additional tokens): %s", joinedInvocations)
	}
}

func TestProvisionAppPropagatesGeneratePasswordError(t *testing.T) {
	client := &fakeNtfyClient{}
	service := NewService(ServiceConfig{
		Client:           client,
		GeneratePassword: func() (string, error) { return "", errors.New("password generation failed") },
	})

	if _, err := service.ProvisionApp(context.Background(), ProvisionAppRequest{AppID: "urls4irl"}); err == nil {
		t.Fatal("expected error from GeneratePassword to propagate")
	}
	if len(client.invocations) != 0 {
		t.Fatalf("expected no client invocations when password generation fails, got: %v", client.invocations)
	}
}

func TestProvisionAppPropagatesAddUserError(t *testing.T) {
	client := &fakeNtfyClient{addUserErr: errors.New("add user failed")}
	service := newTestService(client)

	if _, err := service.ProvisionApp(context.Background(), ProvisionAppRequest{AppID: "urls4irl"}); err == nil {
		t.Fatal("expected AddUser error to propagate")
	}
}

func TestProvisionAppPropagatesGrantAccessError(t *testing.T) {
	client := &fakeNtfyClient{grantAccessErr: errors.New("grant access failed")}
	service := newTestService(client)

	if _, err := service.ProvisionApp(context.Background(), ProvisionAppRequest{AppID: "urls4irl"}); err == nil {
		t.Fatal("expected GrantAccess error to propagate")
	}
}

func TestProvisionAppPropagatesAddTokenError(t *testing.T) {
	client := &fakeNtfyClient{addTokenErr: errors.New("add token failed")}
	service := newTestService(client)

	if _, err := service.ProvisionApp(context.Background(), ProvisionAppRequest{AppID: "urls4irl"}); err == nil {
		t.Fatal("expected AddToken error to propagate")
	}
}

func TestDeleteUserDelegatesToClient(t *testing.T) {
	client := &fakeNtfyClient{}
	service := newTestService(client)

	if err := service.DeleteUser(context.Background(), "alice"); err != nil {
		t.Fatalf("DeleteUser returned unexpected error: %v", err)
	}
	if got := strings.Join(client.invocations, " | "); got != "DeleteUser(alice)" {
		t.Fatalf("invocations = %s, expected DeleteUser(alice)", got)
	}
}
