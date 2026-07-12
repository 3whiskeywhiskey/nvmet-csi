package attachment

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/cache"
)

func TestNodeWatcher_UpdateFunc_ReadyToNotReady(t *testing.T) {
	// Create old node (Ready) - add to k8s before creating listers
	testNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}

	k8sClient := fake.NewSimpleClientset(testNode)
	nodeLister, pvLister := createTestListers(k8sClient, testNode)

	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	reconciler, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:    am,
		K8sClient:  k8sClient,
		NodeLister: nodeLister,
		PVLister:   pvLister,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	ctx := context.Background()
	err = reconciler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer reconciler.Stop()

	// Give reconciler time to start
	time.Sleep(10 * time.Millisecond)

	// Create a volume attached to the node
	volumeID := "pvc-test-watcher"
	nodeID := "test-node"
	err = am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}

	nw := NewNodeWatcher(reconciler, nil)
	handlers := nw.GetEventHandlers()

	// Create new node (NotReady)
	newNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			},
		},
	}

	// Trigger UpdateFunc
	handlers.UpdateFunc(testNode, newNode)

	// Give reconciler time to process trigger
	time.Sleep(50 * time.Millisecond)

	// Verify reconciliation was triggered (attachment should still exist because node exists in k8s)
	_, exists := am.GetAttachment(volumeID)
	if !exists {
		t.Error("Attachment should still exist (node exists in k8s, just NotReady)")
	}
}

func TestNodeWatcher_UpdateFunc_NoChange(t *testing.T) {
	// Create fake reconciler wrapper
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	reconciler, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:    am,
		K8sClient:  k8sClient,
		NodeLister: nodeLister,
		PVLister:   pvLister,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	nw := NewNodeWatcher(reconciler, nil)
	handlers := nw.GetEventHandlers()

	// Both nodes Ready - no change
	oldNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
	newNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}

	// Trigger UpdateFunc - should not trigger reconciliation
	handlers.UpdateFunc(oldNode, newNode)

	// Verify no trigger (we can't directly check with real reconciler, but no panic is good)
}

func TestNodeWatcher_UpdateFunc_NotReadyToNotReady(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	reconciler, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:    am,
		K8sClient:  k8sClient,
		NodeLister: nodeLister,
		PVLister:   pvLister,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	nw := NewNodeWatcher(reconciler, nil)
	handlers := nw.GetEventHandlers()

	// Both nodes NotReady - no change
	oldNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			},
		},
	}
	newNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			},
		},
	}

	// Trigger UpdateFunc - should not trigger reconciliation (no transition)
	handlers.UpdateFunc(oldNode, newNode)

	// No panic means success
}

func TestNodeWatcher_DeleteFunc_TriggersReconciliation(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	reconciler, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:     am,
		K8sClient:   k8sClient,
		NodeLister:  nodeLister,
		PVLister:    pvLister,
		GracePeriod: 1 * time.Nanosecond, // Very short for immediate cleanup
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	ctx := context.Background()
	err = reconciler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer reconciler.Stop()

	// Give reconciler time to start
	time.Sleep(10 * time.Millisecond)

	// Create a volume attached to the node
	volumeID := "pvc-test-delete"
	nodeID := "deleted-node"
	err = am.TrackAttachment(ctx, volumeID, nodeID)
	if err != nil {
		t.Fatalf("TrackAttachment failed: %v", err)
	}

	nw := NewNodeWatcher(reconciler, nil)
	handlers := nw.GetEventHandlers()

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "deleted-node"},
	}

	// Trigger DeleteFunc
	handlers.DeleteFunc(node)

	// Give reconciler time to process trigger
	time.Sleep(50 * time.Millisecond)

	// Verify reconciliation was triggered (attachment should be cleared because node not in k8s)
	_, exists := am.GetAttachment(volumeID)
	if exists {
		t.Error("Attachment should be cleared after node deletion")
	}
}

func TestNodeWatcher_DeleteFunc_HandlesTombstone(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	reconciler, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:    am,
		K8sClient:  k8sClient,
		NodeLister: nodeLister,
		PVLister:   pvLister,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	ctx := context.Background()
	err = reconciler.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer reconciler.Stop()

	// Give reconciler time to start
	time.Sleep(10 * time.Millisecond)

	nw := NewNodeWatcher(reconciler, nil)
	handlers := nw.GetEventHandlers()

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "tombstone-node"},
	}

	// Wrap in tombstone
	tombstone := cache.DeletedFinalStateUnknown{
		Key: "tombstone-node",
		Obj: node,
	}

	// Trigger DeleteFunc with tombstone - should not panic
	handlers.DeleteFunc(tombstone)

	// Give reconciler time to process
	time.Sleep(50 * time.Millisecond)

	// No panic means success
}

func TestNodeWatcher_NilMetrics(t *testing.T) {
	k8sClient := fake.NewSimpleClientset()
	nodeLister, pvLister := createTestListers(k8sClient)

	am := NewAttachmentManager(nil, "nvmet.csi.whiskey.works")
	reconciler, err := NewAttachmentReconciler(ReconcilerConfig{
		Manager:    am,
		K8sClient:  k8sClient,
		NodeLister: nodeLister,
		PVLister:   pvLister,
	})
	if err != nil {
		t.Fatalf("Failed to create reconciler: %v", err)
	}

	// Create watcher with nil metrics - should not panic
	nw := NewNodeWatcher(reconciler, nil)
	handlers := nw.GetEventHandlers()

	oldNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
			},
		},
	}
	newNode := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "test-node"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
			},
		},
	}

	// Trigger UpdateFunc - should not panic with nil metrics
	handlers.UpdateFunc(oldNode, newNode)

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "deleted-node"},
	}

	// Trigger DeleteFunc - should not panic with nil metrics
	handlers.DeleteFunc(node)

	// No panic means success
}

func TestIsNodeReady(t *testing.T) {
	tests := []struct {
		name     string
		node     *corev1.Node
		expected bool
	}{
		{
			name: "Node is Ready",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
					},
				},
			},
			expected: true,
		},
		{
			name: "Node is NotReady",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
					},
				},
			},
			expected: false,
		},
		{
			name: "Node has no Ready condition",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{},
				},
			},
			expected: false,
		},
		{
			name: "Node has Unknown Ready status",
			node: &corev1.Node{
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{Type: corev1.NodeReady, Status: corev1.ConditionUnknown},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isNodeReady(tt.node)
			if result != tt.expected {
				t.Errorf("isNodeReady() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
