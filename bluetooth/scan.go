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

	// ManufacturerData holds the advertisement's manufacturer data, keyed by
	// Bluetooth SIG company identifier.
	//
	// Some devices publish their whole state here rather than exposing it over
	// a connection, so a scan is all that is needed to read them. Reporting it
	// lets a caller collect from those devices on the same adapter, and under
	// the same serialisation, as devices it connects to.
	//
	// When a device is seen more than once during a scan, the most recent
	// advertisement wins, because this is live state rather than identity.
	ManufacturerData map[uint16][]byte
}

type scanConfig struct {
	adapter     *bluetooth.Adapter
	logger      *slog.Logger
	names       map[string]struct{}
	addresses   map[string]struct{}
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

// ScanWithAddresses restricts results to peripherals with one of the given
// device addresses.
//
// Scanning for an address you already know is not redundant. BlueZ can only
// connect to a device it currently has an object for, and it discards those
// objects over time, so a scan is what makes a known address connectable again.
// Address matching is case-insensitive.
func ScanWithAddresses(addresses ...bluetooth.Address) ScanOption {
	return func(cfg *scanConfig) {
		cfg.addresses = make(map[string]struct{}, len(addresses))
		for _, address := range addresses {
			cfg.addresses[strings.ToUpper(address.String())] = struct{}{}
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
	cfg := newScanConfig(opts...)

	if err := enableAdapter(cfg.adapter); err != nil {
		return nil, err
	}

	state := stateFor(cfg.adapter)
	state.radioMu.Lock()
	defer state.radioMu.Unlock()

	return scanLocked(ctx, cfg)
}

func newScanConfig(opts ...ScanOption) *scanConfig {
	cfg := &scanConfig{
		adapter: bluetooth.DefaultAdapter,
		timeout: DefaultScanTimeout,
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, opt := range opts {
		opt(cfg)
	}

	return cfg
}

// scanLocked performs the scan. The caller must already hold the adapter's
// radioMu and must have enabled the adapter.
//
// Connect uses this rather than ScanForDevices so that it can hold the radio
// across both the scan and the connection that follows, which is the whole
// point of the lock: a scan starting between the two would abort the connect.
func scanLocked(ctx context.Context, cfg *scanConfig) ([]DiscoveredDevice, error) {
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
			key := result.Address.String()

			if !cfg.matches(name, key) {
				return
			}

			mu.Lock()
			existing, seen := found[key]
			// Keep the first non-empty name we learn for a device.
			if seen && existing.Name != "" {
				name = existing.Name
			}
			found[key] = DiscoveredDevice{
				Name:             name,
				Address:          result.Address,
				RSSI:             result.RSSI,
				ManufacturerData: manufacturerData(result.ManufacturerData()),
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

// matches reports whether a scan result passes the configured filters. Name and
// address filters are alternatives rather than conditions to satisfy together,
// so a caller can resolve a mixed set of batteries in one scan.
// manufacturerData collects an advertisement's manufacturer data by company id.
//
// The bytes are copied because the underlying stack reuses its advertisement
// buffers between callbacks, so retaining the slice would hand the caller data
// that changes underneath it.
func manufacturerData(elements []bluetooth.ManufacturerDataElement) map[uint16][]byte {
	if len(elements) == 0 {
		return nil
	}

	data := make(map[uint16][]byte, len(elements))
	for _, element := range elements {
		data[element.CompanyID] = append([]byte(nil), element.Data...)
	}

	return data
}

func (cfg *scanConfig) matches(name, address string) bool {
	if len(cfg.names) == 0 && len(cfg.addresses) == 0 {
		return true
	}

	if _, ok := cfg.names[name]; ok {
		return true
	}

	_, ok := cfg.addresses[strings.ToUpper(address)]

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
