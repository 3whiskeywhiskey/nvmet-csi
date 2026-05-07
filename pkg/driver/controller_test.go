package driver

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/attachment"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/rds"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/utils"
)

func TestValidateVolumeCapabilities(t *testing.T) {
	cs := &ControllerServer{
		driver: &Driver{
			vcaps: []*csi.VolumeCapability_AccessMode{
				{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
				{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY},
			},
		},
	}

	tests := []struct {
		name      string
		caps      []*csi.VolumeCapability
		expectErr bool
	}{
		{
			name: "valid single node writer with mount",
			caps: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "valid single node reader with block",
			caps: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
					},
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
				},
			},
			expectErr: false,
		},
		{
			name: "unsupported multi node access",
			caps: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			expectErr: true,
		},
		{
			name: "missing access type",
			caps: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
				},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := cs.validateVolumeCapabilities(tt.caps)
			if tt.expectErr && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestControllerGetCapabilities(t *testing.T) {
	cs := &ControllerServer{
		driver: &Driver{
			cscaps: []*csi.ControllerServiceCapability{
				{
					Type: &csi.ControllerServiceCapability_Rpc{
						Rpc: &csi.ControllerServiceCapability_RPC{
							Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
						},
					},
				},
			},
		},
	}

	req := &csi.ControllerGetCapabilitiesRequest{}
	resp, err := cs.ControllerGetCapabilities(context.Background(), req)

	if err != nil {
		t.Fatalf("ControllerGetCapabilities failed: %v", err)
	}

	if len(resp.Capabilities) == 0 {
		t.Error("Expected capabilities but got none")
	}

	hasCreateDelete := false
	for _, cap := range resp.Capabilities {
		if cap.GetRpc() != nil {
			if cap.GetRpc().Type == csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME {
				hasCreateDelete = true
				break
			}
		}
	}

	if !hasCreateDelete {
		t.Error("Expected CREATE_DELETE_VOLUME capability but not found")
	}
}

func TestCreateVolumeValidation(t *testing.T) {
	cs := &ControllerServer{
		driver: &Driver{
			vcaps: []*csi.VolumeCapability_AccessMode{
				{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
			},
		},
	}

	tests := []struct {
		name      string
		req       *csi.CreateVolumeRequest
		expectErr bool
		errCode   codes.Code
	}{
		{
			name: "missing volume name",
			req: &csi.CreateVolumeRequest{
				Name: "",
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
			},
			expectErr: true,
			errCode:   codes.InvalidArgument,
		},
		{
			name: "missing volume capabilities",
			req: &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: nil,
			},
			expectErr: true,
			errCode:   codes.InvalidArgument,
		},
		{
			name: "invalid volume capabilities",
			req: &csi.CreateVolumeRequest{
				Name: "test-volume",
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
			},
			expectErr: true,
			errCode:   codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := cs.CreateVolume(context.Background(), tt.req)
			if tt.expectErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("Expected gRPC status error, got: %v", err)
					return
				}
				if st.Code() != tt.errCode {
					t.Errorf("Expected error code %v, got %v", tt.errCode, st.Code())
				}
			} else if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestDeleteVolumeValidation(t *testing.T) {
	cs := &ControllerServer{
		driver: &Driver{},
	}

	tests := []struct {
		name      string
		volumeID  string
		expectErr bool
		errCode   codes.Code
	}{
		{
			name:      "missing volume ID",
			volumeID:  "",
			expectErr: true,
			errCode:   codes.InvalidArgument,
		},
		{
			name:      "injection attempt",
			volumeID:  "pvc-test; rm -rf /",
			expectErr: true,
			errCode:   codes.InvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &csi.DeleteVolumeRequest{
				VolumeId: tt.volumeID,
			}

			_, err := cs.DeleteVolume(context.Background(), req)
			if tt.expectErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("Expected gRPC status error, got: %v", err)
					return
				}
				if st.Code() != tt.errCode {
					t.Errorf("Expected error code %v, got %v", tt.errCode, st.Code())
				}
			} else if err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestCreateVolume_PlannerIdempotentNoMutation(t *testing.T) {
	ctx := context.Background()
	cs, mockRDS := testControllerServer(t)
	nqn, err := utils.VolumeIDToNQN(testVolumeID1)
	if err != nil {
		t.Fatalf("generate nqn: %v", err)
	}
	filePath, err := utils.VolumeIDToFilePath(testVolumeID1, defaultVolumeBasePath)
	if err != nil {
		t.Fatalf("generate file path: %v", err)
	}

	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:          testVolumeID1,
		Type:          "file",
		FilePath:      filePath,
		FileSizeBytes: 1 * 1024 * 1024 * 1024,
		NVMETCPExport: true,
		NVMETCPPort:   defaultNVMETCPPort,
		NVMETCPNQN:    nqn,
		Status:        "ready",
	})

	resp, err := cs.CreateVolume(ctx, testCreateVolumeRequest(testVolumeID1, 1*1024*1024*1024))
	if err != nil {
		t.Fatalf("CreateVolume failed: %v", err)
	}
	if resp.GetVolume().GetCapacityBytes() != 1*1024*1024*1024 {
		t.Fatalf("capacity = %d, want 1GiB", resp.GetVolume().GetCapacityBytes())
	}
	if mockRDS.MutationCount("create_volume") != 0 {
		t.Fatalf("create_volume mutations = %d, want 0", mockRDS.MutationCount("create_volume"))
	}
	if mockRDS.MutationCount("resize_volume") != 0 {
		t.Fatalf("resize_volume mutations = %d, want 0", mockRDS.MutationCount("resize_volume"))
	}
}

func TestCreateVolume_PlannerResizesSmallerExistingVolume(t *testing.T) {
	ctx := context.Background()
	cs, mockRDS := testControllerServer(t)
	nqn, err := utils.VolumeIDToNQN(testVolumeID2)
	if err != nil {
		t.Fatalf("generate nqn: %v", err)
	}
	filePath, err := utils.VolumeIDToFilePath(testVolumeID2, defaultVolumeBasePath)
	if err != nil {
		t.Fatalf("generate file path: %v", err)
	}

	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:          testVolumeID2,
		Type:          "file",
		FilePath:      filePath,
		FileSizeBytes: 1 * 1024 * 1024 * 1024,
		NVMETCPExport: true,
		NVMETCPPort:   defaultNVMETCPPort,
		NVMETCPNQN:    nqn,
		Status:        "ready",
	})

	resp, err := cs.CreateVolume(ctx, testCreateVolumeRequest(testVolumeID2, 2*1024*1024*1024))
	if err != nil {
		t.Fatalf("CreateVolume failed: %v", err)
	}
	if resp.GetVolume().GetCapacityBytes() != 2*1024*1024*1024 {
		t.Fatalf("capacity = %d, want 2GiB", resp.GetVolume().GetCapacityBytes())
	}
	if mockRDS.MutationCount("create_volume") != 0 {
		t.Fatalf("create_volume mutations = %d, want 0", mockRDS.MutationCount("create_volume"))
	}
	if mockRDS.MutationCount("resize_volume") != 1 {
		t.Fatalf("resize_volume mutations = %d, want 1", mockRDS.MutationCount("resize_volume"))
	}

	volume, err := mockRDS.GetVolume(testVolumeID2)
	if err != nil {
		t.Fatalf("GetVolume failed: %v", err)
	}
	if volume.FileSizeBytes != 2*1024*1024*1024 {
		t.Fatalf("stored size = %d, want 2GiB", volume.FileSizeBytes)
	}
}

func TestCreateVolume_PlannerConflictFailsClosed(t *testing.T) {
	ctx := context.Background()
	cs, mockRDS := testControllerServer(t)
	nqn, err := utils.VolumeIDToNQN(testVolumeID3)
	if err != nil {
		t.Fatalf("generate nqn: %v", err)
	}

	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:          testVolumeID3,
		Type:          "file",
		FilePath:      "/storage-pool/metal-csi/unexpected.img",
		FileSizeBytes: 1 * 1024 * 1024 * 1024,
		NVMETCPExport: true,
		NVMETCPPort:   defaultNVMETCPPort,
		NVMETCPNQN:    nqn,
		Status:        "ready",
	})

	_, err = cs.CreateVolume(ctx, testCreateVolumeRequest(testVolumeID3, 1*1024*1024*1024))
	if err == nil {
		t.Fatal("expected conflict error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T %v", err, err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition", st.Code())
	}
	if !strings.Contains(st.Message(), "backing file path differs") {
		t.Fatalf("message = %q, want backing file path conflict", st.Message())
	}
	if mockRDS.MutationCount("create_volume") != 0 {
		t.Fatalf("create_volume mutations = %d, want 0", mockRDS.MutationCount("create_volume"))
	}
	if mockRDS.MutationCount("resize_volume") != 0 {
		t.Fatalf("resize_volume mutations = %d, want 0", mockRDS.MutationCount("resize_volume"))
	}
}

func TestDeleteVolume_PlannerMissingVolumeNoMutation(t *testing.T) {
	ctx := context.Background()
	cs, mockRDS := testControllerServer(t)

	_, err := cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: testVolumeID4})
	if err != nil {
		t.Fatalf("DeleteVolume failed: %v", err)
	}
	if mockRDS.MutationCount("delete_volume") != 0 {
		t.Fatalf("delete_volume mutations = %d, want 0", mockRDS.MutationCount("delete_volume"))
	}
}

func testCreateVolumeRequest(name string, size int64) *csi.CreateVolumeRequest {
	return &csi.CreateVolumeRequest{
		Name: name,
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
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: size,
		},
	}
}

func TestUnimplementedMethods(t *testing.T) {
	cs := &ControllerServer{
		driver: &Driver{},
	}

	// Test all unimplemented methods return Unimplemented error
	// Note: ControllerPublishVolume and ControllerUnpublishVolume are now implemented
	// Note: ControllerExpandVolume is now implemented, so it's not in this test
	// Note: CreateSnapshot, DeleteSnapshot, and ListSnapshots are now implemented, so they're not in this test

	// Currently all previously unimplemented methods have been implemented
	// This test is kept as a placeholder for future unimplemented methods
	t.Run("ControllerGetVolume", func(t *testing.T) {
		_, err := cs.ControllerGetVolume(context.Background(), &csi.ControllerGetVolumeRequest{})
		if err == nil {
			t.Error("Expected unimplemented error")
		}
		st, _ := status.FromError(err)
		if st.Code() != codes.Unimplemented {
			t.Errorf("Expected Unimplemented code, got %v", st.Code())
		}
	})
}

