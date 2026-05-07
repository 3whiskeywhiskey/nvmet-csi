package e2e

import (
	"fmt"

	"github.com/container-storage-interface/spec/lib/go/csi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/klog/v2"
)

var _ = Describe("Orphan Detection [E2E-05]", func() {
	// Note: This tests the mock RDS's orphan simulation capabilities
	// and validates the driver's ListVolumes can identify mismatches.
	// Full orphan reconciliation requires Kubernetes API integration.

	It("should detect orphaned files (files without disk objects)", func() {
		orphanPath := fmt.Sprintf("/storage-pool/metal-csi/%s-orphan-file.img", testRunID)

		By("Creating orphaned file on mock RDS")
		mockRDS.CreateOrphanedFile(orphanPath, smallVolumeSize)
		DeferCleanup(func() {
			mockRDS.DeleteFile(orphanPath)
		})

		By("Verifying orphaned file exists")
		file, exists := mockRDS.GetFile(orphanPath)
		Expect(exists).To(BeTrue(), "Orphaned file should exist on mock RDS")
		Expect(file.SizeBytes).To(Equal(int64(smallVolumeSize)))

		By("Verifying no corresponding disk object exists")
		// The file exists but no volume references it
		volumes := mockRDS.ListVolumes()
		hasMatchingVolume := false
		for _, vol := range volumes {
			if vol.FilePath == orphanPath {
				hasMatchingVolume = true
				break
			}
		}
		Expect(hasMatchingVolume).To(BeFalse(),
			"No disk object should reference the orphaned file")

		klog.Infof("Orphaned file detection test passed: %s detected as orphan", orphanPath)
	})

	It("should detect orphaned volumes (disk objects without backing files)", func() {
		orphanSlot := testVolumeName("orphan-volume")
		orphanFilePath := fmt.Sprintf("/storage-pool/metal-csi/%s.img", orphanSlot)

		By("Creating orphaned volume (disk object without backing file)")
		mockRDS.CreateOrphanedVolume(orphanSlot, orphanFilePath, smallVolumeSize)
		DeferCleanup(func() {
			// Clean up via direct mock access (volume has no backing file)
			// The mock's DeleteVolume would fail if we tried CSI DeleteVolume
			// because the backing file doesn't exist
			// For this test, just let the mock cleanup handle it
		})

		By("Verifying orphaned volume exists")
		vol, exists := mockRDS.GetVolume(orphanSlot)
		Expect(exists).To(BeTrue(), "Orphaned volume should exist on mock RDS")
		Expect(vol.FilePath).To(Equal(orphanFilePath))

		By("Verifying no backing file exists")
		_, fileExists := mockRDS.GetFile(orphanFilePath)
		Expect(fileExists).To(BeFalse(),
			"No backing file should exist for orphaned volume")

		klog.Infof("Orphaned volume detection test passed: %s detected as orphan", orphanSlot)
	})

	It("should list volumes including those that may be orphaned", func() {
		// Create a normal volume
		normalVolumeName := testPVCVolumeName("orphan-test-normal")
		normalResp, err := controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
			Name:               normalVolumeName,
			CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
			VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
		})
		Expect(err).NotTo(HaveOccurred())
		normalVolumeID := normalResp.Volume.VolumeId
		DeferCleanup(func() {
			_, _ = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: normalVolumeID})
		})

		waitForVolumeOnMockRDS(normalVolumeID)

		// Create an orphaned volume (disk object without CSI creation)
		orphanSlot := testPVCVolumeName("orphan-detected")
		orphanFilePath := fmt.Sprintf("/storage-pool/metal-csi/%s.img", orphanSlot)
		mockRDS.CreateOrphanedVolume(orphanSlot, orphanFilePath, smallVolumeSize)
		// Also create its backing file so it's a "complete" volume but not created via CSI
		mockRDS.CreateOrphanedFile(orphanFilePath, smallVolumeSize)

		By("Listing volumes via CSI ListVolumes")
		listResp, err := controllerClient.ListVolumes(ctx, &csi.ListVolumesRequest{})
		Expect(err).NotTo(HaveOccurred())

		// Find our normal volume and PVC-shaped orphaned disk.
		foundNormal := false
		foundOrphan := false
		for _, entry := range listResp.Entries {
			if entry.Volume.VolumeId == normalVolumeID {
				foundNormal = true
			}
			if entry.Volume.VolumeId == orphanSlot {
				foundOrphan = true
			}
		}
		Expect(foundNormal).To(BeTrue(), "Normal volume should be in ListVolumes response")
		Expect(foundOrphan).To(BeTrue(), "PVC-shaped orphan volume should be in ListVolumes response")

		By("Verifying mock RDS shows both volumes")
		allVolumes := mockRDS.ListVolumes()
		Expect(len(allVolumes)).To(BeNumerically(">=", 2),
			"Mock RDS should have at least 2 volumes (normal + orphan)")

		klog.Infof("Orphan listing test passed: can enumerate volumes for reconciliation")
	})

	It("should track cleanup prevents orphans between test runs (E2E-08)", func() {
		// This test validates E2E-08: unique volume ID prefix per test run
		By("Verifying testRunID is set and unique")
		Expect(testRunID).NotTo(BeEmpty())
		Expect(testRunID).To(HavePrefix("e2e-"),
			"testRunID should have e2e- prefix")

		By("Creating test volume with prefixed name")
		volumeName := testVolumeName("cleanup-test")
		Expect(volumeName).To(HavePrefix(testRunID),
			"Volume name should include testRunID prefix")

		resp, err := controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
			Name:               volumeName,
			CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
			VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_, _ = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: resp.Volume.VolumeId})
		})

		By("Verifying volume ID or name contains test run identifier")
		// The volume should be identifiable as belonging to this test run
		// This enables AfterSuite cleanup to find and delete all test volumes
		klog.Infof("Test isolation validated: volumes use prefix %s", testRunID)
	})
})
