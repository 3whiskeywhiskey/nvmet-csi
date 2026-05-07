package attachment

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	corev1listers "k8s.io/client-go/listers/core/v1"
	storagev1listers "k8s.io/client-go/listers/storage/v1"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/observability"
)

// createTestListers creates test node and PV listers from a fake clientset
func createTestListers(clientset *fake.Clientset, objects ...runtime.Object) (nodeLister corev1listers.NodeLister, pvLister corev1listers.PersistentVolumeLister) {
	nodeLister, pvLister, _ = createTestListersWithVolumeAttachments(clientset, objects...)
	return nodeLister, pvLister
}

func createTestListersWithVolumeAttachments(clientset *fake.Clientset, objects ...runtime.Object) (nodeLister corev1listers.NodeLister, pvLister corev1listers.PersistentVolumeLister, vaLister storagev1listers.VolumeAttachmentLister) {
	for _, obj := range objects {
		switch v := obj.(type) {
		case *corev1.Node:
			clientset.CoreV1().Nodes().Create(context.Background(), v, metav1.CreateOptions{})
		case *corev1.PersistentVolume:
			clientset.CoreV1().PersistentVolumes().Create(context.Background(), v, metav1.CreateOptions{})
		case *storagev1.VolumeAttachment:
			clientset.StorageV1().VolumeAttachments().Create(context.Background(), v, metav1.CreateOptions{})
		}
	}
	informerFactory := informers.NewSharedInformerFactory(clientset, 0)
	nodeLister = informerFactory.Core().V1().Nodes().Lister()
	pvLister = informerFactory.Core().V1().PersistentVolumes().Lister()
	vaLister = informerFactory.Storage().V1().VolumeAttachments().Lister()
	stopCh := make(chan struct{})
	defer close(stopCh)
	informerFactory.Start(stopCh)
	informerFactory.WaitForCacheSync(stopCh)
	return nodeLister, pvLister, vaLister
}

func requireStaleVAMetric(t *testing.T, metrics *observability.Metrics, labels ...string) {
	t.Helper()

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "rds_csi_attachment_stale_volumeattachment_cleanup_total{") || !strings.HasSuffix(line, " 1") {
			continue
		}
		matched := true
		for _, label := range labels {
			if !strings.Contains(line, label) {
				matched = false
				break
			}
		}
		if matched {
			return
		}
	}

	t.Fatalf("expected stale VolumeAttachment cleanup metric with labels %v, got:\n%s", labels, body)
}

func TestNewAttachmentReconciler_RequiresManager(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	_, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:    nil,
		K8sClient:  k8sClient,
		NodeLister: nodeLister,
		PVLister:   pvLister,
	})

	if err == nil {
		t.Error("Expected error when manager is nil")
	}
}

func TestNewAttachmentReconciler_RequiresK8sClient(t *testing.T) {
	am := NewAttachmentManager(nil)
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	_, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:    am,
		K8sClient:  nil,
		NodeLister: nodeLister,
		PVLister:   pvLister,
	})

	if err == nil {
		t.Error("Expected error when k8sClient is nil")
	}
}

func TestNewAttachmentReconciler_DefaultValues(t *testing.T) {
	am := NewAttachmentManager(nil)
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:    am,
		K8sClient:  k8sClient,
		NodeLister: nodeLister,
		PVLister:   pvLister,
		// No interval or grace period specified
	})

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Check defaults
	if r.interval != 5*time.Minute {
		t.Errorf("Expected default interval of 5 minutes, got %v", r.interval)
	}
	if r.gracePeriod != 30*time.Second {
		t.Errorf("Expected default grace period of 30 seconds, got %v", r.gracePeriod)
	}
}

func TestReconciler_StartStop(t *testing.T) {
	am := NewAttachmentManager(nil)
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		Interval:    1 * time.Hour, // Long interval - test only verifies Start/Stop lifecycle
		GracePeriod: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	ctx := context.Background()

	// Start should succeed
	err = r.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give goroutine time to start
	time.Sleep(10 * time.Millisecond)

	// Start again should fail (already running)
	err = r.Start(ctx)
	if err == nil {
		t.Error("Expected error when starting already-running reconciler")
	}

	// Stop should succeed
	r.Stop()

	// Stop again should be safe (no-op)
	r.Stop()
}

