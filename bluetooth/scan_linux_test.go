//go:build linux

package bluetooth

import "testing"

// Addresses are MAC addresses on Linux, so the accepted textual form can only be
// asserted on a platform-specific basis.
func TestParseAddressMAC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "upper case", input: "11:22:33:AA:BB:CC", want: "11:22:33:AA:BB:CC"},
		{name: "lower case", input: "11:22:33:aa:bb:cc", want: "11:22:33:AA:BB:CC"},
		{name: "mixed case", input: "11:22:33:Aa:bB:cC", want: "11:22:33:AA:BB:CC"},
		{name: "digits only", input: "00:11:22:33:44:55", want: "00:11:22:33:44:55"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			address, err := ParseAddress(tt.input)
			if err != nil {
				t.Fatalf("ParseAddress(%q) returned error: %v", tt.input, err)
			}

			if got := address.String(); got != tt.want {
				t.Errorf("ParseAddress(%q).String() = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Two different addresses must not collapse to the same value, which is what
// would happen if parsing failed open and returned the zero address.
func TestParseAddressDistinguishesDevices(t *testing.T) {
	t.Parallel()

	first, err := ParseAddress("11:22:33:AA:BB:CC")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	second, err := ParseAddress("11:22:33:AA:BB:CD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if first.String() == second.String() {
		t.Errorf("distinct addresses both parsed to %q", first.String())
	}
}
