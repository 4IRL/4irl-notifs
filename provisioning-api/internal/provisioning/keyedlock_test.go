package provisioning

import (
	"sync"
	"testing"
	"time"
)

const keyedLockTestTimeout = 2 * time.Second

func entryCount(locks *keyedLock) int {
	locks.guard.Lock()
	defer locks.guard.Unlock()
	return len(locks.entries)
}

func TestKeyedLockDifferentKeysDoNotBlockEachOther(t *testing.T) {
	var locks keyedLock
	releaseFirst := locks.lock("alpha")

	acquired := make(chan func())
	go func() { acquired <- locks.lock("beta") }()

	select {
	case releaseSecond := <-acquired:
		releaseSecond()
	case <-time.After(keyedLockTestTimeout):
		t.Fatal("lock on a different key blocked while another key was held")
	}
	releaseFirst()
}

func TestKeyedLockSameKeySerializes(t *testing.T) {
	var locks keyedLock
	releaseFirst := locks.lock("alpha")

	acquired := make(chan func())
	go func() { acquired <- locks.lock("alpha") }()

	select {
	case <-acquired:
		t.Fatal("second lock on the same key was acquired while the first was held")
	case <-time.After(50 * time.Millisecond):
	}

	releaseFirst()
	select {
	case releaseSecond := <-acquired:
		releaseSecond()
	case <-time.After(keyedLockTestTimeout):
		t.Fatal("second lock on the same key was never acquired after the first released")
	}
}

func TestKeyedLockEntriesEmptyAfterAllUnlocks(t *testing.T) {
	var locks keyedLock
	const goroutines = 20
	var group sync.WaitGroup
	for index := 0; index < goroutines; index++ {
		key := "alpha"
		if index%2 == 0 {
			key = "beta"
		}
		group.Add(1)
		go func() {
			defer group.Done()
			locks.lock(key)()
		}()
	}
	group.Wait()

	if remaining := entryCount(&locks); remaining != 0 {
		t.Fatalf("entries map has %d entries after all unlocks, expected 0", remaining)
	}
}