func TestReconciler_ContextCancellation(t *testing.T) {
	am := NewAttachmentManager(nil)
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		Interval:    1 * time.Hour, // Long interval so we control timing
		GracePeriod: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	err = r.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Get doneCh before cancelling
	r.mu.Lock()
	doneCh := r.doneCh
	r.mu.Unlock()

	// Cancel context - this should cause the goroutine to exit
	cancel()

	// Wait for goroutine to exit (with timeout)
	select {
	case <-doneCh:
		// Successfully stopped
	case <-time.After(1 * time.Second):
		t.Fatal("Reconciler did not stop within 1 second after context cancellation")
	}
}

func TestReconciler_ClearsStaleAttachment_NodeDeleted(t *testing.T) {
	// Create fake k8s client with NO nodes
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	// Create attachment manager and track a volume
	am := NewAttachmentManager(nil)
	ctx := context.Background()
	volumeID := "pvc-test-stale"
	nodeID := "deleted-node"

	// Track attachment to a node that doesn't exist in k8s
	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}

	// Verify attachment exists
	_, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist before reconciliation")
	}

	// Create reconciler with very short grace period (already expired)
	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		Interval:    100 * time.Millisecond,
		GracePeriod: 1 * time.Nanosecond, // Effectively expired immediately
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	// Run a single reconciliation
	r.reconcile(ctx)

	// Verify attachment was cleared
	_, exists = am.GetAttachment(volumeID)
	if exists {
		t.Error("Expected stale attachment to be cleared after reconciliation")
	}
}

func TestReconciler_PreservesValidAttachment_NodeExists(t *testing.T) {
	// Create fake k8s client with a node
	existingNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "existing-node",
		},
	}
	k8sClient := fake.NewSimpleClientset(existingNode)
	nodeLister, pvLister := createTestListers(k8sClient)

	// Create attachment manager and track a volume
	am := NewAttachmentManager(nil)
	ctx := context.Background()
	volumeID := "pvc-test-valid"
	nodeID := "existing-node"

	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}

	// Create reconciler
	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		Interval:    100 * time.Millisecond,
		GracePeriod: 1 * time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	// Run reconciliation
	r.reconcile(ctx)

	// Verify attachment still exists (node exists, so attachment is valid)
	_, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Error("Expected valid attachment to be preserved after reconciliation")
	}
}

func TestReconciler_RespectsGracePeriod(t *testing.T) {
	// Create fake k8s client with NO nodes
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	// Create attachment manager
	am := NewAttachmentManager(nil)
	ctx := context.Background()
	volumeID := "pvc-test-grace"
	nodeID := "deleted-node"

	// Track and then untrack to create a recent detach timestamp
	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}
	err = am.UntrackAttachment(ctx, volumeID)
	if err != nil {
		t.Fatalf("UntrackAttachment failed: %v", err)
	}

	// Re-track (simulating the state when reconciler runs)
	err = am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("Re-track failed: %v", err)
	}

	// Create reconciler with long grace period
	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		Interval:    100 * time.Millisecond,
		GracePeriod: 1 * time.Hour, // Very long grace period
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	// Run reconciliation
	r.reconcile(ctx)

	// Attachment should still exist because we're within grace period
	// (detach timestamp was set just before)
	_, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Error("Expected attachment to be preserved during grace period")
	}
}

func TestReconciler_HandlesAPIErrors(t *testing.T) {
	// NOTE: With informer-based caching, API errors during reconciliation don't occur
	// because we use cached listers. API errors would only happen during initial cache sync.
	// This test now verifies correct behavior with cached data: node not in cache = deleted

	// Create fake k8s client with NO nodes (simulating deleted node)
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	// Create attachment manager and track a volume to non-existent node
	am := NewAttachmentManager(nil)
	ctx := context.Background()
	volumeID := "pvc-test-cache-miss"
	nodeID := "deleted-node"

	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}

	// Create reconciler
	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		Interval:    100 * time.Millisecond,
		GracePeriod: 1 * time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	// Run reconciliation - should clear attachment (node not in cache = deleted)
	r.reconcile(ctx)

	// Attachment should be cleared (node doesn't exist in cache)
	_, exists := am.GetAttachment(volumeID)
	if exists {
		t.Error("Expected attachment to be cleared when node not in informer cache")
	}
}