func TestParseEndpoint(t *testing.T) {
	tests := []struct {
		name        string
		endpoint    string
		expectProto string
		expectAddr  string
		expectErr   bool
	}{
		{
			name:        "unix socket with scheme",
			endpoint:    "unix:///tmp/csi.sock",
			expectProto: "unix",
			expectAddr:  "/tmp/csi.sock",
			expectErr:   false,
		},
		{
			name:        "unix socket without scheme",
			endpoint:    "/tmp/csi.sock",
			expectProto: "unix",
			expectAddr:  "/tmp/csi.sock",
			expectErr:   false,
		},
		{
			name:        "tcp endpoint",
			endpoint:    "tcp://0.0.0.0:10000",
			expectProto: "tcp",
			expectAddr:  "0.0.0.0:10000",
			expectErr:   false,
		},
		{
			name:      "invalid scheme",
			endpoint:  "http://localhost:80",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			proto, addr, err := parseEndpoint(tt.endpoint)
			if tt.expectErr {
				if err == nil {
					t.Error("Expected error but got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
				if proto != tt.expectProto {
					t.Errorf("Expected proto %s, got %s", tt.expectProto, proto)
				}
				if addr != tt.expectAddr {
					t.Errorf("Expected addr %s, got %s", tt.expectAddr, addr)
				}
			}
		})
	}
}

// testControllerServer creates a ControllerServer with mock RDS client and fake k8s client
func testControllerServer(t *testing.T, nodes ...*corev1.Node) (*ControllerServer, *rds.MockClient) {
	t.Helper()

	// Create fake k8s client with provided nodes
	var objects []runtime.Object
	for _, n := range nodes {
		objects = append(objects, n)
	}
	k8sClient := fake.NewSimpleClientset(objects...)

	// Create mock RDS client
	mockRDS := rds.NewMockClient()
	mockRDS.SetAddress("10.0.0.1")

	// Create driver with attachment manager
	driver := &Driver{
		name:              DriverName,
		version:           "test",
		rdsClient:         mockRDS,
		k8sClient:         k8sClient,
		attachmentManager: attachment.NewAttachmentManager(k8sClient),
	}
	driver.addVolumeCapabilities()
	driver.addControllerServiceCapabilities()

	return NewControllerServer(driver), mockRDS
}

// testNode creates a test Node object
func testNode(name string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
	}
}

// ========================================
// ControllerPublishVolume Tests
// ========================================

// Test volume IDs must be valid UUIDs (pvc-<uuid> format)
const (
	testVolumeID1     = "pvc-11111111-1111-1111-1111-111111111111"
	testVolumeID2     = "pvc-22222222-2222-2222-2222-222222222222"
	testVolumeID3     = "pvc-33333333-3333-3333-3333-333333333333"
	testVolumeID4     = "pvc-44444444-4444-4444-4444-444444444444"
	testVolumeID5     = "pvc-55555555-5555-5555-5555-555555555555"
	testVolumeID6     = "pvc-66666666-6666-6666-6666-666666666666"
	testVolumeID7     = "pvc-77777777-7777-7777-7777-777777777777"
	testVolumeID8     = "pvc-88888888-8888-8888-8888-888888888888"
	testVolumeIDStale = "pvc-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

func TestControllerPublishVolume_Success(t *testing.T) {
	ctx := context.Background()
	node1 := testNode("node-1")
	cs, mockRDS := testControllerServer(t, node1)

	// Add test volume to mock
	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:          testVolumeID1,
		FilePath:      "/storage-pool/metal-csi/" + testVolumeID1 + ".img",
		FileSizeBytes: 1073741824,
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2000-02.com.mikrotik:" + testVolumeID1,
	})

	req := &csi.ControllerPublishVolumeRequest{
		VolumeId: testVolumeID1,
		NodeId:   "node-1",
		VolumeContext: map[string]string{
			"nvmeAddress": "10.0.0.1",
		},
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
		},
	}

	resp, err := cs.ControllerPublishVolume(ctx, req)
	if err != nil {
		t.Fatalf("ControllerPublishVolume failed: %v", err)
	}

	// CSI-05: Verify publish_context
	if resp.PublishContext == nil {
		t.Fatal("PublishContext is nil")
	}
	if resp.PublishContext["nvme_address"] == "" {
		t.Error("nvme_address missing from PublishContext")
	}
	if resp.PublishContext["nvme_port"] != "4420" {
		t.Errorf("nvme_port = %s, want 4420", resp.PublishContext["nvme_port"])
	}
	expectedNQN := "nqn.2000-02.com.mikrotik:" + testVolumeID1
	if resp.PublishContext["nvme_nqn"] != expectedNQN {
		t.Errorf("nvme_nqn = %s, want %s", resp.PublishContext["nvme_nqn"], expectedNQN)
	}
	if resp.PublishContext["fs_type"] == "" {
		t.Error("fs_type missing from PublishContext")
	}
}

func TestControllerPublishVolume_Idempotent(t *testing.T) {
	// CSI-01: Same volume, same node = success
	ctx := context.Background()
	node1 := testNode("node-1")
	cs, mockRDS := testControllerServer(t, node1)

	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:        testVolumeID2,
		NVMETCPPort: 4420,
		NVMETCPNQN:  "nqn.2000-02.com.mikrotik:" + testVolumeID2,
	})

	req := &csi.ControllerPublishVolumeRequest{
		VolumeId: testVolumeID2,
		NodeId:   "node-1",
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
		},
	}

	// First publish
	_, err := cs.ControllerPublishVolume(ctx, req)
	if err != nil {
		t.Fatalf("First publish failed: %v", err)
	}

	// Second publish (same node) - should succeed
	resp, err := cs.ControllerPublishVolume(ctx, req)
	if err != nil {
		t.Fatalf("Second publish (idempotent) failed: %v", err)
	}
	if resp.PublishContext == nil {
		t.Error("Idempotent publish should return PublishContext")
	}
}

func TestControllerPublishVolume_RWOConflict(t *testing.T) {
	// CSI-02: Same volume, different node = FAILED_PRECONDITION
	ctx := context.Background()
	node1 := testNode("node-1")
	node2 := testNode("node-2")
	cs, mockRDS := testControllerServer(t, node1, node2)

	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:        testVolumeID3,
		NVMETCPPort: 4420,
		NVMETCPNQN:  "nqn.2000-02.com.mikrotik:" + testVolumeID3,
	})

	// Attach to node-1
	req1 := &csi.ControllerPublishVolumeRequest{
		VolumeId: testVolumeID3,
		NodeId:   "node-1",
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
		},
	}
	_, err := cs.ControllerPublishVolume(ctx, req1)
	if err != nil {
		t.Fatalf("First publish failed: %v", err)
	}

	// Try to attach to node-2 - should fail
	req2 := &csi.ControllerPublishVolumeRequest{
		VolumeId: testVolumeID3,
		NodeId:   "node-2",
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
		},
	}
	_, err = cs.ControllerPublishVolume(ctx, req2)
	if err == nil {
		t.Fatal("Expected FAILED_PRECONDITION error for RWO conflict")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("Expected code FailedPrecondition (9), got %v", st.Code())
	}
	if !strings.Contains(st.Message(), "node-1") {
		t.Errorf("Error message should mention blocking node, got: %s", st.Message())
	}
}

func TestControllerPublishVolume_StaleAttachmentSelfHealing(t *testing.T) {
	// CSI-06: Volume attached to deleted node = auto-clear and allow new attach
	ctx := context.Background()
	node1 := testNode("node-1")
	// "deleted-node" does NOT exist in k8s (simulates deleted node)
	cs, mockRDS := testControllerServer(t, node1)

	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:        testVolumeIDStale,
		NVMETCPPort: 4420,
		NVMETCPNQN:  "nqn.2000-02.com.mikrotik:" + testVolumeIDStale,
	})

	// Manually track attachment to non-existent node (simulates stale state after node deletion)
	am := cs.driver.GetAttachmentManager()
	// Use TrackAttachment directly on the manager to bypass validation
	// (in reality, this could happen if controller restarts and loads stale state)
	_ = am.TrackAttachment(ctx, testVolumeIDStale, "deleted-node")

	// Try to attach to node-1 - should succeed (self-healing)
	req := &csi.ControllerPublishVolumeRequest{
		VolumeId: testVolumeIDStale,
		NodeId:   "node-1",
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
		},
	}
	resp, err := cs.ControllerPublishVolume(ctx, req)
	if err != nil {
		t.Fatalf("Expected self-healing success, got error: %v", err)
	}
	if resp.PublishContext == nil {
		t.Error("Expected PublishContext after self-healing")
	}

	// Verify attachment is now to node-1
	state, exists := am.GetAttachment(testVolumeIDStale)
	if !exists {
		t.Fatal("Attachment should exist after self-healing")
	}
	if state.NodeID != "node-1" {
		t.Errorf("Attachment should be to node-1, got %s", state.NodeID)
	}
}

func TestControllerPublishVolume_VolumeNotFound(t *testing.T) {
	ctx := context.Background()
	node1 := testNode("node-1")
	cs, _ := testControllerServer(t, node1)

	// Don't add volume to mock - it won't exist
	// Use a valid UUID format but non-existent volume
	nonExistentVolumeID := "pvc-99999999-9999-9999-9999-999999999999"

	req := &csi.ControllerPublishVolumeRequest{
		VolumeId: nonExistentVolumeID,
		NodeId:   "node-1",
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
		},
	}

	_, err := cs.ControllerPublishVolume(ctx, req)
	if err == nil {
		t.Fatal("Expected NOT_FOUND error")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Expected gRPC status error, got: %v", err)
	}
	if st.Code() != codes.NotFound {
		t.Errorf("Expected code NotFound (5), got %v", st.Code())
	}
}

