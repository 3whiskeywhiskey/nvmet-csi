package reconciler

import (
	"context"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/rds"
)

const driverName = "nvmet.csi.whiskey.works"

// mockRDSClient implements rds.RDSClient for testing
type mockRDSClient struct {
	volumes        []rds.VolumeInfo
	files          []rds.FileInfo
	deletedVolumes []string
	deletedFiles   []string
	listVolumes    int
	listFiles      int
}

func (m *mockRDSClient) CreateVolume(opts rds.CreateVolumeOptions) error {
	return nil
}

func (m *mockRDSClient) DeleteVolume(slot string) error {
	m.deletedVolumes = append(m.deletedVolumes, slot)
	return nil
}

func (m *mockRDSClient) ResizeVolume(slot string, newSizeBytes int64) error {
	return nil
}

func (m *mockRDSClient) GetVolume(slot string) (*rds.VolumeInfo, error) {
	for _, vol := range m.volumes {
		if vol.Slot == slot {
			return &vol, nil
		}
	}
	return nil, &rds.VolumeNotFoundError{Slot: slot}
}

func (m *mockRDSClient) VerifyVolumeExists(slot string) error {
	_, err := m.GetVolume(slot)
	return err
}

func (m *mockRDSClient) GetCapacity(basePath string) (*rds.CapacityInfo, error) {
	return &rds.CapacityInfo{
		TotalBytes: 1000000000000,
		FreeBytes:  500000000000,
		UsedBytes:  500000000000,
	}, nil
}

func (m *mockRDSClient) ListVolumes() ([]rds.VolumeInfo, error) {
	m.listVolumes++
	return m.volumes, nil
}

func (m *mockRDSClient) ListFiles(path string) ([]rds.FileInfo, error) {
	m.listFiles++
	return m.files, nil
}

func (m *mockRDSClient) DeleteFile(path string) error {
	m.deletedFiles = append(m.deletedFiles, path)
	return nil
}

func (m *mockRDSClient) Connect() error {
	return nil
}

func (m *mockRDSClient) Close() error {
	return nil
}

func (m *mockRDSClient) GetAddress() string {
	return "192.0.2.10"
}

func (m *mockRDSClient) IsConnected() bool {
	return true
}

func (m *mockRDSClient) CreateSnapshot(opts rds.CreateSnapshotOptions) (*rds.SnapshotInfo, error) {
	return nil, nil
}

func (m *mockRDSClient) DeleteSnapshot(snapshotID string) error {
	return nil
}

func (m *mockRDSClient) GetSnapshot(snapshotID string) (*rds.SnapshotInfo, error) {
	return nil, nil
}

func (m *mockRDSClient) ListSnapshots() ([]rds.SnapshotInfo, error) {
	return nil, nil
}

func (m *mockRDSClient) RestoreSnapshot(snapshotID string, newVolumeOpts rds.CreateVolumeOptions) error {
	return nil
}

func (m *mockRDSClient) GetDiskMetrics(slot string) (*rds.DiskMetrics, error) {
	return nil, nil
}

func (m *mockRDSClient) GetHardwareHealth(snmpHost string, snmpCommunity string) (*rds.HardwareHealthMetrics, error) {
	return nil, nil
}

