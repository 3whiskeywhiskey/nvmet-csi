package rds

import (
	"fmt"
	"testing"

	"github.com/gosnmp/gosnmp"
)

func TestParseFloat64(t *testing.T) {
	tests := []struct {
		name     string
		pduType  gosnmp.Asn1BER
		pduValue interface{}
		expected float64
	}{
		{"Integer", gosnmp.Integer, 42, 42.0},
		{"Gauge32", gosnmp.Gauge32, uint(7500), 7500.0},
		{"Counter32", gosnmp.Counter32, uint(1000), 1000.0},
		{"Unknown type", gosnmp.OctetString, "test", 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pdu := gosnmp.SnmpPDU{
				Type:  tt.pduType,
				Value: tt.pduValue,
			}
			result := parseFloat64(pdu)
			if result != tt.expected {
				t.Errorf("parseFloat64() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestMockClient_GetHardwareHealth(t *testing.T) {
	client := NewMockClient()

	// Test default response
	health, err := client.GetHardwareHealth("203.0.113.1", "public")
	if err != nil {
		t.Fatalf("GetHardwareHealth() error = %v", err)
	}
	if health.CPUTemperature != 40 {
		t.Errorf("CPUTemperature = %v, want 40", health.CPUTemperature)
	}
	if health.DiskPoolSizeBytes != 8_000_000_000_000 {
		t.Errorf("DiskPoolSizeBytes = %v, want 8TB", health.DiskPoolSizeBytes)
	}

	// Test custom response
	custom := &HardwareHealthMetrics{
		CPUTemperature:    55,
		Fan1Speed:         8000,
		DiskPoolSizeBytes: 10_000_000_000_000,
		DiskPoolUsedBytes: 5_000_000_000_000,
	}
	client.SetHardwareHealth(custom)

	health, err = client.GetHardwareHealth("203.0.113.1", "public")
	if err != nil {
		t.Fatalf("GetHardwareHealth() error = %v", err)
	}
	if health.CPUTemperature != 55 {
		t.Errorf("CPUTemperature = %v, want 55", health.CPUTemperature)
	}
	if health.Fan1Speed != 8000 {
		t.Errorf("Fan1Speed = %v, want 8000", health.Fan1Speed)
	}

	// Test error injection
	client.SetError(fmt.Errorf("SNMP timeout"))
	_, err = client.GetHardwareHealth("203.0.113.1", "public")
	if err == nil || err.Error() != "SNMP timeout" {
		t.Errorf("Expected SNMP timeout error, got %v", err)
	}
}
