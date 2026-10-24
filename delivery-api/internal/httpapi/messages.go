package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	// maxMessageBodyBytes caps a POST /v1/messages body: the 128 KiB payload cap
	// plus the rest of the envelope, with headroom for JSON escaping.
	maxMessageBodyBytes = 256 << 10
	// maxWaitSeconds is the longest a submit may block waiting for a first
	// delivery attempt; a larger ?wait= is clamped to it.
	maxWaitSeconds = 10
	// MaxWait is maxWaitSeconds as a duration: the longest a request can hold
	// HTTP shutdown open, which the process shutdown budget must cover.
	MaxWait = maxWaitSeconds * time.Second
)

// Error messages returned by the message endpoints.
const (
	errMsgMessagesUnavailable        = "messages unavailable"
	errMsgInvalidWait                = "invalid wait"
	errMsgInvalidDeliveryID          = "invalid delivery id"
	errMsgAppIDRequired              = "app_id is required"
	errMsgKeyLookupRequired          = "app_id and idempotency_key are required"
	errMsgDestinationDisabled        = "destination disabled"
	errMsgNoDestinationMatched       = "no destination matched"
	errMsgDestinationTypeUnsupported = "destination type not yet supported"
	errMsgNoPublisher                = "app has no ntfy publisher"
	errMsgMessageNotFound            = "message not found"
	errMsgDeliveryNotFound           = "delivery not found"
	errMsgDeliveryNotDead            = "delivery not dead"
	errMsgTooManyScheduled           = "too many scheduled messages"
	errMsgScheduledCapacityReached   = "scheduled message capacity reached"
)

// Query parameter and path value names, and list defaults.
const (
	queryAppID          = "app_id"
	queryIdempotencyKey = "idempotency_key"
	queryWait           = "wait"
	queryState          = "state"
	queryLimit          = "limit"
	pathValueID         = "id"

	// defaultListState is the delivery list's state when none is named.
	defaultListState = message.StateDead
	// invalidListLimit is passed to the service for an unparseable ?limit=, so the
	// service's range check produces the 400.
	invalidListLimit = -1
)

// Messages is the message surface the /v1/messages and /v1/admin/deliveries
// endpoints call. *message.Service satisfies it.
//
// Reads and cancels that an app can make (Get, GetByKey, Await, Cancel,
// CancelByKey) take the calling app's id and must treat another app's message
// exactly like an absent one. The delivery, scheduled-list and AdminCancel
// methods are operator-only and span apps by design.
type Messages interface {
	// Submit validates, resolves and queues envelope. The outcome reports a
	// duplicate of an earlier submission (whose original message is returned), a
	// replace of a scheduled message, and whether the result is scheduled.
	Submit(ctx context.Context, envelope message.Envelope) (message.StoredMessage, message.SubmitOutcome, error)
	// Get returns app appID's message with the given id.
	Get(ctx context.Context, appID string, id string) (message.StoredMessage, error)
	// GetByKey returns the message app appID submitted with idempotencyKey.
	GetByKey(ctx context.Context, appID string, idempotencyKey string) (message.StoredMessage, error)
	// Await waits up to timeout for every delivery of app appID's message to be
	// attempted, and returns the last read.
	Await(ctx context.Context, appID string, id string, timeout time.Duration) (message.StoredMessage, error)
	// ListDeliveries returns up to limit deliveries in state.
	ListDeliveries(ctx context.Context, state string, limit int) ([]message.AdminDelivery, error)
	// ListAttempts returns a delivery's attempts in attempt order.
	ListAttempts(ctx context.Context, deliveryID int64) ([]message.Attempt, error)
	// Replay returns a dead delivery to the queue.
	Replay(ctx context.Context, deliveryID int64) (message.AdminDelivery, error)
	// Cancel cancels app appID's scheduled message with the given id. A miss
	// (unknown id, another app's message) is a StatusNotFound outcome, not an error.
	Cancel(ctx context.Context, appID string, id string) (message.CancelOutcome, error)
	// CancelByKey is Cancel for the message app appID submitted with idempotencyKey.
	CancelByKey(ctx context.Context, appID string, idempotencyKey string) (message.CancelOutcome, error)
	// AdminCancel is Cancel across every app, for the operator.
	AdminCancel(ctx context.Context, id string) (message.CancelOutcome, error)
	// ListScheduled returns up to limit scheduled messages, soonest first, and the
	// total number of scheduled messages.
	ListScheduled(ctx context.Context, limit int) ([]message.ScheduledMessage, int, error)
}