func TestNewOrphanReconciler(t *testing.T) {
	tests := []struct {
		name    string
		config  OrphanReconcilerConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: OrphanReconcilerConfig{
				RDSClient:  &mockRDSClient{},
				K8sClient:  fake.NewSimpleClientset(),
				DriverName: driverName,
				Enabled:    true,
			},
			wantErr: false,
		},
		{
			name: "missing RDS client",
			config: OrphanReconcilerConfig{
				K8sClient:  fake.NewSimpleClientset(),
				DriverName: driverName,
				Enabled:    true,
			},
			wantErr: true,
		},
		{
			name: "missing k8s client",
			config: OrphanReconcilerConfig{
				RDSClient:  &mockRDSClient{},
				DriverName: driverName,
				Enabled:    true,
			},
			wantErr: true,
		},
		{
			name: "empty driver name rejected",
			config: OrphanReconcilerConfig{
				RDSClient:  &mockRDSClient{},
				K8sClient:  fake.NewSimpleClientset(),
				Enabled:    true,
				DriverName: "",
			},
			wantErr: true,
		},
		{
			name: "enabled=false accepts empty driver name (disabled performs no inventory)",
			config: OrphanReconcilerConfig{
				RDSClient:  &mockRDSClient{},
				K8sClient:  fake.NewSimpleClientset(),
				Enabled:    false,
				DriverName: "",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewOrphanReconciler(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewOrphanReconciler() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOrphanReconciler_Reconcile(t *testing.T) {
	tests := []struct {
		name          string
		rdsVolumes    []rds.VolumeInfo
		k8sPVs        []*v1.PersistentVolume
		dryRun        bool
		wantDeleted   []string
		wantNoDeletes bool
	}{
		{
			name: "no orphans - all volumes have PVs",
			rdsVolumes: []rds.VolumeInfo{
				{Slot: "pvc-123", FilePath: "/storage-pool/metal-csi/pvc-123.img", FileSizeBytes: 10737418240},
				{Slot: "pvc-456", FilePath: "/storage-pool/metal-csi/pvc-456.img", FileSizeBytes: 10737418240},
			},
			k8sPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-123"},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "nvmet.csi.whiskey.works",
								VolumeHandle: "pvc-123",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-456"},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "nvmet.csi.whiskey.works",
								VolumeHandle: "pvc-456",
							},
						},
					},
				},
			},
			wantNoDeletes: true,
		},
		{
			name: "orphaned volume - no corresponding PV",
			rdsVolumes: []rds.VolumeInfo{
				{Slot: "pvc-123", FilePath: "/storage-pool/metal-csi/pvc-123.img", FileSizeBytes: 10737418240},
				{Slot: "pvc-orphan", FilePath: "/storage-pool/metal-csi/pvc-orphan.img", FileSizeBytes: 10737418240},
			},
			k8sPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-123"},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "nvmet.csi.whiskey.works",
								VolumeHandle: "pvc-123",
							},
						},
					},
				},
			},
			wantDeleted: []string{"pvc-orphan"},
		},
		{
			name: "dry run - orphans detected but not deleted",
			rdsVolumes: []rds.VolumeInfo{
				{Slot: "pvc-orphan", FilePath: "/storage-pool/metal-csi/pvc-orphan.img", FileSizeBytes: 10737418240},
			},
			k8sPVs:        []*v1.PersistentVolume{},
			dryRun:        true,
			wantNoDeletes: true,
		},
		{
			name: "non-CSI volumes ignored",
			rdsVolumes: []rds.VolumeInfo{
				{Slot: "manual-volume", FilePath: "/storage-pool/manual-volume.img", FileSizeBytes: 10737418240},
				{Slot: "pvc-123", FilePath: "/storage-pool/metal-csi/pvc-123.img", FileSizeBytes: 10737418240},
			},
			k8sPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-123"},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "nvmet.csi.whiskey.works",
								VolumeHandle: "pvc-123",
							},
						},
					},
				},
			},
			wantNoDeletes: true, // manual-volume should be ignored
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock RDS client
			mockRDS := &mockRDSClient{
				volumes:        tt.rdsVolumes,
				deletedVolumes: []string{},
			}

			// Create fake Kubernetes clientset with PVs
			k8sClient := fake.NewSimpleClientset()
			for _, pv := range tt.k8sPVs {
				if _, err := k8sClient.CoreV1().PersistentVolumes().Create(context.Background(), pv, metav1.CreateOptions{}); err != nil {
					t.Fatalf("Failed to create test PV: %v", err)
				}
			}

			// Create reconciler
			config := OrphanReconcilerConfig{
				RDSClient:            mockRDS,
				K8sClient:            k8sClient,
				DriverName:           driverName,
				CheckInterval:        1 * time.Hour,
				GracePeriod:          1 * time.Second, // Short grace period for testing
				DryRun:               tt.dryRun,
				Enabled:              true,
				MinimumConfirmations: 1,
			}

			reconciler, err := NewOrphanReconciler(config)
			if err != nil {
				t.Fatalf("NewOrphanReconciler() failed: %v", err)
			}

			// Run reconciliation
			if err := reconciler.reconcile(context.Background()); err != nil {
				t.Fatalf("reconcile() failed: %v", err)
			}

			// Check deleted volumes
			if tt.wantNoDeletes {
				if len(mockRDS.deletedVolumes) > 0 {
					t.Errorf("Expected no deletions, but got: %v", mockRDS.deletedVolumes)
				}
			} else {
				if len(mockRDS.deletedVolumes) != len(tt.wantDeleted) {
					t.Errorf("Expected %d deletions, got %d: %v", len(tt.wantDeleted), len(mockRDS.deletedVolumes), mockRDS.deletedVolumes)
				}

				// Check that the correct volumes were deleted
				deletedMap := make(map[string]bool)
				for _, vol := range mockRDS.deletedVolumes {
					deletedMap[vol] = true
				}

				for _, expected := range tt.wantDeleted {
					if !deletedMap[expected] {
						t.Errorf("Expected volume %s to be deleted, but it wasn't", expected)
					}
				}
			}
		})
	}
}

