package driver

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/observability"
)

// Event reasons - use consistent naming for filtering
const (
	EventReasonMountFailure       = "MountFailure"
	EventReasonRecoveryFailed     = "RecoveryFailed"
	EventReasonStaleMountDetected = "StaleMountDetected"

	// Connection lifecycle events
	EventReasonConnectionFailure  = "ConnectionFailure"
	EventReasonConnectionRecovery = "ConnectionRecovery"

	// Orphan cleanup events
	EventReasonOrphanDetected = "OrphanDetected"
	EventReasonOrphanCleaned  = "OrphanCleaned"

	// Attachment conflict events
	EventReasonAttachmentConflict = "AttachmentConflict"

	// Attachment lifecycle events
	EventReasonVolumeAttached         = "VolumeAttached"
	EventReasonVolumeDetached         = "VolumeDetached"
	EventReasonStaleAttachmentCleared = "StaleAttachmentCleared"

	// Migration lifecycle events
	EventReasonMigrationStarted   = "MigrationStarted"
	EventReasonMigrationCompleted = "MigrationCompleted"
	EventReasonMigrationFailed    = "MigrationFailed"

	// RDS connection state events
	EventReasonRDSDisconnected       = "RDSDisconnected"
	EventReasonRDSReconnected        = "RDSReconnected"
	EventReasonStartupReconciliation = "StartupReconciliation"
)

// EventPoster posts Kubernetes events for mount operations
type EventPoster struct {
	recorder  record.EventRecorder
	clientset kubernetes.Interface
	metrics   *observability.Metrics
}

// SetMetrics sets the Prometheus metrics for recording event posting
func (ep *EventPoster) SetMetrics(m *observability.Metrics) {
	ep.metrics = m
}

// eventSinkAdapter adapts the EventInterface to record.EventSink
// record.EventSink has methods without context, but EventInterface requires context
// This adapter extracts the namespace from the event object to support multi-namespace events
type eventSinkAdapter struct {
	clientset kubernetes.Interface
}

func (a *eventSinkAdapter) Create(event *corev1.Event) (*corev1.Event, error) {
	// Extract namespace from event's ObjectMeta
	// Events must be created in the same namespace as their InvolvedObject
	namespace := event.Namespace
	if namespace == "" {
		// Fallback to InvolvedObject namespace if ObjectMeta.Namespace not set
		namespace = event.InvolvedObject.Namespace
		if namespace == "" {
			// Default to "default" namespace if both are empty (shouldn't happen)
			namespace = "default"
			klog.Warningf("Event has no namespace, defaulting to 'default': %+v", event)
		}
		// Set the namespace in ObjectMeta for consistency
		event.Namespace = namespace
	}
	return a.clientset.CoreV1().Events(namespace).Create(context.Background(), event, metav1.CreateOptions{})
}

func (a *eventSinkAdapter) Update(event *corev1.Event) (*corev1.Event, error) {
	namespace := event.Namespace
	if namespace == "" {
		namespace = event.InvolvedObject.Namespace
		if namespace == "" {
			namespace = "default"
		}
		event.Namespace = namespace
	}
	return a.clientset.CoreV1().Events(namespace).Update(context.Background(), event, metav1.UpdateOptions{})
}

func (a *eventSinkAdapter) Patch(event *corev1.Event, data []byte) (*corev1.Event, error) {
	namespace := event.Namespace
	if namespace == "" {
		namespace = event.InvolvedObject.Namespace
		if namespace == "" {
			namespace = "default"
		}
		event.Namespace = namespace
	}
	return a.clientset.CoreV1().Events(namespace).Patch(context.Background(), event.Name, types.JSONPatchType, data, metav1.PatchOptions{})
}

// NewEventPoster creates a new EventPoster
// Accepts kubernetes.Interface and creates EventRecorder for posting events to PVCs
func NewEventPoster(clientset kubernetes.Interface) *EventPoster {
	// Create event broadcaster
	broadcaster := record.NewBroadcaster()

	// Start logging events to klog for visibility
	broadcaster.StartLogging(klog.Infof)

	// Start recording events to Kubernetes EventSink
	// Use adapter that dynamically routes events to correct namespace
	// based on the event's InvolvedObject namespace
	broadcaster.StartRecordingToSink(&eventSinkAdapter{
		clientset: clientset,
	})

	// Create event recorder with driver component name
	recorder := broadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{
		Component: "nvmet-csi-node",
	})

	return &EventPoster{
		recorder:  recorder,
		clientset: clientset,
	}
}

