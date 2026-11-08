package httpapi

import (
	"context"
	"time"
)

// HeldAppCount is one app's held-message count.
type HeldAppCount struct {
	AppID string
	Count int
}

// HeldSnapshot is the aggregate held-work view served on /readyz. When
// Available is false no other field is meaningful.
type HeldSnapshot struct {
	Available        bool
	Stale            bool
	AsOf             time.Time
	HeldMessages     int
	OldestHeldAt     *time.Time
	UnassignedAlerts int
	Apps             []HeldAppCount
	AppsMore         int
}

// HeldReporter returns the current held snapshot. It has no error return: the
// reporter fails soft and flags unavailable or stale data in the snapshot.
type HeldReporter interface {
	HeldSnapshot(ctx context.Context) HeldSnapshot
}

// heldBody is the JSON form of the held block on GET /readyz. When Available
// is false every other field is nil and omitted, so the unavailable form is
// exactly {"available":false}; the available form always emits every field.
// The block is status-neutral for the same compose-healthcheck reason as
// heartbeatCheckerBody: it never changes the /readyz status or HTTP code.
type heldBody struct {
	Available        bool              `json:"available"`
	Stale            *bool             `json:"stale,omitempty"`
	AsOf             *string           `json:"as_of,omitempty"`
	Total            *int              `json:"total,omitempty"`
	Messages         *heldMessagesBody `json:"messages,omitempty"`
	UnassignedAlerts *heldAlertsBody   `json:"unassigned_alerts,omitempty"`
	Apps             *[]heldAppBody    `json:"apps,omitempty"`
	AppsMore         *int              `json:"apps_more,omitempty"`
}

type heldMessagesBody struct {
	Count            int     `json:"count"`
	OldestHeldAt     *string `json:"oldest_held_at"`
	OldestAgeSeconds *int64  `json:"oldest_age_seconds"`
}

type heldAlertsBody struct {
	Count int `json:"count"`
}

type heldAppBody struct {
	AppID string `json:"app_id"`
	Count int    `json:"count"`
}

// heldStatus renders the reporter's snapshot. oldest_age_seconds is computed
// here from the server clock so it does not lag the reporter's cache TTL.
func (server *Server) heldStatus(ctx context.Context) *heldBody {
	snapshot := server.held.HeldSnapshot(ctx)
	if !snapshot.Available {
		return &heldBody{Available: false}
	}
	messages := heldMessagesBody{Count: snapshot.HeldMessages, OldestHeldAt: formatUTC(snapshot.OldestHeldAt)}
	if snapshot.OldestHeldAt != nil {
		ageSeconds := int64(server.now().Sub(*snapshot.OldestHeldAt) / time.Second)
		if ageSeconds < 0 {
			ageSeconds = 0
		}
		messages.OldestAgeSeconds = &ageSeconds
	}
	apps := make([]heldAppBody, 0, len(snapshot.Apps))
	for _, app := range snapshot.Apps {
		apps = append(apps, heldAppBody(app))
	}
	stale := snapshot.Stale
	total := snapshot.HeldMessages + snapshot.UnassignedAlerts
	appsMore := snapshot.AppsMore
	return &heldBody{
		Available:        true,
		Stale:            &stale,
		AsOf:             formatUTC(&snapshot.AsOf),
		Total:            &total,
		Messages:         &messages,
		UnassignedAlerts: &heldAlertsBody{Count: snapshot.UnassignedAlerts},
		Apps:             &apps,
		AppsMore:         &appsMore,
	}
}
