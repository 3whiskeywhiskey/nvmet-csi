package e2e

import (
	"fmt"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	. "github.com/onsi/gomega"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
)

// Constants for test configuration
const (
	GiB                = 1024 * 1024 * 1024
	MiB                = 1024 * 1024
	defaultTimeout     = 2 * time.Minute
	pollInterval       = 200 * time.Millisecond
	testVolumeBasePath = "/storage-pool/metal-csi"
)

var testPVCVolumeNames = map[string]struct{}{}

// testVolumeName creates a unique volume name for the current test
// by prepending the test run ID to ensure isolation between test runs
func testVolumeName(name string) string {
	return fmt.Sprintf("%s-%s", testRunID, name)
}

// testPVCVolumeName creates a unique volume name that matches production PVC
// volume IDs, which are the only slots returned by RDS ListVolumes.
func testPVCVolumeName(name string) string {
	volumeName := utils.VolumeNameToID(testVolumeName(name))
	testPVCVolumeNames[volumeName] = struct{}{}
	return volumeName
}

func isTestVolumeName(volumeName string) bool {
	if strings.HasPrefix(volumeName, testRunID) {
		return true
	}
	_, ok := testPVCVolumeNames[volumeName]
	return ok
}

// mountVolumeCapability returns a mount volume capability with SINGLE_NODE_WRITER access mode
func mountVolumeCapability(fsType string) *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
		AccessType: &csi.VolumeCapability_Mount{
			Mount: &csi.VolumeCapability_MountVolume{
				FsType: fsType,
			},
		},
	}
}

// blockVolumeCapability returns a block volume capability with SINGLE_NODE_WRITER access mode
func blockVolumeCapability() *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
		AccessType: &csi.VolumeCapability_Block{
			Block: &csi.VolumeCapability_BlockVolume{},
		},
	}
}

// waitForVolumeOnMockRDS waits for a volume to appear on the mock RDS server
// and be exported via NVMe/TCP
func waitForVolumeOnMockRDS(volumeID string) {
	Eventually(func() bool {
		vol, exists := mockRDS.GetVolume(volumeID)
		return exists && vol.Exported
	}, defaultTimeout, pollInterval).Should(BeTrue(),
		"Volume %s should exist and be exported on mock RDS", volumeID)
}

// waitForVolumeDeletedFromMockRDS waits for a volume to be deleted from the mock RDS server
func waitForVolumeDeletedFromMockRDS(volumeID string) {
	Eventually(func() bool {
		_, exists := mockRDS.GetVolume(volumeID)
		return !exists
	}, defaultTimeout, pollInterval).Should(BeTrue(),
		"Volume %s should be deleted from mock RDS", volumeID)
}
