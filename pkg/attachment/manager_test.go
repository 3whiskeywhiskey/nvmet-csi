package attachment

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAttachmentManager_TrackAttachment(t *testing.T) {
	// Create AttachmentManager with nil k8sClient for in-memory only
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()

	volumeID := "vol-1"
	nodeID := "node-1"

	// Track attachment
	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}

	// Get attachment and verify
	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist, but it doesn't")
	}

	if state.NodeID != nodeID {
		t.Errorf("Expected nodeID %s, got %s", nodeID, state.NodeID)
	}

	if state.VolumeID != volumeID {
		t.Errorf("Expected volumeID %s, got %s", volumeID, state.VolumeID)
	}

	// Verify AttachedAt is approximately now
	if time.Since(state.AttachedAt) > 5*time.Second {
		t.Errorf("Expected AttachedAt to be recent, but it's %v old", time.Since(state.AttachedAt))
	}
}

func TestAttachmentManager_TrackAttachment_Idempotent(t *testing.T) {
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()

	volumeID := "vol-1"
	nodeID := "node-1"

	// Track attachment
	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("First TrackAttachment failed: %v", err)
	}

	// Track same attachment again (idempotent)
	err = am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("Second TrackAttachment (idempotent) failed: %v", err)
	}

	// Verify still only one attachment
	attachments := am.ListAttachments()
	if len(attachments) != 1 {
		t.Errorf("Expected 1 attachment after idempotent track, got %d", len(attachments))
	}
}

func TestAttachmentManager_TrackAttachment_ConflictError(t *testing.T) {
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()

	volumeID := "vol-1"
	nodeID1 := "node-1"
	nodeID2 := "node-2"

	// Track attachment to node-1
	err := am.TrackAttachment(ctx, volumeID, nodeID1)
	if err != nil {
		t.Fatalf("First TrackAttachment failed: %v", err)
	}

	// Try to track same volume to different node - should fail
	err = am.TrackAttachment(ctx, volumeID, nodeID2)
	if err == nil {
		t.Fatal("Expected error when tracking volume to different node, but got nil")
	}

	// Verify error message mentions the conflict
	expectedSubstring := "already attached to node node-1"
	if !contains(err.Error(), expectedSubstring) {
		t.Errorf("Expected error message to contain '%s', got: %s", expectedSubstring, err.Error())
	}

	// Verify original attachment is still intact
	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected original attachment to exist")
	}
	if state.NodeID != nodeID1 {
		t.Errorf("Expected attachment to still be on node-1, got %s", state.NodeID)
	}
}

func TestAttachmentManager_UntrackAttachment(t *testing.T) {
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()

	volumeID := "vol-1"
	nodeID := "node-1"

	// Track attachment
	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}

	// Verify it exists
	_, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist before untracking")
	}

	// Untrack attachment
	err = am.UntrackAttachment(ctx, volumeID)
	if err != nil {
		t.Fatalf("UntrackAttachment failed: %v", err)
	}

	// Verify it no longer exists
	_, exists = am.GetAttachment(volumeID)
	if exists {
		t.Fatal("Expected attachment to not exist after untracking")
	}
}

func TestAttachmentManager_UntrackAttachment_Idempotent(t *testing.T) {
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()

	// Untrack non-existent volume - should not error
	err := am.UntrackAttachment(ctx, "vol-nonexistent")
	if err != nil {
		t.Fatalf("UntrackAttachment of non-existent volume failed: %v", err)
	}

	// Track and untrack
	volumeID := "vol-1"
	_ = am.TrackAttachment(ctx, volumeID, "node-1")
	_ = am.UntrackAttachment(ctx, volumeID)

	// Untrack again - should be idempotent
	err = am.UntrackAttachment(ctx, volumeID)
	if err != nil {
		t.Fatalf("Second UntrackAttachment (idempotent) failed: %v", err)
	}
}

