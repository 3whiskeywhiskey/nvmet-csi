package driver

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	storagebackend "git.srvlab.io/whiskey/rds-csi-driver/pkg/backend"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/rds"
)

func TestControllerBackendForParamsDefaultsRouterOS(t *testing.T) {
	mockRDS := rds.NewMockClient()
	mockRDS.SetAddress("10.42.68.1")
	driver := &Driver{rdsClient: mockRDS}

	got, selection, err := driver.controllerBackendForParams(map[string]string{})
	if err != nil {
		t.Fatalf("controllerBackendForParams() error = %v", err)
	}
	if got.Name() != storagebackend.TypeRouterOSRDS {
		t.Fatalf("expected RouterOS backend, got %s", got.Name())
	}
	if selection.Type != storagebackend.TypeRouterOSRDS || selection.Executor != storagebackend.ExecutorSSH {
		t.Fatalf("unexpected default selection: %+v", selection)
	}
}

func TestControllerBackendForParamsLinuxZOL(t *testing.T) {
	driver := &Driver{}
	params := map[string]string{
		"backend":       storagebackend.TypeLinuxZOLNVMET,
		"executor":      storagebackend.ExecutorLocal,
		"zfsPool":       "tank/nvmet",
		"targetAddress": "10.42.0.10",
	}

	got, selection, err := driver.controllerBackendForParams(params)
	if err != nil {
		t.Fatalf("controllerBackendForParams() error = %v", err)
	}
	if got.Name() != storagebackend.TypeLinuxZOLNVMET {
		t.Fatalf("expected Linux ZoL backend, got %s", got.Name())
	}
	if got.Address() != "10.42.0.10" {
		t.Fatalf("expected backend address from targetAddress, got %q", got.Address())
	}
	if selection.ZFSPool != "tank/nvmet" {
		t.Fatalf("expected zfsPool in selection, got %+v", selection)
	}
}

func TestControllerBackendForVolumeUsesPersistentVolumeAttributes(t *testing.T) {
	volumeID := "test-volume"
	k8s := fake.NewSimpleClientset(&corev1.PersistentVolume{
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       DriverName,
					VolumeHandle: volumeID,
					VolumeAttributes: map[string]string{
						"backend":       storagebackend.TypeLinuxZOLNVMET,
						"executor":      storagebackend.ExecutorSSH,
						"zfsPool":       "tank/nvmet",
						"targetAddress": "10.42.0.10",
						"sshTarget":     "storage@r740xd",
					},
				},
			},
		},
	})
	driver := &Driver{
		name:      DriverName,
		k8sClient: k8s,
		backendFactory: func(selection BackendSelection) (storagebackend.Backend, error) {
			return &testBackend{name: selection.Type, address: selection.TargetAddr}, nil
		},
	}

	got, selection, err := driver.controllerBackendForVolume(context.Background(), volumeID)
	if err != nil {
		t.Fatalf("controllerBackendForVolume() error = %v", err)
	}
	if got.Name() != storagebackend.TypeLinuxZOLNVMET {
		t.Fatalf("expected Linux backend from PV attributes, got %s", got.Name())
	}
	if selection.Executor != storagebackend.ExecutorSSH || selection.SSHTarget != "storage@r740xd" {
		t.Fatalf("unexpected PV-derived selection: %+v", selection)
	}
}

