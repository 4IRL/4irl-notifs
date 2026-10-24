package heartbeat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	// AlertChannel is the message channel every heartbeat alert is submitted on.
	AlertChannel = "heartbeat"

	// Alert payload types.
	payloadTypeDown      = "heartbeat.down"
	payloadTypeRecovered = "heartbeat.recovered"

	// Display priorities (ntfy's names).
	priorityDown      = "high"
	priorityRecovered = "default"

	// bodyCreation stands in for the last-ping time of a check that never
	// received a ping.
	bodyCreation = "creation"
	// bodyUnknown stands in for a timestamp the store did not record. It cannot
	// occur for a check the state machine moved, but an alert body must never
	// panic on a missing pointer.
	bodyUnknown = "unknown"
	// bodyUnrecognisedReason is the down body for a nil or unknown down reason.
	bodyUnrecognisedReason = "Check is down; reason unrecognised."

	// StartupHoldOff is how long after a Service is built its Tick leaves overdue
	// checks alone: one grace window, the smallest a check can have
	// (MinGraceSeconds). Pings the previous outage blocked get that long to
	// arrive before any check is marked down, so a restart never announces down
	// alerts for checks that were only unreachable.
	StartupHoldOff = MinGraceSeconds * time.Second
)

// Messages is the narrow slice of the message service the checker needs: submit
// one alert envelope. *message.Service satisfies it.
type Messages interface {
	Submit(ctx context.Context, envelope message.Envelope) (message.StoredMessage, message.SubmitOutcome, error)
}

// ServiceConfig configures a Service. Store and Messages are required; Now is
// time.Now and Logger slog.Default() when left zero.
type ServiceConfig struct {
	Store    Store
	Messages Messages
	// Now supplies the time passed to the store, so tests can run on a fake
	// clock.
	Now    func() time.Time
	Logger *slog.Logger
}

// Service is the heartbeat use-case layer: check CRUD, pause and resume, pings,
// and the checker tick that turns the store's alert outbox into messages.
type Service struct {
	store    Store
	messages Messages
	now      func() time.Time
	logger   *slog.Logger
	// holdOffUntil is when the startup hold-off ends: Tick does not advance
	// overdue checks before it.
	holdOffUntil time.Time
}

// NewService builds a Service from config. It panics when Store or Messages is
// nil: a service without them can only fail later, at request or tick time, so
// this fails fast at construction (as message.NewService does).
func NewService(config ServiceConfig) *Service {
	if config.Store == nil {
		panic("heartbeat.NewService: Store is required")
	}
	if config.Messages == nil {
		panic("heartbeat.NewService: Messages is required")
	}
	service := &Service{
		store:    config.Store,
		messages: config.Messages,
		now:      config.Now,
		logger:   config.Logger,
	}
	if service.now == nil {
		service.now = time.Now
	}
	if service.logger == nil {
		service.logger = slog.Default()
	}
	service.holdOffUntil = service.now().Add(StartupHoldOff)
	return service
}

// HoldOffUntil is when the startup hold-off ends: before it, Tick does not
// advance overdue checks and a probe result must not take a check down.
func (service *Service) HoldOffUntil() time.Time {
	return service.holdOffUntil
}

// Create validates and persists a new check in state new, its deadline clock
// starting now.
func (service *Service) Create(ctx context.Context, check NewCheck) (StoredCheck, error) {
	if validateErr := ValidateNewCheck(check); validateErr != nil {
		return StoredCheck{}, validateErr
	}
	return service.store.Create(ctx, check, service.now())
}

// Get returns the check with that id, or ErrCheckNotFound.
func (service *Service) Get(ctx context.Context, id int64) (StoredCheck, error) {
	return service.store.Get(ctx, id)
}

// List returns every check ordered by app_id, then name.
func (service *Service) List(ctx context.Context) ([]StoredCheck, error) {
	return service.store.List(ctx)
}

// Patch merges the patch into the check atomically and replaces its period,
// grace and destination set; the merged result is validated by the store under
// the row lock. A patch naming no field changes nothing and returns the current
// check.
func (service *Service) Patch(ctx context.Context, id int64, patch CheckPatch) (StoredCheck, error) {
	if patch.IsEmpty() {
		return service.store.Get(ctx, id)
	}
	return service.store.Patch(ctx, id, patch, service.now())
}

// Delete removes the check and reports whether it existed.
func (service *Service) Delete(ctx context.Context, id int64) (bool, error) {
	return service.store.Delete(ctx, id)
}

// Pause pauses the check and clears any unflushed alert.
func (service *Service) Pause(ctx context.Context, id int64) (StoredCheck, error) {
	return service.store.Pause(ctx, id, service.now())
}

