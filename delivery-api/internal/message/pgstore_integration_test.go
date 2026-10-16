//go:build integration

package message_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

const messageStoreTestBudget = 60 * time.Second

// newMessageStore returns a PGStore over a fresh isolated schema, plus the
// registry store for seeding destinations and the pool for assertions that
// bypass the store.
func newMessageStore(
	testInstance *testing.T,
) (context.Context, *message.PGStore, *registry.PGStore, *pgxpool.Pool) {
	testInstance.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), messageStoreTestBudget)
	testInstance.Cleanup(cancel)
	pool := pgtest.IsolatedPool(ctx, testInstance)
	return ctx, message.NewPGStore(pool), registry.NewPGStore(pool), pool
}

func seedTarget(
	ctx context.Context, testInstance *testing.T, registryStore *registry.PGStore, name string,
) message.DeliveryTarget {
	testInstance.Helper()
	destination, err := registryStore.InsertDestination(ctx, registry.StoredDestination{
		Name:          name,
		Type:          registry.TypeNtfy,
		Config:        map[string]string{"topic": "alerts_" + name},
		SealedSecrets: []byte{0x01, 0xAA, 0xBB},
		Enabled:       true,
	})
	if err != nil {
		testInstance.Fatalf("insert destination %s: %v", name, err)
	}
	return message.DeliveryTarget{
		DestinationID:   destination.ID,
		DestinationName: destination.Name,
		DestinationType: destination.Type,
	}
}

func sampleEnvelope(idempotencyKey string) message.Envelope {
	return message.Envelope{
		AppID:          "u4i",
		Channel:        "alerts",
		IdempotencyKey: idempotencyKey,
		ToDestination:  "pager",
		Critical:       true,
		Payload:        json.RawMessage(`{"kind":"deploy","count":3}`),
		Display: &message.Display{
			Title:    "Deploy finished",
			Body:     "All green",
			URL:      "https://example.com/run/1",
			Priority: "high",
		},
	}
}

func countRows(ctx context.Context, testInstance *testing.T, pool *pgxpool.Pool, table string) int {
	testInstance.Helper()
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
		testInstance.Fatalf("count %s: %v", table, err)
	}
	return count
}

