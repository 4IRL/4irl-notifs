//go:build integration

package publisher_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/publisher"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const (
	pgStoreTestBudget   = 30 * time.Second
	sqlStateCheckFailed = "23514"
)

func newTestStore(testInstance *testing.T) (context.Context, *publisher.PGStore, *pgxpool.Pool) {
	testInstance.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), pgStoreTestBudget)
	testInstance.Cleanup(cancel)
	pool := pgtest.IsolatedPool(ctx, testInstance)
	return ctx, publisher.NewPGStore(pool), pool
}

func samplePublisher(appID string, sealed []byte) publisher.StoredPublisher {
	return publisher.StoredPublisher{
		AppID: appID, PublisherUserID: "app-" + appID, TopicPattern: appID + "-*", SealedToken: sealed,
	}
}

func TestPGStoreUpsertGetRoundTrip(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	inserted, upsertErr := store.Upsert(ctx, samplePublisher("urls4irl", []byte{0x01, 0xAA, 0xBB}))
	if upsertErr != nil {
		testInstance.Fatalf("Upsert: %v", upsertErr)
	}
	if inserted.CreatedAt.IsZero() || inserted.UpdatedAt.IsZero() {
		testInstance.Errorf("timestamps not set: %+v", inserted)
	}

	got, getErr := store.Get(ctx, "urls4irl")
	if getErr != nil {
		testInstance.Fatalf("Get: %v", getErr)
	}
	if got.AppID != "urls4irl" || got.PublisherUserID != "app-urls4irl" || got.TopicPattern != "urls4irl-*" {
		testInstance.Errorf("got = %+v", got)
	}
	if !bytes.Equal(got.SealedToken, []byte{0x01, 0xAA, 0xBB}) {
		testInstance.Errorf("sealed token = %x, want 01aabb", got.SealedToken)
	}
}

func TestPGStoreUpsertReplacesExistingRow(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)

	first, firstErr := store.Upsert(ctx, samplePublisher("urls4irl", []byte{0x01}))
	if firstErr != nil {
		testInstance.Fatalf("first Upsert: %v", firstErr)
	}
	replacement := samplePublisher("urls4irl", []byte{0x02, 0x03})
	replacement.PublisherUserID = "app-urls4irl-v2"
	replacement.TopicPattern = "urls4irl-v2-*"
	second, secondErr := store.Upsert(ctx, replacement)
	if secondErr != nil {
		testInstance.Fatalf("second Upsert: %v", secondErr)
	}

	if !bytes.Equal(second.SealedToken, []byte{0x02, 0x03}) || second.PublisherUserID != "app-urls4irl-v2" ||
		second.TopicPattern != "urls4irl-v2-*" {
		testInstance.Errorf("second = %+v, want the replacement fields", second)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		testInstance.Errorf("created_at changed on replace: %v -> %v", first.CreatedAt, second.CreatedAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		testInstance.Errorf("updated_at not bumped: %v -> %v", first.UpdatedAt, second.UpdatedAt)
	}
	var rowCount int
	if countErr := pool.QueryRow(ctx, "SELECT count(*) FROM app_publishers").Scan(&rowCount); countErr != nil {
		testInstance.Fatalf("count: %v", countErr)
	}
	if rowCount != 1 {
		testInstance.Errorf("rows = %d, want 1", rowCount)
	}
}

func TestPGStoreRejectsInvalidAppID(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	_, upsertErr := store.Upsert(ctx, samplePublisher("Bad-App", []byte{0x01}))
	if upsertErr == nil {
		testInstance.Fatal("Upsert accepted app_id Bad-App")
	}
	// WrapDBError flattens a PgError to text, so the SQLSTATE is read from the message.
	var pgErr *pgconn.PgError
	if errors.As(upsertErr, &pgErr) {
		testInstance.Errorf("error still wraps *pgconn.PgError (row content could leak): %v", upsertErr)
	}
	if !bytes.Contains([]byte(upsertErr.Error()), []byte("SQLSTATE "+sqlStateCheckFailed)) {
		testInstance.Errorf("error = %v, want a check violation (SQLSTATE %s)", upsertErr, sqlStateCheckFailed)
	}
}

func TestPGStoreGetMissIsErrNoPublisher(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	if _, getErr := store.Get(ctx, "ghost"); !errors.Is(getErr, registry.ErrNoPublisher) {
		testInstance.Errorf("Get err = %v, want ErrNoPublisher", getErr)
	}
}
