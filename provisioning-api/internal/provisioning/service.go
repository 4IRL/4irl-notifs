// Package provisioning implements the provision/deprovision semantics of the
// notification hub: one global ntfy user per person, derived from an
// email-hash identity shared by every 4IRL app, with a scoped read-only
// topic ACL ("{app_id}-{personHash}-*") plus one app-labeled access token
// granted per app the person is provisioned into. The person never
// authenticates with the ntfy password; consuming apps store the issued
// token.
package provisioning

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/personhash"
)

// NtfyClient is the subset of the ntfy CLI client the service depends on.
type NtfyClient interface {
	AddUser(ctx context.Context, userID string, password string) error
	DeleteUser(ctx context.Context, userID string) error
	GrantAccess(ctx context.Context, userID string, topicPattern string, permission ntfycli.Permission) error
	ResetAccess(ctx context.Context, userID string, topicPattern string) error
	AddToken(ctx context.Context, userID string, label string) (string, error)
	ListTokens(ctx context.Context, userID string) ([]ntfycli.Token, error)
	RemoveToken(ctx context.Context, userID string, token string) error
	ListUsers(ctx context.Context) ([]ntfycli.User, error)
}

// PersonServiceClient is the subset of the person-service dual-write client
// (internal/personsvc) the service depends on.
type PersonServiceClient interface {
	Configured() bool
	UpsertPerson(ctx context.Context, personHash string, email string) error
}

// ServiceConfig configures a Service.
type ServiceConfig struct {
	Client NtfyClient
	// GeneratePassword returns the throwaway password for newly created
	// users (people authenticate with tokens, never this password).
	GeneratePassword func() (string, error)
	// PersonClient is the optional person-service dual-write client. When
	// nil (or PersonClient.Configured() is false), Provision skips the
	// dual-write entirely.
	PersonClient PersonServiceClient
	// Logger receives best-effort dual-write failure warnings. Optional;
	// defaults to a discarding logger.
	Logger *slog.Logger
}

// Service orchestrates ntfy CLI operations into provision/deprovision calls.
type Service struct {
	client           NtfyClient
	generatePassword func() (string, error)
	personClient     PersonServiceClient
	logger           *slog.Logger
}

// NewService builds a Service from config.
func NewService(config ServiceConfig) *Service {
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		client:           config.Client,
		generatePassword: config.GeneratePassword,
		personClient:     config.PersonClient,
		logger:           logger,
	}
}

// ProvisionRequest identifies the person (by email) to provision into an
// app. AppUserID is the calling app's own user id, retained for caller
// reference/logging only — it is never sent to ntfy; the derived ntfy
// identity is keyed on Email alone.
type ProvisionRequest struct {
	AppID     string
	AppUserID string
	Email     string
}

// ProvisionResult is returned to the caller; UserID is the derived ntfy
// username ("u_<personHash>"), and Token is the app-labeled ntfy access
// token the consuming app must store for this person.
type ProvisionResult struct {
	UserID       string
	AppID        string
	PersonHash   string
	TopicPattern string
	Token        string
}

// Provision ensures the person's global ntfy user exists, grants a scoped
// read-only topic ACL for this app, and issues a fresh app-labeled token
// (removing stale tokens for the same app so repeated provisioning does not
// accumulate credentials).
func (service *Service) Provision(ctx context.Context, request ProvisionRequest) (ProvisionResult, error) {
	personHash := personhash.Hash(request.Email)
	ntfyUserID := personhash.NtfyUser(request.Email)
	topicPattern := ntfycli.TopicPattern(request.AppID, personHash)

	password, passwordErr := service.generatePassword()
	if passwordErr != nil {
		return ProvisionResult{}, passwordErr
	}
	if addUserErr := service.client.AddUser(ctx, ntfyUserID, password); addUserErr != nil && !errors.Is(addUserErr, ntfycli.ErrAlreadyExists) {
		return ProvisionResult{}, addUserErr
	}
	if grantErr := service.client.GrantAccess(ctx, ntfyUserID, topicPattern, ntfycli.PermissionReadOnly); grantErr != nil {
		return ProvisionResult{}, grantErr
	}
	existingTokens, listErr := service.client.ListTokens(ctx, ntfyUserID)
	if listErr != nil {
		return ProvisionResult{}, listErr
	}
	for _, existingToken := range existingTokens {
		if existingToken.Label == request.AppID {
			if removeErr := service.client.RemoveToken(ctx, ntfyUserID, existingToken.Value); removeErr != nil {
				return ProvisionResult{}, removeErr
			}
		}
	}
	tokenValue, tokenErr := service.client.AddToken(ctx, ntfyUserID, request.AppID)
	if tokenErr != nil {
		return ProvisionResult{}, tokenErr
	}

	service.dualWritePerson(ctx, personHash, request.Email)

	return ProvisionResult{
		UserID:       ntfyUserID,
		AppID:        request.AppID,
		PersonHash:   personHash,
		TopicPattern: topicPattern,
		Token:        tokenValue,
	}, nil
}

