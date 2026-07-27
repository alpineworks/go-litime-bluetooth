package bluetooth

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"tinygo.org/x/bluetooth"
)

// DiscoveredDevice is a peripheral observed during a scan.
type DiscoveredDevice struct {
	// Name is the advertised local name. It may be empty: BlueZ surfaces a
	// device as soon as it sees it, sometimes before the name is known.
	Name string

	// Address identifies the peripheral for a later Connect. Its form is
	// platform-specific: a MAC address on Linux and Windows, an opaque
	// CoreBluetooth UUID on macOS. Addresses are therefore not portable between
	// machines of different operating systems.
	Address bluetooth.Address

	// RSSI is the signal strength in dBm at the time the device was seen.
	RSSI int16
}

type scanConfig struct {
	adapter     *bluetooth.Adapter
	logger      *slog.Logger
	names       map[string]struct{}
	targetCount int
	timeout     time.Duration
}

// ScanOption configures ScanForDevices.
type ScanOption func(*scanConfig)

// ScanWithAdapter sets the adapter to scan with. Defaults to
// bluetooth.DefaultAdapter.
func ScanWithAdapter(adapter *bluetooth.Adapter) ScanOption {
	return func(cfg *scanConfig) {
		cfg.adapter = adapter
	}
}

// ScanWithLogger attaches a logger to the scan.
func ScanWithLogger(logger *slog.Logger) ScanOption {
	return func(cfg *scanConfig) {
		cfg.logger = logger
	}
}

// ScanWithTimeout bounds how long the scan runs. Defaults to
// DefaultScanTimeout.
func ScanWithTimeout(timeout time.Duration) ScanOption {
	return func(cfg *scanConfig) {
		cfg.timeout = timeout
	}
}

// ScanWithNames restricts results to peripherals advertising one of the given
// local names. With no names given, every peripheral seen is returned, which is
// the useful mode for discovering which batteries are in range and what their
// addresses are.
func ScanWithNames(names ...string) ScanOption {
	return func(cfg *scanConfig) {
		cfg.names = make(map[string]struct{}, len(names))
		for _, name := range names {
			cfg.names[name] = struct{}{}
		}
	}
}

// ScanWithTargetCount stops the scan early once this many distinct matching
// devices have been found, rather than always waiting out the full timeout.
// Zero, the default, means scan until the timeout expires.
func ScanWithTargetCount(count int) ScanOption {
	return func(cfg *scanConfig) {
		cfg.targetCount = count
	}
}

// ScanForDevices runs a single BLE scan and returns everything it saw, subject
// to the configured filters.
//
// One scan resolving every device is the intended way to bring up several
// batteries at once. Scanning per battery does not work: an adapter supports
// only one scan at a time, so concurrent scans fail and sequential scans pay the
// timeout over and over. Calls are serialised per adapter, so it is safe to call
// this from several goroutines; they will simply queue.
//
// Results are deduplicated by address. A device seen without a name that is
// later re-advertised with one is reported with the name.
func ScanForDevices(ctx context.Context, opts ...ScanOption) ([]DiscoveredDevice, error) {
	cfg := &scanConfig{
		adapter: bluetooth.DefaultAdapter,
		timeout: DefaultScanTimeout,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	if err := enableAdapter(cfg.adapter); err != nil {
		return nil, err
	}

	state := stateFor(cfg.adapter)
	state.scanMu.Lock()
	defer state.scanMu.Unlock()

	scanCtx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()

	// tinygo's StopScan closes a channel held on the adapter without guarding
	// against a second call, so two concurrent stops panic. Both the callback
	// and the timeout path below can want to stop the scan, hence the Once.
	var stopOnce sync.Once
	stopScan := func() {
		stopOnce.Do(func() {
			_ = cfg.adapter.StopScan()
		})
	}

	var (
		mu       sync.Mutex
		found    = make(map[string]DiscoveredDevice)
		targetCh = make(chan struct{})
		hitOnce  sync.Once
	)

	scanDone := make(chan error, 1)
	go func() {
		scanDone <- cfg.adapter.Scan(func(_ *bluetooth.Adapter, result bluetooth.ScanResult) {
			name := result.LocalName()
			if !cfg.matches(name) {
				return
			}

			key := result.Address.String()

			mu.Lock()
			existing, seen := found[key]
			// Keep the first non-empty name we learn for a device.
			if seen && existing.Name != "" {
				name = existing.Name
			}
			found[key] = DiscoveredDevice{
				Name:    name,
				Address: result.Address,
				RSSI:    result.RSSI,
			}
			reached := cfg.targetCount > 0 && len(found) >= cfg.targetCount
			mu.Unlock()

			if !seen {
				cfg.logger.Debug("discovered device",
					slog.String("name", name),
					slog.String("address", key),
					slog.Int("rssi", int(result.RSSI)))
			}

			if reached {
				stopScan()
				hitOnce.Do(func() { close(targetCh) })
			}
		})
	}()

	var (
		scanErr      error
		scanReturned bool
	)

	select {
	case <-targetCh:
		// The callback already stopped the scan; fall through and wait for Scan
		// to unwind.
	case scanErr = <-scanDone:
		// The scan ended on its own, either with an error or because something
		// external stopped discovery.
		scanReturned = true
	case <-scanCtx.Done():
		stopScan()
	}

	// Scan must fully return before the adapter lock is released, otherwise the
	// next scan can start while this one is still tearing down and be rejected.
	if !scanReturned {
		scanErr = <-scanDone
	}
	if scanErr != nil {
		return nil, fmt.Errorf("failed to scan for devices: %w", scanErr)
	}

	// A cancelled parent context is a real error; an expired scan timeout just
	// means we return whatever was found in the window.
	if ctx.Err() != nil {
		return nil, fmt.Errorf("scan cancelled: %w", ctx.Err())
	}

	mu.Lock()
	defer mu.Unlock()

	devices := make([]DiscoveredDevice, 0, len(found))
	for _, device := range found {
		devices = append(devices, device)
	}

	return devices, nil
}

func (cfg *scanConfig) matches(name string) bool {
	if len(cfg.names) == 0 {
		return true
	}

	_, ok := cfg.names[name]

	return ok
}

// ParseAddress turns a textual device address into a bluetooth.Address suitable
// for WithAddress.
//
// The accepted form is platform-specific, matching what the underlying stack
// uses: a MAC address such as "11:22:33:AA:BB:CC" on Linux and Windows, and a
// CoreBluetooth UUID on macOS. Use ScanForDevices to discover the right value
// for a given machine rather than assuming addresses transfer between hosts.
func ParseAddress(s string) (bluetooth.Address, error) {
	// Set silently ignores malformed input on every platform, so the only way to
	// validate is to check that the value survives a round trip.
	//
	// The input is also tried upper-cased because MAC parsing accepts upper-case
	// hex only, while addresses are very often written in lower case.
	for _, candidate := range []string{s, strings.ToUpper(s)} {
		var address bluetooth.Address

		address.Set(candidate)
		if strings.EqualFold(address.String(), s) {
			return address, nil
		}
	}

	return bluetooth.Address{}, fmt.Errorf("invalid device address %q for this platform", s)
}
