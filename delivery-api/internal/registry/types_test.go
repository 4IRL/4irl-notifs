package registry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStoredDestinationNeverSerializesSealedSecrets(t *testing.T) {
	encoded, err := json.Marshal(StoredDestination{Name: "ops", SealedSecrets: []byte("SEALEDBLOBVALUE")})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "SealedSecrets") || strings.Contains(string(encoded), "U0VBTEVE") {
		t.Fatalf("sealed secrets serialized: %s", encoded)
	}
}

func TestViewsUseSnakeCaseJSON(t *testing.T) {
	timestamp := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	destination, err := json.Marshal(DestinationView{Name: "ops", Type: TypeNtfy, Config: map[string]string{}, Secrets: map[string]string{}, CreatedAt: timestamp, UpdatedAt: timestamp})
	if err != nil {
		t.Fatalf("marshal destination: %v", err)
	}
	route, err := json.Marshal(RouteView{ID: 1, AppPattern: "*", ChannelPattern: "*", Destinations: []string{"ops"}, CreatedAt: timestamp, UpdatedAt: timestamp})
	if err != nil {
		t.Fatalf("marshal route: %v", err)
	}
	for _, want := range []string{`"name"`, `"type"`, `"enabled"`, `"disabled_reason"`, `"config"`, `"secrets"`, `"created_at"`, `"updated_at"`} {
		if !strings.Contains(string(destination), want) {
			t.Errorf("destination JSON %s missing %s", destination, want)
		}
	}
	for _, want := range []string{`"id"`, `"app_pattern"`, `"channel_pattern"`, `"destinations"`, `"enabled"`, `"created_at"`, `"updated_at"`} {
		if !strings.Contains(string(route), want) {
			t.Errorf("route JSON %s missing %s", route, want)
		}
	}
}