// Resume resumes a paused check and restarts its deadline clock.
func (service *Service) Resume(ctx context.Context, id int64) (StoredCheck, error) {
	return service.store.Resume(ctx, id, service.now())
}

// Ping records a success ping for the check named by app_id and name.
func (service *Service) Ping(ctx context.Context, appID string, name string) (StoredCheck, error) {
	return service.recordPing(ctx, appID, name, PingSuccess)
}

// Fail records a /fail ping for the check named by app_id and name.
func (service *Service) Fail(ctx context.Context, appID string, name string) (StoredCheck, error) {
	return service.recordPing(ctx, appID, name, PingFail)
}

// recordPing validates the names, then reads the clock just before the store call. This only
// narrows the window: the reading is taken before the store waits for the row lock, so a ping
// blocked behind a newer one can still write an older anchor (bounded by the lock wait).
func (service *Service) recordPing(ctx context.Context, appID string, name string, kind string) (StoredCheck, error) {
	if validateErr := ValidateAppID(appID); validateErr != nil {
		return StoredCheck{}, validateErr
	}
	if validateErr := ValidateName(name); validateErr != nil {
		return StoredCheck{}, validateErr
	}
	return service.store.RecordPing(ctx, appID, name, kind, service.now())
}

// Tick advances overdue checks (skipped during the startup hold-off, which never delays
// flushing) and flushes pending alerts; one check's failed flush never blocks the others.
func (service *Service) Tick(ctx context.Context) error {
	now := service.now()
	if now.Before(service.holdOffUntil) {
		service.logger.Debug("heartbeat startup hold-off: overdue checks not advanced",
			"hold_off_until", service.holdOffUntil)
	} else if _, advanceErr := service.store.AdvanceDue(ctx, now); advanceErr != nil {
		return fmt.Errorf("advance due heartbeat checks: %w", advanceErr)
	}
	pending, listErr := service.store.ListPendingAlerts(ctx)
	if listErr != nil {
		return fmt.Errorf("list pending heartbeat alerts: %w", listErr)
	}
	var flushErrs []error
	for _, check := range pending {
		if flushErr := service.flush(ctx, check); flushErr != nil {
			service.logger.Error("heartbeat alert flush failed; it stays pending",
				"check_id", check.ID, "app_id", check.AppID, "check", check.Name, "error", flushErr)
			flushErrs = append(flushErrs, flushErr)
		}
	}
	return errors.Join(flushErrs...)
}

