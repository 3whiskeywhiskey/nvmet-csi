// Package attachment provides thread-safe tracking of volume-to-node attachments
// for the nvmet-csi driver.
package attachment

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	storagev1listers "k8s.io/client-go/listers/storage/v1"
	"k8s.io/klog/v2"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/observability"
)

// EventPoster is an interface for posting Kubernetes events for attachment lifecycle.
// This interface allows the reconciler to post events without creating a circular
// dependency with the driver package.
type EventPoster interface {
	// PostStaleAttachmentCleared posts an event when a stale attachment is cleared
	PostStaleAttachmentCleared(ctx context.Context, pvcNamespace, pvcName, volumeID, staleNodeID string) error
}

// AttachmentReconciler periodically checks for stale attachments and cleans them up.
// Stale attachments occur when a node is deleted without proper cleanup of attached volumes.
type AttachmentReconciler struct {
	manager                       *AttachmentManager
	k8sClient                     kubernetes.Interface
	nodeLister                    corev1listers.NodeLister             // Cached node lister (avoids API calls)
	pvLister                      corev1listers.PersistentVolumeLister // Cached PV lister (avoids API calls)
	volumeAttachmentLister        storagev1listers.VolumeAttachmentLister
	interval                      time.Duration
	gracePeriod                   time.Duration
	staleVolumeAttachmentCleanup  bool
	staleVolumeAttachmentDelete   bool
	staleVolumeAttachmentMinConfs int
	staleVolumeAttachmentSeen     map[string]staleVolumeAttachmentCandidate
	metrics                       *observability.Metrics
	eventPoster                   EventPoster // Optional, may be nil

	// Control channels
	stopCh    chan struct{}
	doneCh    chan struct{}
	triggerCh chan struct{} // Buffered channel for event-driven reconciliation triggers
	mu        sync.Mutex
}

// ReconcilerConfig holds configuration for the AttachmentReconciler.
type ReconcilerConfig struct {
	Manager                       *AttachmentManager
	K8sClient                     kubernetes.Interface
	NodeLister                    corev1listers.NodeLister             // Required: cached node lister to avoid API throttling
	PVLister                      corev1listers.PersistentVolumeLister // Required: cached PV lister to avoid API throttling
	VolumeAttachmentLister        storagev1listers.VolumeAttachmentLister
	Interval                      time.Duration // Default: 5 minutes
	GracePeriod                   time.Duration // Default: 30 seconds
	StaleVolumeAttachmentCleanup  bool
	StaleVolumeAttachmentDelete   bool
	StaleVolumeAttachmentMinConfs int
	Metrics                       *observability.Metrics
	EventPoster                   EventPoster // Optional, may be nil - for posting lifecycle events
}

// NewAttachmentReconciler creates a new AttachmentReconciler.
func NewAttachmentReconciler(config ReconcilerConfig) (*AttachmentReconciler, error) {
	if config.Manager == nil {
		return nil, fmt.Errorf("manager is required")
	}
	if config.K8sClient == nil {
		return nil, fmt.Errorf("k8sClient is required")
	}
	if config.NodeLister == nil {
		return nil, fmt.Errorf("nodeLister is required (use informer to avoid API throttling)")
	}
	if config.PVLister == nil {
		return nil, fmt.Errorf("pvLister is required (use informer to avoid API throttling)")
	}
	if config.StaleVolumeAttachmentCleanup && config.VolumeAttachmentLister == nil {
		return nil, fmt.Errorf("volumeAttachmentLister is required when stale VolumeAttachment cleanup is enabled")
	}
	if config.Interval <= 0 {
		config.Interval = 5 * time.Minute
	}
	if config.GracePeriod <= 0 {
		config.GracePeriod = 30 * time.Second
	}
	if config.StaleVolumeAttachmentMinConfs <= 0 {
		config.StaleVolumeAttachmentMinConfs = 2
	}

	return &AttachmentReconciler{
		manager:                       config.Manager,
		k8sClient:                     config.K8sClient,
		nodeLister:                    config.NodeLister,
		pvLister:                      config.PVLister,
		volumeAttachmentLister:        config.VolumeAttachmentLister,
		interval:                      config.Interval,
		gracePeriod:                   config.GracePeriod,
		staleVolumeAttachmentCleanup:  config.StaleVolumeAttachmentCleanup,
		staleVolumeAttachmentDelete:   config.StaleVolumeAttachmentDelete,
		staleVolumeAttachmentMinConfs: config.StaleVolumeAttachmentMinConfs,
		staleVolumeAttachmentSeen:     make(map[string]staleVolumeAttachmentCandidate),
		metrics:                       config.Metrics,
		eventPoster:                   config.EventPoster,
		triggerCh:                     make(chan struct{}, 1), // Buffered size 1 for deduplication
	}, nil
}

