package dispatch

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net/url"
	"sync"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/sink"
)

// Worker defaults, applied by NewWorker for every zero WorkerConfig field.
const (
	// DefaultConcurrency is the number of claim loops Run starts. It equals
	// pgxpool's default MaxConns on the 2-vCPU VPS; that is accepted because
	// ClaimDue and RecordAttempt are short transactions that commit before any
	// network I/O, so four workers each briefly holding one connection should
	// not starve the HTTP handlers. If /readyz flaps or HTTP latency spikes,
	// raise the pool's MaxConns above Concurrency (Concurrency+4) or lower
	// Concurrency.
	DefaultConcurrency = 4
	// DefaultPollInterval is how long an idle loop waits before asking the
	// queue again.
	DefaultPollInterval = time.Second
	// DefaultLease is how long a claimed delivery stays invisible to other
	// workers; a worker that dies mid-attempt leaves the row claimable again
	// once it expires. Invariant: the lease must outlast one whole attempt
	// (AttemptTimeout, the record and disable timeouts) plus leaseMargin, or
	// another worker could claim and resend a delivery that is still in flight.
	// NewWorker raises a smaller configured Lease to that floor.
	DefaultLease = 2 * time.Minute
	// DefaultAttemptTimeout bounds one sink call. It matches the transport's
	// overall timeout. The shutdown grace must cover MaxInFlightShutdown, not
	// just this value.
	DefaultAttemptTimeout = 9 * time.Second
	// DefaultRecordRetryDelay is the pause between RecordAttempt retries.
	DefaultRecordRetryDelay = 250 * time.Millisecond
	// MaxInFlightShutdown is the worst-case time an attempt already in flight
	// can take to finish after Run's context is canceled: a full sink call,
	// then the attempt record with its retries, then the destination disable.
	// The process shutdown grace in main must be at least this long (with the
	// default AttemptTimeout) or the worker can be killed mid-record.
	MaxInFlightShutdown = DefaultAttemptTimeout + recordTimeout + disableTimeout
)

const (
	// recordAttempts is how many times a finished attempt is written before its
	// outcome is given up on.
	recordAttempts = 3
	// recordTimeout bounds all RecordAttempt retries after a send or a
	// never-sent dead-letter. It is separate from AttemptTimeout, which a slow
	// sink may have fully used.
	recordTimeout = 5 * time.Second
	// disableTimeout bounds DisableDestination after a 410. It has its own
	// context so a slow disable can never starve the record, which is written
	// first.
	disableTimeout = 2 * time.Second
	// leaseMargin is the slack the lease must keep over a worst-case attempt
	// (see DefaultLease).
	leaseMargin = 30 * time.Second
	// maxClaimBackoff is the longest pause between ClaimDue retries while the
	// queue keeps failing.
	maxClaimBackoff = 30 * time.Second
	// maxClaimBackoffShift caps the doubling of the claim retry pause
	// (PollInterval << 5 is 32x).
	maxClaimBackoffShift = 5
)

// Fixed, secret-free error text stored in the delivery log for attempts that
// never reached a sink.
const (
	errorTextDestinationDeleted  = "destination no longer exists"
	errorTextDestinationDisabled = "destination is disabled"
	errorTextUnsupportedType     = "no sink for destination type"
	errorTextUnresolvable        = "destination could not be resolved"
	errorTextNoPublisher         = "app has no ntfy publisher"
	errorTextSinkPanic           = "sink panicked"
)

// disableReason is stored as the destination's disabled_reason after the
// destination reports itself gone: a 410, or a sink-asserted Gone such as a
// deleted Discord webhook.
const disableReason = "destination reported gone (410 or equivalent)"

var (
	// errSinkPanic is the outcome error recorded when a sink panics. The panic
	// value is dropped because it can quote a URL, a secret or payload content.
	errSinkPanic = errors.New(errorTextSinkPanic)
	// errUnexpectedResolution reports a named resolve that did not return
	// exactly one destination.
	errUnexpectedResolution = errors.New("resolve returned an unexpected number of destinations")
)

