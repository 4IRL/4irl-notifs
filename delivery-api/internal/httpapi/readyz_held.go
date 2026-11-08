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