func TestAttachmentManager_ListAttachments(t *testing.T) {
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()

	// Track multiple volumes
	_ = am.TrackAttachment(ctx, "vol-1", "node-1")
	_ = am.TrackAttachment(ctx, "vol-2", "node-2")
	_ = am.TrackAttachment(ctx, "vol-3", "node-1")

	// List attachments
	attachments := am.ListAttachments()

	if len(attachments) != 3 {
		t.Errorf("Expected 3 attachments, got %d", len(attachments))
	}

	// Verify correct mappings
	if attachments["vol-1"].NodeID != "node-1" {
		t.Errorf("Expected vol-1 on node-1, got %s", attachments["vol-1"].NodeID)
	}
	if attachments["vol-2"].NodeID != "node-2" {
		t.Errorf("Expected vol-2 on node-2, got %s", attachments["vol-2"].NodeID)
	}
	if attachments["vol-3"].NodeID != "node-1" {
		t.Errorf("Expected vol-3 on node-1, got %s", attachments["vol-3"].NodeID)
	}

	// Delete from returned map - internal state should be unchanged
	delete(attachments, "vol-1")

	// Verify internal state still has vol-1
	freshState, exists := am.GetAttachment("vol-1")
	if !exists {
		t.Error("Expected vol-1 to still exist in internal state after deleting from returned map")
	}
	if freshState.NodeID != "node-1" {
		t.Errorf("Expected vol-1 to still be on node-1, but got %s", freshState.NodeID)
	}

	// Verify returned map copy is actually separate
	if len(attachments) != 2 {
		t.Errorf("Expected returned map to have 2 entries after delete, got %d", len(attachments))
	}
	if len(am.ListAttachments()) != 3 {
		t.Errorf("Expected internal state to still have 3 entries, got %d", len(am.ListAttachments()))
	}
}

func TestAttachmentManager_ConcurrentTrack(t *testing.T) {
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()

	numGoroutines := 50
	errChan := make(chan error, numGoroutines)

	// Track different volumes concurrently
	for i := 0; i < numGoroutines; i++ {
		go func(n int) {
			volumeID := fmt.Sprintf("vol-%d", n)
			nodeID := fmt.Sprintf("node-%d", n)
			errChan <- am.TrackAttachment(ctx, volumeID, nodeID)
		}(i)
	}

	// Collect results
	for i := 0; i < numGoroutines; i++ {
		err := <-errChan
		if err != nil {
			t.Errorf("Goroutine %d failed: %v", i, err)
		}
	}

	// Verify all attachments tracked
	attachments := am.ListAttachments()
	if len(attachments) != numGoroutines {
		t.Errorf("Expected %d attachments, got %d", numGoroutines, len(attachments))
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

// Helper to create test PV
func createTestPV(volumeID, nodeID string) *corev1.PersistentVolume {
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: volumeID,
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "nvmet.csi.whiskey.works",
					VolumeHandle: volumeID,
				},
			},
		},
	}
	if nodeID != "" {
		pv.Annotations = map[string]string{
			AnnotationAttachedNode: nodeID,
			AnnotationAttachedAt:   metav1.Now().Format(metav1.RFC3339Micro),
		}
	}
	return pv
}

func TestAttachmentManager_PersistAttachment(t *testing.T) {
	volumeID := "pv-vol-1"
	nodeID := "node-1"

	// Create fake clientset with a PV
	pv := createTestPV(volumeID, "")
	fakeClient := fake.NewSimpleClientset(pv)

	// Create AttachmentManager with fake client
	am := NewAttachmentManager(fakeClient, "nvmet.csi.whiskey.works")
	ctx := context.Background()

	// Track attachment
	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}

	// Get PV from fake client and verify annotation
	updatedPV, err := fakeClient.CoreV1().PersistentVolumes().Get(ctx, volumeID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get PV: %v", err)
	}

	if updatedPV.Annotations == nil {
		t.Fatal("Expected PV to have annotations, but it's nil")
	}

	if updatedPV.Annotations[AnnotationAttachedNode] != nodeID {
		t.Errorf("Expected annotation %s=%s, got %s", AnnotationAttachedNode, nodeID, updatedPV.Annotations[AnnotationAttachedNode])
	}

	if updatedPV.Annotations[AnnotationAttachedAt] == "" {
		t.Errorf("Expected annotation %s to be set", AnnotationAttachedAt)
	}
}

