package message

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	// DefaultListLimit is the page size of an operator delivery list that names
	// no limit.
	DefaultListLimit = 50
	// MaxListLimit is the largest accepted operator delivery list limit. The
	// store has no upper bound of its own, so the service enforces it.
	MaxListLimit = 200

	// defaultPollInterval is how often Await re-reads the message.
	defaultPollInterval = 100 * time.Millisecond
	// defaultMaxConcurrentAwaits bounds the Await calls polling the store at once.
	defaultMaxConcurrentAwaits = 32
)

// Resolver turns an envelope's addressing into the destinations it fans out to.
// *registry.Service satisfies it.
type Resolver interface {
	Resolve(ctx context.Context, appID, destinationName, channel string) ([]registry.ResolvedDestination, error)
}

// ServiceConfig configures a Service.
type ServiceConfig struct {
	// Store persists messages. Required.
	Store Store
	// Resolver resolves destinations at submit time. Required.
	Resolver Resolver
	// SupportedTypes is the set of destination types that have a sink. A resolved
	// destination of any other type is refused (fail closed), so a nil or empty
	// set accepts nothing.
	SupportedTypes map[string]bool
	// Wake nudges the dispatch worker after new work is queued; defaults to a no-op.
	Wake func()
	// Logger receives accepted-message audit lines (identifiers only, never
	// payloads); defaults to slog.Default().
	Logger *slog.Logger
	// PollInterval is how often Await re-reads a message; defaults to 100 ms.
	PollInterval time.Duration
	// Now supplies the replay time; defaults to time.Now.
	Now func() time.Time
	// MaxConcurrentAwaits bounds how many Await calls may poll the store at once;
	// defaults to 32. An Await that finds every slot taken returns its first read
	// without polling.
	MaxConcurrentAwaits int
}

// Service is the message use-case layer behind the HTTP endpoints: it validates
// an envelope, resolves it against the registry, queues it, and reads status
// back. Reads are scoped to an app: a message is never returned to a caller
// that names a different app_id.
type Service struct {
	store          Store
	resolver       Resolver
	supportedTypes map[string]bool
	wake           func()
	logger         *slog.Logger
	pollInterval   time.Duration
	now            func() time.Time
	// awaitSlots is a counting semaphore bounding concurrent Await pollers.
	awaitSlots chan struct{}
}

// NewService builds a Service from config, applying the documented defaults.
// The supported-type set is copied, so later changes to the caller's map have
// no effect. It panics when Store or Resolver is nil: a service without them
// can only fail later, at request time, so this fails fast at construction.
func NewService(config ServiceConfig) *Service {
	if config.Store == nil {
		panic("message.NewService: Store is required")
	}
	if config.Resolver == nil {
		panic("message.NewService: Resolver is required")
	}
	service := &Service{
		store:          config.Store,
		resolver:       config.Resolver,
		supportedTypes: make(map[string]bool, len(config.SupportedTypes)),
		wake:           config.Wake,
		logger:         config.Logger,
		pollInterval:   config.PollInterval,
		now:            config.Now,
	}
	for destinationType, supported := range config.SupportedTypes {
		service.supportedTypes[destinationType] = supported
	}
	if service.wake == nil {
		service.wake = func() {}
	}
	if service.logger == nil {
		service.logger = slog.Default()
	}
	if service.pollInterval <= 0 {
		service.pollInterval = defaultPollInterval
	}
	if service.now == nil {
		service.now = time.Now
	}
	maxAwaits := config.MaxConcurrentAwaits
	if maxAwaits <= 0 {
		maxAwaits = defaultMaxConcurrentAwaits
	}
	service.awaitSlots = make(chan struct{}, maxAwaits)
	return service
}

// Submit validates envelope, resolves its destinations and queues one delivery
// per destination. The bool reports a duplicate: the same (app_id,
// idempotency_key) returns the original message and ignores the new content. A
// duplicate skips resolution entirely, so it succeeds even if the destination
// has since been disabled. Resolution errors are returned unchanged
// (registry.ErrUnknownDestination, registry.ErrDestinationDisabled) or as
// ErrNoDestinationMatched / ErrUnsupportedDestinationType. The worker is woken
// only when a new message was stored.
func (service *Service) Submit(ctx context.Context, envelope Envelope) (StoredMessage, bool, error) {
	if validateErr := ValidateEnvelope(envelope); validateErr != nil {
		return StoredMessage{}, false, validateErr
	}

	existing, findErr := service.store.GetMessageByKey(ctx, envelope.AppID, envelope.IdempotencyKey)
	if findErr == nil {
		return existing, true, nil
	}
	if !errors.Is(findErr, ErrMessageNotFound) {
		return StoredMessage{}, false, findErr
	}

	targets, resolveErr := service.resolveTargets(ctx, envelope)
	if resolveErr != nil {
		return StoredMessage{}, false, resolveErr
	}

	stored, duplicate, insertErr := service.store.InsertMessage(ctx, envelope, targets)
	if insertErr != nil {
		return StoredMessage{}, false, insertErr
	}
	if !duplicate {
		service.logger.Info("message accepted",
			"message_id", stored.ID, "app_id", stored.Envelope.AppID,
			"channel", stored.Envelope.Channel, "deliveries", len(stored.Deliveries))
		service.wake()
	}
	return stored, duplicate, nil
}