func TestReconciler_GetGracePeriod(t *testing.T) {
	am := NewAttachmentManager(nil)
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	gracePeriod := 45 * time.Second
	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		GracePeriod: gracePeriod,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	if r.GetGracePeriod() != gracePeriod {
		t.Errorf("Expected grace period %v, got %v", gracePeriod, r.GetGracePeriod())
	}
}

func TestTriggerReconcile_ImmediateReconciliation(t *testing.T) {
	// Create fake k8s client with NO nodes (simulating deleted node)
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	// Create attachment manager and track a volume to non-existent node
	am := NewAttachmentManager(nil)
	ctx := context.Background()
	volumeID := "pvc-test-trigger"
	nodeID := "deleted-node"

	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}

	// Create reconciler with very long interval so periodic reconciliation won't fire
	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		Interval:    1 * time.Hour, // Very long - we control timing via TriggerReconcile
		GracePeriod: 1 * time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	// Start reconciler
	err = r.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer r.Stop()

	// Wait for initial reconciliation to complete
	time.Sleep(50 * time.Millisecond)

	// Verify attachment was cleared by initial reconciliation
	_, exists := am.GetAttachment(volumeID)
	if exists {
		t.Error("Expected stale attachment to be cleared by initial reconciliation")
	}

	// Re-track the attachment to test TriggerReconcile
	err = am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("Re-track failed: %v", err)
	}

	// Trigger reconciliation immediately
	r.TriggerReconcile()

	// Wait for reconciliation to process (should be fast)
	time.Sleep(50 * time.Millisecond)

	// Verify attachment was cleared by triggered reconciliation
	_, exists = am.GetAttachment(volumeID)
	if exists {
		t.Error("Expected stale attachment to be cleared by triggered reconciliation")
	}
}

func TestTriggerReconcile_Deduplication(t *testing.T) {
	am := NewAttachmentManager(nil)
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		Interval:    1 * time.Hour,
		GracePeriod: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	ctx := context.Background()
	err = r.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer r.Stop()

	// Send multiple rapid trigger requests
	for i := 0; i < 10; i++ {
		r.TriggerReconcile()
	}

	// Channel should have at most 1 pending trigger due to buffered size 1
	// We can't directly check channel length, but we verify no panic/deadlock occurs
	time.Sleep(100 * time.Millisecond)

	// If we got here without deadlock, deduplication works
}

func TestTriggerReconcile_NotRunning(t *testing.T) {
	am := NewAttachmentManager(nil)
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		Interval:    100 * time.Millisecond,
		GracePeriod: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	// Call TriggerReconcile when reconciler is not started - should be safe no-op
	r.TriggerReconcile()

	// Should not panic
}

func TestReconciler_RESIL03_StaleCleanupAndReattachment(t *testing.T) {
	// Scenario: Volume attached to node-A, node-A is deleted,
	// reconciler clears stale attachment, volume reattaches to node-B.

	// Setup: fake k8s with only node-B (node-A does not exist = "deleted")
	nodeB := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-b"},
	}
	k8sClient := fake.NewSimpleClientset(nodeB)
	nodeLister, pvLister := createTestListers(k8sClient, nodeB)

	am := NewAttachmentManager(nil)
	ctx := context.Background()
	volumeID := "pvc-resil-03"
	nodeA := "node-a" // This node does NOT exist in k8s

	// Step 1: Track attachment to node-A (simulates ControllerPublishVolume)
	err := am.TrackAttachment(ctx, volumeID, nodeA)
	if err != nil {
		t.Fatalf("TrackAttachment to node-a failed: %v", err)
	}

	// Verify attachment exists on node-A
	state, exists := am.GetAttachment(volumeID)
	if !exists || state.NodeID != nodeA {
		t.Fatalf("Expected attachment on node-a, got exists=%v state=%+v", exists, state)
	}

	// Step 2: Run reconciliation (should detect node-A is gone and clear attachment)
	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		Interval:    100 * time.Millisecond,
		GracePeriod: 1 * time.Nanosecond, // Immediately expired
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	r.reconcile(ctx)

	// Step 3: Verify stale attachment was cleared
	_, exists = am.GetAttachment(volumeID)
	if exists {
		t.Error("Expected stale attachment on node-a to be cleared after reconciliation")
	}

	// Step 4: Reattach to node-B (simulates volume moving to surviving node)
	err = am.TrackAttachment(ctx, volumeID, "node-b")
	if err != nil {
		t.Fatalf("TrackAttachment to node-b failed: %v", err)
	}

	// Step 5: Run reconciliation again — attachment on node-B should be preserved
	r.reconcile(ctx)

	state, exists = am.GetAttachment(volumeID)
	if !exists {
		t.Error("Expected attachment on node-b to be preserved (node exists)")
	}
	if exists && state.NodeID != "node-b" {
		t.Errorf("Expected attachment on node-b, got %s", state.NodeID)
	}
}

