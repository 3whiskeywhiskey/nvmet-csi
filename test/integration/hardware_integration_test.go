package integration

import (
	"os"
	"strconv"
	"testing"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/rds"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
)

// TestHardwareIntegration tests against a real RDS server
// Requires environment variables to be set:
//   - RDS_ADDRESS: RDS server IP address
//   - RDS_USER: SSH username (e.g., "admin")
//   - RDS_PRIVATE_KEY_PATH: Path to SSH private key
//   - RDS_PORT: (optional) SSH port, defaults to 22
//   - RDS_VOLUME_BASE_PATH: (optional) Base path for volumes
func TestHardwareIntegration(t *testing.T) {
	// Check if hardware test is enabled
	address := os.Getenv("RDS_ADDRESS")
	if address == "" {
		t.Skip("Skipping hardware integration test: RDS_ADDRESS not set")
	}

	user := os.Getenv("RDS_USER")
	if user == "" {
		t.Skip("Skipping hardware integration test: RDS_USER not set")
	}

	privateKeyPath := os.Getenv("RDS_PRIVATE_KEY_PATH")
	if privateKeyPath == "" {
		t.Skip("Skipping hardware integration test: RDS_PRIVATE_KEY_PATH not set")
	}

	// Read private key
	privateKey, err := os.ReadFile(privateKeyPath)
	if err != nil {
		t.Fatalf("Failed to read private key from %s: %v", privateKeyPath, err)
	}

	// Get optional configuration
	port := 22
	if portStr := os.Getenv("RDS_PORT"); portStr != "" {
		if parsedPort, err := strconv.Atoi(portStr); err == nil {
			if parsedPort > 0 && parsedPort <= 65535 {
				port = parsedPort
			} else {
				t.Logf("Warning: RDS_PORT %d out of range, using default port 22", parsedPort)
			}
		} else {
			t.Logf("Warning: RDS_PORT '%s' is not a valid number, using default port 22", portStr)
		}
	}

	volumeBasePath := "/storage-pool/kubernetes-volumes"
	if basePath := os.Getenv("RDS_VOLUME_BASE_PATH"); basePath != "" {
		volumeBasePath = basePath
	}

	// Set up allowed base paths for testing
	utils.ResetAllowedBasePaths()
	if err := utils.SetAllowedBasePath(volumeBasePath); err != nil {
		t.Fatalf("Failed to set allowed base path: %v", err)
	}
	t.Cleanup(utils.ResetAllowedBasePaths)

	t.Logf("Testing with real RDS hardware:")
	t.Logf("  Address: %s", address)
	t.Logf("  Port: %d", port)
	t.Logf("  User: %s", user)
	t.Logf("  Base Path: %s", volumeBasePath)
	t.Logf("  Private Key: %s", privateKeyPath)

	// Create RDS client
	config := rds.ClientConfig{
		Address:    address,
		Port:       port,
		User:       user,
		PrivateKey: privateKey,
	}

	client, err := rds.NewClient(config)
	if err != nil {
		t.Fatalf("Failed to create RDS client: %v", err)
	}

	// Connect to RDS
	t.Run("Connect", func(t *testing.T) {
		err := client.Connect()
		if err != nil {
			t.Fatalf("Failed to connect to RDS at %s:%d: %v", address, port, err)
		}
		t.Logf("✅ Successfully connected to RDS")
	})
	defer func() { _ = client.Close() }()

	// Test connection status
	t.Run("IsConnected", func(t *testing.T) {
		if !client.IsConnected() {
			t.Fatal("Expected client to be connected")
		}
		t.Logf("✅ Connection status verified")
	})

	// Test volume operations
	testVolumeID := "test-hardware-volume-001"

	t.Run("CreateVolume", func(t *testing.T) {
		opts := rds.CreateVolumeOptions{
			Slot:          testVolumeID,
			FilePath:      volumeBasePath + "/" + testVolumeID + ".img",
			FileSizeBytes: 1 * 1024 * 1024 * 1024, // 1 GiB
			NVMETCPPort:   4420,
			NVMETCPNQN:    "nqn.2024-01.io.srvlab:test-volume",
		}

		err := client.CreateVolume(opts)
		if err != nil {
			t.Fatalf("Failed to create volume: %v", err)
		}
		t.Logf("✅ Successfully created volume: %s", testVolumeID)
	})

	t.Run("GetVolume", func(t *testing.T) {
		vol, err := client.GetVolume(testVolumeID)
		if err != nil {
			t.Fatalf("Failed to get volume: %v", err)
		}

		if vol.Slot != testVolumeID {
			t.Errorf("Expected slot %s, got %s", testVolumeID, vol.Slot)
		}

		if vol.FileSizeBytes != 1*1024*1024*1024 {
			t.Errorf("Expected file size 1073741824, got %d", vol.FileSizeBytes)
		}

		if !vol.NVMETCPExport {
			t.Error("Expected volume to be exported via NVMe/TCP")
		}

		t.Logf("✅ Successfully retrieved volume info")
		t.Logf("   Slot: %s", vol.Slot)
		t.Logf("   File Path: %s", vol.FilePath)
		t.Logf("   File Size: %d bytes", vol.FileSizeBytes)
		t.Logf("   NVMe TCP Port: %d", vol.NVMETCPPort)
		t.Logf("   NVMe TCP NQN: %s", vol.NVMETCPNQN)
		t.Logf("   NVMe TCP Export: %v", vol.NVMETCPExport)
	})

	t.Run("VerifyVolumeExists", func(t *testing.T) {
		err := client.VerifyVolumeExists(testVolumeID)
		if err != nil {
			t.Fatalf("Failed to verify volume exists: %v", err)
		}
		t.Logf("✅ Volume existence verified")
	})

	t.Run("ListVolumes", func(t *testing.T) {
		volumes, err := client.ListVolumes()
		if err != nil {
			t.Fatalf("Failed to list volumes: %v", err)
		}

		found := false
		for _, vol := range volumes {
			if vol.Slot == testVolumeID {
				found = true
				break
			}
		}

		if !found {
			t.Errorf("Expected to find volume %s in list", testVolumeID)
		}

		t.Logf("✅ Successfully listed %d volumes", len(volumes))
	})

	t.Run("GetCapacity", func(t *testing.T) {
		capacity, err := client.GetCapacity(volumeBasePath)
		if err != nil {
			t.Fatalf("Failed to get capacity: %v", err)
		}

		if capacity.TotalBytes == 0 {
			t.Error("Expected non-zero total capacity")
		}

		t.Logf("✅ Capacity information:")
		t.Logf("   Total: %d bytes (%.2f GB)", capacity.TotalBytes, float64(capacity.TotalBytes)/(1024*1024*1024))
		t.Logf("   Used: %d bytes (%.2f GB)", capacity.UsedBytes, float64(capacity.UsedBytes)/(1024*1024*1024))
		t.Logf("   Free: %d bytes (%.2f GB)", capacity.FreeBytes, float64(capacity.FreeBytes)/(1024*1024*1024))
	})

	t.Run("DeleteVolume", func(t *testing.T) {
		err := client.DeleteVolume(testVolumeID)
		if err != nil {
			t.Fatalf("Failed to delete volume: %v", err)
		}
		t.Logf("✅ Successfully deleted volume: %s", testVolumeID)
	})

	t.Run("VerifyVolumeDeleted", func(t *testing.T) {
		_, err := client.GetVolume(testVolumeID)
		if err == nil {
			t.Error("Expected error when getting deleted volume")
		}
		t.Logf("✅ Verified volume was deleted")
	})
}
