package message

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/envint"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/logthrottle"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personal"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/personhash"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	// maxNtfyTopicLength is ntfy's cap on a topic name.
	maxNtfyTopicLength = 64

	// errMsgDisplayRequired is the validation message for an envelope with no
	// renderable display text bound for an ntfy or discord destination.
	errMsgDisplayRequired = "display.title or display.body is required for ntfy and discord destinations"

	// DefaultListLimit is the page size of an operator delivery list that names
	// no limit.
	DefaultListLimit = 50
	// MaxListLimit is the largest accepted operator delivery list limit. The
	// store has no upper bound of its own, so the service enforces it.
	MaxListLimit = 200

	// DefaultMaxScheduledPerApp is how many scheduled messages one app may hold
	// when DELIVERY_MAX_SCHEDULED_PER_APP is unset. docker-compose.yml and
	// docker-compose.prod.yml repeat it as the pass-through default.
	DefaultMaxScheduledPerApp = 1000
	// MaxScheduledPerAppLimit is the largest accepted DELIVERY_MAX_SCHEDULED_PER_APP.
	MaxScheduledPerAppLimit = 100000
	// MaxScheduledEnvVar names the optional environment variable that overrides
	// the per-app scheduled-message cap.
	MaxScheduledEnvVar = "DELIVERY_MAX_SCHEDULED_PER_APP"

	// DefaultMaxScheduledTotal is how many scheduled messages all apps together
	// may hold when DELIVERY_MAX_SCHEDULED_TOTAL is unset. docker-compose.yml and
	// docker-compose.prod.yml repeat it as the pass-through default.
	DefaultMaxScheduledTotal = 100000
	// MaxScheduledTotalLimit is the largest accepted DELIVERY_MAX_SCHEDULED_TOTAL.
	MaxScheduledTotalLimit = 10000000
	// MaxScheduledTotalEnvVar names the optional environment variable that
	// overrides the service-wide scheduled-message cap.
	MaxScheduledTotalEnvVar = "DELIVERY_MAX_SCHEDULED_TOTAL"

	// HoldUnroutedEnvVar names the environment variable that turns on holding an
	// app message that matches no route (instead of refusing it). Off by default.
	HoldUnroutedEnvVar = "DELIVERY_HOLD_UNROUTED"

	// DefaultMaxHeldPerApp is how many held messages one app may have when
	// DELIVERY_MAX_HELD_PER_APP is unset. docker-compose.yml and
	// docker-compose.prod.yml repeat it as the pass-through default.
	DefaultMaxHeldPerApp = 1000
	// MaxHeldPerAppLimit is the largest accepted DELIVERY_MAX_HELD_PER_APP.
	MaxHeldPerAppLimit = 100000
	// MaxHeldPerAppEnvVar names the optional environment variable that overrides
	// the per-app held-message cap.
	MaxHeldPerAppEnvVar = "DELIVERY_MAX_HELD_PER_APP"

	// DefaultMaxHeldTotal is how many held messages all apps together may have
	// when DELIVERY_MAX_HELD_TOTAL is unset. docker-compose.yml and
	// docker-compose.prod.yml repeat it as the pass-through default.
	DefaultMaxHeldTotal = 100000
	// MaxHeldTotalLimit is the largest accepted DELIVERY_MAX_HELD_TOTAL.
	MaxHeldTotalLimit = 10000000
	// MaxHeldTotalEnvVar names the optional environment variable that overrides
	// the service-wide held-message cap.
	MaxHeldTotalEnvVar = "DELIVERY_MAX_HELD_TOTAL"

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

// Publishers reports whether an app has an ntfy publisher token, which a
// to.recipient delivery needs. *publisher.Service satisfies it.
type Publishers interface {
	HasPublisher(ctx context.Context, appID string) (bool, error)
}

// PersonalTargets lists the personal destinations a to.recipient message also
// fans out to. *personal.Service satisfies it.
type PersonalTargets interface {
	// SelectedTargets returns only the named ids that are the person's, enabled and opted in for appID.
	SelectedTargets(ctx context.Context, appID, personHash string, ids []int64) ([]personal.Target, error)
}

