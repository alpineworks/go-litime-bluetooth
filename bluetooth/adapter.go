package bluetooth

import (
	"fmt"
	"sync"

	"tinygo.org/x/bluetooth"
)

// adapterState carries the per-adapter coordination that tinygo's bluetooth
// package leaves to the caller. It matters as soon as more than one client
// shares an adapter, which is unavoidable when talking to several batteries:
// there is normally only one radio.
//
// Two operations are unsafe to call concurrently on a single adapter:
//
//   - Enable, which populates the adapter's connection fields without any
//     synchronisation, so overlapping calls race on those writes.
//   - Scan, which stores its cancel channel on the adapter. A second concurrent
//     Scan fails outright, and a StopScan issued by one caller tears down a scan
//     another caller is still relying on.
//
// Connect, DiscoverServices and the characteristic operations are all scoped to
// a single device and need no coordination here.
type adapterState struct {
	enableMu sync.Mutex
	enabled  bool

	// scanMu serialises whole Scan/StopScan cycles on this adapter.
	scanMu sync.Mutex

	// connectMu serialises connection establishment. A controller can only
	// bring up one LE connection at a time; overlapping attempts abort each
	// other with "le-connection-abort-by-local", which looks like both peers
	// refusing rather than the two callers colliding. Only establishment is
	// serialised, not the resulting connections, which operate independently.
	connectMu sync.Mutex
}

var (
	adapterStatesMu sync.Mutex
	adapterStates   = make(map[*bluetooth.Adapter]*adapterState)
)

// stateFor returns the coordination state for an adapter, creating it on first
// use. Adapters are long-lived process-wide objects, so the map is never pruned.
func stateFor(adapter *bluetooth.Adapter) *adapterState {
	adapterStatesMu.Lock()
	defer adapterStatesMu.Unlock()

	state, ok := adapterStates[adapter]
	if !ok {
		state = &adapterState{}
		adapterStates[adapter] = state
	}

	return state
}

// enableAdapter enables an adapter exactly once per process. Callers may invoke
// it freely: every entry point that touches the radio calls it first, so no
// caller has to reason about whether some other client got there already.
//
// A failed attempt is not cached, so a caller can retry once whatever blocked
// the adapter (a powered-off radio, a D-Bus that was not up yet) is resolved.
func enableAdapter(adapter *bluetooth.Adapter) error {
	state := stateFor(adapter)

	state.enableMu.Lock()
	defer state.enableMu.Unlock()

	if state.enabled {
		return nil
	}

	if err := adapter.Enable(); err != nil {
		return fmt.Errorf("failed to enable bluetooth adapter: %w", err)
	}
	state.enabled = true

	return nil
}