// PostMountFailure posts a Warning event about a mount failure to the PVC
// Parameters: ctx, pvcNamespace, pvcName, volumeID, nodeName, reason, message
// Message format: "[volumeID] on [nodeName]: [message]"
func (ep *EventPoster) PostMountFailure(ctx context.Context, pvcNamespace, pvcName, volumeID, nodeName, message string) error {
	// Get PVC object to attach event
	pvc, err := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		// Don't fail the operation just because event couldn't be posted
		// PVC might be deleted, terminating, or temporarily unavailable
		klog.Warningf("Failed to get PVC %s/%s for event posting: %v", pvcNamespace, pvcName, err)
		return nil
	}

	// Format event message with volume and node context
	eventMessage := fmt.Sprintf("[%s] on [%s]: %s", volumeID, nodeName, message)

	// Post Warning event to PVC
	ep.recorder.Event(pvc, corev1.EventTypeWarning, EventReasonMountFailure, eventMessage)

	// Record metric
	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonMountFailure)
	}

	klog.V(2).Infof("Posted mount failure event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)

	return nil
}

// PostRecoveryFailed posts a Warning event about a failed recovery attempt to the PVC
// Includes attempt count and final error in message
func (ep *EventPoster) PostRecoveryFailed(ctx context.Context, pvcNamespace, pvcName, volumeID, nodeName string, attemptCount int, finalErr error) error {
	// Get PVC object to attach event
	pvc, err := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		// Don't fail the operation just because event couldn't be posted
		klog.Warningf("Failed to get PVC %s/%s for recovery failure event posting: %v", pvcNamespace, pvcName, err)
		return nil
	}

	// Format event message with recovery context
	eventMessage := fmt.Sprintf("[%s] on [%s]: Recovery failed after %d attempts: %v", volumeID, nodeName, attemptCount, finalErr)

	// Post Warning event to PVC
	ep.recorder.Event(pvc, corev1.EventTypeWarning, EventReasonRecoveryFailed, eventMessage)

	// Record metric
	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonRecoveryFailed)
	}

	klog.V(2).Infof("Posted recovery failure event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)

	return nil
}

// PostStaleMountDetected posts a Normal event about stale mount detection to the PVC
// Normal event type (not Warning) since detection is informational
// Includes old device path and new device path in message
func (ep *EventPoster) PostStaleMountDetected(ctx context.Context, pvcNamespace, pvcName, volumeID, nodeName, oldDevicePath, newDevicePath string) error {
	// Get PVC object to attach event
	pvc, err := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		// Don't fail the operation just because event couldn't be posted
		klog.Warningf("Failed to get PVC %s/%s for stale mount event posting: %v", pvcNamespace, pvcName, err)
		return nil
	}

	// Format event message with device path info
	eventMessage := fmt.Sprintf("[%s] on [%s]: Stale mount detected - old device: %s, new device: %s", volumeID, nodeName, oldDevicePath, newDevicePath)

	// Post Normal event to PVC (informational, not a failure)
	ep.recorder.Event(pvc, corev1.EventTypeNormal, EventReasonStaleMountDetected, eventMessage)

	// Record metric
	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonStaleMountDetected)
	}

	klog.V(2).Infof("Posted stale mount detection event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)

	return nil
}

// PostConnectionFailure posts a Warning event when NVMe connection fails
// Parameters: ctx, pvcNamespace, pvcName, volumeID, nodeName, targetAddress, err
func (ep *EventPoster) PostConnectionFailure(ctx context.Context, pvcNamespace, pvcName, volumeID, nodeName, targetAddress string, err error) error {
	pvc, getErr := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if getErr != nil {
		klog.Warningf("Failed to get PVC %s/%s for connection failure event: %v", pvcNamespace, pvcName, getErr)
		return nil
	}

	eventMessage := fmt.Sprintf("[%s] on [%s]: Connection to %s failed: %v", volumeID, nodeName, targetAddress, err)
	ep.recorder.Event(pvc, corev1.EventTypeWarning, EventReasonConnectionFailure, eventMessage)

	// Record metric
	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonConnectionFailure)
	}

	klog.V(2).Infof("Posted connection failure event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)
	return nil
}

// PostConnectionRecovery posts a Normal event when NVMe connection is recovered
func (ep *EventPoster) PostConnectionRecovery(ctx context.Context, pvcNamespace, pvcName, volumeID, nodeName, targetAddress string, attempts int) error {
	pvc, getErr := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if getErr != nil {
		klog.Warningf("Failed to get PVC %s/%s for connection recovery event: %v", pvcNamespace, pvcName, getErr)
		return nil
	}

	eventMessage := fmt.Sprintf("[%s] on [%s]: Connection to %s recovered after %d attempts", volumeID, nodeName, targetAddress, attempts)
	ep.recorder.Event(pvc, corev1.EventTypeNormal, EventReasonConnectionRecovery, eventMessage)

	// Record metric
	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonConnectionRecovery)
	}

	klog.V(2).Infof("Posted connection recovery event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)
	return nil
}