// ServiceConfig configures a Service.
type ServiceConfig struct {
	// PersonalTargets adds a delivery per enabled personal destination the
	// recipient opted in for the sending app. Optional: when nil, a recipient
	// message goes to the ntfy topic only.
	PersonalTargets PersonalTargets
	// Store persists messages. Required.
	Store Store
	// Resolver resolves destinations at submit time. Required.
	Resolver Resolver
	// Publishers gates to.recipient submits on the app having a publisher token.
	// Optional: when nil, every recipient submit is refused with
	// registry.ErrNoPublisher (fail closed).
	Publishers Publishers
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
	// MaxScheduledPerApp caps how many scheduled messages one app may hold; a new
	// scheduled submit over it fails with ErrTooManyScheduled. A value <= 0 takes
	// DefaultMaxScheduledPerApp.
	MaxScheduledPerApp int
	// MaxScheduledTotal caps how many scheduled messages all apps may hold
	// together; a new scheduled submit over it fails with
	// ErrScheduledCapacityReached. A value <= 0 takes DefaultMaxScheduledTotal.
	MaxScheduledTotal int
	// HoldUnrouted makes an app message with no matching route a held message
	// instead of ErrNoDestinationMatched. Off (false) by default.
	HoldUnrouted bool
	// MaxHeldPerApp caps how many held messages one app may have. A value <= 0
	// takes DefaultMaxHeldPerApp.
	MaxHeldPerApp int
	// MaxHeldTotal caps how many held messages all apps may have together. A
	// value <= 0 takes DefaultMaxHeldTotal.
	MaxHeldTotal int
}

// HoldUnroutedFromEnv reads DELIVERY_HOLD_UNROUTED through getenv. It fails
// closed: empty, "0" and "false" mean off, "1" and "true" mean on, and anything
// else is an error. The error text never echoes the value.
func HoldUnroutedFromEnv(getenv func(string) string) (bool, error) {
	switch getenv(HoldUnroutedEnvVar) {
	case "", "0", "false":
		return false, nil
	case "1", "true":
		return true, nil
	default:
		return false, fmt.Errorf("invalid %s", HoldUnroutedEnvVar)
	}
}

// MaxHeldPerAppFromEnv reads DELIVERY_MAX_HELD_PER_APP through getenv. An empty
// value gives DefaultMaxHeldPerApp; anything else must be an integer from 1 to
// MaxHeldPerAppLimit. The error text never echoes the value.
func MaxHeldPerAppFromEnv(getenv func(string) string) (int, error) {
	return envint.Bounded(getenv, MaxHeldPerAppEnvVar, DefaultMaxHeldPerApp, MaxHeldPerAppLimit)
}

// MaxHeldTotalFromEnv reads DELIVERY_MAX_HELD_TOTAL through getenv. An empty
// value gives DefaultMaxHeldTotal; anything else must be an integer from 1 to
// MaxHeldTotalLimit. The error text never echoes the value.
func MaxHeldTotalFromEnv(getenv func(string) string) (int, error) {
	return envint.Bounded(getenv, MaxHeldTotalEnvVar, DefaultMaxHeldTotal, MaxHeldTotalLimit)
}

// MaxScheduledFromEnv reads DELIVERY_MAX_SCHEDULED_PER_APP through getenv. An
// empty value gives DefaultMaxScheduledPerApp; anything else must be an integer
// from 1 to MaxScheduledPerAppLimit. The error text never echoes the value.
func MaxScheduledFromEnv(getenv func(string) string) (int, error) {
	return envint.Bounded(getenv, MaxScheduledEnvVar, DefaultMaxScheduledPerApp, MaxScheduledPerAppLimit)
}

// MaxScheduledTotalFromEnv reads DELIVERY_MAX_SCHEDULED_TOTAL through getenv. An
// empty value gives DefaultMaxScheduledTotal; anything else must be an integer
// from 1 to MaxScheduledTotalLimit. The error text never echoes the value.
func MaxScheduledTotalFromEnv(getenv func(string) string) (int, error) {
	return envint.Bounded(getenv, MaxScheduledTotalEnvVar, DefaultMaxScheduledTotal, MaxScheduledTotalLimit)
}

