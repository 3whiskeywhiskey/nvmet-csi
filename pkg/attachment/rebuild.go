package attachment

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
)

const (
	// driverName is the CSI driver name used to filter PVs
	driverName = "nvmet.csi.whiskey.works"
)

// RebuildState rebuilds attachment state from VolumeAttachment objects.
// This is the main entry point for state recovery on controller startup.
func (am *AttachmentManager) RebuildState(ctx context.Context) error {
	return am.RebuildStateFromVolumeAttachments(ctx)
}

// RebuildStateFromAnnotations reconstructs state from PV annotations.
// Deprecated: Use RebuildStateFromVolumeAttachments instead.
// PV annotations are now informational-only; VolumeAttachment is the authoritative source.
// This function is kept for backward compatibility testing.
func (am *AttachmentManager) RebuildStateFromAnnotations(ctx context.Context) error {
	if am.k8sClient == nil {
		klog.V(2).Info("Skipping state rebuild (no k8s client)")
		return nil
	}

	klog.Info("Rebuilding attachment state from PersistentVolumes")

	// List all PVs in the cluster
	pvList, err := am.k8sClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}

	// Acquire write lock to rebuild state
	am.mu.Lock()
	defer am.mu.Unlock()

	// Clear existing state
	am.attachments = make(map[string]*AttachmentState)

	// Process each PV
	rebuiltCount := 0
	for _, pv := range pvList.Items {
		// Only process PVs belonging to our driver
		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != driverName {
			continue
		}

		// Extract volume ID from CSI volume handle
		volumeID := pv.Spec.CSI.VolumeHandle
		if volumeID == "" {
			klog.Warningf("PV %s has empty volume handle, skipping", pv.Name)
			continue
		}

		// Check for attachment annotation
		if pv.Annotations == nil {
			continue
		}

		nodeID, hasNode := pv.Annotations[AnnotationAttachedNode]
		if !hasNode || nodeID == "" {
			continue
		}

		// Parse timestamp if present, otherwise use current time
		attachedAt := time.Now()
		if attachedAtStr, ok := pv.Annotations[AnnotationAttachedAt]; ok {
			if parsed, err := time.Parse(metav1.RFC3339Micro, attachedAtStr); err == nil {
				attachedAt = parsed
			} else {
				klog.Warningf("Failed to parse attachment timestamp for volume %s: %v", volumeID, err)
			}
		}

		// Create and store attachment state
		state := &AttachmentState{
			VolumeID:   volumeID,
			NodeID:     nodeID,
			AttachedAt: attachedAt,
			Nodes: []NodeAttachment{
				{NodeID: nodeID, AttachedAt: attachedAt},
			},
		}
		am.attachments[volumeID] = state
		rebuiltCount++

		klog.V(2).Infof("Rebuilt attachment: volume=%s, node=%s", volumeID, nodeID)
	}

	klog.Infof("State rebuild complete: %d attachments recovered", rebuiltCount)
	return nil
}

// lookupAccessMode retrieves the access mode from a PersistentVolume.
// Returns "RWX" if any access mode contains ReadWriteMany, otherwise "RWO".
// Returns "RWO" if PV not found or on error (conservative default).
func (am *AttachmentManager) lookupAccessMode(ctx context.Context, volumeID string) string {
	if am.k8sClient == nil {
		return "RWO"
	}

	pv, err := am.k8sClient.CoreV1().PersistentVolumes().Get(ctx, volumeID, metav1.GetOptions{})
	if err != nil {
		klog.V(2).Infof("Could not look up PV %s for access mode: %v (defaulting to RWO)", volumeID, err)
		return "RWO"
	}

	// Check if any access mode is RWX
	for _, mode := range pv.Spec.AccessModes {
		if mode == corev1.ReadWriteMany {
			return "RWX"
		}
	}
	return "RWO"
}