// PostOrphanDetected logs when an orphan NVMe connection is detected.
// Orphans have no associated PVC, so this logs rather than posting a K8s event.
// The log format is structured for easy parsing by log aggregation systems.
func (ep *EventPoster) PostOrphanDetected(ctx context.Context, nodeName, nqn string) error {
	klog.Infof("OrphanDetected: node=%s nqn=%s", nodeName, nqn)
	// Record metric (even though no K8s event is posted)
	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonOrphanDetected)
	}
	return nil
}

// PostOrphanCleaned logs when an orphan NVMe connection is cleaned up.
// Orphans have no associated PVC, so this logs rather than posting a K8s event.
func (ep *EventPoster) PostOrphanCleaned(ctx context.Context, nodeName, nqn string) error {
	klog.Infof("OrphanCleaned: node=%s nqn=%s", nodeName, nqn)
	// Record metric (even though no K8s event is posted)
	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonOrphanCleaned)
	}
	return nil
}

// PostAttachmentConflict posts a Warning event when a volume attachment is rejected
// due to the volume being attached to a different node.
// Parameters: ctx, pvcNamespace, pvcName, volumeID, requestedNode, attachedNode
func (ep *EventPoster) PostAttachmentConflict(ctx context.Context, pvcNamespace, pvcName, volumeID, requestedNode, attachedNode string) error {
	pvc, err := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		// Don't fail the operation just because event couldn't be posted
		klog.Warningf("Failed to get PVC %s/%s for attachment conflict event: %v", pvcNamespace, pvcName, err)
		return nil
	}

	// Format message with actionable information for operators
	eventMessage := fmt.Sprintf("[%s]: Attachment to node %s rejected - volume already attached to node %s. Delete the pod on %s to release the volume.", volumeID, requestedNode, attachedNode, attachedNode)

	ep.recorder.Event(pvc, corev1.EventTypeWarning, EventReasonAttachmentConflict, eventMessage)

	// Record metric
	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonAttachmentConflict)
	}

	klog.V(2).Infof("Posted attachment conflict event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)
	return nil
}

// PostVolumeAttached posts a Normal event when a volume is attached to a node.
// Parameters: ctx, pvcNamespace, pvcName, volumeID, nodeID, duration
func (ep *EventPoster) PostVolumeAttached(ctx context.Context, pvcNamespace, pvcName, volumeID, nodeID string, duration time.Duration) error {
	pvc, err := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		klog.Warningf("Failed to get PVC %s/%s for volume attached event: %v", pvcNamespace, pvcName, err)
		return nil // Don't fail the operation
	}

	eventMessage := fmt.Sprintf("[%s]: Attached to node %s (duration: %s)", volumeID, nodeID, duration.Round(time.Millisecond))
	ep.recorder.Event(pvc, corev1.EventTypeNormal, EventReasonVolumeAttached, eventMessage)

	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonVolumeAttached)
	}

	klog.V(2).Infof("Posted volume attached event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)
	return nil
}

// PostVolumeDetached posts a Normal event when a volume is detached from a node.
// Parameters: ctx, pvcNamespace, pvcName, volumeID, nodeID
func (ep *EventPoster) PostVolumeDetached(ctx context.Context, pvcNamespace, pvcName, volumeID, nodeID string) error {
	pvc, err := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		klog.Warningf("Failed to get PVC %s/%s for volume detached event: %v", pvcNamespace, pvcName, err)
		return nil
	}
	if ep.shouldSkipPVCEvent(ctx, pvcNamespace, pvcName, EventReasonVolumeDetached, pvc) {
		return nil
	}

	eventMessage := fmt.Sprintf("[%s]: Detached from node %s", volumeID, nodeID)
	ep.recorder.Event(pvc, corev1.EventTypeNormal, EventReasonVolumeDetached, eventMessage)

	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonVolumeDetached)
	}

	klog.V(2).Infof("Posted volume detached event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)
	return nil
}