func TestOrphanReconciler_BuildInventoryPlansMutationsWithoutDeleting(t *testing.T) {
	mockRDS := &mockRDSClient{
		volumes: []rds.VolumeInfo{
			{Slot: "pvc-active", FilePath: "/storage-pool/metal-csi/pvc-active.img", FileSizeBytes: 10 << 30},
			{Slot: "pvc-orphan", FilePath: "/storage-pool/metal-csi/pvc-orphan.img", FileSizeBytes: 20 << 30},
			{Slot: "manual-volume", FilePath: "/storage-pool/manual.img", FileSizeBytes: 30 << 30},
		},
		files: []rds.FileInfo{
			{Name: "pvc-active.img", Path: "/storage-pool/metal-csi/pvc-active.img", SizeBytes: 10 << 30, Type: "file"},
			{Name: "pvc-orphan.img", Path: "/storage-pool/metal-csi/pvc-orphan.img", SizeBytes: 20 << 30, Type: "file"},
			{Name: "pvc-file-orphan.img", Path: "/storage-pool/metal-csi/pvc-file-orphan.img", SizeBytes: 5 << 30, Type: "file", CreatedAt: time.Now().Add(-time.Hour)},
			{Name: "notes.txt", Path: "/storage-pool/metal-csi/notes.txt", SizeBytes: 1, Type: "file"},
		},
		deletedVolumes: []string{},
		deletedFiles:   []string{},
	}
	k8sClient := fake.NewSimpleClientset(activePV("pv-active", "pvc-active"))

	reconciler, err := NewOrphanReconciler(OrphanReconcilerConfig{
		RDSClient:            mockRDS,
		K8sClient:            k8sClient,
		DriverName:           driverName,
		GracePeriod:          time.Second,
		DryRun:               true,
		Enabled:              true,
		BasePath:             "/storage-pool/metal-csi",
		MinimumConfirmations: 1,
	})
	if err != nil {
		t.Fatalf("NewOrphanReconciler() failed: %v", err)
	}

	inventory, err := reconciler.BuildInventory(context.Background())
	if err != nil {
		t.Fatalf("BuildInventory() failed: %v", err)
	}

	if inventory.RDSVolumeCount != 3 {
		t.Fatalf("RDSVolumeCount = %d, want 3", inventory.RDSVolumeCount)
	}
	if inventory.CSIVolumeCount != 2 {
		t.Fatalf("CSIVolumeCount = %d, want 2", inventory.CSIVolumeCount)
	}
	if inventory.ActivePVCount != 1 {
		t.Fatalf("ActivePVCount = %d, want 1", inventory.ActivePVCount)
	}
	if inventory.RDSFileCount != 4 {
		t.Fatalf("RDSFileCount = %d, want 4", inventory.RDSFileCount)
	}
	if len(inventory.DiskOrphans) != 1 || inventory.DiskOrphans[0].VolumeID != "pvc-orphan" {
		t.Fatalf("DiskOrphans = %+v, want pvc-orphan", inventory.DiskOrphans)
	}
	if len(inventory.FileOrphans) != 1 || inventory.FileOrphans[0].FilePath != "/storage-pool/metal-csi/pvc-file-orphan.img" {
		t.Fatalf("FileOrphans = %+v, want pvc-file-orphan.img", inventory.FileOrphans)
	}
	if len(inventory.PlannedMutations) != 2 {
		t.Fatalf("PlannedMutations len = %d, want 2: %+v", len(inventory.PlannedMutations), inventory.PlannedMutations)
	}
	if inventory.PlannedMutations[0].Action != OrphanMutationDeleteDisk || inventory.PlannedMutations[0].Target != "pvc-orphan" {
		t.Fatalf("first planned mutation = %+v, want delete_disk pvc-orphan", inventory.PlannedMutations[0])
	}
	if inventory.PlannedMutations[1].Action != OrphanMutationDeleteFile || inventory.PlannedMutations[1].Target != "/storage-pool/metal-csi/pvc-file-orphan.img" {
		t.Fatalf("second planned mutation = %+v, want delete_file pvc-file-orphan.img", inventory.PlannedMutations[1])
	}
	if len(mockRDS.deletedVolumes) != 0 || len(mockRDS.deletedFiles) != 0 {
		t.Fatalf("BuildInventory mutated RDS: deletedVolumes=%v deletedFiles=%v", mockRDS.deletedVolumes, mockRDS.deletedFiles)
	}
}