// Queue is the slice of the message store the worker needs. It is satisfied by
// *message.PGStore.
type Queue interface {
	ClaimDue(ctx context.Context, now time.Time, lease time.Duration) (message.Claim, bool, error)
	RecordAttempt(ctx context.Context, result message.AttemptResult) (message.Delivery, error)
}

// Resolver is the slice of the registry the worker needs. It is satisfied by
// *registry.Service.
type Resolver interface {
	Resolve(ctx context.Context, appID, destinationName, channel string) ([]registry.ResolvedDestination, error)
	// DisableDestination disables the named destination only while its ID is
	// still destinationID; otherwise it returns registry.ErrDestinationNotFound
	// and changes nothing.
	DisableDestination(ctx context.Context, name string, destinationID int64, reason string) error
}

// RecipientResolver builds the synthetic ntfy destination of a to.recipient
// delivery. It is satisfied by *publisher.Service; dispatch does not import
// publisher. It returns registry.ErrNoPublisher when the app has no publisher.
type RecipientResolver interface {
	ResolveRecipient(ctx context.Context, appID, personHash, channel string) (registry.ResolvedDestination, error)
}

// WorkerConfig configures a Worker. Queue, Resolver and Sinks are required;
// Sinks is keyed by destination type. Every other zero field takes its default
// (see the Default* constants; Now is time.Now and Random is rand.Float64).
type WorkerConfig struct {
	Queue    Queue
	Resolver Resolver
	// Recipients resolves to.recipient deliveries. It is optional: when nil
	// (ntfy is not configured), a recipient delivery is dead-lettered with
	// message.DeadReasonNoPublisher instead of being sent.
	Recipients RecipientResolver
	Sinks      map[string]sink.Sink
	Logger     *slog.Logger

	Concurrency    int
	PollInterval   time.Duration
	Lease          time.Duration
	AttemptTimeout time.Duration
	// RecordRetryDelay is the pause between RecordAttempt retries after a send.
	RecordRetryDelay time.Duration

	// Now supplies every timestamp, so tests can run on a fake clock.
	Now func() time.Time
	// Random is the jitter source for backoff; it must return a value in
	// [0, 1).
	Random func() float64
}

// Worker claims due deliveries and sends them through their sinks.
type Worker struct {
	config WorkerConfig
	wake   chan struct{}
}

