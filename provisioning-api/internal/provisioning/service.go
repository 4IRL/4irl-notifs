// Package provisioning implements the provision/deprovision semantics of the
// notification hub: one global ntfy user per person, derived from an
// email-hash identity shared by every 4IRL app, with a scoped read-only
// topic ACL ("{app_id}-{personHash}-*") plus one app-labeled access token
// granted per app the person is provisioned into. Most consuming apps
// authenticate via the issued token; a person may separately set an ntfy
// password (via Service.SetPassword) to log into the ntfy iOS app directly.
package provisioning

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/4IRL/4irl-notifs/provisioning-api/internal/enforcement"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfycli"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/ntfypublish"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/personsvc"
	"github.com/4IRL/4irl-notifs/provisioning-api/internal/validation"
)

// NtfyClient is the subset of the ntfy CLI client the service depends on.
type NtfyClient interface {
	AddUser(ctx context.Context, userID string, password string) error
	ChangePassword(ctx context.Context, userID string, password string) error
	DeleteUser(ctx context.Context, userID string) error
	GrantAccess(ctx context.Context, userID string, topicPattern string, permission ntfycli.Permission) error
	ResetAccess(ctx context.Context, userID string, topicPattern string) error
	AddToken(ctx context.Context, userID string, label string) (string, error)
	ListTokens(ctx context.Context, userID string) ([]ntfycli.Token, error)
	RemoveToken(ctx context.Context, userID string, token string) error
	ListUsers(ctx context.Context) ([]ntfycli.User, error)
	// GetUser reads one user's grants; a missing user is ntfycli.ErrNotFound.
	GetUser(ctx context.Context, userID string) (ntfycli.User, error)
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
	// ClaimPassword atomically records password_set_at per mode.
	ClaimPassword(ctx context.Context, personHash string, email string, mode personsvc.ClaimMode) (personsvc.PasswordClaim, error)
	// ClaimPasswordWithStamp is ClaimPassword with a client-generated stamp the Worker stores
	// instead of its own clock, so the caller can release it after a lost response.
	ClaimPasswordWithStamp(ctx context.Context, personHash string, email string, mode personsvc.ClaimMode, stamp string) (personsvc.PasswordClaim, error)
	// ReleasePasswordClaim restores restoreTo only if the stamp still equals claim.SetAt.
	ReleasePasswordClaim(ctx context.Context, personHash string, claim personsvc.PasswordClaim, restoreTo string) error
	// ClaimResetSlot reserves a reset slot; a limit returns *personsvc.ResetLimitError.
	ClaimResetSlot(ctx context.Context, personHash string) (string, error)
	// ReleaseResetSlot gives a reset slot back (idempotent).
	ReleaseResetSlot(ctx context.Context, personHash string, resetID string) error
}

// NotificationPublisher is the subset of the outbound ntfy HTTP publisher
// (internal/ntfypublish) that TestNotify depends on: a Configured() gate and a
// Publish that POSTs an ntfy message (topic, bearer token, body and optional
// title/tags/click) and returns the ntfy message id.
type NotificationPublisher interface {
	Configured() bool
	Publish(ctx context.Context, message ntfypublish.Message) (string, error)
}

// ServiceConfig configures a Service.
type ServiceConfig struct {
	Client NtfyClient
	// GeneratePassword returns the throwaway password for newly created
	// users (people authenticate with tokens, never this password).
	GeneratePassword func() (string, error)
	// GenerateSubscriberPassword returns the typeable password generated for
	// a person's phone sign-in. Nil means the real GenerateSubscriberPassword.
	GenerateSubscriberPassword func() (string, error)
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
	// AppGrantMode switches the per-app grant check between enforce and
	// log-only. The zero value is enforce (fail closed).
	AppGrantMode enforcement.Mode
}

