package golitimebluetooth

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// LiTimeBatteryData represents the parsed LiTime battery notification data
type LiTimeBatteryData struct {
	TotalVoltage      float32   `json:"total_voltage"`
	CellVoltageSum    float32   `json:"cell_voltage_sum"`
	Current           float32   `json:"current"`
	MosfetTemp        int16     `json:"mosfet_temp"`
	CellTemp          int16     `json:"cell_temp"`
	RemainingAh       float32   `json:"remaining_ah"`
	FullCapacityAh    float32   `json:"full_capacity_ah"`
	ProtectionState   string    `json:"protection_state"`
	HeatState         string    `json:"heat_state"`
	BalanceMemory     string    `json:"balance_memory"`
	FailureState      string    `json:"failure_state"`
	BalancingState    string    `json:"balancing_state"`
	BatteryState      string    `json:"battery_state"`
	SOC               uint16    `json:"soc"`
	SOH               string    `json:"soh"`
	DischargesCount   uint32    `json:"discharges_count"`
	DischargesAhCount float32   `json:"discharges_ah_count"`
	CellVoltages      []float32 `json:"cell_voltages"`
}

// Adapted from https://github.com/mirosieber/Litime_BMS_ESP32/blob/00f21683d08e753452acdef15c7bb8470091d132/src/BMSClient.cpp#L81-L108
func ParseLiTimeBatteryData(data []byte) (*LiTimeBatteryData, error) {
	if len(data) < 104 {
		return nil, fmt.Errorf("data length %d is less than required 104 bytes", len(data))
	}

	bms := &LiTimeBatteryData{
		CellVoltages: make([]float32, 0),
	}

	// Parse total voltage (bytes 8-11, little endian)
	bms.TotalVoltage = float32(uint32(data[11])<<24|uint32(data[10])<<16|uint32(data[9])<<8|uint32(data[8])) / 1000.0

	// Parse cell voltage sum (bytes 12-15, little endian)
	bms.CellVoltageSum = float32(uint32(data[15])<<24|uint32(data[14])<<16|uint32(data[13])<<8|uint32(data[12])) / 1000.0

	// Parse current (bytes 48-51, little endian, signed)
	currentRaw := int32(uint32(data[51])<<24 | uint32(data[50])<<16 | uint32(data[49])<<8 | uint32(data[48]))
	bms.Current = float32(currentRaw) / 1000.0

	// Parse temperatures (little endian, signed 16-bit)
	bms.MosfetTemp = int16(uint16(data[55])<<8 | uint16(data[54]))
	bms.CellTemp = int16(uint16(data[53])<<8 | uint16(data[52]))

	// Parse remaining and full capacity (little endian)
	bms.RemainingAh = float32(uint16(data[63])<<8|uint16(data[62])) / 100.0
	bms.FullCapacityAh = float32(uint16(data[65])<<8|uint16(data[64])) / 100.0

	// Parse hex string fields
	bms.ProtectionState = bytesToHexString(data, 76, 4)
	bms.HeatState = bytesToHexString(data, 68, 4)
	bms.BalanceMemory = bytesToHexString(data, 72, 4)
	bms.FailureState = bytesToHexString(data, 80, 3)
	bms.BatteryState = bytesToHexString(data, 88, 2)

	// Parse balancing state as binary string
	bms.BalancingState = bytesToBinaryString(data, 84, 4)

	// Parse SOC (little endian)
	bms.SOC = uint16(data[91])<<8 | uint16(data[90])

	// Parse SOH (little endian, format as percentage string)
	sohValue := uint32(data[95])<<24 | uint32(data[94])<<16 | uint32(data[93])<<8 | uint32(data[92])
	bms.SOH = fmt.Sprintf("%d%%", sohValue)

	// Parse discharge counts (little endian)
	bms.DischargesCount = uint32(data[99])<<24 | uint32(data[98])<<16 | uint32(data[97])<<8 | uint32(data[96])
	bms.DischargesAhCount = float32(uint32(data[103])<<24|uint32(data[102])<<16|uint32(data[101])<<8|uint32(data[100])) / 1000.0

	// Parse cell voltages (bytes 16-47, pairs of little endian 16-bit values)
	for i := 16; i < 48; i += 2 {
		if data[i] == 0 && data[i+1] == 0 {
			continue // Skip zero voltage cells
		}
		voltage := float32(uint16(data[i+1])<<8|uint16(data[i])) / 1000.0
		bms.CellVoltages = append(bms.CellVoltages, voltage)
	}

	return bms, nil
}

// bytesToHexString converts a slice of bytes to a hex string
func bytesToHexString(data []byte, start, length int) string {
	if start+length > len(data) {
		return ""
	}
	return strings.ToUpper(hex.EncodeToString(data[start : start+length]))
}

// bytesToBinaryString converts a slice of bytes to a binary string
func bytesToBinaryString(data []byte, start, length int) string {
	if start+length > len(data) {
		return ""
	}

	var result strings.Builder
	for i := start; i < start+length; i++ {
		result.WriteString(fmt.Sprintf("%08b", data[i]))
	}
	return result.String()
}
