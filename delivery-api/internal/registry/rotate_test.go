package registry

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"maps"
	"strings"
	"testing"
	"time"
)

var rotateTestNow = time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)

func newRotateFixture(t *testing.T) serviceFixture {
	t.Helper()
	fixture := newServiceFixture(t)
	fixture.service.now = func() time.Time { return rotateTestNow }
	return fixture
}

func TestRotateWebhookSecretShapeAndStoredState(t *testing.T) {
	fixture := newRotateFixture(t)
	fixture.createWebhook(t, "hook")

	view, newSecret, err := fixture.service.RotateWebhookSecret(context.Background(), "hook", 24)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if len(newSecret) != 43 || newSecret == testSigningSecret {
		t.Fatalf("new secret = %q (len %d)", newSecret, len(newSecret))
	}
	decoded, decodeErr := base64.RawURLEncoding.DecodeString(newSecret)
	if decodeErr != nil || len(decoded) != 32 {
		t.Fatalf("new secret is not 32 bytes of unpadded base64url: %v", decodeErr)
	}
	stored := fixture.openSecrets(t, "hook")
	if stored["signing_secret"] != newSecret {
		t.Fatal("returned plaintext is not the stored new signing_secret")
	}
	if stored["signing_secret_previous"] != testSigningSecret || stored["url"] != testWebhookURL {
		t.Fatalf("stored secrets = %v", stored)
	}
	wantExpiry := rotateTestNow.Add(24 * time.Hour).Format(time.RFC3339)
	if got := fixture.store.destinations["hook"].Config["previous_secret_expires_at"]; got != wantExpiry {
		t.Fatalf("expiry = %q, want %q", got, wantExpiry)
	}
	if view.Config["previous_secret_expires_at"] != wantExpiry {
		t.Fatalf("view config = %v", view.Config)
	}
	if view.Secrets["signing_secret"] != "****" || view.Secrets["signing_secret_previous"] != "****" {
		t.Fatalf("view secrets not masked: %v", view.Secrets)
	}
	if strings.Contains(fixture.logs.String(), newSecret) {
		t.Fatal("new secret appeared in the log")
	}
}

func TestRotateWebhookSecretRunsInsideTheStoreUpdate(t *testing.T) {
	fixture := newRotateFixture(t)
	fixture.createWebhook(t, "hook")
	fixture.store.calls = nil

	if _, _, err := fixture.service.RotateWebhookSecret(context.Background(), "hook", 1); err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if len(fixture.store.calls) != 1 || fixture.store.calls[0] != "UpdateDestination" {
		t.Fatalf("store calls = %v, want exactly one UpdateDestination (mutate path)", fixture.store.calls)
	}
}

func TestRotateWebhookSecretTwiceKeepsFirstSecretAsPrevious(t *testing.T) {
	fixture := newRotateFixture(t)
	fixture.createWebhook(t, "hook")

	_, first, err := fixture.service.RotateWebhookSecret(context.Background(), "hook", 24)
	if err != nil {
		t.Fatalf("first rotate: %v", err)
	}
	_, second, err := fixture.service.RotateWebhookSecret(context.Background(), "hook", 48)
	if err != nil {
		t.Fatalf("second rotate: %v", err)
	}

	stored := fixture.openSecrets(t, "hook")
	if first == second || stored["signing_secret_previous"] != first || stored["signing_secret"] != second {
		t.Fatalf("stored = %v, first = %q, second = %q", stored, first, second)
	}
	wantExpiry := rotateTestNow.Add(48 * time.Hour).Format(time.RFC3339)
	if got := fixture.store.destinations["hook"].Config["previous_secret_expires_at"]; got != wantExpiry {
		t.Fatalf("expiry = %q, want %q", got, wantExpiry)
	}
}

func TestRotateWebhookSecretKeepsDisabledDestinationDisabled(t *testing.T) {
	fixture := newRotateFixture(t)
	fixture.createWebhook(t, "hook")
	disabled := false
	if _, err := fixture.service.UpdateDestination(context.Background(), "hook", DestinationPatch{Enabled: &disabled}); err != nil {
		t.Fatalf("disable: %v", err)
	}

	view, _, err := fixture.service.RotateWebhookSecret(context.Background(), "hook", 24)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}

	if view.Enabled || fixture.store.destinations["hook"].Enabled {
		t.Fatal("rotation re-enabled a disabled destination")
	}
}

func TestRotateWebhookSecretErrorsWriteNothing(t *testing.T) {
	tests := map[string]struct {
		setup   func(t *testing.T, fixture serviceFixture)
		name    string
		wantErr error
	}{
		"ntfy is unsupported": {
			setup: func(t *testing.T, fixture serviceFixture) {
				if _, err := fixture.service.CreateDestination(context.Background(), DestinationInput{
					Name: "phone", Type: TypeNtfy, Config: map[string]string{"topic": "alerts"},
				}); err != nil {
					t.Fatalf("create: %v", err)
				}
			},
			name:    "phone",
			wantErr: ErrRotationUnsupported,
		},
		"discord is unsupported": {
			setup:   func(t *testing.T, fixture serviceFixture) { fixture.createDiscord(t, "ops") },
			name:    "ops",
			wantErr: ErrRotationUnsupported,
		},
		"missing destination": {
			setup:   func(*testing.T, serviceFixture) {},
			name:    "ghost",
			wantErr: ErrDestinationNotFound,
		},
	}
	for label, test := range tests {
		t.Run(label, func(t *testing.T) {
			fixture := newRotateFixture(t)
			test.setup(t, fixture)
			before := maps.Clone(fixture.store.destinations)

			_, secret, err := fixture.service.RotateWebhookSecret(context.Background(), test.name, 24)

			if !errors.Is(err, test.wantErr) {
				t.Fatalf("err = %v, want %v", err, test.wantErr)
			}
			if secret != "" {
				t.Fatalf("secret = %q on error", secret)
			}
			for name, beforeRow := range before {
				afterRow := fixture.store.destinations[name]
				if !bytes.Equal(beforeRow.SealedSecrets, afterRow.SealedSecrets) || !maps.Equal(beforeRow.Config, afterRow.Config) {
					t.Fatalf("destination %q changed on a rejected rotation", name)
				}
			}
		})
	}
}

func TestRotateWebhookSecretUnreadableSecretsWritesNothing(t *testing.T) {
	fixture := newRotateFixture(t)
	fixture.createWebhook(t, "hook")
	stored := fixture.store.destinations["hook"]
	stored.SealedSecrets = bytes.Repeat([]byte{0xAB}, 64)
	fixture.store.destinations["hook"] = stored

	_, secret, err := fixture.service.RotateWebhookSecret(context.Background(), "hook", 24)

	if err == nil || errors.Is(err, ErrRotationUnsupported) || errors.Is(err, ErrDestinationNotFound) {
		t.Fatalf("err = %v, want a generic open failure", err)
	}
	if secret != "" {
		t.Fatalf("secret = %q on error", secret)
	}
	after := fixture.store.destinations["hook"]
	if !bytes.Equal(after.SealedSecrets, stored.SealedSecrets) || len(after.Config) != 0 {
		t.Fatal("an unreadable-secrets rotation wrote to the row")
	}
}
