package media

import "sync"

type snapshotLocks struct {
	mu      sync.Mutex
	entries map[string]*snapshotLock
}

type snapshotLock struct {
	mu   sync.Mutex
	refs int
}

func (locks *snapshotLocks) lock(id string) func() {
	locks.mu.Lock()
	if locks.entries == nil {
		locks.entries = make(map[string]*snapshotLock)
	}
	entry := locks.entries[id]
	if entry == nil {
		entry = &snapshotLock{}
		locks.entries[id] = entry
	}
	entry.refs++
	locks.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		locks.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(locks.entries, id)
		}
		locks.mu.Unlock()
	}
}