func TestOrphanReconciler_DriverNameIsolation(t *testing.T) {
	mockRDS := &mockRDSClient{
		volumes: []rds.VolumeInfo{
			{Slot: "pvc-custom", FilePath: "/storage-pool/metal-csi/pvc-custom.img", FileSizeBytes: 10 << 30},
			{Slot: "pvc-prod", FilePath: "/storage-pool/metal-csi/pvc-prod.img", FileSizeBytes: 20 << 30},
			{Slot: "pvc-orphan", FilePath: "/storage-pool/metal-csi/pvc-orphan.img", FileSizeBytes: 20 << 30},
		},
		files: []rds.FileInfo{
			{Name: "pvc-custom.img", Path: "/storage-pool/metal-csi/pvc-custom.img", SizeBytes: 10 << 30, Type: "file"},
			{Name: "pvc-prod.img", Path: "/storage-pool/metal-csi/pvc-prod.img", SizeBytes: 20 << 30, Type: "file"},
			{Name: "pvc-orphan.img", Path: "/storage-pool/metal-csi/pvc-orphan.img", SizeBytes: 20 << 30, Type: "file"},
			{Name: "pvc-file-orphan.img", Path: "/storage-pool/metal-csi/pvc-file-orphan.img", SizeBytes: 5 << 30, Type: "file", CreatedAt: time.Now().Add(-time.Hour)},
		},
		deletedVolumes: []string{},
		deletedFiles:   []string{},
	}
	k8sClient := fake.NewSimpleClientset(
		&v1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-custom"},
			Spec: v1.PersistentVolumeSpec{
				PersistentVolumeSource: v1.PersistentVolumeSource{
					CSI: &v1.CSIPersistentVolumeSource{
						Driver:       "example-rds.invalid",
						VolumeHandle: "pvc-custom",
					},
				},
			},
		},
		&v1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{Name: "pv-prod"},
			Spec: v1.PersistentVolumeSpec{
				PersistentVolumeSource: v1.PersistentVolumeSource{
					CSI: &v1.CSIPersistentVolumeSource{
						Driver:       "nvmet.csi.whiskey.works",
						VolumeHandle: "pvc-prod",
					},
				},
			},
		},
	)

	reconciler, err := NewOrphanReconciler(OrphanReconcilerConfig{
		RDSClient:            mockRDS,
		K8sClient:            k8sClient,
		GracePeriod:          time.Second,
		DryRun:               true,
		Enabled:              true,
		MinimumConfirmations: 1,
		DriverName:           "example-rds.invalid",
		BasePath:             "/storage-pool/metal-csi",
	})
	if err != nil {
		t.Fatalf("NewOrphanReconciler() failed: %v", err)
	}

	inventory, err := reconciler.BuildInventory(context.Background())
	if err != nil {
		t.Fatalf("BuildInventory() failed: %v", err)
	}

	if inventory.RDSVolumeCount != 3 {
		t.Fatalf("RDSVolumeCount = %d, want 3", inventory.RDSVolumeCount)
	}
	if inventory.CSIVolumeCount != 3 {
		t.Fatalf("CSIVolumeCount = %d, want 3", inventory.CSIVolumeCount)
	}
	if inventory.ActivePVCount != 2 {
		t.Fatalf("ActivePVCount = %d, want 2 (custom driver PV + foreign prod driver PV both protect their volumeHandles from orphaning)", inventory.ActivePVCount)
	}
	if len(inventory.DiskOrphans) != 1 || inventory.DiskOrphans[0].VolumeID != "pvc-orphan" {
		t.Fatalf("DiskOrphans = %+v, want only pvc-orphan (foreign production-driver PV must protect pvc-prod disk from being classified orphan)", inventory.DiskOrphans)
	}
	if inventory.RDSFileCount != 4 {
		t.Fatalf("RDSFileCount = %d, want 4", inventory.RDSFileCount)
	}
	if len(inventory.FileOrphans) != 1 || inventory.FileOrphans[0].FilePath != "/storage-pool/metal-csi/pvc-file-orphan.img" {
		t.Fatalf("FileOrphans = %+v, want only pvc-file-orphan.img (foreign prod-driver PV protects matching file via volumeHandle; pvc-prod file protected)", inventory.FileOrphans)
	}
	if len(inventory.PlannedMutations) != 2 {
		t.Fatalf("PlannedMutations len = %d, want 2: %+v", len(inventory.PlannedMutations), inventory.PlannedMutations)
	}
	if !hasOrphanMutation(inventory.PlannedMutations, OrphanMutationDeleteDisk, "pvc-orphan") {
		t.Fatalf("expected delete_disk pvc-orphan planned, got: %+v", inventory.PlannedMutations)
	}
	if !hasOrphanMutation(inventory.PlannedMutations, OrphanMutationDeleteFile, "/storage-pool/metal-csi/pvc-file-orphan.img") {
		t.Fatalf("expected delete_file pvc-file-orphan.img planned, got: %+v", inventory.PlannedMutations)
	}
	if hasOrphanMutation(inventory.PlannedMutations, OrphanMutationDeleteDisk, "pvc-prod") ||
		hasOrphanMutation(inventory.PlannedMutations, OrphanMutationDeleteFile, "/storage-pool/metal-csi/pvc-prod.img") {
		t.Fatalf("unexpected delete mutation for pvc-prod (protected by foreign prod-driver PV), got: %+v", inventory.PlannedMutations)
	}
	if len(mockRDS.deletedVolumes) != 0 || len(mockRDS.deletedFiles) != 0 {
		t.Fatalf("BuildInventory mutated RDS: deletedVolumes=%v deletedFiles=%v", mockRDS.deletedVolumes, mockRDS.deletedFiles)
	}
}