func TestInsertThenGetRoundTripsEveryEnvelopeField(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")
	deliverAt := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	envelope := sampleEnvelope("deploy-1")
	envelope.DeliverAt = &deliverAt

	inserted, duplicate, err := store.InsertMessage(ctx, envelope, []message.DeliveryTarget{pager, audit})
	if err != nil {
		testInstance.Fatalf("InsertMessage: %v", err)
	}
	if duplicate {
		testInstance.Fatal("first insert reported duplicate")
	}

	for name, load := range map[string]func() (message.StoredMessage, error){
		"GetMessage":      func() (message.StoredMessage, error) { return store.GetMessage(ctx, inserted.ID) },
		"GetMessageByKey": func() (message.StoredMessage, error) { return store.GetMessageByKey(ctx, "u4i", "deploy-1") },
	} {
		got, loadErr := load()
		if loadErr != nil {
			testInstance.Fatalf("%s: %v", name, loadErr)
		}
		if got.ID != inserted.ID || got.ID == "" {
			testInstance.Errorf("%s id = %q, want %q", name, got.ID, inserted.ID)
		}
		gotEnvelope := got.Envelope
		if gotEnvelope.AppID != "u4i" || gotEnvelope.Channel != "alerts" ||
			gotEnvelope.IdempotencyKey != "deploy-1" || gotEnvelope.ToDestination != "pager" ||
			gotEnvelope.ToRecipient != "" || !gotEnvelope.Critical {
			testInstance.Errorf("%s envelope scalars = %+v", name, gotEnvelope)
		}
		if gotEnvelope.DeliverAt == nil || !gotEnvelope.DeliverAt.Equal(deliverAt) {
			testInstance.Errorf("%s deliver_at = %v, want %v", name, gotEnvelope.DeliverAt, deliverAt)
		}
		var gotPayload, wantPayload map[string]any
		if unmarshalErr := json.Unmarshal(gotEnvelope.Payload, &gotPayload); unmarshalErr != nil {
			testInstance.Fatalf("%s payload not JSON: %v", name, unmarshalErr)
		}
		if unmarshalErr := json.Unmarshal(envelope.Payload, &wantPayload); unmarshalErr != nil {
			testInstance.Fatalf("decode want payload: %v", unmarshalErr)
		}
		if !reflect.DeepEqual(gotPayload, wantPayload) {
			testInstance.Errorf("%s payload = %v, want %v", name, gotPayload, wantPayload)
		}
		if gotEnvelope.Display == nil || *gotEnvelope.Display != *envelope.Display {
			testInstance.Errorf("%s display = %+v, want %+v", name, gotEnvelope.Display, envelope.Display)
		}
		if got.CreatedAt.IsZero() {
			testInstance.Errorf("%s created_at is zero", name)
		}

		if len(got.Deliveries) != 2 {
			testInstance.Fatalf("%s deliveries = %d, want 2", name, len(got.Deliveries))
		}
		// Ordered by destination name: audit before pager.
		for index, target := range []message.DeliveryTarget{audit, pager} {
			delivery := got.Deliveries[index]
			if delivery.DestinationName != target.DestinationName ||
				delivery.DestinationType != target.DestinationType ||
				delivery.DestinationID == nil || *delivery.DestinationID != target.DestinationID {
				testInstance.Errorf("%s delivery[%d] = %+v, want target %+v", name, index, delivery, target)
			}
			if delivery.MessageID != inserted.ID || delivery.ID == 0 {
				testInstance.Errorf("%s delivery[%d] ids = %+v", name, index, delivery)
			}
			if delivery.State != message.StatePending || delivery.DeadReason != nil ||
				delivery.AttemptCount != 0 || delivery.BudgetAttempts != 0 ||
				delivery.LastStatusCode != nil || delivery.DeliveredAt != nil {
				testInstance.Errorf("%s delivery[%d] initial state = %+v", name, index, delivery)
			}
			if delivery.BudgetStartedAt.IsZero() || delivery.NextAttemptAt.IsZero() ||
				delivery.CreatedAt.IsZero() || delivery.UpdatedAt.IsZero() {
				testInstance.Errorf("%s delivery[%d] timestamps unset = %+v", name, index, delivery)
			}
		}
	}
	if status := message.DeriveStatus(inserted.Deliveries); status != message.StatusPending {
		testInstance.Errorf("status of a fresh message = %q, want pending", status)
	}
}

func TestInsertWithToOmittedReadsBackEmptyStrings(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	target := seedTarget(ctx, testInstance, registryStore, "pager")
	envelope := sampleEnvelope("no-to")
	envelope.ToDestination = ""
	envelope.ToRecipient = ""
	envelope.Display = nil
	envelope.DeliverAt = nil
	envelope.Critical = false

	inserted, _, err := store.InsertMessage(ctx, envelope, []message.DeliveryTarget{target})
	if err != nil {
		testInstance.Fatalf("InsertMessage with to omitted: %v", err)
	}
	got, err := store.GetMessage(ctx, inserted.ID)
	if err != nil {
		testInstance.Fatalf("GetMessage: %v", err)
	}
	if got.Envelope.ToDestination != "" || got.Envelope.ToRecipient != "" {
		testInstance.Errorf("to = %q/%q, want empty strings", got.Envelope.ToDestination, got.Envelope.ToRecipient)
	}
	if got.Envelope.Display != nil || got.Envelope.DeliverAt != nil || got.Envelope.Critical {
		testInstance.Errorf("optional fields = display %v deliver_at %v critical %v, want unset",
			got.Envelope.Display, got.Envelope.DeliverAt, got.Envelope.Critical)
	}
}