// dualWritePerson best-effort mirrors the personHash -> email mapping to the
// person-service Worker's D1 reverse-index, after ntfy provisioning has
// already succeeded. ntfy is the source of truth and must succeed for
// Provision to succeed; person-service exists only so operators can resolve
// a personHash back to an email later (support/debugging), so a failure here
// is logged and swallowed rather than failing the provision. Skips the call
// entirely when no person client is configured (e.g. the local dev stack,
// which has no Worker).
func (service *Service) dualWritePerson(ctx context.Context, personHash string, email string) {
	if service.personClient == nil || !service.personClient.Configured() {
		return
	}
	if upsertErr := service.personClient.UpsertPerson(ctx, personHash, personhash.Normalize(email)); upsertErr != nil {
		service.logger.Warn("person-service dual-write failed", "person_hash", personHash, "error", upsertErr)
	}
}

// ProvisionAppRequest identifies the app to mint a publisher identity for.
type ProvisionAppRequest struct {
	AppID string
}

// ProvisionAppResult is returned to the caller; PublisherUserID is the
// derived ntfy username ("{app_id}-publisher"), and Token is the freshly
// minted write-only ntfy access token the operator must hand off to the app.
type ProvisionAppResult struct {
	AppID           string
	PublisherUserID string
	TopicPattern    string
	Token           string
}

// ProvisionApp ensures the app's publisher ntfy user exists, grants a
// write-only app-wide topic ACL, and issues a fresh publisher-labeled token.
// Unlike Provision, repeat calls do NOT remove existing publisher tokens — a
// repeat call mints an additional token (rotation by issuing a new
// credential; the operator revokes old ones explicitly).
func (service *Service) ProvisionApp(ctx context.Context, request ProvisionAppRequest) (ProvisionAppResult, error) {
	publisherUserID := ntfycli.PublisherUserID(request.AppID)
	topicPattern := ntfycli.PublisherTopicPattern(request.AppID)

	password, passwordErr := service.generatePassword()
	if passwordErr != nil {
		return ProvisionAppResult{}, passwordErr
	}
	if addUserErr := service.client.AddUser(ctx, publisherUserID, password); addUserErr != nil && !errors.Is(addUserErr, ntfycli.ErrAlreadyExists) {
		return ProvisionAppResult{}, addUserErr
	}
	if grantErr := service.client.GrantAccess(ctx, publisherUserID, topicPattern, ntfycli.PermissionWriteOnly); grantErr != nil {
		return ProvisionAppResult{}, grantErr
	}
	tokenValue, tokenErr := service.client.AddToken(ctx, publisherUserID, ntfycli.PublisherTokenLabel)
	if tokenErr != nil {
		return ProvisionAppResult{}, tokenErr
	}
	return ProvisionAppResult{
		AppID:           request.AppID,
		PublisherUserID: publisherUserID,
		TopicPattern:    topicPattern,
		Token:           tokenValue,
	}, nil
}

// UserSummary is the admin-facing view of one ntfy user: the derived list of
// apps the user is provisioned into, plus the raw topic patterns for
// transparency.
type UserSummary struct {
	UserID        string
	Apps          []string
	TopicPatterns []string
}