// NewWorker returns a Worker with config's zero fields defaulted.
func NewWorker(config WorkerConfig) *Worker {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Concurrency < 1 {
		config.Concurrency = DefaultConcurrency
	}
	if config.PollInterval <= 0 {
		config.PollInterval = DefaultPollInterval
	}
	if config.Lease <= 0 {
		config.Lease = DefaultLease
	}
	if config.AttemptTimeout <= 0 {
		config.AttemptTimeout = DefaultAttemptTimeout
	}
	if config.RecordRetryDelay <= 0 {
		config.RecordRetryDelay = DefaultRecordRetryDelay
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Random == nil {
		config.Random = rand.Float64
	}
	// A lease shorter than one worst-case attempt would let another worker
	// claim and resend a delivery that is still in flight, so it is raised to
	// the floor rather than trusted.
	leaseFloor := config.AttemptTimeout + recordTimeout + disableTimeout + leaseMargin
	if config.Lease < leaseFloor {
		config.Logger.Warn("lease is shorter than one worst-case attempt plus margin; raising it",
			"configured_lease", config.Lease, "effective_lease", leaseFloor)
		config.Lease = leaseFloor
	}
	return &Worker{config: config, wake: make(chan struct{}, 1)}
}

// Wake nudges an idle loop to check the queue now instead of at the next poll.
// It never blocks, and several calls before a loop wakes coalesce into one.
func (worker *Worker) Wake() {
	select {
	case worker.wake <- struct{}{}:
	default:
	}
}

// Run starts Concurrency claim loops and returns once ctx is done and every
// loop has exited. An attempt that is already in flight when ctx is canceled is
// allowed to finish and be recorded, bounded by AttemptTimeout plus the record
// timeout plus the disable timeout (MaxInFlightShutdown, 16 s with the default
// AttemptTimeout); the process shutdown grace must be at least that long. No
// new delivery is claimed after cancellation.
func (worker *Worker) Run(ctx context.Context) {
	var loops sync.WaitGroup
	for range worker.config.Concurrency {
		loops.Add(1)
		go func() {
			defer loops.Done()
			worker.loop(ctx)
		}()
	}
	loops.Wait()
}

// loop claims and processes deliveries until ctx is done. While ClaimDue keeps
// failing it backs off (see claimBackoff) and logs only the first failure of a
// streak at Error, so an outage is neither hammered nor flooding the log; any
// successful ClaimDue ends the streak.
func (worker *Worker) loop(ctx context.Context) {
	consecutiveFailures := 0
	for ctx.Err() == nil {
		claim, found, claimErr := worker.config.Queue.ClaimDue(ctx, worker.config.Now(), worker.config.Lease)
		if claimErr != nil {
			if ctx.Err() != nil {
				return
			}
			consecutiveFailures++
			level := slog.LevelDebug
			if consecutiveFailures == 1 {
				level = slog.LevelError
			}
			worker.config.Logger.Log(ctx, level, "claim delivery failed",
				"error", claimErr, "consecutive_failures", consecutiveFailures)
			worker.idle(ctx, claimBackoff(worker.config.PollInterval, consecutiveFailures))
			continue
		}
		consecutiveFailures = 0
		if !found {
			worker.idle(ctx, worker.config.PollInterval)
			continue
		}
		// Pass the baton: more work may be due, and the other loops may be
		// asleep until the next poll.
		worker.Wake()
		worker.processGuarded(ctx, claim)
	}
}

// claimBackoff returns how long to wait after the failures-th consecutive
// ClaimDue failure (1-based): pollInterval doubled per failure after the first,
// at most 32x, capped at maxClaimBackoff (or at pollInterval itself when that
// is already longer, so a failure never polls faster than an empty queue).
func claimBackoff(pollInterval time.Duration, failures int) time.Duration {
	shift := min(max(failures-1, 0), maxClaimBackoffShift)
	ceiling := max(maxClaimBackoff, pollInterval)
	// Compare against the shifted-down ceiling so the doubling below cannot
	// overflow.
	if pollInterval > ceiling>>shift {
		return ceiling
	}
	return pollInterval << shift
}

// idle waits for the given duration, a Wake, or ctx.
func (worker *Worker) idle(ctx context.Context, wait time.Duration) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-worker.wake:
	case <-timer.C:
	}
}

// processGuarded runs process and keeps a panic anywhere in it from killing the
// loop. The panic value is dropped (it can quote a secret); the delivery's
// lease expires and the row is retried.
func (worker *Worker) processGuarded(ctx context.Context, claim message.Claim) {
	defer func() {
		if recover() != nil {
			worker.config.Logger.Error("delivery attempt panicked; the lease will expire",
				"message_id", claim.Delivery.MessageID, "delivery_id", claim.Delivery.ID)
		}
	}()
	worker.process(ctx, claim)
}