func TestInsertWithRecipientOnlyRoundTrips(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	target := seedTarget(ctx, testInstance, registryStore, "pager")
	envelope := sampleEnvelope("recipient-only")
	envelope.ToDestination = ""
	envelope.ToRecipient = "someone"

	inserted, _, err := store.InsertMessage(ctx, envelope, []message.DeliveryTarget{target})
	if err != nil {
		testInstance.Fatalf("InsertMessage with recipient only: %v", err)
	}
	got, err := store.GetMessage(ctx, inserted.ID)
	if err != nil {
		testInstance.Fatalf("GetMessage: %v", err)
	}
	if got.Envelope.ToRecipient != "someone" || got.Envelope.ToDestination != "" {
		testInstance.Errorf("to = %q/%q, want empty destination and recipient %q",
			got.Envelope.ToDestination, got.Envelope.ToRecipient, "someone")
	}
}

// jsonbRejectionStates are the SQLSTATEs Postgres raises for JSON text jsonb
// cannot store: unsupported \u0000 escape (22P05), lone surrogate escape (22P02)
// and invalid UTF-8 (22021).
var jsonbRejectionStates = []string{"22P05", "22P02", "22021"}

func TestValidatedPayloadsNeverReachADatabaseError(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	target := seedTarget(ctx, testInstance, registryStore, "pager")

	rejectedPayloads := map[string]string{
		"NUL escape":          `{"a":"x\u0000y"}`,
		"NUL escape in key":   `{"a\u0000":1}`,
		"lone high surrogate": `{"a":"\ud800"}`,
		"lone low surrogate":  `{"a":"\udc00"}`,
		"broken pair":         `{"a":"\ud800A"}`,
		"invalid UTF-8":       "{\"a\":\"\xff\"}",
	}
	for name, payload := range rejectedPayloads {
		testInstance.Run("rejected payload "+name, func(subTest *testing.T) {
			if validateErr := message.ValidatePayload(json.RawMessage(payload)); validateErr == nil {
				subTest.Fatal("ValidatePayload accepted a payload jsonb cannot store")
			}
			_, execErr := pool.Exec(ctx, "SELECT $1::text::jsonb", payload)
			assertJSONBRejection(subTest, execErr)
		})
	}

	nulDisplay := message.Display{Title: "bad\x00title"}
	testInstance.Run("rejected display NUL", func(subTest *testing.T) {
		if validateErr := message.ValidateDisplay(&nulDisplay); validateErr == nil {
			subTest.Fatal("ValidateDisplay accepted a NUL title")
		}
		encoded, marshalErr := json.Marshal(nulDisplay)
		if marshalErr != nil {
			subTest.Fatalf("marshal display: %v", marshalErr)
		}
		_, execErr := pool.Exec(ctx, "SELECT $1::text::jsonb", string(encoded))
		assertJSONBRejection(subTest, execErr)
	})

	acceptedPayloads := map[string]string{
		"surrogate pair":          `{"a":"😀"}`,
		"escaped backslash u0000": `{"a":"\\u0000"}`,
		"multibyte":               `{"a":"é😀"}`,
		"duplicate keys":          `{"a":1,"a":2}`,
	}
	for name, payload := range acceptedPayloads {
		testInstance.Run("accepted payload "+name, func(subTest *testing.T) {
			envelope := sampleEnvelope("accepted-" + strings.ReplaceAll(name, " ", "-"))
			envelope.Payload = json.RawMessage(payload)
			envelope.Display = &message.Display{Title: "café 😀", Body: "line one\nline two"}
			if validateErr := message.ValidateEnvelope(envelope); validateErr != nil {
				subTest.Fatalf("ValidateEnvelope rejected a storable envelope: %v", validateErr)
			}
			inserted, _, insertErr := store.InsertMessage(ctx, envelope, []message.DeliveryTarget{target})
			if insertErr != nil {
				subTest.Fatalf("InsertMessage of a validated envelope: %v", insertErr)
			}
			if inserted.Envelope.Display == nil || *inserted.Envelope.Display != *envelope.Display {
				subTest.Errorf("display = %+v, want %+v", inserted.Envelope.Display, envelope.Display)
			}
		})
	}
}

