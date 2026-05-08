// Package attachment provides thread-safe tracking of volume-to-node attachments
// for the nvmet-csi driver. It uses in-memory state with RWMutex for concurrent access
// and per-volume locks to serialize operations on individual volumes.
package attachment

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/observability"
)

// AttachmentManager tracks which volumes are attached to which nodes
// and provides thread-safe operations for managing attachment state.
type AttachmentManager struct {
	// mu protects the attachments map for concurrent access
	mu sync.RWMutex

	// attachments maps volumeID to attachment state
	attachments map[string]*AttachmentState

	// detachTimestamps tracks last detach time per volume for grace period calculation
	detachTimestamps map[string]time.Time

	// volumeLocks provides per-volume operation locking
	volumeLocks *VolumeLockManager

	// k8sClient is used for future PV annotation updates (can be nil initially)
	k8sClient kubernetes.Interface

	// metrics for recording migration operations (optional, can be nil)
	metrics *observability.Metrics
}

// NewAttachmentManager creates a new AttachmentManager
func NewAttachmentManager(k8sClient kubernetes.Interface) *AttachmentManager {
	return &AttachmentManager{
		attachments:      make(map[string]*AttachmentState),
		detachTimestamps: make(map[string]time.Time),
		volumeLocks:      NewVolumeLockManager(),
		k8sClient:        k8sClient,
	}
}

// TrackAttachment records that a volume is attached to a node.
// This method is idempotent - if the volume is already attached to the same node,
// it returns nil. If the volume is attached to a different node, it returns an error.
// For RWX dual-attach, use TrackAttachmentWithMode or AddSecondaryAttachment instead.
func (am *AttachmentManager) TrackAttachment(ctx context.Context, volumeID, nodeID string) error {
	// Call TrackAttachmentWithMode with default "RWO" for backward compatibility
	return am.TrackAttachmentWithMode(ctx, volumeID, nodeID, "RWO")
}

// TrackAttachmentWithMode records that a volume is attached to a node with access mode awareness.
// accessMode should be "RWO" or "RWX" to determine if dual-attach is allowed later.
func (am *AttachmentManager) TrackAttachmentWithMode(ctx context.Context, volumeID, nodeID, accessMode string) error {
	am.volumeLocks.Lock(volumeID)
	defer am.volumeLocks.Unlock(volumeID)

	am.mu.RLock()
	existing, exists := am.attachments[volumeID]
	am.mu.RUnlock()

	if exists {
		// Check if already attached to this node (idempotent)
		if existing.IsAttachedToNode(nodeID) {
			klog.V(2).Infof("Volume %s already attached to node %s (idempotent)", volumeID, nodeID)
			return nil
		}

		// Different node - caller must handle via AddSecondaryAttachment for RWX
		return fmt.Errorf("volume %s already attached to node %s", volumeID, existing.NodeID)
	}

	// Create new attachment state with first node
	now := time.Now()
	state := &AttachmentState{
		VolumeID: volumeID,
		NodeID:   nodeID, // Keep for backward compat
		Nodes: []NodeAttachment{
			{NodeID: nodeID, AttachedAt: now},
		},
		AttachedAt: now,
		AccessMode: accessMode,
	}

	am.mu.Lock()
	am.attachments[volumeID] = state
	am.mu.Unlock()

	klog.V(2).Infof("Tracked attachment: volume=%s, node=%s, accessMode=%s (primary)", volumeID, nodeID, accessMode)

	// Persist to PV annotations for debugging/observability (informational only)
	// Note: If persistence fails, we rollback in-memory state because the
	// annotation write is part of the operation contract, even though
	// annotations are not authoritative.
	if err := am.persistAttachment(ctx, volumeID, nodeID); err != nil {
		am.mu.Lock()
		delete(am.attachments, volumeID)
		am.mu.Unlock()
		return fmt.Errorf("failed to persist attachment: %w", err)
	}

	return nil
}