// Service is the message use-case layer behind the HTTP endpoints: it validates
// an envelope, resolves it against the registry, queues it, and reads status
// back. Reads are scoped to an app: a message is never returned to a caller
// that names a different app_id.
type Service struct {
	store          Store
	resolver       Resolver
	publishers     Publishers
	personals      PersonalTargets
	supportedTypes map[string]bool
	wake           func()
	logger         *slog.Logger
	pollInterval   time.Duration
	now            func() time.Time
	maxScheduled   int
	maxTotal       int
	holdUnrouted   bool
	heldCaps       HeldCaps
	// heldCursor is the keyset cursor ReleaseHeld pages distinct held pairs with;
	// heldCursorMutex guards it.
	heldCursor      HeldPair
	heldCursorMutex sync.Mutex
	// warnThrottle rate-limits ReleaseHeld's warnings per key.
	warnThrottle *logthrottle.Throttle
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
		publishers:     config.Publishers,
		personals:      config.PersonalTargets,
		supportedTypes: make(map[string]bool, len(config.SupportedTypes)),
		wake:           config.Wake,
		logger:         config.Logger,
		pollInterval:   config.PollInterval,
		now:            config.Now,
		maxScheduled:   config.MaxScheduledPerApp,
		maxTotal:       config.MaxScheduledTotal,
		holdUnrouted:   config.HoldUnrouted,
		heldCaps:       HeldCaps{PerApp: config.MaxHeldPerApp, Total: config.MaxHeldTotal},
		warnThrottle:   logthrottle.New(heldReleaseWarnInterval),
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
	if service.maxScheduled <= 0 {
		service.maxScheduled = DefaultMaxScheduledPerApp
	}
	if service.maxTotal <= 0 {
		service.maxTotal = DefaultMaxScheduledTotal
	}
	if service.heldCaps.PerApp <= 0 {
		service.heldCaps.PerApp = DefaultMaxHeldPerApp
	}
	if service.heldCaps.Total <= 0 {
		service.heldCaps.Total = DefaultMaxHeldTotal
	}
	maxAwaits := config.MaxConcurrentAwaits
	if maxAwaits <= 0 {
		maxAwaits = defaultMaxConcurrentAwaits
	}
	service.awaitSlots = make(chan struct{}, maxAwaits)
	return service
}

// Submit validates envelope, resolves its destinations and queues one delivery
// per destination. A deliver_at after the service clock's now holds the message
// (deliveries 'scheduled'); a missing, equal or past one sends it immediately.
//
// The same (app_id, idempotency_key) never creates a second message:
//   - a scheduled message is replaced in place (same id, outcome Replaced) when
//     the submitted content differs from the stored one, re-resolving targets;
//     a failed resolution returns its error and leaves the original untouched;
//   - everything else is a duplicate (outcome Duplicate) that returns the stored
//     message and ignores the new content: a released or cancelled message, an
//     identical retry of a scheduled one, a replace that lost the race to a
//     release or cancel, and a concurrent insert of the same key. A duplicate
//     skips resolution, so it succeeds even if a destination has since been
//     disabled.
//
// A new scheduled message is refused with ErrTooManyScheduled when the app is
// at its cap, or with ErrScheduledCapacityReached when all apps together are at
// the service-wide cap (the app's own cap is reported first when both are full);
// immediate submits and replaces are exempt. Resolution errors are
// returned unchanged (registry.ErrUnknownDestination,
// registry.ErrDestinationDisabled) or as ErrNoDestinationMatched /
// ErrUnsupportedDestinationType. The worker is woken only when a message was
// stored (or released by a replace) with work due now, never for a scheduled one.
func (service *Service) Submit(ctx context.Context, envelope Envelope) (StoredMessage, SubmitOutcome, error) {
	if validateErr := ValidateEnvelope(envelope); validateErr != nil {
		return StoredMessage{}, SubmitOutcome{}, validateErr
	}

	// Only the hash is ever stored: an email recipient is replaced by its hash
	// before it is compared, resolved or stored.
	if envelope.ToRecipient != "" && !personhash.IsHash(envelope.ToRecipient) {
		envelope.ToRecipient = personhash.Hash(envelope.ToRecipient)
	}
	envelope = normaliseRouting(envelope)
	now := service.now()

	existing, findErr := service.store.GetMessageByKey(ctx, envelope.AppID, envelope.IdempotencyKey)
	if findErr == nil {
		stored, outcome, settled, existingErr := service.submitExisting(ctx, envelope, existing, now)
		if existingErr != nil || settled {
			return stored, outcome, existingErr
		}
		// The message vanished between the lookup and the replace (a retention
		// sweep): submit it as a new one.
	} else if !errors.Is(findErr, ErrMessageNotFound) {
		return StoredMessage{}, SubmitOutcome{}, findErr
	}

	targets, resolveErr := service.resolveTargets(ctx, envelope)
	if errors.Is(resolveErr, ErrNoDestinationMatched) && envelope.Hold && service.holdUnrouted &&
		envelope.ToRecipient == "" && envelope.ToDestination == "" {
		// Only the new-message branch holds, and only when the route (not the
		// body) opted in and the flag is on. Recipient mode and a named
		// destination never hold (fail closed), and every other resolver error
		// returns unchanged below.
		return service.holdMessage(ctx, envelope, now)
	}
	if resolveErr != nil {
		return StoredMessage{}, SubmitOutcome{}, resolveErr
	}

	var stored StoredMessage
	var duplicate bool
	var insertErr error
	if isFuture(envelope.DeliverAt, now) {
		stored, duplicate, insertErr = service.store.InsertScheduledMessage(
			ctx, envelope, targets, ScheduledCaps{PerApp: service.maxScheduled, Total: service.maxTotal})
	} else {
		stored, duplicate, insertErr = service.store.InsertMessage(ctx, envelope, targets)
	}
	if insertErr != nil {
		return StoredMessage{}, SubmitOutcome{}, insertErr
	}
	outcome := newSubmitOutcome(stored, duplicate, false)
	if !duplicate {
		service.logger.Info("message accepted",
			"message_id", stored.ID, "app_id", stored.Envelope.AppID,
			"channel", stored.Envelope.Channel, "deliveries", len(stored.Deliveries),
			"scheduled", outcome.Scheduled)
		if !outcome.Scheduled {
			service.wake()
		}
	}
	return stored, outcome, nil
}