// Start begins the background reconciliation loop.
// Returns immediately; reconciliation runs in a separate goroutine.
// Call Stop() to gracefully shut down.
func (r *AttachmentReconciler) Start(ctx context.Context) error {
	r.mu.Lock()
	if r.stopCh != nil {
		r.mu.Unlock()
		return fmt.Errorf("reconciler already running")
	}
	r.stopCh = make(chan struct{})
	r.doneCh = make(chan struct{})
	r.mu.Unlock()

	klog.Infof("Starting attachment reconciler (interval=%v, grace_period=%v, stale_volumeattachment_cleanup=%t, stale_volumeattachment_delete=%t, stale_volumeattachment_min_confirmations=%d)",
		r.interval, r.gracePeriod, r.staleVolumeAttachmentCleanup, r.staleVolumeAttachmentDelete, r.staleVolumeAttachmentMinConfs)

	go r.run(ctx)

	return nil
}

// Stop gracefully stops the reconciliation loop.
// Blocks until the reconciler has fully stopped.
func (r *AttachmentReconciler) Stop() {
	r.mu.Lock()
	if r.stopCh == nil {
		r.mu.Unlock()
		return
	}
	close(r.stopCh)
	doneCh := r.doneCh
	// Clear channels so subsequent Stop() calls are no-op
	r.stopCh = nil
	r.doneCh = nil
	r.triggerCh = nil // Prevent TriggerReconcile from using closed reconciler
	r.mu.Unlock()

	// Wait for run() to exit
	<-doneCh

	klog.Info("Attachment reconciler stopped")
}

// TriggerReconcile triggers an immediate reconciliation pass.
// If a reconciliation trigger is already pending, this is a no-op (deduplication).
// This method is safe to call from multiple goroutines and safe to call when reconciler is not running.
func (r *AttachmentReconciler) TriggerReconcile() {
	r.mu.Lock()
	triggerCh := r.triggerCh
	r.mu.Unlock()

	if triggerCh == nil {
		// Reconciler not initialized yet or already stopped - safe no-op
		klog.V(2).Info("TriggerReconcile called but reconciler not running (no-op)")
		return
	}

	// Non-blocking send - if channel is full, a trigger is already pending
	select {
	case triggerCh <- struct{}{}:
		klog.V(2).Info("Attachment reconciliation triggered by event")
	default:
		klog.V(4).Info("Attachment reconciliation trigger already pending (deduplicated)")
	}
}

// run is the main reconciliation loop.
func (r *AttachmentReconciler) run(ctx context.Context) {
	// Capture channels as local variables to avoid race with Stop()
	r.mu.Lock()
	stopCh := r.stopCh
	doneCh := r.doneCh
	triggerCh := r.triggerCh
	r.mu.Unlock()

	defer close(doneCh)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	// Run initial reconciliation immediately
	r.reconcile(ctx)

	for {
		// Priority check: stop signals take precedence over work
		select {
		case <-stopCh:
			klog.V(2).Info("Attachment reconciler shutting down")
			return
		case <-ctx.Done():
			klog.V(2).Info("Attachment reconciler context cancelled")
			return
		default:
		}

		// Wait for work or stop signal
		select {
		case <-ticker.C:
			klog.V(2).Info("Attachment reconciliation triggered by periodic timer")
			r.reconcile(ctx)
		case <-triggerCh:
			klog.V(2).Info("Attachment reconciliation triggered by node event")
			r.reconcile(ctx)
		case <-stopCh:
			klog.V(2).Info("Attachment reconciler shutting down")
			return
		case <-ctx.Done():
			klog.V(2).Info("Attachment reconciler context cancelled")
			return
		}
	}
}

