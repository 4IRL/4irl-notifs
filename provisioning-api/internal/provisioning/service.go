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
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/validation"
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
// (internal/personsvc) the service depends on. Writes and deletes are
// best-effort: ntfy is the source of truth, so a person-service failure is
// logged and swallowed rather than failing the ntfy-side operation.
type PersonServiceClient interface {
	Configured() bool
	UpsertPerson(ctx context.Context, personHash string, email string) error
	// DeletePerson removes a personHash -> email reverse-index row (idempotent).
	DeletePerson(ctx context.Context, personHash string) error
	// DeleteApp removes an app registry row (idempotent).
	DeleteApp(ctx context.Context, appID string) error
}

// NotificationPublisher is the subset of the outbound ntfy HTTP publisher
// (internal/ntfypublish) that TestNotify depends on: a Configured() gate and a
// Publish that POSTs a message to a concrete topic with a bearer token,
// returning the ntfy message id.
type NotificationPublisher interface {
	Configured() bool
	Publish(ctx context.Context, topic string, token string, message string) (string, error)
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
	// Publisher is the optional outbound ntfy HTTP publisher. When nil (or
	// Publisher.Configured() is false), TestNotify returns an error rather
	// than attempting a publish.
	Publisher NotificationPublisher
	// Logger receives best-effort dual-write failure warnings. Optional;
	// defaults to a discarding logger.
	Logger *slog.Logger
}

// Service orchestrates ntfy CLI operations into provision/deprovision calls.
type Service struct {
	client           NtfyClient
	generatePassword func() (string, error)
	personClient     PersonServiceClient
	publisher        NotificationPublisher
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
		publisher:        config.Publisher,
		logger:           logger,
	}
}

// ProvisionRequest identifies the person (by email) to provision into an
// app. The derived ntfy identity is keyed on Email alone.
type ProvisionRequest struct {
	AppID string
	Email string
}

