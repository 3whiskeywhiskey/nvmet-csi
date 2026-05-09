package reconciler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/rds"
)

const (
	// DefaultOrphanCheckInterval is the default interval between orphan checks
	DefaultOrphanCheckInterval = 1 * time.Hour

	// DefaultOrphanGracePeriod is the minimum age before a volume is considered orphaned
	// This prevents premature cleanup of volumes that are still being provisioned
	DefaultOrphanGracePeriod = 5 * time.Minute

	// DefaultOrphanMinimumConfirmations is the number of consecutive inventory
	// passes that must see the same orphan before destructive cleanup is planned.
	DefaultOrphanMinimumConfirmations = 2

	// VolumeIDPrefix is the expected prefix for CSI-managed volumes
	VolumeIDPrefix = "pvc-"
)

// OrphanReconcilerConfig contains configuration for the orphan reconciler
type OrphanReconcilerConfig struct {
	// RDSClient is the RDS client for listing/deleting volumes
	RDSClient rds.RDSClient

	// K8sClient is the Kubernetes clientset for listing PVs
	K8sClient kubernetes.Interface

	// CheckInterval is how often to check for orphans
	CheckInterval time.Duration

	// GracePeriod is the minimum age before considering a volume orphaned
	GracePeriod time.Duration

	// DryRun if true, will only log orphans without deleting them
	DryRun bool

	// RunOnStart runs one inventory pass immediately when the reconciler starts.
	// Leave false in production to avoid adding RouterOS command chatter during
	// controller restarts, node churn, or cluster recovery.
	RunOnStart bool

	// MinimumConfirmations is the number of consecutive inventory passes required
	// before cleanup is planned. Values <= 0 use DefaultOrphanMinimumConfirmations.
	MinimumConfirmations int

	// Enabled enables/disables the reconciler
	Enabled bool

	// BasePath is the directory path on RDS where volume files are stored
	// Example: /storage-pool/metal-csi
	BasePath string
}

// OrphanReconciler periodically checks for orphaned volumes and cleans them up
type OrphanReconciler struct {
	config         OrphanReconcilerConfig
	stopCh         chan struct{}
	wg             sync.WaitGroup
	candidatesMu   sync.Mutex
	diskCandidates map[string]orphanCandidateState
	fileCandidates map[string]orphanCandidateState
}

// OrphanedVolume represents a volume that appears to be orphaned
type OrphanedVolume struct {
	VolumeID  string
	FilePath  string
	SizeBytes int64
	CreatedAt time.Time
}

// OrphanedFile represents a file that exists on the filesystem but has no disk object
type OrphanedFile struct {
	FileName  string
	FilePath  string
	SizeBytes int64
	CreatedAt time.Time
}

// OrphanMutationAction is a planned cleanup operation from an orphan inventory.
type OrphanMutationAction string

const (
	OrphanMutationDeleteDisk OrphanMutationAction = "delete_disk"
	OrphanMutationDeleteFile OrphanMutationAction = "delete_file"
)

// OrphanMutation describes a planned cleanup action without applying it.
type OrphanMutation struct {
	Action OrphanMutationAction
	Target string
	Reason string
}

// OrphanInventory is the observed RDS/Kubernetes stale-object state for one
// reconciliation pass.
type OrphanInventory struct {
	ObservedAt       time.Time
	RDSVolumeCount   int
	CSIVolumeCount   int
	ActivePVCount    int
	RDSFileCount     int
	DiskOrphans      []OrphanedVolume
	FileOrphans      []OrphanedFile
	PlannedMutations []OrphanMutation
}

type orphanCandidateState struct {
	firstSeen    time.Time
	lastSeen     time.Time
	observations int
}

// NewOrphanReconciler creates a new orphan reconciler
func NewOrphanReconciler(config OrphanReconcilerConfig) (*OrphanReconciler, error) {
	// Validate config
	if config.RDSClient == nil {
		return nil, fmt.Errorf("RDSClient is required")
	}
	if config.K8sClient == nil {
		return nil, fmt.Errorf("K8sClient is required")
	}

	// Set defaults
	if config.CheckInterval == 0 {
		config.CheckInterval = DefaultOrphanCheckInterval
	}
	if config.GracePeriod == 0 {
		config.GracePeriod = DefaultOrphanGracePeriod
	}
	if config.MinimumConfirmations <= 0 {
		config.MinimumConfirmations = DefaultOrphanMinimumConfirmations
	}

	return &OrphanReconciler{
		config:         config,
		stopCh:         make(chan struct{}),
		diskCandidates: make(map[string]orphanCandidateState),
		fileCandidates: make(map[string]orphanCandidateState),
	}, nil
}