func TestControllerPublishVolume_RDSObserveErrorDoesNotTrackAttachment(t *testing.T) {
	ctx := context.Background()
	node1 := testNode("node-1")
	cs, mockRDS := testControllerServer(t, node1)
	mockRDS.SetPersistentError(fmt.Errorf("ssh: %w", utils.ErrConnectionFailed))

	req := &csi.ControllerPublishVolumeRequest{
		VolumeId: testVolumeID1,
		NodeId:   "node-1",
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
		},
	}

	_, err := cs.ControllerPublishVolume(ctx, req)
	if err == nil {
		t.Fatal("expected RDS observe error")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T %v", err, err)
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("code = %v, want Unavailable", st.Code())
	}
	if _, exists := cs.driver.GetAttachmentManager().GetAttachment(testVolumeID1); exists {
		t.Fatal("attachment should not be tracked when RDS observe fails")
	}
}

func TestControllerPublishVolume_InvalidVolumeID(t *testing.T) {
	ctx := context.Background()
	cs, _ := testControllerServer(t)

	req := &csi.ControllerPublishVolumeRequest{
		VolumeId: "", // Invalid
		NodeId:   "node-1",
	}

	_, err := cs.ControllerPublishVolume(ctx, req)
	if err == nil {
		t.Fatal("Expected INVALID_ARGUMENT error")
	}

	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("Expected code InvalidArgument (3), got %v", st.Code())
	}
}

func TestControllerPublishVolume_InvalidNodeID(t *testing.T) {
	ctx := context.Background()
	cs, mockRDS := testControllerServer(t)

	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:        testVolumeID4,
		NVMETCPPort: 4420,
		NVMETCPNQN:  "nqn.2000-02.com.mikrotik:" + testVolumeID4,
	})

	req := &csi.ControllerPublishVolumeRequest{
		VolumeId: testVolumeID4,
		NodeId:   "", // Invalid
	}

	_, err := cs.ControllerPublishVolume(ctx, req)
	if err == nil {
		t.Fatal("Expected INVALID_ARGUMENT error")
	}

	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("Expected code InvalidArgument (3), got %v", st.Code())
	}
}

func TestControllerPublishVolume_InvalidVolumeIDFormat(t *testing.T) {
	ctx := context.Background()
	node1 := testNode("node-1")
	cs, _ := testControllerServer(t, node1)

	req := &csi.ControllerPublishVolumeRequest{
		VolumeId: "invalid-format; rm -rf /", // Injection attempt
		NodeId:   "node-1",
	}

	_, err := cs.ControllerPublishVolume(ctx, req)
	if err == nil {
		t.Fatal("Expected INVALID_ARGUMENT error for invalid volume ID format")
	}

	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("Expected code InvalidArgument (3), got %v", st.Code())
	}
}

// ========================================
// ControllerUnpublishVolume Tests
// ========================================

func TestControllerUnpublishVolume_Success(t *testing.T) {
	ctx := context.Background()
	node1 := testNode("node-1")
	cs, mockRDS := testControllerServer(t, node1)

	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot: testVolumeID5,
	})

	// First publish
	am := cs.driver.GetAttachmentManager()
	_ = am.TrackAttachment(ctx, testVolumeID5, "node-1")

	// Unpublish
	req := &csi.ControllerUnpublishVolumeRequest{
		VolumeId: testVolumeID5,
		NodeId:   "node-1",
	}

	_, err := cs.ControllerUnpublishVolume(ctx, req)
	if err != nil {
		t.Fatalf("ControllerUnpublishVolume failed: %v", err)
	}

	// Verify attachment removed
	_, exists := am.GetAttachment(testVolumeID5)
	if exists {
		t.Error("Attachment should be removed after unpublish")
	}
}

func TestControllerUnpublishVolume_Idempotent(t *testing.T) {
	// CSI-03: Unpublish on non-attached volume = success
	ctx := context.Background()
	cs, _ := testControllerServer(t)

	// Don't attach first - just try to unpublish
	// Use a valid UUID format
	req := &csi.ControllerUnpublishVolumeRequest{
		VolumeId: testVolumeID6,
		NodeId:   "node-1",
	}

	// Should succeed (idempotent)
	_, err := cs.ControllerUnpublishVolume(ctx, req)
	if err != nil {
		t.Fatalf("Idempotent unpublish should succeed, got: %v", err)
	}
}

func TestControllerUnpublishVolume_InvalidVolumeID(t *testing.T) {
	ctx := context.Background()
	cs, _ := testControllerServer(t)

	req := &csi.ControllerUnpublishVolumeRequest{
		VolumeId: "", // Invalid
	}

	_, err := cs.ControllerUnpublishVolume(ctx, req)
	if err == nil {
		t.Fatal("Expected INVALID_ARGUMENT error")
	}

	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("Expected code InvalidArgument (3), got %v", st.Code())
	}
}

func TestControllerUnpublishVolume_InvalidVolumeIDFormat(t *testing.T) {
	ctx := context.Background()
	cs, _ := testControllerServer(t)

	req := &csi.ControllerUnpublishVolumeRequest{
		VolumeId: "injection; drop table", // Injection attempt
		NodeId:   "node-1",
	}

	_, err := cs.ControllerUnpublishVolume(ctx, req)
	if err == nil {
		t.Fatal("Expected INVALID_ARGUMENT error for invalid volume ID format")
	}

	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("Expected code InvalidArgument (3), got %v", st.Code())
	}
}

func TestControllerUnpublishVolume_EmptyNodeID(t *testing.T) {
	// CSI spec allows empty nodeID for force-detach scenarios
	ctx := context.Background()
	cs, _ := testControllerServer(t)

	// Track an attachment first
	am := cs.driver.GetAttachmentManager()
	_ = am.TrackAttachment(ctx, testVolumeID7, "some-node")

	req := &csi.ControllerUnpublishVolumeRequest{
		VolumeId: testVolumeID7,
		NodeId:   "", // Empty - force detach
	}

	// Should succeed - empty nodeID is allowed per CSI spec
	_, err := cs.ControllerUnpublishVolume(ctx, req)
	if err != nil {
		t.Fatalf("Unpublish with empty nodeID should succeed, got: %v", err)
	}
	if _, exists := am.GetAttachment(testVolumeID7); exists {
		t.Fatal("empty nodeID force-detach should clear the tracked attachment")
	}
}

func TestControllerUnpublishVolume_MigrationCompleted(t *testing.T) {
	// Test that ControllerUnpublishVolume posts MigrationCompleted event
	// when source node detaches during an active migration
	ctx := context.Background()

	// Create PV with ClaimRef for event posting
	volumeID := testVolumeID8
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: volumeID,
		},
		Spec: corev1.PersistentVolumeSpec{
			ClaimRef: &corev1.ObjectReference{
				Namespace: "default",
				Name:      "test-pvc",
			},
		},
	}

	// Create PVC for event posting
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
		},
	}

	// Create controller with PV and PVC
	cs, mockRDS := testControllerServer(t)
	// Add PV and PVC to fake clientset
	_, err := cs.driver.k8sClient.CoreV1().PersistentVolumes().Create(ctx, pv, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create test PV: %v", err)
	}
	_, err = cs.driver.k8sClient.CoreV1().PersistentVolumeClaims("default").Create(ctx, pvc, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create test PVC: %v", err)
	}

	// Add volume to mock RDS
	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot: volumeID,
	})

	// Set up migration scenario: volume attached to two nodes (RWX migration)
	am := cs.driver.GetAttachmentManager()

	// Add primary attachment (source node) with RWX mode
	err = am.TrackAttachmentWithMode(ctx, volumeID, "node-1", "RWX")
	if err != nil {
		t.Fatalf("Failed to track primary attachment: %v", err)
	}

	// Add secondary attachment (migration target)
	err = am.AddSecondaryAttachment(ctx, volumeID, "node-2", 5*time.Minute)
	if err != nil {
		t.Fatalf("Failed to add secondary attachment: %v", err)
	}

	// Verify migration state is active
	state, found := am.GetAttachment(volumeID)
	if !found {
		t.Fatal("Volume should have attachment state")
	}
	if !state.IsMigrating() {
		t.Fatal("Volume should be in migration state")
	}
	if len(state.Nodes) != 2 {
		t.Fatalf("Expected 2 nodes, got %d", len(state.Nodes))
	}

	// Sleep briefly to ensure migration duration is measurable
	time.Sleep(100 * time.Millisecond)

	// Unpublish from source node (completes migration)
	req := &csi.ControllerUnpublishVolumeRequest{
		VolumeId: volumeID,
		NodeId:   "node-1", // Remove source node
	}

	_, err = cs.ControllerUnpublishVolume(ctx, req)
	if err != nil {
		t.Fatalf("ControllerUnpublishVolume failed: %v", err)
	}

	// Verify partial detach (target node remains)
	state, found = am.GetAttachment(volumeID)
	if !found {
		t.Fatal("Volume should still have attachment state")
	}
	if len(state.Nodes) != 1 {
		t.Fatalf("Expected 1 node after partial detach, got %d", len(state.Nodes))
	}
	if state.Nodes[0].NodeID != "node-2" {
		t.Errorf("Expected target node-2 to remain, got %s", state.Nodes[0].NodeID)
	}

	// Verify migration state is cleared
	if state.IsMigrating() {
		t.Error("Migration state should be cleared after unpublish")
	}

	// Event posting is best-effort, so we just verify the code path executed
	// without error. The event itself is tested in events_test.go
	t.Log("Migration completed event code path executed successfully")
}

// ========================================
// RWX Capability Tests (Phase 08-03)
// ========================================

func TestValidateVolumeCapabilities_RWX(t *testing.T) {
	tests := []struct {
		name          string
		caps          []*csi.VolumeCapability
		expectError   bool
		errorContains string
	}{
		{
			name: "RWX block - should succeed",
			caps: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
				},
			},
			expectError: false,
		},
		{
			name: "RWX filesystem - should fail with actionable error",
			caps: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"},
					},
				},
			},
			expectError:   true,
			errorContains: "volumeMode: Block",
		},
		{
			name: "RWO block - should succeed",
			caps: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
				},
			},
			expectError: false,
		},
		{
			name: "RWO filesystem - should succeed",
			caps: []*csi.VolumeCapability{
				{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"},
					},
				},
			},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Create controller with driver that has RWX capability
			cs, _ := testControllerServer(t)

			err := cs.validateVolumeCapabilities(tc.caps)

			if tc.expectError {
				if err == nil {
					t.Errorf("expected error but got nil")
				} else if tc.errorContains != "" && !strings.Contains(err.Error(), tc.errorContains) {
					t.Errorf("expected error containing %q, got %q", tc.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestCreateVolume_RWXFilesystemRejected(t *testing.T) {
	cs, _ := testControllerServer(t)

	req := &csi.CreateVolumeRequest{
		Name: "test-vol",
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
				},
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"},
				},
			},
		},
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 1 * 1024 * 1024 * 1024,
		},
	}

	_, err := cs.CreateVolume(context.Background(), req)

	if err == nil {
		t.Fatal("expected error for RWX filesystem, got nil")
	}

	// Check it's an InvalidArgument error
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", st.Code())
	}
	if !strings.Contains(st.Message(), "volumeMode: Block") {
		t.Errorf("expected error to mention volumeMode: Block, got %q", st.Message())
	}
}