// holdMessage stores an unrouted message as held. The worker is never woken: a
// held message has no delivery. Held is true only for a fresh insert.
func (service *Service) holdMessage(
	ctx context.Context, envelope Envelope, now time.Time,
) (StoredMessage, SubmitOutcome, error) {
	stored, duplicate, insertErr := service.store.InsertHeldMessage(ctx, envelope, service.heldCaps, now)
	if insertErr != nil {
		return StoredMessage{}, SubmitOutcome{}, insertErr
	}
	outcome := SubmitOutcome{Duplicate: duplicate, Held: !duplicate}
	if !duplicate {
		service.logger.Info("message held",
			"message_id", stored.ID, "app_id", stored.Envelope.AppID, "channel", stored.Envelope.Channel)
	}
	return stored, outcome, nil
}

// submitExisting handles a submit whose key already exists. settled is false
// only when the message disappeared before it could be replaced, in which case
// the caller submits it as a new message.
func (service *Service) submitExisting(
	ctx context.Context, envelope Envelope, existing StoredMessage, now time.Time,
) (StoredMessage, SubmitOutcome, bool, error) {
	if DeriveStatus(existing.Deliveries, existing.State()) != StatusScheduled || sameContent(existing.Envelope, envelope) {
		return existing, newSubmitOutcome(existing, true, false), true, nil
	}

	targets, resolveErr := service.resolveTargets(ctx, envelope)
	if resolveErr != nil {
		return StoredMessage{}, SubmitOutcome{}, true, resolveErr
	}
	stored, replaced, replaceErr := service.store.ReplaceScheduledMessage(ctx, envelope, targets, now)
	if errors.Is(replaceErr, ErrMessageNotFound) {
		// The row was not there to lock. Another submit may have inserted the key
		// in the meantime; if so it is the stored message.
		current, findErr := service.store.GetMessageByKey(ctx, envelope.AppID, envelope.IdempotencyKey)
		if findErr == nil {
			return current, newSubmitOutcome(current, true, false), true, nil
		}
		if errors.Is(findErr, ErrMessageNotFound) {
			return StoredMessage{}, SubmitOutcome{}, false, nil
		}
		return StoredMessage{}, SubmitOutcome{}, true, findErr
	}
	if replaceErr != nil {
		return StoredMessage{}, SubmitOutcome{}, true, replaceErr
	}
	outcome := newSubmitOutcome(stored, !replaced, replaced)
	if replaced {
		service.logger.Info("message replaced",
			"message_id", stored.ID, "app_id", stored.Envelope.AppID,
			"channel", stored.Envelope.Channel, "deliveries", len(stored.Deliveries),
			"scheduled", outcome.Scheduled)
		if !outcome.Scheduled {
			service.wake()
		}
	}
	return stored, outcome, true, nil
}