// Service orchestrates ntfy CLI operations into provision/deprovision calls.
type Service struct {
	client                     NtfyClient
	generatePassword           func() (string, error)
	generateSubscriberPassword func() (string, error)
	// personLocks serializes every password-touching flow per person.
	personLocks  keyedLock
	personClient PersonServiceClient
	publisher    NotificationPublisher
	logger       *slog.Logger
	// appGrantMode is the per-app grant check mode (zero value = enforce).
	appGrantMode enforcement.Mode
	// resetAttempts throttles ResetPassword attempts per person/app ahead of
	// the grant lookup (see resetAttemptLimit).
	resetAttempts attemptThrottle
}

// NewService builds a Service from config.
func NewService(config ServiceConfig) *Service {
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	generateSubscriberPassword := config.GenerateSubscriberPassword
	if generateSubscriberPassword == nil {
		generateSubscriberPassword = GenerateSubscriberPassword
	}
	return &Service{
		client:                     config.Client,
		generatePassword:           config.GeneratePassword,
		generateSubscriberPassword: generateSubscriberPassword,
		personClient:               config.PersonClient,
		publisher:                  config.Publisher,
		logger:                     logger,
		appGrantMode:               config.AppGrantMode,
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
	// Password is the first-time ntfy sign-in password, set only when this call
	// generated one. It is shown to the person once and never stored.
	Password string
}

// Provision ensures the person's global ntfy user exists, grants a scoped
// read-only topic ACL for this app, and issues a fresh app-labeled token
// (removing stale tokens for the same app so repeated provisioning does not
// accumulate credentials).
func (service *Service) Provision(ctx context.Context, request ProvisionRequest) (result ProvisionResult, provisionErr error) {
	personHash := personhash.Hash(request.Email)
	ntfyUserID := personhash.NtfyUser(request.Email)
	topicPattern := ntfycli.TopicPattern(request.AppID, personHash)

	unlock := service.personLocks.lock(personHash)
	defer unlock()

	// heldClaim is the password claim this call owns. If anything fails after
	// it is taken, it is released back to NULL so the next Provision claims
	// again and regenerates; an undelivered password is simply superseded.
	var heldClaim *personsvc.PasswordClaim
	defer func() {
		if provisionErr != nil && heldClaim != nil {
			service.releasePasswordClaim(personHash, *heldClaim, "")
		}
	}()

	subscriberPassword, applyErr := service.applySubscriberPassword(ctx, request, personHash, ntfyUserID, &heldClaim)
	if applyErr != nil {
		return ProvisionResult{}, applyErr
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
		Password:       subscriberPassword,
	}, nil
}

// Outcome values of the "subscriber password decision" log line.
const (
	outcomeGenerated        = "generated"
	outcomeAlreadySet       = "already_set"
	outcomeStoreUnavailable = "store_unavailable"
	outcomeHealed           = "healed"
)

// releaseTimeout bounds a claim release; it runs on a fresh context because
// the failure being handled may be a cancelled request.
const releaseTimeout = 5 * time.Second

// personStoreConfigured reports whether a person-service client is usable.
func (service *Service) personStoreConfigured() bool {
	return service.personClient != nil && service.personClient.Configured()
}

// releasePasswordClaim gives a claim back on a fresh bounded context. A failed
// release is logged at Error level with its error text (never fatal).
func (service *Service) releasePasswordClaim(personHash string, claim personsvc.PasswordClaim, restoreTo string) {
	releaseCtx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	if releaseErr := service.personClient.ReleasePasswordClaim(releaseCtx, personHash, claim, restoreTo); releaseErr != nil {
		service.logger.Error("password claim release failed", "person_hash", personHash, "error", releaseErr)
	}
}

// applySubscriberPassword runs the password half of Provision: it claims the
// person's password stamp, creates or rotates the ntfy user's password, and
// returns the generated password ("" when none was generated this call). On a
// successful claim it stores the claim in heldClaim so Provision can release
// it if a later step fails. The caller holds the person lock.
func (service *Service) applySubscriberPassword(ctx context.Context, request ProvisionRequest, personHash string, ntfyUserID string, heldClaim **personsvc.PasswordClaim) (string, error) {
	email := personhash.Normalize(request.Email)

	var claim personsvc.PasswordClaim
	storeAvailable := service.personStoreConfigured()
	if storeAvailable {
		var claimErr error
		// The stamp is generated here so an ambiguous failure (the Worker may
		// have committed it before the response was lost) can still be released.
		claimStamp := personsvc.NewClaimStamp(time.Now())
		claim, claimErr = service.personClient.ClaimPasswordWithStamp(ctx, personHash, email, personsvc.ClaimIfUnset, claimStamp)
		if claimErr != nil {
			ambiguous := personsvc.IsAmbiguousFailure(claimErr)
			service.logger.Error("password claim failed", "person_hash", personHash, "error", claimErr, "ambiguous", ambiguous)
			if ambiguous {
				// Best-effort: a no-op if the stamp was never written; otherwise it
				// returns the person to NULL so a later provision can claim again.
				service.logger.Info("releasing possibly committed password claim", "person_hash", personHash, "stamp", claimStamp)
				service.releasePasswordClaim(personHash, personsvc.PasswordClaim{SetAt: claimStamp}, "")
			}
			storeAvailable = false
		}
	}

	if storeAvailable && claim.Claimed {
		*heldClaim = &claim
		subscriberPassword, generateErr := service.generateSubscriberPassword()
		if generateErr != nil {
			return "", generateErr
		}
		addUserErr := service.client.AddUser(ctx, ntfyUserID, subscriberPassword)
		if errors.Is(addUserErr, ntfycli.ErrAlreadyExists) {
			// A legacy person (NULL stamp, working password): rotate it.
			addUserErr = service.client.ChangePassword(ctx, ntfyUserID, subscriberPassword)
		}
		if addUserErr != nil {
			return "", addUserErr
		}
		service.logger.Info("subscriber password decision", "person_hash", personHash, "app_id", request.AppID, "outcome", outcomeGenerated)
		return subscriberPassword, nil
	}

	// No claim was won: keep the existing password. A newly created ntfy user
	// is given a throwaway password (people authenticate with tokens).
	throwawayPassword, passwordErr := service.generatePassword()
	if passwordErr != nil {
		return "", passwordErr
	}
	addUserErr := service.client.AddUser(ctx, ntfyUserID, throwawayPassword)
	alreadyExisted := errors.Is(addUserErr, ntfycli.ErrAlreadyExists)
	if addUserErr != nil && !alreadyExisted {
		return "", addUserErr
	}

	outcome := outcomeStoreUnavailable
	if storeAvailable {
		outcome = outcomeAlreadySet
		if !alreadyExisted {
			// The stamp is set but the ntfy user did not exist: it is stale
			// (the user was deleted without clearing the person row). Heal it,
			// recording the claim BEFORE changing the ntfy password.
			healedPassword, healErr := service.healStaleStamp(ctx, personHash, email, ntfyUserID, heldClaim)
			if healErr != nil {
				return "", healErr
			}
			if healedPassword != "" {
				service.logger.Info("subscriber password decision", "person_hash", personHash, "app_id", request.AppID, "outcome", outcomeHealed)
				return healedPassword, nil
			}
			outcome = outcomeStoreUnavailable
		}
	}
	service.logger.Info("subscriber password decision", "person_hash", personHash, "app_id", request.AppID, "outcome", outcome)
	return "", nil
}

// healStaleStamp repairs a stale password stamp: it claims `always` first, then
// sets a generated subscriber password. An `always` claim error changes nothing
// and returns "" (the store-unavailable path); a ChangePassword failure
// returns its error and leaves the claim in heldClaim for Provision to release
// back to NULL.
func (service *Service) healStaleStamp(ctx context.Context, personHash string, email string, ntfyUserID string, heldClaim **personsvc.PasswordClaim) (string, error) {
	subscriberPassword, generateErr := service.generateSubscriberPassword()
	if generateErr != nil {
		return "", generateErr
	}
	claim, claimErr := service.personClient.ClaimPassword(ctx, personHash, email, personsvc.ClaimAlways)
	if claimErr != nil {
		service.logger.Error("password claim failed", "person_hash", personHash, "error", claimErr)
		return "", nil
	}
	*heldClaim = &claim
	if changeErr := service.client.ChangePassword(ctx, ntfyUserID, subscriberPassword); changeErr != nil {
		return "", changeErr
	}
	return subscriberPassword, nil
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
	// Title is the ntfy notification title; the httpapi layer guarantees it is
	// non-empty.
	Title string
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

// publishFailedMessage is the generic per-recipient publish-failure text
// returned to the client. The real publish error (which can include a snippet
// of ntfy's raw HTTP error body) is logged server-side instead of surfaced,
// mirroring writeServiceError's log-real / return-generic convention.
const publishFailedMessage = "publish failed"

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

		messageID, publishErr := service.publisher.Publish(ctx, ntfypublish.Message{Topic: topic, Token: token, Body: request.Message, Title: request.Title})
		if publishErr != nil {
			// Log the real error server-side, but return only a generic message
			// to the client so ntfy's raw HTTP body snippet never reaches the
			// admin (consistent with writeServiceError's log-real/return-generic
			// convention).
			service.logger.Warn("test-notify publish failed", "topic", topic, "err", publishErr)
			result.Error = publishFailedMessage
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
// then deletes the person's reverse-index row so the two stores stay in sync.
// A full teardown is idempotent: an already-gone ntfy user
// (ntfycli.ErrNotFound) is treated as success so the person row is still
// cleaned up and a repeat delete does not 404. Only "u_"-prefixed ids carry a
// person row, so the dual-delete is scoped to those (a publisher identity like
// "{app}-publisher" has no reverse-index row).
//
// A person id ("u_" + 16 base32 chars) holds the person lock across both
// deletes and fails loudly when the configured person store's delete fails
// (the call is idempotent, so a retry is safe). Any other id, including a "u_"
// id with a non-hash suffix, only deletes the ntfy user: no person row can
// exist for it, and the Worker would answer 400 on every retry.
func (service *Service) DeleteUser(ctx context.Context, userID string) error {
	personHash, hasPersonRow := personHashFromUserID(userID)
	if hasPersonRow {
		unlock := service.personLocks.lock(personHash)
		defer unlock()
	}
	if deleteErr := service.client.DeleteUser(ctx, userID); deleteErr != nil && !errors.Is(deleteErr, ntfycli.ErrNotFound) {
		return deleteErr
	}
	if hasPersonRow && service.personStoreConfigured() {
		if deleteErr := service.personClient.DeletePerson(ctx, personHash); deleteErr != nil {
			service.logger.Error("person-service delete failed", "person_hash", personHash, "error", deleteErr)
			return deleteErr
		}
	}
	return nil
}

// personHashFromUserID returns the person hash of a person id ("u_" followed by
// a suffix matching personHashPattern), and false for any other id.
func personHashFromUserID(userID string) (string, bool) {
	personHash, hasPrefix := strings.CutPrefix(userID, "u_")
	if !hasPrefix || !personHashPattern.MatchString(personHash) {
		return "", false
	}
	return personHash, true
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
// somehow absent from ntfy after a successful reset, this is treated as
// already gone.
//
// Before any reset, checkAppGrant verifies the person holds a grant in the
// calling app. An unknown ntfy user is caught by that pre-reset GetUser lookup
// and returns nil as the already-gone no-op, so this path never reaches
// ResetAccess; a residual ResetAccess-level ntfycli.ErrNotFound (e.g. a grant
// revoked in the narrow window between the grant check and the reset) still
// propagates as before.
func (service *Service) Deprovision(ctx context.Context, request DeprovisionRequest) error {
	personHash := strings.TrimPrefix(request.NtfyUserID, "u_")
	topicPattern := ntfycli.TopicPattern(request.AppID, personHash)

	alreadyGone, grantErr := service.checkAppGrant(ctx, appGrantCheck{
		route:      grantRouteDeprovision,
		appID:      request.AppID,
		ntfyUserID: request.NtfyUserID,
		personHash: personHash,
	})
	if grantErr != nil {
		return grantErr
	}
	if alreadyGone {
		return nil
	}

	if resetErr := service.client.ResetAccess(ctx, request.NtfyUserID, topicPattern); resetErr != nil {
		return resetErr
	}
	// Reset the per-app broadcast grant too, BEFORE the zero-count GetUser
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

	// The last-app check and teardown hold the person lock so they cannot
	// interleave with a Provision or Reset of the same person. DeprovisionApp
	// takes no lock itself, so the lock is never nested.
	unlock := service.personLocks.lock(personHash)
	defer unlock()

	// One single-user lookup replaces a full ListUsers scan; an absent user
	// (ErrNotFound) is the same already-gone no-op as in the grant check.
	remainingUser, getUserErr := service.client.GetUser(ctx, request.NtfyUserID)
	if getUserErr != nil {
		if errors.Is(getUserErr, ntfycli.ErrNotFound) {
			return nil
		}
		return getUserErr
	}
	if len(remainingUser.TopicPatterns) == 0 {
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

// SetPasswordRequest identifies the person (by email) whose ntfy password
// is being changed to Password. AppID is carried for request-body parity
// with the sibling requests and is echoed back in the response; the service
// body itself uses only Email and Password (the ntfy password is global per
// person).
type SetPasswordRequest struct {
	AppID    string
	Email    string
	Password string
}

// SetPasswordResult is returned to the caller; UserID is the derived ntfy
// username whose password was changed.
type SetPasswordResult struct {
	UserID string
}

// SetPassword changes the person's ntfy password via the CLI, deriving
// their ntfy user id from the email. The user must already exist (a
// missing user propagates ntfycli.ErrNotFound).
//
// The password stamp is recorded BEFORE the password changes (update-only: it
// never creates a person row), so a later Provision cannot replace a password
// the person chose. If the stamp cannot be recorded (store unconfigured or the
// claim fails) nothing changes and an error is returned. If ChangePassword
// fails, the prior stamp is restored (only if it still matches). For an ntfy
// user with no person row, the stamp is recorded after the change succeeds.
//
// Before anything else, checkAppGrant verifies the person holds a grant in the
// calling app (any app they hold works: the password is global per person).
func (service *Service) SetPassword(ctx context.Context, request SetPasswordRequest) (SetPasswordResult, error) {
	personHash := personhash.Hash(request.Email)
	ntfyUserID := personhash.NtfyUser(request.Email)
	email := personhash.Normalize(request.Email)

	if _, grantErr := service.checkAppGrant(ctx, appGrantCheck{
		route:      grantRouteSetPassword,
		appID:      request.AppID,
		ntfyUserID: ntfyUserID,
		personHash: personHash,
	}); grantErr != nil {
		return SetPasswordResult{}, grantErr
	}

	unlock := service.personLocks.lock(personHash)
	defer unlock()

	if !service.personStoreConfigured() {
		service.logger.Error("set-password cannot record password stamp: person store not configured", "person_hash", personHash)
		return SetPasswordResult{}, ErrPersonStoreUnavailable
	}
	claim, claimErr := service.personClient.ClaimPassword(ctx, personHash, email, personsvc.ClaimUpdateOnly)
	if claimErr != nil {
		service.logger.Error("password claim failed", "person_hash", personHash, "error", claimErr)
		return SetPasswordResult{}, claimErr
	}
	if changePassErr := service.client.ChangePassword(ctx, ntfyUserID, request.Password); changePassErr != nil {
		if claim.RowExists && claim.Claimed {
			service.releasePasswordClaim(personHash, claim, claim.PreviousSetAt)
		}
		return SetPasswordResult{}, changePassErr
	}
	if !claim.RowExists {
		service.recordStampAfterChange(ctx, personHash, email)
	}
	return SetPasswordResult{UserID: ntfyUserID}, nil
}

// ErrTooManyPasswordChanges is the sentinel a *TooManyPasswordChangesError
// satisfies via errors.Is.
var ErrTooManyPasswordChanges = errors.New("too many password changes")

// DefaultRetryAfterSeconds is the Retry-After for a reset refused because the
// limit could not be checked (store down or unconfigured), not a true limit.
// The HTTP layer reuses it for a 429 whose error carries no seconds.
const DefaultRetryAfterSeconds = 60

// TooManyPasswordChangesError refuses a reset and says when to retry.
type TooManyPasswordChangesError struct {
	RetryAfterSeconds int
}

func (tooManyErr *TooManyPasswordChangesError) Error() string {
	return fmt.Sprintf("%s: retry after %d seconds", ErrTooManyPasswordChanges.Error(), tooManyErr.RetryAfterSeconds)
}

// Is makes errors.Is(err, ErrTooManyPasswordChanges) hold.
func (tooManyErr *TooManyPasswordChangesError) Is(target error) bool {
	return target == ErrTooManyPasswordChanges
}

// ResetPasswordRequest identifies the person (by email) whose ntfy password is
// replaced by a generated one. AppID is carried for parity and echoed back.
type ResetPasswordRequest struct {
	AppID string
	Email string
}

// ResetPasswordResult carries the new password; it is shown once and never stored.
type ResetPasswordResult struct {
	UserID   string
	AppID    string
	Password string
}

// The ResetPassword attempt throttle: attempts (granted or rejected) allowed per
// person/app pair per rolling window before the grant lookup. It sits well above
// the 5-per-hour reset limit so a legitimate caller never meets it; it only caps
// how many lookups a hammering caller can queue.
const resetAttemptLimit = 20

// Outcome values of the "password reset" log line.
const (
	resetOutcomeReset             = "reset"
	resetOutcomeLimited           = "limited"
	resetOutcomeLimitStoreUnavail = "limit_store_unavailable"
	resetOutcomeStampUnavailable  = "stamp_unavailable"
	resetOutcomeNotFound          = "not_found"
	resetOutcomeFailed            = "failed"
)

// ResetPassword replaces the person's ntfy password with a generated one, at
// most 5 times per person per rolling hour. A cheap in-process attempt throttle
// (resetAttemptLimit per person/app per attemptThrottleWindow, rejected attempts
// included) runs first, so an over-limit caller gets 429 without any ntfy CLI
// call queuing behind the ntfycli mutex. The per-app grant check
// (checkAppGrant) runs next, so a person who does not belong to the calling app
// gets 404 without claiming a reset slot, and that 404 takes precedence over
// the reset limit. After it, the limit is checked first as before (429 wins over
// 404, and an unconfigured or failing limit store refuses). A reset slot is
// claimed before the person lock; the stamp claim and the password change run
// under it. A failed attempt gives its slot (and stamp) back.
func (service *Service) ResetPassword(ctx context.Context, request ResetPasswordRequest) (ResetPasswordResult, error) {
	personHash := personhash.Hash(request.Email)
	ntfyUserID := personhash.NtfyUser(request.Email)
	email := personhash.Normalize(request.Email)

	logOutcome := func(outcome string) {
		service.logger.Info("password reset", "person_hash", personHash, "app_id", request.AppID, "outcome", outcome)
	}

	allowed, retryAfter := service.resetAttempts.allow(personHash+"|"+request.AppID, resetAttemptLimit)
	if !allowed {
		retryAfterSeconds := int(math.Ceil(retryAfter.Seconds()))
		if retryAfterSeconds < 1 {
			retryAfterSeconds = DefaultRetryAfterSeconds
		}
		logOutcome(resetOutcomeLimited)
		return ResetPasswordResult{}, &TooManyPasswordChangesError{RetryAfterSeconds: retryAfterSeconds}
	}

	if _, grantErr := service.checkAppGrant(ctx, appGrantCheck{
		route:      grantRouteResetPassword,
		appID:      request.AppID,
		ntfyUserID: ntfyUserID,
		personHash: personHash,
	}); grantErr != nil {
		return ResetPasswordResult{}, grantErr
	}

	if !service.personStoreConfigured() {
		service.logger.Error("reset refused: person store not configured", "person_hash", personHash)
		logOutcome(resetOutcomeLimitStoreUnavail)
		return ResetPasswordResult{}, &TooManyPasswordChangesError{RetryAfterSeconds: DefaultRetryAfterSeconds}
	}
	slotID, slotErr := service.personClient.ClaimResetSlot(ctx, personHash)
	if slotErr != nil {
		var limitErr *personsvc.ResetLimitError
		if errors.As(slotErr, &limitErr) {
			retryAfter := limitErr.RetryAfterSeconds
			if retryAfter < 1 {
				retryAfter = DefaultRetryAfterSeconds
			}
			logOutcome(resetOutcomeLimited)
			return ResetPasswordResult{}, &TooManyPasswordChangesError{RetryAfterSeconds: retryAfter}
		}
		service.logger.Error("reset slot claim failed", "person_hash", personHash, "error", slotErr)
		logOutcome(resetOutcomeLimitStoreUnavail)
		return ResetPasswordResult{}, &TooManyPasswordChangesError{RetryAfterSeconds: DefaultRetryAfterSeconds}
	}

	// Any failure below gives the slot back; success keeps it.
	slotKept := false
	defer func() {
		if !slotKept {
			service.releaseResetSlot(personHash, slotID)
		}
	}()

	newPassword, generateErr := service.generateSubscriberPassword()
	if generateErr != nil {
		service.logger.Error("subscriber password generation failed", "person_hash", personHash, "error", generateErr)
		logOutcome(resetOutcomeFailed)
		return ResetPasswordResult{}, generateErr
	}

	unlock := service.personLocks.lock(personHash)
	defer unlock()

	claim, claimErr := service.personClient.ClaimPassword(ctx, personHash, email, personsvc.ClaimUpdateOnly)
	if claimErr != nil {
		service.logger.Error("password claim failed", "person_hash", personHash, "error", claimErr)
		logOutcome(resetOutcomeStampUnavailable)
		return ResetPasswordResult{}, claimErr
	}
	if changeErr := service.client.ChangePassword(ctx, ntfyUserID, newPassword); changeErr != nil {
		if claim.RowExists && claim.Claimed {
			service.releasePasswordClaim(personHash, claim, claim.PreviousSetAt)
		}
		if errors.Is(changeErr, ntfycli.ErrNotFound) {
			logOutcome(resetOutcomeNotFound)
		} else {
			logOutcome(resetOutcomeFailed)
		}
		return ResetPasswordResult{}, changeErr
	}
	if !claim.RowExists {
		service.recordStampAfterChange(ctx, personHash, email)
	}
	slotKept = true
	logOutcome(resetOutcomeReset)
	return ResetPasswordResult{UserID: ntfyUserID, AppID: request.AppID, Password: newPassword}, nil
}

// releaseResetSlot gives a reset slot back on a fresh bounded context. A failed
// release is logged at Error level with its error text (never fatal).
func (service *Service) releaseResetSlot(personHash string, slotID string) {
	releaseCtx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	if releaseErr := service.personClient.ReleaseResetSlot(releaseCtx, personHash, slotID); releaseErr != nil {
		service.logger.Error("reset slot release failed", "person_hash", personHash, "error", releaseErr)
	}
}

// ErrPersonStoreUnavailable is returned when a flow needs the person store
// (to record a password stamp) and it is not configured.
var ErrPersonStoreUnavailable = errors.New("person store unavailable")

// recordStampAfterChange records the password stamp for an ntfy user that had
// no person row, after its password was already changed. A failure is logged at
// Error level but never fails the call: the password is already applied.
func (service *Service) recordStampAfterChange(ctx context.Context, personHash string, email string) {
	if _, recordErr := service.personClient.ClaimPassword(ctx, personHash, email, personsvc.ClaimAlways); recordErr != nil {
		service.logger.Error("password stamp record after change failed", "person_hash", personHash, "error", recordErr)
	}
}
