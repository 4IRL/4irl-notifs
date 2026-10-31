package publisher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/appid"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

// tokenAADPrefix binds a sealed token to its app: a blob sealed for one app
// cannot be opened as another's.
const tokenAADPrefix = "delivery-api/app-publisher/v1:"

// Service mints, stores and reads per-app ntfy publisher tokens.
type Service struct {
	store  Store
	minter Minter
	sealer *seal.Sealer
	logger *slog.Logger

	// mintSlots is the process-wide in-flight mint semaphore (capacity MaxInFlightMints). A nil
	// channel (Service not built by NewService) refuses every mint.
	mintSlots chan struct{}
	// persistTimeout bounds grant check, seal and upsert after the upstream mint; tests override it.
	persistTimeout time.Duration
}

// NewService builds a Service. minter may be nil, in which case Mint returns
// ErrMintUnavailable. A nil logger uses slog.Default().
func NewService(store Store, minter Minter, sealer *seal.Sealer, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store: store, minter: minter, sealer: sealer, logger: logger,
		mintSlots: make(chan struct{}, MaxInFlightMints), persistTimeout: MintPersistTimeout,
	}
}

// logRotateNotStored records that a rotate=true mint failed after the minter
// was invoked: provisioning-api revokes old tokens before minting, so the
// stored token may already be dead. The token is never logged.
func (service *Service) logRotateNotStored(ctx context.Context, appID, stage string, cause error) {
	service.logger.ErrorContext(ctx,
		"publisher rotate attempted but new token not stored; recipient delivery may fail until re-mint",
		"app_id", appID, "stage", stage, "error", cause)
}

// TokenAAD returns the additional data a token is sealed under. The key
// rotation tool shares this derivation.
func TokenAAD(appID string) []byte {
	return []byte(tokenAADPrefix + appID)
}

// Mint asks the minter for a new publisher token for appID, seals it and
// upserts it, replacing any previous token. It returns *ValidationError for an
// invalid app id (before the minter is called) and ErrMintUnavailable when no
// minter is configured. A minter error is returned and nothing is stored.
//
// rotate is forwarded to the minter. When rotate is true and anything fails
// after the minter was invoked, a distinct ERROR is logged (see
// logRotateNotStored).
//
// This is the app path: it is always rate limited (MintRateLimit per MintRateWindow per app,
// *RateLimitedError). Overlapping mints for one app return ErrMintBusy. At most MaxInFlightMints
// mints run at once; over the cap, and for a lock/transaction failure or a failure to store a token
// that was already minted upstream, the error satisfies errors.Is(err, ErrMintUnavailable). Only a
// minter error or a grant for another app is a plain error.
func (service *Service) Mint(ctx context.Context, appID string, rotate bool) (PublisherView, error) {
	return service.mint(ctx, appID, rotate, true)
}

// MintAsOperator is Mint for the trusted operator route: it skips the rate limit (no slot is
// recorded, never *RateLimitedError) but takes the same per-app lock and in-flight cap.
func (service *Service) MintAsOperator(ctx context.Context, appID string, rotate bool) (PublisherView, error) {
	return service.mint(ctx, appID, rotate, false)
}

// tryAcquireMintSlot takes one in-flight mint slot without blocking. A nil channel never succeeds
// (fails closed).
func (service *Service) tryAcquireMintSlot() (release func(), ok bool) {
	select {
	case service.mintSlots <- struct{}{}:
		return func() { <-service.mintSlots }, true
	default:
		return nil, false
	}
}

// mint is the single mint path. Order: validate, nil-minter, in-flight cap, transaction + try-lock
// (busy / unavailable), rate-limit slot (app path only), minter, grant check, seal, upsert, commit.
func (service *Service) mint(ctx context.Context, appID string, rotate, limited bool) (PublisherView, error) {
	if validateErr := appid.ValidateAppID(appID); validateErr != nil {
		return PublisherView{}, &ValidationError{Message: validateErr.Error()}
	}
	if service.minter == nil {
		return PublisherView{}, ErrMintUnavailable
	}
	release, acquired := service.tryAcquireMintSlot()
	if !acquired {
		service.logger.WarnContext(ctx, "publisher mint refused", "app_id", appID, "reason", "in-flight cap")
		return PublisherView{}, ErrMintUnavailable
	}
	defer release()

	var view PublisherView
	fnRan, fnOK := false, false
	storeErr := service.store.WithAppMintLock(ctx, appID, func(tx MintTx) error {
		fnRan = true
		mintedView, mintErr := service.mintLocked(ctx, tx, appID, rotate, limited)
		if mintErr != nil {
			return mintErr
		}
		view = mintedView
		fnOK = true
		return nil
	})
	if storeErr == nil {
		return view, nil
	}
	switch {
	case fnRan && fnOK:
		// The token was minted and stored in the transaction, but the commit failed.
		cause := fmt.Errorf("%w: commit: %w", ErrMintUnavailable, storeErr)
		if rotate {
			service.logRotateNotStored(ctx, appID, "commit", storeErr)
		} else {
			service.logger.ErrorContext(ctx, "publisher mint commit failed", "app_id", appID, "error", storeErr)
		}
		return PublisherView{}, cause
	case fnRan:
		return PublisherView{}, storeErr
	case errors.Is(storeErr, ErrMintBusy):
		service.logger.InfoContext(ctx, "publisher mint refused", "app_id", appID, "reason", "mint already in progress")
	default:
		service.logger.ErrorContext(ctx, "publisher mint transaction unavailable", "app_id", appID, "error", storeErr)
	}
	return PublisherView{}, storeErr
}

