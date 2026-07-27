package bluetooth

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"go.uber.org/multierr"
	"tinygo.org/x/bluetooth"
)

const (
	WriteCharacteristicUUID  = "0000ffe2-0000-1000-8000-00805f9b34fb"
	NotifyCharacteristicUUID = "0000ffe1-0000-1000-8000-00805f9b34fb"
	DefaultScanTimeout       = 30 * time.Second
)

var (
	MagicQuery = []byte{0x00, 0x00, 0x04, 0x01, 0x13, 0x55, 0xAA, 0x17}

	// ErrNotConnected is returned by QueryData when there is no live connection
	// to write to, which includes the window after an explicit Disconnect.
	ErrNotConnected = fmt.Errorf("not connected")
)

type LiTimeBluetoothClient struct {
	logger                      *slog.Logger
	DeviceName                  string
	writeCharacteristicUUID     string
	notifyCharacteristicUUID    string
	bluetoothAdapter            *bluetooth.Adapter
	enableNotificationsCallback func(b []byte)
	scanTimeout                 time.Duration

	// mu guards the connection state below, which Connect and Disconnect
	// replace wholesale and QueryData reads.
	mu sync.Mutex
	// address, when set, is connected to directly and no scan is performed.
	address             *bluetooth.Address
	connected           bool
	device              bluetooth.Device
	writeCharacteristic bluetooth.DeviceCharacteristic
}

type LiTimeBluetoothClientOption func(*LiTimeBluetoothClient)

func NewLiTimeBluetoothClient(deviceName string, opts ...LiTimeBluetoothClientOption) *LiTimeBluetoothClient {
	client := &LiTimeBluetoothClient{
		DeviceName:               deviceName,
		writeCharacteristicUUID:  WriteCharacteristicUUID,
		notifyCharacteristicUUID: NotifyCharacteristicUUID,
		bluetoothAdapter:         bluetooth.DefaultAdapter,
		scanTimeout:              DefaultScanTimeout,
		enableNotificationsCallback: func(b []byte) {
			slog.Warn("no notification callback set, received data", slog.String("data", hex.EncodeToString(b)))
		},
	}

	for _, opt := range opts {
		opt(client)
	}

	if client.logger == nil {
		client.logger = slog.New(slog.NewTextHandler(io.Discard, nil)) // discard logs by default
	} else {
		client.logger = client.logger.With(slog.String("source", "go-litime-bluetooth")) // ensure source is set
	}

	return client
}

func WithLogger(logger *slog.Logger) LiTimeBluetoothClientOption {
	return func(client *LiTimeBluetoothClient) {
		client.logger = logger
	}
}

func WithWriteCharacteristicUUID(uuid string) LiTimeBluetoothClientOption {
	return func(client *LiTimeBluetoothClient) {
		client.writeCharacteristicUUID = uuid
	}
}

func WithNotifyCharacteristicUUID(uuid string) LiTimeBluetoothClientOption {
	return func(client *LiTimeBluetoothClient) {
		client.notifyCharacteristicUUID = uuid
	}
}

func WithEnableNotificationCallback(callback func(b []byte)) LiTimeBluetoothClientOption {
	return func(client *LiTimeBluetoothClient) {
		client.enableNotificationsCallback = callback
	}
}

func WithScanTimeout(timeout time.Duration) LiTimeBluetoothClientOption {
	return func(client *LiTimeBluetoothClient) {
		client.scanTimeout = timeout
	}
}

// WithAdapter selects the adapter this client uses, for hosts with more than one
// radio. Defaults to bluetooth.DefaultAdapter.
func WithAdapter(adapter *bluetooth.Adapter) LiTimeBluetoothClientOption {
	return func(client *LiTimeBluetoothClient) {
		client.bluetoothAdapter = adapter
	}
}