// createMessageRequestBody is the POST /v1/messages body. Unknown top-level
// fields are rejected; the contents of payload are opaque and never inspected
// by the decoder.
type createMessageRequestBody struct {
	AppID          string                `json:"app_id"`
	To             *messageToRequestBody `json:"to"`
	Channel        string                `json:"channel"`
	IdempotencyKey string                `json:"idempotency_key"`
	DeliverAt      *time.Time            `json:"deliver_at"`
	Critical       bool                  `json:"critical"`
	Payload        json.RawMessage       `json:"payload"`
	Display        *message.Display      `json:"display"`
}

// messageToRequestBody is the optional "to" block of a submit.
type messageToRequestBody struct {
	Destination string `json:"destination"`
	Recipient   string `json:"recipient"`
}

// messageView is the JSON form of a stored message. It deliberately omits the
// payload and display, which an app already has and a status read never needs.
// DeliverAt is echoed as submitted, except that a replace which releases the
// message at once reports the release time. It is null when none was given (a
// past value is returned non-null). CancelledAt is null unless the message was
// cancelled; both are UTC.
type messageView struct {
	MessageID      string         `json:"message_id"`
	AppID          string         `json:"app_id"`
	Channel        string         `json:"channel"`
	IdempotencyKey string         `json:"idempotency_key"`
	Critical       bool           `json:"critical"`
	Status         string         `json:"status"`
	DeliverAt      *time.Time     `json:"deliver_at"`
	CreatedAt      time.Time      `json:"created_at"`
	CancelledAt    *time.Time     `json:"cancelled_at"`
	Deliveries     []deliveryView `json:"deliveries"`
}

// deliveryView is the JSON form of one delivery. NextAttemptAt is set only
// while the delivery is pending: for any other state (including scheduled and
// cancelled) it is meaningless.
type deliveryView struct {
	ID             int64      `json:"id"`
	Destination    string     `json:"destination"`
	State          string     `json:"state"`
	DeadReason     *string    `json:"dead_reason"`
	AttemptCount   int        `json:"attempt_count"`
	LastStatusCode *int       `json:"last_status_code"`
	NextAttemptAt  *time.Time `json:"next_attempt_at"`
	DeliveredAt    *time.Time `json:"delivered_at"`
}