// process resolves the claim's destination, sends the message and records the
// outcome.
func (worker *Worker) process(ctx context.Context, claim message.Claim) {
	// Detach from ctx's cancellation so an attempt in flight finishes during
	// shutdown, but keep a deadline.
	attemptCtx, cancelAttempt := context.WithTimeout(context.WithoutCancel(ctx), worker.config.AttemptTimeout)
	defer cancelAttempt()

	destination, deadReason, resolveErr := worker.resolve(attemptCtx, claim)
	if resolveErr != nil {
		// A zero BudgetStartedAt counts as ancient (fail closed), matching
		// Decide. Without this bound a delivery whose destination can never be
		// resolved would be reclaimed every lease forever, its retry budget
		// never charged.
		budgetStart := claim.Delivery.BudgetStartedAt
		if budgetStart.IsZero() || worker.config.Now().Sub(budgetStart) >= MaxBudgetAge {
			worker.config.Logger.Error("resolve destination failed and the retry budget is spent; dead-lettering",
				"message_id", claim.Delivery.MessageID, "delivery_id", claim.Delivery.ID,
				"destination", claim.Delivery.DestinationName, "error", resolveErr)
			worker.recordNeverSent(ctx, claim, message.DeadReasonBudgetExhausted)
			return
		}
		// Unknown state: write nothing and let the lease expire so the delivery
		// is claimed again later.
		worker.config.Logger.Error("resolve destination failed; the lease will expire",
			"message_id", claim.Delivery.MessageID, "delivery_id", claim.Delivery.ID,
			"destination", claim.Delivery.DestinationName, "error", resolveErr)
		return
	}
	if deadReason != "" {
		worker.recordNeverSent(ctx, claim, deadReason)
		return
	}

	destinationSink, ok := worker.config.Sinks[destination.Type]
	if !ok || destinationSink == nil {
		worker.recordNeverSent(ctx, claim, message.DeadReasonUnsupportedType)
		return
	}

	started := worker.config.Now()
	outcome := worker.deliver(attemptCtx, destinationSink, destination, sink.Request{
		MessageID: claim.Message.ID,
		CreatedAt: claim.Message.CreatedAt,
		Envelope:  claim.Message.Envelope,
	})
	finished := worker.config.Now()
	// Defense in depth: a sink should already have stripped the *url.Error
	// wrapper, which embeds the URL. Only a top-level *url.Error is stripped.
	// errors.As is deliberately not used: it would reach through the sink's own
	// host-masking wrapper and replace its safe text with the raw cause.
	if urlErr, isURLError := outcome.Err.(*url.Error); isURLError { //nolint:errorlint // see the comment above
		outcome.Err = urlErr.Err
	}

	result := Decide(claim.Delivery, outcome, finished, worker.config.Random)
	result.Record.StartedAt = started
	result.Record.LatencyMS = latencyMS(started, finished)

	// Record first, then disable. The plan lists "disable, then record", but
	// the outcome must not be lost: if the disable were first and slow or
	// failing, the send would go unrecorded and the delivery would be sent
	// again after the lease. Each step gets a context of its own (derived from
	// ctx without its cancellation, with its own timeout), because the sink
	// call may have used the whole attempt budget and a slow disable must never
	// starve the record.
	worker.record(ctx, claim, result)
	// A recipient destination is synthetic: there is no registry row to
	// disable, and a gone topic must never disable anything.
	isRecipientDelivery := claim.Delivery.DestinationName == registry.RecipientDestinationName
	if result.Record.Outcome == message.OutcomeGone && !isRecipientDelivery {
		worker.disableDestination(ctx, claim, destination.ID)
	}
}

// disableDestination disables the claim's destination after it reported gone, on its
// own short context so it neither depends on the exhausted attempt context nor
// eats into the record's time. It disables only the destination whose ID the
// attempt resolved (destinationID): one deleted and re-created under the same
// name meanwhile is left alone. A failure is logged and otherwise ignored: the
// outcome has already been recorded.
func (worker *Worker) disableDestination(ctx context.Context, claim message.Claim, destinationID int64) {
	disableCtx, cancelDisable := context.WithTimeout(context.WithoutCancel(ctx), disableTimeout)
	defer cancelDisable()
	disableErr := worker.config.Resolver.DisableDestination(
		disableCtx, claim.Delivery.DestinationName, destinationID, disableReason)
	if errors.Is(disableErr, registry.ErrDestinationNotFound) {
		worker.config.Logger.Warn("destination was deleted or replaced after the 410; not disabled",
			"message_id", claim.Delivery.MessageID, "delivery_id", claim.Delivery.ID,
			"destination", claim.Delivery.DestinationName)
		return
	}
	if disableErr != nil {
		worker.config.Logger.Error("disable destination after 410 failed",
			"message_id", claim.Delivery.MessageID, "delivery_id", claim.Delivery.ID,
			"destination", claim.Delivery.DestinationName, "error", disableErr)
	}
}

