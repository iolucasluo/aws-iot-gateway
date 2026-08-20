package media

import (
	"testing"
	"time"
)

func TestSnapshotLocksSerializeOneSnapshotAndReleaseTheEntry(t *testing.T) {
	t.Parallel()
	var locks snapshotLocks
	unlockFirst := locks.lock("snapshot-1")
	acquired := make(chan func(), 1)
	go func() { acquired <- locks.lock("snapshot-1") }()

	select {
	case unlock := <-acquired:
		unlock()
		t.Fatal("second resolver acquired the same snapshot concurrently")
	case <-time.After(25 * time.Millisecond):
	}

	unlockFirst()
	select {
	case unlockSecond := <-acquired:
		unlockSecond()
	case <-time.After(time.Second):
		t.Fatal("second resolver did not acquire the released snapshot lock")
	}

	locks.mu.Lock()
	defer locks.mu.Unlock()
	if len(locks.entries) != 0 {
		t.Fatalf("snapshot lock entries were not released: %d", len(locks.entries))
	}
}