// flush submits the pending alert to every destination, then clears it; a failure leaves it
// pending and the next tick resubmits with the same idempotency keys, so repeats deduplicate.
func (service *Service) flush(ctx context.Context, check StoredCheck) error {
	if check.PendingAlert == nil {
		return nil
	}
	kind := *check.PendingAlert
	if kind != AlertDown && kind != AlertRecovered {
		// Fail closed: an alert kind this build does not know is left pending
		// rather than announced as something else or silently dropped.
		return fmt.Errorf("unknown pending alert kind %q", kind)
	}
	var failures []error
	for _, destination := range check.Destinations {
		if !destination.Enabled {
			service.logSkipped(check, destination.Name, "destination disabled")
			continue
		}
		envelope, envelopeErr := buildAlertEnvelope(check, kind, destination.Name)
		if envelopeErr != nil {
			failures = append(failures, fmt.Errorf("build alert for destination %q: %w", destination.Name, envelopeErr))
			continue
		}
		_, _, submitErr := service.messages.Submit(ctx, envelope)
		switch {
		case submitErr == nil:
			// Sent, or a duplicate of an earlier send of the same alert.
		case errors.Is(submitErr, registry.ErrUnknownDestination),
			errors.Is(submitErr, registry.ErrDestinationDisabled),
			errors.Is(submitErr, message.ErrUnsupportedDestinationType):
			service.logSkipped(check, destination.Name, submitErr.Error())
		default:
			failures = append(failures, fmt.Errorf("submit alert to destination %q: %w", destination.Name, submitErr))
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	if _, clearErr := service.store.ClearPendingAlert(ctx, check.ID, check.AlertSeq); clearErr != nil {
		return fmt.Errorf("clear pending alert: %w", clearErr)
	}
	return nil
}

// logSkipped records a destination an alert was not sent to and why.
func (service *Service) logSkipped(check StoredCheck, destination string, reason string) {
	service.logger.Warn("heartbeat alert destination skipped",
		"check_id", check.ID, "app_id", check.AppID, "check", check.Name,
		"destination", destination, "reason", reason)
}

// alertPayload is the machine-readable alert payload.
type alertPayload struct {
	Type          string  `json:"type"`
	AppID         string  `json:"app_id"`
	Check         string  `json:"check"`
	Reason        *string `json:"reason"`
	LastPingAt    *string `json:"last_ping_at"`
	DownSince     *string `json:"down_since"`
	PeriodSeconds int     `json:"period_seconds"`
	GraceSeconds  int     `json:"grace_seconds"`
}

// buildAlertEnvelope renders the alert for one destination. The idempotency key
// is stable per check, alert and destination, so a retried flush never delivers
// twice.
func buildAlertEnvelope(check StoredCheck, kind string, destination string) (message.Envelope, error) {
	lastPing := formatTime(check.LastPingAt)
	downSince := formatTime(check.DownSince)
	payload := alertPayload{
		AppID:         check.AppID,
		Check:         check.Name,
		LastPingAt:    lastPing,
		DownSince:     downSince,
		PeriodSeconds: check.PeriodSeconds,
		GraceSeconds:  check.GraceSeconds,
	}
	display := message.Display{}
	critical := false

	subject := check.AppID + "/" + check.Name
	switch kind {
	case AlertDown:
		payload.Type = payloadTypeDown
		payload.Reason = check.DownReason
		critical = true
		display.Title = "Heartbeat down: " + subject
		display.Priority = priorityDown
		display.Body = downBody(check, lastPing)
	case AlertRecovered:
		payload.Type = payloadTypeRecovered
		display.Title = "Heartbeat recovered: " + subject
		display.Priority = priorityRecovered
		display.Body = recoveredBody(check, lastPing, downSince)
	default:
		return message.Envelope{}, fmt.Errorf("unknown alert kind %q", kind)
	}

	encoded, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		return message.Envelope{}, fmt.Errorf("encode alert payload: %w", marshalErr)
	}
	return message.Envelope{
		AppID:          check.AppID,
		Channel:        AlertChannel,
		IdempotencyKey: "hb:" + strconv.FormatInt(check.ID, 10) + ":" + strconv.Itoa(check.AlertSeq) + ":" + destination,
		ToDestination:  destination,
		Critical:       critical,
		Payload:        encoded,
		Display:        &display,
	}, nil
}

// probeReasonLabels is the human label of each probe result reason in a down
// alert body.
var probeReasonLabels = map[string]string{
	ReasonHTTPStatus:   "unexpected HTTP status",
	ReasonTimeout:      "timed out",
	ReasonBodyMismatch: "body did not match",
	ReasonBlocked:      "blocked address",
	ReasonTLS:          "TLS error",
	ReasonScheme:       "non-HTTPS URL",
	ReasonNetwork:      "network error",
	ReasonConfig:       "access credential error",
}

// downBody is the down alert's body, chosen by the check's kind and down reason.
// An absent or unrecognised reason (unreachable under the table CHECKs) gets a
// generic line, never a claim about pings. A probe's detail comes from
// Probe.DownDetail only (already sanitised; sanitised again here so a body can
// never carry a URL path, query or credential).
func downBody(check StoredCheck, lastPing *string) string {
	if check.DownReason == nil {
		return bodyUnrecognisedReason
	}
	reason := *check.DownReason
	if check.Kind() == KindProbe {
		return probeDownBody(check, reason)
	}
	switch reason {
	case ReasonFail:
		return fmt.Sprintf("Failure reported at %s.", valueOr(lastPing, bodyUnknown))
	case ReasonMissed:
		return fmt.Sprintf("No ping since %s (period %s, grace %s).",
			valueOr(lastPing, bodyCreation),
			time.Duration(check.PeriodSeconds)*time.Second,
			time.Duration(check.GraceSeconds)*time.Second)
	default:
		return bodyUnrecognisedReason
	}
}

// probeDownBody is the down body of a probe check.
func probeDownBody(check StoredCheck, reason string) string {
	if reason == ReasonMissed {
		return fmt.Sprintf("Check went down at %s; no probe result was recorded (the prober may be stalled).",
			valueOr(formatTime(check.DownSince), bodyUnknown))
	}
	label, known := probeReasonLabels[reason]
	if !known {
		return bodyUnrecognisedReason
	}
	if check.Probe.DownDetail != nil {
		if detail := SanitizeProbeDetail(*check.Probe.DownDetail); detail != "" {
			return fmt.Sprintf("Probe failed (%s): %s.", label, detail)
		}
	}
	return fmt.Sprintf("Probe failed (%s).", label)
}

// recoveredBody is the recovered alert's body.
func recoveredBody(check StoredCheck, lastPing *string, downSince *string) string {
	verb := "Ping received"
	if check.Kind() == KindProbe {
		verb = "Probe succeeded"
	}
	return fmt.Sprintf("%s at %s; down since %s.",
		verb, valueOr(lastPing, bodyUnknown), valueOr(downSince, bodyUnknown))
}

// formatTime renders a timestamp as RFC3339 UTC, or nil for a nil time.
func formatTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

// valueOr returns *value, or fallback when value is nil.
func valueOr(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}