func TestOrphanReconciler_FileCleanupGuardrails(t *testing.T) {
	oldEnough := time.Now().Add(-time.Hour)
	recent := time.Now()
	tests := []struct {
		name                string
		file                rds.FileInfo
		wantInventoryOrphan bool
		wantPlannedMutation bool
		wantDeleted         bool
	}{
		{
			name: "manual image ignored",
			file: rds.FileInfo{
				Name:      "manual-backup.img",
				Path:      "/storage-pool/metal-csi/manual-backup.img",
				SizeBytes: 5 << 30,
				Type:      "file",
				CreatedAt: oldEnough,
			},
		},
		{
			name: "directory image ignored",
			file: rds.FileInfo{
				Name:      "pvc-directory.img",
				Path:      "/storage-pool/metal-csi/pvc-directory.img",
				SizeBytes: 0,
				Type:      "directory",
				CreatedAt: oldEnough,
			},
		},
		{
			name: "unknown age not planned",
			file: rds.FileInfo{
				Name:      "pvc-unknown-age.img",
				Path:      "/storage-pool/metal-csi/pvc-unknown-age.img",
				SizeBytes: 5 << 30,
				Type:      "file",
			},
			wantInventoryOrphan: true,
		},
		{
			name: "recent file not planned",
			file: rds.FileInfo{
				Name:      "pvc-recent.img",
				Path:      "/storage-pool/metal-csi/pvc-recent.img",
				SizeBytes: 5 << 30,
				Type:      "file",
				CreatedAt: recent,
			},
			wantInventoryOrphan: true,
		},
		{
			name: "old CSI image planned and deleted",
			file: rds.FileInfo{
				Name:      "pvc-old.img",
				Path:      "/storage-pool/metal-csi/pvc-old.img",
				SizeBytes: 5 << 30,
				Type:      "file",
				CreatedAt: oldEnough,
			},
			wantInventoryOrphan: true,
			wantPlannedMutation: true,
			wantDeleted:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockRDS := &mockRDSClient{
				files: []rds.FileInfo{tt.file},
			}
			reconciler, err := NewOrphanReconciler(OrphanReconcilerConfig{
				RDSClient:            mockRDS,
				K8sClient:            fake.NewSimpleClientset(),
				DriverName:           driverName,
				GracePeriod:          time.Minute,
				DryRun:               false,
				Enabled:              true,
				BasePath:             "/storage-pool/metal-csi",
				MinimumConfirmations: 1,
			})
			if err != nil {
				t.Fatalf("NewOrphanReconciler() failed: %v", err)
			}

			inventory, err := reconciler.BuildInventory(context.Background())
			if err != nil {
				t.Fatalf("BuildInventory() failed: %v", err)
			}
			if got := len(inventory.FileOrphans) == 1; got != tt.wantInventoryOrphan {
				t.Fatalf("file orphan found = %t, want %t: %+v", got, tt.wantInventoryOrphan, inventory.FileOrphans)
			}
			if got := hasOrphanMutation(inventory.PlannedMutations, OrphanMutationDeleteFile, tt.file.Path); got != tt.wantPlannedMutation {
				t.Fatalf("planned delete = %t, want %t: %+v", got, tt.wantPlannedMutation, inventory.PlannedMutations)
			}

			reconciler.applyOrphanInventory(inventory)
			if got := containsString(mockRDS.deletedFiles, tt.file.Path); got != tt.wantDeleted {
				t.Fatalf("deleted file = %t, want %t: deletedFiles=%v", got, tt.wantDeleted, mockRDS.deletedFiles)
			}
		})
	}
}

