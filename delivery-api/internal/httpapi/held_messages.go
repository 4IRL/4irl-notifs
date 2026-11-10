package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

// unassignedChecksLimit caps the unassigned-checks list; unassigned_total stays uncapped.
const unassignedChecksLimit = 50

// errMsgHeldUnavailable is the 503 body when a held-messages data source is not wired.
const errMsgHeldUnavailable = "held messages unavailable"

// HeldMessageSummarizer reports every app's held-message count.
type HeldMessageSummarizer interface {
	HeldSummary(ctx context.Context) ([]message.HeldAppSummary, error)
}

// UnassignedAlertLister lists checks whose pending alert has no deliverable destination.
type UnassignedAlertLister interface {
	ListUnassignedAlerts(ctx context.Context, limit int) ([]heartbeat.UnassignedCheck, int, error)
}

// heldAppView is one app's held count in the held-messages body.
type heldAppView struct {
	AppID    string    `json:"app_id"`
	Count    int       `json:"count"`
	OldestAt time.Time `json:"oldest_at"`
}

// unassignedCheckView is one check with a pending alert and no destination.
type unassignedCheckView struct {
	CheckID      int64  `json:"check_id"`
	AppID        string `json:"app_id"`
	Name         string `json:"name"`
	PendingAlert string `json:"pending_alert"`
}

// heldMessagesResponseBody is the 200 body of GET /v1/admin/held-messages.
type heldMessagesResponseBody struct {
	Apps             []heldAppView         `json:"apps"`
	UnassignedChecks []unassignedCheckView `json:"unassigned_checks"`
	UnassignedTotal  int                   `json:"unassigned_total"`
}

// handleHeldMessages serves GET /v1/admin/held-messages: every app's held count
// (uncapped, uncached, unlike readyz) and the capped list of heartbeat checks whose
// pending alert has nowhere to go. A missing data source answers 503 (fail closed);
// a store error answers a generic 500 without echoing the error. Operator-only
// through the Cloudflare Access /v1/admin split, not by delivery-api code.
func (server *Server) handleHeldMessages(responseWriter http.ResponseWriter, request *http.Request) {
	if server.heldSummary == nil || server.unassignedAlerts == nil {
		writeError(responseWriter, http.StatusServiceUnavailable, errMsgHeldUnavailable)
		return
	}
	summary, summaryErr := server.heldSummary.HeldSummary(request.Context())
	if summaryErr != nil {
		server.writeMessageError(responseWriter, request, summaryErr)
		return
	}
	checks, total, listErr := server.unassignedAlerts.ListUnassignedAlerts(request.Context(), unassignedChecksLimit)
	if listErr != nil {
		server.writeMessageError(responseWriter, request, listErr)
		return
	}
	apps := make([]heldAppView, 0, len(summary))
	for _, row := range summary {
		apps = append(apps, heldAppView{AppID: row.AppID, Count: row.Count, OldestAt: row.OldestHeldAt.UTC()})
	}
	views := make([]unassignedCheckView, 0, len(checks))
	for _, check := range checks {
		views = append(views, unassignedCheckView{
			CheckID: check.CheckID, AppID: check.AppID, Name: check.Name, PendingAlert: check.PendingAlert,
		})
	}
	writeJSON(responseWriter, http.StatusOK, heldMessagesResponseBody{
		Apps: apps, UnassignedChecks: views, UnassignedTotal: total,
	})
}
