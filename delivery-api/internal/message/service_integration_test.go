//go:build integration

package message_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/4IRL/4irl-notifs/delivery-api/internal/message"
	"github.com/4IRL/4irl-notifs/delivery-api/internal/registry"
)

// staticResolver resolves every envelope to the one destination it was built with.
type staticResolver struct {
	destination registry.ResolvedDestination
}

func (resolver staticResolver) Resolve(context.Context, string, string, string) ([]registry.ResolvedDestination, error) {
	return []registry.ResolvedDestination{resolver.destination}, nil
}

// newRealService returns a Service over the real Postgres store with one seeded
// destination.
func newRealService(testInstance *testing.T) (context.Context, *message.Service, *pgxpool.Pool) {
	testInstance.Helper()
	ctx, store, registryStore, pool := newMessageStore(testInstance)
	target := seedTarget(ctx, testInstance, registryStore, "pager")
	service := message.NewService(message.ServiceConfig{
		Store: store,
		Resolver: staticResolver{destination: registry.ResolvedDestination{
			ID: *target.DestinationID, Name: target.DestinationName, Type: target.DestinationType,
		}},
		SupportedTypes: map[string]bool{registry.TypeNtfy: true},
	})
	return ctx, service, pool
}

// TestServiceReadsAreScopedToTheCallingApp proves, against the real store, that
// one app can neither read nor look up another app's message, and that the
// answer for a foreign message is identical to the answer for an absent one.
func TestServiceReadsAreScopedToTheCallingApp(testInstance *testing.T) {
	ctx, service, _ := newRealService(testInstance)
	envelope := sampleEnvelope("scoped-key")

	stored, outcome, submitErr := service.Submit(ctx, envelope)
	if submitErr != nil || outcome.Duplicate {
		testInstance.Fatalf("Submit = (%v, outcome %+v)", submitErr, outcome)
	}

	if own, err := service.Get(ctx, "u4i", stored.ID); err != nil || own.ID != stored.ID {
		testInstance.Fatalf("Get as the owning app = (%+v, %v), want the message", own, err)
	}
	if own, err := service.GetByKey(ctx, "u4i", "scoped-key"); err != nil || own.ID != stored.ID {
		testInstance.Fatalf("GetByKey as the owning app = (%+v, %v), want the message", own, err)
	}

	foreign, foreignErr := service.Get(ctx, "other_app", stored.ID)
	_, absentErr := service.Get(ctx, "other_app", "00000000-0000-4000-8000-000000000000")
	if !errors.Is(foreignErr, message.ErrMessageNotFound) || foreignErr.Error() != absentErr.Error() {
		testInstance.Fatalf("Get foreign = %v, absent = %v; want identical ErrMessageNotFound", foreignErr, absentErr)
	}
	if foreign.ID != "" || len(foreign.Deliveries) != 0 {
		testInstance.Fatalf("Get foreign leaked %+v", foreign)
	}
	if _, err := service.Await(ctx, "other_app", stored.ID, 0); !errors.Is(err, message.ErrMessageNotFound) {
		testInstance.Fatalf("Await foreign error = %v, want ErrMessageNotFound", err)
	}
	if _, err := service.GetByKey(ctx, "other_app", "scoped-key"); !errors.Is(err, message.ErrMessageNotFound) {
		testInstance.Fatalf("GetByKey foreign error = %v, want ErrMessageNotFound", err)
	}

	// The same key submitted by another app is its own message, not a duplicate.
	otherEnvelope := sampleEnvelope("scoped-key")
	otherEnvelope.AppID = "other_app"
	other, otherOutcome, otherErr := service.Submit(ctx, otherEnvelope)
	if otherErr != nil || otherOutcome.Duplicate || other.ID == stored.ID {
		testInstance.Fatalf("Submit as another app = (%+v, outcome %+v, %v), want a distinct new message", other, otherOutcome, otherErr)
	}
}

// TestServiceResubmitReturnsTheOriginalAndIgnoresNewContent proves the
// idempotent-resubmit semantics end to end.
func TestServiceResubmitReturnsTheOriginalAndIgnoresNewContent(testInstance *testing.T) {
	ctx, service, _ := newRealService(testInstance)
	first, _, firstErr := service.Submit(ctx, sampleEnvelope("resubmit-key"))
	if firstErr != nil {
		testInstance.Fatalf("first Submit error = %v", firstErr)
	}

	changed := sampleEnvelope("resubmit-key")
	changed.Channel = "changed"
	changed.Critical = false
	changed.Payload = json.RawMessage(`{"different":true}`)
	second, secondOutcome, secondErr := service.Submit(ctx, changed)
	if secondErr != nil || !secondOutcome.Duplicate {
		testInstance.Fatalf("second Submit = (%v, outcome %+v), want a duplicate", secondErr, secondOutcome)
	}
	if second.ID != first.ID || second.Envelope.Channel != "alerts" || !second.Envelope.Critical {
		testInstance.Fatalf("second = %+v, want the original %+v", second.Envelope, first.Envelope)
	}
}

// TestPayloadNumberLimitsMatchPostgresJSONB pins validate.go's numeric model to
// the real database: a payload the validator accepts must store, and the exact
// boundary literals it rejects must be ones Postgres would also refuse.
func TestPayloadNumberLimitsMatchPostgresJSONB(testInstance *testing.T) {
	ctx, service, pool := newRealService(testInstance)

	acceptedLiterals := []string{
		"1.5e-7", "1e308", "5e-324", "1e131071", "12e131070", "1e-16383",
		"0." + strings.Repeat("0", 16382) + "1", strings.Repeat("9", 100000),
	}
	for index, literal := range acceptedLiterals {
		envelope := sampleEnvelope(fmt.Sprintf("number-accepted-%d", index))
		envelope.Payload = json.RawMessage(`{"a":` + literal + `}`)
		if _, _, err := service.Submit(ctx, envelope); err != nil {
			testInstance.Fatalf("Submit with an accepted literal (%.40s...) error = %v", literal, err)
		}
	}

	rejectedLiterals := []string{
		"1e999999", "1e131072", "12e131071", "1e-16384", "0.12e-16382", "1e-999999",
		"0." + strings.Repeat("0", 16383) + "1",
	}
	for _, literal := range rejectedLiterals {
		payload := json.RawMessage(`{"a":` + literal + `}`)
		if err := message.ValidatePayload(payload); err == nil {
			testInstance.Fatalf("ValidatePayload accepted %.40s..., want a rejection", literal)
		}
		if _, execErr := pool.Exec(ctx, `SELECT $1::text::jsonb`, string(payload)); execErr == nil {
			testInstance.Fatalf("Postgres stored %.40s...: the validator is stricter than needed, tighten the model", literal)
		}
	}
}
