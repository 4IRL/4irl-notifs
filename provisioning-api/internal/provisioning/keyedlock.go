package provisioning

import "sync"

// keyedLock serializes work per string key (a person hash) inside one process.
// Production runs a single provisioning-api replica, so an in-process lock is
// enough to keep two flows for the same person from interleaving. Locks are
// not re-entrant: a flow takes a person's lock at most once per operation.
type keyedLock struct {
	guard   sync.Mutex
	entries map[string]*keyedLockEntry
}

// keyedLockEntry is one key's mutex plus the number of goroutines holding or
// waiting for it, so the entry can be dropped when nobody needs it.
type keyedLockEntry struct {
	mutex   sync.Mutex
	waiters int
}

// lock blocks until the key's lock is held and returns the function that
// releases it.
func (locks *keyedLock) lock(key string) func() {
	locks.guard.Lock()
	if locks.entries == nil {
		locks.entries = map[string]*keyedLockEntry{}
	}
	entry, exists := locks.entries[key]
	if !exists {
		entry = &keyedLockEntry{}
		locks.entries[key] = entry
	}
	entry.waiters++
	locks.guard.Unlock()

	entry.mutex.Lock()
	return func() {
		entry.mutex.Unlock()
		locks.guard.Lock()
		entry.waiters--
		if entry.waiters == 0 {
			delete(locks.entries, key)
		}
		locks.guard.Unlock()
	}
}
