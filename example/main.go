package main

import (
	"context"
	"log/slog"
	"os"

	golitimebluetooth "alpineworks.io/go-litime-bluetooth"
	"alpineworks.io/go-litime-bluetooth/bluetooth"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	liTimeClient := bluetooth.NewLiTimeBluetoothClient("L-12100BNNA70-A19081", bluetooth.WithLogger(slog.Default()),
		bluetooth.WithEnableNotificationCallback(func(b []byte) {
			data, err := golitimebluetooth.ParseLiTimeBatteryData(b)
			if err != nil {
				slog.Error("failed to parse notification data", slog.String("error", err.Error()))
				return
			}
			slog.Info("received notification data", slog.Any("data", data))
		}),
	)

	err := liTimeClient.Connect(context.Background())
	if err != nil {
		slog.Error("failed to connect to LiTime Bluetooth client", slog.String("error", err.Error()))
		return
	}

	slog.Info("connected to LiTime Bluetooth client", slog.String("device_name", liTimeClient.DeviceName))
	err = liTimeClient.QueryData()
	if err != nil {
		slog.Error("failed to query data from LiTime Bluetooth client", slog.String("error", err.Error()))
		return
	}
	slog.Info("data queried successfully from LiTime Bluetooth client")

	select {}
}