func TestTriggerReconcile_AfterStop(t *testing.T) {
	am := NewAttachmentManager(nil)
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		Interval:    1 * time.Hour, // Long interval - test does not need periodic timer to fire
		GracePeriod: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	ctx := context.Background()
	err = r.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Give goroutine time to start
	time.Sleep(10 * time.Millisecond)

	// Stop the reconciler
	r.Stop()

	// Call TriggerReconcile after stop - should be safe no-op
	r.TriggerReconcile()

	// Should not panic
}

func TestReconciler_StaleVolumeAttachmentRequiresConfirmations(t *testing.T) {
	ctx := context.Background()
	volumeID := "pvc-stale-va-confirm"
	va := testVolumeAttachment("va-stale-confirm", volumeID, "deleted-node", driverName, time.Now().Add(-time.Hour))
	pv := testPersistentVolume(volumeID)
	k8sClient := fake.NewSimpleClientset(pv, va)
	nodeLister, pvLister, vaLister := createTestListersWithVolumeAttachments(k8sClient)
	metrics := observability.NewMetrics()

	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:                       NewAttachmentManager(nil),
		K8sClient:                     k8sClient,
		NodeLister:                    nodeLister,
		PVLister:                      pvLister,
		VolumeAttachmentLister:        vaLister,
		Interval:                      time.Hour,
		GracePeriod:                   time.Nanosecond,
		StaleVolumeAttachmentCleanup:  true,
		StaleVolumeAttachmentDelete:   true,
		StaleVolumeAttachmentMinConfs: 2,
		Metrics:                       metrics,
	})
	if err != nil {
		t.Fatalf("NewAttachmentReconciler failed: %v", err)
	}

	r.reconcile(ctx)
	if _, err := k8sClient.StorageV1().VolumeAttachments().Get(ctx, va.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("VolumeAttachment should survive first confirmation pass: %v", err)
	}
	requireStaleVAMetric(t, metrics, `result="candidate"`, `dry_run="false"`)

	r.reconcile(ctx)
	if _, err := k8sClient.StorageV1().VolumeAttachments().Get(ctx, va.Name, metav1.GetOptions{}); err == nil {
		t.Fatal("VolumeAttachment should be deleted after second confirmation pass")
	}
	requireStaleVAMetric(t, metrics, `result="planned"`, `dry_run="false"`)
	requireStaleVAMetric(t, metrics, `result="delete_attempt"`, `dry_run="false"`)
	requireStaleVAMetric(t, metrics, `result="delete_success"`, `dry_run="false"`)
}