// reconcile performs a single reconciliation pass.
func (r *AttachmentReconciler) reconcile(ctx context.Context) {
	startTime := time.Now()
	klog.V(4).Info("Starting attachment reconciliation")

	// Get all current attachments
	attachments := r.manager.ListAttachments()

	clearedCount := 0
	for volumeID, state := range attachments {
		// Check if context is cancelled
		if ctx.Err() != nil {
			klog.V(2).Info("Reconciliation interrupted by context cancellation")
			return
		}

		// Check if node still exists
		nodeExists, err := r.nodeExists(ctx, state.NodeID)
		if err != nil {
			// API error - fail open (don't clear on transient errors)
			klog.Warningf("Failed to check node %s for volume %s: %v (skipping)", state.NodeID, volumeID, err)
			continue
		}

		if nodeExists {
			// Node exists, attachment is valid
			continue
		}

		// Node deleted - check if within grace period
		detachTime := r.manager.GetDetachTimestamp(volumeID)
		if !detachTime.IsZero() && time.Since(detachTime) < r.gracePeriod {
			klog.V(4).Infof("Node %s deleted but within grace period for volume %s", state.NodeID, volumeID)
			continue
		}

		// Clear stale attachment
		staleNodeID := state.NodeID // Capture before clearing
		klog.Infof("Clearing stale attachment: volume=%s node=%s (node deleted)", volumeID, staleNodeID)
		if err := r.manager.UntrackAttachment(ctx, volumeID); err != nil {
			klog.Errorf("Failed to clear stale attachment for volume %s: %v", volumeID, err)
			continue
		}

		clearedCount++

		// Record metrics
		if r.metrics != nil {
			r.metrics.RecordStaleAttachmentCleared()
			r.metrics.RecordReconcileAction("clear_stale")
		}

		// Post event (best effort - don't fail reconciliation if event posting fails)
		r.postStaleAttachmentClearedEvent(ctx, volumeID, staleNodeID)
	}

	duration := time.Since(startTime)

	// Record reconcile duration
	if r.metrics != nil {
		r.metrics.RecordAttachmentOp("reconcile", nil, duration)
	}

	if clearedCount > 0 {
		klog.Infof("Attachment reconciliation complete: cleared %d stale attachments (duration=%v)", clearedCount, duration)
	} else {
		klog.V(4).Infof("Attachment reconciliation complete: no stale attachments (duration=%v)", duration)
	}

	if r.staleVolumeAttachmentCleanup {
		r.reconcileStaleVolumeAttachments(ctx)
	}
}