func TestDriverVolumeCapabilities_IncludesRWX(t *testing.T) {
	cs, _ := testControllerServer(t)
	driver := cs.driver

	found := false
	for _, vcap := range driver.vcaps {
		if vcap.GetMode() == csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected MULTI_NODE_MULTI_WRITER in vcaps, not found")
	}
}

func TestControllerPublishVolume_RWXDualAttach(t *testing.T) {
	ctx := context.Background()
	node1 := testNode("node-1")
	node2 := testNode("node-2")
	node3 := testNode("node-3")
	cs, mockRDS := testControllerServer(t, node1, node2, node3)

	volumeID := testVolumeID1
	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:        volumeID,
		NVMETCPNQN:  "nqn.2000-02.com.mikrotik:" + volumeID,
		NVMETCPPort: 4420,
	})

	rwxCap := &csi.VolumeCapability{
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
		},
		AccessType: &csi.VolumeCapability_Block{
			Block: &csi.VolumeCapability_BlockVolume{},
		},
	}

	// First attach should succeed
	req1 := &csi.ControllerPublishVolumeRequest{
		VolumeId:         volumeID,
		NodeId:           "node-1",
		VolumeCapability: rwxCap,
	}
	_, err := cs.ControllerPublishVolume(ctx, req1)
	if err != nil {
		t.Fatalf("first attach failed: %v", err)
	}

	// Second attach (migration target) should succeed
	req2 := &csi.ControllerPublishVolumeRequest{
		VolumeId:         volumeID,
		NodeId:           "node-2",
		VolumeCapability: rwxCap,
	}
	_, err = cs.ControllerPublishVolume(ctx, req2)
	if err != nil {
		t.Fatalf("second attach failed: %v", err)
	}

	// Third attach should fail with migration limit
	req3 := &csi.ControllerPublishVolumeRequest{
		VolumeId:         volumeID,
		NodeId:           "node-3",
		VolumeCapability: rwxCap,
	}
	_, err = cs.ControllerPublishVolume(ctx, req3)
	if err == nil {
		t.Fatal("expected error for 3rd attach, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("expected FailedPrecondition, got %v", st.Code())
	}
	if !strings.Contains(st.Message(), "migration limit") {
		t.Errorf("expected 'migration limit' in error, got %q", st.Message())
	}
}

func TestControllerPublishVolume_RWOConflictHintsRWX(t *testing.T) {
	ctx := context.Background()
	node1 := testNode("node-1")
	node2 := testNode("node-2")
	cs, mockRDS := testControllerServer(t, node1, node2)

	volumeID := testVolumeID2
	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:        volumeID,
		NVMETCPNQN:  "nqn.2000-02.com.mikrotik:" + volumeID,
		NVMETCPPort: 4420,
	})

	rwoCap := &csi.VolumeCapability{
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
		AccessType: &csi.VolumeCapability_Block{
			Block: &csi.VolumeCapability_BlockVolume{},
		},
	}

	// First attach should succeed
	req1 := &csi.ControllerPublishVolumeRequest{
		VolumeId:         volumeID,
		NodeId:           "node-1",
		VolumeCapability: rwoCap,
	}
	_, err := cs.ControllerPublishVolume(ctx, req1)
	if err != nil {
		t.Fatalf("first attach failed: %v", err)
	}

	// Second attach should fail with RWX hint
	req2 := &csi.ControllerPublishVolumeRequest{
		VolumeId:         volumeID,
		NodeId:           "node-2",
		VolumeCapability: rwoCap,
	}
	_, err = cs.ControllerPublishVolume(ctx, req2)
	if err == nil {
		t.Fatal("expected error for RWO conflict, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %v", err)
	}
	if st.Code() != codes.FailedPrecondition {
		t.Errorf("expected FailedPrecondition, got %v", st.Code())
	}
	if !strings.Contains(st.Message(), "RWX") {
		t.Errorf("expected RWX hint in error message, got %q", st.Message())
	}
}

func TestControllerPublishVolume_RWXIdempotent(t *testing.T) {
	ctx := context.Background()
	node1 := testNode("node-1")
	cs, mockRDS := testControllerServer(t, node1)

	volumeID := testVolumeID3
	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:        volumeID,
		NVMETCPNQN:  "nqn.2000-02.com.mikrotik:" + volumeID,
		NVMETCPPort: 4420,
	})

	rwxCap := &csi.VolumeCapability{
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
		},
		AccessType: &csi.VolumeCapability_Block{
			Block: &csi.VolumeCapability_BlockVolume{},
		},
	}

	req := &csi.ControllerPublishVolumeRequest{
		VolumeId:         volumeID,
		NodeId:           "node-1",
		VolumeCapability: rwxCap,
	}

	// First call
	_, err := cs.ControllerPublishVolume(ctx, req)
	if err != nil {
		t.Fatalf("first attach failed: %v", err)
	}

	// Second call (idempotent) - should succeed
	_, err = cs.ControllerPublishVolume(ctx, req)
	if err != nil {
		t.Fatalf("idempotent attach failed: %v", err)
	}
}

func TestValidateVolumeCapabilities_ErrorMessageStructure(t *testing.T) {
	cs, _ := testControllerServer(t)

	// RWX filesystem should produce actionable error
	err := cs.validateVolumeCapabilities([]*csi.VolumeCapability{
		{
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
			},
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{FsType: "ext4"},
			},
		},
	})

	if err == nil {
		t.Fatal("expected error for RWX filesystem")
	}

	errMsg := err.Error()

	// WHAT: identifies the problem
	if !strings.Contains(errMsg, "RWX") && !strings.Contains(errMsg, "MULTI_NODE") {
		t.Error("error should mention RWX or MULTI_NODE (what's wrong)")
	}

	// HOW: provides remediation
	if !strings.Contains(errMsg, "volumeMode: Block") {
		t.Error("error should provide fix (use volumeMode: Block)")
	}
}

func TestControllerPublishVolume_MigrationTimeout(t *testing.T) {
	// Use valid UUID format for volume ID
	testVolID := "pvc-11111111-2222-3333-4444-555555555555"

	// Test that timed-out migrations reject new secondary attachments
	tests := []struct {
		name          string
		setupState    func(am *attachment.AttachmentManager)
		expectError   bool
		errorContains string
	}{
		{
			name: "allow secondary attachment - migration not started",
			setupState: func(am *attachment.AttachmentManager) {
				// Primary attachment exists, no migration yet
				_ = am.TrackAttachmentWithMode(context.Background(), testVolID, "node-1", "RWX")
			},
			expectError: false,
		},
		{
			name: "allow secondary attachment - migration within timeout",
			setupState: func(am *attachment.AttachmentManager) {
				_ = am.TrackAttachmentWithMode(context.Background(), testVolID, "node-1", "RWX")
				// Simulate recent migration start (1 minute ago)
				state, _ := am.GetAttachment(testVolID)
				recentTime := time.Now().Add(-1 * time.Minute)
				state.MigrationStartedAt = &recentTime
				state.MigrationTimeout = 5 * time.Minute
			},
			expectError: false,
		},
		{
			name: "reject secondary attachment - migration timed out",
			setupState: func(am *attachment.AttachmentManager) {
				_ = am.TrackAttachmentWithMode(context.Background(), testVolID, "node-1", "RWX")
				// Simulate old migration start (10 minutes ago)
				state, _ := am.GetAttachment(testVolID)
				oldTime := time.Now().Add(-10 * time.Minute)
				state.MigrationStartedAt = &oldTime
				state.MigrationTimeout = 5 * time.Minute
			},
			expectError:   true,
			errorContains: "migration timeout exceeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			node1 := testNode("node-1")
			node2 := testNode("node-2")

			// Setup driver with attachment manager
			k8sClient := fake.NewSimpleClientset(node1, node2)
			mockRDS := rds.NewMockClient()
			mockRDS.SetAddress("10.0.0.1")

			am := attachment.NewAttachmentManager(k8sClient)
			tt.setupState(am)

			driver := &Driver{
				name:              DriverName,
				version:           "test",
				rdsClient:         mockRDS,
				k8sClient:         k8sClient,
				attachmentManager: am,
			}
			driver.addVolumeCapabilities()
			driver.addControllerServiceCapabilities()

			cs := NewControllerServer(driver)

			// Add volume to mock
			mockRDS.AddVolume(&rds.VolumeInfo{
				Slot:        testVolID,
				NVMETCPNQN:  "nqn.2000-02.com.mikrotik:" + testVolID,
				NVMETCPPort: 4420,
			})

			// Try to publish to a different node
			req := &csi.ControllerPublishVolumeRequest{
				VolumeId: testVolID,
				NodeId:   "node-2", // Different from node-1
				VolumeCapability: &csi.VolumeCapability{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
					},
					AccessType: &csi.VolumeCapability_Block{
						Block: &csi.VolumeCapability_BlockVolume{},
					},
				},
				VolumeContext: map[string]string{
					"migrationTimeoutSeconds": "300",
				},
			}

			_, err := cs.ControllerPublishVolume(ctx, req)

			if tt.expectError {
				if err == nil {
					t.Error("expected error but got nil")
				} else if tt.errorContains != "" && !strings.Contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error containing %q, got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// ========================================
// Error Path Tests (Phase 25-01)
// ========================================

func TestCreateVolume_ErrorScenarios(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(*rds.MockClient)
		requestName   string
		requestSize   int64
		expectCode    codes.Code
		errorContains string
	}{
		{
			name: "SSH connection failure returns Unavailable",
			setupMock: func(m *rds.MockClient) {
				m.SetPersistentError(fmt.Errorf("ssh: %w", utils.ErrConnectionFailed))
			},
			requestName:   "test-volume",
			requestSize:   1 * 1024 * 1024 * 1024,
			expectCode:    codes.Unavailable,
			errorContains: "RDS unavailable",
		},
		{
			name: "SSH timeout returns Unavailable",
			setupMock: func(m *rds.MockClient) {
				m.SetPersistentError(fmt.Errorf("operation timed out: %w", utils.ErrOperationTimeout))
			},
			requestName:   "test-volume",
			requestSize:   1 * 1024 * 1024 * 1024,
			expectCode:    codes.Unavailable,
			errorContains: "RDS unavailable",
		},
		{
			name: "Disk full returns ResourceExhausted",
			setupMock: func(m *rds.MockClient) {
				m.SetPersistentError(fmt.Errorf("not enough space on device: %w", utils.ErrResourceExhausted))
			},
			requestName:   "test-volume",
			requestSize:   1 * 1024 * 1024 * 1024,
			expectCode:    codes.ResourceExhausted,
			errorContains: "insufficient storage",
		},
		{
			name: "Generic error returns Internal",
			setupMock: func(m *rds.MockClient) {
				m.SetPersistentError(fmt.Errorf("unexpected error"))
			},
			requestName:   "test-volume",
			requestSize:   1 * 1024 * 1024 * 1024,
			expectCode:    codes.Internal,
			errorContains: "failed to create volume",
		},
		{
			name: "Empty volume name returns InvalidArgument",
			setupMock: func(m *rds.MockClient) {
				// No error setup needed - validation happens before RDS call
			},
			requestName:   "",
			requestSize:   1 * 1024 * 1024 * 1024,
			expectCode:    codes.InvalidArgument,
			errorContains: "volume name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cs, mockRDS := testControllerServer(t)

			// Setup mock behavior
			tt.setupMock(mockRDS)

			// Create request
			req := &csi.CreateVolumeRequest{
				Name: tt.requestName,
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
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: tt.requestSize,
				},
			}

			// Call CreateVolume
			_, err := cs.CreateVolume(ctx, req)

			// Verify error
			if err == nil {
				t.Fatal("Expected error but got nil")
			}

			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("Expected gRPC status error, got: %T %v", err, err)
			}

			if st.Code() != tt.expectCode {
				t.Errorf("Expected code %v, got %v", tt.expectCode, st.Code())
			}

			if !strings.Contains(st.Message(), tt.errorContains) {
				t.Errorf("Expected error containing %q, got %q", tt.errorContains, st.Message())
			}
		})
	}
}

