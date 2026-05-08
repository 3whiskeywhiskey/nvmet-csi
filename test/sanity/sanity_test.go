package sanity

import (
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/kubernetes-csi/csi-test/v5/pkg/sanity"
	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/driver"
	"git.srvlab.io/whiskey/nvmet-csi/test/mock"
)

const (
	// Use port 12222 to avoid conflicts with other tests (port 2222 used elsewhere)
	mockRDSPort = 12222

	// Test socket path
	testSocketPath = "/tmp/csi-sanity-test.sock"

	// Test volume size: 10 GiB per CONTEXT.md decision
	testVolumeSize = 10 * 1024 * 1024 * 1024

	// Storage class parameters for testing
	testVolumeBasePath = "/storage-pool/metal-csi"

	// Test SSH private key (OpenSSH format, not actually used by mock but needed for parsing)
	testSSHPrivateKey = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAABFwAAAAdzc2gtcn
NhAAAAAwEAAQAAAQEAuUxbIus8fUPSxG419c2P3JAqRnA8DJe77phQZMCtAc1WXWPPv0fn
SZlYjoOqFBs6b3C5hvISxOva2R/wDvAfrMMWtUbyMKmEaYNQuoekSXOGoFsQ3bfR0INCf1
ZSQZT52kDYbUvGUjVj6VSUXkFK2UEZEh1SKrkR2EtldjTwZu8LJtticxhyqoWgRWT+vLU0
pE7SY1xFi31ybJLUr6654NpybzpBvk/kP02QUd8oDMmIFv47evtAHRoI0Ywpr4wTA6M91R
WlvZkXAYG8SdbZe8PR1S1vDXrOamHUF7dLPtUncdPmnpH4HuhknXk9DdzRCH8EEQ2zX5rk
cM9fV7jsHQAAA8ivcOOYr3DjmAAAAAdzc2gtcnNhAAABAQC5TFsi6zx9Q9LEbjX1zY/ckC
pGcDwMl7vumFBkwK0BzVZdY8+/R+dJmViOg6oUGzpvcLmG8hLE69rZH/AO8B+swxa1RvIw
qYRpg1C6h6RJc4agWxDdt9HQg0J/VlJBlPnaQNhtS8ZSNWPpVJReQUrZQRkSHVIquRHYS2
V2NPBm7wsm22JzGHKqhaBFZP68tTSkTtJjXEWLfXJsktSvrrng2nJvOkG+T+Q/TZBR3ygM
yYgW/jt6+0AdGgjRjCmvjBMDoz3VFaW9mRcBgbxJ1tl7w9HVLW8Nes5qYdQXt0s+1Sdx0+
aekfge6GSdeT0N3NEIfwQRDbNfmuRwz19XuOwdAAAAAwEAAQAAAQEAllRFJ/oyk+nfZ6+G
JYoE6csoEQdjIFBFjpeRuXu7oFendpLQa335PXOkLdLRvAgvC1QnoDxqT8qNPVO03VmgSP
fpR15shGAy5as8Zmg/N7v6/8OB1m8YUJL88vPkPgKQBapQK7OrDOz1xsnwqNtNzx4KKfER
xUlqGdFpWlIuq0Joj19iUrCObp0NAD9YqvfB1KyCnCXwaMnhUT9rw7ZDdG6x4sxbwT2gDs
fsuaxb8CZfcOoC4CwHhBvnOHR7eFvqsOv4QZJ9LkuTLV1DGQragGOy3HS3Obbu4ePhbTkN
+ab1GPq+eZMQmDh4+BCDGWgcpcATH0UyVvPXrD0jG5GFvQAAAIATHVBodA7W+pD+aG6ck/
g+hqoXTzRYaD4RdmKU9YQztjvYUQsnfGjYMnMwnN7tYeCXFDYgzPbUc2JUkxAqNmqMof/d
07FoG8Bfu4GAvxDtpGrdYSbOYYXiD6/Oosb0+5ayhcT2uZFWxIy23+Q/Cpcm6qjSSUQGZU
EafLsnKNrvtwAAAIEA3lw8ZXQBmnTI3VJEJuJcM6v7dnzkE/n6ifMhQicaXoUni51Abfki
sA0yaQF1PTIwMOVjGOB7I3DUNIGDLEx79+HnGdSpLY9RNNQe0ZCdvp/akyRY7bTLJqnUZC
xC1e1p1LcjMph+hdDHLlQpS1dH1M4lhkuBdTTLrLx9gQcr6a8AAACBANVUv9OwimPx/Efb
ZPCXDHVgUJbOfmll0fUjBMMJm3tlx8b49xqrrOWO0Nk9GdztTF4nlsYg/563Vw4kU9T7S+
krQ96352S167OB47OvBUozv0hKcJlU9W3TYmt/zXC3asLS4Xhyv7/YxI3wGIs9TNDAKPaT
AUTw5BKMQuNGfVXzAAAADHRlc3RAcmRzLWNzaQECAwQFBg==
-----END OPENSSH PRIVATE KEY-----`
)

// TestCSISanity runs the official CSI sanity test suite against the nvmet-csi driver
// with mock RDS and NVMe backends for fast and reliable testing.
//
// This validates:
//   - Identity service: GetPluginInfo, GetPluginCapabilities, Probe
//   - Controller service: CreateVolume, DeleteVolume, ValidateVolumeCapabilities,
//     GetCapacity, ControllerExpandVolume, ListVolumes
//   - Node service: NodeStageVolume, NodeUnstageVolume, NodePublishVolume,
//     NodeUnpublishVolume, NodeGetCapabilities, NodeGetInfo
//   - Idempotency: CreateVolume/DeleteVolume called multiple times
//
// Both controller and node services are tested using mocks (no real hardware needed).
func TestCSISanity(t *testing.T) {
	// Setup logging for test visibility
	klog.SetOutput(os.Stdout)

	// Start mock RDS server
	t.Log("Starting mock RDS server...")
	mockRDS, err := mock.NewMockRDSServer(mockRDSPort)
	if err != nil {
		t.Fatalf("Failed to create mock RDS server: %v", err)
	}

	if err := mockRDS.Start(); err != nil {
		t.Fatalf("Failed to start mock RDS server: %v", err)
	}
	defer func() {
		t.Log("Stopping mock RDS server...")
		if err := mockRDS.Stop(); err != nil {
			t.Errorf("Failed to stop mock RDS server: %v", err)
		}
	}()

	// Wait for mock RDS to be ready
	time.Sleep(500 * time.Millisecond)

	// Create mock NVMe connector for node service testing
	t.Log("Creating mock NVMe connector...")
	mockNVMe := mock.NewMockNVMEConnector()

	// Create mock mounter for node service testing
	t.Log("Creating mock mounter...")
	mockMounter := mock.NewMockMounter()

	// Create driver with both controller and node services enabled
	t.Log("Creating CSI driver with mock RDS and NVMe...")
	driverConfig := driver.DriverConfig{
		DriverName:            "nvmet.csi.whiskey.works",
		Version:               "test",
		NodeID:                "test-node-1", // Node ID required for node service
		RDSAddress:            mockRDS.Address(),
		RDSPort:               mockRDS.Port(),
		RDSUser:               "admin",
		RDSPrivateKey:         []byte(testSSHPrivateKey), // Valid RSA key format for parsing
		RDSInsecureSkipVerify: true,                      // Skip host key verification for mock
		RDSVolumeBasePath:     testVolumeBasePath,
		ManagedNQNPrefix:      "nqn.2000-02.com.mikrotik:", // Required for node service (NVMe format requires colon)
		EnableController:      true,
		EnableNode:            true, // Enable node service with mock NVMe connector
		K8sClient:             nil,  // Not needed for basic sanity tests
		Metrics:               nil,  // Not needed for testing
	}

	drv, err := driver.NewDriver(driverConfig)
	if err != nil {
		t.Fatalf("Failed to create driver: %v", err)
	}

	// Inject mock NVMe connector and mounter for testing
	drv.SetNVMEConnector(mockNVMe)
	drv.SetMounter(mockMounter)

	// Inject mock getMountDev function to prevent stale mount false positives
	// The mock mounter tracks mounts but they're not in /proc/mountinfo
	drv.SetGetMountDevFunc(mockMounter.GetMountDevice)

	// Remove old socket if exists
	_ = os.Remove(testSocketPath)

	// Start driver on Unix socket in background
	t.Logf("Starting driver on %s...", testSocketPath)
	endpoint := fmt.Sprintf("unix://%s", testSocketPath)

	// Run driver in goroutine (in-process pattern from RESEARCH.md)
	// The driver will run until the test completes
	go func() {
		if err := drv.Run(endpoint); err != nil {
			t.Logf("Driver stopped: %v", err)
		}
	}()

	// Wait for socket to be created
	t.Log("Waiting for CSI socket to be ready...")
	socketReady := false
	for i := 0; i < 30; i++ {
		if _, err := os.Stat(testSocketPath); err == nil {
			// Verify we can connect to the socket
			conn, err := net.Dial("unix", testSocketPath)
			if err == nil {
				conn.Close()
				socketReady = true
				t.Log("CSI socket is ready")
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	if !socketReady {
		t.Fatalf("CSI socket not ready after 3 seconds")
	}

	// Cleanup socket and test directories after test
	defer func() {
		t.Log("Cleaning up CSI socket...")
		_ = os.Remove(testSocketPath)
		// Clean up sanity test directories to prevent next run failures
		_ = os.RemoveAll("/tmp/csi-target")
		_ = os.RemoveAll("/tmp/csi-staging")
	}()

	// Configure CSI sanity tests
	t.Log("Configuring CSI sanity tests...")
	config := sanity.NewTestConfig()

	// Connection settings
	config.Address = endpoint
	config.ControllerAddress = "" // Use same endpoint for both

	// Test volume configuration (10 GiB per CONTEXT.md)
	config.TestVolumeSize = testVolumeSize
	config.TestVolumeExpandSize = 20 * 1024 * 1024 * 1024 // 20 GiB for expansion tests

	// Idempotency testing - critical per CONTEXT.md
	// This causes sanity to call CreateVolume/DeleteVolume twice with same params
	config.IdempotentCount = 2

	// StorageClass parameters for CreateVolume
	config.TestVolumeParameters = map[string]string{
		"volumePath": testVolumeBasePath,
		"nvmePort":   "4420",
	}

	// Snapshot test parameters (enables snapshot sanity test suite)
	// copy-from approach needs no special StorageClass parameters
	config.TestSnapshotParameters = map[string]string{}

	// Staging/target paths (not used for controller-only tests, but required by config)
	config.TargetPath = "/tmp/csi-target"
	config.StagingPath = "/tmp/csi-staging"

	// Run sanity tests
	t.Log("Running CSI sanity tests...")
	t.Log("Testing both Controller and Node services with mocks")

	// Run full sanity test suite with mocked backends
	sanity.Test(t, config)

	// If we get here, all sanity tests passed
	t.Log("CSI sanity tests completed successfully")
}
