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

// upsertViaLock stores the publisher through WithAppMintLock and MintTx.Upsert, the only write path.
func upsertViaLock(
	ctx context.Context, store *publisher.PGStore, row publisher.StoredPublisher,
) (publisher.StoredPublisher, error) {
	var stored publisher.StoredPublisher
	lockErr := store.WithAppMintLock(ctx, row.AppID, func(tx publisher.MintTx) error {
		var upsertErr error
		stored, upsertErr = tx.Upsert(ctx, row)
		return upsertErr
	})
	return stored, lockErr
}

func TestPGStoreUpsertGetRoundTrip(testInstance *testing.T) {
	ctx, store, _ := newTestStore(testInstance)

	inserted, upsertErr := upsertViaLock(ctx, store, samplePublisher("urls4irl", []byte{0x01, 0xAA, 0xBB}))
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

	first, firstErr := upsertViaLock(ctx, store, samplePublisher("urls4irl", []byte{0x01}))
	if firstErr != nil {
		testInstance.Fatalf("first Upsert: %v", firstErr)
	}
	replacement := samplePublisher("urls4irl", []byte{0x02, 0x03})
	replacement.PublisherUserID = "app-urls4irl-v2"
	replacement.TopicPattern = "urls4irl-v2-*"
	second, secondErr := upsertViaLock(ctx, store, replacement)
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

	_, upsertErr := upsertViaLock(ctx, store, samplePublisher("Bad-App", []byte{0x01}))
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

func TestPGStoreDeleteRemovesOnlyTheNamedAppAndIsIdempotent(testInstance *testing.T) {
	ctx, store, pool := newTestStore(testInstance)
	for _, appID := range []string{"urls4irl", "chores4irl"} {
		if _, upsertErr := upsertViaLock(ctx, store, samplePublisher(appID, []byte{0x01})); upsertErr != nil {
			testInstance.Fatalf("Upsert %s: %v", appID, upsertErr)
		}
	}

	if deleteErr := store.Delete(ctx, "urls4irl"); deleteErr != nil {
		testInstance.Fatalf("Delete: %v", deleteErr)
	}

	if _, getErr := store.Get(ctx, "urls4irl"); !errors.Is(getErr, registry.ErrNoPublisher) {
		testInstance.Errorf("Get after Delete err = %v, want ErrNoPublisher", getErr)
	}
	if _, getErr := store.Get(ctx, "chores4irl"); getErr != nil {
		testInstance.Errorf("Delete removed another app's publisher: %v", getErr)
	}
	if deleteErr := store.Delete(ctx, "urls4irl"); deleteErr != nil {
		testInstance.Errorf("second Delete of the same app: %v, want nil (idempotent)", deleteErr)
	}
	if deleteErr := store.Delete(ctx, "ghost"); deleteErr != nil {
		testInstance.Errorf("Delete of an app with no publisher: %v, want nil", deleteErr)
	}
	var rowCount int
	if countErr := pool.QueryRow(ctx, "SELECT count(*) FROM app_publishers").Scan(&rowCount); countErr != nil {
		testInstance.Fatalf("count: %v", countErr)
	}
	if rowCount != 1 {
		testInstance.Errorf("rows = %d, want 1", rowCount)
	}
}