func TestCreateVolumeLinuxSelectionUsesResolvedBackend(t *testing.T) {
	volumeID := "test-volume"
	driver := &Driver{
		name: DriverName,
		backendFactory: func(selection BackendSelection) (storagebackend.Backend, error) {
			return &testBackend{
				name:    selection.Type,
				address: selection.TargetAddr,
				normalizeCreate: func(req storagebackend.CreateVolumeRequest) storagebackend.CreateVolumeRequest {
					req.Path = "/dev/zvol/" + selection.ZFSPool + "/" + req.ID
					return req
				},
			}, nil
		},
	}
	driver.addVolumeCapabilities()
	cs := NewControllerServer(driver)

	resp, err := cs.CreateVolume(context.Background(), &csi.CreateVolumeRequest{
		Name: volumeID,
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
				AccessType: &csi.VolumeCapability_Mount{Mount: &csi.VolumeCapability_MountVolume{}},
			},
		},
		CapacityRange: &csi.CapacityRange{RequiredBytes: minVolumeSizeBytes},
		Parameters: map[string]string{
			"backend":       storagebackend.TypeLinuxZOLNVMET,
			"executor":      storagebackend.ExecutorLocal,
			"zfsPool":       "tank/nvmet",
			"targetAddress": "10.42.0.10",
		},
	})
	if err != nil {
		t.Fatalf("CreateVolume() error = %v", err)
	}
	ctx := resp.GetVolume().GetVolumeContext()
	if ctx["backend"] != storagebackend.TypeLinuxZOLNVMET || ctx["executor"] != storagebackend.ExecutorLocal {
		t.Fatalf("expected Linux backend context, got %#v", ctx)
	}
	if ctx["zfsPool"] != "tank/nvmet" || ctx["targetAddress"] != "10.42.0.10" {
		t.Fatalf("expected Linux target context, got %#v", ctx)
	}
	if ctx["volumePath"] != "/dev/zvol/tank/nvmet/"+volumeID {
		t.Fatalf("expected normalized zvol volumePath, got %q", ctx["volumePath"])
	}
	if ctx["nvmeAddress"] != "10.42.0.10" {
		t.Fatalf("expected nvmeAddress from targetAddress, got %q", ctx["nvmeAddress"])
	}
}

func TestListVolumesAggregatesKnownBackends(t *testing.T) {
	localVolumeID := "pvc-11111111-1111-1111-1111-111111111111"
	sshVolumeID := "pvc-22222222-2222-2222-2222-222222222222"
	driver := &Driver{
		name: DriverName,
		k8sClient: fake.NewSimpleClientset(
			testPersistentVolume(localVolumeID, map[string]string{
				"backend":       storagebackend.TypeLinuxZOLNVMET,
				"executor":      storagebackend.ExecutorLocal,
				"zfsPool":       "tank/local",
				"targetAddress": "10.42.0.10",
			}),
			testPersistentVolume(sshVolumeID, map[string]string{
				"backend":       storagebackend.TypeLinuxZOLNVMET,
				"executor":      storagebackend.ExecutorSSH,
				"zfsPool":       "tank/remote",
				"targetAddress": "10.42.0.11",
				"sshTarget":     "storage@r740xd",
			}),
		),
		backendFactory: func(selection BackendSelection) (storagebackend.Backend, error) {
			return &testBackend{
				name:    selection.Type,
				address: selection.TargetAddr,
				listVolumes: []storagebackend.Volume{
					{ID: "vol-" + selection.TargetAddr, SizeBytes: minVolumeSizeBytes},
				},
			}, nil
		},
	}
	cs := NewControllerServer(driver)

	resp, err := cs.ListVolumes(context.Background(), &csi.ListVolumesRequest{})
	if err != nil {
		t.Fatalf("ListVolumes() error = %v", err)
	}
	if got := len(resp.GetEntries()); got != 2 {
		t.Fatalf("expected two volume entries across known backends, got %d", got)
	}
	if resp.GetEntries()[0].GetVolume().GetVolumeId() != "vol-10.42.0.10" ||
		resp.GetEntries()[1].GetVolume().GetVolumeId() != "vol-10.42.0.11" {
		t.Fatalf("unexpected aggregated entries: %#v", resp.GetEntries())
	}
}