func TestReconciler_StaleVolumeAttachmentDeleteDoesNotDelete(t *testing.T) {
	ctx := context.Background()
	volumeID := "pvc-stale-va-dry-run"
	va := testVolumeAttachment("va-stale-dry-run", volumeID, "deleted-node", driverName, time.Now().Add(-time.Hour))
	pv := testPersistentVolume(volumeID)
	k8sClient := fake.NewSimpleClientset(pv, va)
	nodeLister, pvLister, vaLister := createTestListersWithVolumeAttachments(k8sClient)
	metrics := observability.NewMetrics()

	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:                       NewAttachmentManager(nil),
		K8sClient:                     k8sClient,
		NodeLister:                    nodeLister,
		PVLister:                      pvLister,
		VolumeAttachmentLister:        vaLister,
		Interval:                      time.Hour,
		GracePeriod:                   time.Nanosecond,
		StaleVolumeAttachmentCleanup:  true,
		StaleVolumeAttachmentDelete:   false,
		StaleVolumeAttachmentMinConfs: 1,
		Metrics:                       metrics,
	})
	if err != nil {
		t.Fatalf("NewAttachmentReconciler failed: %v", err)
	}

	r.reconcile(ctx)
	if _, err := k8sClient.StorageV1().VolumeAttachments().Get(ctx, va.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("dry-run cleanup should not delete VolumeAttachment: %v", err)
	}
	requireStaleVAMetric(t, metrics, `result="candidate"`, `dry_run="true"`)
	requireStaleVAMetric(t, metrics, `result="planned"`, `dry_run="true"`)
}

func TestReconciler_StaleVolumeAttachmentPreservesLiveNode(t *testing.T) {
	ctx := context.Background()
	volumeID := "pvc-live-va"
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "live-node"}}
	va := testVolumeAttachment("va-live-node", volumeID, node.Name, driverName, time.Now().Add(-time.Hour))
	pv := testPersistentVolume(volumeID)
	k8sClient := fake.NewSimpleClientset(node, pv, va)
	nodeLister, pvLister, vaLister := createTestListersWithVolumeAttachments(k8sClient)

	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:                       NewAttachmentManager(nil),
		K8sClient:                     k8sClient,
		NodeLister:                    nodeLister,
		PVLister:                      pvLister,
		VolumeAttachmentLister:        vaLister,
		Interval:                      time.Hour,
		GracePeriod:                   time.Nanosecond,
		StaleVolumeAttachmentCleanup:  true,
		StaleVolumeAttachmentDelete:   true,
		StaleVolumeAttachmentMinConfs: 1,
	})
	if err != nil {
		t.Fatalf("NewAttachmentReconciler failed: %v", err)
	}

	r.reconcile(ctx)
	if _, err := k8sClient.StorageV1().VolumeAttachments().Get(ctx, va.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("VolumeAttachment for live node should be preserved: %v", err)
	}
}

func TestReconciler_StaleVolumeAttachmentRequiresPV(t *testing.T) {
	ctx := context.Background()
	volumeID := "pvc-missing-pv"
	va := testVolumeAttachment("va-missing-pv", volumeID, "deleted-node", driverName, time.Now().Add(-time.Hour))
	k8sClient := fake.NewSimpleClientset(va)
	nodeLister, pvLister, vaLister := createTestListersWithVolumeAttachments(k8sClient)

	r, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:                       NewAttachmentManager(nil),
		K8sClient:                     k8sClient,
		NodeLister:                    nodeLister,
		PVLister:                      pvLister,
		VolumeAttachmentLister:        vaLister,
		Interval:                      time.Hour,
		GracePeriod:                   time.Nanosecond,
		StaleVolumeAttachmentCleanup:  true,
		StaleVolumeAttachmentDelete:   true,
		StaleVolumeAttachmentMinConfs: 1,
	})
	if err != nil {
		t.Fatalf("NewAttachmentReconciler failed: %v", err)
	}

	r.reconcile(ctx)
	if _, err := k8sClient.StorageV1().VolumeAttachments().Get(ctx, va.Name, metav1.GetOptions{}); err != nil {
		t.Fatalf("VolumeAttachment without PV should be preserved for manual inspection: %v", err)
	}
}

func testPersistentVolume(name string) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       driverName,
					VolumeHandle: name,
				},
			},
		},
	}
}

func testVolumeAttachment(name, volumeID, nodeID, attacher string, createdAt time.Time) *storagev1.VolumeAttachment {
	return &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(createdAt),
		},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: attacher,
			NodeName: nodeID,
			Source: storagev1.VolumeAttachmentSource{
				PersistentVolumeName: stringPtr(volumeID),
			},
		},
		Status: storagev1.VolumeAttachmentStatus{
			Attached: true,
		},
	}
}

func stringPtr(value string) *string {
	return &value
}