// resolve re-resolves the claim's destination by name. A non-empty deadReason
// means the delivery must be dead-lettered without a send; a non-nil error
// means the state is unknown and nothing may be written.
func (worker *Worker) resolve(
	ctx context.Context, claim message.Claim,
) (registry.ResolvedDestination, string, error) {
	if claim.Delivery.DestinationName == registry.RecipientDestinationName {
		return worker.resolveRecipient(ctx, claim)
	}
	// An empty name would make Resolve fan out by route instead of naming one
	// destination: fail closed.
	if claim.Delivery.DestinationName == "" {
		return registry.ResolvedDestination{}, message.DeadReasonDestinationDeleted, nil
	}
	resolved, resolveErr := worker.config.Resolver.Resolve(
		ctx, claim.Message.Envelope.AppID, claim.Delivery.DestinationName, claim.Message.Envelope.Channel)
	switch {
	case errors.Is(resolveErr, registry.ErrUnknownDestination):
		return registry.ResolvedDestination{}, message.DeadReasonDestinationDeleted, nil
	case errors.Is(resolveErr, registry.ErrDestinationDisabled):
		return registry.ResolvedDestination{}, message.DeadReasonDestinationDisabled, nil
	case resolveErr != nil:
		return registry.ResolvedDestination{}, "", resolveErr
	case len(resolved) != 1:
		return registry.ResolvedDestination{}, "", errUnexpectedResolution
	}
	// Resolve looks up by name only, and message_deliveries.destination_id is
	// ON DELETE SET NULL: if the original destination was deleted and another
	// created under the same name, sending would hand this payload to (and sign
	// it for) a destination it was never addressed to. Fail closed on any
	// mismatch.
	if claim.Delivery.DestinationID == nil || *claim.Delivery.DestinationID != resolved[0].ID {
		return registry.ResolvedDestination{}, message.DeadReasonDestinationDeleted, nil
	}
	return resolved[0], "", nil
}

// resolveRecipient builds the ntfy destination of a to.recipient delivery from
// the envelope's person hash. It fails closed: with no recipient resolver
// configured, or an app with no publisher, the delivery is dead-lettered with
// message.DeadReasonNoPublisher; any other error means the state is unknown.
func (worker *Worker) resolveRecipient(
	ctx context.Context, claim message.Claim,
) (registry.ResolvedDestination, string, error) {
	if worker.config.Recipients == nil {
		return registry.ResolvedDestination{}, message.DeadReasonNoPublisher, nil
	}
	envelope := claim.Message.Envelope
	resolved, resolveErr := worker.config.Recipients.ResolveRecipient(
		ctx, envelope.AppID, envelope.ToRecipient, envelope.Channel)
	switch {
	case errors.Is(resolveErr, registry.ErrNoPublisher):
		return registry.ResolvedDestination{}, message.DeadReasonNoPublisher, nil
	case resolveErr != nil:
		return registry.ResolvedDestination{}, "", resolveErr
	}
	return resolved, "", nil
}

// deliver calls the sink, turning a panic into a retryable outcome so one bad
// sink cannot kill the loop.
func (worker *Worker) deliver(
	ctx context.Context, destinationSink sink.Sink, destination registry.ResolvedDestination, request sink.Request,
) (outcome sink.Outcome) {
	defer func() {
		if recover() != nil {
			worker.config.Logger.Error("sink panicked",
				"message_id", request.MessageID, "destination", destination.Name)
			outcome = sink.Outcome{Err: errSinkPanic}
		}
	}()
	return destinationSink.Deliver(ctx, destination, request)
}