// AddSecondaryAttachment adds a second node attachment for RWX volumes during migration.
// Records migration start time for timeout tracking.
// Returns error if volume not attached, not RWX, or already has 2 nodes.
func (am *AttachmentManager) AddSecondaryAttachment(ctx context.Context, volumeID, nodeID string, migrationTimeout time.Duration) error {
	am.volumeLocks.Lock(volumeID)
	defer am.volumeLocks.Unlock(volumeID)

	am.mu.Lock()
	defer am.mu.Unlock()

	existing, exists := am.attachments[volumeID]
	if !exists {
		return fmt.Errorf("volume %s not attached", volumeID)
	}

	// Check if already attached to this node (idempotent)
	if existing.IsAttachedToNode(nodeID) {
		klog.V(2).Infof("Volume %s already attached to node %s (idempotent)", volumeID, nodeID)
		return nil
	}

	// ROADMAP-5: Enforce 2-node limit
	if len(existing.Nodes) >= 2 {
		return fmt.Errorf("volume %s already attached to 2 nodes (migration limit)", volumeID)
	}

	// Add secondary attachment
	existing.Nodes = append(existing.Nodes, NodeAttachment{
		NodeID:     nodeID,
		AttachedAt: time.Now(),
	})

	// Track migration start time for timeout enforcement
	now := time.Now()
	existing.MigrationStartedAt = &now
	existing.MigrationTimeout = migrationTimeout

	// Record metric: migration started
	if am.metrics != nil {
		am.metrics.RecordMigrationStarted()
	}

	klog.V(2).Infof("Tracked secondary attachment: volume=%s, node=%s, timeout=%v (migration target)",
		volumeID, nodeID, migrationTimeout)
	return nil
}

// UntrackAttachment removes the attachment record for a volume.
// This method is idempotent - if the volume is not tracked, it returns nil.
func (am *AttachmentManager) UntrackAttachment(ctx context.Context, volumeID string) error {
	// Acquire per-volume lock to serialize operations on this volume
	am.volumeLocks.Lock(volumeID)
	defer am.volumeLocks.Unlock(volumeID)

	// Check if exists before deleting
	am.mu.RLock()
	_, exists := am.attachments[volumeID]
	am.mu.RUnlock()

	if !exists {
		klog.V(2).Infof("Volume %s not tracked, nothing to untrack (idempotent)", volumeID)
		return nil
	}

	// Delete under write lock and record detach timestamp
	am.mu.Lock()
	// Record detach timestamp for grace period tracking
	am.detachTimestamps[volumeID] = time.Now()
	delete(am.attachments, volumeID)
	am.mu.Unlock()

	klog.V(2).Infof("Untracked attachment: volume=%s", volumeID)

	// Clear PV annotations (informational only, outside of lock - I/O operation)
	// Log warning if it fails but don't fail the operation
	// (VolumeAttachment is source of truth during rebuild, not annotations)
	if err := am.clearAttachment(ctx, volumeID); err != nil {
		klog.Warningf("Failed to clear attachment annotation for volume %s: %v", volumeID, err)
	}

	return nil
}

// GetAttachment retrieves the attachment state for a volume.
// Returns the state and a boolean indicating if the volume is tracked.
// This is a read-only operation and does not block other readers.
func (am *AttachmentManager) GetAttachment(volumeID string) (*AttachmentState, bool) {
	am.mu.RLock()
	defer am.mu.RUnlock()

	state, exists := am.attachments[volumeID]
	return state, exists
}

// ListAttachments returns a copy of all current attachments.
// The returned map is a copy to prevent external mutation of internal state.
func (am *AttachmentManager) ListAttachments() map[string]*AttachmentState {
	am.mu.RLock()
	defer am.mu.RUnlock()

	// Create a copy to prevent external mutation
	copy := make(map[string]*AttachmentState, len(am.attachments))
	for volumeID, state := range am.attachments {
		copy[volumeID] = state
	}

	return copy
}

// IsWithinGracePeriod checks if a volume was recently detached and is within grace period.
// This allows live migration handoff by preventing false conflicts.
// Returns true if volume was detached less than gracePeriod ago.
func (am *AttachmentManager) IsWithinGracePeriod(volumeID string, gracePeriod time.Duration) bool {
	am.mu.RLock()
	defer am.mu.RUnlock()

	detachTime, exists := am.detachTimestamps[volumeID]
	if !exists {
		return false
	}

	return time.Since(detachTime) < gracePeriod
}

// GetDetachTimestamp returns the last detach timestamp for a volume.
// Returns zero time if volume was never detached.
func (am *AttachmentManager) GetDetachTimestamp(volumeID string) time.Time {
	am.mu.RLock()
	defer am.mu.RUnlock()

	return am.detachTimestamps[volumeID]
}

// ClearDetachTimestamp removes the detach timestamp for a volume.
// Called after successful reattachment.
func (am *AttachmentManager) ClearDetachTimestamp(volumeID string) {
	am.mu.Lock()
	defer am.mu.Unlock()

	delete(am.detachTimestamps, volumeID)
}

