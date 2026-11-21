package driver

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/moby/sys/mountinfo"
	"golang.org/x/sys/unix"
	"k8s.io/klog/v2"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/mount"
)

// A filesystem volume's ext4 can stay mounted after kubelet has unpublished it everywhere: a bind
// mount of a file on it made outside kubelet. KubeVirt does this for a hot-plugged Filesystem
// volume: virt-handler bind-mounts <publish path>/disk.img into the virt-launcher pod's
// hotplug-disks directory, and qemu writes to it. If kubelet then unstages the volume (KubeVirt
// moved the volume to a new attachment pod that lists it without mounting it, so no pod on the
// node uses it), unmounting the staging path only detaches one mount of the filesystem, and the
// NVMe disconnect that follows takes the device from under qemu: the VM pauses with an I/O error.
// lsof on the device does not see this, since nothing has the device itself open and the plugin
// does not share the host's PID namespace. Mountinfo does: kubelet's directory is rshared into the
// plugin (Bidirectional), so mounts under it made in the host's namespace show here, with the
// device's major:minor.

// listMounts and deviceNumber are variables so tests can replace them.
var (
	listMounts   = mount.GetMountsWithTimeout
	deviceNumber = func(devicePath string) (int, int, error) {
		var st unix.Stat_t
		if err := unix.Stat(devicePath, &st); err != nil {
			return 0, 0, err
		}
		dev := uint64(st.Rdev) //nolint:unconvert // Rdev is uint64 on linux, int32 on darwin
		return int(unix.Major(dev)), int(unix.Minor(dev)), nil
	}
)

// mountsHoldingDevice sorts the mounts of the filesystem on major:minor, other than the staging
// path itself, into those that hold it (anything not kubelet's) and kubelet's publish paths for
// this volume (stale when unstage is called: kubelet unpublishes every pod first).
func mountsHoldingDevice(mounts []*mountinfo.Info, major, minor int, stagingPath, volumeID string) (held, stalePublish []string) {
	staging := filepath.Clean(stagingPath)
	publishSuffix := "/volumes/kubernetes.io~csi/" + volumeID + "/mount"
	for _, m := range mounts {
		if m.Major != major || m.Minor != minor {
			continue
		}
		point := filepath.Clean(m.Mountpoint)
		switch {
		case point == staging:
		case m.Root == "/" && strings.HasSuffix(point, publishSuffix):
			stalePublish = append(stalePublish, point)
		default:
			held = append(held, point+" (root "+m.Root+")")
		}
	}
	return held, stalePublish
}

// filesystemHeldElsewhere returns where the filesystem on devicePath is still mounted other than
// stagingPath and this volume's own stale publish paths (which it unmounts). An error means it
// could not tell.
func (ns *NodeServer) filesystemHeldElsewhere(ctx context.Context, devicePath, stagingPath, volumeID string) ([]string, error) {
	major, minor, err := deviceNumber(devicePath)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", devicePath, err)
	}
	mounts, err := listMounts(ctx)
	if err != nil {
		return nil, err
	}
	held, stale := mountsHoldingDevice(mounts, major, minor, stagingPath, volumeID)
	for _, p := range stale {
		if err := ns.mounter.Unmount(p); err != nil {
			klog.Warningf("Failed to unmount stale publish path %s of volume %s: %v", p, volumeID, err)
			held = append(held, p)
		} else {
			klog.Infof("Unmounted stale publish path %s of volume %s before unstaging", p, volumeID)
		}
	}
	return held, nil
}