// adminDeliveryView is the operator JSON form of one delivery: the app-facing
// deliveryView fields plus the parent message's identity and the delivery
// timestamps. It is used only by the admin list and replay responses; app-facing
// message responses keep deliveryView.
type adminDeliveryView struct {
	deliveryView
	MessageID       string    `json:"message_id"`
	AppID           string    `json:"app_id"`
	Channel         string    `json:"channel"`
	DestinationType string    `json:"destination_type"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// attemptView is the JSON form of one delivery-log row.
type attemptView struct {
	AttemptNumber   int       `json:"attempt_number"`
	Outcome         string    `json:"outcome"`
	StatusCode      *int      `json:"status_code"`
	LatencyMS       int       `json:"latency_ms"`
	Error           *string   `json:"error"`
	ResponseSnippet *string   `json:"response_snippet"`
	StartedAt       time.Time `json:"started_at"`
}

// submitResponseBody is the 202 body of a submit.
type submitResponseBody struct {
	Message   messageView `json:"message"`
	Duplicate bool        `json:"duplicate"`
	Replaced  bool        `json:"replaced"`
}

// cancelMessageResponseBody is the 200 body of every cancel outcome, including a
// miss (Status "not_found", Cancelled false).
type cancelMessageResponseBody struct {
	MessageID string `json:"message_id"`
	Cancelled bool   `json:"cancelled"`
	Status    string `json:"status"`
}

// scheduledDestinationView is one destination a scheduled message targets.
type scheduledDestinationView struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// scheduledMessageView is the operator JSON form of one scheduled message. It
// carries no payload, display or person hash; Destinations is never null.
type scheduledMessageView struct {
	MessageID      string                     `json:"message_id"`
	AppID          string                     `json:"app_id"`
	Channel        string                     `json:"channel"`
	IdempotencyKey string                     `json:"idempotency_key"`
	DeliverAt      time.Time                  `json:"deliver_at"`
	CreatedAt      time.Time                  `json:"created_at"`
	Recipient      bool                       `json:"recipient"`
	Destinations   []scheduledDestinationView `json:"destinations"`
}

// scheduledMessagesResponseBody wraps the scheduled list (never nil) and the
// total number of scheduled messages, which may exceed the list's length.
type scheduledMessagesResponseBody struct {
	ScheduledMessages []scheduledMessageView `json:"scheduled_messages"`
	Total             int                    `json:"total"`
}

// messageResponseBody wraps one message view.
type messageResponseBody struct {
	Message messageView `json:"message"`
}

// deliveriesResponseBody wraps the delivery list; the slice is never nil.
type deliveriesResponseBody struct {
	Deliveries []adminDeliveryView `json:"deliveries"`
}

// deliveryResponseBody wraps one admin delivery view.
type deliveryResponseBody struct {
	Delivery adminDeliveryView `json:"delivery"`
}

// attemptsResponseBody wraps the attempt list; the slice is never nil.
type attemptsResponseBody struct {
	Attempts []attemptView `json:"attempts"`
}

// requireMessages wraps a message handler so that it fails closed: with no
// Messages configured the request is answered 503 before anything else is
// looked at.
func (server *Server) requireMessages(handler http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		// Message status and delivery logs are per-app data: never cache them or let a
		// browser sniff them.
		setSensitiveHeaders(responseWriter)
		if server.messages == nil {
			writeError(responseWriter, http.StatusServiceUnavailable, errMsgMessagesUnavailable)
			return
		}
		handler(responseWriter, request)
	}
}

// handleCreateMessage serves POST /v1/messages (202) and the operator's POST
// /v1/admin/messages, which differ only in the route's auth: the app route sits behind
// requireAppKey (the body's app_id is the key's app), the operator route behind the
// Access /v1/admin split. ?wait=N (0-10 seconds)
// holds the response until every delivery has had a first attempt or the wait
// elapses; a duplicate never waits.
func (server *Server) handleCreateMessage(responseWriter http.ResponseWriter, request *http.Request) {
	wait, waitOK := parseWait(responseWriter, request)
	if !waitOK {
		return
	}

	var body createMessageRequestBody
	if !decodeJSON(responseWriter, request, &body, maxMessageBodyBytes) {
		return
	}

	envelope := message.Envelope{
		AppID:          body.AppID,
		Channel:        body.Channel,
		IdempotencyKey: body.IdempotencyKey,
		DeliverAt:      body.DeliverAt,
		Critical:       body.Critical,
		Payload:        body.Payload,
		Display:        body.Display,
	}
	if body.To != nil {
		envelope.ToDestination = body.To.Destination
		envelope.ToRecipient = body.To.Recipient
	}

	stored, outcome, err := server.messages.Submit(request.Context(), envelope)
	if err != nil {
		server.writeMessageError(responseWriter, request, err)
		return
	}
	duplicate := outcome.Duplicate
	if wait > 0 && !duplicate && !outcome.Scheduled {
		// The message is already committed, so a failed wait is not a failed
		// submit: answer with the freshest view available. A scheduled message has
		// no attempt to wait for, so it answers at once.
		awaited, awaitErr := server.messages.Await(request.Context(), stored.Envelope.AppID, stored.ID, wait)
		if awaitErr != nil {
			server.logger.Warn("message wait failed", "error", awaitErr, "path", request.URL.Path)
		}
		if awaited.ID != "" {
			stored = awaited
		}
	}
	writeJSON(responseWriter, http.StatusAccepted, submitResponseBody{
		Message:   newMessageView(stored),
		Duplicate: duplicate,
		Replaced:  outcome.Replaced,
	})
}

// handleGetMessage serves GET /v1/messages/{id}?app_id=. The app_id names the
// calling app: a message that belongs to another app is a 404, exactly like an
// absent one.
func (server *Server) handleGetMessage(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := parseAppID(responseWriter, request)
	if !ok {
		return
	}

	stored, err := server.messages.Get(request.Context(), appID, request.PathValue(pathValueID))
	if err != nil {
		server.writeMessageError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, messageResponseBody{Message: newMessageView(stored)})
}

// handleGetMessageByKey serves GET /v1/messages?app_id=&idempotency_key=.
func (server *Server) handleGetMessageByKey(responseWriter http.ResponseWriter, request *http.Request) {
	appID, idempotencyKey, ok := parseKeyLookup(responseWriter, request)
	if !ok {
		return
	}

	stored, err := server.messages.GetByKey(request.Context(), appID, idempotencyKey)
	if err != nil {
		server.writeMessageError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, messageResponseBody{Message: newMessageView(stored)})
}

// handleCancelMessage serves DELETE /v1/messages/{id}?app_id=. Every outcome
// including a miss is a 200 (the cancel is idempotent); only a missing or
// malformed app_id is a 400.
func (server *Server) handleCancelMessage(responseWriter http.ResponseWriter, request *http.Request) {
	appID, ok := parseAppID(responseWriter, request)
	if !ok {
		return
	}

	outcome, err := server.messages.Cancel(request.Context(), appID, request.PathValue(pathValueID))
	server.writeCancelOutcome(responseWriter, request, outcome, err)
}

// handleCancelMessageByKey serves DELETE /v1/messages?app_id=&idempotency_key=.
func (server *Server) handleCancelMessageByKey(responseWriter http.ResponseWriter, request *http.Request) {
	appID, idempotencyKey, ok := parseKeyLookup(responseWriter, request)
	if !ok {
		return
	}

	outcome, err := server.messages.CancelByKey(request.Context(), appID, idempotencyKey)
	server.writeCancelOutcome(responseWriter, request, outcome, err)
}

// handleAdminCancelMessage serves DELETE /v1/admin/scheduled-messages/{id}: the
// app cancel without the app scoping.
func (server *Server) handleAdminCancelMessage(responseWriter http.ResponseWriter, request *http.Request) {
	outcome, err := server.messages.AdminCancel(request.Context(), request.PathValue(pathValueID))
	server.writeCancelOutcome(responseWriter, request, outcome, err)
}

// writeCancelOutcome answers a cancel with its 200 body, or maps err.
func (server *Server) writeCancelOutcome(
	responseWriter http.ResponseWriter, request *http.Request, outcome message.CancelOutcome, err error,
) {
	if err != nil {
		server.writeMessageError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, cancelMessageResponseBody{
		MessageID: outcome.MessageID,
		Cancelled: outcome.Cancelled,
		Status:    outcome.Status,
	})
}

// handleListScheduledMessages serves GET /v1/admin/scheduled-messages?limit=.
// The limit is parsed like the delivery list's: a non-integer is passed on as an
// out-of-range value so the service's range check is the single source of the 400.
func (server *Server) handleListScheduledMessages(responseWriter http.ResponseWriter, request *http.Request) {
	scheduled, total, err := server.messages.ListScheduled(request.Context(), parseListLimit(request))
	if err != nil {
		server.writeMessageError(responseWriter, request, err)
		return
	}
	views := make([]scheduledMessageView, 0, len(scheduled))
	for _, scheduledMessage := range scheduled {
		views = append(views, newScheduledMessageView(scheduledMessage))
	}
	writeJSON(responseWriter, http.StatusOK, scheduledMessagesResponseBody{ScheduledMessages: views, Total: total})
}

// handleListDeliveries serves GET /v1/admin/deliveries?state=&limit=. The state
// defaults to dead and the limit to 50. A limit that is not an integer is passed
// on as an out-of-range value, so the service's range check is the single
// source of the 400 for both a non-integer and an out-of-range limit.
func (server *Server) handleListDeliveries(responseWriter http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	state := query.Get(queryState)
	if state == "" {
		state = defaultListState
	}

	deliveries, err := server.messages.ListDeliveries(request.Context(), state, parseListLimit(request))
	if err != nil {
		server.writeMessageError(responseWriter, request, err)
		return
	}
	views := make([]adminDeliveryView, 0, len(deliveries))
	for _, delivery := range deliveries {
		views = append(views, newAdminDeliveryView(delivery))
	}
	writeJSON(responseWriter, http.StatusOK, deliveriesResponseBody{Deliveries: views})
}

// handleListAttempts serves GET /v1/admin/deliveries/{id}/attempts.
func (server *Server) handleListAttempts(responseWriter http.ResponseWriter, request *http.Request) {
	deliveryID, ok := parseDeliveryID(responseWriter, request)
	if !ok {
		return
	}

	attempts, err := server.messages.ListAttempts(request.Context(), deliveryID)
	if err != nil {
		server.writeMessageError(responseWriter, request, err)
		return
	}
	views := make([]attemptView, 0, len(attempts))
	for _, attempt := range attempts {
		views = append(views, newAttemptView(attempt))
	}
	writeJSON(responseWriter, http.StatusOK, attemptsResponseBody{Attempts: views})
}

// handleReplayDelivery serves POST /v1/admin/deliveries/{id}/replay.
func (server *Server) handleReplayDelivery(responseWriter http.ResponseWriter, request *http.Request) {
	deliveryID, ok := parseDeliveryID(responseWriter, request)
	if !ok {
		return
	}

	replayed, err := server.messages.Replay(request.Context(), deliveryID)
	if err != nil {
		server.writeMessageError(responseWriter, request, err)
		return
	}
	writeJSON(responseWriter, http.StatusOK, deliveryResponseBody{Delivery: newAdminDeliveryView(replayed)})
}

// parseListLimit reads the optional ?limit= query: a missing value is the
// default, and a value that is not an integer becomes an out-of-range one, so the
// service's range check is the single source of the 400.
func parseListLimit(request *http.Request) int {
	rawLimit := request.URL.Query().Get(queryLimit)
	if rawLimit == "" {
		return message.DefaultListLimit
	}
	parsed, err := strconv.Atoi(rawLimit)
	if err != nil {
		return invalidListLimit
	}
	return parsed
}

// parseAppID reads the required ?app_id= query. A missing value writes 400
// "app_id is required" and returns false; a present but malformed one is left
// for the service to reject.
func parseAppID(responseWriter http.ResponseWriter, request *http.Request) (string, bool) {
	appID := request.URL.Query().Get(queryAppID)
	if appID == "" {
		writeError(responseWriter, http.StatusBadRequest, errMsgAppIDRequired)
		return "", false
	}
	return appID, true
}

// parseKeyLookup reads the required ?app_id= and ?idempotency_key= queries of a
// by-key request. Either missing writes 400 and returns false.
func parseKeyLookup(responseWriter http.ResponseWriter, request *http.Request) (string, string, bool) {
	query := request.URL.Query()
	appID := query.Get(queryAppID)
	idempotencyKey := query.Get(queryIdempotencyKey)
	if appID == "" || idempotencyKey == "" {
		writeError(responseWriter, http.StatusBadRequest, errMsgKeyLookupRequired)
		return "", "", false
	}
	return appID, idempotencyKey, true
}

// parseWait reads the optional ?wait= query as whole seconds. A missing value is
// 0, a value above maxWaitSeconds is clamped to it, and a negative or
// non-integer value writes 400 "invalid wait" and returns false.
func parseWait(responseWriter http.ResponseWriter, request *http.Request) (time.Duration, bool) {
	rawWait := request.URL.Query().Get(queryWait)
	if rawWait == "" {
		return 0, true
	}
	seconds, err := strconv.Atoi(rawWait)
	if err != nil || seconds < 0 {
		writeError(responseWriter, http.StatusBadRequest, errMsgInvalidWait)
		return 0, false
	}
	return time.Duration(min(seconds, maxWaitSeconds)) * time.Second, true
}

// parseDeliveryID reads the {id} path value as a canonical positive integer (no
// sign, no leading zeros, so each delivery has exactly one URL). On failure it
// writes 400 "invalid delivery id" and returns false.
func parseDeliveryID(responseWriter http.ResponseWriter, request *http.Request) (int64, bool) {
	return parsePositiveID(responseWriter, request, errMsgInvalidDeliveryID)
}

// newMessageView converts a stored message into its JSON view. Times are UTC and
// the deliveries slice is never nil.
func newMessageView(stored message.StoredMessage) messageView {
	deliveries := make([]deliveryView, 0, len(stored.Deliveries))
	for _, delivery := range stored.Deliveries {
		deliveries = append(deliveries, newDeliveryView(delivery))
	}
	return messageView{
		MessageID:      stored.ID,
		AppID:          stored.Envelope.AppID,
		Channel:        stored.Envelope.Channel,
		IdempotencyKey: stored.Envelope.IdempotencyKey,
		Critical:       stored.Envelope.Critical,
		Status:         message.DeriveStatus(stored.Deliveries),
		DeliverAt:      utcOrNil(stored.Envelope.DeliverAt),
		CreatedAt:      stored.CreatedAt.UTC(),
		CancelledAt:    utcOrNil(stored.CancelledAt),
		Deliveries:     deliveries,
	}
}

// newScheduledMessageView converts a scheduled-message read model into its JSON
// view; times are UTC and the destinations slice is never nil.
func newScheduledMessageView(scheduled message.ScheduledMessage) scheduledMessageView {
	destinations := make([]scheduledDestinationView, 0, len(scheduled.Destinations))
	for _, destination := range scheduled.Destinations {
		destinations = append(destinations, scheduledDestinationView{Name: destination.Name, Type: destination.Type})
	}
	return scheduledMessageView{
		MessageID:      scheduled.MessageID,
		AppID:          scheduled.AppID,
		Channel:        scheduled.Channel,
		IdempotencyKey: scheduled.IdempotencyKey,
		DeliverAt:      scheduled.DeliverAt.UTC(),
		CreatedAt:      scheduled.CreatedAt.UTC(),
		Recipient:      scheduled.Recipient,
		Destinations:   destinations,
	}
}

// newDeliveryView converts a delivery into its JSON view; next_attempt_at is set
// only for a pending delivery.
func newDeliveryView(delivery message.Delivery) deliveryView {
	view := deliveryView{
		ID:             delivery.ID,
		Destination:    delivery.DestinationName,
		State:          delivery.State,
		DeadReason:     delivery.DeadReason,
		AttemptCount:   delivery.AttemptCount,
		LastStatusCode: delivery.LastStatusCode,
		DeliveredAt:    utcOrNil(delivery.DeliveredAt),
	}
	if delivery.State == message.StatePending {
		nextAttemptAt := delivery.NextAttemptAt.UTC()
		view.NextAttemptAt = &nextAttemptAt
	}
	return view
}

// newAdminDeliveryView converts an admin delivery into its JSON view; times are UTC.
func newAdminDeliveryView(delivery message.AdminDelivery) adminDeliveryView {
	return adminDeliveryView{
		deliveryView:    newDeliveryView(delivery.Delivery),
		MessageID:       delivery.MessageID,
		AppID:           delivery.AppID,
		Channel:         delivery.Channel,
		DestinationType: delivery.DestinationType,
		CreatedAt:       delivery.CreatedAt.UTC(),
		UpdatedAt:       delivery.UpdatedAt.UTC(),
	}
}

// newAttemptView converts a delivery-log row into its JSON view.
func newAttemptView(attempt message.Attempt) attemptView {
	return attemptView{
		AttemptNumber:   attempt.AttemptNumber,
		Outcome:         attempt.Outcome,
		StatusCode:      attempt.StatusCode,
		LatencyMS:       attempt.LatencyMS,
		Error:           attempt.Error,
		ResponseSnippet: attempt.ResponseSnippet,
		StartedAt:       attempt.StartedAt.UTC(),
	}
}

// utcOrNil returns a UTC copy of value, or nil when value is nil.
func utcOrNil(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

// writeMessageError is the single chokepoint mapping a message-service error to
// an HTTP response. Validation errors carry a client-safe message; known
// sentinels map to fixed messages; anything else is logged (path only, never the
// request body or payload) and answered with a generic 500 so no internal detail
// leaks.
func (server *Server) writeMessageError(responseWriter http.ResponseWriter, request *http.Request, err error) {
	var validationErr *message.ValidationError
	switch {
	case errors.As(err, &validationErr):
		writeError(responseWriter, http.StatusBadRequest, validationErr.Message)
	case errors.Is(err, registry.ErrUnknownDestination):
		writeError(responseWriter, http.StatusBadRequest, errMsgUnknownDestination)
	case errors.Is(err, registry.ErrDestinationDisabled):
		writeError(responseWriter, http.StatusConflict, errMsgDestinationDisabled)
	case errors.Is(err, message.ErrNoDestinationMatched):
		writeError(responseWriter, http.StatusUnprocessableEntity, errMsgNoDestinationMatched)
	case errors.Is(err, message.ErrUnsupportedDestinationType):
		writeError(responseWriter, http.StatusUnprocessableEntity, errMsgDestinationTypeUnsupported)
	case errors.Is(err, registry.ErrNoPublisher):
		writeError(responseWriter, http.StatusUnprocessableEntity, errMsgNoPublisher)
	case errors.Is(err, message.ErrMessageNotFound):
		writeError(responseWriter, http.StatusNotFound, errMsgMessageNotFound)
	case errors.Is(err, message.ErrDeliveryNotFound):
		writeError(responseWriter, http.StatusNotFound, errMsgDeliveryNotFound)
	case errors.Is(err, message.ErrDeliveryNotDead):
		writeError(responseWriter, http.StatusConflict, errMsgDeliveryNotDead)
	case errors.Is(err, message.ErrTooManyScheduled):
		writeError(responseWriter, http.StatusTooManyRequests, errMsgTooManyScheduled)
	case errors.Is(err, message.ErrScheduledCapacityReached):
		writeError(responseWriter, http.StatusTooManyRequests, errMsgScheduledCapacityReached)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The client went away or the request ran out of time: not a server fault,
		// and a client that can disconnect at will must not be able to flood the
		// error log. The response is moot, but stays the generic 500.
		server.logger.Debug("message request ended early", "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	default:
		server.logger.Error("message service error", "error", err, "path", request.URL.Path)
		writeError(responseWriter, http.StatusInternalServerError, errMsgInternalError)
	}
}
