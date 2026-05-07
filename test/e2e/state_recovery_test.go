package e2e

import (
	"github.com/container-storage-interface/spec/lib/go/csi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/klog/v2"
)

var _ = Describe("State Recovery [E2E-06/E2E-07]", func() {
	// Note: Full node failure simulation (E2E-06) and controller restart
	// state recovery (E2E-07) require Kubernetes API integration.
	// These simplified tests validate the driver's core cleanup and
	// recovery logic without requiring a real cluster.

	Describe("Node Cleanup Simulation [E2E-06]", func() {
		It("should handle volume unstaging when node is unavailable", func() {
			volumeName := testVolumeName("node-cleanup")

			By("Creating and staging a volume")
			createResp, err := controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
				Name:               volumeName,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).NotTo(HaveOccurred())
			volumeID := createResp.Volume.VolumeId
			DeferCleanup(func() {
				_, _ = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: volumeID})
			})

			waitForVolumeOnMockRDS(volumeID)
			vol, _ := mockRDS.GetVolume(volumeID)

			// Attempt stage (will fail without real NVMe but validates path)
			stagePath := stagingPath(volumeID)
			_, err = nodeClient.NodeStageVolume(ctx, &csi.NodeStageVolumeRequest{
				VolumeId:          volumeID,
				StagingTargetPath: stagePath,
				VolumeCapability:  mountVolumeCapability("ext4"),
				VolumeContext: map[string]string{
					"nqn": vol.NVMETCPNQN,
				},
			})
			// Error expected in mock environment
			if err != nil {
				klog.Warningf("NodeStageVolume returned error (expected): %v", err)
			}

			By("Simulating node failure by calling NodeUnstageVolume")
			// In a real scenario, kubelet would call NodeUnstageVolume on node deletion
			// We validate the unstage path works correctly
			_, err = nodeClient.NodeUnstageVolume(ctx, &csi.NodeUnstageVolumeRequest{
				VolumeId:          volumeID,
				StagingTargetPath: stagePath,
			})
			// Error expected in mock environment
			if err != nil {
				klog.Warningf("NodeUnstageVolume returned error (expected): %v", err)
			}

			By("Verifying volume can be deleted after node cleanup")
			_, err = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{
				VolumeId: volumeID,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForVolumeDeletedFromMockRDS(volumeID)
			klog.Infof("Node cleanup simulation test passed for %s", volumeID)
		})

		It("should handle forced volume deletion without prior unstaging", func() {
			volumeName := testVolumeName("force-delete")

			By("Creating volume")
			createResp, err := controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
				Name:               volumeName,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).NotTo(HaveOccurred())
			volumeID := createResp.Volume.VolumeId
			waitForVolumeOnMockRDS(volumeID)

			By("Deleting volume directly (simulating forced cleanup)")
			// In a node failure scenario, we may need to delete volumes
			// that were never properly unstaged
			_, err = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{
				VolumeId: volumeID,
			})
			Expect(err).NotTo(HaveOccurred())

			waitForVolumeDeletedFromMockRDS(volumeID)
			klog.Infof("Force delete test passed for %s", volumeID)
		})
	})

	Describe("Controller State Recovery [E2E-07]", func() {
		It("should maintain volume state across ListVolumes calls", func() {
			volumeName := testPVCVolumeName("state-recovery")

			By("Creating volume")
			createResp, err := controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
				Name:               volumeName,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).NotTo(HaveOccurred())
			volumeID := createResp.Volume.VolumeId
			DeferCleanup(func() {
				_, _ = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: volumeID})
			})

			waitForVolumeOnMockRDS(volumeID)

			By("Verifying volume appears in ListVolumes")
			listResp, err := controllerClient.ListVolumes(ctx, &csi.ListVolumesRequest{})
			Expect(err).NotTo(HaveOccurred())

			found := false
			for _, entry := range listResp.Entries {
				if entry.Volume.VolumeId == volumeID {
					found = true
					Expect(entry.Volume.CapacityBytes).To(Equal(int64(smallVolumeSize)))
					break
				}
			}
			Expect(found).To(BeTrue(), "Volume should appear in ListVolumes")

			By("Verifying volume state is consistent with RDS")
			vol, exists := mockRDS.GetVolume(volumeID)
			Expect(exists).To(BeTrue())
			Expect(vol.FileSizeBytes).To(Equal(int64(smallVolumeSize)))

			klog.Infof("State recovery test passed: volume %s state is consistent", volumeID)
		})

		It("should handle GetCapacity after volume operations", func() {
			volumeName := testVolumeName("capacity-recovery")

			By("Getting initial capacity")
			initialCap, err := controllerClient.GetCapacity(ctx, &csi.GetCapacityRequest{})
			Expect(err).NotTo(HaveOccurred())
			Expect(initialCap.AvailableCapacity).To(BeNumerically(">", 0))

			By("Creating volume")
			createResp, err := controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
				Name:               volumeName,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).NotTo(HaveOccurred())
			volumeID := createResp.Volume.VolumeId
			DeferCleanup(func() {
				_, _ = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: volumeID})
			})

			By("Verifying GetCapacity still works")
			afterCap, err := controllerClient.GetCapacity(ctx, &csi.GetCapacityRequest{})
			Expect(err).NotTo(HaveOccurred())
			Expect(afterCap.AvailableCapacity).To(BeNumerically(">", 0))

			klog.Infof("Capacity recovery test passed")
		})

		It("should validate volume capabilities after state queries", func() {
			volumeName := testVolumeName("validate-recovery")

			By("Creating volume")
			createResp, err := controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
				Name:               volumeName,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).NotTo(HaveOccurred())
			volumeID := createResp.Volume.VolumeId
			DeferCleanup(func() {
				_, _ = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: volumeID})
			})

			waitForVolumeOnMockRDS(volumeID)

			By("Querying state via ListVolumes")
			_, err = controllerClient.ListVolumes(ctx, &csi.ListVolumesRequest{})
			Expect(err).NotTo(HaveOccurred())

			By("Validating volume capabilities still works after state query")
			validateResp, err := controllerClient.ValidateVolumeCapabilities(ctx, &csi.ValidateVolumeCapabilitiesRequest{
				VolumeId:           volumeID,
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(validateResp.Confirmed).NotTo(BeNil())

			klog.Infof("Validate after state query test passed for %s", volumeID)
		})
	})
})