// WithAddress identifies the device by address rather than advertised name.
//
// This is the option to use when talking to several batteries, because matching
// on the advertised name is only reliable when every battery advertises a
// distinct one. Use ScanForDevices or ParseAddress to obtain an address.
//
// Note that this narrows the scan rather than skipping it: BlueZ needs to have
// seen the device recently to connect to it, no matter how it was identified.
func WithAddress(address bluetooth.Address) LiTimeBluetoothClientOption {
	return func(client *LiTimeBluetoothClient) {
		client.address = &address
	}
}

// Address reports the address this client is connected to, or is configured to
// connect to. The second return value is false when the client was created with
// only a name and has not yet resolved it by scanning.
func (c *LiTimeBluetoothClient) Address() (bluetooth.Address, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.address == nil {
		return bluetooth.Address{}, false
	}

	return *c.address, true
}

// Connected reports whether the client currently believes it holds a connection.
//
// This tracks Connect and Disconnect calls made through this client. It does not
// detect a peer that has gone away on its own: as a central, the underlying
// stack provides no such notification, so a battery that drops out of range or
// powers off still reads as connected here. Treat a lack of incoming
// notifications as the authoritative liveness signal and reconnect on that.
func (c *LiTimeBluetoothClient) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.connected
}

// Connect locates the device, connects to it, and enables notifications.
//
// It may be called again after Disconnect to re-establish the connection.
//
// Every attempt scans first, including reconnects and connects to a known
// address. This is not an optimisation that was missed: BlueZ can only connect
// to a device it currently holds an object for, and it discards those objects
// over time, so a connect that skips the scan fails once the device has aged
// out. Knowing the address avoids having to search by name; it does not avoid
// the scan.
func (c *LiTimeBluetoothClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	alreadyConnected := c.connected
	address := c.address
	c.mu.Unlock()

	if alreadyConnected {
		return nil
	}

	c.logger.Debug("enabling bluetooth adapter")
	if err := enableAdapter(c.bluetoothAdapter); err != nil {
		return err
	}

	address, err := c.locate(ctx, address)
	if err != nil {
		return err
	}

	// One connection may be established at a time per adapter.
	state := stateFor(c.bluetoothAdapter)
	state.connectMu.Lock()
	defer state.connectMu.Unlock()

	c.logger.Debug("connecting to device", slog.String("address", address.String()))
	device, err := c.bluetoothAdapter.Connect(*address, bluetooth.ConnectionParams{})
	if err != nil {
		return fmt.Errorf("failed to connect to device: %w", err)
	}

	writeCharacteristic, notifyCharacteristic, err := c.discoverCharacteristics(device)
	if err != nil {
		_ = device.Disconnect()
		return err
	}

	if err := notifyCharacteristic.EnableNotifications(c.enableNotificationsCallback); err != nil {
		_ = device.Disconnect()
		return fmt.Errorf("failed to enable notifications: %w", err)
	}

	c.mu.Lock()
	c.address = address
	c.device = device
	c.writeCharacteristic = writeCharacteristic
	c.connected = true
	c.mu.Unlock()

	return nil
}

// locate scans for the device and returns the address to connect to.
//
// A configured address narrows the scan to that one device and is returned as
// given; otherwise the client's name is used to discover it. Either way a scan
// runs, which is what leaves the device connectable.
func (c *LiTimeBluetoothClient) locate(ctx context.Context, address *bluetooth.Address) (*bluetooth.Address, error) {
	opts := []ScanOption{
		ScanWithAdapter(c.bluetoothAdapter),
		ScanWithLogger(c.logger),
		ScanWithTimeout(c.scanTimeout),
		ScanWithTargetCount(1),
	}

	switch {
	case address != nil:
		opts = append(opts, ScanWithAddresses(*address))
	case c.DeviceName != "":
		opts = append(opts, ScanWithNames(c.DeviceName))
	default:
		return nil, fmt.Errorf("no device name or address configured")
	}

	devices, err := ScanForDevices(ctx, opts...)
	if err != nil {
		return nil, err
	}

	if len(devices) == 0 {
		if address != nil {
			return nil, fmt.Errorf("device %s not seen within %v", address.String(), c.scanTimeout)
		}
		return nil, fmt.Errorf("device '%s' not found within %v", c.DeviceName, c.scanTimeout)
	}

	if address != nil {
		return address, nil
	}

	if len(devices) > 1 {
		// Several batteries of the same model can share a local name, in which
		// case the winner here is whichever the adapter surfaced first and is
		// not stable across runs. Configure addresses to disambiguate.
		c.logger.Warn("multiple devices advertise this name, picking the first",
			slog.String("device_name", c.DeviceName),
			slog.Int("count", len(devices)))
	}

	resolved := devices[0].Address
	c.logger.Debug("resolved device by name",
		slog.String("device_name", c.DeviceName),
		slog.String("address", resolved.String()))

	return &resolved, nil
}