// nodeExists checks if a Kubernetes node exists using the cached node lister.
// This avoids direct API calls and prevents throttling during reconciliation.
func (r *AttachmentReconciler) nodeExists(ctx context.Context, nodeID string) (bool, error) {
	// Use cached lister instead of API call - this is the key fix for API throttling!
	_, err := r.nodeLister.Get(nodeID)
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetGracePeriod returns the configured grace period duration.
func (r *AttachmentReconciler) GetGracePeriod() time.Duration {
	return r.gracePeriod
}

// postStaleAttachmentClearedEvent posts an event when a stale attachment is cleared.
// This is a best-effort operation - failures are logged but don't affect reconciliation.
func (r *AttachmentReconciler) postStaleAttachmentClearedEvent(ctx context.Context, volumeID, staleNodeID string) {
	if r.eventPoster == nil {
		return
	}

	// Look up the PV to get the bound PVC information
	// Volume ID is typically the PV name (e.g., pvc-<uuid>)
	// Use cached lister instead of API call to avoid throttling
	pv, err := r.pvLister.Get(volumeID)
	if err != nil {
		klog.V(4).Infof("Cannot get PV %s for stale attachment event: %v", volumeID, err)
		return
	}

	claimRef := pv.Spec.ClaimRef
	if claimRef == nil {
		klog.V(4).Infof("PV %s has no claimRef for stale attachment event", volumeID)
		return
	}

	if err := r.eventPoster.PostStaleAttachmentCleared(ctx, claimRef.Namespace, claimRef.Name, volumeID, staleNodeID); err != nil {
		klog.Warningf("Failed to post stale attachment cleared event for volume %s: %v", volumeID, err)
	}
}

type staleVolumeAttachmentCandidate struct {
	name          string
	volumeID      string
	nodeID        string
	confirmations int
	firstSeen     time.Time
	lastSeen      time.Time
	reason        string
}

func (r *AttachmentReconciler) reconcileStaleVolumeAttachments(ctx context.Context) {
	startTime := time.Now()

	if r.volumeAttachmentLister == nil {
		klog.Warning("Stale VolumeAttachment cleanup enabled without a VolumeAttachment lister; skipping")
		return
	}

	attachments, err := r.volumeAttachmentLister.List(labels.Everything())
	if err != nil {
		klog.Warningf("Failed to list VolumeAttachments from cache: %v", err)
		return
	}

	now := time.Now()
	observed := make(map[string]staleVolumeAttachmentCandidate)
	planned := make([]staleVolumeAttachmentCandidate, 0)

	for _, va := range attachments {
		if ctx.Err() != nil {
			klog.V(2).Info("Stale VolumeAttachment reconciliation interrupted by context cancellation")
			return
		}
		if va.Spec.Attacher != driverName {
			continue
		}
		if va.DeletionTimestamp != nil {
			continue
		}
		if va.Spec.Source.PersistentVolumeName == nil || *va.Spec.Source.PersistentVolumeName == "" {
			klog.V(4).Infof("Skipping VolumeAttachment %s with empty PersistentVolumeName", va.Name)
			continue
		}
		if va.Spec.NodeName == "" {
			klog.V(4).Infof("Skipping VolumeAttachment %s with empty node name", va.Name)
			continue
		}

		volumeID := *va.Spec.Source.PersistentVolumeName
		if _, err := r.pvLister.Get(volumeID); err != nil {
			if errors.IsNotFound(err) {
				klog.V(4).Infof("Skipping stale VolumeAttachment candidate %s: PV %s no longer exists", va.Name, volumeID)
				continue
			}
			klog.Warningf("Failed to check PV %s for VolumeAttachment %s: %v (skipping)", volumeID, va.Name, err)
			continue
		}

		nodeExists, err := r.nodeExists(ctx, va.Spec.NodeName)
		if err != nil {
			klog.Warningf("Failed to check node %s for VolumeAttachment %s: %v (skipping)", va.Spec.NodeName, va.Name, err)
			continue
		}
		if nodeExists {
			continue
		}

		if age := now.Sub(va.CreationTimestamp.Time); age < r.gracePeriod {
			klog.V(4).Infof("Skipping VolumeAttachment %s for deleted node %s: age %v is within grace period %v", va.Name, va.Spec.NodeName, age, r.gracePeriod)
			continue
		}

		candidate := staleVolumeAttachmentCandidate{
			name:     va.Name,
			volumeID: volumeID,
			nodeID:   va.Spec.NodeName,
			lastSeen: now,
			reason:   "VolumeAttachment points at a node that no longer exists",
		}
		if previous, ok := r.staleVolumeAttachmentSeen[va.Name]; ok {
			candidate.confirmations = previous.confirmations + 1
			candidate.firstSeen = previous.firstSeen
		} else {
			candidate.confirmations = 1
			candidate.firstSeen = now
		}
		observed[va.Name] = candidate
		if r.metrics != nil {
			r.metrics.RecordStaleVolumeAttachmentCandidate(!r.staleVolumeAttachmentDelete)
		}
		if candidate.confirmations >= r.staleVolumeAttachmentMinConfs {
			if r.metrics != nil {
				r.metrics.RecordStaleVolumeAttachmentPlanned(!r.staleVolumeAttachmentDelete)
			}
			planned = append(planned, candidate)
		}
	}

	r.staleVolumeAttachmentSeen = observed

	for _, candidate := range planned {
		if !r.staleVolumeAttachmentDelete {
			klog.Infof("[DRY-RUN] Would delete stale VolumeAttachment: name=%s volume=%s node=%s confirmations=%d reason=%q",
				candidate.name, candidate.volumeID, candidate.nodeID, candidate.confirmations, candidate.reason)
			continue
		}

		klog.Infof("Deleting stale VolumeAttachment: name=%s volume=%s node=%s confirmations=%d reason=%q",
			candidate.name, candidate.volumeID, candidate.nodeID, candidate.confirmations, candidate.reason)
		if r.metrics != nil {
			r.metrics.RecordStaleVolumeAttachmentDeleteAttempt()
		}
		if err := r.k8sClient.StorageV1().VolumeAttachments().Delete(ctx, candidate.name, metav1.DeleteOptions{}); err != nil {
			if errors.IsNotFound(err) {
				if r.metrics != nil {
					r.metrics.RecordStaleVolumeAttachmentDeleteNotFound()
				}
				continue
			}
			klog.Errorf("Failed to delete stale VolumeAttachment %s for volume %s: %v", candidate.name, candidate.volumeID, err)
			if r.metrics != nil {
				r.metrics.RecordStaleVolumeAttachmentDeleteFailure()
			}
			continue
		}
		if r.metrics != nil {
			r.metrics.RecordStaleVolumeAttachmentDeleteSuccess()
			r.metrics.RecordReconcileAction("delete_stale_volumeattachment")
		}
	}

	klog.V(2).Infof("Stale VolumeAttachment reconciliation complete (duration=%v, candidates=%d, planned_deletes=%d, dry_run=%t)",
		time.Since(startTime), len(observed), len(planned), !r.staleVolumeAttachmentDelete)
}
