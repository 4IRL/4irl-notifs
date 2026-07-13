package provisioning

import (
	"context"
	"errors"
	"fmt"
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

func (client *fakeNtfyClient) GrantAccess(_ context.Context, userID string, appID string) error {
	client.record("GrantAccess(%s,%s)", userID, appID)
	return client.grantAccessErr
}

func (client *fakeNtfyClient) ResetAccess(_ context.Context, userID string, appID string) error {
	client.record("ResetAccess(%s,%s)", userID, appID)
	return client.resetAccessErr
}

func (client *fakeNtfyClient) AddToken(_ context.Context, userID string, appID string) (string, error) {
	client.record("AddToken(%s,%s)", userID, appID)
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

func TestProvisionHappyPathCreatesUserGrantsAccessAndIssuesToken(t *testing.T) {
	client := &fakeNtfyClient{addTokenValue: "tk_new_token"}
	service := newTestService(client)

	result, err := service.Provision(context.Background(), ProvisionRequest{AppID: "urls4irl", UserID: "alice"})
	if err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}

	expectedResult := ProvisionResult{
		UserID:       "alice",
		AppID:        "urls4irl",
		TopicPattern: "urls4irl-*",
		Token:        "tk_new_token",
	}
	if result != expectedResult {
		t.Fatalf("result = %#v, expected %#v", result, expectedResult)
	}

	expectedInvocations := strings.Join([]string{
		"AddUser(alice,pw=generated-pw)",
		"GrantAccess(alice,urls4irl)",
		"ListTokens(alice)",
		"AddToken(alice,urls4irl)",
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

	if _, err := service.Provision(context.Background(), ProvisionRequest{AppID: "urls4irl", UserID: "alice"}); err != nil {
		t.Fatalf("Provision returned unexpected error: %v", err)
	}

	joinedInvocations := strings.Join(client.invocations, " | ")
	if !strings.Contains(joinedInvocations, "RemoveToken(alice,tk_stale_urls4irl)") {
		t.Fatalf("stale same-app token not removed: %s", joinedInvocations)
	}
	if strings.Contains(joinedInvocations, "RemoveToken(alice,tk_other_app)") {
		t.Fatalf("other app's token must not be removed: %s", joinedInvocations)
	}
	if strings.Contains(joinedInvocations, "RemoveToken(alice,tk_unlabeled)") {
		t.Fatalf("unlabeled token must not be removed: %s", joinedInvocations)
	}
}

func TestDeprovisionResetsAccessAndRemovesAppTokens(t *testing.T) {
	client := &fakeNtfyClient{
		listTokens: []ntfycli.Token{
			{Value: "tk_urls4irl_token", Label: "urls4irl"},
			{Value: "tk_other_app", Label: "chores4irl"},
		},
	}
	service := newTestService(client)

	if err := service.Deprovision(context.Background(), ProvisionRequest{AppID: "urls4irl", UserID: "alice"}); err != nil {
		t.Fatalf("Deprovision returned unexpected error: %v", err)
	}

	expectedInvocations := strings.Join([]string{
		"ResetAccess(alice,urls4irl)",
		"ListTokens(alice)",
		"RemoveToken(alice,tk_urls4irl_token)",
	}, " | ")
	if got := strings.Join(client.invocations, " | "); got != expectedInvocations {
		t.Fatalf("invocations = %s, expected %s", got, expectedInvocations)
	}
}

func TestDeprovisionPropagatesUnknownUser(t *testing.T) {
	client := &fakeNtfyClient{
		resetAccessErr: fmt.Errorf("ntfy access: %w: user ghost does not exist", ntfycli.ErrNotFound),
	}
	service := newTestService(client)

	err := service.Deprovision(context.Background(), ProvisionRequest{AppID: "urls4irl", UserID: "ghost"})
	if !errors.Is(err, ntfycli.ErrNotFound) {
		t.Fatalf("expected ErrNotFound to propagate, got: %v", err)
	}
}

func TestListUsersDerivesAppsFromWildcardPatterns(t *testing.T) {
	client := &fakeNtfyClient{
		listUsers: []ntfycli.User{
			{Name: "alice", TopicPatterns: []string{"urls4irl-*", "chores4irl-*", "custom-topic"}},
			{Name: "bob", TopicPatterns: nil},
		},
	}
	service := newTestService(client)

	users, err := service.ListUsers(context.Background())
	if err != nil {
		t.Fatalf("ListUsers returned unexpected error: %v", err)
	}

	expected := []UserSummary{
		{UserID: "alice", Apps: []string{"urls4irl", "chores4irl"}, TopicPatterns: []string{"urls4irl-*", "chores4irl-*", "custom-topic"}},
		{UserID: "bob", Apps: []string{}, TopicPatterns: nil},
	}
	if !reflect.DeepEqual(users, expected) {
		t.Fatalf("users = %#v, expected %#v", users, expected)
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

func TestProvisionToleratesExistingUser(t *testing.T) {
	client := &fakeNtfyClient{
		addUserErr:    fmt.Errorf("ntfy user: %w: user alice already exists", ntfycli.ErrAlreadyExists),
		addTokenValue: "tk_second_app",
	}
	service := newTestService(client)

	result, err := service.Provision(context.Background(), ProvisionRequest{AppID: "chores4irl", UserID: "alice"})
	if err != nil {
		t.Fatalf("Provision must tolerate an existing user, got: %v", err)
	}
	if result.Token != "tk_second_app" {
		t.Fatalf("token = %q, expected tk_second_app", result.Token)
	}
	if got := strings.Join(client.invocations, " | "); !strings.Contains(got, "GrantAccess(alice,chores4irl)") {
		t.Fatalf("provisioning did not continue past existing user: %s", got)
	}
}
