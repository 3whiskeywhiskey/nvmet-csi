// persist.go handles PV annotation persistence for attachment state.
//
// IMPORTANT: These annotations are INFORMATIONAL ONLY for debugging/observability.
// They are written during ControllerPublishVolume but NEVER read during state rebuild.
// VolumeAttachment objects are the authoritative source of truth for attachment state.
//
// Why write-only annotations?
// - Backward compatibility: kubectl describe pv shows attachment info
// - Debugging: Operators can see which node a volume is attached to
// - Observability: External tools may read annotations for dashboards
//
// Why NOT read during rebuild?
// - Annotations can become stale (clearing may fail, manual kubectl edits)
// - VolumeAttachment objects are managed by external-attacher (authoritative)
// - Reading annotations would contradict VolumeAttachment state
package attachment

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
)

const (
	// AnnotationAttachedNode stores the node ID for debugging/observability.
	// Informational only - never read during state rebuild.
	AnnotationAttachedNode = "nvmet.csi.whiskey.works/attached-node"

	// AnnotationAttachedAt stores the attachment timestamp for debugging.
	// Informational only - never read during state rebuild.
	AnnotationAttachedAt = "nvmet.csi.whiskey.works/attached-at"
)

// persistAttachment writes attachment metadata to PV annotations for debugging.
// These annotations are INFORMATIONAL ONLY - they are never read during state rebuild.
// VolumeAttachment objects are the authoritative source of truth.
// Uses retry.RetryOnConflict to handle concurrent updates safely.
// Returns nil if k8sClient is nil (allows operation without k8s in tests).
func (am *AttachmentManager) persistAttachment(ctx context.Context, volumeID, nodeID string) error {
	if am.k8sClient == nil {
		klog.V(2).Infof("Skipping persistence (no k8s client): volume=%s, node=%s", volumeID, nodeID)
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the current PV
		pv, err := am.k8sClient.CoreV1().PersistentVolumes().Get(ctx, volumeID, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// Ensure annotations map exists
		if pv.Annotations == nil {
			pv.Annotations = make(map[string]string)
		}

		// Update annotations
		pv.Annotations[AnnotationAttachedNode] = nodeID
		pv.Annotations[AnnotationAttachedAt] = metav1.Now().Format(metav1.RFC3339Micro)

		// Update the PV
		_, err = am.k8sClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
		return err
	})

	if err != nil {
		// Handle "not found" gracefully - PV may be created later
		if err.Error() == "not found" || isNotFoundError(err) {
			klog.Warningf("PV not found for volume %s, skipping persistence (may be created later)", volumeID)
			return nil
		}
		return fmt.Errorf("failed to persist attachment annotation: %w", err)
	}

	klog.V(2).Infof("Persisted attachment: volume=%s, node=%s", volumeID, nodeID)
	return nil
}

// clearAttachment removes attachment annotations from a PV.
// This is called when a volume is fully detached to keep annotations accurate.
// Note: Even if clearing fails, behavior is correct because annotations are
// never read during rebuild - VolumeAttachment absence is authoritative.
// Uses retry.RetryOnConflict to handle concurrent updates safely.
// Returns nil if k8sClient is nil (allows operation without k8s in tests).
func (am *AttachmentManager) clearAttachment(ctx context.Context, volumeID string) error {
	if am.k8sClient == nil {
		klog.V(2).Infof("Skipping persistence clear (no k8s client): volume=%s", volumeID)
		return nil
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Get the current PV
		pv, err := am.k8sClient.CoreV1().PersistentVolumes().Get(ctx, volumeID, metav1.GetOptions{})
		if err != nil {
			return err
		}

		// Remove annotations if they exist
		if pv.Annotations != nil {
			delete(pv.Annotations, AnnotationAttachedNode)
			delete(pv.Annotations, AnnotationAttachedAt)
		}

		// Update the PV
		_, err = am.k8sClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
		return err
	})

	if err != nil {
		// Handle "not found" gracefully
		if err.Error() == "not found" || isNotFoundError(err) {
			klog.Warningf("PV not found for volume %s, skipping clear (already deleted?)", volumeID)
			return nil
		}
		return fmt.Errorf("failed to clear attachment annotation: %w", err)
	}

	klog.V(2).Infof("Cleared attachment annotation: volume=%s", volumeID)
	return nil
}

// isNotFoundError checks if an error is a Kubernetes "not found" error
func isNotFoundError(err error) bool {
	// Check if error message contains "not found"
	return err != nil && (err.Error() == "not found" ||
		containsSubstring(err.Error(), "not found"))
}

// containsSubstring is a simple substring check helper
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		(len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr ||
			indexOf(s, substr) >= 0)))
}

// indexOf returns the index of substr in s, or -1 if not found
func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