// isFuture reports whether deliverAt names an instant strictly after now.
func isFuture(deliverAt *time.Time, now time.Time) bool {
	return deliverAt != nil && deliverAt.After(now)
}

// newSubmitOutcome builds the outcome for stored, deriving Scheduled from its
// deliveries.
func newSubmitOutcome(stored StoredMessage, duplicate bool, replaced bool) SubmitOutcome {
	return SubmitOutcome{
		Duplicate: duplicate,
		Replaced:  replaced,
		Scheduled: DeriveStatus(stored.Deliveries, stored.State()) == StatusScheduled,
	}
}

// resolveTargets resolves envelope's destinations and snapshots them as delivery
// targets. It fails closed: no match, or a destination type without a sink,
// stores nothing.
func (service *Service) resolveTargets(ctx context.Context, envelope Envelope) ([]DeliveryTarget, error) {
	if envelope.ToRecipient != "" {
		return service.resolveRecipientTargets(ctx, envelope)
	}
	resolved, resolveErr := service.resolveRouteDestinations(ctx, envelope)
	if resolveErr != nil {
		return nil, resolveErr
	}
	return service.routeTargets(envelope, resolved)
}

// resolveRouteDestinations is the pair-level half of the route branch: it asks
// the registry for envelope's destinations and returns them in order, with no
// type or display check. An empty result is ErrNoDestinationMatched.
func (service *Service) resolveRouteDestinations(
	ctx context.Context, envelope Envelope,
) ([]registry.ResolvedDestination, error) {
	resolved, resolveErr := service.resolver.Resolve(ctx, envelope.AppID, envelope.ToDestination, envelope.Channel)
	if resolveErr != nil {
		return nil, resolveErr
	}
	if len(resolved) == 0 {
		return nil, ErrNoDestinationMatched
	}
	return resolved, nil
}

// routeTargets is the per-message half of the route branch: for each resolved
// destination, in order, it applies the supported-type check and then the
// display check, and snapshots the destination as a delivery target.
func (service *Service) routeTargets(
	envelope Envelope, resolved []registry.ResolvedDestination,
) ([]DeliveryTarget, error) {
	targets := make([]DeliveryTarget, 0, len(resolved))
	for _, destination := range resolved {
		if !service.supportedTypes[destination.Type] {
			return nil, ErrUnsupportedDestinationType
		}
		humanFacing := destination.Type == registry.TypeNtfy || destination.Type == registry.TypeDiscord
		if humanFacing && !envelope.Display.HasRenderableText() {
			return nil, &ValidationError{Message: errMsgDisplayRequired}
		}
		// Copy the ID so each target points at its own value, not the loop variable.
		destinationID := destination.ID
		targets = append(targets, DeliveryTarget{
			DestinationID:   &destinationID,
			DestinationName: destination.Name,
			DestinationType: destination.Type,
		})
	}
	return targets, nil
}

// resolveRecipientTargets snapshots the delivery targets for a to.recipient
// envelope, whose ToRecipient is already a person hash, according to its
// effective via: ntfy builds only the @recipient target, personal only one
// @personal_<id> target per named destination, both the two together. The set
// is a snapshot taken here, at submit/replace time; it is never recomputed at
// release. An unknown via is denied.
//
// The ntfy leg bypasses the registry (there is no destination row), so it fails
// closed itself: ntfy must have a sink, the per-person topic must fit ntfy's
// 64-character cap, the display must be renderable, and the app must have a
// publisher token (a nil Publishers is that same refusal, not a panic). The
// personal leg needs only a renderable display and every named destination
// deliverable.
func (service *Service) resolveRecipientTargets(ctx context.Context, envelope Envelope) ([]DeliveryTarget, error) {
	switch envelope.ToRecipientVia.OrDefault() {
	case RecipientViaNtfy:
		ntfyTarget, ntfyErr := service.recipientNtfyTarget(ctx, envelope)
		if ntfyErr != nil {
			return nil, ntfyErr
		}
		return []DeliveryTarget{ntfyTarget}, nil
	case RecipientViaPersonal:
		if !envelope.Display.HasRenderableText() {
			return nil, &ValidationError{Message: errMsgDisplayRequired}
		}
		return service.selectedPersonalTargets(ctx, envelope)
	case RecipientViaBoth:
		ntfyTarget, ntfyErr := service.recipientNtfyTarget(ctx, envelope)
		if ntfyErr != nil {
			return nil, ntfyErr
		}
		personalTargets, personalErr := service.selectedPersonalTargets(ctx, envelope)
		if personalErr != nil {
			return nil, personalErr
		}
		return append([]DeliveryTarget{ntfyTarget}, personalTargets...), nil
	default:
		return nil, &ValidationError{Message: "invalid to.via"}
	}
}