func TestDeleteSnapshotResolvesBackendBeforeMutating(t *testing.T) {
	snapshotID := "snap-11111111-1111-1111-1111-111111111111-at-abcdef"
	targetBackend := &testBackend{
		name:    storagebackend.TypeLinuxZOLNVMET,
		address: "10.42.0.11",
		snapshots: map[string]storagebackend.Snapshot{
			snapshotID: {
				ID:             snapshotID,
				SourceVolumeID: "pvc-22222222-2222-2222-2222-222222222222",
				SizeBytes:      minVolumeSizeBytes,
				CreatedAt:      time.Now(),
			},
		},
	}
	otherBackend := &testBackend{
		name:      storagebackend.TypeLinuxZOLNVMET,
		address:   "10.42.0.10",
		snapshots: map[string]storagebackend.Snapshot{},
	}

	driver := &Driver{
		name: DriverName,
		k8sClient: fake.NewSimpleClientset(
			testPersistentVolume("pvc-11111111-1111-1111-1111-111111111111", map[string]string{
				"backend":       storagebackend.TypeLinuxZOLNVMET,
				"executor":      storagebackend.ExecutorLocal,
				"zfsPool":       "tank/local",
				"targetAddress": "10.42.0.10",
			}),
			testPersistentVolume("pvc-22222222-2222-2222-2222-222222222222", map[string]string{
				"backend":       storagebackend.TypeLinuxZOLNVMET,
				"executor":      storagebackend.ExecutorSSH,
				"zfsPool":       "tank/remote",
				"targetAddress": "10.42.0.11",
				"sshTarget":     "storage@r740xd",
			}),
		),
		backendFactory: func(selection BackendSelection) (storagebackend.Backend, error) {
			if selection.TargetAddr == "10.42.0.11" {
				return targetBackend, nil
			}
			return otherBackend, nil
		},
	}
	cs := NewControllerServer(driver)

	_, err := cs.DeleteSnapshot(context.Background(), &csi.DeleteSnapshotRequest{SnapshotId: snapshotID})
	if err != nil {
		t.Fatalf("DeleteSnapshot() error = %v", err)
	}
	if len(otherBackend.deletedSnapshots) != 0 {
		t.Fatalf("non-owner backend was mutated: %#v", otherBackend.deletedSnapshots)
	}
	if got := targetBackend.deletedSnapshots; len(got) != 1 || got[0] != snapshotID {
		t.Fatalf("owner backend deletion not recorded: %#v", got)
	}
}

type testBackend struct {
	name             string
	address          string
	normalizeCreate  func(storagebackend.CreateVolumeRequest) storagebackend.CreateVolumeRequest
	volumes          map[string]storagebackend.Volume
	listVolumes      []storagebackend.Volume
	snapshots        map[string]storagebackend.Snapshot
	deletedSnapshots []string
}

func testPersistentVolume(volumeID string, attributes map[string]string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-" + volumeID},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:           DriverName,
					VolumeHandle:     volumeID,
					VolumeAttributes: attributes,
				},
			},
		},
	}
}

func (b *testBackend) Name() string {
	return b.name
}

func (b *testBackend) Address() string {
	return b.address
}

func (b *testBackend) BuildEnsureVolumePlan(_ context.Context, desired storagebackend.CreateVolumeRequest) (*storagebackend.VolumePlan, error) {
	if b.normalizeCreate != nil {
		desired = b.normalizeCreate(desired)
	}
	return &storagebackend.VolumePlan{
		Action:   storagebackend.VolumeActionCreate,
		VolumeID: desired.ID,
		Reason:   "test create",
		Desired:  &desired,
	}, nil
}

func (b *testBackend) BuildDeleteVolumePlan(_ context.Context, volumeID string) (*storagebackend.VolumePlan, error) {
	return &storagebackend.VolumePlan{
		Action:   storagebackend.VolumeActionDelete,
		VolumeID: volumeID,
		Actual:   &storagebackend.Volume{ID: volumeID, Path: "/dev/test", SizeBytes: minVolumeSizeBytes},
	}, nil
}

func (b *testBackend) ApplyVolumePlan(_ context.Context, plan *storagebackend.VolumePlan, _ storagebackend.VolumeApplyOptions) (*storagebackend.VolumeApplyResult, error) {
	return &storagebackend.VolumeApplyResult{Action: plan.Action, VolumeID: plan.VolumeID, Applied: true}, nil
}