// recordNeverSent dead-letters a delivery that could not be sent at all. The
// record is a terminal attempt with no status code, zero latency and a fixed
// error string.
func (worker *Worker) recordNeverSent(ctx context.Context, claim message.Claim, deadReason string) {
	var errorText string
	switch deadReason {
	case message.DeadReasonDestinationDeleted:
		errorText = errorTextDestinationDeleted
	case message.DeadReasonDestinationDisabled:
		errorText = errorTextDestinationDisabled
	case message.DeadReasonUnsupportedType:
		errorText = errorTextUnsupportedType
	case message.DeadReasonBudgetExhausted:
		errorText = errorTextUnresolvable
	case message.DeadReasonNoPublisher:
		errorText = errorTextNoPublisher
	}
	result := message.AttemptResult{
		Record: message.AttemptRecord{
			DeliveryID: claim.Delivery.ID,
			Outcome:    message.OutcomeTerminal,
			Error:      &errorText,
			StartedAt:  worker.config.Now(),
		},
		NextState:  message.StateDead,
		DeadReason: &deadReason,
	}
	worker.record(ctx, claim, result)
}

// record writes the attempt, retrying a few times so a transient database error
// right after a successful send does not lose the outcome. It runs on its own
// context (ctx without its cancellation, bounded by recordTimeout), so neither
// shutdown nor an exhausted attempt context can cut the write short.
//
// If every try fails, the lease expires and the delivery is sent again
// (delivery is at-least-once, and receivers dedupe on
// X-Delivery-Idempotency-Key); the loss is logged loudly. A deterministic
// RecordAttempt failure after a send is NOT bounded by the retry budget,
// because no attempt is ever recorded to charge it to: it resends once per
// lease until the underlying fault is fixed.
func (worker *Worker) record(ctx context.Context, claim message.Claim, result message.AttemptResult) {
	ctx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancelRecord()
	var recordErr error
	for attempt := 1; attempt <= recordAttempts; attempt++ {
		if _, recordErr = worker.config.Queue.RecordAttempt(ctx, result); recordErr == nil {
			break
		}
		if attempt == recordAttempts || ctx.Err() != nil {
			break
		}
		worker.config.Logger.Warn("record attempt failed; retrying",
			"message_id", claim.Delivery.MessageID, "delivery_id", claim.Delivery.ID, "error", recordErr)
		timer := time.NewTimer(worker.config.RecordRetryDelay)
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		timer.Stop()
	}
	if recordErr != nil {
		worker.config.Logger.Error("record attempt failed; the outcome is lost and the delivery will be sent again",
			"message_id", claim.Delivery.MessageID, "delivery_id", claim.Delivery.ID, "error", recordErr)
	}
	worker.logOutcome(claim, result)
}

// logOutcome logs one attempt: Info on success, Warn otherwise. It carries
// identifiers and counters only, never a URL, a payload or a secret.
func (worker *Worker) logOutcome(claim message.Claim, result message.AttemptResult) {
	statusCode := 0
	if result.Record.StatusCode != nil {
		statusCode = *result.Record.StatusCode
	}
	level := slog.LevelWarn
	if result.Record.Outcome == message.OutcomeSuccess {
		level = slog.LevelInfo
	}
	worker.config.Logger.Log(context.Background(), level, "delivery attempt",
		"message_id", claim.Delivery.MessageID,
		"delivery_id", claim.Delivery.ID,
		"destination", claim.Delivery.DestinationName,
		"attempt", claim.Delivery.AttemptCount+1,
		"outcome", result.Record.Outcome,
		"status_code", statusCode,
		"latency_ms", result.Record.LatencyMS,
	)
}

// latencyMS returns the whole milliseconds between started and finished, never
// negative.
func latencyMS(started, finished time.Time) int {
	return int(max(finished.Sub(started), 0).Milliseconds())
}
