package bluetooth

import "testing"

func TestScanConfigMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		filters []string
		input   string
		want    bool
	}{
		{name: "no filter matches anything", filters: nil, input: "LiTime Battery", want: true},
		{name: "no filter matches empty name", filters: nil, input: "", want: true},
		{name: "single filter matches", filters: []string{"LiTime Battery"}, input: "LiTime Battery", want: true},
		{name: "single filter rejects other", filters: []string{"LiTime Battery"}, input: "Something Else", want: false},
		{name: "filter is case sensitive", filters: []string{"LiTime Battery"}, input: "litime battery", want: false},
		{name: "filter rejects unnamed device", filters: []string{"LiTime Battery"}, input: "", want: false},
		{name: "multiple filters match either", filters: []string{"a", "b"}, input: "b", want: true},
		{name: "multiple filters reject third", filters: []string{"a", "b"}, input: "c", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &scanConfig{}
			if tt.filters != nil {
				ScanWithNames(tt.filters...)(cfg)
			}

			if got := cfg.matches(tt.input, "11:22:33:AA:BB:CC"); got != tt.want {
				t.Errorf("matches(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// TestScanWithNamesEmptyIsNotAWildcard guards a footgun: calling ScanWithNames
// with no arguments must not silently behave like "match everything" for a
// caller that expected to filter.
func TestScanWithNamesEmpty(t *testing.T) {
	t.Parallel()

	cfg := &scanConfig{}
	ScanWithNames()(cfg)

	if !cfg.matches("anything", "11:22:33:AA:BB:CC") {
		t.Error("ScanWithNames() with no names should leave the scan unfiltered")
	}
}

func TestParseAddressRejectsInvalid(t *testing.T) {
	t.Parallel()

	// These are invalid on every platform: neither a MAC nor a CoreBluetooth
	// UUID. A silently-zero address here would mean connecting to the wrong
	// device, so parsing must fail loudly.
	invalid := []string{
		"",
		"not-an-address",
		"11:22:33:AA:BB",       // too short for a MAC
		"11:22:33:AA:BB:CC:DD", // too long for a MAC
		"11:22:33:AA:BB:GG",    // non-hex digits
	}

	for _, input := range invalid {
		if _, err := ParseAddress(input); err == nil {
			t.Errorf("ParseAddress(%q) succeeded, want error", input)
		}
	}
}
