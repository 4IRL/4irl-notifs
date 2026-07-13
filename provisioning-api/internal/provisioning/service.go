// Package provisioning implements the provision/deprovision semantics of the
// notification hub: one global ntfy user per person, one wildcard topic ACL
// ("{app_id}-*") plus one app-labeled access token per app the user is
// provisioned into.
package provisioning

import (
	"context"
	"errors"
	"strings"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
)

// NtfyClient is the subset of the ntfy CLI client the service depends on.
type NtfyClient interface {
	AddUser(ctx context.Context, userID string, password string) error
	DeleteUser(ctx context.Context, userID string) error
	GrantAccess(ctx context.Context, userID string, appID string) error
	ResetAccess(ctx context.Context, userID string, appID string) error
	AddToken(ctx context.Context, userID string, appID string) (string, error)
	ListTokens(ctx context.Context, userID string) ([]ntfycli.Token, error)
	RemoveToken(ctx context.Context, userID string, token string) error
	ListUsers(ctx context.Context) ([]ntfycli.User, error)
}

// ServiceConfig configures a Service.
type ServiceConfig struct {
	Client NtfyClient
	// GeneratePassword returns the throwaway password for newly created
	// users (people authenticate with tokens, never this password).
	GeneratePassword func() (string, error)
}

// Service orchestrates ntfy CLI operations into provision/deprovision calls.
type Service struct {
	client           NtfyClient
	generatePassword func() (string, error)
}

// NewService builds a Service from config.
func NewService(config ServiceConfig) *Service {
	return &Service{client: config.Client, generatePassword: config.GeneratePassword}
}

// ProvisionRequest identifies the app/user pair to provision.
type ProvisionRequest struct {
	AppID  string
	UserID string
}

// ProvisionResult is returned to the caller; Token is the app-labeled ntfy
// access token the consuming app must store for this user.
type ProvisionResult struct {
	UserID       string
	AppID        string
	TopicPattern string
	Token        string
}

// Provision ensures the user exists, grants the app's wildcard topic ACL,
// and issues a fresh app-labeled token (removing stale tokens for the same
// app so repeated provisioning does not accumulate credentials).
func (service *Service) Provision(ctx context.Context, request ProvisionRequest) (ProvisionResult, error) {
	password, passwordErr := service.generatePassword()
	if passwordErr != nil {
		return ProvisionResult{}, passwordErr
	}
	if addUserErr := service.client.AddUser(ctx, request.UserID, password); addUserErr != nil && !errors.Is(addUserErr, ntfycli.ErrAlreadyExists) {
		return ProvisionResult{}, addUserErr
	}
	if grantErr := service.client.GrantAccess(ctx, request.UserID, request.AppID); grantErr != nil {
		return ProvisionResult{}, grantErr
	}
	existingTokens, listErr := service.client.ListTokens(ctx, request.UserID)
	if listErr != nil {
		return ProvisionResult{}, listErr
	}
	for _, existingToken := range existingTokens {
		if existingToken.Label == request.AppID {
			if removeErr := service.client.RemoveToken(ctx, request.UserID, existingToken.Value); removeErr != nil {
				return ProvisionResult{}, removeErr
			}
		}
	}
	tokenValue, tokenErr := service.client.AddToken(ctx, request.UserID, request.AppID)
	if tokenErr != nil {
		return ProvisionResult{}, tokenErr
	}
	return ProvisionResult{
		UserID:       request.UserID,
		AppID:        request.AppID,
		TopicPattern: ntfycli.TopicPattern(request.AppID),
		Token:        tokenValue,
	}, nil
}

// UserSummary is the admin-facing view of one ntfy user: the derived list of
// apps the user is provisioned into (from "{app_id}-*" grants) plus the raw
// topic patterns for transparency.
type UserSummary struct {
	UserID        string
	Apps          []string
	TopicPatterns []string
}

// wildcardSuffix marks a topic pattern as an app-wide provisioning grant.
const wildcardSuffix = "-*"

// ListUsers returns every user with the apps derived from their wildcard
// topic grants.
func (service *Service) ListUsers(ctx context.Context) ([]UserSummary, error) {
	ntfyUsers, listErr := service.client.ListUsers(ctx)
	if listErr != nil {
		return nil, listErr
	}

	summaries := make([]UserSummary, 0, len(ntfyUsers))
	for _, ntfyUser := range ntfyUsers {
		apps := []string{}
		for _, topicPattern := range ntfyUser.TopicPatterns {
			if appID, isWildcard := strings.CutSuffix(topicPattern, wildcardSuffix); isWildcard {
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

// Deprovision revokes the app's wildcard topic ACL for the user and removes
// the user's tokens labeled with the app. The user itself is kept (they may
// still be provisioned into other apps); ntfycli.ErrNotFound propagates when
// the user does not exist.
func (service *Service) Deprovision(ctx context.Context, request ProvisionRequest) error {
	if resetErr := service.client.ResetAccess(ctx, request.UserID, request.AppID); resetErr != nil {
		return resetErr
	}
	existingTokens, listErr := service.client.ListTokens(ctx, request.UserID)
	if listErr != nil {
		return listErr
	}
	for _, existingToken := range existingTokens {
		if existingToken.Label == request.AppID {
			if removeErr := service.client.RemoveToken(ctx, request.UserID, existingToken.Value); removeErr != nil {
				return removeErr
			}
		}
	}
	return nil
}