func TestAttachmentManager_ClearAttachment(t *testing.T) {
	volumeID := "pv-vol-1"
	nodeID := "node-1"

	// Create PV with attachment annotations
	pv := createTestPV(volumeID, nodeID)
	fakeClient := fake.NewSimpleClientset(pv)

	// Create AttachmentManager
	am := NewAttachmentManager(fakeClient, "nvmet.csi.whiskey.works")
	ctx := context.Background()

	// Track then untrack
	_ = am.TrackAttachment(ctx, volumeID, nodeID)
	err := am.UntrackAttachment(ctx, volumeID)
	if err != nil {
		t.Fatalf("UntrackAttachment failed: %v", err)
	}

	// Verify annotations removed from PV
	updatedPV, err := fakeClient.CoreV1().PersistentVolumes().Get(ctx, volumeID, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("Failed to get PV: %v", err)
	}

	if updatedPV.Annotations != nil {
		if _, hasNode := updatedPV.Annotations[AnnotationAttachedNode]; hasNode {
			t.Errorf("Expected %s annotation to be removed, but it's still present", AnnotationAttachedNode)
		}
		if _, hasAt := updatedPV.Annotations[AnnotationAttachedAt]; hasAt {
			t.Errorf("Expected %s annotation to be removed, but it's still present", AnnotationAttachedAt)
		}
	}
}

func TestAttachmentManager_RebuildState(t *testing.T) {
	ctx := context.Background()

	// Create fake clientset with 3 PVs and corresponding VolumeAttachments
	pv1 := createTestPV("pv-1", "node-a")
	pv2 := createTestPV("pv-2", "node-b")
	pv3 := createTestPV("pv-3", "") // No annotations (not attached)

	// Create VolumeAttachments for pv-1 and pv-2 (pv-3 has no VA = not attached)
	va1 := createTestVolumeAttachment("va-1", "nvmet.csi.whiskey.works", "pv-1", "node-a", true)
	va2 := createTestVolumeAttachment("va-2", "nvmet.csi.whiskey.works", "pv-2", "node-b", true)

	fakeClient := fake.NewSimpleClientset(pv1, pv2, pv3, va1, va2)

	// Create AttachmentManager
	am := NewAttachmentManager(fakeClient, "nvmet.csi.whiskey.works")

	// Call Initialize (which calls RebuildState from VolumeAttachments)
	err := am.Initialize(ctx)
	if err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Verify ListAttachments returns 2 entries (pv-1 and pv-2)
	attachments := am.ListAttachments()
	if len(attachments) != 2 {
		t.Errorf("Expected 2 attachments after rebuild, got %d", len(attachments))
	}

	// Verify correct volume-to-node mappings
	if state, exists := attachments["pv-1"]; exists {
		if state.NodeID != "node-a" {
			t.Errorf("Expected pv-1 on node-a, got %s", state.NodeID)
		}
	} else {
		t.Error("Expected pv-1 to be in attachments")
	}

	if state, exists := attachments["pv-2"]; exists {
		if state.NodeID != "node-b" {
			t.Errorf("Expected pv-2 on node-b, got %s", state.NodeID)
		}
	} else {
		t.Error("Expected pv-2 to be in attachments")
	}

	// Verify pv-3 is not in attachments (no VolumeAttachment)
	if _, exists := attachments["pv-3"]; exists {
		t.Error("Expected pv-3 to not be in attachments (no VolumeAttachment)")
	}
}

func TestAttachmentManager_RebuildState_IgnoresOtherDrivers(t *testing.T) {
	ctx := context.Background()

	// Create PV with different driver
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: "pv-other",
			Annotations: map[string]string{
				AnnotationAttachedNode: "node-1",
				AnnotationAttachedAt:   metav1.Now().Format(metav1.RFC3339Micro),
			},
		},
		Spec: corev1.PersistentVolumeSpec{
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       "other.csi.io",
					VolumeHandle: "pv-other",
				},
			},
		},
	}

	fakeClient := fake.NewSimpleClientset(pv)
	am := NewAttachmentManager(fakeClient, "nvmet.csi.whiskey.works")

	// RebuildState should not include it
	err := am.RebuildState(ctx)
	if err != nil {
		t.Fatalf("RebuildState failed: %v", err)
	}

	attachments := am.ListAttachments()
	if len(attachments) != 0 {
		t.Errorf("Expected 0 attachments (different driver), got %d", len(attachments))
	}
}