func (ep *EventPoster) shouldSkipPVCEvent(ctx context.Context, pvcNamespace, pvcName, reason string, pvc *corev1.PersistentVolumeClaim) bool {
	if pvc.DeletionTimestamp != nil {
		klog.V(4).Infof("Skipping %s event for deleting PVC %s/%s", reason, pvcNamespace, pvcName)
		return true
	}

	namespace, err := ep.clientset.CoreV1().Namespaces().Get(ctx, pvcNamespace, metav1.GetOptions{})
	if err != nil {
		klog.V(4).Infof("Could not inspect namespace %s before posting %s event for PVC %s/%s: %v", pvcNamespace, reason, pvcNamespace, pvcName, err)
		return false
	}
	if namespace.Status.Phase == corev1.NamespaceTerminating {
		klog.V(4).Infof("Skipping %s event for PVC %s/%s because namespace is terminating", reason, pvcNamespace, pvcName)
		return true
	}

	return false
}

// PostStaleAttachmentCleared posts a Normal event when a stale attachment is cleared by reconciler.
// Parameters: ctx, pvcNamespace, pvcName, volumeID, staleNodeID
func (ep *EventPoster) PostStaleAttachmentCleared(ctx context.Context, pvcNamespace, pvcName, volumeID, staleNodeID string) error {
	pvc, err := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		klog.Warningf("Failed to get PVC %s/%s for stale attachment cleared event: %v", pvcNamespace, pvcName, err)
		return nil
	}

	eventMessage := fmt.Sprintf("[%s]: Cleared stale attachment from deleted node %s", volumeID, staleNodeID)
	ep.recorder.Event(pvc, corev1.EventTypeNormal, EventReasonStaleAttachmentCleared, eventMessage)

	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonStaleAttachmentCleared)
	}

	klog.V(2).Infof("Posted stale attachment cleared event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)
	return nil
}

// PostMigrationStarted posts a Normal event when a KubeVirt live migration starts.
// Parameters: ctx, pvcNamespace, pvcName, volumeID, sourceNode, targetNode, timeout
func (ep *EventPoster) PostMigrationStarted(ctx context.Context, pvcNamespace, pvcName, volumeID, sourceNode, targetNode string, timeout time.Duration) error {
	pvc, err := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		klog.Warningf("Failed to get PVC %s/%s for migration started event: %v", pvcNamespace, pvcName, err)
		return nil
	}

	eventMessage := fmt.Sprintf("[%s]: KubeVirt live migration started - source: %s, target: %s, timeout: %s", volumeID, sourceNode, targetNode, timeout.Round(time.Second))
	ep.recorder.Event(pvc, corev1.EventTypeNormal, EventReasonMigrationStarted, eventMessage)

	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonMigrationStarted)
	}

	klog.V(2).Infof("Posted migration started event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)
	return nil
}

// PostMigrationCompleted posts a Normal event when a KubeVirt live migration completes successfully.
// Parameters: ctx, pvcNamespace, pvcName, volumeID, sourceNode, targetNode, duration
func (ep *EventPoster) PostMigrationCompleted(ctx context.Context, pvcNamespace, pvcName, volumeID, sourceNode, targetNode string, duration time.Duration) error {
	pvc, err := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		klog.Warningf("Failed to get PVC %s/%s for migration completed event: %v", pvcNamespace, pvcName, err)
		return nil
	}

	eventMessage := fmt.Sprintf("[%s]: KubeVirt live migration completed - source: %s -> target: %s (duration: %s)", volumeID, sourceNode, targetNode, duration.Round(time.Second))
	ep.recorder.Event(pvc, corev1.EventTypeNormal, EventReasonMigrationCompleted, eventMessage)

	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonMigrationCompleted)
	}

	klog.V(2).Infof("Posted migration completed event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)
	return nil
}

// PostMigrationFailed posts a Warning event when a KubeVirt live migration fails.
// Parameters: ctx, pvcNamespace, pvcName, volumeID, sourceNode, targetNode, reason, duration
func (ep *EventPoster) PostMigrationFailed(ctx context.Context, pvcNamespace, pvcName, volumeID, sourceNode, targetNode, reason string, duration time.Duration) error {
	pvc, err := ep.clientset.CoreV1().PersistentVolumeClaims(pvcNamespace).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		klog.Warningf("Failed to get PVC %s/%s for migration failed event: %v", pvcNamespace, pvcName, err)
		return nil
	}

	eventMessage := fmt.Sprintf("[%s]: KubeVirt live migration failed - source: %s, attempted target: %s, reason: %s, elapsed: %s", volumeID, sourceNode, targetNode, reason, duration.Round(time.Second))
	ep.recorder.Event(pvc, corev1.EventTypeWarning, EventReasonMigrationFailed, eventMessage)

	if ep.metrics != nil {
		ep.metrics.RecordEventPosted(EventReasonMigrationFailed)
	}

	klog.V(2).Infof("Posted migration failed event to PVC %s/%s: %s", pvcNamespace, pvcName, eventMessage)
	return nil
}
