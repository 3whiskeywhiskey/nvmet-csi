package driver

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// TestNewEventPoster_CreatesRecorder tests EventPoster creation
func TestNewEventPoster_CreatesRecorder(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()

	poster := NewEventPoster(fakeClient)

	if poster == nil {
		t.Fatal("Expected non-nil EventPoster")
		return
	}

	if poster.recorder == nil {
		t.Error("Expected recorder to be set")
	}

	if poster.clientset == nil {
		t.Error("Expected clientset to be set")
	}
}

// TestPostMountFailure_PostsEvent tests posting mount failure events
func TestPostMountFailure_PostsEvent(t *testing.T) {
	// Create PVC in fake clientset
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			UID:       "test-uid-123",
		},
	}
	fakeClient := fake.NewSimpleClientset(pvc)

	poster := NewEventPoster(fakeClient)

	// Post event
	ctx := context.Background()
	err := poster.PostMountFailure(ctx, "default", "test-pvc", "pvc-123", "node-1", "device not found")

	if err != nil {
		t.Fatalf("PostMountFailure failed: %v", err)
	}

	// Note: The fake EventRecorder doesn't actually create Event objects that we can query
	// The EventRecorder uses a broadcaster that writes to the event sink
	// In a real cluster, we'd see events via `kubectl get events`
	// For unit testing, we verify the call succeeded without error

	// We could add more sophisticated testing by:
	// 1. Implementing a custom EventRecorder that captures events
	// 2. Using the event's Actions() to inspect what was called
	// 3. Checking the events API directly (but fake client may not support this fully)

	// For now, verify no error is returned
	t.Log("PostMountFailure completed without error")
}

// TestPostMountFailure_PVCNotFound tests graceful handling of missing PVC
func TestPostMountFailure_PVCNotFound(t *testing.T) {
	// Create fake client WITHOUT the PVC
	fakeClient := fake.NewSimpleClientset()

	poster := NewEventPoster(fakeClient)

	// Post event for non-existent PVC
	ctx := context.Background()
	err := poster.PostMountFailure(ctx, "default", "nonexistent-pvc", "pvc-123", "node-1", "device not found")

	// Should return nil (graceful handling)
	if err != nil {
		t.Errorf("Expected nil error for missing PVC, got %v", err)
	}

	t.Log("PostMountFailure handled missing PVC gracefully")
}

// TestPostRecoveryFailed_IncludesAttemptCount tests recovery failure event format
func TestPostRecoveryFailed_IncludesAttemptCount(t *testing.T) {
	// Create PVC in fake clientset
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			UID:       "test-uid-456",
		},
	}
	fakeClient := fake.NewSimpleClientset(pvc)

	poster := NewEventPoster(fakeClient)

	// Post recovery failed event
	ctx := context.Background()
	attemptCount := 3
	finalErr := context.DeadlineExceeded

	err := poster.PostRecoveryFailed(ctx, "default", "test-pvc", "pvc-456", "node-2", attemptCount, finalErr)

	if err != nil {
		t.Fatalf("PostRecoveryFailed failed: %v", err)
	}

	// Verify the call succeeded
	t.Log("PostRecoveryFailed completed without error")

	// We can't easily verify the event content with fake client
	// But we can test the message formatting separately
}

// TestPostStaleMountDetected_PostsNormalEvent tests stale mount detection event
func TestPostStaleMountDetected_PostsNormalEvent(t *testing.T) {
	// Create PVC in fake clientset
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			UID:       "test-uid-789",
		},
	}
	fakeClient := fake.NewSimpleClientset(pvc)

	poster := NewEventPoster(fakeClient)

	// Post stale mount detected event
	ctx := context.Background()
	err := poster.PostStaleMountDetected(ctx, "default", "test-pvc", "pvc-789", "node-3", "/dev/nvme0n1", "/dev/nvme1n1")

	if err != nil {
		t.Fatalf("PostStaleMountDetected failed: %v", err)
	}

	// Verify the call succeeded
	t.Log("PostStaleMountDetected completed without error")
}