// assertJSONBRejection fails the test unless err is a Postgres jsonb rejection.
func assertJSONBRejection(testInstance *testing.T, err error) {
	testInstance.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		testInstance.Fatalf("error = %v, want a *pgconn.PgError jsonb rejection", err)
	}
	if !slices.Contains(jsonbRejectionStates, pgErr.Code) {
		testInstance.Fatalf("SQLSTATE = %s, want one of %v", pgErr.Code, jsonbRejectionStates)
	}
}

func TestInsertDuplicateKeyReturnsOriginalAndInsertsNothing(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	pager := seedTarget(ctx, testInstance, registryStore, "pager")
	audit := seedTarget(ctx, testInstance, registryStore, "audit")

	first, duplicate, err := store.InsertMessage(ctx, sampleEnvelope("dup"), []message.DeliveryTarget{pager})
	if err != nil || duplicate {
		testInstance.Fatalf("first insert: duplicate=%v err=%v", duplicate, err)
	}

	changed := sampleEnvelope("dup")
	changed.Channel = "other"
	changed.Payload = json.RawMessage(`{"kind":"changed"}`)
	second, duplicate, err := store.InsertMessage(ctx, changed, []message.DeliveryTarget{pager, audit})
	if err != nil {
		testInstance.Fatalf("second insert: %v", err)
	}
	if !duplicate {
		testInstance.Fatal("second insert with the same key did not report duplicate")
	}
	if second.ID != first.ID {
		testInstance.Errorf("duplicate id = %q, want %q", second.ID, first.ID)
	}
	if second.Envelope.Channel != "alerts" {
		testInstance.Errorf("duplicate channel = %q, want the original %q", second.Envelope.Channel, "alerts")
	}
	if !reflect.DeepEqual(second.Deliveries, first.Deliveries) {
		testInstance.Errorf("duplicate deliveries = %+v, want unchanged %+v", second.Deliveries, first.Deliveries)
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != 1 {
		testInstance.Errorf("messages rows = %d, want 1", got)
	}
	if got := countRows(ctx, testInstance, pool, "message_deliveries"); got != 1 {
		testInstance.Errorf("message_deliveries rows = %d, want 1", got)
	}

	// The same key under a different app is a different message.
	otherApp := sampleEnvelope("dup")
	otherApp.AppID = "other_app"
	if _, otherDuplicate, otherErr := store.InsertMessage(
		ctx, otherApp, []message.DeliveryTarget{pager},
	); otherErr != nil || otherDuplicate {
		testInstance.Errorf("same key, other app: duplicate=%v err=%v, want a new message", otherDuplicate, otherErr)
	}
}

func TestConcurrentInsertSameKeyProducesOneMessage(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	target := seedTarget(ctx, testInstance, registryStore, "pager")

	const racers = 8
	type outcome struct {
		stored    message.StoredMessage
		duplicate bool
		err       error
	}
	outcomes := make([]outcome, racers)
	start := make(chan struct{})
	var ready, finished sync.WaitGroup
	ready.Add(racers)
	finished.Add(racers)
	for index := range racers {
		go func() {
			defer finished.Done()
			ready.Done()
			<-start
			stored, duplicate, err := store.InsertMessage(
				ctx, sampleEnvelope("race"), []message.DeliveryTarget{target})
			outcomes[index] = outcome{stored: stored, duplicate: duplicate, err: err}
		}()
	}
	ready.Wait()
	close(start)
	finished.Wait()

	duplicates := 0
	for index, result := range outcomes {
		if result.err != nil {
			testInstance.Fatalf("racer %d: %v", index, result.err)
		}
		if result.stored.ID != outcomes[0].stored.ID {
			testInstance.Errorf("racer %d id = %q, want %q", index, result.stored.ID, outcomes[0].stored.ID)
		}
		if len(result.stored.Deliveries) != 1 {
			testInstance.Errorf("racer %d deliveries = %d, want 1", index, len(result.stored.Deliveries))
		}
		if result.duplicate {
			duplicates++
		}
	}
	if duplicates != racers-1 {
		testInstance.Errorf("duplicates = %d, want %d (exactly one winner)", duplicates, racers-1)
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != 1 {
		testInstance.Errorf("messages rows = %d, want 1", got)
	}
	if got := countRows(ctx, testInstance, pool, "message_deliveries"); got != 1 {
		testInstance.Errorf("message_deliveries rows = %d, want 1", got)
	}
}

func TestInsertWithoutTargetsIsRejectedAndStoresNothing(testInstance *testing.T) {
	ctx, store, _, pool := newMessageStore(testInstance)

	if _, _, err := store.InsertMessage(ctx, sampleEnvelope("no-targets"), nil); err == nil {
		testInstance.Fatal("a message with no delivery targets must be rejected")
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != 0 {
		testInstance.Errorf("messages rows = %d, want 0", got)
	}
}

func TestInsertDeliveryFailureRollsBackTheMessage(testInstance *testing.T) {
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	target := seedTarget(ctx, testInstance, registryStore, "pager")

	// The same destination twice violates UNIQUE (message_id, destination_name)
	// after the message row was already inserted.
	_, _, err := store.InsertMessage(ctx, sampleEnvelope("rollback"), []message.DeliveryTarget{target, target})
	if err == nil {
		testInstance.Fatal("duplicate delivery targets must fail")
	}
	if got := countRows(ctx, testInstance, pool, "messages"); got != 0 {
		testInstance.Errorf("messages rows = %d after a failed fan-out, want 0 (atomic)", got)
	}
}

func TestGetMessageMisses(testInstance *testing.T) {
	ctx, store, _, _ := newMessageStore(testInstance)

	cases := []struct {
		name string
		load func() (message.StoredMessage, error)
	}{
		{"unknown key", func() (message.StoredMessage, error) { return store.GetMessageByKey(ctx, "u4i", "absent") }},
		{"absent uuid", func() (message.StoredMessage, error) {
			return store.GetMessage(ctx, "00000000-0000-4000-8000-000000000000")
		}},
		{"malformed id", func() (message.StoredMessage, error) { return store.GetMessage(ctx, "not-a-uuid") }},
		{"empty id", func() (message.StoredMessage, error) { return store.GetMessage(ctx, "") }},
		{"sql-looking id", func() (message.StoredMessage, error) { return store.GetMessage(ctx, "' OR 1=1 --") }},
	}
	for _, testCase := range cases {
		testInstance.Run(testCase.name, func(testInstance *testing.T) {
			if _, err := testCase.load(); !errors.Is(err, message.ErrMessageNotFound) {
				testInstance.Errorf("error = %v, want ErrMessageNotFound", err)
			}
		})
	}
}

func TestFindMessageByKeyReportsPresenceWithoutError(testInstance *testing.T) {
	ctx, store, registryStore, _ := newMessageStore(testInstance)
	target := seedTarget(ctx, testInstance, registryStore, "pager")
	inserted, _, err := store.InsertMessage(ctx, sampleEnvelope("present"), []message.DeliveryTarget{target})
	if err != nil {
		testInstance.Fatalf("InsertMessage: %v", err)
	}

	found, ok, err := store.FindMessageByKey(ctx, "u4i", "present")
	if err != nil || !ok {
		testInstance.Fatalf("existing key: found=%v err=%v, want found with no error", ok, err)
	}
	if found.ID != inserted.ID || len(found.Deliveries) != 1 {
		testInstance.Errorf("found = %+v, want id %q with 1 delivery", found, inserted.ID)
	}

	missing, ok, err := store.FindMessageByKey(ctx, "u4i", "absent")
	if err != nil {
		testInstance.Fatalf("absent key returned error %v, want nil (not ErrMessageNotFound)", err)
	}
	if ok || missing.ID != "" {
		testInstance.Errorf("absent key: found=%v id=%q, want not found", ok, missing.ID)
	}
}
