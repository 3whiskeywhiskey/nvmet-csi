package integration

import (
	"context"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/driver"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/rds"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/test/mock"
)

// TestControllerIntegrationWithMockRDS tests the full controller flow with a mock RDS server
func TestControllerIntegrationWithMockRDS(t *testing.T) {
	// Set up allowed base paths for testing
	utils.ResetAllowedBasePaths()
	if err := utils.SetAllowedBasePath("/storage-pool/metal-csi"); err != nil {
		t.Fatalf("Failed to set allowed base path: %v", err)
	}
	t.Cleanup(utils.ResetAllowedBasePaths)
	// Start mock RDS server
	mockRDS, err := mock.NewMockRDSServer(12222)
	if err != nil {
		t.Fatalf("Failed to create mock RDS server: %v", err)
	}

	if err := mockRDS.Start(); err != nil {
		t.Fatalf("Failed to start mock RDS server: %v", err)
	}
	defer func() {
		if err := mockRDS.Stop(); err != nil {
			t.Logf("Warning: failed to stop mock RDS server: %v", err)
		}
	}()

	// Wait for server to be ready
	time.Sleep(100 * time.Millisecond)

	// Create RDS client connected to mock
	rdsClient, err := rds.NewClient(rds.ClientConfig{
		Address: mockRDS.Address(),
		Port:    mockRDS.Port(),
		User:    "admin",
		// No auth for mock server
		PrivateKey:         nil,
		InsecureSkipVerify: true, // Skip host key verification for mock server
	})
	if err != nil {
		t.Fatalf("Failed to create RDS client: %v", err)
	}

	// Connect to mock RDS
	if err := rdsClient.Connect(); err != nil {
		t.Fatalf("Failed to connect to mock RDS: %v", err)
	}
	defer func() { _ = rdsClient.Close() }()

	// Create driver with mock RDS client
	drv := &driver.Driver{}
	// Manually initialize for testing
	drv.SetRDSClient(rdsClient)
	drv.AddVolumeCapabilities()
	drv.AddControllerServiceCapabilities()

	cs := driver.NewControllerServer(drv)

	t.Run("CreateVolume_Success", func(t *testing.T) {
		req := &csi.CreateVolumeRequest{
			Name: "pvc-11111111-1111-1111-1111-111111111111",
			CapacityRange: &csi.CapacityRange{
				RequiredBytes: 1073741824, // 1 GiB
			},
			VolumeCapabilities: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
		}

		resp, err := cs.CreateVolume(context.Background(), req)
		if err != nil {
			t.Fatalf("CreateVolume failed: %v", err)
		}

		if resp.Volume == nil {
			t.Fatal("Expected volume in response")
		}

		volumeID := resp.Volume.VolumeId
		if volumeID == "" {
			t.Error("Expected non-empty volume ID")
		}

		// Verify volume was created on mock RDS
		vol, exists := mockRDS.GetVolume(volumeID)
		if !exists {
			t.Errorf("Volume %s not found on mock RDS", volumeID)
		}

		if vol.FileSizeBytes != 1073741824 {
			t.Errorf("Expected file size 1073741824, got %d", vol.FileSizeBytes)
		}

		if !vol.Exported {
			t.Error("Expected volume to be exported via NVMe/TCP")
		}

		t.Logf("✅ Created volume: %s", volumeID)
	})

	t.Run("CreateVolume_Idempotency", func(t *testing.T) {
		// Create volume first time
		req := &csi.CreateVolumeRequest{
			Name: "pvc-22222222-2222-2222-2222-222222222222",
			CapacityRange: &csi.CapacityRange{
				RequiredBytes: 2147483648, // 2 GiB
			},
			VolumeCapabilities: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
		}

		resp1, err := cs.CreateVolume(context.Background(), req)
		if err != nil {
			t.Fatalf("First CreateVolume failed: %v", err)
		}

		volumeID := resp1.Volume.VolumeId

		// Create same volume again (should be idempotent)
		resp2, err := cs.CreateVolume(context.Background(), req)
		if err != nil {
			t.Fatalf("Second CreateVolume failed: %v", err)
		}

		if resp2.Volume.VolumeId != volumeID {
			t.Errorf("Expected same volume ID, got %s vs %s", volumeID, resp2.Volume.VolumeId)
		}

		// Verify only one volume on mock RDS
		volumes := mockRDS.ListVolumes()
		count := 0
		for _, vol := range volumes {
			if vol.Slot == volumeID {
				count++
			}
		}

		if count != 1 {
			t.Errorf("Expected exactly 1 volume with ID %s, found %d", volumeID, count)
		}

		t.Logf("✅ Idempotency verified for volume: %s", volumeID)
	})

	t.Run("DeleteVolume_Success", func(t *testing.T) {
		// First create a volume
		createReq := &csi.CreateVolumeRequest{
			Name: "pvc-33333333-3333-3333-3333-333333333333",
			CapacityRange: &csi.CapacityRange{
				RequiredBytes: 1073741824,
			},
			VolumeCapabilities: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
		}

		createResp, err := cs.CreateVolume(context.Background(), createReq)
		if err != nil {
			t.Fatalf("CreateVolume failed: %v", err)
		}

		volumeID := createResp.Volume.VolumeId

		// Verify volume exists on mock RDS
		_, exists := mockRDS.GetVolume(volumeID)
		if !exists {
			t.Fatal("Volume should exist before deletion")
		}

		// Now delete it
		deleteReq := &csi.DeleteVolumeRequest{
			VolumeId: volumeID,
		}

		_, err = cs.DeleteVolume(context.Background(), deleteReq)
		if err != nil {
			t.Fatalf("DeleteVolume failed: %v", err)
		}

		// Verify volume is gone from mock RDS
		_, exists = mockRDS.GetVolume(volumeID)
		if exists {
			t.Error("Volume should not exist after deletion")
		}

		t.Logf("✅ Deleted volume: %s", volumeID)
	})

	t.Run("DeleteVolume_Idempotency", func(t *testing.T) {
		// Delete a non-existent volume (should succeed - idempotent)
		deleteReq := &csi.DeleteVolumeRequest{
			VolumeId: "pvc-00000000-0000-0000-0000-000000000000", // Valid format but doesn't exist
		}

		_, err := cs.DeleteVolume(context.Background(), deleteReq)
		if err != nil {
			t.Errorf("DeleteVolume should be idempotent, got error: %v", err)
		}

		t.Log("✅ Delete idempotency verified")
	})

	t.Run("GetCapacity_Success", func(t *testing.T) {
		req := &csi.GetCapacityRequest{}

		resp, err := cs.GetCapacity(context.Background(), req)
		if err != nil {
			t.Fatalf("GetCapacity failed: %v", err)
		}

		if resp.AvailableCapacity == 0 {
			t.Error("Expected non-zero available capacity")
		}

		t.Logf("✅ Available capacity: %d bytes", resp.AvailableCapacity)
	})

	t.Run("ValidateVolumeCapabilities_Success", func(t *testing.T) {
		// First create a volume
		createReq := &csi.CreateVolumeRequest{
			Name: "pvc-44444444-4444-4444-4444-444444444444",
			CapacityRange: &csi.CapacityRange{
				RequiredBytes: 1073741824,
			},
			VolumeCapabilities: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
		}

		createResp, err := cs.CreateVolume(context.Background(), createReq)
		if err != nil {
			t.Fatalf("CreateVolume failed: %v", err)
		}

		volumeID := createResp.Volume.VolumeId

		// Validate with supported capabilities
		validateReq := &csi.ValidateVolumeCapabilitiesRequest{
			VolumeId: volumeID,
			VolumeCapabilities: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
		}

		validateResp, err := cs.ValidateVolumeCapabilities(context.Background(), validateReq)
		if err != nil {
			t.Fatalf("ValidateVolumeCapabilities failed: %v", err)
		}

		if validateResp.Confirmed == nil {
			t.Error("Expected capabilities to be confirmed")
		}

		t.Log("✅ Volume capabilities validated")
	})

	t.Run("ValidateVolumeCapabilities_Unsupported", func(t *testing.T) {
		// Create a volume first
		createReq := &csi.CreateVolumeRequest{
			Name: "pvc-55555555-5555-5555-5555-555555555555",
			CapacityRange: &csi.CapacityRange{
				RequiredBytes: 1073741824,
			},
			VolumeCapabilities: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
		}

		createResp, err := cs.CreateVolume(context.Background(), createReq)
		if err != nil {
			t.Fatalf("CreateVolume failed: %v", err)
		}

		volumeID := createResp.Volume.VolumeId

		// Try to validate with unsupported MULTI_NODE access mode
		validateReq := &csi.ValidateVolumeCapabilitiesRequest{
			VolumeId: volumeID,
			VolumeCapabilities: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
		}

		validateResp, err := cs.ValidateVolumeCapabilities(context.Background(), validateReq)
		if err != nil {
			t.Fatalf("ValidateVolumeCapabilities failed: %v", err)
		}

		if validateResp.Confirmed != nil {
			t.Error("Expected unsupported capabilities to not be confirmed")
		}

		if validateResp.Message == "" {
			t.Error("Expected error message for unsupported capabilities")
		}

		t.Log("✅ Unsupported capabilities correctly rejected")
	})

	t.Run("ListVolumes_Success", func(t *testing.T) {
		// Create a few volumes
		volumeNames := []string{
			"pvc-66666666-6666-6666-6666-666666666666",
			"pvc-77777777-7777-7777-7777-777777777777",
			"pvc-88888888-8888-8888-8888-888888888888",
		}
		for _, name := range volumeNames {
			createReq := &csi.CreateVolumeRequest{
				Name: name,
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1073741824,
				},
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
						},
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			}

			_, err := cs.CreateVolume(context.Background(), createReq)
			if err != nil {
				t.Fatalf("CreateVolume failed: %v", err)
			}
		}

		// List all volumes
		listReq := &csi.ListVolumesRequest{}
		listResp, err := cs.ListVolumes(context.Background(), listReq)
		if err != nil {
			t.Fatalf("ListVolumes failed: %v", err)
		}

		if len(listResp.Entries) < 3 {
			t.Errorf("Expected at least 3 volumes, got %d", len(listResp.Entries))
		}

		t.Logf("✅ Listed %d volumes", len(listResp.Entries))
	})

	t.Run("CreateVolume_InvalidCapabilities", func(t *testing.T) {
		req := &csi.CreateVolumeRequest{
			Name: "pvc-99999999-9999-9999-9999-999999999999",
			CapacityRange: &csi.CapacityRange{
				RequiredBytes: 1073741824,
			},
			VolumeCapabilities: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
		}

		_, err := cs.CreateVolume(context.Background(), req)
		if err == nil {
			t.Fatal("Expected error for invalid capabilities")
		}

		st, ok := status.FromError(err)
		if !ok {
			t.Fatal("Expected gRPC status error")
		}

		if st.Code() != codes.InvalidArgument {
			t.Errorf("Expected InvalidArgument error code, got %v", st.Code())
		}

		t.Log("✅ Invalid capabilities correctly rejected")
	})
}