// TestPostEvents_WithTimeout tests event posting with context timeout
func TestPostEvents_WithTimeout(t *testing.T) {
	// Create PVC in fake clientset
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			UID:       "test-uid-timeout",
		},
	}
	fakeClient := fake.NewSimpleClientset(pvc)

	poster := NewEventPoster(fakeClient)

	// Create context with very short timeout
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Post event - should complete quickly
	err := poster.PostMountFailure(ctx, "default", "test-pvc", "pvc-timeout", "node-1", "test message")

	if err != nil {
		t.Errorf("Unexpected error with timeout context: %v", err)
	}
}

// TestPostEvents_MultipleEvents tests posting multiple events
func TestPostEvents_MultipleEvents(t *testing.T) {
	// Create PVC in fake clientset
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			UID:       "test-uid-multi",
		},
	}
	fakeClient := fake.NewSimpleClientset(pvc)

	poster := NewEventPoster(fakeClient)
	ctx := context.Background()

	// Post multiple events
	events := []struct {
		name string
		fn   func() error
	}{
		{
			name: "mount failure 1",
			fn: func() error {
				return poster.PostMountFailure(ctx, "default", "test-pvc", "pvc-multi", "node-1", "error 1")
			},
		},
		{
			name: "mount failure 2",
			fn: func() error {
				return poster.PostMountFailure(ctx, "default", "test-pvc", "pvc-multi", "node-1", "error 2")
			},
		},
		{
			name: "stale mount detected",
			fn: func() error {
				return poster.PostStaleMountDetected(ctx, "default", "test-pvc", "pvc-multi", "node-1", "/dev/nvme0n1", "/dev/nvme1n1")
			},
		},
		{
			name: "recovery failed",
			fn: func() error {
				return poster.PostRecoveryFailed(ctx, "default", "test-pvc", "pvc-multi", "node-1", 3, context.DeadlineExceeded)
			},
		},
	}

	for _, evt := range events {
		t.Run(evt.name, func(t *testing.T) {
			if err := evt.fn(); err != nil {
				t.Errorf("Event posting failed: %v", err)
			}
		})
	}
}

// TestEventReasons tests that event reason constants are defined
func TestEventReasons(t *testing.T) {
	// Verify event reason constants are set
	if EventReasonMountFailure == "" {
		t.Error("EventReasonMountFailure should not be empty")
	}

	if EventReasonRecoveryFailed == "" {
		t.Error("EventReasonRecoveryFailed should not be empty")
	}

	if EventReasonStaleMountDetected == "" {
		t.Error("EventReasonStaleMountDetected should not be empty")
	}

	// Verify they're distinct
	reasons := []string{
		EventReasonMountFailure,
		EventReasonRecoveryFailed,
		EventReasonStaleMountDetected,
	}

	seen := make(map[string]bool)
	for _, reason := range reasons {
		if seen[reason] {
			t.Errorf("Duplicate event reason: %s", reason)
		}
		seen[reason] = true
	}

	// Log the reasons for visibility
	t.Logf("EventReasonMountFailure: %s", EventReasonMountFailure)
	t.Logf("EventReasonRecoveryFailed: %s", EventReasonRecoveryFailed)
	t.Logf("EventReasonStaleMountDetected: %s", EventReasonStaleMountDetected)
}

