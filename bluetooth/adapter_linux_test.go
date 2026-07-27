//go:build linux

package bluetooth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"tinygo.org/x/bluetooth"
)

// Coordination is per adapter, so every client sharing a radio must land on the
// same state. Returning fresh state per call would leave each client locking
// something nobody else observes, which is indistinguishable from no locking.
func TestStateForIsPerAdapter(t *testing.T) {
	t.Parallel()

	adapter := bluetooth.NewAdapter("test-adapter-a")
	other := bluetooth.NewAdapter("test-adapter-b")

	first := stateFor(adapter)
	again := stateFor(adapter)
	separate := stateFor(other)

	if first != again {
		t.Error("stateFor returned different state for the same adapter")
	}

	if first == separate {
		t.Error("stateFor returned shared state for different adapters")
	}
}

// Scanning and connection bring-up share one lock because they conflict with
// each other, not just with themselves. This pins that the critical sections
// cannot overlap; whether every caller takes the lock is what hardware
// exercises.
func TestRadioLockIsExclusive(t *testing.T) {
	t.Parallel()

	state := stateFor(bluetooth.NewAdapter("test-adapter-exclusive"))

	var (
		inside   atomic.Int32
		overlaps atomic.Int32
		wg       sync.WaitGroup
	)

	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				state.radioMu.Lock()
				if inside.Add(1) != 1 {
					overlaps.Add(1)
				}
				time.Sleep(time.Microsecond)
				inside.Add(-1)
				state.radioMu.Unlock()
			}
		}()
	}
	wg.Wait()

	if got := overlaps.Load(); got != 0 {
		t.Errorf("radio lock allowed %d overlapping critical sections, want 0", got)
	}
}

// enableAdapter must not cache a failure. A radio that was not ready yet has to
// stay retryable, otherwise one early failure disables the process for good.
func TestEnableAdapterDoesNotCacheFailure(t *testing.T) {
	t.Parallel()

	// An adapter id with no corresponding D-Bus object fails to enable.
	adapter := bluetooth.NewAdapter("definitely-not-a-real-adapter")

	if err := enableAdapter(adapter); err == nil {
		t.Skip("adapter unexpectedly enabled; nothing to assert about retries")
	}

	if stateFor(adapter).enabled {
		t.Error("adapter marked enabled after a failed Enable")
	}

	// A second attempt must retry rather than return a cached verdict.
	if err := enableAdapter(adapter); err == nil {
		t.Error("second enable unexpectedly succeeded without the adapter changing")
	}
}
