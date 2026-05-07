package routeros

import (
	"context"
	"testing"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/backend"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/rds"
)

func TestBuildEnsureVolumePlanCreateAndApply(t *testing.T) {
	client := rds.NewMockClient()
	storageBackend := New(client)

	desired := backend.CreateVolumeRequest{
		ID:          "pvc-test-volume",
		Path:        "/storage-pool/metal-csi/pvc-test-volume.img",
		SizeBytes:   10 << 30,
		NVMETCPPort: 4420,
		NVMETCPNQN:  "nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume",
	}

	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionCreate {
		t.Fatalf("expected create plan, got %s", plan.Action)
	}

	result, err := storageBackend.ApplyVolumePlan(context.Background(), plan, backend.VolumeApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyVolumePlan() error = %v", err)
	}
	if !result.Applied {
		t.Fatal("expected plan to be applied")
	}
	if client.MutationCount("create_volume") != 1 {
		t.Fatalf("expected one create mutation, got %d", client.MutationCount("create_volume"))
	}

	volume, err := storageBackend.GetVolume(context.Background(), desired.ID)
	if err != nil {
		t.Fatalf("GetVolume() error = %v", err)
	}
	if volume.ID != desired.ID || volume.Path != desired.Path || volume.SizeBytes != desired.SizeBytes {
		t.Fatalf("unexpected volume after create: %+v", volume)
	}
}

func TestBuildEnsureVolumePlanNoop(t *testing.T) {
	client := rds.NewMockClient()
	storageBackend := New(client)
	client.AddVolume(&rds.VolumeInfo{
		Slot:          "pvc-test-volume",
		Type:          "file",
		FilePath:      "/storage-pool/metal-csi/pvc-test-volume.img",
		FileSizeBytes: 10 << 30,
		NVMETCPExport: true,
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume",
		Status:        "ready",
	})

	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), backend.CreateVolumeRequest{
		ID:          "pvc-test-volume",
		Path:        "/storage-pool/metal-csi/pvc-test-volume.img",
		SizeBytes:   10 << 30,
		NVMETCPPort: 4420,
		NVMETCPNQN:  "nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume",
	})
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionNoop {
		t.Fatalf("expected noop plan, got %s", plan.Action)
	}
	if plan.Actual == nil || plan.Actual.ID != "pvc-test-volume" {
		t.Fatalf("expected actual volume in noop plan, got %+v", plan.Actual)
	}
}

func TestGetVolumeMapsNotFound(t *testing.T) {
	storageBackend := New(rds.NewMockClient())

	_, err := storageBackend.GetVolume(context.Background(), "pvc-missing")
	if err == nil {
		t.Fatal("expected not found error")
	}
	if !backend.IsVolumeNotFound(err) {
		t.Fatalf("expected backend volume not found error, got %T %v", err, err)
	}
}

func TestSnapshotMapping(t *testing.T) {
	client := rds.NewMockClient()
	storageBackend := New(client)
	client.AddVolume(&rds.VolumeInfo{
		Slot:          "pvc-source",
		FilePath:      "/storage-pool/metal-csi/pvc-source.img",
		FileSizeBytes: 5 << 30,
		NVMETCPExport: true,
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2014-08.org.nvmexpress:uuid:pvc-source",
		Status:        "ready",
	})

	snapshot, err := storageBackend.CreateSnapshot(context.Background(), backend.CreateSnapshotRequest{
		ID:             "snap-pvc-source-at-test",
		SourceVolumeID: "pvc-source",
		BasePath:       "/storage-pool/metal-csi",
	})
	if err != nil {
		t.Fatalf("CreateSnapshot() error = %v", err)
	}
	if snapshot.ID != "snap-pvc-source-at-test" || snapshot.SourceVolumeID != "pvc-source" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}
