package e2e

import (
	"github.com/container-storage-interface/spec/lib/go/csi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/nvmet-csi/test/mock"
)

var _ = Describe("Resilience Regression", func() {
	// RESIL-01: SSH error recovery
	// Validates that after SSH command failures are injected and then cleared,
	// controller volume operations succeed without manual intervention.
	Describe("RESIL-01: SSH Error Recovery", func() {
		It("should recover volume operations after SSH command failures are cleared", func() {
			volumeName := testVolumeName("resil-01-baseline")

			By("Creating a baseline volume to confirm normal operation")
			createResp, err := controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
				Name:               volumeName,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).NotTo(HaveOccurred(), "Baseline CreateVolume should succeed")
			baselineVolumeID := createResp.Volume.VolumeId
			klog.Infof("RESIL-01: Baseline volume created: %s", baselineVolumeID)

			DeferCleanup(func() {
				// Always reset error mode first to ensure cleanup succeeds
				mockRDS.SetErrorMode(mock.ErrorModeNone)
				_, _ = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: baselineVolumeID})
			})

			waitForVolumeOnMockRDS(baselineVolumeID)

			By("Injecting SSH command failure errors via SetErrorMode")
			mockRDS.SetErrorMode(mock.ErrorModeCommandFail)
			klog.Infof("RESIL-01: Error mode set to CommandFail")

			DeferCleanup(func() {
				// Reset error mode so other tests are not affected
				mockRDS.SetErrorMode(mock.ErrorModeNone)
				mockRDS.ResetErrorInjector()
			})

			By("Verifying that CreateVolume fails while errors are injected")
			failVolumeName := testVolumeName("resil-01-fail")
			_, err = controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
				Name:               failVolumeName,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).To(HaveOccurred(), "CreateVolume should fail while errors are injected")
			klog.Infof("RESIL-01: CreateVolume correctly failed during error injection: %v", err)

			By("Clearing error injection to simulate SSH recovery")
			mockRDS.SetErrorMode(mock.ErrorModeNone)
			mockRDS.ResetErrorInjector()
			klog.Infof("RESIL-01: Error mode cleared — simulating SSH recovery")

			By("Verifying that CreateVolume succeeds immediately after recovery")
			recoveryVolumeName := testVolumeName("resil-01-recovery")
			recoveryResp, err := controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
				Name:               recoveryVolumeName,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).NotTo(HaveOccurred(), "CreateVolume should succeed after error recovery")
			recoveryVolumeID := recoveryResp.Volume.VolumeId
			klog.Infof("RESIL-01: Recovery volume created successfully: %s", recoveryVolumeID)

			DeferCleanup(func() {
				_, _ = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: recoveryVolumeID})
			})

			By("Verifying recovery volume exists on mock RDS")
			waitForVolumeOnMockRDS(recoveryVolumeID)

			By("Deleting recovery volume to confirm full lifecycle recovery")
			_, err = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: recoveryVolumeID})
			Expect(err).NotTo(HaveOccurred(), "DeleteVolume should succeed after recovery")
			waitForVolumeDeletedFromMockRDS(recoveryVolumeID)

			klog.Infof("RESIL-01: SSH error recovery test passed — operations resume without restart")
		})
	})

	// RESIL-02: RDS unavailability simulation (error injection approach)
	// Validates that after RDS is simulated as unavailable (all commands fail),
	// controller operations resume when error injection is cleared.
	// CRITICAL: Uses SetErrorMode only — NOT Stop()/Start() (restart is not supported).
	Describe("RESIL-02: RDS Unavailability Recovery (Error Injection)", func() {
		It("should resume volume operations after RDS simulated unavailability is cleared", func() {
			volumeName := testVolumeName("resil-02-baseline")

			By("Creating a baseline volume to confirm normal operation")
			createResp, err := controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
				Name:               volumeName,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).NotTo(HaveOccurred(), "Baseline CreateVolume should succeed")
			baselineVolumeID := createResp.Volume.VolumeId
			klog.Infof("RESIL-02: Baseline volume created: %s", baselineVolumeID)

			DeferCleanup(func() {
				// Always reset error mode first to ensure cleanup succeeds
				mockRDS.SetErrorMode(mock.ErrorModeNone)
				_, _ = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: baselineVolumeID})
			})

			waitForVolumeOnMockRDS(baselineVolumeID)

			By("Simulating RDS unavailability by injecting command failures for all operations")
			mockRDS.SetErrorMode(mock.ErrorModeCommandFail)
			klog.Infof("RESIL-02: RDS simulated as unavailable via ErrorModeCommandFail")

			DeferCleanup(func() {
				mockRDS.SetErrorMode(mock.ErrorModeNone)
				mockRDS.ResetErrorInjector()
			})

			By("Verifying CreateVolume fails while RDS is simulated as down")
			downVolumeName := testVolumeName("resil-02-down")
			_, err = controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
				Name:               downVolumeName,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).To(HaveOccurred(), "CreateVolume should fail while RDS is simulated as unavailable")
			klog.Infof("RESIL-02: CreateVolume correctly failed during simulated RDS unavailability: %v", err)

			By("Verifying GetCapacity fails while RDS is simulated as down")
			// Note: GetCapacity queries mount-point which doesn't go through error injection
			// in the current mock (mount-point handler has no error injection hook).
			// We validate at least one failing operation path — CreateVolume above is sufficient.
			// GetCapacity uses a static response from formatMountPointCapacity, so we skip this assertion.
			klog.Infof("RESIL-02: GetCapacity uses static mock response (no error injection path)")

			By("Simulating RDS recovery by clearing error injection")
			mockRDS.SetErrorMode(mock.ErrorModeNone)
			mockRDS.ResetErrorInjector()
			klog.Infof("RESIL-02: RDS simulated as recovered — error injection cleared")

			By("Verifying GetCapacity succeeds after recovery")
			capResp, err := controllerClient.GetCapacity(ctx, &csi.GetCapacityRequest{})
			Expect(err).NotTo(HaveOccurred(), "GetCapacity should succeed after RDS recovery")
			Expect(capResp.AvailableCapacity).To(BeNumerically(">", 0),
				"Available capacity should be positive after recovery")
			klog.Infof("RESIL-02: GetCapacity succeeded after recovery, available: %d bytes", capResp.AvailableCapacity)

			By("Verifying CreateVolume works after RDS recovery")
			recoveryVolumeName := testVolumeName("resil-02-recovery")
			recoveryResp, err := controllerClient.CreateVolume(ctx, &csi.CreateVolumeRequest{
				Name:               recoveryVolumeName,
				CapacityRange:      &csi.CapacityRange{RequiredBytes: smallVolumeSize},
				VolumeCapabilities: []*csi.VolumeCapability{mountVolumeCapability("ext4")},
			})
			Expect(err).NotTo(HaveOccurred(), "CreateVolume should succeed after RDS recovery")
			recoveryVolumeID := recoveryResp.Volume.VolumeId
			klog.Infof("RESIL-02: Recovery volume created successfully: %s", recoveryVolumeID)

			DeferCleanup(func() {
				_, _ = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: recoveryVolumeID})
			})

			By("Verifying recovery volume exists on mock RDS")
			waitForVolumeOnMockRDS(recoveryVolumeID)

			By("Deleting all test volumes to confirm full lifecycle after recovery")
			_, err = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: recoveryVolumeID})
			Expect(err).NotTo(HaveOccurred(), "DeleteVolume should succeed after recovery")
			waitForVolumeDeletedFromMockRDS(recoveryVolumeID)

			_, err = controllerClient.DeleteVolume(ctx, &csi.DeleteVolumeRequest{VolumeId: baselineVolumeID})
			Expect(err).NotTo(HaveOccurred(), "Baseline volume deletion should succeed after recovery")
			waitForVolumeDeletedFromMockRDS(baselineVolumeID)

			klog.Infof("RESIL-02: RDS unavailability recovery test passed — operations resume after error clearance")
		})
	})
})
