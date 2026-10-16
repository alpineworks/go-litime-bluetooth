package bluetooth

import (
	"errors"
	"testing"
)

type fakeNotifier struct {
	callbacks []func([]byte)
	err       error
}

func (f *fakeNotifier) EnableNotifications(callback func([]byte)) error {
	f.callbacks = append(f.callbacks, callback)
	return f.err
}

type fakeDevice struct{ disconnects int }

func (f *fakeDevice) Disconnect() error {
	f.disconnects++
	return nil
}

// Disconnect must turn notifications off before dropping the device. On Linux
// the stack's subscription outlives the connection otherwise, and because a
// reconnect reuses the same characteristic, every stale subscription delivers
// each new notification again: N reconnects meant N+1 copies of every reading.
func TestDisconnectDisablesNotifications(t *testing.T) {
	t.Parallel()

	notifier := &fakeNotifier{}
	device := &fakeDevice{}
	client := NewLiTimeBluetoothClient("test")
	client.connected = true
	client.device = device
	client.notifyCharacteristic = notifier

	if err := client.Disconnect(); err != nil {
		t.Fatalf("Disconnect() returned error: %v", err)
	}

	if len(notifier.callbacks) != 1 || notifier.callbacks[0] != nil {
		t.Fatalf("EnableNotifications calls = %d (nil callback: %v), want exactly one call with nil",
			len(notifier.callbacks), len(notifier.callbacks) == 1 && notifier.callbacks[0] == nil)
	}
	if device.disconnects != 1 {
		t.Errorf("device disconnects = %d, want 1", device.disconnects)
	}
	if client.notifyCharacteristic != nil {
		t.Error("notifyCharacteristic still set after Disconnect; a second Disconnect would disable it again")
	}
}

// A failure to disable notifications must not stop the device from being
// dropped: a half-released client that still holds the radio link is worse.
func TestDisconnectStillDisconnectsWhenDisableFails(t *testing.T) {
	t.Parallel()

	notifier := &fakeNotifier{err: errors.New("dbus gone")}
	device := &fakeDevice{}
	client := NewLiTimeBluetoothClient("test")
	client.connected = true
	client.device = device
	client.notifyCharacteristic = notifier

	if err := client.Disconnect(); err == nil {
		t.Fatal("Disconnect() returned nil, want the disable error reported")
	}
	if device.disconnects != 1 {
		t.Errorf("device disconnects = %d, want 1", device.disconnects)
	}
	if client.Connected() {
		t.Error("client still reports connected")
	}
}

func TestDisconnectWhenNotConnectedIsNoop(t *testing.T) {
	t.Parallel()

	notifier := &fakeNotifier{}
	client := NewLiTimeBluetoothClient("test")
	client.notifyCharacteristic = notifier

	if err := client.Disconnect(); err != nil {
		t.Fatalf("Disconnect() returned error: %v", err)
	}
	if len(notifier.callbacks) != 0 {
		t.Errorf("EnableNotifications called %d times on a disconnected client, want 0", len(notifier.callbacks))
	}
}
