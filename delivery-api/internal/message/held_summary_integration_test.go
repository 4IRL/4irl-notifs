//go:build integration

package message_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
)

// clearHeldAt makes a held row no longer held, as release, release-failure and
// held-cancel each do.
func clearHeldAt(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, messageID string) {
	testInstance.Helper()
	if _, execErr := pool.Exec(ctx, `UPDATE messages SET held_at = NULL WHERE id = $1::text::uuid`, messageID); execErr != nil {
		testInstance.Fatalf("clear held_at: %v", execErr)
	}
}

func TestHeldSummaryCountsOnlyHeldRowsPerApp(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)
	base := scheduleBase

	insertFor := func(appID string, key string, heldAt time.Time) message.StoredMessage {
		testInstance.Helper()
		stored, _, err := store.InsertHeldMessage(ctx, heldEnvelopeFor(appID, key), heldCaps(50, 100), heldAt)
		if err != nil {
			testInstance.Fatalf("hold %s: %v", key, err)
		}
		return stored
	}

	// u4i: 3 held, oldest is base-3h. billing: 2 held, oldest is base-1h.
	insertFor("u4i", "u1", base)
	insertFor("u4i", "u2", base.Add(-3*time.Hour))
	insertFor("u4i", "u3", base.Add(-2*time.Hour))
	insertFor("billing", "b1", base.Add(-time.Hour))
	insertFor("billing", "b2", base)
	// alpha: 3 held too, to prove the app id tie-break (alpha before u4i).
	insertFor("alpha", "a1", base.Add(-10*time.Minute))
	insertFor("alpha", "a2", base)
	insertFor("alpha", "a3", base)

	// Rows that are no longer held.
	cancelled := insertFor("billing", "cancelled", base.Add(-48*time.Hour))
	// cancelled_at is only legal alongside held_cancelled_at, so set both in one statement.
	if _, execErr := pool.Exec(ctx,
		`UPDATE messages SET cancelled_at = $2, held_cancelled_at = $2 WHERE id = $1::text::uuid`,
		cancelled.ID, base); execErr != nil {
		testInstance.Fatalf("cancel held message: %v", execErr)
	}
	clearHeldAt(ctx, testInstance, pool, cancelled.ID)

	released := insertFor("billing", "released", base.Add(-48*time.Hour))
	setMessageColumn(ctx, testInstance, pool, released.ID, "released_at", base)
	clearHeldAt(ctx, testInstance, pool, released.ID)

	failed := insertFor("billing", "failed", base.Add(-48*time.Hour))
	if _, execErr := pool.Exec(ctx,
		`UPDATE messages SET release_failed_at = $2, release_failed_reason = 'release_invalid' WHERE id = $1::text::uuid`,
		failed.ID, base); execErr != nil {
		testInstance.Fatalf("set release failure: %v", execErr)
	}
	clearHeldAt(ctx, testInstance, pool, failed.ID)

	summary, err := store.HeldSummary(ctx)
	if err != nil {
		testInstance.Fatalf("HeldSummary: %v", err)
	}
	want := []message.HeldAppSummary{
		{AppID: "alpha", Count: 3, OldestHeldAt: base.Add(-10 * time.Minute)},
		{AppID: "u4i", Count: 3, OldestHeldAt: base.Add(-3 * time.Hour)},
		{AppID: "billing", Count: 2, OldestHeldAt: base.Add(-time.Hour)},
	}
	if len(summary) != len(want) {
		testInstance.Fatalf("HeldSummary = %+v, want %+v", summary, want)
	}
	for index, wantRow := range want {
		got := summary[index]
		if got.AppID != wantRow.AppID || got.Count != wantRow.Count || !got.OldestHeldAt.Equal(wantRow.OldestHeldAt) {
			testInstance.Errorf("row %d = %+v, want %+v", index, got, wantRow)
		}
	}
}

func TestHeldSummaryEmptyIsNonNil(testInstance *testing.T) {
	ctx, store, _, _ := newMessageStore(testInstance)

	summary, err := store.HeldSummary(ctx)
	if err != nil {
		testInstance.Fatalf("HeldSummary: %v", err)
	}
	if summary == nil || len(summary) != 0 {
		testInstance.Fatalf("HeldSummary = %#v, want an empty non-nil slice", summary)
	}
}