// mintLocked runs inside the per-app lock transaction.
func (service *Service) mintLocked(
	ctx context.Context, tx MintTx, appID string, rotate, limited bool,
) (PublisherView, error) {
	if limited {
		if reserveErr := tx.ReserveMintSlot(ctx, appID, MintRateLimit, MintRateWindow); reserveErr != nil {
			var rateLimited *RateLimitedError
			if errors.As(reserveErr, &rateLimited) {
				service.logger.InfoContext(ctx, "publisher mint rate limited",
					"app_id", appID, "retry_after", rateLimited.RetryAfter.String())
				return PublisherView{}, reserveErr
			}
			service.logger.ErrorContext(ctx, "publisher mint rate-limit store failed", "app_id", appID, "error", reserveErr)
			return PublisherView{}, fmt.Errorf("%w: rate limit: %w", ErrMintUnavailable, reserveErr)
		}
	}
	fail := func(stage string, cause error) error {
		if rotate {
			service.logRotateNotStored(ctx, appID, stage, cause)
		}
		return cause
	}
	// A client disconnect must not cancel an in-flight mint: a rotate=true mint revokes the old
	// tokens upstream first, so abandoning it would leave nothing stored.
	grant, mintErr := service.minter.MintPublisher(context.WithoutCancel(ctx), appID, rotate)
	if mintErr != nil {
		return PublisherView{}, fail("minter", fmt.Errorf("mint publisher: %w", mintErr))
	}
	if grant.AppID != appID {
		return PublisherView{}, fail("app_mismatch", errors.New("mint publisher: grant is for a different app"))
	}
	persistCtx, cancelPersist := context.WithTimeout(context.WithoutCancel(ctx), service.persistTimeout)
	defer cancelPersist()
	sealedToken, sealErr := service.sealer.Seal([]byte(grant.Token), TokenAAD(appID))
	if sealErr != nil {
		return PublisherView{}, fail("seal", fmt.Errorf("%w: seal publisher token: %w", ErrMintUnavailable, sealErr))
	}
	stored, upsertErr := tx.Upsert(persistCtx, StoredPublisher{
		AppID:           appID,
		PublisherUserID: grant.PublisherUserID,
		TopicPattern:    grant.TopicPattern,
		SealedToken:     sealedToken,
	})
	if upsertErr != nil {
		return PublisherView{}, fail("upsert", fmt.Errorf("%w: upsert: %w", ErrMintUnavailable, upsertErr))
	}
	return viewOf(stored), nil
}

// Get returns the app's publisher record, or registry.ErrNoPublisher.
func (service *Service) Get(ctx context.Context, appID string) (PublisherView, error) {
	stored, getErr := service.store.Get(ctx, appID)
	if getErr != nil {
		return PublisherView{}, getErr
	}
	return viewOf(stored), nil
}

// Remove deletes the app's stored publisher so recipient submits for the app
// fail closed with registry.ErrNoPublisher until it is minted again. It is
// idempotent: an app with no publisher is not an error. It returns
// *ValidationError for an invalid app id (before the store is touched). It does
// not revoke the token in ntfy; that is a provisioning-api operator step.
func (service *Service) Remove(ctx context.Context, appID string) error {
	if validateErr := appid.ValidateAppID(appID); validateErr != nil {
		return &ValidationError{Message: validateErr.Error()}
	}
	return service.store.Delete(ctx, appID)
}

// HasPublisher reports whether the app has a publisher. Only a store failure is
// an error; a missing publisher is false, nil.
func (service *Service) HasPublisher(ctx context.Context, appID string) (bool, error) {
	if _, getErr := service.store.Get(ctx, appID); getErr != nil {
		if errors.Is(getErr, registry.ErrNoPublisher) {
			return false, nil
		}
		return false, getErr
	}
	return true, nil
}

// Token returns the app's plaintext ntfy publisher token, or
// registry.ErrNoPublisher. A blob that fails to open (wrong key, wrong app,
// corruption) is an error that never contains the token.
func (service *Service) Token(ctx context.Context, appID string) (string, error) {
	stored, getErr := service.store.Get(ctx, appID)
	if getErr != nil {
		return "", getErr
	}
	token, openErr := service.sealer.Open(stored.SealedToken, TokenAAD(appID))
	if openErr != nil {
		return "", fmt.Errorf("open publisher token: %w", openErr)
	}
	return string(token), nil
}

// ResolveRecipient builds the ntfy destination for a to.recipient delivery: the
// person's topic for the app and channel, authenticated with the app's
// publisher token. The result is synthetic (ID 0, name
// registry.RecipientDestinationName) and never persisted. A personHash that is
// not a person hash fails closed with an error before the store is read; an app
// with no publisher returns registry.ErrNoPublisher.
func (service *Service) ResolveRecipient(
	ctx context.Context, appID, personHash, channel string,
) (registry.ResolvedDestination, error) {
	if !personhash.IsHash(personHash) {
		return registry.ResolvedDestination{}, errors.New("recipient is not a person hash")
	}
	token, tokenErr := service.Token(ctx, appID)
	if tokenErr != nil {
		return registry.ResolvedDestination{}, tokenErr
	}
	return registry.ResolvedDestination{
		ID:      0,
		Name:    registry.RecipientDestinationName,
		Type:    registry.TypeNtfy,
		Config:  map[string]string{"topic": personhash.PersonChannelTopic(appID, personHash, channel)},
		Secrets: map[string]string{"access_token": token},
	}, nil
}

func viewOf(stored StoredPublisher) PublisherView {
	return PublisherView{
		AppID:           stored.AppID,
		PublisherUserID: stored.PublisherUserID,
		TopicPattern:    stored.TopicPattern,
		CreatedAt:       stored.CreatedAt,
		UpdatedAt:       stored.UpdatedAt,
	}
}
