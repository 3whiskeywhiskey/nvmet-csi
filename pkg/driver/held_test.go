package driver

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/moby/sys/mountinfo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/circuitbreaker"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/observability"
)

const (
	heldVolumeID = "pvc-9e82f911-0000-4000-8000-000000000000"
	heldStaging  = "/var/lib/kubelet/plugins/kubernetes.io/csi/nvmet.csi.whiskey.works/abc/globalmount"
	heldPublish  = "/var/lib/kubelet/pods/0cde722f/volumes/kubernetes.io~csi/" + heldVolumeID + "/mount"
	heldDiskImg  = "/var/lib/kubelet/pods/5e1f/volumes/kubernetes.io~empty-dir/hotplug-disks/pvc-9e82f911.img"
)

func TestMountsHoldingDevice(t *testing.T) {
	mounts := []*mountinfo.Info{
		{Major: 259, Minor: 4, Root: "/", Mountpoint: heldStaging},
		{Major: 259, Minor: 4, Root: "/", Mountpoint: heldPublish},
		{Major: 259, Minor: 4, Root: "/disk.img", Mountpoint: heldDiskImg},
		{Major: 259, Minor: 5, Root: "/", Mountpoint: "/var/lib/kubelet/pods/x/volumes/kubernetes.io~csi/pvc-other/mount"},
		{Major: 8, Minor: 1, Root: "/", Mountpoint: "/"},
	}
	held, stale := mountsHoldingDevice(mounts, 259, 4, heldStaging+"/", heldVolumeID)
	if want := []string{heldDiskImg + " (root /disk.img)"}; !reflect.DeepEqual(held, want) {
		t.Errorf("held = %v, want %v", held, want)
	}
	if want := []string{heldPublish}; !reflect.DeepEqual(stale, want) {
		t.Errorf("stale = %v, want %v", stale, want)
	}

	held, stale = mountsHoldingDevice(mounts[:1], 259, 4, heldStaging, heldVolumeID)
	if len(held) != 0 || len(stale) != 0 {
		t.Errorf("staging only: held %v, stale %v; want none", held, stale)
	}
}

// fakeMounts replaces listMounts and deviceNumber for one test.
func fakeMounts(t *testing.T, mounts []*mountinfo.Info, err error) {
	t.Helper()
	oldList, oldDev := listMounts, deviceNumber
	listMounts = func(context.Context) ([]*mountinfo.Info, error) { return mounts, err }
	deviceNumber = func(string) (int, int, error) { return 259, 4, nil }
	t.Cleanup(func() { listMounts, deviceNumber = oldList, oldDev })
}

func heldNodeServer(mounter *mockMounter, connector *mockNVMEConnector) *NodeServer {
	return &NodeServer{
		driver:         &Driver{name: "nvmet.csi.whiskey.works", version: "test", metrics: observability.NewMetrics()},
		mounter:        mounter,
		nvmeConn:       connector,
		nodeID:         "test-node",
		circuitBreaker: circuitbreaker.NewVolumeCircuitBreaker(),
	}
}

func TestNodeUnstageVolume_FilesystemHeldByDiskImage(t *testing.T) {
	fakeMounts(t, []*mountinfo.Info{
		{Major: 259, Minor: 4, Root: "/", Mountpoint: heldStaging},
		{Major: 259, Minor: 4, Root: "/disk.img", Mountpoint: heldDiskImg},
	}, nil)
	mounter := &mockMounter{isLikelyMounted: true}
	connector := &mockNVMEConnector{devicePath: "/dev/nvme4n1"}
	ns := heldNodeServer(mounter, connector)

	_, err := ns.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId: heldVolumeID, StagingTargetPath: heldStaging,
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("err = %v, want FailedPrecondition", err)
	}
	if mounter.unmountCalled {
		t.Error("nothing should be unmounted while the filesystem is held")
	}
	if connector.disconnectCalled {
		t.Error("NVMe must not be disconnected while the filesystem is held")
	}
}

func TestNodeUnstageVolume_FilesystemStalePublishCleared(t *testing.T) {
	fakeMounts(t, []*mountinfo.Info{
		{Major: 259, Minor: 4, Root: "/", Mountpoint: heldStaging},
		{Major: 259, Minor: 4, Root: "/", Mountpoint: heldPublish},
	}, nil)
	mounter := &mockMounter{isLikelyMounted: true}
	connector := &mockNVMEConnector{devicePath: "/dev/nvme4n1"}
	ns := heldNodeServer(mounter, connector)

	if _, err := ns.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId: heldVolumeID, StagingTargetPath: heldStaging,
	}); err != nil {
		t.Fatalf("NodeUnstageVolume: %v", err)
	}
	if !connector.disconnectCalled {
		t.Error("a stale publish path of the volume itself should not keep it connected")
	}
}

func TestNodeUnstageVolume_FilesystemMountsUnreadable(t *testing.T) {
	fakeMounts(t, nil, errors.New("procmounts parsing timed out"))
	mounter := &mockMounter{isLikelyMounted: true}
	connector := &mockNVMEConnector{devicePath: "/dev/nvme4n1"}
	ns := heldNodeServer(mounter, connector)

	if _, err := ns.NodeUnstageVolume(context.Background(), &csi.NodeUnstageVolumeRequest{
		VolumeId: heldVolumeID, StagingTargetPath: heldStaging,
	}); err != nil {
		t.Fatalf("NodeUnstageVolume: %v (an unreadable mount table proceeds, as before)", err)
	}
	if !connector.disconnectCalled {
		t.Error("NVMe disconnect should be called")
	}
}