// GetNodeCount returns the number of nodes a volume is attached to.
func (am *AttachmentManager) GetNodeCount(volumeID string) int {
	am.mu.RLock()
	defer am.mu.RUnlock()

	if state, exists := am.attachments[volumeID]; exists {
		return state.NodeCount()
	}
	return 0
}

// IsAttachedToNode checks if volume is attached to a specific node.
func (am *AttachmentManager) IsAttachedToNode(volumeID, nodeID string) bool {
	am.mu.RLock()
	defer am.mu.RUnlock()

	if state, exists := am.attachments[volumeID]; exists {
		return state.IsAttachedToNode(nodeID)
	}
	return false
}

// GetAccessMode returns the access mode for a tracked volume.
func (am *AttachmentManager) GetAccessMode(volumeID string) string {
	am.mu.RLock()
	defer am.mu.RUnlock()

	if state, exists := am.attachments[volumeID]; exists {
		return state.AccessMode
	}
	return ""
}

// ClearMigrationState clears migration tracking fields.
// Called when source node detaches, completing migration.
func (am *AttachmentManager) ClearMigrationState(volumeID string) {
	am.mu.Lock()
	defer am.mu.Unlock()

	if state, exists := am.attachments[volumeID]; exists {
		state.MigrationStartedAt = nil
		state.MigrationTimeout = 0
	}
}

// SetMetrics sets the Prometheus metrics for recording migration operations.
func (am *AttachmentManager) SetMetrics(m *observability.Metrics) {
	am.metrics = m
}

// RemoveNodeAttachment removes a specific node's attachment from a volume.
// For RWX during migration, this removes one node while keeping the other.
// Returns true if this was the last node (volume now fully detached).
func (am *AttachmentManager) RemoveNodeAttachment(ctx context.Context, volumeID, nodeID string) (bool, error) {
	am.volumeLocks.Lock(volumeID)
	defer am.volumeLocks.Unlock(volumeID)

	am.mu.Lock()
	defer am.mu.Unlock()

	existing, exists := am.attachments[volumeID]
	if !exists {
		klog.V(2).Infof("Volume %s not tracked, nothing to remove (idempotent)", volumeID)
		return false, nil
	}

	// Capture migration state before potentially clearing it
	wasMigrating := existing.MigrationStartedAt != nil
	var migrationStartedAt time.Time
	if wasMigrating {
		migrationStartedAt = *existing.MigrationStartedAt
	}

	// Find and remove the node
	newNodes := make([]NodeAttachment, 0, len(existing.Nodes))
	found := false
	for _, na := range existing.Nodes {
		if na.NodeID == nodeID {
			found = true
			continue // Skip this node
		}
		newNodes = append(newNodes, na)
	}

	if !found {
		klog.V(2).Infof("Volume %s not attached to node %s (idempotent)", volumeID, nodeID)
		return false, nil
	}

	if len(newNodes) == 0 {
		// Last node removed - fully detach
		am.detachTimestamps[volumeID] = time.Now()
		delete(am.attachments, volumeID)
		klog.V(2).Infof("Removed last node attachment for volume %s, volume now detached", volumeID)

		// Clear PV annotations to keep them accurate for debugging
		// Note: Even if this fails, rebuild uses VolumeAttachments not annotations
		if err := am.clearAttachment(ctx, volumeID); err != nil {
			klog.Warningf("Failed to clear attachment annotations for volume %s: %v", volumeID, err)
			// Continue anyway - in-memory state is already cleared
		}

		return true, nil
	}

	// If removing primary node (migration source), clear migration state
	// Down to 1 node - migration completed, clear migration state
	if found && len(newNodes) == 1 {
		existing.MigrationStartedAt = nil
		existing.MigrationTimeout = 0
		klog.V(2).Infof("Migration completed for volume %s, cleared migration state", volumeID)

		// If this was a migration completion (was migrating, now down to 1 node)
		if wasMigrating {
			duration := time.Since(migrationStartedAt)
			if am.metrics != nil {
				am.metrics.RecordMigrationResult("success", duration)
			}
		}
	}

	// Update with remaining nodes
	existing.Nodes = newNodes
	existing.NodeID = newNodes[0].NodeID // Update primary for backward compat
	klog.V(2).Infof("Removed node %s from volume %s, %d node(s) remaining", nodeID, volumeID, len(newNodes))
	return false, nil
}