func TestDeleteVolume_ErrorScenarios(t *testing.T) {
	tests := []struct {
		name          string
		volumeID      string
		setupMock     func(*rds.MockClient)
		expectCode    codes.Code
		errorContains string
	}{
		{
			name:     "SSH failure during delete returns Unavailable",
			volumeID: testVolumeID1,
			setupMock: func(m *rds.MockClient) {
				// Add volume first
				m.AddVolume(&rds.VolumeInfo{
					Slot:          testVolumeID1,
					FileSizeBytes: 1024 * 1024 * 1024,
				})
				// Set persistent error for all operations
				m.SetPersistentError(fmt.Errorf("ssh: %w", utils.ErrConnectionFailed))
			},
			expectCode:    codes.Unavailable,
			errorContains: "RDS unavailable",
		},
		{
			name:     "SSH timeout during delete returns Unavailable",
			volumeID: testVolumeID2,
			setupMock: func(m *rds.MockClient) {
				m.AddVolume(&rds.VolumeInfo{
					Slot:          testVolumeID2,
					FileSizeBytes: 1024 * 1024 * 1024,
				})
				m.SetPersistentError(fmt.Errorf("timeout: %w", utils.ErrOperationTimeout))
			},
			expectCode:    codes.Unavailable,
			errorContains: "RDS unavailable",
		},
		{
			name:     "Invalid volume ID format returns InvalidArgument",
			volumeID: "invalid; rm -rf /",
			setupMock: func(m *rds.MockClient) {
				// No setup needed - validation happens before RDS call
			},
			expectCode:    codes.InvalidArgument,
			errorContains: "invalid volume ID",
		},
		{
			name:     "Empty volume ID returns InvalidArgument",
			volumeID: "",
			setupMock: func(m *rds.MockClient) {
				// No setup needed - validation happens before RDS call
			},
			expectCode:    codes.InvalidArgument,
			errorContains: "volume ID is required",
		},
		{
			name:     "Idempotent delete of non-existent volume succeeds",
			volumeID: testVolumeID3,
			setupMock: func(m *rds.MockClient) {
				// Don't add volume - it doesn't exist
			},
			expectCode:    codes.OK,
			errorContains: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cs, mockRDS := testControllerServer(t)

			// Setup mock behavior
			tt.setupMock(mockRDS)

			// Create request
			req := &csi.DeleteVolumeRequest{
				VolumeId: tt.volumeID,
			}

			// Call DeleteVolume
			_, err := cs.DeleteVolume(ctx, req)

			if tt.expectCode == codes.OK {
				// Success case - no error expected
				if err != nil {
					t.Errorf("Expected success but got error: %v", err)
				}
				return
			}

			// Error cases
			if err == nil {
				t.Fatal("Expected error but got nil")
			}

			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("Expected gRPC status error, got: %T %v", err, err)
			}

			if st.Code() != tt.expectCode {
				t.Errorf("Expected code %v, got %v", tt.expectCode, st.Code())
			}

			if tt.errorContains != "" && !strings.Contains(st.Message(), tt.errorContains) {
				t.Errorf("Expected error containing %q, got %q", tt.errorContains, st.Message())
			}
		})
	}
}