// recipientNtfyTarget runs the ntfy-leg checks (sink, topic length, display,
// publisher) and returns the synthetic @recipient target.
func (service *Service) recipientNtfyTarget(ctx context.Context, envelope Envelope) (DeliveryTarget, error) {
	if !service.supportedTypes[registry.TypeNtfy] {
		return DeliveryTarget{}, ErrUnsupportedDestinationType
	}
	topic := personhash.PersonChannelTopic(envelope.AppID, envelope.ToRecipient, envelope.Channel)
	if len(topic) > maxNtfyTopicLength {
		return DeliveryTarget{}, &ValidationError{Message: "app_id and channel are too long for a recipient topic"}
	}
	if !envelope.Display.HasRenderableText() {
		return DeliveryTarget{}, &ValidationError{Message: errMsgDisplayRequired}
	}
	if service.publishers == nil {
		return DeliveryTarget{}, registry.ErrNoPublisher
	}
	hasPublisher, publisherErr := service.publishers.HasPublisher(ctx, envelope.AppID)
	if publisherErr != nil {
		return DeliveryTarget{}, publisherErr
	}
	if !hasPublisher {
		return DeliveryTarget{}, registry.ErrNoPublisher
	}
	return DeliveryTarget{
		DestinationID:   nil,
		DestinationName: registry.RecipientDestinationName,
		DestinationType: registry.TypeNtfy,
	}, nil
}

