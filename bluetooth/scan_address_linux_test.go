//go:build linux

package bluetooth

import (
	"testing"

	"tinygo.org/x/bluetooth"
)

func mustParse(t *testing.T, s string) bluetooth.Address {
	t.Helper()

	address, err := ParseAddress(s)
	if err != nil {
		t.Fatalf("ParseAddress(%q): %v", s, err)
	}

	return address
}

func TestScanMatchesByAddress(t *testing.T) {
	t.Parallel()

	cfg := &scanConfig{}
	ScanWithAddresses(mustParse(t, "11:22:33:AA:BB:CC"))(cfg)

	tests := []struct {
		name    string
		address string
		want    bool
	}{
		{name: "exact", address: "11:22:33:AA:BB:CC", want: true},
		// BlueZ reports upper case, but an address that made a round trip
		// through configuration may not have; a case mismatch here would mean
		// silently never matching the device.
		{name: "lower case", address: "11:22:33:aa:bb:cc", want: true},
		{name: "different device", address: "11:22:33:AA:BB:CD", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Name is deliberately unmatched: an address filter must stand on
			// its own, since a device may advertise no name at all.
			if got := cfg.matches("", tt.address); got != tt.want {
				t.Errorf("matches(_, %q) = %v, want %v", tt.address, got, tt.want)
			}
		})
	}
}

// Name and address filters are alternatives, so one scan can resolve a mixed
// set of batteries.
func TestScanNameAndAddressFiltersAreAlternatives(t *testing.T) {
	t.Parallel()

	cfg := &scanConfig{}
	ScanWithNames("LiTime Battery")(cfg)
	ScanWithAddresses(mustParse(t, "11:22:33:AA:BB:CC"))(cfg)

	cases := []struct {
		name    string
		devName string
		address string
		want    bool
	}{
		{name: "matches by name only", devName: "LiTime Battery", address: "99:99:99:99:99:99", want: true},
		{name: "matches by address only", devName: "Something Else", address: "11:22:33:AA:BB:CC", want: true},
		{name: "matches neither", devName: "Something Else", address: "99:99:99:99:99:99", want: false},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := cfg.matches(tt.devName, tt.address); got != tt.want {
				t.Errorf("matches(%q, %q) = %v, want %v", tt.devName, tt.address, got, tt.want)
			}
		})
	}
}