// rebuildVolumeState reconstructs AttachmentState for a single volume from VolumeAttachments.
// Takes volumeID and slice of VolumeAttachments for that volume.
// Creates AttachmentState with Nodes populated from each VA.
// If len(vas) > 1, marks as migration (MigrationStartedAt = older VA's timestamp).
// Looks up PV to get AccessMode. Logs warning if more than 2 VAs for same volume.
func (am *AttachmentManager) rebuildVolumeState(ctx context.Context, volumeID string, vas []*storagev1.VolumeAttachment) (*AttachmentState, error) {
	if len(vas) == 0 {
		return nil, fmt.Errorf("no VolumeAttachments provided for volume %s", volumeID)
	}

	// Handle more than 2 VAs (unexpected, but be resilient)
	if len(vas) > 2 {
		klog.Warningf("Volume %s has %d VolumeAttachments (expected <=2), rebuilding first 2 only", volumeID, len(vas))
		vas = vas[:2]
	}

	// Look up access mode from PV
	accessMode := am.lookupAccessMode(ctx, volumeID)

	// Create AttachmentState with nodes from VAs
	nodes := make([]NodeAttachment, 0, len(vas))
	var firstAttachedAt time.Time

	for i, va := range vas {
		nodeID := va.Spec.NodeName
		attachedAt := va.CreationTimestamp.Time

		nodes = append(nodes, NodeAttachment{
			NodeID:     nodeID,
			AttachedAt: attachedAt,
		})

		if i == 0 || attachedAt.Before(firstAttachedAt) {
			firstAttachedAt = attachedAt
		}

		klog.V(2).Infof("Rebuilt node attachment: volume=%s, node=%s, attachedAt=%v", volumeID, nodeID, attachedAt)
	}

	state := &AttachmentState{
		VolumeID:   volumeID,
		NodeID:     nodes[0].NodeID, // Primary node for backward compat
		Nodes:      nodes,
		AttachedAt: firstAttachedAt,
		AccessMode: accessMode,
	}

	// If multiple VAs, this is migration state
	if len(vas) > 1 {
		// Find the older VA's timestamp as migration start
		var migrationStartedAt time.Time
		if vas[0].CreationTimestamp.Before(&vas[1].CreationTimestamp) {
			migrationStartedAt = vas[0].CreationTimestamp.Time
		} else {
			migrationStartedAt = vas[1].CreationTimestamp.Time
		}
		state.MigrationStartedAt = &migrationStartedAt
		klog.Infof("Detected migration state for volume %s: %d nodes, started at %v", volumeID, len(vas), migrationStartedAt)
	}

	return state, nil
}

// RebuildStateFromVolumeAttachments reconstructs the in-memory attachment state from VolumeAttachment objects.
// This is the authoritative source for attachment state (managed by external-attacher).
// VolumeAttachment objects are preferred over PV annotations because they are:
// - Managed by external-attacher (authoritative)
// - The Kubernetes attachment object that can block reattach when stale
// - Support proper dual-attach tracking for RWX
func (am *AttachmentManager) RebuildStateFromVolumeAttachments(ctx context.Context) error {
	if am.k8sClient == nil {
		klog.V(2).Info("Skipping state rebuild (no k8s client)")
		return nil
	}

	klog.Info("Rebuilding attachment state from VolumeAttachment objects")

	// Step 1: List all VolumeAttachments for our driver
	allVAs, err := ListDriverVolumeAttachments(ctx, am.k8sClient)
	if err != nil {
		return fmt.Errorf("failed to list VolumeAttachments: %w", err)
	}

	// Step 2: Filter to only attached VAs
	attachedVAs := FilterAttachedVolumeAttachments(allVAs)

	// Step 3: Group by volume ID
	vaByVolume := GroupVolumeAttachmentsByVolume(attachedVAs)

	am.mu.Lock()
	defer am.mu.Unlock()

	// Clear existing state
	am.attachments = make(map[string]*AttachmentState)

	rebuiltCount := 0
	for volumeID, vas := range vaByVolume {
		state, err := am.rebuildVolumeState(ctx, volumeID, vas)
		if err != nil {
			klog.Warningf("Failed to rebuild state for volume %s: %v", volumeID, err)
			continue
		}
		am.attachments[volumeID] = state
		rebuiltCount++
	}

	klog.Infof("State rebuild complete: %d attachments recovered from VolumeAttachment objects", rebuiltCount)
	return nil
}

// Initialize initializes the AttachmentManager by rebuilding state from VolumeAttachments.
// This should be called once during driver startup.
func (am *AttachmentManager) Initialize(ctx context.Context) error {
	klog.Info("Initializing AttachmentManager")

	// Use VolumeAttachment-based rebuild (authoritative source)
	if err := am.RebuildStateFromVolumeAttachments(ctx); err != nil {
		return fmt.Errorf("failed to rebuild state from VolumeAttachments: %w", err)
	}

	klog.Info("AttachmentManager initialized successfully")
	return nil
}
