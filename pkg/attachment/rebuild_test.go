package attachment

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// Helper functions for creating test objects

const driverName = "nvmet.csi.whiskey.works"

// createFakeVolumeAttachment creates a test VolumeAttachment with specified parameters.
func createFakeVolumeAttachment(name, attacher, volumeID, nodeID string, attached bool) *storagev1.VolumeAttachment {
	return &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.Now(),
		},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: attacher,
			NodeName: nodeID,
			Source: storagev1.VolumeAttachmentSource{
				PersistentVolumeName: &volumeID,
			},
		},
		Status: storagev1.VolumeAttachmentStatus{
			Attached: attached,
		},
	}
}

// createFakeVolumeAttachmentWithTime creates a VA with a specific creation timestamp.
func createFakeVolumeAttachmentWithTime(name, attacher, volumeID, nodeID string, attached bool, creationTime time.Time) *storagev1.VolumeAttachment {
	va := createFakeVolumeAttachment(name, attacher, volumeID, nodeID, attached)
	va.CreationTimestamp = metav1.NewTime(creationTime)
	return va
}

// createFakePV creates a test PersistentVolume with specified access modes.
func createFakePV(volumeID string, accessModes []corev1.PersistentVolumeAccessMode) *corev1.PersistentVolume {
	return &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: volumeID,
		},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: accessModes,
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       driverName,
					VolumeHandle: volumeID,
				},
			},
		},
	}
}

// createFakePVWithAnnotations creates a PV with attachment annotations.
func createFakePVWithAnnotations(volumeID, nodeID, attachedAt string) *corev1.PersistentVolume {
	pv := createFakePV(volumeID, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce})
	pv.Annotations = map[string]string{
		AnnotationAttachedNode: nodeID,
		AnnotationAttachedAt:   attachedAt,
	}
	return pv
}

// Task 1: Test basic VA-based rebuild scenarios

func TestRebuildStateFromVolumeAttachments_SingleAttachment(t *testing.T) {
	volumeID := "pvc-vol1"
	nodeID := "node-1"

	// Create fake VA and PV
	va := createFakeVolumeAttachment("va1", driverName, volumeID, nodeID, true)
	pv := createFakePV(volumeID, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce})

	// Create fake clientset
	client := fake.NewSimpleClientset(va, pv)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	// Verify attachment exists
	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist after rebuild")
	}

	// Verify correct volumeID
	if state.VolumeID != volumeID {
		t.Errorf("Expected volumeID %s, got %s", volumeID, state.VolumeID)
	}

	// Verify correct nodeID
	if state.NodeID != nodeID {
		t.Errorf("Expected nodeID %s, got %s", nodeID, state.NodeID)
	}

	// Verify Nodes slice
	if len(state.Nodes) != 1 {
		t.Fatalf("Expected 1 node, got %d", len(state.Nodes))
	}
	if state.Nodes[0].NodeID != nodeID {
		t.Errorf("Expected node[0].NodeID %s, got %s", nodeID, state.Nodes[0].NodeID)
	}

	// Verify access mode is RWO
	if state.AccessMode != "RWO" {
		t.Errorf("Expected AccessMode RWO, got %s", state.AccessMode)
	}

	// Verify not in migration
	if state.MigrationStartedAt != nil {
		t.Errorf("Expected no migration state, got MigrationStartedAt=%v", state.MigrationStartedAt)
	}
}

func TestRebuildStateFromVolumeAttachments_MultipleVolumes(t *testing.T) {
	vol1 := "pvc-vol1"
	vol2 := "pvc-vol2"
	vol3 := "pvc-vol3"

	// Create 3 VAs for different volumes
	va1 := createFakeVolumeAttachment("va1", driverName, vol1, "node-1", true)
	va2 := createFakeVolumeAttachment("va2", driverName, vol2, "node-2", true)
	va3 := createFakeVolumeAttachment("va3", driverName, vol3, "node-3", true)

	// Create corresponding PVs
	pv1 := createFakePV(vol1, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce})
	pv2 := createFakePV(vol2, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce})
	pv3 := createFakePV(vol3, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce})

	client := fake.NewSimpleClientset(va1, va2, va3, pv1, pv2, pv3)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	// Verify all 3 attachments exist
	attachments := am.ListAttachments()
	if len(attachments) != 3 {
		t.Fatalf("Expected 3 attachments, got %d", len(attachments))
	}

	// Verify each volume
	for volumeID, expectedNode := range map[string]string{
		vol1: "node-1",
		vol2: "node-2",
		vol3: "node-3",
	} {
		state, exists := am.GetAttachment(volumeID)
		if !exists {
			t.Errorf("Expected attachment for volume %s to exist", volumeID)
			continue
		}
		if state.NodeID != expectedNode {
			t.Errorf("Volume %s: expected node %s, got %s", volumeID, expectedNode, state.NodeID)
		}
	}
}