// TestCSI_NegativeScenarios_Controller validates CSI spec error code requirements
// for controller service operations. Each test case documents the specific CSI
// spec section that mandates the error code behavior.
//
// CSI Spec Reference: https://github.com/container-storage-interface/spec/blob/master/spec.md
func TestCSI_NegativeScenarios_Controller(t *testing.T) {
	tests := []struct {
		name       string
		method     string // CSI method name
		setupMock  func(*rds.MockClient)
		request    interface{} // CSI request object
		wantCode   codes.Code
		wantErrMsg string
		specRef    string // CSI spec section reference
	}{
		// CreateVolume - CSI spec section 3.4
		{
			name:   "CreateVolume: missing volume name",
			method: "CreateVolume",
			request: &csi.CreateVolumeRequest{
				Name: "", // Missing required field
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
			},
			wantCode:   codes.InvalidArgument,
			wantErrMsg: "volume name is required",
			specRef:    "CSI 3.4 CreateVolume: name field is REQUIRED",
		},
		{
			name:   "CreateVolume: missing volume capabilities",
			method: "CreateVolume",
			request: &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: nil, // Missing required field
			},
			wantCode:   codes.InvalidArgument,
			wantErrMsg: "volume capabilities are required",
			specRef:    "CSI 3.4 CreateVolume: volume_capabilities field is REQUIRED",
		},
		{
			name:   "CreateVolume: empty volume capabilities array",
			method: "CreateVolume",
			request: &csi.CreateVolumeRequest{
				Name:               "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{}, // Empty array
			},
			wantCode:   codes.InvalidArgument,
			wantErrMsg: "volume capabilities are required",
			specRef:    "CSI 3.4 CreateVolume: at least one capability required",
		},
		{
			name:   "CreateVolume: unsupported RWX filesystem",
			method: "CreateVolume",
			request: &csi.CreateVolumeRequest{
				Name: "test-volume",
				VolumeCapabilities: []*csi.VolumeCapability{
					{
						AccessMode: &csi.VolumeCapability_AccessMode{
							Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER, // RWX filesystem unsupported
						},
						AccessType: &csi.VolumeCapability_Mount{
							Mount: &csi.VolumeCapability_MountVolume{},
						},
					},
				},
			},
			wantCode:   codes.InvalidArgument,
			wantErrMsg: "RWX access mode requires volumeMode: Block",
			specRef:    "CSI 3.4 CreateVolume: unsupported RWX filesystem returns InvalidArgument",
		},
		{
			name:   "CreateVolume: required > limit capacity",
			method: "CreateVolume",
			request: &csi.CreateVolumeRequest{
				Name: "test-volume",
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
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 100 * 1024 * 1024 * 1024, // 100 GiB
					LimitBytes:    10 * 1024 * 1024 * 1024,  // 10 GiB - less than required
				},
			},
			wantCode:   codes.OutOfRange,
			wantErrMsg: "exceeds limit",
			specRef:    "CSI 3.4 CreateVolume: required > limit returns OutOfRange",
		},
		{
			name:   "CreateVolume: exceeds maximum volume size",
			method: "CreateVolume",
			request: &csi.CreateVolumeRequest{
				Name: "test-volume",
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
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 20 * 1024 * 1024 * 1024 * 1024, // 20 TiB - exceeds 16 TiB max
				},
			},
			wantCode:   codes.OutOfRange,
			wantErrMsg: "exceeds maximum",
			specRef:    "CSI 3.4 CreateVolume: exceeds max capacity returns OutOfRange",
		},
		{
			name:   "CreateVolume: insufficient capacity on RDS",
			method: "CreateVolume",
			setupMock: func(m *rds.MockClient) {
				m.SetPersistentError(fmt.Errorf("disk full: %w", utils.ErrResourceExhausted))
			},
			request: &csi.CreateVolumeRequest{
				Name: "test-volume",
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
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1 * 1024 * 1024 * 1024,
				},
			},
			wantCode:   codes.ResourceExhausted,
			wantErrMsg: "insufficient storage",
			specRef:    "CSI 3.4 CreateVolume: insufficient capacity returns ResourceExhausted",
		},

		// DeleteVolume - CSI spec section 3.5
		{
			name:   "DeleteVolume: missing volume ID",
			method: "DeleteVolume",
			request: &csi.DeleteVolumeRequest{
				VolumeId: "", // Missing required field
			},
			wantCode:   codes.InvalidArgument,
			wantErrMsg: "volume ID is required",
			specRef:    "CSI 3.5 DeleteVolume: volume_id field is REQUIRED",
		},
		{
			name:   "DeleteVolume: volume not found (idempotent)",
			method: "DeleteVolume",
			setupMock: func(m *rds.MockClient) {
				// Volume doesn't exist - driver validates format first
			},
			request: &csi.DeleteVolumeRequest{
				VolumeId: testVolumeID1, // Valid format but doesn't exist
			},
			wantCode:   codes.OK,
			wantErrMsg: "",
			specRef:    "CSI 3.5 DeleteVolume: nonexistent volume returns success (idempotent)",
		},
		{
			name:   "DeleteVolume: invalid volume ID format",
			method: "DeleteVolume",
			request: &csi.DeleteVolumeRequest{
				VolumeId: "invalid; rm -rf /", // Command injection attempt
			},
			wantCode:   codes.InvalidArgument,
			wantErrMsg: "invalid volume ID",
			specRef:    "CSI 3.5 DeleteVolume: malformed ID returns InvalidArgument",
		},

		// ControllerPublishVolume - CSI spec section 3.6
		{
			name:   "ControllerPublishVolume: missing volume ID",
			method: "ControllerPublishVolume",
			request: &csi.ControllerPublishVolumeRequest{
				VolumeId: "", // Missing required field
				NodeId:   "test-node",
				VolumeCapability: &csi.VolumeCapability{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			wantCode:   codes.InvalidArgument,
			wantErrMsg: "volume ID",
			specRef:    "CSI 3.6 ControllerPublishVolume: volume_id is REQUIRED",
		},
		{
			name:   "ControllerPublishVolume: missing node ID",
			method: "ControllerPublishVolume",
			request: &csi.ControllerPublishVolumeRequest{
				VolumeId: testVolumeID1,
				NodeId:   "", // Missing required field
				VolumeCapability: &csi.VolumeCapability{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			wantCode:   codes.InvalidArgument,
			wantErrMsg: "node ID",
			specRef:    "CSI 3.6 ControllerPublishVolume: node_id is REQUIRED",
		},
		{
			name:   "ControllerPublishVolume: volume not found",
			method: "ControllerPublishVolume",
			setupMock: func(m *rds.MockClient) {
				// Volume doesn't exist - but must have valid format
			},
			request: &csi.ControllerPublishVolumeRequest{
				VolumeId: testVolumeID2, // Valid format but doesn't exist
				NodeId:   "test-node",
				VolumeCapability: &csi.VolumeCapability{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
					},
					AccessType: &csi.VolumeCapability_Mount{
						Mount: &csi.VolumeCapability_MountVolume{},
					},
				},
			},
			wantCode:   codes.NotFound,
			wantErrMsg: "not found",
			specRef:    "CSI 3.6 ControllerPublishVolume: nonexistent volume returns NotFound",
		},
		// Note: RWO conflict test requires prior attachment tracking which needs K8s client setup
		// This is covered in existing TestControllerPublishVolume_RWOConflict

		// ControllerUnpublishVolume - CSI spec section 3.7
		{
			name:   "ControllerUnpublishVolume: missing volume ID",
			method: "ControllerUnpublishVolume",
			request: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "", // Missing required field
				NodeId:   "test-node",
			},
			wantCode:   codes.InvalidArgument,
			wantErrMsg: "volume ID",
			specRef:    "CSI 3.7 ControllerUnpublishVolume: volume_id is REQUIRED",
		},
		{
			name:   "ControllerUnpublishVolume: not published (idempotent)",
			method: "ControllerUnpublishVolume",
			setupMock: func(m *rds.MockClient) {
				m.AddVolume(&rds.VolumeInfo{
					Slot:          testVolumeID1,
					FileSizeBytes: 1 * 1024 * 1024 * 1024,
				})
			},
			request: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: testVolumeID1,
				NodeId:   "test-node",
			},
			wantCode:   codes.OK,
			wantErrMsg: "",
			specRef:    "CSI 3.7 ControllerUnpublishVolume: not published returns success (idempotent)",
		},

		// ValidateVolumeCapabilities - CSI spec section 3.8
		{
			name:   "ValidateVolumeCapabilities: missing volume ID",
			method: "ValidateVolumeCapabilities",
			request: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           "", // Missing required field
				VolumeCapabilities: []*csi.VolumeCapability{},
			},
			wantCode:   codes.InvalidArgument,
			wantErrMsg: "volume ID",
			specRef:    "CSI 3.8 ValidateVolumeCapabilities: volume_id is REQUIRED",
		},
		{
			name:   "ValidateVolumeCapabilities: volume not found",
			method: "ValidateVolumeCapabilities",
			setupMock: func(m *rds.MockClient) {
				// Volume doesn't exist - valid format required
			},
			request: &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId: testVolumeID3, // Valid format but doesn't exist
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
			},
			wantCode:   codes.NotFound,
			wantErrMsg: "not found",
			specRef:    "CSI 3.8 ValidateVolumeCapabilities: nonexistent volume returns NotFound",
		},

		// ControllerExpandVolume - CSI spec section 3.11
		{
			name:   "ControllerExpandVolume: missing volume ID",
			method: "ControllerExpandVolume",
			request: &csi.ControllerExpandVolumeRequest{
				VolumeId: "", // Missing required field
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 2 * 1024 * 1024 * 1024,
				},
			},
			wantCode:   codes.InvalidArgument,
			wantErrMsg: "volume ID",
			specRef:    "CSI 3.11 ControllerExpandVolume: volume_id is REQUIRED",
		},
		{
			name:   "ControllerExpandVolume: volume not found",
			method: "ControllerExpandVolume",
			setupMock: func(m *rds.MockClient) {
				// Volume doesn't exist - valid format required
			},
			request: &csi.ControllerExpandVolumeRequest{
				VolumeId: testVolumeID4, // Valid format but doesn't exist
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 2 * 1024 * 1024 * 1024,
				},
			},
			wantCode:   codes.NotFound,
			wantErrMsg: "not found",
			specRef:    "CSI 3.11 ControllerExpandVolume: nonexistent volume returns NotFound",
		},
		{
			name:   "ControllerExpandVolume: idempotent (already at size)",
			method: "ControllerExpandVolume",
			setupMock: func(m *rds.MockClient) {
				m.AddVolume(&rds.VolumeInfo{
					Slot:          testVolumeID1,
					FileSizeBytes: 10 * 1024 * 1024 * 1024, // 10 GiB
				})
			},
			request: &csi.ControllerExpandVolumeRequest{
				VolumeId: testVolumeID1,
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 10 * 1024 * 1024 * 1024, // Same as current size
				},
			},
			wantCode:   codes.OK,
			wantErrMsg: "",
			specRef:    "CSI 3.11 ControllerExpandVolume: idempotent when already at requested size",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cs, mockRDS := testControllerServer(t)

			// Setup mock behavior
			if tt.setupMock != nil {
				tt.setupMock(mockRDS)
			}

			var err error
			switch tt.method {
			case "CreateVolume":
				_, err = cs.CreateVolume(ctx, tt.request.(*csi.CreateVolumeRequest))
			case "DeleteVolume":
				_, err = cs.DeleteVolume(ctx, tt.request.(*csi.DeleteVolumeRequest))
			case "ControllerPublishVolume":
				_, err = cs.ControllerPublishVolume(ctx, tt.request.(*csi.ControllerPublishVolumeRequest))
			case "ControllerUnpublishVolume":
				_, err = cs.ControllerUnpublishVolume(ctx, tt.request.(*csi.ControllerUnpublishVolumeRequest))
			case "ValidateVolumeCapabilities":
				_, err = cs.ValidateVolumeCapabilities(ctx, tt.request.(*csi.ValidateVolumeCapabilitiesRequest))
			case "ControllerExpandVolume":
				_, err = cs.ControllerExpandVolume(ctx, tt.request.(*csi.ControllerExpandVolumeRequest))
			default:
				t.Fatalf("Unknown method: %s", tt.method)
			}

			// Verify error code
			if tt.wantCode == codes.OK {
				if err != nil {
					t.Errorf("[%s] Expected success, got error: %v", tt.specRef, err)
				}
				return
			}

			if err == nil {
				t.Fatalf("[%s] Expected error code %v, got nil", tt.specRef, tt.wantCode)
			}

			st, ok := status.FromError(err)
			if !ok {
				t.Fatalf("[%s] Expected gRPC status error, got: %T %v", tt.specRef, err, err)
			}

			if st.Code() != tt.wantCode {
				t.Errorf("[%s] Expected code %v, got %v\nError message: %s",
					tt.specRef, tt.wantCode, st.Code(), st.Message())
			}

			if tt.wantErrMsg != "" && !strings.Contains(st.Message(), tt.wantErrMsg) {
				t.Errorf("[%s] Expected error containing %q, got %q",
					tt.specRef, tt.wantErrMsg, st.Message())
			}
		})
	}
}

// TestSanityRegression_CreateVolumeZeroCapacity is a regression test
// for CSI sanity edge case: capacity_range with required_bytes=0.
// Driver should use default minimum capacity (1 GiB).
func TestSanityRegression_CreateVolumeZeroCapacity(t *testing.T) {
	ctx := context.Background()
	cs, _ := testControllerServer(t)

	req := &csi.CreateVolumeRequest{
		Name: "test-volume-zero-capacity",
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
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 0, // Zero capacity - should use default
		},
	}

	resp, err := cs.CreateVolume(ctx, req)
	if err != nil {
		t.Fatalf("Expected success with zero capacity, got error: %v", err)
	}

	if resp.Volume.CapacityBytes < 1*1024*1024*1024 {
		t.Errorf("Expected minimum 1 GiB capacity, got %d bytes", resp.Volume.CapacityBytes)
	}

	// Cleanup
	_, _ = cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: resp.Volume.VolumeId})
}

// TestSanityRegression_CreateVolumeMaxInt64Capacity is a regression test
// for CSI sanity edge case: max int64 capacity should return OutOfRange.
func TestSanityRegression_CreateVolumeMaxInt64Capacity(t *testing.T) {
	ctx := context.Background()
	cs, _ := testControllerServer(t)

	req := &csi.CreateVolumeRequest{
		Name: "test-volume-max-capacity",
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
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 9223372036854775807, // Max int64
		},
	}

	_, err := cs.CreateVolume(ctx, req)
	if err == nil {
		t.Fatal("Expected error for max int64 capacity, got nil")
	}

	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("Expected gRPC status error, got: %v", err)
	}

	if st.Code() != codes.OutOfRange {
		t.Errorf("Expected OutOfRange error, got: %v", st.Code())
	}
}

