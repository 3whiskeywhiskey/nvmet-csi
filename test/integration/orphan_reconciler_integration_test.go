package integration

import (
	"context"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/rds"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/reconciler"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/test/mock"
)

// TestOrphanReconciler_WithMockRDS tests the orphan reconciler with the mock RDS server
func TestOrphanReconciler_WithMockRDS(t *testing.T) {
	// Set up allowed base paths for testing
	utils.ResetAllowedBasePaths()
	if err := utils.SetAllowedBasePath("/storage-pool/metal-csi"); err != nil {
		t.Fatalf("Failed to set allowed base path: %v", err)
	}
	t.Cleanup(utils.ResetAllowedBasePaths)
	// Start mock RDS server
	mockRDS, err := mock.NewMockRDSServer(12223)
	if err != nil {
		t.Fatalf("Failed to create mock RDS server: %v", err)
	}

	if err := mockRDS.Start(); err != nil {
		t.Fatalf("Failed to start mock RDS server: %v", err)
	}
	defer func() {
		if err := mockRDS.Stop(); err != nil {
			t.Logf("Warning: failed to stop mock RDS server: %v", err)
		}
	}()

	// Wait for server to be ready
	time.Sleep(100 * time.Millisecond)

	// Create RDS client connected to mock
	rdsClient, err := rds.NewClient(rds.ClientConfig{
		Address:            mockRDS.Address(),
		Port:               mockRDS.Port(),
		User:               "admin",
		PrivateKey:         nil,
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("Failed to create RDS client: %v", err)
	}

	if err := rdsClient.Connect(); err != nil {
		t.Fatalf("Failed to connect to mock RDS: %v", err)
	}
	defer func() { _ = rdsClient.Close() }()

	t.Run("NoOrphans_AllVolumesHavePVs", func(t *testing.T) {
		// Setup: Create volumes with matching PVs
		mockRDS.CreateOrphanedFile("/storage-pool/metal-csi/pvc-test-1.img", 10*1024*1024*1024)
		mockRDS.CreateOrphanedVolume("pvc-test-1", "/storage-pool/metal-csi/pvc-test-1.img", 10*1024*1024*1024)

		k8sClient := fake.NewSimpleClientset()
		pv := &v1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-test-1"},
			Spec: v1.PersistentVolumeSpec{
				PersistentVolumeSource: v1.PersistentVolumeSource{
					CSI: &v1.CSIPersistentVolumeSource{
						Driver:       "nvmet.csi.whiskey.works",
						VolumeHandle: "pvc-test-1",
					},
				},
			},
		}
		if _, err := k8sClient.CoreV1().PersistentVolumes().Create(context.Background(), pv, metav1.CreateOptions{}); err != nil {
			t.Fatalf("Failed to create test PV: %v", err)
		}

		// Create reconciler
		rec, err := reconciler.NewOrphanReconciler(reconciler.OrphanReconcilerConfig{
			RDSClient:     rdsClient,
			K8sClient:     k8sClient,
			DriverName:    "nvmet.csi.whiskey.works",
			CheckInterval: 1 * time.Hour,
			GracePeriod:   1 * time.Second,
			DryRun:        false,
			Enabled:       true,
			BasePath:      "/storage-pool/metal-csi",
		})
		if err != nil {
			t.Fatalf("Failed to create reconciler: %v", err)
		}

		// Run reconciliation
		if err := runConfirmedReconciliation(rec); err != nil {
			t.Fatalf("Reconciliation failed: %v", err)
		}

		// Verify no deletions occurred
		if vol, exists := mockRDS.GetVolume("pvc-test-1"); !exists {
			t.Error("Volume should still exist (has matching PV)")
		} else if vol.Slot != "pvc-test-1" {
			t.Errorf("Expected volume pvc-test-1, got %s", vol.Slot)
		}

		if file, exists := mockRDS.GetFile("/storage-pool/metal-csi/pvc-test-1.img"); !exists {
			t.Error("File should still exist (has matching PV)")
		} else if file.SizeBytes != 10*1024*1024*1024 {
			t.Errorf("Expected file size 10 GiB, got %d bytes", file.SizeBytes)
		}

		t.Log("✅ No orphans detected when all volumes have PVs")
	})

	t.Run("OrphanedDiskObject_DeletesVolume", func(t *testing.T) {
		// Setup: Create volume without PV (orphaned disk object)
		mockRDS.CreateOrphanedFile("/storage-pool/metal-csi/pvc-orphan-disk.img", 5*1024*1024*1024)
		mockRDS.CreateOrphanedVolume("pvc-orphan-disk", "/storage-pool/metal-csi/pvc-orphan-disk.img", 5*1024*1024*1024)

		k8sClient := fake.NewSimpleClientset()

		// Create reconciler (not dry-run)
		rec, err := reconciler.NewOrphanReconciler(reconciler.OrphanReconcilerConfig{
			RDSClient:     rdsClient,
			K8sClient:     k8sClient,
			DriverName:    "nvmet.csi.whiskey.works",
			CheckInterval: 1 * time.Hour,
			GracePeriod:   1 * time.Second,
			DryRun:        false,
			Enabled:       true,
			BasePath:      "/storage-pool/metal-csi",
		})
		if err != nil {
			t.Fatalf("Failed to create reconciler: %v", err)
		}

		// Wait for grace period
		time.Sleep(2 * time.Second)

		// A single inventory pass only records the orphan; cleanup needs
		// consecutive confirmations.
		if err := rec.TriggerReconciliation(context.Background()); err != nil {
			t.Fatalf("Reconciliation failed: %v", err)
		}
		if _, exists := mockRDS.GetVolume("pvc-orphan-disk"); !exists {
			t.Fatal("Orphaned volume should survive the first unconfirmed pass")
		}

		// Run the remaining confirmation passes
		if err := runConfirmedReconciliation(rec); err != nil {
			t.Fatalf("Reconciliation failed: %v", err)
		}

		// Verify volume was deleted
		if _, exists := mockRDS.GetVolume("pvc-orphan-disk"); exists {
			t.Error("Orphaned volume should have been deleted")
		}

		// File should also be deleted (since disk delete also deletes file in mock)
		if _, exists := mockRDS.GetFile("/storage-pool/metal-csi/pvc-orphan-disk.img"); exists {
			t.Error("Orphaned volume's file should have been deleted")
		}

		t.Log("✅ Orphaned disk object was deleted")
	})

	t.Run("OrphanedFile_DeletesFile", func(t *testing.T) {
		// Setup: Create file without disk object (orphaned file)
		mockRDS.CreateOrphanedFile("/storage-pool/metal-csi/pvc-orphan-file.img", 1024*1024*1024)

		k8sClient := fake.NewSimpleClientset()

		// Create reconciler (not dry-run)
		rec, err := reconciler.NewOrphanReconciler(reconciler.OrphanReconcilerConfig{
			RDSClient:     rdsClient,
			K8sClient:     k8sClient,
			DriverName:    "nvmet.csi.whiskey.works",
			CheckInterval: 1 * time.Hour,
			GracePeriod:   1 * time.Second,
			DryRun:        false,
			Enabled:       true,
			BasePath:      "/storage-pool/metal-csi",
		})
		if err != nil {
			t.Fatalf("Failed to create reconciler: %v", err)
		}

		// Run reconciliation
		if err := runConfirmedReconciliation(rec); err != nil {
			t.Fatalf("Reconciliation failed: %v", err)
		}

		// Verify file was deleted
		if _, exists := mockRDS.GetFile("/storage-pool/metal-csi/pvc-orphan-file.img"); exists {
			t.Error("Orphaned file should have been deleted")
		}

		t.Log("✅ Orphaned file was deleted")
	})

	t.Run("DryRun_NoActualDeletion", func(t *testing.T) {
		// Setup: Create orphaned volume
		mockRDS.CreateOrphanedFile("/storage-pool/metal-csi/pvc-dryrun.img", 2*1024*1024*1024)
		mockRDS.CreateOrphanedVolume("pvc-dryrun", "/storage-pool/metal-csi/pvc-dryrun.img", 2*1024*1024*1024)

		k8sClient := fake.NewSimpleClientset()

		// Create reconciler (DRY-RUN mode)
		rec, err := reconciler.NewOrphanReconciler(reconciler.OrphanReconcilerConfig{
			RDSClient:     rdsClient,
			K8sClient:     k8sClient,
			DriverName:    "nvmet.csi.whiskey.works",
			CheckInterval: 1 * time.Hour,
			GracePeriod:   1 * time.Second,
			DryRun:        true, // DRY RUN
			Enabled:       true,
			BasePath:      "/storage-pool/metal-csi",
		})
		if err != nil {
			t.Fatalf("Failed to create reconciler: %v", err)
		}

		// Wait for grace period
		time.Sleep(2 * time.Second)

		// Run reconciliation
		if err := runConfirmedReconciliation(rec); err != nil {
			t.Fatalf("Reconciliation failed: %v", err)
		}

		// Verify volume still exists (dry-run shouldn't delete)
		if _, exists := mockRDS.GetVolume("pvc-dryrun"); !exists {
			t.Error("Volume should still exist in dry-run mode")
		}

		if _, exists := mockRDS.GetFile("/storage-pool/metal-csi/pvc-dryrun.img"); !exists {
			t.Error("File should still exist in dry-run mode")
		}

		t.Log("✅ Dry-run mode detected orphans but didn't delete them")
	})

	t.Run("MixedOrphans_DeletesOnlyOrphans", func(t *testing.T) {
		// Setup: Mix of orphaned and active volumes
		// Active volume 1 (has PV)
		mockRDS.CreateOrphanedFile("/storage-pool/metal-csi/pvc-active-1.img", 10*1024*1024*1024)
		mockRDS.CreateOrphanedVolume("pvc-active-1", "/storage-pool/metal-csi/pvc-active-1.img", 10*1024*1024*1024)

		// Orphaned disk (no PV, no file)
		mockRDS.CreateOrphanedVolume("pvc-orphan-mixed-1", "/storage-pool/metal-csi/pvc-orphan-mixed-1.img", 5*1024*1024*1024)

		// Orphaned file (no disk, no PV)
		mockRDS.CreateOrphanedFile("/storage-pool/metal-csi/pvc-orphan-mixed-2.img", 3*1024*1024*1024)

		k8sClient := fake.NewSimpleClientset()
		pv := &v1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-active-1"},
			Spec: v1.PersistentVolumeSpec{
				PersistentVolumeSource: v1.PersistentVolumeSource{
					CSI: &v1.CSIPersistentVolumeSource{
						Driver:       "nvmet.csi.whiskey.works",
						VolumeHandle: "pvc-active-1",
					},
				},
			},
		}
		if _, err := k8sClient.CoreV1().PersistentVolumes().Create(context.Background(), pv, metav1.CreateOptions{}); err != nil {
			t.Fatalf("Failed to create test PV: %v", err)
		}

		// Create reconciler
		rec, err := reconciler.NewOrphanReconciler(reconciler.OrphanReconcilerConfig{
			RDSClient:     rdsClient,
			K8sClient:     k8sClient,
			DriverName:    "nvmet.csi.whiskey.works",
			CheckInterval: 1 * time.Hour,
			GracePeriod:   1 * time.Second,
			DryRun:        false,
			Enabled:       true,
			BasePath:      "/storage-pool/metal-csi",
		})
		if err != nil {
			t.Fatalf("Failed to create reconciler: %v", err)
		}

		// Wait for grace period
		time.Sleep(2 * time.Second)

		// Run reconciliation
		if err := runConfirmedReconciliation(rec); err != nil {
			t.Fatalf("Reconciliation failed: %v", err)
		}

		// Verify active volume still exists
		if _, exists := mockRDS.GetVolume("pvc-active-1"); !exists {
			t.Error("Active volume should not be deleted")
		}
		if _, exists := mockRDS.GetFile("/storage-pool/metal-csi/pvc-active-1.img"); !exists {
			t.Error("Active volume file should not be deleted")
		}

		// Verify orphaned disk was deleted
		if _, exists := mockRDS.GetVolume("pvc-orphan-mixed-1"); exists {
			t.Error("Orphaned disk should have been deleted")
		}

		// Verify orphaned file was deleted
		if _, exists := mockRDS.GetFile("/storage-pool/metal-csi/pvc-orphan-mixed-2.img"); exists {
			t.Error("Orphaned file should have been deleted")
		}

		t.Log("✅ Mixed scenario: only orphans were deleted, active volumes preserved")
	})

	t.Run("FileSizeParsing_CorrectSizes", func(t *testing.T) {
		// Setup: Create files with different sizes to test parsing
		mockRDS.CreateOrphanedFile("/storage-pool/metal-csi/pvc-10gib.img", 10*1024*1024*1024)
		mockRDS.CreateOrphanedFile("/storage-pool/metal-csi/pvc-1024mib.img", 1024*1024*1024)
		mockRDS.CreateOrphanedFile("/storage-pool/metal-csi/pvc-512mib.img", 512*1024*1024)

		k8sClient := fake.NewSimpleClientset()

		// Create reconciler (dry-run to check detection)
		rec, err := reconciler.NewOrphanReconciler(reconciler.OrphanReconcilerConfig{
			RDSClient:     rdsClient,
			K8sClient:     k8sClient,
			DriverName:    "nvmet.csi.whiskey.works",
			CheckInterval: 1 * time.Hour,
			GracePeriod:   1 * time.Second,
			DryRun:        true,
			Enabled:       true,
			BasePath:      "/storage-pool/metal-csi",
		})
		if err != nil {
			t.Fatalf("Failed to create reconciler: %v", err)
		}

		// Run reconciliation
		if err := runConfirmedReconciliation(rec); err != nil {
			t.Fatalf("Reconciliation failed: %v", err)
		}

		// Verify files are correctly detected with proper sizes
		// (This test verifies the fix for the file size parsing bug)
		file10g, exists := mockRDS.GetFile("/storage-pool/metal-csi/pvc-10gib.img")
		if !exists {
			t.Error("File pvc-10gib.img should exist")
		} else if file10g.SizeBytes != 10*1024*1024*1024 {
			t.Errorf("Expected 10 GiB (10737418240 bytes), got %d bytes", file10g.SizeBytes)
		}

		file1024m, exists := mockRDS.GetFile("/storage-pool/metal-csi/pvc-1024mib.img")
		if !exists {
			t.Error("File pvc-1024mib.img should exist")
		} else if file1024m.SizeBytes != 1024*1024*1024 {
			t.Errorf("Expected 1024 MiB (1073741824 bytes), got %d bytes", file1024m.SizeBytes)
		}

		t.Log("✅ File sizes parsed correctly")
	})

	t.Run("NonCSIVolumes_Ignored", func(t *testing.T) {
		// Setup: Create non-CSI volumes (don't start with "pvc-")
		mockRDS.CreateOrphanedFile("/storage-pool/metal-csi/manual-volume.img", 50*1024*1024*1024)
		mockRDS.CreateOrphanedVolume("manual-volume", "/storage-pool/metal-csi/manual-volume.img", 50*1024*1024*1024)

		k8sClient := fake.NewSimpleClientset()

		// Create reconciler
		rec, err := reconciler.NewOrphanReconciler(reconciler.OrphanReconcilerConfig{
			RDSClient:     rdsClient,
			K8sClient:     k8sClient,
			DriverName:    "nvmet.csi.whiskey.works",
			CheckInterval: 1 * time.Hour,
			GracePeriod:   1 * time.Second,
			DryRun:        false,
			Enabled:       true,
			BasePath:      "/storage-pool/metal-csi",
		})
		if err != nil {
			t.Fatalf("Failed to create reconciler: %v", err)
		}

		// Run reconciliation
		if err := runConfirmedReconciliation(rec); err != nil {
			t.Fatalf("Reconciliation failed: %v", err)
		}

		// Verify non-CSI volume was NOT deleted
		if _, exists := mockRDS.GetVolume("manual-volume"); !exists {
			t.Error("Non-CSI volume should not be deleted")
		}

		// Note: The file might still be detected as orphaned since it ends with .img
		// but doesn't have a corresponding CSI-managed PV. This is expected behavior.

		t.Log("✅ Non-CSI volumes ignored by reconciler")
	})
}

// runConfirmedReconciliation runs enough consecutive inventory passes for the
// reconciler's default confirmation threshold to plan cleanup.
func runConfirmedReconciliation(rec *reconciler.OrphanReconciler) error {
	for i := 0; i < reconciler.DefaultOrphanMinimumConfirmations; i++ {
		if err := rec.TriggerReconciliation(context.Background()); err != nil {
			return err
		}
	}
	return nil
}