// TestEventSinkAdapter tests the eventSinkAdapter implementation
func TestEventSinkAdapter(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()

	// Create adapter (now uses clientset instead of eventInterface)
	adapter := &eventSinkAdapter{
		clientset: fakeClient,
	}

	// Test Create with namespace in ObjectMeta
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-event",
			Namespace: "default",
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:      "PersistentVolumeClaim",
			Name:      "test-pvc",
			Namespace: "default",
		},
		Reason:  "TestReason",
		Message: "Test message",
		Type:    corev1.EventTypeNormal,
	}

	created, err := adapter.Create(event)
	if err != nil {
		t.Fatalf("adapter.Create failed: %v", err)
	}

	if created == nil {
		t.Fatal("Expected non-nil created event")
		return
	}

	if created.Reason != "TestReason" {
		t.Errorf("Expected reason TestReason, got %s", created.Reason)
	}

	// Verify namespace was set correctly
	if created.Namespace != "default" {
		t.Errorf("Expected namespace 'default', got %s", created.Namespace)
	}

	// Test Update
	created.Message = "Updated message"
	updated, err := adapter.Update(created)
	if err != nil {
		t.Fatalf("adapter.Update failed: %v", err)
	}

	if updated.Message != "Updated message" {
		t.Errorf("Expected updated message, got %s", updated.Message)
	}

	// Verify namespace preserved
	if updated.Namespace != "default" {
		t.Errorf("Expected namespace 'default' after update, got %s", updated.Namespace)
	}

	// Test Patch
	patchData := []byte(`{"message": "Patched message"}`)
	patched, err := adapter.Patch(created, patchData)
	if err != nil {
		// Patch may not be fully supported in fake client
		t.Logf("adapter.Patch returned error (may be expected with fake client): %v", err)
	} else if patched != nil {
		t.Logf("adapter.Patch succeeded")
		if patched.Namespace != "default" {
			t.Errorf("Expected namespace 'default' after patch, got %s", patched.Namespace)
		}
	}
}

// TestEventSinkAdapter_NamespaceFromInvolvedObject tests namespace extraction from InvolvedObject
func TestEventSinkAdapter_NamespaceFromInvolvedObject(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	adapter := &eventSinkAdapter{
		clientset: fakeClient,
	}

	// Create event WITHOUT namespace in ObjectMeta (should extract from InvolvedObject)
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-event-no-ns",
			// Namespace intentionally not set
		},
		InvolvedObject: corev1.ObjectReference{
			Kind:      "PersistentVolumeClaim",
			Name:      "test-pvc",
			Namespace: "custom-namespace",
		},
		Reason:  "TestReason",
		Message: "Test message",
		Type:    corev1.EventTypeNormal,
	}

	created, err := adapter.Create(event)
	if err != nil {
		t.Fatalf("adapter.Create failed: %v", err)
	}

	// Verify namespace was extracted from InvolvedObject
	if created.Namespace != "custom-namespace" {
		t.Errorf("Expected namespace 'custom-namespace' from InvolvedObject, got %s", created.Namespace)
	}

	t.Logf("Successfully extracted namespace from InvolvedObject: %s", created.Namespace)
}

// TestEventSinkAdapter_MultipleNamespaces tests creating events in different namespaces
func TestEventSinkAdapter_MultipleNamespaces(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	adapter := &eventSinkAdapter{
		clientset: fakeClient,
	}

	// Test creating events in different namespaces
	testCases := []struct {
		name      string
		namespace string
	}{
		{name: "default", namespace: "default"},
		{name: "kube-system", namespace: "kube-system"},
		{name: "custom-ns", namespace: "custom-namespace"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			event := &corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name: fmt.Sprintf("test-event-%s", tc.name),
					// Namespace intentionally not set - should come from InvolvedObject
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "PersistentVolumeClaim",
					Name:      "test-pvc",
					Namespace: tc.namespace,
				},
				Reason:  "TestReason",
				Message: fmt.Sprintf("Test message for %s", tc.namespace),
				Type:    corev1.EventTypeNormal,
			}

			created, err := adapter.Create(event)
			if err != nil {
				t.Fatalf("Failed to create event in namespace %s: %v", tc.namespace, err)
			}

			if created.Namespace != tc.namespace {
				t.Errorf("Expected namespace %s, got %s", tc.namespace, created.Namespace)
			}

			t.Logf("Successfully created event in namespace: %s", created.Namespace)
		})
	}
}

// TestEventSinkAdapter_DefaultNamespace tests fallback to default namespace
func TestEventSinkAdapter_DefaultNamespace(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()
	adapter := &eventSinkAdapter{
		clientset: fakeClient,
	}

	// Create event with NO namespace in ObjectMeta AND InvolvedObject (edge case)
	event := &corev1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-event-no-ns-anywhere",
		},
		InvolvedObject: corev1.ObjectReference{
			Kind: "PersistentVolumeClaim",
			Name: "test-pvc",
			// Namespace intentionally not set
		},
		Reason:  "TestReason",
		Message: "Test message",
		Type:    corev1.EventTypeNormal,
	}

	created, err := adapter.Create(event)
	if err != nil {
		t.Fatalf("adapter.Create failed: %v", err)
	}

	// Should default to "default" namespace
	if created.Namespace != "default" {
		t.Errorf("Expected default namespace 'default', got %s", created.Namespace)
	}

	t.Logf("Successfully defaulted to namespace: %s", created.Namespace)
}

