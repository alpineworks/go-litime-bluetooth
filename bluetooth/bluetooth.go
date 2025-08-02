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
)

type LiTimeBluetoothClient struct {
	logger                      *slog.Logger
	DeviceName                  string
	writeCharacteristicUUID     string
	notifyCharacteristicUUID    string
	bluetoothAdapter            *bluetooth.Adapter
	enableNotificationsCallback func(b []byte)
	scanTimeout                 time.Duration

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

func (c *LiTimeBluetoothClient) Connect(ctx context.Context) error {
	c.logger.Debug("enabling bluetooth adapter")
	err := c.bluetoothAdapter.Enable()
	if err != nil {
		return fmt.Errorf("failed to enable bluetooth adapter: %w", err)
	}

	scanCtx, cancel := context.WithTimeout(ctx, c.scanTimeout)
	defer cancel()

	var deviceAddress bluetooth.Address
	deviceFound := make(chan bool, 1)
	scanErr := make(chan error, 1)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		err := c.bluetoothAdapter.Scan(func(adapter *bluetooth.Adapter, device bluetooth.ScanResult) {
			c.logger.Debug("found device", slog.String("name", device.LocalName()), slog.String("address", device.Address.String()))
			if device.LocalName() == c.DeviceName {
				deviceAddress = device.Address
				adapter.StopScan()
				deviceFound <- true
			}
		})
		if err != nil {
			scanErr <- err
		}
	}()

	select {
	case <-deviceFound:
		c.bluetoothAdapter.StopScan()
	case err := <-scanErr:
		c.bluetoothAdapter.StopScan()
		wg.Wait()
		return fmt.Errorf("failed to scan for devices: %w", err)
	case <-scanCtx.Done():
		c.bluetoothAdapter.StopScan()
		wg.Wait()
		if scanCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("scan timeout: device '%s' not found within %v", c.DeviceName, c.scanTimeout)
		}
		return fmt.Errorf("scan cancelled: %w", scanCtx.Err())
	}

	wg.Wait()

	if deviceAddress.String() == "" {
		return fmt.Errorf("device '%s' not found during scan", c.DeviceName)
	}

	c.logger.Debug("found correct device", slog.String("device_name", c.DeviceName), slog.String("address", deviceAddress.String()))

	c.logger.Debug("connecting to device", slog.String("address", deviceAddress.String()))
	device, err := c.bluetoothAdapter.Connect(deviceAddress, bluetooth.ConnectionParams{})
	if err != nil {
		return fmt.Errorf("failed to connect to device: %w", err)
	}

	c.logger.Debug("discovering services for device", slog.String("address", deviceAddress.String()))
	services, err := device.DiscoverServices(nil)
	if err != nil {
		return fmt.Errorf("failed to discover services: %w", err)
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
		return fmt.Errorf("failed to discover characteristics: %w", characteristicErr)
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
		return fmt.Errorf("failed to find characteristics in map: %w", characteristicExistsErr)
	}

	c.writeCharacteristic = writeCharacteristic

	err = notifyCharacteristic.EnableNotifications(c.enableNotificationsCallback)
	if err != nil {
		return fmt.Errorf("failed to enable notifications: %w", err)
	}

	return nil
}

func (c *LiTimeBluetoothClient) QueryData() error {
	_, err := c.writeCharacteristic.WriteWithoutResponse(MagicQuery)
	if err != nil {
		return fmt.Errorf("failed to write query data: %w", err)
	}

	return nil
}