func TestAttachmentManager_RebuildState_NoClient(t *testing.T) {
	// Create AttachmentManager with nil k8sClient
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()

	// RebuildState should return nil (not error)
	err := am.RebuildState(ctx)
	if err != nil {
		t.Fatalf("RebuildState with nil client should not error, got: %v", err)
	}

	// Attachments should be empty
	attachments := am.ListAttachments()
	if len(attachments) != 0 {
		t.Errorf("Expected 0 attachments (nil client), got %d", len(attachments))
	}
}

func TestAttachmentManager_PersistRollback(t *testing.T) {
	volumeID := "pv-nonexistent"
	nodeID := "node-1"

	// Create fake client WITHOUT the PV (simulate PV not existing)
	fakeClient := fake.NewSimpleClientset()

	am := NewAttachmentManager(fakeClient, "nvmet.csi.whiskey.works")
	ctx := context.Background()

	// Track attachment - should succeed (persistence failure is non-fatal for missing PV)
	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment should succeed even if PV doesn't exist yet, got: %v", err)
	}

	// Verify in-memory state is still set (persistence failure for missing PV is logged but not fatal)
	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Error("Expected attachment to exist in memory even if PV doesn't exist")
	}
	if state != nil && state.NodeID != nodeID {
		t.Errorf("Expected nodeID %s, got %s", nodeID, state.NodeID)
	}
}

func TestIsWithinGracePeriod_NoDetachTimestamp(t *testing.T) {
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")

	// Volume never tracked/detached should not be within grace period
	result := am.IsWithinGracePeriod("pvc-never-existed", 30*time.Second)
	if result {
		t.Error("Expected IsWithinGracePeriod to return false for non-existent volume")
	}
}

func TestIsWithinGracePeriod_WithinPeriod(t *testing.T) {
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()
	volumeID := "pvc-test-grace-within"
	nodeID := "node-1"

	// Track and then untrack to create detach timestamp
	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}

	err = am.UntrackAttachment(ctx, volumeID)
	if err != nil {
		t.Fatalf("UntrackAttachment failed: %v", err)
	}

	// Check immediately after detach (should be within grace period)
	result := am.IsWithinGracePeriod(volumeID, 30*time.Second)
	if !result {
		t.Error("Expected IsWithinGracePeriod to return true immediately after detach")
	}
}

func TestIsWithinGracePeriod_OutsidePeriod(t *testing.T) {
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()
	volumeID := "pvc-test-grace-outside"
	nodeID := "node-1"

	// Track and then untrack
	err := am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}

	err = am.UntrackAttachment(ctx, volumeID)
	if err != nil {
		t.Fatalf("UntrackAttachment failed: %v", err)
	}

	// Use a very short grace period that we've already exceeded
	// Sleep a tiny bit to ensure we're past the grace period
	time.Sleep(1 * time.Millisecond)

	result := am.IsWithinGracePeriod(volumeID, 1*time.Nanosecond)
	if result {
		t.Error("Expected IsWithinGracePeriod to return false after grace period expired")
	}
}

func TestGetDetachTimestamp(t *testing.T) {
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()
	volumeID := "pvc-test-detach-time"
	nodeID := "node-1"

	// Before any tracking, should return zero time
	ts := am.GetDetachTimestamp(volumeID)
	if !ts.IsZero() {
		t.Error("Expected zero timestamp for never-tracked volume")
	}

	// Track and untrack
	_ = am.TrackAttachment(ctx, volumeID, nodeID)
	_ = am.UntrackAttachment(ctx, volumeID)

	// Should have a non-zero timestamp now
	ts = am.GetDetachTimestamp(volumeID)
	if ts.IsZero() {
		t.Error("Expected non-zero timestamp after untrack")
	}

	// Timestamp should be recent
	if time.Since(ts) > 5*time.Second {
		t.Error("Detach timestamp is too old")
	}
}