func TestRebuildStateFromVolumeAttachments_NoAttachments(t *testing.T) {
	// Create fake clientset with no VolumeAttachments
	client := fake.NewSimpleClientset()
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	// Verify no attachments
	attachments := am.ListAttachments()
	if len(attachments) != 0 {
		t.Errorf("Expected 0 attachments, got %d", len(attachments))
	}
}

func TestRebuildStateFromVolumeAttachments_DetachedVA(t *testing.T) {
	volumeID := "pvc-vol1"

	// Create detached VA (attached=false)
	va := createFakeVolumeAttachment("va1", driverName, volumeID, "node-1", false)
	pv := createFakePV(volumeID, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce})

	client := fake.NewSimpleClientset(va, pv)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	// Verify detached VA is NOT included
	state, exists := am.GetAttachment(volumeID)
	if exists {
		t.Errorf("Expected detached VA to NOT be in state, but found: %+v", state)
	}
}

func TestRebuildStateFromVolumeAttachments_OtherDriverVA(t *testing.T) {
	volumeID := "pvc-vol1"
	otherDriver := "ebs.csi.aws.com"

	// Create VA for different driver
	va := createFakeVolumeAttachment("va1", otherDriver, volumeID, "node-1", true)
	pv := createFakePV(volumeID, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce})

	client := fake.NewSimpleClientset(va, pv)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	// Verify other driver VA is NOT included
	state, exists := am.GetAttachment(volumeID)
	if exists {
		t.Errorf("Expected other driver VA to NOT be in state, but found: %+v", state)
	}
}

// Task 2: Test migration detection from dual VolumeAttachments

func TestRebuildStateFromVolumeAttachments_MigrationState(t *testing.T) {
	volumeID := "pvc-vol1"
	node1 := "node-1"
	node2 := "node-2"

	// Create 2 VAs for SAME volume (different nodes) - simulates migration
	now := time.Now()
	older := now.Add(-5 * time.Minute)

	va1 := createFakeVolumeAttachmentWithTime("va1", driverName, volumeID, node1, true, older)
	va2 := createFakeVolumeAttachmentWithTime("va2", driverName, volumeID, node2, true, now)

	// Create PV with ReadWriteMany (migration requires RWX)
	pv := createFakePV(volumeID, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})

	client := fake.NewSimpleClientset(va1, va2, pv)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	// Verify attachment exists
	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist after rebuild")
	}

	// Verify 2 nodes
	if len(state.Nodes) != 2 {
		t.Fatalf("Expected 2 nodes for migration, got %d", len(state.Nodes))
	}

	// Verify both nodes present
	nodeIDs := state.GetNodeIDs()
	if len(nodeIDs) != 2 {
		t.Fatalf("Expected 2 node IDs, got %d", len(nodeIDs))
	}
	foundNode1 := false
	foundNode2 := false
	for _, nid := range nodeIDs {
		if nid == node1 {
			foundNode1 = true
		}
		if nid == node2 {
			foundNode2 = true
		}
	}
	if !foundNode1 {
		t.Errorf("Expected to find node %s in Nodes", node1)
	}
	if !foundNode2 {
		t.Errorf("Expected to find node %s in Nodes", node2)
	}

	// Verify MigrationStartedAt is set to older VA's timestamp
	if state.MigrationStartedAt == nil {
		t.Fatal("Expected MigrationStartedAt to be set for migration state")
	}
	if !state.MigrationStartedAt.Equal(older) {
		t.Errorf("Expected MigrationStartedAt=%v, got %v", older, *state.MigrationStartedAt)
	}

	// Verify AccessMode is RWX
	if state.AccessMode != "RWX" {
		t.Errorf("Expected AccessMode RWX, got %s", state.AccessMode)
	}

	// Verify IsMigrating returns true
	if !state.IsMigrating() {
		t.Error("Expected IsMigrating() to return true")
	}
}

func TestRebuildStateFromVolumeAttachments_MigrationTimestamp(t *testing.T) {
	volumeID := "pvc-vol1"

	// Create 2 VAs with different CreationTimestamps
	now := time.Now()
	older := now.Add(-10 * time.Minute)
	newer := now.Add(-2 * time.Minute)

	va1 := createFakeVolumeAttachmentWithTime("va1", driverName, volumeID, "node-1", true, newer)
	va2 := createFakeVolumeAttachmentWithTime("va2", driverName, volumeID, "node-2", true, older)

	pv := createFakePV(volumeID, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})

	client := fake.NewSimpleClientset(va1, va2, pv)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist")
	}

	// Verify MigrationStartedAt equals OLDER VA's timestamp (not newer)
	if state.MigrationStartedAt == nil {
		t.Fatal("Expected MigrationStartedAt to be set")
	}
	if !state.MigrationStartedAt.Equal(older) {
		t.Errorf("Expected MigrationStartedAt to be older timestamp %v, got %v", older, *state.MigrationStartedAt)
	}
}