// Start begins the reconciliation loop
func (r *OrphanReconciler) Start(ctx context.Context) error {
	if !r.config.Enabled {
		klog.Info("Orphan reconciler is disabled")
		return nil
	}

	klog.Infof("Starting orphan reconciler (interval=%v, grace_period=%v, dry_run=%v, run_on_start=%v, minimum_confirmations=%d)",
		r.config.CheckInterval, r.config.GracePeriod, r.config.DryRun, r.config.RunOnStart, r.config.MinimumConfirmations)

	r.wg.Add(1)
	go r.run(ctx)

	return nil
}

// Stop stops the reconciliation loop
func (r *OrphanReconciler) Stop() {
	if !r.config.Enabled {
		return
	}

	klog.Info("Stopping orphan reconciler")
	close(r.stopCh)
	r.wg.Wait()
	klog.Info("Orphan reconciler stopped")
}

// run is the main reconciliation loop
func (r *OrphanReconciler) run(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.config.CheckInterval)
	defer ticker.Stop()

	if r.config.RunOnStart {
		if err := r.reconcile(ctx); err != nil {
			klog.Errorf("Initial orphan reconciliation failed: %v", err)
		}
	} else {
		klog.Infof("Orphan reconciler startup inventory skipped; first scan scheduled in %v", r.config.CheckInterval)
	}

	for {
		select {
		case <-ticker.C:
			if err := r.reconcile(ctx); err != nil {
				klog.Errorf("Orphan reconciliation failed: %v", err)
			}
		case <-r.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

// reconcile performs one reconciliation cycle
func (r *OrphanReconciler) reconcile(ctx context.Context) error {
	klog.V(2).Info("Starting orphan reconciliation cycle")
	start := time.Now()

	inventory, err := r.BuildInventory(ctx)
	if err != nil {
		return err
	}

	klog.V(2).Infof("Orphan reconciliation inventory built (duration=%v, rds_volumes=%d, csi_volumes=%d, active_pvs=%d, rds_files=%d, disk_orphans=%d, file_orphans=%d, planned_mutations=%d)",
		time.Since(start),
		inventory.RDSVolumeCount,
		inventory.CSIVolumeCount,
		inventory.ActivePVCount,
		inventory.RDSFileCount,
		len(inventory.DiskOrphans),
		len(inventory.FileOrphans),
		len(inventory.PlannedMutations))

	r.applyOrphanInventory(inventory)

	klog.V(2).Infof("Orphan reconciliation cycle complete (duration=%v, disk_orphans=%d, file_orphans=%d, total=%d, planned_mutations=%d)",
		time.Since(start),
		len(inventory.DiskOrphans),
		len(inventory.FileOrphans),
		len(inventory.DiskOrphans)+len(inventory.FileOrphans),
		len(inventory.PlannedMutations))

	return nil
}

// BuildInventory observes RDS and Kubernetes state and returns planned cleanup
// actions without mutating RDS.
func (r *OrphanReconciler) BuildInventory(ctx context.Context) (*OrphanInventory, error) {
	// Get all volumes from RDS
	rdsVolumes, err := r.config.RDSClient.ListVolumes()
	if err != nil {
		return nil, fmt.Errorf("failed to list RDS volumes: %w", err)
	}

	// Get all PVs from Kubernetes
	pvList, err := r.config.K8sClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list Kubernetes PVs: %w", err)
	}

	// Build a map of active volume IDs from Kubernetes PVs
	activeVolumeIDs := make(map[string]bool)
	klog.V(4).Infof("Scanning %d PersistentVolumes in Kubernetes", len(pvList.Items))
	for _, pv := range pvList.Items {
		// Only consider PVs from this CSI driver
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == "nvmet.csi.whiskey.works" {
			volumeID := pv.Spec.CSI.VolumeHandle
			activeVolumeIDs[volumeID] = true
			klog.V(4).Infof("  Found active PV: %s → VolumeHandle=%s, Phase=%s, ClaimRef=%s/%s",
				pv.Name, volumeID, pv.Status.Phase,
				getNamespace(pv.Spec.ClaimRef), getName(pv.Spec.ClaimRef))
		}
	}

	klog.Infof("Orphan reconciliation: %d volumes in RDS, %d active PVs in Kubernetes", len(rdsVolumes), len(activeVolumeIDs))

	inventory := &OrphanInventory{
		ObservedAt:     time.Now(),
		RDSVolumeCount: len(rdsVolumes),
		ActivePVCount:  len(activeVolumeIDs),
	}

	// Log all RDS volumes for visibility
	for _, vol := range rdsVolumes {
		if strings.HasPrefix(vol.Slot, VolumeIDPrefix) {
			inventory.CSIVolumeCount++
			hasActivePV := activeVolumeIDs[vol.Slot]
			klog.V(4).Infof("  RDS volume: %s (size=%d bytes, path=%s, hasActivePV=%v)",
				vol.Slot, vol.FileSizeBytes, vol.FilePath, hasActivePV)
		}
	}

	inventory.DiskOrphans = r.findOrphanedDisks(rdsVolumes, activeVolumeIDs)

	if r.config.BasePath != "" {
		fileOrphans, fileCount, err := r.findOrphanedFiles(rdsVolumes, activeVolumeIDs)
		if err != nil {
			klog.Errorf("Failed to inventory orphaned files: %v", err)
		} else {
			inventory.RDSFileCount = fileCount
			inventory.FileOrphans = fileOrphans
		}
	}

	r.planConfirmedMutations(inventory)

	return inventory, nil
}

// findOrphanedDisks identifies orphaned disk objects without mutating RDS.
func (r *OrphanReconciler) findOrphanedDisks(rdsVolumes []rds.VolumeInfo, activeVolumeIDs map[string]bool) []OrphanedVolume {
	orphans := []OrphanedVolume{}

	klog.V(4).Infof("Checking %d RDS volumes for orphans (CSI-managed volumes must start with '%s')", len(rdsVolumes), VolumeIDPrefix)

	for _, vol := range rdsVolumes {
		// Skip volumes that don't match our CSI-managed pattern
		if !strings.HasPrefix(vol.Slot, VolumeIDPrefix) {
			klog.V(5).Infof("  Skipping non-CSI volume: %s (does not start with '%s')", vol.Slot, VolumeIDPrefix)
			continue
		}

		// Check if this volume has a corresponding PV
		if activeVolumeIDs[vol.Slot] {
			klog.V(4).Infof("  Volume %s: HAS active PV - keeping", vol.Slot)
			continue
		}

		klog.V(4).Infof("  Volume %s: NO active PV - marking as orphan candidate", vol.Slot)

		// Volume appears to be orphaned
		// Note: We can't get creation time from RDS, so we'll use grace period
		// to avoid deleting volumes that were just created
		orphan := OrphanedVolume{
			VolumeID:  vol.Slot,
			FilePath:  vol.FilePath,
			SizeBytes: vol.FileSizeBytes,
			// We can't determine actual creation time from RDS, so assume it's old enough
			CreatedAt: time.Now().Add(-r.config.GracePeriod * 2),
		}
		orphans = append(orphans, orphan)
	}

	if len(orphans) == 0 {
		klog.V(4).Info("No orphaned disk objects found")
		return orphans
	}

	return orphans
}

func (r *OrphanReconciler) applyOrphanInventory(inventory *OrphanInventory) {
	if inventory == nil {
		return
	}

	plannedDiskDeletes := plannedTargets(inventory.PlannedMutations, OrphanMutationDeleteDisk)
	plannedFileDeletes := plannedTargets(inventory.PlannedMutations, OrphanMutationDeleteFile)

	if len(inventory.DiskOrphans) > 0 {
		klog.Warningf("Found %d orphaned disk objects", len(inventory.DiskOrphans))
	}
	for _, orphan := range inventory.DiskOrphans {
		age := time.Since(orphan.CreatedAt)

		if age < r.config.GracePeriod {
			klog.V(4).Infof("Orphaned volume %s is too young (age=%v, grace=%v), skipping",
				orphan.VolumeID, age, r.config.GracePeriod)
			continue
		}

		if !plannedDiskDeletes[orphan.VolumeID] {
			klog.Warningf("Orphaned disk object candidate observed but not yet confirmed for cleanup: %s", orphan.VolumeID)
			continue
		}

		klog.Warningf("Orphaned disk object detected: %s (path=%s, size=%d bytes, age=%v)",
			orphan.VolumeID, orphan.FilePath, orphan.SizeBytes, age)

		if r.config.DryRun {
			klog.Infof("[DRY-RUN] Would delete orphaned volume: %s", orphan.VolumeID)
			continue
		}

		// Delete the orphaned volume
		if err := r.deleteOrphanedVolume(orphan); err != nil {
			klog.Errorf("Failed to delete orphaned volume %s: %v", orphan.VolumeID, err)
			continue
		}

		klog.Infof("Successfully deleted orphaned volume: %s", orphan.VolumeID)
	}

	r.applyOrphanedFiles(inventory.FileOrphans, plannedFileDeletes)
}

// findOrphanedFiles identifies orphaned files (files without disk objects AND without PVs).
func (r *OrphanReconciler) findOrphanedFiles(rdsVolumes []rds.VolumeInfo, activeVolumeIDs map[string]bool) ([]OrphanedFile, int, error) {
	klog.V(4).Infof("Checking for orphaned files in %s", r.config.BasePath)

	// Get all files in the base path
	files, err := r.config.RDSClient.ListFiles(r.config.BasePath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list files: %w", err)
	}

	// Build a map of file paths from disk objects
	diskFilePaths := make(map[string]bool)
	for _, vol := range rdsVolumes {
		if vol.FilePath != "" {
			diskFilePaths[vol.FilePath] = true
		}
	}

	// Filter for .img files only
	orphans := []OrphanedFile{}
	for _, file := range files {
		if file.Type != "" && file.Type != "file" {
			continue
		}

		// Only check .img files
		if !strings.HasSuffix(file.Name, ".img") {
			continue
		}

		// Skip if this file has a corresponding disk object
		if diskFilePaths[file.Path] {
			klog.V(5).Infof("File %s has disk object", file.Path)
			continue
		}

		// Extract volume ID from file name (e.g., "pvc-xxx.img" -> "pvc-xxx")
		volumeID := strings.TrimSuffix(file.Name, ".img")
		if !strings.HasPrefix(volumeID, VolumeIDPrefix) {
			klog.V(5).Infof("Skipping non-CSI image file: %s (volume ID %s does not start with %q)", file.Path, volumeID, VolumeIDPrefix)
			continue
		}

		// Skip if this file is referenced by an active PV
		if activeVolumeIDs[volumeID] {
			klog.V(5).Infof("File %s is referenced by active PV %s (missing disk object)", file.Path, volumeID)
			continue
		}

		// File appears to be orphaned (no disk object AND no PV)
		orphan := OrphanedFile{
			FileName:  file.Name,
			FilePath:  file.Path,
			SizeBytes: file.SizeBytes,
			CreatedAt: file.CreatedAt,
		}
		orphans = append(orphans, orphan)
	}

	if len(orphans) == 0 {
		klog.V(4).Info("No orphaned files found")
		return orphans, len(files), nil
	}

	return orphans, len(files), nil
}

func (r *OrphanReconciler) applyOrphanedFiles(orphans []OrphanedFile, plannedDeletes map[string]bool) {
	if len(orphans) == 0 {
		return
	}

	totalSize := int64(0)
	for _, orphan := range orphans {
		totalSize += orphan.SizeBytes
	}

	// Log orphaned files
	klog.Warningf("Found %d orphaned .img files consuming %d bytes (%.2f GB)",
		len(orphans), totalSize, float64(totalSize)/(1024*1024*1024))

	for _, orphan := range orphans {
		klog.Warningf("Orphaned file detected: %s (path=%s, size=%d bytes, created=%v)",
			orphan.FileName, orphan.FilePath, orphan.SizeBytes, orphan.CreatedAt)

		if !orphanOldEnough(orphan.CreatedAt, time.Now(), r.config.GracePeriod) {
			klog.Warningf("Orphaned file %s is too young or has unknown age, skipping cleanup (created=%v, grace=%v)",
				orphan.FilePath, orphan.CreatedAt, r.config.GracePeriod)
			continue
		}

		if !plannedDeletes[orphan.FilePath] {
			klog.Warningf("Orphaned file candidate observed but not yet confirmed for cleanup: %s", orphan.FilePath)
			continue
		}

		if r.config.DryRun {
			klog.Infof("[DRY-RUN] Would delete orphaned file: %s", orphan.FilePath)
			continue
		}

		// Delete the orphaned file
		if err := r.config.RDSClient.DeleteFile(orphan.FilePath); err != nil {
			klog.Errorf("Failed to delete orphaned file %s: %v", orphan.FilePath, err)
			continue
		}

		klog.Infof("Successfully deleted orphaned file: %s", orphan.FilePath)
	}
}

func (r *OrphanReconciler) planConfirmedMutations(inventory *OrphanInventory) {
	if inventory == nil {
		return
	}

	observedAt := inventory.ObservedAt
	if observedAt.IsZero() {
		observedAt = time.Now()
	}

	r.candidatesMu.Lock()
	defer r.candidatesMu.Unlock()

	diskKeys := make(map[string]bool, len(inventory.DiskOrphans))
	for _, orphan := range inventory.DiskOrphans {
		diskKeys[orphan.VolumeID] = true
		if !orphanOldEnough(orphan.CreatedAt, observedAt, r.config.GracePeriod) {
			continue
		}

		state := r.observeCandidate(r.diskCandidates, orphan.VolumeID, observedAt)
		if state.observations >= r.config.MinimumConfirmations {
			inventory.PlannedMutations = append(inventory.PlannedMutations, OrphanMutation{
				Action: OrphanMutationDeleteDisk,
				Target: orphan.VolumeID,
				Reason: fmt.Sprintf("CSI disk object has no active PersistentVolume across %d consecutive inventory passes", state.observations),
			})
		}
	}
	pruneMissingCandidates(r.diskCandidates, diskKeys)

	fileKeys := make(map[string]bool, len(inventory.FileOrphans))
	for _, orphan := range inventory.FileOrphans {
		fileKeys[orphan.FilePath] = true
		if !orphanOldEnough(orphan.CreatedAt, observedAt, r.config.GracePeriod) {
			continue
		}

		state := r.observeCandidate(r.fileCandidates, orphan.FilePath, observedAt)
		if state.observations >= r.config.MinimumConfirmations {
			inventory.PlannedMutations = append(inventory.PlannedMutations, OrphanMutation{
				Action: OrphanMutationDeleteFile,
				Target: orphan.FilePath,
				Reason: fmt.Sprintf("CSI image file has no disk object and no active PersistentVolume across %d consecutive inventory passes", state.observations),
			})
		}
	}
	pruneMissingCandidates(r.fileCandidates, fileKeys)
}

func orphanOldEnough(createdAt, observedAt time.Time, gracePeriod time.Duration) bool {
	if createdAt.IsZero() {
		return false
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}
	return observedAt.Sub(createdAt) >= gracePeriod
}

func (r *OrphanReconciler) observeCandidate(candidates map[string]orphanCandidateState, key string, observedAt time.Time) orphanCandidateState {
	state, ok := candidates[key]
	if !ok {
		state = orphanCandidateState{
			firstSeen: observedAt,
		}
	}
	state.lastSeen = observedAt
	state.observations++
	candidates[key] = state
	return state
}

func pruneMissingCandidates(candidates map[string]orphanCandidateState, observed map[string]bool) {
	for key := range candidates {
		if !observed[key] {
			delete(candidates, key)
		}
	}
}

func plannedTargets(mutations []OrphanMutation, action OrphanMutationAction) map[string]bool {
	targets := make(map[string]bool)
	for _, mutation := range mutations {
		if mutation.Action == action {
			targets[mutation.Target] = true
		}
	}
	return targets
}

// deleteOrphanedVolume deletes an orphaned volume from RDS
func (r *OrphanReconciler) deleteOrphanedVolume(orphan OrphanedVolume) error {
	klog.V(2).Infof("Deleting orphaned volume: %s", orphan.VolumeID)

	if err := r.config.RDSClient.DeleteVolume(orphan.VolumeID); err != nil {
		return fmt.Errorf("failed to delete volume from RDS: %w", err)
	}

	return nil
}

// TriggerReconciliation triggers an immediate reconciliation (for testing/debugging)
func (r *OrphanReconciler) TriggerReconciliation(ctx context.Context) error {
	if !r.config.Enabled {
		return fmt.Errorf("reconciler is disabled")
	}
	return r.reconcile(ctx)
}

// Helper functions for safe access to ObjectReference fields
func getNamespace(ref *v1.ObjectReference) string {
	if ref == nil {
		return "<none>"
	}
	if ref.Namespace == "" {
		return "<none>"
	}
	return ref.Namespace
}

func getName(ref *v1.ObjectReference) string {
	if ref == nil {
		return "<none>"
	}
	if ref.Name == "" {
		return "<none>"
	}
	return ref.Name
}