// selectedPersonalTargets resolves the named personal destinations. Any miss
// (nil service, count mismatch, a type with no sink) is the one generic
// ErrPersonalDestinationUnavailable so a response never confirms that a named
// id exists; a store error propagates unchanged.
func (service *Service) selectedPersonalTargets(ctx context.Context, envelope Envelope) ([]DeliveryTarget, error) {
	if service.personals == nil || len(envelope.ToPersonalDestinationIDs) == 0 {
		return nil, ErrPersonalDestinationUnavailable
	}
	selected, selectErr := service.personals.SelectedTargets(
		ctx, envelope.AppID, envelope.ToRecipient, envelope.ToPersonalDestinationIDs)
	if selectErr != nil {
		return nil, selectErr
	}
	if len(selected) != len(envelope.ToPersonalDestinationIDs) {
		return nil, ErrPersonalDestinationUnavailable
	}
	targets := make([]DeliveryTarget, 0, len(selected))
	for _, personalTarget := range selected {
		if !service.supportedTypes[personalTarget.Type] {
			return nil, ErrPersonalDestinationUnavailable
		}
		targets = append(targets, DeliveryTarget{
			DestinationID:   nil,
			DestinationName: personal.PersonalDeliveryName(personalTarget.ID),
			DestinationType: personalTarget.Type,
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
func (service *Service) ListDeliveries(ctx context.Context, state string, limit int) ([]AdminDelivery, error) {
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
		deliveries = []AdminDelivery{}
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

// Cancel cancels app appID's scheduled message with the given id. It is scoped
// to the app: another app's message, an unknown id and an id that is not a
// canonical UUID all report StatusNotFound with Found false (the id is echoed
// as MessageID), and a non-UUID id never reaches the store. An already
// cancelled message reports Cancelled true; a released one reports Cancelled
// false with its real status. A malformed appID is a *ValidationError.
func (service *Service) Cancel(ctx context.Context, appID string, id string) (CancelOutcome, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return CancelOutcome{}, validateErr
	}
	if !canonicalUUIDPattern.MatchString(id) {
		return notFoundOutcome(id), nil
	}
	outcome, cancelErr := service.store.CancelMessage(ctx, id, appID, service.now())
	return service.finishCancel(outcome, cancelErr, id)
}

// CancelByKey is Cancel for the message app appID submitted with idempotencyKey.
// An unknown key reports StatusNotFound with an empty MessageID. Malformed
// inputs are a *ValidationError and never reach the store.
func (service *Service) CancelByKey(ctx context.Context, appID string, idempotencyKey string) (CancelOutcome, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return CancelOutcome{}, validateErr
	}
	if validateErr := ValidateIdempotencyKey(idempotencyKey); validateErr != nil {
		return CancelOutcome{}, validateErr
	}
	outcome, cancelErr := service.store.CancelMessageByKey(ctx, appID, idempotencyKey, service.now())
	return service.finishCancel(outcome, cancelErr, "")
}

// AdminCancel is Cancel for the operator: it finds the message in any app.
func (service *Service) AdminCancel(ctx context.Context, id string) (CancelOutcome, error) {
	if !canonicalUUIDPattern.MatchString(id) {
		return notFoundOutcome(id), nil
	}
	outcome, cancelErr := service.store.AdminCancelMessage(ctx, id, service.now())
	return service.finishCancel(outcome, cancelErr, id)
}

// finishCancel turns a store result into the service's: a miss becomes the
// not_found outcome echoing requestedID, and a message this call newly
// cancelled is logged (an idempotent repeat is not).
func (service *Service) finishCancel(
	outcome CancelOutcome, cancelErr error, requestedID string,
) (CancelOutcome, error) {
	if cancelErr != nil {
		return CancelOutcome{}, cancelErr
	}
	if !outcome.Found {
		return notFoundOutcome(requestedID), nil
	}
	if outcome.Transitioned {
		service.logger.Info("message cancel", "message_id", outcome.MessageID)
	}
	return outcome, nil
}

// notFoundOutcome is the cancel result for a message that does not exist (or
// that the caller may not see), echoing the requested id.
func notFoundOutcome(requestedID string) CancelOutcome {
	return CancelOutcome{MessageID: requestedID, Status: StatusNotFound}
}

// ListScheduled returns up to limit scheduled messages, soonest deliver_at
// first, and the total number of scheduled messages. limit must be in [1,
// MaxListLimit], else a *ValidationError. The slice is never nil without an
// error.
func (service *Service) ListScheduled(ctx context.Context, limit int) ([]ScheduledMessage, int, error) {
	if limit < 1 || limit > MaxListLimit {
		return nil, 0, &ValidationError{Message: "invalid limit"}
	}
	scheduled, total, listErr := service.store.ListScheduled(ctx, limit)
	if listErr != nil {
		return nil, 0, listErr
	}
	if scheduled == nil {
		scheduled = []ScheduledMessage{}
	}
	return scheduled, total, nil
}

// ListHeldFailures returns up to limit held messages whose release failed
// terminally, newest first, and the uncapped total. limit must be in [1,
// MaxListLimit], else a *ValidationError. The slice is never nil without an
// error.
func (service *Service) ListHeldFailures(ctx context.Context, limit int) ([]HeldFailure, int, error) {
	if limit < 1 || limit > MaxListLimit {
		return nil, 0, &ValidationError{Message: "invalid limit"}
	}
	failures, total, listErr := service.store.ListHeldFailures(ctx, limit)
	if listErr != nil {
		return nil, 0, listErr
	}
	if failures == nil {
		failures = []HeldFailure{}
	}
	return failures, total, nil
}

// Replay returns a dead delivery to the queue with a fresh retry budget and
// wakes the worker. It returns ErrDeliveryNotFound for an unknown delivery and
// ErrDeliveryNotDead unless the delivery is dead.
func (service *Service) Replay(ctx context.Context, deliveryID int64) (AdminDelivery, error) {
	replayed, replayErr := service.store.Replay(ctx, deliveryID, service.now())
	if replayErr != nil {
		return AdminDelivery{}, replayErr
	}
	service.logger.Info("delivery replayed", "delivery_id", replayed.ID, "message_id", replayed.MessageID)
	service.wake()
	return replayed, nil
}