func TestRebuildStateFromVolumeAttachments_MoreThanTwoVAs(t *testing.T) {
	volumeID := "pvc-vol1"

	// Create 3 VAs for same volume (anomaly case)
	now := time.Now()
	va1 := createFakeVolumeAttachmentWithTime("va1", driverName, volumeID, "node-1", true, now.Add(-15*time.Minute))
	va2 := createFakeVolumeAttachmentWithTime("va2", driverName, volumeID, "node-2", true, now.Add(-10*time.Minute))
	va3 := createFakeVolumeAttachmentWithTime("va3", driverName, volumeID, "node-3", true, now.Add(-5*time.Minute))

	pv := createFakePV(volumeID, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany})

	client := fake.NewSimpleClientset(va1, va2, va3, pv)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state (should log warning but continue)
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist")
	}

	// Verify only first 2 VAs are used
	if len(state.Nodes) != 2 {
		t.Errorf("Expected only 2 nodes (first 2 VAs), got %d", len(state.Nodes))
	}

	// Warning should be logged (verified by manual inspection or log capture)
	// Here we just ensure rebuild doesn't fail
}

func TestRebuildStateFromVolumeAttachments_AccessModeFallback(t *testing.T) {
	volumeID := "pvc-vol1"

	// Create VA but NO corresponding PV
	va := createFakeVolumeAttachment("va1", driverName, volumeID, "node-1", true)

	// Client has VA but no PV
	client := fake.NewSimpleClientset(va)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist")
	}

	// Verify AccessMode defaults to RWO (conservative default)
	if state.AccessMode != "RWO" {
		t.Errorf("Expected AccessMode to default to RWO when PV not found, got %s", state.AccessMode)
	}

	// Verify no error - graceful handling
	// (already verified by err check above)
}

// Task 3: Test backward compatibility with stale annotations
//
// These tests verify that VolumeAttachment is the authoritative source of truth,
// and PV annotations are ignored during rebuild even when they contradict VA state.

func TestRebuildStateFromVolumeAttachments_IgnoresStaleAnnotations(t *testing.T) {
	volumeID := "pvc-vol1"
	correctNode := "node-2"
	staleNode := "node-1"

	// Create PV with stale attachment annotations (different node than VA)
	pv := createFakePVWithAnnotations(volumeID, staleNode, time.Now().Add(-1*time.Hour).Format(metav1.RFC3339Micro))

	// Create VA pointing to correct node
	va := createFakeVolumeAttachment("va1", driverName, volumeID, correctNode, true)

	client := fake.NewSimpleClientset(va, pv)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist")
	}

	// Verify state uses VA's node, NOT annotation's node
	if state.NodeID != correctNode {
		t.Errorf("Expected nodeID from VA (%s), got %s (from stale annotation?)", correctNode, state.NodeID)
	}

	// Verify stale node is NOT in state
	if state.IsAttachedToNode(staleNode) {
		t.Errorf("Expected stale node %s to NOT be in state, but IsAttachedToNode returned true", staleNode)
	}

	// Verify correct node IS in state
	if !state.IsAttachedToNode(correctNode) {
		t.Errorf("Expected correct node %s to be in state", correctNode)
	}
}

func TestRebuildStateFromVolumeAttachments_NoVAButHasAnnotation(t *testing.T) {
	volumeID := "pvc-vol1"

	// Create PV with attachment annotations but NO VolumeAttachment
	pv := createFakePVWithAnnotations(volumeID, "node-1", time.Now().Format(metav1.RFC3339Micro))

	// Client has PV but no VA
	client := fake.NewSimpleClientset(pv)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	// Verify volume is NOT in rebuilt state
	state, exists := am.GetAttachment(volumeID)
	if exists {
		t.Errorf("Expected volume to NOT be in state (no VA), but found: %+v", state)
	}

	// Previously (with annotation-based rebuild) this would have been rebuilt.
	// Now correctly absent because VolumeAttachment is authoritative.
}

func TestRebuildStateFromVolumeAttachments_VAAndMatchingAnnotation(t *testing.T) {
	volumeID := "pvc-vol1"
	nodeID := "node-1"
	attachedAt := time.Now().Add(-30 * time.Minute)

	// Create VA and PV with matching annotations (happy path)
	va := createFakeVolumeAttachmentWithTime("va1", driverName, volumeID, nodeID, true, attachedAt)
	pv := createFakePVWithAnnotations(volumeID, nodeID, attachedAt.Format(metav1.RFC3339Micro))

	client := fake.NewSimpleClientset(va, pv)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Rebuild state
	err := am.RebuildStateFromVolumeAttachments(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	// Verify rebuild works (happy path)
	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist")
	}

	if state.NodeID != nodeID {
		t.Errorf("Expected nodeID %s, got %s", nodeID, state.NodeID)
	}
}