// TestSanityRegression_CreateVolumeReadOnly is a regression test
// for CSI sanity edge case: readonly flag validation.
func TestSanityRegression_CreateVolumeReadOnly(t *testing.T) {
	ctx := context.Background()
	cs, _ := testControllerServer(t)

	req := &csi.CreateVolumeRequest{
		Name: "test-volume-readonly",
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
				},
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{
						MountFlags: []string{"ro"},
					},
				},
			},
		},
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 1 * 1024 * 1024 * 1024,
		},
	}

	resp, err := cs.CreateVolume(ctx, req)
	if err != nil {
		t.Fatalf("Expected success for readonly volume, got error: %v", err)
	}

	// Cleanup
	_, _ = cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: resp.Volume.VolumeId})
}

// TestSanityRegression_DeleteVolumeIdempotency is a regression test
// for CSI sanity requirement: DeleteVolume must be idempotent.
// Calling DeleteVolume multiple times should always succeed.
func TestSanityRegression_DeleteVolumeIdempotency(t *testing.T) {
	ctx := context.Background()
	cs, _ := testControllerServer(t)

	// Create a volume
	createReq := &csi.CreateVolumeRequest{
		Name: "test-volume-delete-idempotent",
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
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: 1 * 1024 * 1024 * 1024,
		},
	}

	createResp, err := cs.CreateVolume(ctx, createReq)
	if err != nil {
		t.Fatalf("Failed to create volume: %v", err)
	}

	volumeID := createResp.Volume.VolumeId

	// Delete the volume
	deleteReq := &csi.DeleteVolumeRequest{VolumeId: volumeID}

	_, err = cs.DeleteVolume(ctx, deleteReq)
	if err != nil {
		t.Fatalf("First delete failed: %v", err)
	}

	// Delete again - should succeed (idempotent)
	_, err = cs.DeleteVolume(ctx, deleteReq)
	if err != nil {
		t.Errorf("Second delete (idempotency test) failed: %v", err)
	}

	// Delete third time - still should succeed
	_, err = cs.DeleteVolume(ctx, deleteReq)
	if err != nil {
		t.Errorf("Third delete (idempotency test) failed: %v", err)
	}
}

// TestSanityRegression_VolumeContextParameters is a regression test
// for CSI sanity edge case: volume_context parameter validation.
func TestSanityRegression_VolumeContextParameters(t *testing.T) {
	ctx := context.Background()
	cs, _ := testControllerServer(t)

	tests := []struct {
		name    string
		params  map[string]string
		wantErr bool
	}{
		{
			name: "valid fsType",
			params: map[string]string{
				"fsType": "ext4",
			},
			wantErr: false,
		},
		{
			name: "valid nvmeAddress",
			params: map[string]string{
				"nvmeAddress": "10.42.68.1",
			},
			wantErr: false,
		},
		// Note: parameter validation happens at NodeStageVolume, not CreateVolume
		// CreateVolume accepts parameters and stores them in VolumeContext
		{
			name:    "empty parameters",
			params:  map[string]string{},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &csi.CreateVolumeRequest{
				Name: "test-volume-params-" + strings.ReplaceAll(tt.name, " ", "-"),
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
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: 1 * 1024 * 1024 * 1024,
				},
				Parameters: tt.params,
			}

			resp, err := cs.CreateVolume(ctx, req)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error for params %v, got nil", tt.params)
					if resp != nil {
						cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: resp.Volume.VolumeId})
					}
				}
			} else {
				if err != nil {
					t.Errorf("Expected success for params %v, got error: %v", tt.params, err)
				} else {
					// Cleanup
					cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: resp.Volume.VolumeId})
				}
			}
		})
	}
}

// ========================================
// Snapshot Tests
// ========================================

// Test snapshot IDs (snap-<uuid> format)
const (
// Snapshot IDs for future test use
// testSnapshotID1 = "snap-11111111-1111-1111-1111-111111111111"
// testSnapshotID2 = "snap-22222222-2222-2222-2222-222222222222"
// testSnapshotID3 = "snap-33333333-3333-3333-3333-333333333333"
)

func TestCreateSnapshot(t *testing.T) {
	ctx := context.Background()
	cs, mockRDS := testControllerServer(t)

	// Add a test source volume
	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:          testVolumeID1,
		FilePath:      "/storage-pool/metal-csi/" + testVolumeID1 + ".img",
		FileSizeBytes: 10 * 1024 * 1024 * 1024, // 10 GiB
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2000-02.com.mikrotik:" + testVolumeID1,
	})

	tests := []struct {
		name           string
		snapshotName   string
		sourceVolumeID string
		wantErr        bool
		wantCode       codes.Code
	}{
		{
			name:           "success: create snapshot with valid source",
			snapshotName:   "test-snapshot-1",
			sourceVolumeID: testVolumeID1,
			wantErr:        false,
		},
		{
			name:           "error: missing snapshot name",
			snapshotName:   "",
			sourceVolumeID: testVolumeID1,
			wantErr:        true,
			wantCode:       codes.InvalidArgument,
		},
		{
			name:           "error: missing source volume ID",
			snapshotName:   "test-snapshot-2",
			sourceVolumeID: "",
			wantErr:        true,
			wantCode:       codes.InvalidArgument,
		},
		{
			name:           "error: source volume not found",
			snapshotName:   "test-snapshot-3",
			sourceVolumeID: "pvc-99999999-9999-9999-9999-999999999999", // Valid format but doesn't exist
			wantErr:        true,
			wantCode:       codes.NotFound,
		},
		{
			// With deterministic IDs, the same (name, source) pair always produces the same ID.
			// A second call with a different name but same source creates a distinct snapshot.
			name:           "success: second snapshot from same source (different name)",
			snapshotName:   "test-snapshot-1-again",
			sourceVolumeID: testVolumeID1,
			wantErr:        false,
		},
	}

	// Pre-create second volume for future tests
	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:          testVolumeID2,
		FilePath:      "/storage-pool/metal-csi/" + testVolumeID2 + ".img",
		FileSizeBytes: 10 * 1024 * 1024 * 1024,
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2000-02.com.mikrotik:" + testVolumeID2,
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &csi.CreateSnapshotRequest{
				Name:           tt.snapshotName,
				SourceVolumeId: tt.sourceVolumeID,
			}

			resp, err := cs.CreateSnapshot(ctx, req)

			if tt.wantErr {
				if err == nil {
					t.Errorf("Expected error but got nil")
					// Cleanup created snapshot
					if resp != nil {
						_ = mockRDS.DeleteSnapshot(resp.Snapshot.SnapshotId)
					}
					return
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("Expected gRPC status error, got: %T", err)
					return
				}
				if st.Code() != tt.wantCode {
					t.Errorf("Expected code %v, got %v", tt.wantCode, st.Code())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
					return
				}
				if resp == nil || resp.Snapshot == nil {
					t.Error("Expected snapshot response, got nil")
					return
				}
				if resp.Snapshot.SourceVolumeId != tt.sourceVolumeID {
					t.Errorf("Expected source volume %s, got %s", tt.sourceVolumeID, resp.Snapshot.SourceVolumeId)
				}
				if !resp.Snapshot.ReadyToUse {
					t.Error("Expected ReadyToUse=true for CoW snapshot")
				}
			}
		})
	}
}

func TestCreateSnapshotIdempotency(t *testing.T) {
	ctx := context.Background()
	cs, mockRDS := testControllerServer(t)

	// Add a test source volume
	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:          testVolumeID1,
		FilePath:      "/storage-pool/metal-csi/" + testVolumeID1 + ".img",
		FileSizeBytes: 10 * 1024 * 1024 * 1024,
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2000-02.com.mikrotik:" + testVolumeID1,
	})

	snapshotName := "test-idempotent-snapshot"

	// First call: creates the snapshot
	resp1, err := cs.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{
		Name:           snapshotName,
		SourceVolumeId: testVolumeID1,
	})
	if err != nil {
		t.Fatalf("First CreateSnapshot failed: %v", err)
	}
	if resp1.Snapshot == nil {
		t.Fatal("First CreateSnapshot returned nil snapshot")
	}
	snapshotID1 := resp1.Snapshot.SnapshotId

	// Second call with SAME name + source: must return the same snapshot ID (idempotent)
	resp2, err := cs.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{
		Name:           snapshotName,
		SourceVolumeId: testVolumeID1,
	})
	if err != nil {
		t.Fatalf("Second CreateSnapshot (idempotent) failed: %v", err)
	}
	if resp2.Snapshot == nil {
		t.Fatal("Second CreateSnapshot returned nil snapshot")
	}
	snapshotID2 := resp2.Snapshot.SnapshotId

	if snapshotID1 != snapshotID2 {
		t.Errorf("Idempotency violated: first call returned %s, second call returned %s", snapshotID1, snapshotID2)
	}

	// Verify expected ID format: snap-<source-uuid>-at-<hash>
	expectedID := utils.GenerateSnapshotID(snapshotName, testVolumeID1)
	if snapshotID1 != expectedID {
		t.Errorf("Snapshot ID format mismatch: got %s, expected %s", snapshotID1, expectedID)
	}

	// Third call with different name but same source: creates a DIFFERENT snapshot
	resp3, err := cs.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{
		Name:           "different-snapshot-name",
		SourceVolumeId: testVolumeID1,
	})
	if err != nil {
		t.Fatalf("Third CreateSnapshot (different name) failed: %v", err)
	}
	if resp3.Snapshot.SnapshotId == snapshotID1 {
		t.Errorf("Different snapshot name should produce different ID, but got same: %s", snapshotID1)
	}

	// Cleanup
	_ = mockRDS.DeleteSnapshot(snapshotID1)
	_ = mockRDS.DeleteSnapshot(resp3.Snapshot.SnapshotId)
}