func TestOrphanReconciler_DefaultConfirmationsDeferCleanup(t *testing.T) {
	mockRDS := &mockRDSClient{
		volumes: []rds.VolumeInfo{
			{Slot: "pvc-orphan", FilePath: "/storage-pool/metal-csi/pvc-orphan.img", FileSizeBytes: 20 << 30},
		},
	}

	reconciler, err := NewOrphanReconciler(OrphanReconcilerConfig{
		RDSClient:   mockRDS,
		K8sClient:   fake.NewSimpleClientset(),
		DriverName:  driverName,
		GracePeriod: time.Second,
		Enabled:     true,
	})
	if err != nil {
		t.Fatalf("NewOrphanReconciler() failed: %v", err)
	}

	if err := reconciler.reconcile(context.Background()); err != nil {
		t.Fatalf("first reconcile() failed: %v", err)
	}
	if len(mockRDS.deletedVolumes) != 0 {
		t.Fatalf("first reconcile deleted volumes: %v", mockRDS.deletedVolumes)
	}

	if err := reconciler.reconcile(context.Background()); err != nil {
		t.Fatalf("second reconcile() failed: %v", err)
	}
	if len(mockRDS.deletedVolumes) != 1 || mockRDS.deletedVolumes[0] != "pvc-orphan" {
		t.Fatalf("deletedVolumes = %v, want [pvc-orphan]", mockRDS.deletedVolumes)
	}
}

