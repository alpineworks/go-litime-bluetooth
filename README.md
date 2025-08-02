# go-litime-bluetooth

A Go library for connecting to and reading data from LiTime Bluetooth-enabled lithium batteries via BLE (Bluetooth Low Energy).

## Features

- Connect to LiTime Bluetooth batteries by device name
- Query battery data using BLE characteristics
- Parse comprehensive battery metrics including:
  - Voltage measurements (total and per-cell)
  - Current readings
  - Temperature monitoring (MOSFET and cell temperatures)
  - State of Charge (SOC) and State of Health (SOH)
  - Capacity information (remaining and full capacity)
  - Protection states and failure indicators
  - Balancing status and discharge statistics
- Configurable logging and notification callbacks

## Usage

```go
package main

import (
    "context"
    "log/slog"
    "os"
    
    golitimebluetooth "alpineworks.io/go-litime-bluetooth"
    "alpineworks.io/go-litime-bluetooth/bluetooth"
)

func main() {
    // Create a new LiTime Bluetooth client
    client := bluetooth.NewLiTimeBluetoothClient(
        "YOUR-DEVICE-NAME", // Replace with your battery's Bluetooth name
        bluetooth.WithLogger(slog.Default()),
        bluetooth.WithEnableNotificationCallback(func(data []byte) {
            // Parse the received battery data
            batteryData, err := golitimebluetooth.ParseLiTimeBatteryData(data)
            if err != nil {
                slog.Error("failed to parse battery data", slog.String("error", err.Error()))
                return
            }
            slog.Info("battery data", slog.Any("data", batteryData))
        }),
    )
    
    // Connect to the battery
    err := client.Connect(context.Background())
    if err != nil {
        slog.Error("failed to connect", slog.String("error", err.Error()))
        return
    }
    
    // Query battery data
    err = client.QueryData()
    if err != nil {
        slog.Error("failed to query data", slog.String("error", err.Error()))
        return
    }
    
    // Keep running to receive notifications
    select {}
}
```

## Configuration Options

The library supports several configuration options:

- `WithLogger(logger *slog.Logger)` - Custom logger (defaults to discarding logs)
- `WithWriteCharacteristicUUID(uuid string)` - Custom write characteristic UUID
- `WithNotifyCharacteristicUUID(uuid string)` - Custom notify characteristic UUID  
- `WithEnableNotificationCallback(callback func([]byte))` - Callback for receiving data
- `WithScanTimeout(timeout time.Duration)` - Bluetooth scan timeout (default: 30s)

## Battery Data Structure

The parsed battery data includes:

```go
type LiTimeBatteryData struct {
    TotalVoltage      float32   // Total battery voltage (V)
    CellVoltageSum    float32   // Sum of all cell voltages (V)
    Current           float32   // Current flow (A)
    MosfetTemp        int16     // MOSFET temperature (°C)
    CellTemp          int16     // Cell temperature (°C)
    RemainingAh       float32   // Remaining capacity (Ah)
    FullCapacityAh    float32   // Full capacity (Ah)
    SOC               uint16    // State of Charge (%)
    SOH               string    // State of Health (%)
    DischargesCount   uint32    // Number of discharge cycles
    DischargesAhCount float32   // Total Ah discharged
    CellVoltages      []float32 // Individual cell voltages (V)
    // ... additional state and protection fields
}
```

## Requirements

- Go 1.24+
- Bluetooth Low Energy support on the host system
- LiTime Bluetooth-enabled lithium battery

## Dependencies

This library uses [TinyGo's Bluetooth package](https://tinygo.org/x/bluetooth) for cross-platform BLE support.