func (b *testBackend) GetVolume(_ context.Context, volumeID string) (*storagebackend.Volume, error) {
	if b.volumes != nil {
		if volume, ok := b.volumes[volumeID]; ok {
			return &volume, nil
		}
	}
	return &storagebackend.Volume{ID: volumeID, Path: "/dev/test", SizeBytes: minVolumeSizeBytes}, nil
}

func (b *testBackend) ListVolumes(context.Context) ([]storagebackend.Volume, error) {
	return b.listVolumes, nil
}

func (b *testBackend) ResizeVolume(context.Context, string, int64) error {
	return nil
}

func (b *testBackend) GetCapacity(context.Context, string) (*storagebackend.Capacity, error) {
	return &storagebackend.Capacity{TotalBytes: 100 << 30, FreeBytes: 80 << 30, UsedBytes: 20 << 30}, nil
}

func (b *testBackend) CreateSnapshot(context.Context, storagebackend.CreateSnapshotRequest) (*storagebackend.Snapshot, error) {
	return nil, fmt.Errorf("not implemented")
}

func (b *testBackend) DeleteSnapshot(_ context.Context, snapshotID string) error {
	b.deletedSnapshots = append(b.deletedSnapshots, snapshotID)
	if b.snapshots != nil {
		delete(b.snapshots, snapshotID)
	}
	return nil
}

func (b *testBackend) GetSnapshot(_ context.Context, snapshotID string) (*storagebackend.Snapshot, error) {
	if b.snapshots != nil {
		if snapshot, ok := b.snapshots[snapshotID]; ok {
			return &snapshot, nil
		}
	}
	return nil, &storagebackend.SnapshotNotFoundError{}
}

func (b *testBackend) ListSnapshots(context.Context) ([]storagebackend.Snapshot, error) {
	snapshots := make([]storagebackend.Snapshot, 0, len(b.snapshots))
	for _, snapshot := range b.snapshots {
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, nil
}

func (b *testBackend) RestoreSnapshot(context.Context, string, storagebackend.CreateVolumeRequest) error {
	return fmt.Errorf("not implemented")
}

var _ storagebackend.Backend = (*testBackend)(nil)

func TestBackendSelectionVolumeContext(t *testing.T) {
	selection := BackendSelection{
		Type:       storagebackend.TypeLinuxZOLNVMET,
		Executor:   storagebackend.ExecutorSSH,
		ZFSPool:    "tank/nvmet",
		TargetAddr: "10.42.0.10",
		SSHTarget:  "storage@r740xd",
	}
	ctx := selection.volumeContext()
	for _, key := range []string{"backend", "executor", "zfsPool", "targetAddress", "sshTarget"} {
		if ctx[key] == "" {
			t.Fatalf("expected %s in volume context: %#v", key, ctx)
		}
	}
}

func TestBackendSelectionFromPVRejectsInvalidAttributes(t *testing.T) {
	volumeID := "test-volume"
	driver := &Driver{
		name: DriverName,
		k8sClient: fake.NewSimpleClientset(&corev1.PersistentVolume{
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						Driver:       DriverName,
						VolumeHandle: volumeID,
						VolumeAttributes: map[string]string{
							"backend":  storagebackend.TypeLinuxZOLNVMET,
							"executor": storagebackend.ExecutorLocal,
						},
					},
				},
			},
		}),
	}

	_, _, err := driver.controllerBackendForVolume(context.Background(), volumeID)
	if err == nil {
		t.Fatal("expected invalid PV backend attributes to fail closed")
	}
	if got := err.Error(); got == "" || !containsAll(got, "persistent volume", "zfsPool") {
		t.Fatalf("expected PV/zfsPool error, got %v", err)
	}
}

func containsAll(s string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(s, needle) {
			return false
		}
	}
	return true
}