func TestDeleteSnapshot(t *testing.T) {
	ctx := context.Background()
	cs, mockRDS := testControllerServer(t)

	// Add a test volume
	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:          testVolumeID1,
		FilePath:      "/storage-pool/metal-csi/" + testVolumeID1 + ".img",
		FileSizeBytes: 10 * 1024 * 1024 * 1024,
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2000-02.com.mikrotik:" + testVolumeID1,
	})

	// Create a test snapshot
	createResp, err := cs.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{
		Name:           "test-snapshot-delete",
		SourceVolumeId: testVolumeID1,
	})
	if err != nil {
		t.Fatalf("Failed to create test snapshot: %v", err)
	}
	snapshotID := createResp.Snapshot.SnapshotId

	tests := []struct {
		name       string
		snapshotID string
		wantErr    bool
		wantCode   codes.Code
	}{
		{
			name:       "success: delete existing snapshot",
			snapshotID: snapshotID,
			wantErr:    false,
		},
		{
			name:       "error: missing snapshot ID",
			snapshotID: "",
			wantErr:    true,
			wantCode:   codes.InvalidArgument,
		},
		{
			name:       "idempotent: delete non-existent snapshot",
			snapshotID: "snap-99999999-9999-9999-9999-999999999999", // Valid format but doesn't exist
			wantErr:    false,                                       // Per CSI spec, not found = success
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &csi.DeleteSnapshotRequest{
				SnapshotId: tt.snapshotID,
			}

			_, err := cs.DeleteSnapshot(ctx, req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("Expected gRPC status error, got: %T", err)
					return
				}
				if st.Code() != tt.wantCode {
					t.Errorf("Expected code %v, got %v", tt.wantCode, st.Code())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
				}
			}
		})
	}
}

func TestListSnapshots(t *testing.T) {
	ctx := context.Background()
	cs, mockRDS := testControllerServer(t)

	// Add test volumes
	for i, volID := range []string{testVolumeID1, testVolumeID2, testVolumeID3} {
		mockRDS.AddVolume(&rds.VolumeInfo{
			Slot:          volID,
			FilePath:      fmt.Sprintf("/storage-pool/metal-csi/%s.img", volID),
			FileSizeBytes: int64((i + 1) * 10 * 1024 * 1024 * 1024),
			NVMETCPPort:   4420,
			NVMETCPNQN:    "nqn.2000-02.com.mikrotik:" + volID,
		})
	}

	// Insert test snapshots directly into mock to avoid timestamp collision
	// (timestamp-based IDs use time.Now().Unix() which may be the same within a test)
	// Extract UUID parts from testVolumeID1 and testVolumeID2 for snapshot IDs
	snapID1 := "snap-11111111-1111-1111-1111-111111111111-at-1739800001"
	snapID2 := "snap-11111111-1111-1111-1111-111111111111-at-1739800002"
	snapID3 := "snap-22222222-2222-2222-2222-222222222222-at-1739800003"

	mockRDS.AddSnapshot(&rds.SnapshotInfo{
		Name:          snapID1,
		SourceVolume:  testVolumeID1,
		FileSizeBytes: 10 * 1024 * 1024 * 1024,
		FilePath:      "/storage-pool/metal-csi/" + snapID1 + ".img",
	})
	mockRDS.AddSnapshot(&rds.SnapshotInfo{
		Name:          snapID2,
		SourceVolume:  testVolumeID1, // Same source as snap1
		FileSizeBytes: 10 * 1024 * 1024 * 1024,
		FilePath:      "/storage-pool/metal-csi/" + snapID2 + ".img",
	})
	mockRDS.AddSnapshot(&rds.SnapshotInfo{
		Name:          snapID3,
		SourceVolume:  testVolumeID2, // Different source
		FileSizeBytes: 20 * 1024 * 1024 * 1024,
		FilePath:      "/storage-pool/metal-csi/" + snapID3 + ".img",
	})

	// Keep snap1 variable for filter-by-ID test
	snap1 := &csi.CreateSnapshotResponse{Snapshot: &csi.Snapshot{SnapshotId: snapID1}}
	snap2 := &csi.CreateSnapshotResponse{Snapshot: &csi.Snapshot{SnapshotId: snapID2}}
	snap3 := &csi.CreateSnapshotResponse{Snapshot: &csi.Snapshot{SnapshotId: snapID3}}
	_ = snap2
	_ = snap3

	tests := []struct {
		name         string
		snapshotID   string
		sourceVolume string
		maxEntries   int32
		startToken   string
		wantCount    int
		wantErr      bool
		wantCode     codes.Code
	}{
		{
			name:      "list all snapshots",
			wantCount: 3,
		},
		{
			name:       "filter by snapshot ID",
			snapshotID: snap1.Snapshot.SnapshotId,
			wantCount:  1,
		},
		{
			name:       "filter by snapshot ID (not found)",
			snapshotID: "snap-nonexistent",
			wantCount:  0, // Empty response, not error
		},
		{
			name:         "filter by source volume",
			sourceVolume: testVolumeID1,
			wantCount:    2, // snap1 and snap2
		},
		{
			name:         "filter by source volume (single match)",
			sourceVolume: testVolumeID2,
			wantCount:    1, // snap3 only
		},
		{
			name:       "pagination: max_entries=1",
			maxEntries: 1,
			wantCount:  1,
		},
		{
			name:       "pagination: max_entries=2",
			maxEntries: 2,
			wantCount:  2,
		},
		{
			name:       "pagination: use starting_token",
			maxEntries: 1,
			startToken: "1", // Start from second entry
			wantCount:  1,
		},
		{
			name:       "error: invalid starting_token",
			startToken: "invalid",
			wantErr:    true,
			wantCode:   codes.Aborted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &csi.ListSnapshotsRequest{
				SnapshotId:     tt.snapshotID,
				SourceVolumeId: tt.sourceVolume,
				MaxEntries:     tt.maxEntries,
				StartingToken:  tt.startToken,
			}

			resp, err := cs.ListSnapshots(ctx, req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					return
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("Expected gRPC status error, got: %T", err)
					return
				}
				if st.Code() != tt.wantCode {
					t.Errorf("Expected code %v, got %v", tt.wantCode, st.Code())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
					return
				}
				if resp == nil {
					t.Error("Expected response, got nil")
					return
				}
				if len(resp.Entries) != tt.wantCount {
					t.Errorf("Expected %d entries, got %d", tt.wantCount, len(resp.Entries))
				}
				// Check pagination token if max_entries was set and more entries exist
				if tt.maxEntries > 0 && len(resp.Entries) == int(tt.maxEntries) {
					if tt.startToken == "" && tt.wantCount < 3 && resp.NextToken == "" {
						t.Error("Expected NextToken to be set for partial results")
					}
				}
			}
		})
	}

	// Cleanup
	_ = mockRDS.DeleteSnapshot(snap1.Snapshot.SnapshotId)
	_ = mockRDS.DeleteSnapshot(snap2.Snapshot.SnapshotId)
	_ = mockRDS.DeleteSnapshot(snap3.Snapshot.SnapshotId)
}

func TestCreateVolumeFromSnapshot(t *testing.T) {
	ctx := context.Background()
	cs, mockRDS := testControllerServer(t)

	// Add test volume
	mockRDS.AddVolume(&rds.VolumeInfo{
		Slot:          testVolumeID1,
		FilePath:      "/storage-pool/metal-csi/" + testVolumeID1 + ".img",
		FileSizeBytes: 10 * 1024 * 1024 * 1024, // 10 GiB
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2000-02.com.mikrotik:" + testVolumeID1,
	})

	// Create test snapshot
	snapResp, err := cs.CreateSnapshot(ctx, &csi.CreateSnapshotRequest{
		Name:           "test-snapshot-restore",
		SourceVolumeId: testVolumeID1,
	})
	if err != nil {
		t.Fatalf("Failed to create test snapshot: %v", err)
	}
	snapshotID := snapResp.Snapshot.SnapshotId

	tests := []struct {
		name          string
		volumeName    string
		snapshotID    string
		requestedSize int64
		wantErr       bool
		wantCode      codes.Code
	}{
		{
			name:          "success: restore from snapshot",
			volumeName:    "restored-volume-1",
			snapshotID:    snapshotID,
			requestedSize: 10 * 1024 * 1024 * 1024, // Same size
			wantErr:       false,
		},
		{
			name:          "success: restore with larger size",
			volumeName:    "restored-volume-2",
			snapshotID:    snapshotID,
			requestedSize: 20 * 1024 * 1024 * 1024, // Larger
			wantErr:       false,
		},
		{
			name:          "error: snapshot not found",
			volumeName:    "restored-volume-3",
			snapshotID:    "snap-99999999-9999-9999-9999-999999999999", // Valid format but doesn't exist
			requestedSize: 10 * 1024 * 1024 * 1024,
			wantErr:       true,
			wantCode:      codes.NotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &csi.CreateVolumeRequest{
				Name: tt.volumeName,
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
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: tt.requestedSize,
				},
				VolumeContentSource: &csi.VolumeContentSource{
					Type: &csi.VolumeContentSource_Snapshot{
						Snapshot: &csi.VolumeContentSource_SnapshotSource{
							SnapshotId: tt.snapshotID,
						},
					},
				},
				Parameters: map[string]string{
					"volumePath": "/storage-pool/metal-csi",
					"nvmePort":   "4420",
				},
			}

			resp, err := cs.CreateVolume(ctx, req)

			if tt.wantErr {
				if err == nil {
					t.Error("Expected error but got nil")
					if resp != nil {
						// Cleanup
						_, _ = cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: resp.Volume.VolumeId})
					}
					return
				}
				st, ok := status.FromError(err)
				if !ok {
					t.Errorf("Expected gRPC status error, got: %T", err)
					return
				}
				if st.Code() != tt.wantCode {
					t.Errorf("Expected code %v, got %v", tt.wantCode, st.Code())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
					return
				}
				if resp == nil || resp.Volume == nil {
					t.Error("Expected volume response, got nil")
					return
				}
				// Verify ContentSource is populated
				if resp.Volume.ContentSource == nil {
					t.Error("Expected ContentSource in response")
				} else {
					snapSource := resp.Volume.ContentSource.GetSnapshot()
					if snapSource == nil || snapSource.SnapshotId != tt.snapshotID {
						t.Errorf("Expected ContentSource snapshot ID %s, got %v", tt.snapshotID, snapSource)
					}
				}
				// Verify size is at least snapshot size
				if resp.Volume.CapacityBytes < snapResp.Snapshot.SizeBytes {
					t.Errorf("Restored volume size %d smaller than snapshot size %d",
						resp.Volume.CapacityBytes, snapResp.Snapshot.SizeBytes)
				}
				// Cleanup
				_, _ = cs.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: resp.Volume.VolumeId})
			}
		})
	}

	// Cleanup snapshot
	_ = mockRDS.DeleteSnapshot(snapshotID)
}
