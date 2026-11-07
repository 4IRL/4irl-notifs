package message

import (
	"fmt"
	"testing"
	"time"
)

var throttleStart = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

func TestLogThrottleAllowsOncePerInterval(t *testing.T) {
	throttle := newLogThrottle()
	if !throttle.Allow("a", throttleStart) {
		t.Fatal("first call must be allowed")
	}
	if throttle.Allow("a", throttleStart.Add(heldReleaseWarnInterval-time.Second)) {
		t.Error("repeat inside the interval must be suppressed")
	}
	if !throttle.Allow("a", throttleStart.Add(heldReleaseWarnInterval)) {
		t.Error("call after the interval must be allowed")
	}
}

func TestLogThrottleKeysAreIndependent(t *testing.T) {
	throttle := newLogThrottle()
	throttle.Allow("a", throttleStart)
	if !throttle.Allow("b", throttleStart) {
		t.Error("a distinct key must be allowed")
	}
	if throttle.Allow("a", throttleStart) {
		t.Error("key a must still be throttled")
	}
}

func TestLogThrottleDropsStaleEntriesAtTheBound(t *testing.T) {
	throttle := newLogThrottle()
	for index := 0; index < logThrottleMaxKeys; index++ {
		throttle.Allow(fmt.Sprintf("old-%d", index), throttleStart)
	}
	later := throttleStart.Add(heldReleaseWarnInterval + time.Second)
	if !throttle.Allow("new", later) {
		t.Fatal("new key must be allowed")
	}
	if got := len(throttle.last); got != 1 {
		t.Errorf("stale entries not dropped: %d keys", got)
	}
}

func TestLogThrottleNeverExceedsTheBoundWithFreshKeys(t *testing.T) {
	throttle := newLogThrottle()
	total := logThrottleMaxKeys + 50
	for index := 0; index < total; index++ {
		key := fmt.Sprintf("key-%d", index)
		if !throttle.Allow(key, throttleStart.Add(time.Duration(index)*time.Millisecond)) {
			t.Fatalf("fresh key %s must be allowed", key)
		}
		if got := len(throttle.last); got > logThrottleMaxKeys {
			t.Fatalf("map grew to %d", got)
		}
	}
	newest := fmt.Sprintf("key-%d", total-1)
	if _, ok := throttle.last[newest]; !ok {
		t.Error("newest key must be recorded")
	}
	if _, ok := throttle.last["key-0"]; ok {
		t.Error("oldest key must have been evicted")
	}
	if throttle.Allow(newest, throttleStart.Add(time.Duration(total)*time.Millisecond)) {
		t.Error("newest key must be throttled")
	}
}
