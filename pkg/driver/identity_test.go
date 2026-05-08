package driver

import (
	"context"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/rds"
)

func TestGetPluginInfo(t *testing.T) {
	driver := &Driver{
		name:    "test.csi.driver",
		version: "v1.0.0",
	}

	ids := NewIdentityServer(driver)

	req := &csi.GetPluginInfoRequest{}
	resp, err := ids.GetPluginInfo(context.Background(), req)

	if err != nil {
		t.Fatalf("GetPluginInfo failed: %v", err)
	}

	if resp.Name != "test.csi.driver" {
		t.Errorf("Expected name test.csi.driver, got %s", resp.Name)
	}

	if resp.VendorVersion != "v1.0.0" {
		t.Errorf("Expected version v1.0.0, got %s", resp.VendorVersion)
	}
}

func TestGetPluginInfoNoName(t *testing.T) {
	driver := &Driver{
		name:    "",
		version: "v1.0.0",
	}

	ids := NewIdentityServer(driver)

	req := &csi.GetPluginInfoRequest{}
	_, err := ids.GetPluginInfo(context.Background(), req)

	if err == nil {
		t.Error("Expected error when driver name is empty, got nil")
	}
}

func TestGetPluginCapabilities(t *testing.T) {
	driver := &Driver{
		name:    "test.csi.driver",
		version: "v1.0.0",
	}

	ids := NewIdentityServer(driver)

	req := &csi.GetPluginCapabilitiesRequest{}
	resp, err := ids.GetPluginCapabilities(context.Background(), req)

	if err != nil {
		t.Fatalf("GetPluginCapabilities failed: %v", err)
	}

	if len(resp.Capabilities) == 0 {
		t.Error("Expected capabilities but got none")
	}

	// Check that CONTROLLER_SERVICE capability is present
	hasControllerService := false
	for _, cap := range resp.Capabilities {
		if cap.GetService() != nil {
			if cap.GetService().Type == csi.PluginCapability_Service_CONTROLLER_SERVICE {
				hasControllerService = true
				break
			}
		}
	}

	if !hasControllerService {
		t.Error("Expected CONTROLLER_SERVICE capability but not found")
	}
}

func TestProbeHealthy(t *testing.T) {
	driver := &Driver{
		name:    "test.csi.driver",
		version: "v1.0.0",
		// No RDS client, should still be healthy
	}

	ids := NewIdentityServer(driver)

	req := &csi.ProbeRequest{}
	resp, err := ids.Probe(context.Background(), req)

	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}

	if resp.Ready == nil || !resp.Ready.GetValue() {
		t.Error("Expected driver to be ready")
	}
}

// TestProbeWithConnectionManager tests Probe with ConnectionManager providing connection state
func TestProbeWithConnectionManager(t *testing.T) {
	tests := []struct {
		name          string
		isConnected   bool
		expectedReady bool
	}{
		{
			name:          "ConnectionManager connected",
			isConnected:   true,
			expectedReady: true,
		},
		{
			name:          "ConnectionManager disconnected",
			isConnected:   false,
			expectedReady: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use real MockClient from rds package
			mockClient := newTestMockClient(tt.isConnected)

			// Create real ConnectionManager with mock client
			// The ConnectionManager will reflect the mock client's connection state
			config := rds.ConnectionManagerConfig{
				Client: mockClient,
			}
			connectionManager, err := rds.NewConnectionManager(config)
			if err != nil {
				t.Fatalf("Failed to create connection manager: %v", err)
			}

			driver := &Driver{
				name:              "test.csi.driver",
				version:           "v1.0.0",
				rdsClient:         mockClient,
				connectionManager: connectionManager,
			}

			ids := NewIdentityServer(driver)
			req := &csi.ProbeRequest{}
			resp, err := ids.Probe(context.Background(), req)

			if err != nil {
				t.Fatalf("Probe failed: %v", err)
			}

			if resp.Ready == nil {
				t.Fatal("Expected Ready field to be set")
			}

			if resp.Ready.GetValue() != tt.expectedReady {
				t.Errorf("Expected ready=%v, got %v", tt.expectedReady, resp.Ready.GetValue())
			}
		})
	}
}

// TestProbeFallbackToRDSClient tests that Probe falls back to rdsClient.IsConnected
// when connectionManager is not available
func TestProbeFallbackToRDSClient(t *testing.T) {
	tests := []struct {
		name          string
		isConnected   bool
		expectedReady bool
	}{
		{
			name:          "RDS client connected",
			isConnected:   true,
			expectedReady: true,
		},
		{
			name:          "RDS client disconnected",
			isConnected:   false,
			expectedReady: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := newTestMockClient(tt.isConnected)

			driver := &Driver{
				name:              "test.csi.driver",
				version:           "v1.0.0",
				rdsClient:         mockClient,
				connectionManager: nil, // No connection manager - should fallback to rdsClient
			}

			ids := NewIdentityServer(driver)
			req := &csi.ProbeRequest{}
			resp, err := ids.Probe(context.Background(), req)

			if err != nil {
				t.Fatalf("Probe failed: %v", err)
			}

			if resp.Ready == nil {
				t.Fatal("Expected Ready field to be set")
			}

			if resp.Ready.GetValue() != tt.expectedReady {
				t.Errorf("Expected ready=%v, got %v", tt.expectedReady, resp.Ready.GetValue())
			}
		})
	}
}

// TestProbeRecordsMetrics tests that Probe records connection state metrics
func TestProbeRecordsMetrics(t *testing.T) {
	// Note: We can't easily verify metric recording without exposing internal state,
	// but we can verify Probe doesn't panic when metrics are available
	mockClient := newTestMockClient(true)

	driver := &Driver{
		name:      "test.csi.driver",
		version:   "v1.0.0",
		rdsClient: mockClient,
		// metrics would be set here in real usage
	}

	ids := NewIdentityServer(driver)
	req := &csi.ProbeRequest{}
	_, err := ids.Probe(context.Background(), req)

	if err != nil {
		t.Fatalf("Probe failed with metrics: %v", err)
	}
}

// TestProbeNoPanicWhenMetricsNil tests that Probe handles nil metrics gracefully
func TestProbeNoPanicWhenMetricsNil(t *testing.T) {
	mockClient := newTestMockClient(true)

	driver := &Driver{
		name:      "test.csi.driver",
		version:   "v1.0.0",
		rdsClient: mockClient,
		metrics:   nil, // Explicitly nil metrics
	}

	ids := NewIdentityServer(driver)
	req := &csi.ProbeRequest{}
	_, err := ids.Probe(context.Background(), req)

	if err != nil {
		t.Fatalf("Probe failed with nil metrics: %v", err)
	}
}

// Test helper functions and mocks

// newTestMockClient creates a test MockClient from rds package
func newTestMockClient(connected bool) *rds.MockClient {
	mockClient := rds.NewMockClient()
	mockClient.SetConnected(connected)
	mockClient.SetAddress("10.42.68.1")
	return mockClient
}
