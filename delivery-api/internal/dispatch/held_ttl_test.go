package dispatch

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHeldTTLFromEnv(t *testing.T) {
	const day = 24 * time.Hour
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "empty defaults to 7 days", value: "", want: 7 * day},
		{name: "one day", value: "1", want: day},
		{name: "thirty days", value: "30", want: 30 * day},
		{name: "upper bound", value: "365", want: 365 * day},
		{name: "zero", value: "0", wantErr: true},
		{name: "negative", value: "-5", wantErr: true},
		{name: "above upper bound", value: "366", wantErr: true},
		{name: "not a number", value: "secret-looking-value", wantErr: true},
		{name: "fractional", value: "1.5", wantErr: true},
		{name: "padded", value: " 7", wantErr: true},
		{name: "overflowing", value: "99999999999999999999", wantErr: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := HeldTTLFromEnv(func(name string) string {
				if name != "DELIVERY_HELD_TTL_DAYS" {
					t.Errorf("read env %q, want DELIVERY_HELD_TTL_DAYS", name)
				}
				return testCase.value
			})
			if testCase.wantErr {
				if err == nil || err.Error() != "invalid DELIVERY_HELD_TTL_DAYS" {
					t.Fatalf("err = %v, want invalid DELIVERY_HELD_TTL_DAYS", err)
				}
				if testCase.value != "" && strings.Contains(err.Error(), testCase.value) {
					t.Errorf("the error echoes the value %q", testCase.value)
				}
				return
			}
			if err != nil || got != testCase.want {
				t.Errorf("got %v, %v; want %v", got, err, testCase.want)
			}
		})
	}
}

func TestDefaultHeldTTLMatchesTheComposeDefault(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	// This file is <repo>/delivery-api/internal/dispatch/<file>.
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
	pattern := regexp.MustCompile(
		`(?m)^\s*- ` + HeldTTLEnvVar + `=\$\{` + HeldTTLEnvVar + `:-(\d+)\}\s*$`)
	for _, composeName := range []string{"docker-compose.yml", "docker-compose.prod.yml"} {
		composeText, readErr := os.ReadFile(filepath.Join(repoRoot, composeName))
		if readErr != nil {
			t.Fatalf("read %s: %v", composeName, readErr)
		}
		matches := pattern.FindAllStringSubmatch(string(composeText), -1)
		if len(matches) != 1 {
			t.Fatalf("%s has %d %s default lines, want exactly 1", composeName, len(matches), HeldTTLEnvVar)
		}
		if want := strconv.Itoa(DefaultHeldTTLDays); matches[0][1] != want {
			t.Errorf("%s defaults %s to %s, want the code default %s",
				composeName, HeldTTLEnvVar, matches[0][1], want)
		}
	}
}