func TestClearDetachTimestamp(t *testing.T) {
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()
	volumeID := "pvc-test-clear-time"
	nodeID := "node-1"

	// Track and untrack to create timestamp
	_ = am.TrackAttachment(ctx, volumeID, nodeID)
	_ = am.UntrackAttachment(ctx, volumeID)

	// Verify timestamp exists
	ts := am.GetDetachTimestamp(volumeID)
	if ts.IsZero() {
		t.Fatal("Expected timestamp to exist before clear")
	}

	// Clear it
	am.ClearDetachTimestamp(volumeID)

	// Should no longer be within grace period
	result := am.IsWithinGracePeriod(volumeID, 30*time.Second)
	if result {
		t.Error("Expected IsWithinGracePeriod to return false after clearing timestamp")
	}
}

func TestGracePeriod_LiveMigrationScenario(t *testing.T) {
	// Simulates KubeVirt live migration: detach from node-1, attach to node-2
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	ctx := context.Background()
	volumeID := "pvc-kubevirt-vm-disk"
	node1 := "worker-node-1"
	node2 := "worker-node-2"
	gracePeriod := 30 * time.Second

	// Initial attachment to node-1
	err := am.TrackAttachment(ctx, volumeID, node1)
	if err != nil {
		t.Fatalf("Initial track failed: %v", err)
	}

	// Detach from node-1 (VM migrating)
	err = am.UntrackAttachment(ctx, volumeID)
	if err != nil {
		t.Fatalf("Untrack failed: %v", err)
	}

	// Immediately try to attach to node-2 (migration target)
	// Should be within grace period
	if !am.IsWithinGracePeriod(volumeID, gracePeriod) {
		t.Error("Expected to be within grace period immediately after detach")
	}

	// Clear timestamp as handoff completes
	am.ClearDetachTimestamp(volumeID)

	// Track to new node
	err = am.TrackAttachment(ctx, volumeID, node2)
	if err != nil {
		t.Fatalf("Reattachment failed: %v", err)
	}

	// Verify attached to new node
	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist")
	}
	if state.NodeID != node2 {
		t.Errorf("Expected attachment to node %s, got %s", node2, state.NodeID)
	}
}

// ========================================
// RWX Dual-Attach Tests (Phase 08-03)
// ========================================