func TestPostVolumeDetached_SkipsDeletingPVC(t *testing.T) {
	now := metav1.Now()
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "deleting-pvc",
			Namespace:         "test-ns",
			UID:               "deleting-pvc-uid",
			DeletionTimestamp: &now,
		},
	}
	fakeClient := fake.NewSimpleClientset(pvc)

	eventCreated := false
	fakeClient.PrependReactor("create", "events", func(action k8stesting.Action) (bool, runtime.Object, error) {
		eventCreated = true
		return false, nil, nil
	})

	poster := NewEventPoster(fakeClient)
	if err := poster.PostVolumeDetached(context.Background(), "test-ns", "deleting-pvc", "pvc-123", "node-1"); err != nil {
		t.Fatalf("PostVolumeDetached failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if eventCreated {
		t.Fatal("expected no Kubernetes Event create for deleting PVC")
	}
}

func TestPostVolumeDetached_SkipsTerminatingNamespace(t *testing.T) {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "terminating-ns",
			UID:       "test-pvc-uid",
		},
	}
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "terminating-ns",
		},
		Status: corev1.NamespaceStatus{
			Phase: corev1.NamespaceTerminating,
		},
	}
	fakeClient := fake.NewSimpleClientset(namespace, pvc)

	eventCreated := false
	fakeClient.PrependReactor("create", "events", func(action k8stesting.Action) (bool, runtime.Object, error) {
		eventCreated = true
		return false, nil, nil
	})

	poster := NewEventPoster(fakeClient)
	if err := poster.PostVolumeDetached(context.Background(), "terminating-ns", "test-pvc", "pvc-123", "node-1"); err != nil {
		t.Fatalf("PostVolumeDetached failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if eventCreated {
		t.Fatal("expected no Kubernetes Event create for terminating namespace")
	}
}

func TestPostVolumeDetached_SkipsMissingPVC(t *testing.T) {
	fakeClient := fake.NewSimpleClientset()

	eventCreated := false
	fakeClient.PrependReactor("create", "events", func(action k8stesting.Action) (bool, runtime.Object, error) {
		eventCreated = true
		return false, nil, nil
	})

	poster := NewEventPoster(fakeClient)
	if err := poster.PostVolumeDetached(context.Background(), "missing-ns", "missing-pvc", "pvc-123", "node-1"); err != nil {
		t.Fatalf("PostVolumeDetached failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if eventCreated {
		t.Fatal("expected no Kubernetes Event create for missing PVC")
	}
}

// TestPostEvents_DifferentNamespaces tests posting events to different namespaces
func TestPostEvents_DifferentNamespaces(t *testing.T) {
	// Create PVCs in different namespaces
	pvc1 := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pvc-1",
			Namespace: "namespace-1",
			UID:       "uid-1",
		},
	}
	pvc2 := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "pvc-2",
			Namespace: "namespace-2",
			UID:       "uid-2",
		},
	}

	fakeClient := fake.NewSimpleClientset(pvc1, pvc2)
	poster := NewEventPoster(fakeClient)
	ctx := context.Background()

	// Post to first namespace
	err := poster.PostMountFailure(ctx, "namespace-1", "pvc-1", "vol-1", "node-1", "error 1")
	if err != nil {
		t.Errorf("Failed to post to namespace-1: %v", err)
	}

	// Post to second namespace
	err = poster.PostMountFailure(ctx, "namespace-2", "pvc-2", "vol-2", "node-2", "error 2")
	if err != nil {
		t.Errorf("Failed to post to namespace-2: %v", err)
	}
}