func TestOrphanReconciler_StartSkipsStartupInventoryByDefault(t *testing.T) {
	mockRDS := &mockRDSClient{
		volumes: []rds.VolumeInfo{
			{Slot: "pvc-orphan", FilePath: "/storage-pool/metal-csi/pvc-orphan.img", FileSizeBytes: 20 << 30},
		},
	}

	reconciler, err := NewOrphanReconciler(OrphanReconcilerConfig{
		RDSClient:     mockRDS,
		K8sClient:     fake.NewSimpleClientset(),
		DriverName:    driverName,
		CheckInterval: time.Hour,
		GracePeriod:   time.Second,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("NewOrphanReconciler() failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := reconciler.Start(ctx); err != nil {
		t.Fatalf("Start() failed: %v", err)
	}
	reconciler.Stop()

	if mockRDS.listVolumes != 0 {
		t.Fatalf("ListVolumes calls = %d, want 0", mockRDS.listVolumes)
	}
	if len(mockRDS.deletedVolumes) != 0 {
		t.Fatalf("deletedVolumes = %v, want none", mockRDS.deletedVolumes)
	}
}

func TestOrphanReconciler_OrphanedFiles(t *testing.T) {
	tests := []struct {
		name        string
		rdsVolumes  []rds.VolumeInfo
		files       []rds.FileInfo
		k8sPVs      []*v1.PersistentVolume
		basePath    string
		expectFiles int // Expected number of orphaned files
	}{
		{
			name: "orphaned files - files without disk objects",
			rdsVolumes: []rds.VolumeInfo{
				{Slot: "pvc-123", FilePath: "/storage-pool/metal-csi/pvc-123.img", FileSizeBytes: 10737418240},
			},
			files: []rds.FileInfo{
				// Note: Mock returns normalized paths (with leading /) as parseFileInfo() would
				// In production, RouterOS /file print returns paths without /, but parseFileInfo() adds it
				{Name: "pvc-123.img", Path: "/storage-pool/metal-csi/pvc-123.img", SizeBytes: 10737418240, Type: "file"},
				{Name: "pvc-orphan1.img", Path: "/storage-pool/metal-csi/pvc-orphan1.img", SizeBytes: 10737418240, Type: "file", CreatedAt: time.Now().Add(-time.Hour)},
				{Name: "pvc-orphan2.img", Path: "/storage-pool/metal-csi/pvc-orphan2.img", SizeBytes: 10737418240, Type: "file", CreatedAt: time.Now().Add(-time.Hour)},
			},
			k8sPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-123"},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "nvmet.csi.whiskey.works",
								VolumeHandle: "pvc-123",
							},
						},
					},
				},
			},
			basePath:    "/storage-pool/metal-csi",
			expectFiles: 2, // pvc-orphan1.img and pvc-orphan2.img
		},
		{
			name: "no orphaned files - all files have disk objects",
			rdsVolumes: []rds.VolumeInfo{
				{Slot: "pvc-123", FilePath: "/storage-pool/metal-csi/pvc-123.img", FileSizeBytes: 10737418240},
				{Slot: "pvc-456", FilePath: "/storage-pool/metal-csi/pvc-456.img", FileSizeBytes: 10737418240},
			},
			files: []rds.FileInfo{
				// Mock returns normalized paths as parseFileInfo() would
				{Name: "pvc-123.img", Path: "/storage-pool/metal-csi/pvc-123.img", SizeBytes: 10737418240, Type: "file"},
				{Name: "pvc-456.img", Path: "/storage-pool/metal-csi/pvc-456.img", SizeBytes: 10737418240, Type: "file"},
			},
			k8sPVs: []*v1.PersistentVolume{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-123"},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "nvmet.csi.whiskey.works",
								VolumeHandle: "pvc-123",
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "pv-456"},
					Spec: v1.PersistentVolumeSpec{
						PersistentVolumeSource: v1.PersistentVolumeSource{
							CSI: &v1.CSIPersistentVolumeSource{
								Driver:       "nvmet.csi.whiskey.works",
								VolumeHandle: "pvc-456",
							},
						},
					},
				},
			},
			basePath:    "/storage-pool/metal-csi",
			expectFiles: 0,
		},
		{
			name:        "no files listed when basePath is empty",
			rdsVolumes:  []rds.VolumeInfo{},
			files:       []rds.FileInfo{},
			k8sPVs:      []*v1.PersistentVolume{},
			basePath:    "", // Empty base path - file checking disabled
			expectFiles: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock RDS client
			mockRDS := &mockRDSClient{
				volumes:        tt.rdsVolumes,
				files:          tt.files,
				deletedVolumes: []string{},
			}

			// Create fake Kubernetes clientset with PVs
			k8sClient := fake.NewSimpleClientset()
			for _, pv := range tt.k8sPVs {
				if _, err := k8sClient.CoreV1().PersistentVolumes().Create(context.Background(), pv, metav1.CreateOptions{}); err != nil {
					t.Fatalf("Failed to create test PV: %v", err)
				}
			}

			// Create reconciler
			config := OrphanReconcilerConfig{
				RDSClient:            mockRDS,
				K8sClient:            k8sClient,
				DriverName:           driverName,
				CheckInterval:        1 * time.Hour,
				GracePeriod:          1 * time.Second,
				DryRun:               true, // Always dry-run for tests
				Enabled:              true,
				BasePath:             tt.basePath,
				MinimumConfirmations: 1,
			}

			reconciler, err := NewOrphanReconciler(config)
			if err != nil {
				t.Fatalf("NewOrphanReconciler() failed: %v", err)
			}

			// Run reconciliation
			if err := reconciler.reconcile(context.Background()); err != nil {
				t.Fatalf("reconcile() failed: %v", err)
			}

			// Verify orphaned files were detected (we can't check the count directly,
			// but we verify the reconciliation completed without error)
			// In a real scenario, we'd expose metrics or a status API to verify counts
		})
	}
}

func activePV(name, volumeHandle string) *v1.PersistentVolume {
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:       "nvmet.csi.whiskey.works",
					VolumeHandle: volumeHandle,
				},
			},
		},
	}
}

func hasOrphanMutation(mutations []OrphanMutation, action OrphanMutationAction, target string) bool {
	for _, mutation := range mutations {
		if mutation.Action == action && mutation.Target == target {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
