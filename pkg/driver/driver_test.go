package driver

import (
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes/fake"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/attachment"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/observability"
)

// TestAttachmentManager_SetMetricsMethod verifies the SetMetrics method exists and works.
// This is the critical method that enables migration metrics recording.
func TestAttachmentManager_SetMetricsMethod(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	am := attachment.NewAttachmentManager(k8sClient, DriverName)

	if am == nil {
		t.Fatal("Failed to create AttachmentManager")
	}

	// Verify SetMetrics method can be called without panic
	metrics := observability.NewMetrics()
	am.SetMetrics(metrics)

	// The method is a simple setter, so if we got here without panic, it works.
	// The actual metrics recording is tested in prometheus_test.go
	// and the wiring in driver.go is verified by code inspection (line 188-189).
}

// TestAttachmentManager_SetMetricsNil verifies SetMetrics handles nil gracefully.
func TestAttachmentManager_SetMetricsNil(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	am := attachment.NewAttachmentManager(k8sClient, DriverName)

	// SetMetrics with nil should not panic (even though it's pointless)
	am.SetMetrics(nil)
}

// TestNewDriver_NodeModeSkipsAttachmentManager verifies that node-only mode
// does not create an attachment manager (controller-only component).
func TestNewDriver_NodeModeSkipsAttachmentManager(t *testing.T) {
	config := DriverConfig{
		DriverName:            "nvmet.csi.whiskey.works",
		NodeID:                "test-node",
		EnableController:      false, // Node mode only
		EnableNode:            true,
		K8sClient:             fake.NewSimpleClientset(),
		Metrics:               observability.NewMetrics(),
		ManagedNQNPrefix:      "nqn.2000-02.com.example:csi",
		RDSAddress:            "10.0.0.1",
		RDSPort:               4420,
		RDSInsecureSkipVerify: true,
	}

	driver, err := NewDriver(config)
	if err != nil {
		t.Fatalf("NewDriver failed: %v", err)
	}

	if driver.attachmentManager != nil {
		t.Error("AttachmentManager should not be created in node-only mode")
	}
}

func TestNewDriver_ControllerModeWithoutRDSStillEnablesControllerService(t *testing.T) {
	config := DriverConfig{
		DriverName:       "nvmet.csi.whiskey.works",
		EnableController: true,
		EnableNode:       false,
		K8sClient:        fake.NewSimpleClientset(),
	}

	driver, err := NewDriver(config)
	if err != nil {
		t.Fatalf("NewDriver failed: %v", err)
	}

	if !driver.enableController {
		t.Fatal("controller mode should remain enabled without a default RDS backend")
	}
	if len(driver.cscaps) == 0 {
		t.Fatal("controller capabilities should be configured when controller mode is enabled")
	}

	driver.ids = NewIdentityServer(driver)
	if driver.enableController {
		driver.cs = NewControllerServer(driver)
	}
	if driver.cs == nil {
		t.Fatal("controller service should be registered even when no default backend is configured")
	}
}

func TestNewDriver_OrphanReconcilerRequiresRDSBackend(t *testing.T) {
	config := DriverConfig{
		DriverName:             "nvmet.csi.whiskey.works",
		EnableController:       true,
		K8sClient:              fake.NewSimpleClientset(),
		EnableOrphanReconciler: true,
		OrphanDryRun:           true,
		OrphanMinConfirmations: 2,
	}

	_, err := NewDriver(config)
	if err == nil {
		t.Fatal("expected NewDriver to reject orphan reconciler without RDS backend")
	}
	if !strings.Contains(err.Error(), "requires RouterOS/RDS backend configuration") {
		t.Fatalf("expected RDS backend error, got %v", err)
	}
}

func TestNewDriver_ActiveOrphanCleanupRequiresConfirmations(t *testing.T) {
	config := DriverConfig{
		DriverName:             "nvmet.csi.whiskey.works",
		EnableController:       true,
		K8sClient:              fake.NewSimpleClientset(),
		EnableOrphanReconciler: true,
		RDSAddress:             "10.0.0.1",
		OrphanDryRun:           false,
		OrphanMinConfirmations: 1,
	}

	_, err := NewDriver(config)
	if err == nil {
		t.Fatal("expected NewDriver to reject active orphan cleanup with too few confirmations")
	}
	if !strings.Contains(err.Error(), "requires orphan min confirmations >= 2") {
		t.Fatalf("expected min confirmations error, got %v", err)
	}
}

// Note: Testing the full controller mode initialization with AttachmentManager
// requires a valid RDS connection (SSH), which is not feasible in unit tests.
// The metrics wiring in driver.go (lines 188-189) is verified by:
// 1. Code inspection: SetMetrics is called when config.Metrics != nil
// 2. This test: SetMetrics method works correctly
// 3. prometheus_test.go: RecordMigration* methods work correctly
// 4. Manual testing: Full integration test with real driver

// TestNewDriver_NodeWatcherNilWhenControllerDisabled verifies that node watcher
// is not created when controller mode is disabled (node-only mode).
func TestNewDriver_NodeWatcherNilWhenControllerDisabled(t *testing.T) {
	config := DriverConfig{
		DriverName:            "nvmet.csi.whiskey.works",
		NodeID:                "test-node",
		EnableController:      false, // Node mode only
		EnableNode:            true,
		K8sClient:             fake.NewSimpleClientset(),
		Metrics:               observability.NewMetrics(),
		ManagedNQNPrefix:      "nqn.2000-02.com.example:csi",
		RDSAddress:            "10.0.0.1",
		RDSPort:               4420,
		RDSInsecureSkipVerify: true,
	}

	driver, err := NewDriver(config)
	if err != nil {
		t.Fatalf("NewDriver failed: %v", err)
	}

	if driver.nodeWatcher != nil {
		t.Error("NodeWatcher should not be created in node-only mode")
	}

	if driver.connectionManager != nil {
		t.Error("ConnectionManager should not be created in node-only mode")
	}
}

// TestNewDriver_ConnectionManagerNilWhenNoRDSClient verifies that connection manager
// is not created when RDS client is nil (should not happen in practice, but safety check).
func TestNewDriver_ConnectionManagerNilWhenNoRDSClient(t *testing.T) {
	config := DriverConfig{
		DriverName:            "nvmet.csi.whiskey.works",
		NodeID:                "test-node",
		EnableController:      false, // No controller = no RDS client
		EnableNode:            true,
		K8sClient:             fake.NewSimpleClientset(),
		Metrics:               observability.NewMetrics(),
		ManagedNQNPrefix:      "nqn.2000-02.com.example:csi",
		RDSAddress:            "10.0.0.1",
		RDSPort:               4420,
		RDSInsecureSkipVerify: true,
	}

	driver, err := NewDriver(config)
	if err != nil {
		t.Fatalf("NewDriver failed: %v", err)
	}

	if driver.connectionManager != nil {
		t.Error("ConnectionManager should be nil when RDS client is not initialized")
	}
}