func TestRebuildStateFromAnnotations_StillWorks(t *testing.T) {
	// Test the deprecated function still works for backward compatibility
	volumeID := "pvc-vol1"
	nodeID := "node-1"
	attachedAt := time.Now().Add(-1 * time.Hour)

	// Create PV with annotations, no VA
	pv := createFakePVWithAnnotations(volumeID, nodeID, attachedAt.Format(metav1.RFC3339Micro))

	client := fake.NewSimpleClientset(pv)
	am := NewAttachmentManager(client, "nvmet.csi.whiskey.works")

	// Call RebuildStateFromAnnotations (deprecated)
	err := am.RebuildStateFromAnnotations(context.Background())
	if err != nil {
		t.Fatalf("RebuildStateFromAnnotations failed: %v", err)
	}

	// Verify it rebuilds from annotations
	state, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Fatal("Expected attachment to exist from annotation-based rebuild")
	}

	if state.NodeID != nodeID {
		t.Errorf("Expected nodeID %s, got %s", nodeID, state.NodeID)
	}

	// Ensures we didn't break the fallback (even though it's deprecated)
}

// TestAttachmentManager_DriverNameIsolation_Rebuild is the core regression test
// for configurable CSI driver-name isolation in attachment state rebuild.
// It constructs two attached VolumeAttachments + PVs using different driver names
// (production "nvmet.csi.whiskey.works" vs custom "example-rds.invalid").
// Using the desired explicit constructor NewAttachmentManager(k8sClient, driverName)
// and calling Initialize (which calls RebuildStateFromVolumeAttachments), it asserts
// that only the configured driver's volume appears in the manager state.
// The assertions are written so that any cross-driver contamination would cause failure.
func TestAttachmentManager_DriverNameIsolation_Rebuild(t *testing.T) {
	prodDriver := "nvmet.csi.whiskey.works"
	testDriver := "example-rds.invalid"

	prodVol := "pvc-prod-volume"
	testVol := "pvc-test-volume"
	prodNode := "node-prod"
	testNode := "node-test"

	// Attached VAs for both drivers (attacher field carries the driver identity for VA)
	vaProd := createFakeVolumeAttachment("va-prod", prodDriver, prodVol, prodNode, true)
	vaTest := createFakeVolumeAttachment("va-test", testDriver, testVol, testNode, true)

	// PVs must be built with explicit CSI.Driver; we do not call createFakePV
	// (which hard-codes the package driverName const) to keep this test self-contained.
	pvProd := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: prodVol},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       prodDriver,
					VolumeHandle: prodVol,
				},
			},
		},
	}
	pvTest := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: testVol},
		Spec: corev1.PersistentVolumeSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					Driver:       testDriver,
					VolumeHandle: testVol,
				},
			},
		},
	}

	client := fake.NewSimpleClientset(vaProd, vaTest, pvProd, pvTest)

	// DESIRED explicit API: NewAttachmentManager now takes driverName.
	// This call (and the one below) will cause compile failure until parent
	// updates the production signature.
	am := NewAttachmentManager(client, testDriver)

	// Exercise both entry points mentioned in the contract.
	if err := am.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize failed: %v", err)
	}

	// Direct rebuild should be a no-op second time but must still respect driver.
	if err := am.RebuildStateFromVolumeAttachments(context.Background()); err != nil {
		t.Fatalf("RebuildStateFromVolumeAttachments failed: %v", err)
	}

	// === Assertions that MUST fail on cross-driver contamination ===

	// Configured driver's volume must be tracked.
	state, exists := am.GetAttachment(testVol)
	if !exists {
		t.Fatal("expected attachment for test-driver volume to be tracked after rebuild")
	}
	if state.NodeID != testNode {
		t.Errorf("test volume: expected node %s, got %s", testNode, state.NodeID)
	}
	if state.VolumeID != testVol {
		t.Errorf("test volume: expected VolumeID %s, got %s", testVol, state.VolumeID)
	}

	// Production driver's volume must NOT be present (the critical isolation assertion).
	if _, exists := am.GetAttachment(prodVol); exists {
		t.Error("cross-driver contamination: production volume appeared in AttachmentManager for test driver")
	}

	// ListAttachments must contain exactly the one configured volume.
	all := am.ListAttachments()
	if len(all) != 1 {
		t.Fatalf("expected exactly 1 attachment after rebuild with test driver, got %d", len(all))
	}
	if _, hasProd := all[prodVol]; hasProd {
		t.Error("cross-driver contamination: production volume present in ListAttachments()")
	}
	if _, hasTest := all[testVol]; !hasTest {
		t.Error("expected test volume to be present in ListAttachments()")
	}
}
