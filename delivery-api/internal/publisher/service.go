package publisher

import (
	"context"
	"errors"
	"fmt"

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
}

// NewService builds a Service. minter may be nil, in which case Mint returns
// ErrMintUnavailable.
func NewService(store Store, minter Minter, sealer *seal.Sealer) *Service {
	return &Service{store: store, minter: minter, sealer: sealer}
}

// tokenAAD returns the additional data a token is sealed under.
func tokenAAD(appID string) []byte {
	return []byte(tokenAADPrefix + appID)
}

// Mint asks the minter for a new publisher token for appID, seals it and
// upserts it, replacing any previous token. It returns *ValidationError for an
// invalid app id (before the minter is called) and ErrMintUnavailable when no
// minter is configured. A minter error is returned and nothing is stored.
func (service *Service) Mint(ctx context.Context, appID string) (PublisherView, error) {
	if !appIDPattern.MatchString(appID) {
		return PublisherView{}, &ValidationError{Message: "invalid app_id"}
	}
	if service.minter == nil {
		return PublisherView{}, ErrMintUnavailable
	}
	grant, mintErr := service.minter.MintPublisher(ctx, appID)
	if mintErr != nil {
		return PublisherView{}, fmt.Errorf("mint publisher: %w", mintErr)
	}
	if grant.AppID != appID {
		return PublisherView{}, errors.New("mint publisher: grant is for a different app")
	}
	sealedToken, sealErr := service.sealer.Seal([]byte(grant.Token), tokenAAD(appID))
	if sealErr != nil {
		return PublisherView{}, fmt.Errorf("seal publisher token: %w", sealErr)
	}
	stored, upsertErr := service.store.Upsert(ctx, StoredPublisher{
		AppID:           appID,
		PublisherUserID: grant.PublisherUserID,
		TopicPattern:    grant.TopicPattern,
		SealedToken:     sealedToken,
	})
	if upsertErr != nil {
		return PublisherView{}, upsertErr
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
	token, openErr := service.sealer.Open(stored.SealedToken, tokenAAD(appID))
	if openErr != nil {
		return "", fmt.Errorf("open publisher token: %w", openErr)
	}
	return string(token), nil
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