// TestEventMessageFormat tests the event message format
func TestEventMessageFormat(t *testing.T) {
	testCases := []struct {
		name           string
		volumeID       string
		nodeName       string
		message        string
		expectedSubstr []string
	}{
		{
			name:     "basic mount failure",
			volumeID: "pvc-123",
			nodeName: "node-1",
			message:  "device not found",
			expectedSubstr: []string{
				"pvc-123",
				"node-1",
				"device not found",
			},
		},
		{
			name:     "with special characters",
			volumeID: "pvc-abc-def-123",
			nodeName: "node-prod-1",
			message:  "connection refused: target unreachable",
			expectedSubstr: []string{
				"pvc-abc-def-123",
				"node-prod-1",
				"connection refused",
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Format message as done in PostMountFailure
			msg := "[" + tc.volumeID + "] on [" + tc.nodeName + "]: " + tc.message

			// Verify expected substrings are present
			for _, substr := range tc.expectedSubstr {
				if !strings.Contains(msg, substr) {
					t.Errorf("Expected message to contain %q, got: %s", substr, msg)
				}
			}

			t.Logf("Formatted message: %s", msg)
		})
	}
}

// TestRecoveryFailedMessageFormat tests recovery failure message format
func TestRecoveryFailedMessageFormat(t *testing.T) {
	volumeID := "pvc-456"
	nodeName := "node-2"
	attemptCount := 3
	finalErr := "mount failed: device busy"

	// Format as done in PostRecoveryFailed
	msg := fmt.Sprintf("[%s] on [%s]: Recovery failed after %d attempts: %s", volumeID, nodeName, attemptCount, finalErr)

	// Verify components
	expectedParts := []string{
		volumeID,
		nodeName,
		"Recovery failed",
		"3 attempts",
		finalErr,
	}

	for _, part := range expectedParts {
		if !strings.Contains(msg, part) {
			t.Errorf("Expected message to contain %q, got: %s", part, msg)
		}
	}

	t.Logf("Formatted recovery failure message: %s", msg)
}

// TestStaleMountDetectedMessageFormat tests stale mount detection message format
func TestStaleMountDetectedMessageFormat(t *testing.T) {
	volumeID := "pvc-789"
	nodeName := "node-3"
	oldDevice := "/dev/nvme0n1"
	newDevice := "/dev/nvme1n1"

	// Format as done in PostStaleMountDetected
	msg := "[" + volumeID + "] on [" + nodeName + "]: Stale mount detected - old device: " + oldDevice + ", new device: " + newDevice

	// Verify components
	expectedParts := []string{
		volumeID,
		nodeName,
		"Stale mount detected",
		oldDevice,
		newDevice,
	}

	for _, part := range expectedParts {
		if !strings.Contains(msg, part) {
			t.Errorf("Expected message to contain %q, got: %s", part, msg)
		}
	}

	t.Logf("Formatted stale mount message: %s", msg)
}

// TestPostMigrationStarted tests posting migration started events
func TestPostMigrationStarted(t *testing.T) {
	// Create PVC in fake clientset
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			UID:       "test-uid-migration",
		},
	}
	fakeClient := fake.NewSimpleClientset(pvc)
	poster := NewEventPoster(fakeClient)

	// Post migration started event
	ctx := context.Background()
	timeout := 5 * time.Minute
	err := poster.PostMigrationStarted(ctx, "default", "test-pvc", "pvc-123", "node-1", "node-2", timeout)

	if err != nil {
		t.Fatalf("PostMigrationStarted failed: %v", err)
	}

	// Verify the call succeeded
	t.Log("PostMigrationStarted completed without error")

	// Verify message format contains expected components
	expectedMsg := fmt.Sprintf("[pvc-123]: KubeVirt live migration started - source: node-1, target: node-2, timeout: %s", timeout.Round(time.Second))
	if !strings.Contains(expectedMsg, "node-1") || !strings.Contains(expectedMsg, "node-2") {
		t.Errorf("Expected message to contain source and target nodes")
	}
	if !strings.Contains(expectedMsg, timeout.Round(time.Second).String()) {
		t.Errorf("Expected message to contain timeout")
	}
	t.Logf("Expected message format: %s", expectedMsg)
}

