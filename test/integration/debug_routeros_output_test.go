package integration

import (
	"os"
	"testing"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/rds"
)

// TestDebugRouterOSOutput shows the actual RouterOS command output
func TestDebugRouterOSOutput(t *testing.T) {
	// Check if hardware test is enabled
	address := os.Getenv("RDS_ADDRESS")
	if address == "" {
		t.Skip("Skipping: RDS_ADDRESS not set")
	}

	user := os.Getenv("RDS_USER")
	if user == "" {
		t.Skip("Skipping: RDS_USER not set")
	}

	privateKeyPath := os.Getenv("RDS_PRIVATE_KEY_PATH")
	if privateKeyPath == "" {
		t.Skip("Skipping: RDS_PRIVATE_KEY_PATH not set")
	}

	// Read private key
	privateKey, err := os.ReadFile(privateKeyPath)
	if err != nil {
		t.Fatalf("Failed to read private key: %v", err)
	}

	// Create RDS client
	config := rds.ClientConfig{
		Address:    address,
		Port:       22,
		User:       user,
		PrivateKey: privateKey,
	}

	client, err := rds.NewClient(config)
	if err != nil {
		t.Fatalf("Failed to create RDS client: %v", err)
	}

	// Connect
	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer func() { _ = client.Close() }()

	// List existing volumes to see output format
	t.Log("=== Listing volumes to see output format ===")
	volumes, err := client.ListVolumes()
	if err != nil {
		t.Fatalf("Failed to list volumes: %v", err)
	}

	t.Logf("Found %d volumes", len(volumes))
	for i, vol := range volumes {
		if i >= 3 {
			break // Show first 3 only
		}
		t.Logf("Volume %d:", i+1)
		t.Logf("  Slot: %s", vol.Slot)
		t.Logf("  Type: %s", vol.Type)
		t.Logf("  FilePath: %s", vol.FilePath)
		t.Logf("  FileSizeBytes: %d", vol.FileSizeBytes)
		t.Logf("  NVMETCPExport: %v", vol.NVMETCPExport)
		t.Logf("  NVMETCPPort: %d", vol.NVMETCPPort)
		t.Logf("  NVMETCPNQN: %s", vol.NVMETCPNQN)
		t.Logf("  Status: %s", vol.Status)
	}
}
