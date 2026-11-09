package logthrottle

import (
	"fmt"
	"testing"
	"time"
)

var throttleStart = time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

const testInterval = 10 * time.Minute

func TestAllowsOncePerInterval(testInstance *testing.T) {
	throttle := New(testInterval)
	if !throttle.Allow("a", throttleStart) {
		testInstance.Fatal("first call must be allowed")
	}
	if throttle.Allow("a", throttleStart.Add(testInterval-time.Second)) {
		testInstance.Error("repeat inside the interval must be suppressed")
	}
	if !throttle.Allow("a", throttleStart.Add(testInterval)) {
		testInstance.Error("call after the interval must be allowed")
	}
}

func TestKeysAreIndependent(testInstance *testing.T) {
	throttle := New(testInterval)
	throttle.Allow("a", throttleStart)
	if !throttle.Allow("b", throttleStart) {
		testInstance.Error("a distinct key must be allowed")
	}
	if throttle.Allow("a", throttleStart) {
		testInstance.Error("key a must still be throttled")
	}
}

func TestDropsStaleEntriesAtTheBound(testInstance *testing.T) {
	throttle := New(testInterval)
	for index := 0; index < MaxKeys; index++ {
		throttle.Allow(fmt.Sprintf("old-%d", index), throttleStart)
	}
	later := throttleStart.Add(testInterval + time.Second)
	if !throttle.Allow("new", later) {
		testInstance.Fatal("new key must be allowed")
	}
	if got := len(throttle.last); got != 1 {
		testInstance.Errorf("stale entries not dropped: %d keys", got)
	}
}

func TestNeverExceedsTheBoundWithFreshKeys(testInstance *testing.T) {
	throttle := New(testInterval)
	total := MaxKeys + 50
	for index := 0; index < total; index++ {
		key := fmt.Sprintf("key-%d", index)
		if !throttle.Allow(key, throttleStart.Add(time.Duration(index)*time.Millisecond)) {
			testInstance.Fatalf("fresh key %s must be allowed", key)
		}
		if got := len(throttle.last); got > MaxKeys {
			testInstance.Fatalf("map grew to %d", got)
		}
	}
	newest := fmt.Sprintf("key-%d", total-1)
	if _, ok := throttle.last[newest]; !ok {
		testInstance.Error("newest key must be recorded")
	}
	if _, ok := throttle.last["key-0"]; ok {
		testInstance.Error("oldest key must have been evicted")
	}
	if throttle.Allow(newest, throttleStart.Add(time.Duration(total)*time.Millisecond)) {
		testInstance.Error("newest key must be throttled")
	}
}

func TestNewPanicsOnNonPositiveInterval(testInstance *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		func() {
			defer func() {
				if recover() == nil {
					testInstance.Errorf("New(%v) must panic", interval)
				}
			}()
			New(interval)
		}()
	}
}