// ProvisionResult is returned to the caller; UserID is the derived ntfy
// username ("u_<personHash>"), and Token is the app-labeled ntfy access
// token the consuming app must store for this person.
type ProvisionResult struct {
	UserID       string
	AppID        string
	PersonHash   string
	TopicPattern string
	// BroadcastTopic is the per-app broadcast topic ("{app_id}-broadcast")
	// this subscriber was granted read on, surfaced for parity with
	// TopicPattern and discoverability by consuming apps.
	BroadcastTopic string
	Token          string
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
	// Grant read on the per-app broadcast topic ({app_id}-broadcast) so this
	// subscriber receives site-wide announcements (Shape A: an authenticated
	// per-user read grant, always-on per app). Both are "ro" grants on the same
	// user; failing here returns before the token is minted (same contract as
	// the scoped grant), and re-provisioning stays idempotent.
	if grantErr := service.client.GrantAccess(ctx, ntfyUserID,
		ntfycli.BroadcastTopicPattern(request.AppID), ntfycli.PermissionReadOnly); grantErr != nil {
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
		UserID:         ntfyUserID,
		AppID:          request.AppID,
		PersonHash:     personHash,
		TopicPattern:   topicPattern,
		BroadcastTopic: ntfycli.BroadcastTopicPattern(request.AppID),
		Token:          tokenValue,
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

// dualDeletePerson best-effort removes the personHash -> email reverse-index
// row from the person-service Worker, after the ntfy user has already been
// deleted (or confirmed already gone). Symmetric to dualWritePerson: ntfy is
// the source of truth, so a person-service failure is logged and swallowed
// rather than failing the delete. Skips the call entirely when no person
// client is configured (e.g. the local dev stack).
func (service *Service) dualDeletePerson(ctx context.Context, personHash string) {
	if service.personClient == nil || !service.personClient.Configured() {
		return
	}
	if deleteErr := service.personClient.DeletePerson(ctx, personHash); deleteErr != nil {
		service.logger.Warn("person-service dual-delete (person) failed", "person_hash", personHash, "error", deleteErr)
	}
}

// dualDeleteApp best-effort removes the app registry row from the
// person-service Worker, after the app's ntfy footprint (publisher identity +
// every subscriber grant) has already been torn down. Best-effort for the same
// reason as dualDeletePerson.
func (service *Service) dualDeleteApp(ctx context.Context, appID string) {
	if service.personClient == nil || !service.personClient.Configured() {
		return
	}
	if deleteErr := service.personClient.DeleteApp(ctx, appID); deleteErr != nil {
		service.logger.Warn("person-service dual-delete (app) failed", "app_id", appID, "error", deleteErr)
	}
}

// ProvisionAppRequest identifies the app to mint a publisher identity for.
// Rotate selects the token behavior: false (the default) is an additive mint
// (a new publisher token is issued and any existing ones stay valid — safe,
// zero-downtime rotation by-issuing); true is a hard rotation that first
// revokes every existing publisher-labeled token, then mints one fresh (for a
// leaked token — it stops working immediately, at the cost of breaking the
// app's publishing until it is redeployed with the new token).
type ProvisionAppRequest struct {
	AppID  string
	Rotate bool
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
// By default (Rotate false) repeat calls do NOT remove existing publisher
// tokens — a repeat call mints an additional token (safe rotation by issuing a
// new credential; the operator revokes old ones explicitly). When Rotate is
// true, every existing publisher-labeled token is revoked first, then one
// fresh token is minted — a hard rotation for a leaked credential (mirrors
// Provision's per-app token reset).
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
	// Hard rotation: revoke existing publisher-labeled tokens BEFORE minting so
	// the newly issued one is never itself removed. Only publisher-labeled
	// tokens are touched (there is exactly one publisher identity per app, so
	// all its tokens are publisher tokens, but the label filter keeps this
	// symmetric with Provision's app-labeled reset).
	if request.Rotate {
		existingTokens, listErr := service.client.ListTokens(ctx, publisherUserID)
		if listErr != nil {
			return ProvisionAppResult{}, listErr
		}
		for _, existingToken := range existingTokens {
			if existingToken.Label == ntfycli.PublisherTokenLabel {
				if removeErr := service.client.RemoveToken(ctx, publisherUserID, existingToken.Value); removeErr != nil {
					return ProvisionAppResult{}, removeErr
				}
			}
		}
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

// TestNotifyRequest is the admin "send test notification" request: dispatch a
// message on a channel to one or more recipients of an app. Recipients are
// either bare person-hashes or emails (resolved per-recipient). The httpapi
// layer owns the wire shape, so these fields carry no json tags.
type TestNotifyRequest struct {
	AppID      string
	Recipients []string
	Channel    string
	Message    string
}

// RecipientResult is the per-recipient outcome of a TestNotify dispatch. OK
// reports whether the publish to that recipient's topic succeeded; a malformed
// recipient or a failed publish sets OK false with a populated Error, without
// failing the rest of the batch.
type RecipientResult struct {
	Recipient string
	UserID    string
	Topic     string
	OK        bool
	MessageID string
	Error     string
}

// TestNotifyResult wraps the per-recipient results of a TestNotify dispatch.
type TestNotifyResult struct {
	Results []RecipientResult
}

// TestNotify mints an ephemeral, write-only publisher token (idempotently
// ensuring the app's publisher identity first, exactly like ProvisionApp), then
// publishes the message to each recipient's concrete topic
// ("{app_id}-{personHash}-{channel}") over HTTP, then revokes the token in an
// always-run deferred cleanup. The ephemeral token is labeled distinctly
// (TestNotifyTokenLabel) so ProvisionApp's publisher-token rotate never sweeps
// it. Results are reported per recipient: a malformed recipient or a failed
// publish is recorded as OK false without failing the whole batch (mint/publish
// order: mint once, publish to all, revoke once). A revoke failure is logged,
// never fatal.
func (service *Service) TestNotify(ctx context.Context, request TestNotifyRequest) (TestNotifyResult, error) {
	if service.publisher == nil || !service.publisher.Configured() {
		return TestNotifyResult{}, fmt.Errorf("ntfy publisher not configured")
	}

	publisherUserID := ntfycli.PublisherUserID(request.AppID)
	topicPattern := ntfycli.PublisherTopicPattern(request.AppID)

	password, passwordErr := service.generatePassword()
	if passwordErr != nil {
		return TestNotifyResult{}, passwordErr
	}
	if addUserErr := service.client.AddUser(ctx, publisherUserID, password); addUserErr != nil && !errors.Is(addUserErr, ntfycli.ErrAlreadyExists) {
		return TestNotifyResult{}, addUserErr
	}
	if grantErr := service.client.GrantAccess(ctx, publisherUserID, topicPattern, ntfycli.PermissionWriteOnly); grantErr != nil {
		return TestNotifyResult{}, grantErr
	}

	token, tokenErr := service.client.AddToken(ctx, publisherUserID, ntfycli.TestNotifyTokenLabel)
	if tokenErr != nil {
		return TestNotifyResult{}, tokenErr
	}
	// Revoke the ephemeral token unconditionally once the dispatch returns, even
	// if a publish errors or panics. A revoke failure is logged, not fatal — the
	// notifications were already sent and a stale write-only token is low-risk.
	// Use a fresh background context (not the cancellable request ctx) so a
	// client disconnect mid-batch can't cancel the revoke and strand the
	// ephemeral write-only token.
	defer func() {
		revokeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if removeErr := service.client.RemoveToken(revokeCtx, publisherUserID, token); removeErr != nil {
			service.logger.Warn("test-notify token revoke failed", "user", publisherUserID, "err", removeErr)
		}
	}()

	results := make([]RecipientResult, 0, len(request.Recipients))
	for _, recipient := range request.Recipients {
		result := RecipientResult{Recipient: recipient}

		var personHash string
		switch {
		case personHashPattern.MatchString(recipient):
			personHash = recipient
		case validation.IsValidEmail(recipient):
			personHash = personhash.Hash(recipient)
		default:
			result.Error = "invalid recipient"
			results = append(results, result)
			continue
		}

		result.UserID = personhash.NtfyUserFromHash(personHash)
		topic := ntfycli.PersonChannelTopic(request.AppID, personHash, request.Channel)
		result.Topic = topic

		messageID, pubErr := service.publisher.Publish(ctx, topic, token, request.Message)
		if pubErr != nil {
			result.Error = pubErr.Error()
		} else {
			result.OK = true
			result.MessageID = messageID
		}
		results = append(results, result)
	}

	return TestNotifyResult{Results: results}, nil
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

// broadcastSuffix marks a topic pattern as the per-app broadcast grant
// ("{app_id}-broadcast", built by ntfycli.BroadcastTopicPattern) — the single
// shared topic every subscriber of the app is granted read on (Shape A).
const broadcastSuffix = "-broadcast"

// scopedTopicPattern matches a per-person scoped topic pattern,
// "{app_id}-{personHash}-*", capturing the app_id in group 1.
var scopedTopicPattern = regexp.MustCompile(`^([a-z0-9_]+)-([a-z2-7]{16})-\*$`)

// personHashPattern matches a bare person-hash (the base32(sha256(email))[:16]
// shape, [a-z2-7]{16}). TestNotify uses it to tell a person-hash recipient
// apart from an email recipient during per-recipient resolution.
var personHashPattern = regexp.MustCompile(`^[a-z2-7]{16}$`)

// appFromTopicPattern derives the app_id a topic pattern grants access
// into, and whether the pattern is a recognized provisioning grant at all.
func appFromTopicPattern(topicPattern string) (string, bool) {
	if match := scopedTopicPattern.FindStringSubmatch(topicPattern); match != nil {
		return match[1], true
	}
	// Recognize the broadcast grant before the "-*" wildcard fallback.
	// Unambiguous: app_id has no hyphen and a scoped/wildcard pattern never
	// ends in "-broadcast".
	if appID, isBroadcast := strings.CutSuffix(topicPattern, broadcastSuffix); isBroadcast {
		return appID, true
	}
	if appID, isWildcard := strings.CutSuffix(topicPattern, wildcardSuffix); isWildcard {
		return appID, true
	}
	return "", false
}

// userHasScopedAppGrant reports whether the user holds a per-person scoped
// grant ("{app_id}-{personHash}-*") for appID — i.e. they are a subscriber of
// that app. The app-wide wildcard grant ("{app_id}-*") held by the publisher
// identity is deliberately NOT matched, so a cascade can tear subscribers down
// via Deprovision (which targets the scoped pattern) while the publisher
// identity is deleted separately. For the broader selector that also catches a
// broadcast-only remnant, see userHasAppGrant (below), which composes this
// helper with the broadcast-grant check rather than folding it in here — this
// helper's narrower scoped-only contract stays intact for any other caller.
func userHasScopedAppGrant(user ntfycli.User, appID string) bool {
	for _, topicPattern := range user.TopicPatterns {
		if match := scopedTopicPattern.FindStringSubmatch(topicPattern); match != nil && match[1] == appID {
			return true
		}
	}
	return false
}

// userHasAppGrant reports whether the user holds any grant that makes them a
// subscriber of appID — the scoped per-person grant (userHasScopedAppGrant) OR
// the shared broadcast grant ("{app_id}-broadcast"). Composing on top of
// userHasScopedAppGrant (rather than folding broadcast into it) keeps that
// helper's narrower, well-documented scoped-only contract intact for any other
// caller, while giving DeprovisionApp the broader selector it needs to also
// catch half-torn-down (broadcast-only) remnants.
func userHasAppGrant(user ntfycli.User, appID string) bool {
	if userHasScopedAppGrant(user, appID) {
		return true
	}
	broadcastTopic := ntfycli.BroadcastTopicPattern(appID)
	for _, topicPattern := range user.TopicPatterns {
		if topicPattern == broadcastTopic {
			return true
		}
	}
	return false
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
		// Dedupe recognized app_ids so a user holding both the scoped and
		// broadcast grant for an app surfaces that app once (preserving
		// first-seen order for the admin UI's stable key/render).
		seen := make(map[string]struct{})
		apps := []string{}
		for _, topicPattern := range ntfyUser.TopicPatterns {
			if appID, recognized := appFromTopicPattern(topicPattern); recognized {
				if _, dup := seen[appID]; !dup {
					seen[appID] = struct{}{}
					apps = append(apps, appID)
				}
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

// DeleteUser removes the user entirely (ntfy drops their grants and tokens),
// then best-effort dual-deletes the person's reverse-index row so the two
// stores stay in sync. A full teardown is idempotent: an already-gone ntfy
// user (ntfycli.ErrNotFound) is treated as success so the person row is still
// cleaned up and a repeat delete does not 404. Only "u_"-prefixed ids carry a
// person row, so the dual-delete is scoped to those (a publisher identity like
// "{app}-publisher" has no reverse-index row).
func (service *Service) DeleteUser(ctx context.Context, userID string) error {
	if deleteErr := service.client.DeleteUser(ctx, userID); deleteErr != nil && !errors.Is(deleteErr, ntfycli.ErrNotFound) {
		return deleteErr
	}
	if personHash, isPersonUser := strings.CutPrefix(userID, "u_"); isPersonUser {
		service.dualDeletePerson(ctx, personHash)
	}
	return nil
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
	// Reset the per-app broadcast grant too, BEFORE the zero-count ListUsers
	// check below — otherwise a user deprovisioned from their last app would keep
	// a lingering {app_id}-broadcast grant, never reach TopicPatterns == 0, and
	// be orphaned. Tolerate ErrNotFound (Decision 3): a pre-existing subscriber
	// provisioned before broadcast shipped has no broadcast grant to reset.
	if resetErr := service.client.ResetAccess(ctx, request.NtfyUserID,
		ntfycli.BroadcastTopicPattern(request.AppID)); resetErr != nil &&
		!errors.Is(resetErr, ntfycli.ErrNotFound) {
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
			// Deprovisioning the person's last app deletes their ntfy user
			// entirely, so their reverse-index row must go too — otherwise
			// deprovision-to-empty orphans the person row (the same bug
			// DeleteUser's dual-delete fixes for the explicit-delete path).
			if deleteErr := service.client.DeleteUser(ctx, request.NtfyUserID); deleteErr != nil {
				return deleteErr
			}
			if personHash, isPersonUser := strings.CutPrefix(request.NtfyUserID, "u_"); isPersonUser {
				service.dualDeletePerson(ctx, personHash)
			}
			return nil
		}
		return nil
	}
	return nil
}

// DeprovisionAppRequest identifies the app to fully tear down.
type DeprovisionAppRequest struct {
	AppID string
}

// DeprovisionApp removes an app entirely (the cascade behind the admin UI's
// "Remove App"): it revokes every subscriber's scoped grant for the app —
// deleting a subscriber's ntfy user when this was their last app, via the same
// per-app Deprovision path — then deletes the app's publisher identity, then
// best-effort deletes the app registry row. ntfy (the irreversible teardown)
// goes first; the registry cleanup is the trivial last step. The whole
// operation is idempotent: an already-gone publisher (ntfycli.ErrNotFound) is
// tolerated and per-subscriber Deprovision is itself safe to repeat, so a
// re-run — or a Remove of an already-removed app — is harmless.
//
// Scope: this targets the grant shapes the current model produces — the
// canonical publisher identity ("{app_id}-publisher", holding the app-wide
// wildcard) and per-person subscribers, selected via userHasAppGrant so that
// both the scoped grant ("{app_id}-{personHash}-*") AND the shared broadcast
// grant ("{app_id}-broadcast") count — the latter catches a half-torn-down
// (broadcast-only) remnant that a scoped-only selector would skip. A raw
// app-wide wildcard grant held by some OTHER (non-publisher) user is a legacy
// shape the current provisioning flow never creates, so it is deliberately out
// of scope here rather than force-deleted by heuristic.
func (service *Service) DeprovisionApp(ctx context.Context, request DeprovisionAppRequest) error {
	ntfyUsers, listErr := service.client.ListUsers(ctx)
	if listErr != nil {
		return listErr
	}
	for _, ntfyUser := range ntfyUsers {
		if !userHasAppGrant(ntfyUser, request.AppID) {
			continue
		}
		if deprovErr := service.Deprovision(ctx, DeprovisionRequest{AppID: request.AppID, NtfyUserID: ntfyUser.Name}); deprovErr != nil {
			return deprovErr
		}
	}

	publisherUserID := ntfycli.PublisherUserID(request.AppID)
	if deleteErr := service.client.DeleteUser(ctx, publisherUserID); deleteErr != nil && !errors.Is(deleteErr, ntfycli.ErrNotFound) {
		return deleteErr
	}

	service.dualDeleteApp(ctx, request.AppID)
	return nil
}