// wildcardSuffix marks a topic pattern as an app-wide publisher-identity
// grant (the shape ProvisionApp creates via ntfycli.PublisherTopicPattern).
const wildcardSuffix = "-*"

// scopedTopicPattern matches a per-person scoped topic pattern,
// "{app_id}-{personHash}-*", capturing the app_id in group 1.
var scopedTopicPattern = regexp.MustCompile(`^([a-z0-9_]+)-([a-z2-7]{16})-\*$`)

// appFromTopicPattern derives the app_id a topic pattern grants access
// into, and whether the pattern is a recognized provisioning grant at all.
func appFromTopicPattern(topicPattern string) (string, bool) {
	if match := scopedTopicPattern.FindStringSubmatch(topicPattern); match != nil {
		return match[1], true
	}
	if appID, isWildcard := strings.CutSuffix(topicPattern, wildcardSuffix); isWildcard {
		return appID, true
	}
	return "", false
}

// ListUsers returns every user with the apps derived from their topic
// grants (both the scoped per-person shape and the legacy/publisher
// wildcard shape).
func (service *Service) ListUsers(ctx context.Context) ([]UserSummary, error) {
	ntfyUsers, listErr := service.client.ListUsers(ctx)
	if listErr != nil {
		return nil, listErr
	}

	summaries := make([]UserSummary, 0, len(ntfyUsers))
	for _, ntfyUser := range ntfyUsers {
		apps := []string{}
		for _, topicPattern := range ntfyUser.TopicPatterns {
			if appID, recognized := appFromTopicPattern(topicPattern); recognized {
				apps = append(apps, appID)
			}
		}
		summaries = append(summaries, UserSummary{
			UserID:        ntfyUser.Name,
			Apps:          apps,
			TopicPatterns: ntfyUser.TopicPatterns,
		})
	}
	return summaries, nil
}

// DeleteUser removes the user entirely (ntfy drops their grants and tokens).
func (service *Service) DeleteUser(ctx context.Context, userID string) error {
	return service.client.DeleteUser(ctx, userID)
}

// DeprovisionRequest identifies the app/ntfy-user pair to deprovision. The
// HTTP layer resolves an email to NtfyUserID before calling Deprovision; the
// admin UI passes the ntfy user id directly.
type DeprovisionRequest struct {
	AppID      string
	NtfyUserID string
}

// Deprovision revokes the app's scoped topic ACL for the person and removes
// the person's tokens labeled with the app. If the person is left with zero
// remaining topic patterns after the reset, their ntfy user is deleted
// entirely ("left the whole family" semantics) — otherwise they are kept,
// since they may still be provisioned into other apps. If the user is
// somehow absent from the user list after a successful reset, this is
// treated as already gone. ntfycli.ErrNotFound from ResetAccess propagates.
func (service *Service) Deprovision(ctx context.Context, request DeprovisionRequest) error {
	personHash := strings.TrimPrefix(request.NtfyUserID, "u_")
	topicPattern := ntfycli.TopicPattern(request.AppID, personHash)

	if resetErr := service.client.ResetAccess(ctx, request.NtfyUserID, topicPattern); resetErr != nil {
		return resetErr
	}
	existingTokens, listErr := service.client.ListTokens(ctx, request.NtfyUserID)
	if listErr != nil {
		return listErr
	}
	for _, existingToken := range existingTokens {
		if existingToken.Label == request.AppID {
			if removeErr := service.client.RemoveToken(ctx, request.NtfyUserID, existingToken.Value); removeErr != nil {
				return removeErr
			}
		}
	}

	ntfyUsers, listUsersErr := service.client.ListUsers(ctx)
	if listUsersErr != nil {
		return listUsersErr
	}
	for _, ntfyUser := range ntfyUsers {
		if ntfyUser.Name != request.NtfyUserID {
			continue
		}
		if len(ntfyUser.TopicPatterns) == 0 {
			return service.client.DeleteUser(ctx, request.NtfyUserID)
		}
		return nil
	}
	return nil
}