func TestAttachmentManager_AddSecondaryAttachment(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(am *AttachmentManager)
		volumeID      string
		nodeID        string
		expectError   bool
		errorContains string
	}{
		{
			name: "add secondary to existing RWX volume",
			setup: func(am *AttachmentManager) {
				_ = am.TrackAttachmentWithMode(context.Background(), "vol-1", "node-1", "RWX")
			},
			volumeID:    "vol-1",
			nodeID:      "node-2",
			expectError: false,
		},
		{
			name: "idempotent - same node already attached",
			setup: func(am *AttachmentManager) {
				_ = am.TrackAttachmentWithMode(context.Background(), "vol-1", "node-1", "RWX")
				_ = am.AddSecondaryAttachment(context.Background(), "vol-1", "node-2", 5*time.Minute)
			},
			volumeID:    "vol-1",
			nodeID:      "node-2",
			expectError: false,
		},
		{
			name: "reject 3rd attachment - migration limit",
			setup: func(am *AttachmentManager) {
				_ = am.TrackAttachmentWithMode(context.Background(), "vol-1", "node-1", "RWX")
				_ = am.AddSecondaryAttachment(context.Background(), "vol-1", "node-2", 5*time.Minute)
			},
			volumeID:      "vol-1",
			nodeID:        "node-3",
			expectError:   true,
			errorContains: "migration limit",
		},
		{
			name:          "fail if volume not attached",
			setup:         func(am *AttachmentManager) {},
			volumeID:      "vol-not-exist",
			nodeID:        "node-1",
			expectError:   true,
			errorContains: "not attached",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
			tc.setup(am)

			err := am.AddSecondaryAttachment(context.Background(), tc.volumeID, tc.nodeID, 5*time.Minute)

			if tc.expectError {
				if err == nil {
					t.Error("expected error but got nil")
				} else if tc.errorContains != "" && !strings.Contains(err.Error(), tc.errorContains) {
					t.Errorf("expected error containing %q, got %q", tc.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestAttachmentManager_RemoveNodeAttachment(t *testing.T) {
	tests := []struct {
		name            string
		setup           func(am *AttachmentManager)
		volumeID        string
		nodeID          string
		expectDetached  bool
		expectRemaining int
	}{
		{
			name: "remove only node - fully detached",
			setup: func(am *AttachmentManager) {
				_ = am.TrackAttachmentWithMode(context.Background(), "vol-1", "node-1", "RWO")
			},
			volumeID:        "vol-1",
			nodeID:          "node-1",
			expectDetached:  true,
			expectRemaining: 0,
		},
		{
			name: "remove secondary node - one remaining",
			setup: func(am *AttachmentManager) {
				_ = am.TrackAttachmentWithMode(context.Background(), "vol-1", "node-1", "RWX")
				_ = am.AddSecondaryAttachment(context.Background(), "vol-1", "node-2", 5*time.Minute)
			},
			volumeID:        "vol-1",
			nodeID:          "node-2",
			expectDetached:  false,
			expectRemaining: 1,
		},
		{
			name: "remove primary node - secondary promoted",
			setup: func(am *AttachmentManager) {
				_ = am.TrackAttachmentWithMode(context.Background(), "vol-1", "node-1", "RWX")
				_ = am.AddSecondaryAttachment(context.Background(), "vol-1", "node-2", 5*time.Minute)
			},
			volumeID:        "vol-1",
			nodeID:          "node-1",
			expectDetached:  false,
			expectRemaining: 1,
		},
		{
			name:            "idempotent - volume not tracked",
			setup:           func(am *AttachmentManager) {},
			volumeID:        "vol-not-exist",
			nodeID:          "node-1",
			expectDetached:  false,
			expectRemaining: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
			tc.setup(am)

			fullyDetached, err := am.RemoveNodeAttachment(context.Background(), tc.volumeID, tc.nodeID)

			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if fullyDetached != tc.expectDetached {
				t.Errorf("expected fullyDetached=%v, got %v", tc.expectDetached, fullyDetached)
			}

			nodeCount := am.GetNodeCount(tc.volumeID)
			if nodeCount != tc.expectRemaining {
				t.Errorf("expected %d remaining nodes, got %d", tc.expectRemaining, nodeCount)
			}
		})
	}
}

func TestAttachmentState_GetNodeIDs(t *testing.T) {
	state := &AttachmentState{
		VolumeID: "vol-1",
		Nodes: []NodeAttachment{
			{NodeID: "node-1"},
			{NodeID: "node-2"},
		},
	}

	ids := state.GetNodeIDs()
	if len(ids) != 2 {
		t.Errorf("expected 2 node IDs, got %d", len(ids))
	}
	if ids[0] != "node-1" || ids[1] != "node-2" {
		t.Errorf("unexpected node IDs: %v", ids)
	}
}

func TestAttachmentState_IsAttachedToNode(t *testing.T) {
	state := &AttachmentState{
		VolumeID: "vol-1",
		Nodes: []NodeAttachment{
			{NodeID: "node-1"},
		},
	}

	if !state.IsAttachedToNode("node-1") {
		t.Error("expected IsAttachedToNode(node-1) = true")
	}
	if state.IsAttachedToNode("node-2") {
		t.Error("expected IsAttachedToNode(node-2) = false")
	}
}

func TestAttachmentState_NodeCount(t *testing.T) {
	tests := []struct {
		name      string
		state     *AttachmentState
		wantCount int
	}{
		{
			name: "no nodes",
			state: &AttachmentState{
				VolumeID: "vol-1",
				Nodes:    []NodeAttachment{},
			},
			wantCount: 0,
		},
		{
			name: "one node",
			state: &AttachmentState{
				VolumeID: "vol-1",
				Nodes: []NodeAttachment{
					{NodeID: "node-1"},
				},
			},
			wantCount: 1,
		},
		{
			name: "two nodes",
			state: &AttachmentState{
				VolumeID: "vol-1",
				Nodes: []NodeAttachment{
					{NodeID: "node-1"},
					{NodeID: "node-2"},
				},
			},
			wantCount: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			count := tc.state.NodeCount()
			if count != tc.wantCount {
				t.Errorf("expected NodeCount=%d, got %d", tc.wantCount, count)
			}
		})
	}
}

func TestAddSecondaryAttachment_MigrationTracking(t *testing.T) {
	ctx := context.Background()
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")

	// Setup: create primary attachment
	volumeID := "pvc-test-migration"
	primaryNode := "node-primary"
	secondaryNode := "node-secondary"
	migrationTimeout := 5 * time.Minute

	err := am.TrackAttachmentWithMode(ctx, volumeID, primaryNode, "RWX")
	if err != nil {
		t.Fatalf("Failed to track primary attachment: %v", err)
	}

	// Verify no migration state initially
	state, _ := am.GetAttachment(volumeID)
	if state.MigrationStartedAt != nil {
		t.Error("MigrationStartedAt should be nil before secondary attachment")
	}

	// Add secondary attachment
	beforeAdd := time.Now()
	err = am.AddSecondaryAttachment(ctx, volumeID, secondaryNode, migrationTimeout)
	if err != nil {
		t.Fatalf("Failed to add secondary attachment: %v", err)
	}
	afterAdd := time.Now()

	// Verify migration tracking
	state, _ = am.GetAttachment(volumeID)

	if state.MigrationStartedAt == nil {
		t.Fatal("MigrationStartedAt should be set after secondary attachment")
	}

	if state.MigrationStartedAt.Before(beforeAdd) || state.MigrationStartedAt.After(afterAdd) {
		t.Errorf("MigrationStartedAt %v not in expected range [%v, %v]",
			state.MigrationStartedAt, beforeAdd, afterAdd)
	}

	if state.MigrationTimeout != migrationTimeout {
		t.Errorf("MigrationTimeout = %v, want %v", state.MigrationTimeout, migrationTimeout)
	}

	// Verify IsMigrating returns true
	if !state.IsMigrating() {
		t.Error("IsMigrating() should return true after secondary attachment")
	}
}

func TestRemoveNodeAttachment_ClearsMigrationState(t *testing.T) {
	ctx := context.Background()
	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")

	// Setup: create dual-attach migration scenario
	volumeID := "pvc-test-clear-migration"
	primaryNode := "node-primary"
	secondaryNode := "node-secondary"

	err := am.TrackAttachmentWithMode(ctx, volumeID, primaryNode, "RWX")
	if err != nil {
		t.Fatalf("Failed to track primary attachment: %v", err)
	}

	err = am.AddSecondaryAttachment(ctx, volumeID, secondaryNode, 5*time.Minute)
	if err != nil {
		t.Fatalf("Failed to add secondary attachment: %v", err)
	}

	// Verify migration is active
	state, _ := am.GetAttachment(volumeID)
	if !state.IsMigrating() {
		t.Fatal("Should be migrating after secondary attachment")
	}

	// Remove primary node (migration source) - simulates migration completion
	fullyDetached, err := am.RemoveNodeAttachment(ctx, volumeID, primaryNode)
	if err != nil {
		t.Fatalf("Failed to remove primary attachment: %v", err)
	}

	if fullyDetached {
		t.Error("Should not be fully detached - secondary still attached")
	}

	// Verify migration state cleared
	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Volume should still be tracked")
	}

	if state.MigrationStartedAt != nil {
		t.Error("MigrationStartedAt should be nil after migration completes")
	}

	if state.MigrationTimeout != 0 {
		t.Error("MigrationTimeout should be 0 after migration completes")
	}

	if state.IsMigrating() {
		t.Error("IsMigrating() should return false after migration completes")
	}

	// Verify secondary node is now the only attached node
	if len(state.Nodes) != 1 {
		t.Errorf("Should have 1 node, got %d", len(state.Nodes))
	}

	if state.Nodes[0].NodeID != secondaryNode {
		t.Errorf("Remaining node should be %s, got %s", secondaryNode, state.Nodes[0].NodeID)
	}
}
