package e2e

import (
	"fmt"
)

// Test volume sizes for different scenarios
const (
	smallVolumeSize  = 1 * GiB  // 1 GiB for quick tests
	mediumVolumeSize = 5 * GiB  // 5 GiB for block volume tests
	largeVolumeSize  = 10 * GiB // 10 GiB for expansion tests
)

// stagingPath returns a unique staging path for a volume
// This is where NodeStageVolume will mount the volume
func stagingPath(volumeID string) string {
	return fmt.Sprintf("/tmp/csi-e2e-staging/%s", volumeID)
}

// publishPath returns a unique publish path for a volume
// This is where NodePublishVolume will bind-mount the volume
func publishPath(volumeID string) string {
	return fmt.Sprintf("/tmp/csi-e2e-publish/%s", volumeID)
}