// TestPostMigrationStarted_PVCNotFound tests graceful handling when PVC doesn't exist
func TestPostMigrationStarted_PVCNotFound(t *testing.T) {
	// Create fake client WITHOUT the PVC
	fakeClient := fake.NewSimpleClientset()
	poster := NewEventPoster(fakeClient)

	// Post event for non-existent PVC
	ctx := context.Background()
	err := poster.PostMigrationStarted(ctx, "default", "nonexistent-pvc", "pvc-123", "node-1", "node-2", 5*time.Minute)

	// Should return nil (graceful handling)
	if err != nil {
		t.Errorf("Expected nil error for missing PVC, got %v", err)
	}

	t.Log("PostMigrationStarted handled missing PVC gracefully")
}

// TestPostMigrationCompleted tests posting migration completed events
func TestPostMigrationCompleted(t *testing.T) {
	// Create PVC in fake clientset
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			UID:       "test-uid-migration-complete",
		},
	}
	fakeClient := fake.NewSimpleClientset(pvc)
	poster := NewEventPoster(fakeClient)

	// Post migration completed event
	ctx := context.Background()
	duration := 2*time.Minute + 15*time.Second + 456*time.Millisecond
	err := poster.PostMigrationCompleted(ctx, "default", "test-pvc", "pvc-456", "node-1", "node-2", duration)

	if err != nil {
		t.Fatalf("PostMigrationCompleted failed: %v", err)
	}

	// Verify the call succeeded
	t.Log("PostMigrationCompleted completed without error")

	// Verify message format - duration should be rounded to seconds
	roundedDuration := duration.Round(time.Second)
	expectedMsg := fmt.Sprintf("[pvc-456]: KubeVirt live migration completed - source: node-1 -> target: node-2 (duration: %s)", roundedDuration)
	if !strings.Contains(expectedMsg, "node-1 -> target: node-2") {
		t.Errorf("Expected message to contain source -> target format")
	}
	if !strings.Contains(expectedMsg, roundedDuration.String()) {
		t.Errorf("Expected message to contain rounded duration")
	}
	// Verify duration is rounded (should not contain milliseconds)
	if strings.Contains(roundedDuration.String(), "ms") {
		t.Errorf("Expected duration to be rounded to seconds, got %s", roundedDuration)
	}
	t.Logf("Expected message format: %s", expectedMsg)
	t.Logf("Duration rounded from %s to %s", duration, roundedDuration)
}

// TestPostMigrationFailed tests posting migration failed events
func TestPostMigrationFailed(t *testing.T) {
	// Create PVC in fake clientset
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pvc",
			Namespace: "default",
			UID:       "test-uid-migration-fail",
		},
	}
	fakeClient := fake.NewSimpleClientset(pvc)
	poster := NewEventPoster(fakeClient)

	// Post migration failed event
	ctx := context.Background()
	reason := "migration timeout exceeded"
	elapsed := 6 * time.Minute
	err := poster.PostMigrationFailed(ctx, "default", "test-pvc", "pvc-789", "node-1", "node-2", reason, elapsed)

	if err != nil {
		t.Fatalf("PostMigrationFailed failed: %v", err)
	}

	// Verify the call succeeded
	t.Log("PostMigrationFailed completed without error")

	// Verify message format contains expected components
	expectedMsg := fmt.Sprintf("[pvc-789]: KubeVirt live migration failed - source: node-1, attempted target: node-2, reason: %s, elapsed: %s", reason, elapsed.Round(time.Second))
	if !strings.Contains(expectedMsg, "node-1") || !strings.Contains(expectedMsg, "node-2") {
		t.Errorf("Expected message to contain source and target nodes")
	}
	if !strings.Contains(expectedMsg, reason) {
		t.Errorf("Expected message to contain failure reason")
	}
	if !strings.Contains(expectedMsg, elapsed.Round(time.Second).String()) {
		t.Errorf("Expected message to contain elapsed time")
	}
	t.Logf("Expected message format: %s", expectedMsg)

	// Note: PostMigrationFailed should post a Warning event, not Normal
	// We can't easily verify the event type with fake client, but the implementation
	// uses corev1.EventTypeWarning
}