// discoverCharacteristics walks the device's services and returns the write and
// notify characteristics the LiTime protocol needs.
func (c *LiTimeBluetoothClient) discoverCharacteristics(device bluetooth.Device) (bluetooth.DeviceCharacteristic, bluetooth.DeviceCharacteristic, error) {
	var empty bluetooth.DeviceCharacteristic

	c.logger.Debug("discovering services for device")
	services, err := device.DiscoverServices(nil)
	if err != nil {
		return empty, empty, fmt.Errorf("failed to discover services: %w", err)
	}
	c.logger.Debug("discovered services", slog.Int("count", len(services)))

	c.logger.Debug("discovering characteristics for services")
	var characteristicErr error
	characteristicMap := make(map[string]bluetooth.DeviceCharacteristic)
	for _, service := range services {
		characteristics, err := service.DiscoverCharacteristics(nil)
		if err != nil {
			characteristicErr = multierr.Append(characteristicErr, fmt.Errorf("failed to discover characteristics for service %s: %w", service.UUID().String(), err))
			continue
		}

		for _, characteristic := range characteristics {
			characteristicMap[characteristic.String()] = characteristic
		}
	}
	if characteristicErr != nil {
		return empty, empty, fmt.Errorf("failed to discover characteristics: %w", characteristicErr)
	}
	c.logger.Debug("discovered characteristics", slog.Int("count", len(characteristicMap)))

	var characteristicExistsErr error
	writeCharacteristic, exists := characteristicMap[c.writeCharacteristicUUID]
	if !exists {
		characteristicExistsErr = multierr.Append(characteristicExistsErr, fmt.Errorf("write characteristic %s not found", c.writeCharacteristicUUID))
	}

	notifyCharacteristic, exists := characteristicMap[c.notifyCharacteristicUUID]
	if !exists {
		characteristicExistsErr = multierr.Append(characteristicExistsErr, fmt.Errorf("notify characteristic %s not found", c.notifyCharacteristicUUID))
	}

	if characteristicExistsErr != nil {
		return empty, empty, fmt.Errorf("failed to find characteristics in map: %w", characteristicExistsErr)
	}

	return writeCharacteristic, notifyCharacteristic, nil
}

// Disconnect drops the connection. The client can be reconnected afterwards with
// Connect. Disconnecting an already-disconnected client is not an error.
func (c *LiTimeBluetoothClient) Disconnect() error {
	c.mu.Lock()
	if !c.connected {
		c.mu.Unlock()
		return nil
	}
	device := c.device
	c.connected = false
	c.device = bluetooth.Device{}
	c.writeCharacteristic = bluetooth.DeviceCharacteristic{}
	c.mu.Unlock()

	if err := device.Disconnect(); err != nil {
		return fmt.Errorf("failed to disconnect from device: %w", err)
	}

	return nil
}

// QueryData asks the battery to report its current state. The reply arrives
// asynchronously on the notification callback rather than as a return value.
func (c *LiTimeBluetoothClient) QueryData() error {
	c.mu.Lock()
	connected := c.connected
	writeCharacteristic := c.writeCharacteristic
	c.mu.Unlock()

	if !connected {
		return ErrNotConnected
	}

	_, err := writeCharacteristic.WriteWithoutResponse(MagicQuery)
	if err != nil {
		return fmt.Errorf("failed to write query data: %w", err)
	}

	return nil
}
