//go:build integration

package readiness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/heartbeat"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/httpapi"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/migrate"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/pgtest"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/seal"
)

// heldReadyzBody is the decoded /readyz fields these tests assert on.
type heldReadyzBody struct {
	Status string `json:"status"`
	Held   struct {
		Available bool `json:"available"`
		Total     int  `json:"total"`
		Messages  struct {
			Count int `json:"count"`
		} `json:"messages"`
		UnassignedAlerts struct {
			Count int `json:"count"`
		} `json:"unassigned_alerts"`
	} `json:"held"`
}

// newHeldStores builds the message and heartbeat stores on pool.
func newHeldStores(testInstance *testing.T, pool *pgxpool.Pool) (*message.PGStore, *heartbeat.PGStore) {
	testInstance.Helper()
	sealer, sealErr := seal.New([]byte("0123456789abcdef0123456789abcdef"))
	if sealErr != nil {
		testInstance.Fatalf("seal.New: %v", sealErr)
	}
	return message.NewPGStore(pool), heartbeat.NewPGStore(pool, sealer, map[string]bool{registry.TypeWebhook: true})
}

// serveReadyz serves GET /readyz through a real server around checker and held.
func serveReadyz(testInstance *testing.T, checker *Checker, held httpapi.HeldReporter) (int, string, heldReadyzBody) {
	testInstance.Helper()
	server := httpapi.NewServer(httpapi.ServerConfig{Readiness: checker, Held: held})
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var body heldReadyzBody
	if decodeErr := json.Unmarshal(recorder.Body.Bytes(), &body); decodeErr != nil {
		testInstance.Fatalf("decode body %q: %v", recorder.Body.String(), decodeErr)
	}
	return recorder.Code, recorder.Body.String(), body
}

func TestReadyzReportsHeldMessageAndUnassignedAlert(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestBudget)
	defer cancel()
	pool := pgtest.IsolatedPool(ctx, testInstance)
	messageStore, heartbeatStore := newHeldStores(testInstance, pool)

	heldAt := time.Now().UTC().Add(-time.Hour)
	_, _, holdErr := messageStore.InsertHeldMessage(ctx, message.Envelope{
		AppID: "billing", Channel: "alerts", IdempotencyKey: "held-1", Payload: json.RawMessage(`{"k":"v"}`),
	}, message.HeldCaps{PerApp: 10, Total: 10}, heldAt)
	if holdErr != nil {
		testInstance.Fatalf("hold message: %v", holdErr)
	}
	// A check with no destination and a pending alert (the API cannot create one).
	if _, execErr := pool.Exec(ctx,
		`INSERT INTO heartbeat_checks (app_id, name, period_seconds, grace_seconds, anchor_at, created_at, updated_at, pending_alert)
VALUES ('billing', 'zero_dest', 600, 300, $1, $1, $1, 'down')`, heldAt); execErr != nil {
		testInstance.Fatalf("seed zero-destination check: %v", execErr)
	}

	checker := NewChecker(pool, migrate.NewPGStore(pool), expectedVersion(testInstance))
	held := NewHeldCache(HeldConfig{Messages: messageStore, Alerts: heartbeatStore})

	code, raw, body := serveReadyz(testInstance, checker, held)

	if code != http.StatusOK || body.Status != "ok" {
		testInstance.Fatalf("code, status = %d, %q, want 200, ok (body %s)", code, body.Status, raw)
	}
	if !body.Held.Available || body.Held.Messages.Count != 1 || body.Held.UnassignedAlerts.Count != 1 || body.Held.Total != 2 {
		testInstance.Fatalf("held = %+v, want available with 1 message, 1 alert, total 2 (body %s)", body.Held, raw)
	}
}

func TestReadyzHeldUnavailableWhenCountPoolIsClosed(testInstance *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), integrationTestBudget)
	defer cancel()
	livePool := pgtest.IsolatedPool(ctx, testInstance)
	// A second pool on the same isolated schema: the tables are visible, so
	// only the closure can make the count queries fail.
	countPool, countPoolErr := pgxpool.NewWithConfig(ctx, livePool.Config().Copy())
	if countPoolErr != nil {
		testInstance.Fatalf("open second pool: %v", countPoolErr)
	}
	testInstance.Cleanup(countPool.Close)
	messageStore, heartbeatStore := newHeldStores(testInstance, countPool)
	// Positive control: the same stores answer while the pool is open, so the
	// later failure is the closure and not missing tables.
	if control := NewHeldCache(HeldConfig{Messages: messageStore, Alerts: heartbeatStore}).HeldSnapshot(ctx); !control.Available {
		testInstance.Fatal("control snapshot unavailable with the second pool open; tables are not visible to it")
	}
	// A fresh cache so the control's cached success cannot mask the failure.
	held := NewHeldCache(HeldConfig{Messages: messageStore, Alerts: heartbeatStore})
	countPool.Close()

	checker := NewChecker(livePool, migrate.NewPGStore(livePool), expectedVersion(testInstance))
	code, raw, body := serveReadyz(testInstance, checker, held)

	if code != http.StatusOK || body.Status != "ok" {
		testInstance.Fatalf("code, status = %d, %q, want 200, ok (body %s)", code, body.Status, raw)
	}
	if body.Held.Available {
		testInstance.Fatalf("held.available = true, want false (body %s)", raw)
	}
	const wantHeld = `"held":{"available":false}`
	if !strings.Contains(raw, wantHeld) {
		testInstance.Fatalf("body %s does not contain %s", raw, wantHeld)
	}
}
