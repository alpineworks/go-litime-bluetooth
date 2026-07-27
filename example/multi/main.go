// Command multi discovers every LiTime battery in range, connects to all of
// them at once, and polls each on an interval.
//
// Run it with no arguments to list what is in range along with each device's
// address:
//
//	go run ./example/multi
//
// Then pass those addresses to connect to a specific set:
//
//	go run ./example/multi 11:22:33:AA:BB:CC 11:22:33:AA:BB:CD
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	golitimebluetooth "alpineworks.io/go-litime-bluetooth"
	"alpineworks.io/go-litime-bluetooth/bluetooth"
	tinygobluetooth "tinygo.org/x/bluetooth"
)

const pollInterval = 10 * time.Second

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	addresses, err := resolveAddresses(ctx, os.Args[1:])
	if err != nil {
		slog.Error("could not resolve addresses", slog.String("error", err.Error()))
		os.Exit(1)
	}

	if len(addresses) == 0 {
		slog.Warn("no devices to connect to")
		return
	}

	// Connect to every battery. Scanning is already done, and connecting is
	// per-device work that the adapter is happy to do in parallel.
	var wg sync.WaitGroup
	clients := make([]*bluetooth.LiTimeBluetoothClient, len(addresses))

	for i, address := range addresses {
		wg.Add(1)
		go func() {
			defer wg.Done()

			label := address.String()
			client := bluetooth.NewLiTimeBluetoothClient("",
				bluetooth.WithAddress(address),
				bluetooth.WithLogger(slog.Default().With(slog.String("battery", label))),
				bluetooth.WithEnableNotificationCallback(func(b []byte) {
					data, err := golitimebluetooth.ParseLiTimeBatteryData(b)
					if err != nil {
						slog.Error("failed to parse notification data",
							slog.String("battery", label),
							slog.String("error", err.Error()))
						return
					}

					slog.Info("received notification data",
						slog.String("battery", label),
						slog.Any("data", data))
				}),
			)

			if err := client.Connect(ctx); err != nil {
				slog.Error("failed to connect",
					slog.String("battery", label),
					slog.String("error", err.Error()))
				return
			}

			slog.Info("connected", slog.String("battery", label))
			clients[i] = client
		}()
	}
	wg.Wait()

	defer func() {
		for _, client := range clients {
			if client != nil {
				_ = client.Disconnect()
			}
		}
	}()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			return
		case <-ticker.C:
			for _, client := range clients {
				if client == nil {
					continue
				}

				if err := client.QueryData(); err != nil {
					address, _ := client.Address()
					slog.Error("failed to query battery",
						slog.String("battery", address.String()),
						slog.String("error", err.Error()))
				}
			}
		}
	}
}

// resolveAddresses parses addresses given on the command line, or scans for
// every LiTime battery in range when none were supplied.
func resolveAddresses(ctx context.Context, args []string) ([]tinygobluetooth.Address, error) {
	if len(args) > 0 {
		addresses := make([]tinygobluetooth.Address, 0, len(args))
		for _, arg := range args {
			address, err := bluetooth.ParseAddress(arg)
			if err != nil {
				return nil, err
			}
			addresses = append(addresses, address)
		}

		return addresses, nil
	}

	slog.Info("no addresses given, scanning for devices")

	// One scan finds everything. Scanning once per battery would not work: an
	// adapter runs a single scan at a time.
	devices, err := bluetooth.ScanForDevices(ctx,
		bluetooth.ScanWithLogger(slog.Default()),
		bluetooth.ScanWithTimeout(15*time.Second),
	)
	if err != nil {
		return nil, err
	}

	for _, device := range devices {
		slog.Info("found device",
			slog.String("name", device.Name),
			slog.String("address", device.Address.String()),
			slog.Int("rssi", int(device.RSSI)))
	}

	// A bare scan reports every BLE peripheral around, not just batteries, so
	// nothing is connected to on this pass: the operator picks the right ones
	// and passes them back in.
	slog.Info("re-run with the addresses of the batteries you want to poll",
		slog.Int("devices_found", len(devices)))

	return nil, nil
}
