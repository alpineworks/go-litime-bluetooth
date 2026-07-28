package bluetooth

import (
	"bytes"
	"testing"

	"tinygo.org/x/bluetooth"
)

func TestManufacturerDataKeysByCompanyID(t *testing.T) {
	t.Parallel()

	data := manufacturerData([]bluetooth.ManufacturerDataElement{
		{CompanyID: 0x02E1, Data: []byte{0x10, 0x02}},
		{CompanyID: 0x585A, Data: []byte{0xAA}},
	})

	if got := len(data); got != 2 {
		t.Fatalf("got %d company entries, want 2", got)
	}

	if !bytes.Equal(data[0x02E1], []byte{0x10, 0x02}) {
		t.Errorf("company 0x02E1 = %v, want [16 2]", data[0x02E1])
	}

	if !bytes.Equal(data[0x585A], []byte{0xAA}) {
		t.Errorf("company 0x585A = %v, want [170]", data[0x585A])
	}
}

// The stack reuses its advertisement buffers between callbacks, so the reported
// data has to be a copy. Handing back the original would leave a caller holding
// bytes that change underneath it, which reads as intermittently corrupt
// advertisements rather than as an aliasing bug.
func TestManufacturerDataCopiesTheBuffer(t *testing.T) {
	t.Parallel()

	buffer := []byte{0x10, 0x02, 0x56}

	data := manufacturerData([]bluetooth.ManufacturerDataElement{
		{CompanyID: 0x02E1, Data: buffer},
	})

	// Simulate the stack reusing the buffer for the next advertisement.
	buffer[0] = 0xFF

	if data[0x02E1][0] != 0x10 {
		t.Errorf("reported data changed when the source buffer was reused: got 0x%02X, want 0x10", data[0x02E1][0])
	}
}

func TestManufacturerDataAbsentIsNil(t *testing.T) {
	t.Parallel()

	if data := manufacturerData(nil); data != nil {
		t.Errorf("got %v, want nil when the advertisement carries no manufacturer data", data)
	}
}