// resolveTargets resolves envelope's destinations and snapshots them as delivery
// targets. It fails closed: no match, or a destination type without a sink,
// stores nothing.
func (service *Service) resolveTargets(ctx context.Context, envelope Envelope) ([]DeliveryTarget, error) {
	resolved, resolveErr := service.resolver.Resolve(ctx, envelope.AppID, envelope.ToDestination, envelope.Channel)
	if resolveErr != nil {
		return nil, resolveErr
	}
	if len(resolved) == 0 {
		return nil, ErrNoDestinationMatched
	}
	targets := make([]DeliveryTarget, 0, len(resolved))
	for _, destination := range resolved {
		if !service.supportedTypes[destination.Type] {
			return nil, ErrUnsupportedDestinationType
		}
		targets = append(targets, DeliveryTarget{
			DestinationID:   destination.ID,
			DestinationName: destination.Name,
			DestinationType: destination.Type,
		})
	}
	return targets, nil
}

// Get returns the message with the given id, scoped to appID: a message that
// belongs to another app is reported as ErrMessageNotFound, exactly like an
// absent one, so one app can neither read nor probe for another's messages. A
// malformed appID is a *ValidationError.
func (service *Service) Get(ctx context.Context, appID string, id string) (StoredMessage, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return StoredMessage{}, validateErr
	}
	stored, getErr := service.store.GetMessage(ctx, id)
	if getErr != nil {
		return StoredMessage{}, getErr
	}
	if stored.Envelope.AppID != appID {
		return StoredMessage{}, ErrMessageNotFound
	}
	return stored, nil
}

// GetByKey returns the message app appID submitted with idempotencyKey, or
// ErrMessageNotFound. Malformed inputs are a *ValidationError and never reach
// the store.
func (service *Service) GetByKey(ctx context.Context, appID string, idempotencyKey string) (StoredMessage, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return StoredMessage{}, validateErr
	}
	if validateErr := ValidateIdempotencyKey(idempotencyKey); validateErr != nil {
		return StoredMessage{}, validateErr
	}
	return service.store.GetMessageByKey(ctx, appID, idempotencyKey)
}

// Await polls the message (scoped to appID, as in Get) every PollInterval until
// every delivery has been attempted at least once or has left the pending
// state, or until timeout passes (it then reads once more, so the result is
// never staler than the deadline), and returns the last read. A failed read, or
// a cancelled ctx, returns the last successful read together with the error,
// so a caller that already holds a committed message can still answer with it.
// A non-positive timeout returns after the first read. At most
// MaxConcurrentAwaits calls poll at once: when every slot is taken, Await
// returns its first read immediately (nil error) rather than adding store load.
func (service *Service) Await(
	ctx context.Context, appID string, id string, timeout time.Duration,
) (StoredMessage, error) {
	latest, getErr := service.Get(ctx, appID, id)
	if getErr != nil {
		return StoredMessage{}, getErr
	}
	if timeout <= 0 || attempted(latest) {
		return latest, nil
	}

	select {
	case service.awaitSlots <- struct{}{}:
		defer func() { <-service.awaitSlots }()
	default:
		return latest, nil
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(service.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return latest, ctx.Err()
		case <-deadline.C:
			final, finalErr := service.Get(ctx, appID, id)
			if finalErr != nil {
				return latest, finalErr
			}
			return final, nil
		case <-ticker.C:
			next, nextErr := service.Get(ctx, appID, id)
			if nextErr != nil {
				return latest, nextErr
			}
			latest = next
			if attempted(latest) {
				return latest, nil
			}
		}
	}
}

// attempted reports whether every delivery has been tried at least once or is no
// longer pending.
func attempted(stored StoredMessage) bool {
	for _, delivery := range stored.Deliveries {
		if delivery.State == StatePending && delivery.AttemptCount < 1 {
			return false
		}
	}
	return true
}

// ListDeliveries returns up to limit deliveries in state, newest-updated first.
// state must be one of the State* values and limit must be in [1, MaxListLimit];
// anything else is a *ValidationError. The result is never nil without an error.
func (service *Service) ListDeliveries(ctx context.Context, state string, limit int) ([]Delivery, error) {
	if !isDeliveryState(state) {
		return nil, &ValidationError{Message: "invalid state"}
	}
	if limit < 1 || limit > MaxListLimit {
		return nil, &ValidationError{Message: "invalid limit"}
	}
	deliveries, listErr := service.store.ListDeliveries(ctx, state, limit)
	if listErr != nil {
		return nil, listErr
	}
	if deliveries == nil {
		deliveries = []Delivery{}
	}
	return deliveries, nil
}

// ListAttempts returns a delivery's attempts in attempt order, or
// ErrDeliveryNotFound.
func (service *Service) ListAttempts(ctx context.Context, deliveryID int64) ([]Attempt, error) {
	attempts, listErr := service.store.ListAttempts(ctx, deliveryID)
	if listErr != nil {
		return nil, listErr
	}
	if attempts == nil {
		attempts = []Attempt{}
	}
	return attempts, nil
}

// Replay returns a dead delivery to the queue with a fresh retry budget and
// wakes the worker. It returns ErrDeliveryNotFound for an unknown delivery and
// ErrDeliveryNotDead unless the delivery is dead.
func (service *Service) Replay(ctx context.Context, deliveryID int64) (Delivery, error) {
	replayed, replayErr := service.store.Replay(ctx, deliveryID, service.now())
	if replayErr != nil {
		return Delivery{}, replayErr
	}
	service.logger.Info("delivery replayed", "delivery_id", replayed.ID, "message_id", replayed.MessageID)
	service.wake()
	return replayed, nil
}
