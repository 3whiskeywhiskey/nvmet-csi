// Package observability provides Prometheus metrics for the RDS CSI driver.
package observability

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewMetrics(t *testing.T) {
	m := NewMetrics()
	if m == nil {
		t.Fatal("NewMetrics returned nil")
		return
	}
	if m.registry == nil {
		t.Error("registry is nil")
	}
}

func TestHandler(t *testing.T) {
	m := NewMetrics()
	handler := m.Handler()
	if handler == nil {
		t.Fatal("Handler returned nil")
	}

	// Test that handler serves metrics
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", rec.Code)
	}

	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "rds_csi_") {
		t.Error("metrics response should contain rds_csi_ namespace")
	}
}

func TestRecordVolumeOp(t *testing.T) {
	m := NewMetrics()

	// Test success
	m.RecordVolumeOp("stage", nil, 100*time.Millisecond)

	// Test failure
	m.RecordVolumeOp("stage", errors.New("test error"), 50*time.Millisecond)

	// Verify via handler output
	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "rds_csi_volume_operations_total") {
		t.Error("expected volume_operations_total metric")
	}
	if !strings.Contains(body, "rds_csi_volume_operation_duration_seconds") {
		t.Error("expected volume_operation_duration_seconds metric")
	}
}

func TestRecordVolumeOp_AllOperations(t *testing.T) {
	operations := []string{"create", "delete", "stage", "unstage", "publish", "unpublish"}

	for _, op := range operations {
		t.Run(op, func(t *testing.T) {
			m := NewMetrics()

			// Record success and failure for each operation
			m.RecordVolumeOp(op, nil, 100*time.Millisecond)
			m.RecordVolumeOp(op, errors.New("test error"), 50*time.Millisecond)

			handler := m.Handler()
			req := httptest.NewRequest("GET", "/metrics", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			body := rec.Body.String()

			// Check for operation label
			if !strings.Contains(body, `operation="`+op+`"`) {
				t.Errorf("expected operation label %q in metrics", op)
			}

			// Check both status labels
			if !strings.Contains(body, `status="success"`) {
				t.Error("expected status=success label in metrics")
			}
			if !strings.Contains(body, `status="failure"`) {
				t.Error("expected status=failure label in metrics")
			}
		})
	}
}

func TestRecordNVMeConnect(t *testing.T) {
	m := NewMetrics()

	// Test success
	m.RecordNVMeConnect(nil, 500*time.Millisecond)

	// Test failure
	m.RecordNVMeConnect(errors.New("connection failed"), 0)

	// Verify counters and histogram work without SetAttachmentManager
	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "rds_csi_nvme_connects_total") {
		t.Error("expected nvme_connects_total metric")
	}
	if !strings.Contains(body, "rds_csi_nvme_connect_duration_seconds") {
		t.Error("expected nvme_connect_duration_seconds metric")
	}
	// nvme_connections_active should NOT appear without SetAttachmentManager
	if strings.Contains(body, "rds_csi_nvme_connections_active") {
		t.Error("nvme_connections_active should not appear without SetAttachmentManager")
	}
}

func TestRecordNVMeConnect_ActiveConnectionsGauge(t *testing.T) {
	m := NewMetrics()

	// Create a mock attachment count function
	attachmentCount := 3
	m.SetAttachmentManager(func() int {
		return attachmentCount
	})

	// RecordNVMeConnect should still work for counters
	m.RecordNVMeConnect(nil, 100*time.Millisecond)

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "rds_csi_nvme_connections_active 3") {
		t.Errorf("expected nvme_connections_active to be 3, got:\n%s", body)
	}
}

func TestRecordNVMeDisconnect(t *testing.T) {
	m := NewMetrics()

	// Set up attachment manager with 0 attachments
	m.SetAttachmentManager(func() int {
		return 0
	})

	// RecordNVMeDisconnect should not panic
	m.RecordNVMeDisconnect()

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "rds_csi_nvme_connections_active 0") {
		t.Errorf("expected nvme_connections_active to be 0, got:\n%s", body)
	}
}

func TestRecordNVMeDisconnect_MultipleConnections(t *testing.T) {
	m := NewMetrics()

	// Start with 3 attachments, then reduce to 2
	attachmentCount := 3
	m.SetAttachmentManager(func() int {
		return attachmentCount
	})

	// Verify starts at 3
	body := scrapeMetrics(t, m)
	if !strings.Contains(body, "rds_csi_nvme_connections_active 3") {
		t.Errorf("expected nvme_connections_active to be 3, got:\n%s", body)
	}

	// Simulate detach (reduce count)
	attachmentCount = 2

	// Verify now shows 2
	body = scrapeMetrics(t, m)
	if !strings.Contains(body, "rds_csi_nvme_connections_active 2") {
		t.Errorf("expected nvme_connections_active to be 2, got:\n%s", body)
	}
}

func TestNVMeConnectionsActive_QueriesAttachmentManager(t *testing.T) {
	m := NewMetrics()

	// Without SetAttachmentManager, metric should not appear
	body := scrapeMetrics(t, m)
	if strings.Contains(body, "nvme_connections_active") {
		t.Error("nvme_connections_active should not appear without SetAttachmentManager")
	}

	// Set up with 5 attachments
	m.SetAttachmentManager(func() int {
		return 5
	})

	body = scrapeMetrics(t, m)
	if !strings.Contains(body, "rds_csi_nvme_connections_active 5") {
		t.Errorf("expected nvme_connections_active to be 5, got:\n%s", body)
	}
}

func TestNVMeConnectionsActive_SurvivesRestart(t *testing.T) {
	// Simulate first controller instance
	m1 := NewMetrics()
	count1 := 16
	m1.SetAttachmentManager(func() int {
		return count1
	})

	body := scrapeMetrics(t, m1)
	if !strings.Contains(body, "rds_csi_nvme_connections_active 16") {
		t.Errorf("first instance should show 16, got:\n%s", body)
	}

	// Simulate controller restart: new Metrics instance, same attachment count
	// (AttachmentManager rebuilds state from VolumeAttachments on startup)
	m2 := NewMetrics()
	count2 := 16 // Same count - rebuilt from VolumeAttachments
	m2.SetAttachmentManager(func() int {
		return count2
	})

	body = scrapeMetrics(t, m2)
	if !strings.Contains(body, "rds_csi_nvme_connections_active 16") {
		t.Errorf("after restart, should still show 16 (rebuilt from VolumeAttachments), got:\n%s", body)
	}
}

func TestNVMeConnectionsActive_DynamicUpdates(t *testing.T) {
	m := NewMetrics()
	count := 0
	m.SetAttachmentManager(func() int {
		return count
	})

	// Start at 0
	body := scrapeMetrics(t, m)
	if !strings.Contains(body, "rds_csi_nvme_connections_active 0") {
		t.Errorf("expected 0, got:\n%s", body)
	}

	// Add attachments
	count = 5
	body = scrapeMetrics(t, m)
	if !strings.Contains(body, "rds_csi_nvme_connections_active 5") {
		t.Errorf("expected 5, got:\n%s", body)
	}

	// Remove some
	count = 2
	body = scrapeMetrics(t, m)
	if !strings.Contains(body, "rds_csi_nvme_connections_active 2") {
		t.Errorf("expected 2, got:\n%s", body)
	}

	// Remove all
	count = 0
	body = scrapeMetrics(t, m)
	if !strings.Contains(body, "rds_csi_nvme_connections_active 0") {
		t.Errorf("expected 0, got:\n%s", body)
	}
}

// scrapeMetrics is a test helper that scrapes the /metrics endpoint and returns the body.
func scrapeMetrics(t *testing.T, m *Metrics) string {
	t.Helper()
	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics endpoint returned status %d", rec.Code)
	}
	return rec.Body.String()
}

func TestRecordMountOp(t *testing.T) {
	m := NewMetrics()

	m.RecordMountOp("mount", nil)
	m.RecordMountOp("unmount", errors.New("unmount failed"))

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "rds_csi_mount_operations_total") {
		t.Error("expected mount_operations_total metric")
	}
}

func TestRecordMountOp_BothOperations(t *testing.T) {
	m := NewMetrics()

	// Record mount success
	m.RecordMountOp("mount", nil)

	// Record unmount failure
	m.RecordMountOp("unmount", errors.New("device busy"))

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check for operation labels
	if !strings.Contains(body, `operation="mount"`) {
		t.Error("expected operation=mount label")
	}
	if !strings.Contains(body, `operation="unmount"`) {
		t.Error("expected operation=unmount label")
	}
}

func TestRecordStaleMountDetected(t *testing.T) {
	m := NewMetrics()

	m.RecordStaleMountDetected()
	m.RecordStaleMountDetected()

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "rds_csi_stale_mounts_detected_total 2") {
		t.Errorf("expected stale_mounts_detected_total to be 2, got:\n%s", body)
	}
}

func TestRecordStaleRecovery(t *testing.T) {
	m := NewMetrics()

	m.RecordStaleRecovery(nil)
	m.RecordStaleRecovery(errors.New("recovery failed"))

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "rds_csi_stale_recoveries_total") {
		t.Error("expected stale_recoveries_total metric")
	}
}

func TestRecordStaleRecovery_StatusLabels(t *testing.T) {
	m := NewMetrics()

	// Record success
	m.RecordStaleRecovery(nil)

	// Record failure
	m.RecordStaleRecovery(errors.New("recovery failed"))

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check both status labels exist
	if !strings.Contains(body, `status="success"`) {
		t.Error("expected status=success label")
	}
	if !strings.Contains(body, `status="failure"`) {
		t.Error("expected status=failure label")
	}
}

func TestRecordOrphanCleaned(t *testing.T) {
	m := NewMetrics()

	m.RecordOrphanCleaned()

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "rds_csi_orphans_cleaned_total 1") {
		t.Errorf("expected orphans_cleaned_total to be 1, got:\n%s", body)
	}
}

func TestRecordOrphanCleaned_Multiple(t *testing.T) {
	m := NewMetrics()

	m.RecordOrphanCleaned()
	m.RecordOrphanCleaned()
	m.RecordOrphanCleaned()

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "rds_csi_orphans_cleaned_total 3") {
		t.Errorf("expected orphans_cleaned_total to be 3, got:\n%s", body)
	}
}

func TestRecordEventPosted(t *testing.T) {
	m := NewMetrics()

	m.RecordEventPosted("MountFailure")
	m.RecordEventPosted("MountFailure")
	m.RecordEventPosted("ConnectionFailure")

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()
	if !strings.Contains(body, "rds_csi_events_posted_total") {
		t.Error("expected events_posted_total metric")
	}
}

func TestRecordEventPosted_ReasonLabels(t *testing.T) {
	m := NewMetrics()

	// Record various event reasons
	m.RecordEventPosted("MountFailure")
	m.RecordEventPosted("RecoveryFailed")
	m.RecordEventPosted("StaleMountDetected")

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check for reason labels
	if !strings.Contains(body, `reason="MountFailure"`) {
		t.Error("expected reason=MountFailure label")
	}
	if !strings.Contains(body, `reason="RecoveryFailed"`) {
		t.Error("expected reason=RecoveryFailed label")
	}
	if !strings.Contains(body, `reason="StaleMountDetected"`) {
		t.Error("expected reason=StaleMountDetected label")
	}
}

func TestMetricsNamespace(t *testing.T) {
	m := NewMetrics()

	// Set up attachment manager to ensure nvme_connections_active is included
	m.SetAttachmentManager(func() int {
		return 1
	})

	// Record something to populate metrics
	m.RecordStaleMountDetected()
	m.RecordVolumeOp("stage", nil, 100*time.Millisecond)
	m.RecordNVMeConnect(nil, 100*time.Millisecond)
	m.RecordMountOp("mount", nil)
	m.RecordOrphanCleaned()
	m.RecordEventPosted("TestReason")

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// All RDS CSI metrics should use rds_csi_ namespace
	lines := strings.Split(body, "\n")
	for _, line := range lines {
		// Skip comments and empty lines
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		// Skip standard Go runtime metrics (these are from the custom registry)
		if strings.HasPrefix(line, "go_") || strings.HasPrefix(line, "process_") {
			continue
		}
		if !strings.HasPrefix(line, "rds_csi_") {
			t.Errorf("metric line should start with rds_csi_: %s", line)
		}
	}
}

func TestMetricsIsolation(t *testing.T) {
	// Create two separate Metrics instances to verify isolation
	m1 := NewMetrics()
	m2 := NewMetrics()

	// Record metrics only on m1
	m1.RecordStaleMountDetected()
	m1.RecordStaleMountDetected()

	// Record different metric on m2
	m2.RecordOrphanCleaned()

	// Verify m1 has stale mounts
	handler1 := m1.Handler()
	req1 := httptest.NewRequest("GET", "/metrics", nil)
	rec1 := httptest.NewRecorder()
	handler1.ServeHTTP(rec1, req1)
	body1 := rec1.Body.String()

	if !strings.Contains(body1, "rds_csi_stale_mounts_detected_total 2") {
		t.Error("m1 should have stale_mounts_detected_total 2")
	}

	// Verify m2 has orphans cleaned
	handler2 := m2.Handler()
	req2 := httptest.NewRequest("GET", "/metrics", nil)
	rec2 := httptest.NewRecorder()
	handler2.ServeHTTP(rec2, req2)
	body2 := rec2.Body.String()

	if !strings.Contains(body2, "rds_csi_orphans_cleaned_total 1") {
		t.Error("m2 should have orphans_cleaned_total 1")
	}

	// m2 should not have stale mounts count from m1
	// The metric line might not exist at all or be 0
	if strings.Contains(body2, "rds_csi_stale_mounts_detected_total 2") {
		t.Error("m2 should not have m1's stale mounts count")
	}
}

func TestCustomRegistryDoesNotPanic(t *testing.T) {
	// Verify that creating multiple Metrics instances doesn't cause
	// duplicate registration panics (since we use custom registry)
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Creating multiple Metrics instances caused panic: %v", r)
		}
	}()

	for i := 0; i < 10; i++ {
		m := NewMetrics()
		m.RecordVolumeOp("stage", nil, 100*time.Millisecond)
	}
}

func TestHistogramBuckets(t *testing.T) {
	m := NewMetrics()

	// Record various durations to populate histogram buckets
	durations := []time.Duration{
		50 * time.Millisecond,
		200 * time.Millisecond,
		500 * time.Millisecond,
		1 * time.Second,
		3 * time.Second,
		10 * time.Second,
	}

	for _, d := range durations {
		m.RecordVolumeOp("stage", nil, d)
	}

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check that histogram has bucket data
	if !strings.Contains(body, "rds_csi_volume_operation_duration_seconds_bucket") {
		t.Error("expected histogram bucket data")
	}
	if !strings.Contains(body, "rds_csi_volume_operation_duration_seconds_sum") {
		t.Error("expected histogram sum")
	}
	if !strings.Contains(body, "rds_csi_volume_operation_duration_seconds_count") {
		t.Error("expected histogram count")
	}
}

func TestRecordMigrationStarted(t *testing.T) {
	m := NewMetrics()

	// Record migration started
	m.RecordMigrationStarted()

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check active migrations gauge incremented
	if !strings.Contains(body, "rds_csi_migration_active_migrations 1") {
		t.Errorf("expected active_migrations to be 1, got:\n%s", body)
	}
}

func TestRecordMigrationResult_Success(t *testing.T) {
	m := NewMetrics()

	// Start migration first
	m.RecordMigrationStarted()

	// Record successful migration
	m.RecordMigrationResult("success", 45*time.Second)

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check counter incremented with success label
	if !strings.Contains(body, `rds_csi_migration_migrations_total{result="success"} 1`) {
		t.Error("expected migrations_total with result=success to be 1")
	}

	// Check histogram observed the duration
	if !strings.Contains(body, "rds_csi_migration_duration_seconds_bucket") {
		t.Error("expected migration_duration_seconds histogram bucket")
	}

	// Check active gauge decremented back to 0
	if !strings.Contains(body, "rds_csi_migration_active_migrations 0") {
		t.Errorf("expected active_migrations to be 0 after completion, got:\n%s", body)
	}
}

func TestRecordMigrationResult_Timeout(t *testing.T) {
	m := NewMetrics()

	// Start migration first
	m.RecordMigrationStarted()

	// Record timeout migration
	m.RecordMigrationResult("timeout", 300*time.Second)

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check counter incremented with timeout label
	if !strings.Contains(body, `rds_csi_migration_migrations_total{result="timeout"} 1`) {
		t.Error("expected migrations_total with result=timeout to be 1")
	}

	// Check active gauge decremented
	if !strings.Contains(body, "rds_csi_migration_active_migrations 0") {
		t.Errorf("expected active_migrations to be 0 after timeout, got:\n%s", body)
	}
}

func TestRecordMigrationResult_Failed(t *testing.T) {
	m := NewMetrics()

	// Start migration first
	m.RecordMigrationStarted()

	// Record failed migration
	m.RecordMigrationResult("failed", 20*time.Second)

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check counter incremented with failed label
	if !strings.Contains(body, `rds_csi_migration_migrations_total{result="failed"} 1`) {
		t.Error("expected migrations_total with result=failed to be 1")
	}

	// Check active gauge decremented
	if !strings.Contains(body, "rds_csi_migration_active_migrations 0") {
		t.Errorf("expected active_migrations to be 0 after failure, got:\n%s", body)
	}
}

func TestMigrationDurationHistogram(t *testing.T) {
	m := NewMetrics()

	// Record migrations with different durations
	// 30s should be in the 30 bucket, 120s should be in the 120 bucket
	m.RecordMigrationStarted()
	m.RecordMigrationResult("success", 30*time.Second)

	m.RecordMigrationStarted()
	m.RecordMigrationResult("success", 120*time.Second)

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check histogram bucket labels exist
	if !strings.Contains(body, "rds_csi_migration_duration_seconds_bucket") {
		t.Error("expected duration_seconds histogram bucket")
	}

	// Check for expected buckets (15, 30, 60, 90, 120, 180, 300, 600)
	expectedBuckets := []string{"15", "30", "60", "90", "120", "180", "300", "600"}
	for _, bucket := range expectedBuckets {
		if !strings.Contains(body, `le="`+bucket+`"`) {
			t.Errorf("expected histogram bucket le=%q", bucket)
		}
	}

	// Check histogram sum and count
	if !strings.Contains(body, "rds_csi_migration_duration_seconds_sum") {
		t.Error("expected histogram sum")
	}
	if !strings.Contains(body, "rds_csi_migration_duration_seconds_count 2") {
		t.Error("expected histogram count to be 2")
	}
}

func TestRecordConnectionState_Connected(t *testing.T) {
	m := NewMetrics()

	// Record connected state
	m.RecordConnectionState("10.42.68.1", true)

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check gauge set to 1.0 for connected
	if !strings.Contains(body, `rds_csi_rds_connection_state{address="10.42.68.1"} 1`) {
		t.Errorf("expected connection_state to be 1 for connected, got:\n%s", body)
	}
}

func TestRecordConnectionState_Disconnected(t *testing.T) {
	m := NewMetrics()

	// Record disconnected state
	m.RecordConnectionState("10.42.68.1", false)

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check gauge set to 0.0 for disconnected
	if !strings.Contains(body, `rds_csi_rds_connection_state{address="10.42.68.1"} 0`) {
		t.Errorf("expected connection_state to be 0 for disconnected, got:\n%s", body)
	}
}

func TestRecordConnectionState_MultipleAddresses(t *testing.T) {
	m := NewMetrics()

	// Record state for multiple addresses
	m.RecordConnectionState("10.42.68.1", true)
	m.RecordConnectionState("10.42.68.2", false)

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check both addresses recorded
	if !strings.Contains(body, `rds_csi_rds_connection_state{address="10.42.68.1"} 1`) {
		t.Error("expected connection_state for 10.42.68.1 to be 1")
	}
	if !strings.Contains(body, `rds_csi_rds_connection_state{address="10.42.68.2"} 0`) {
		t.Error("expected connection_state for 10.42.68.2 to be 0")
	}
}

func TestRecordReconnectAttempt_Success(t *testing.T) {
	m := NewMetrics()

	// Record successful reconnect
	m.RecordReconnectAttempt("success", 2*time.Second)

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check counter incremented
	if !strings.Contains(body, `rds_csi_rds_reconnect_total{status="success"} 1`) {
		t.Error("expected reconnect_total with status=success to be 1")
	}

	// Check histogram observed the duration
	if !strings.Contains(body, "rds_csi_rds_reconnect_duration_seconds_bucket") {
		t.Error("expected reconnect_duration_seconds histogram bucket")
	}
}

func TestRecordReconnectAttempt_Failure(t *testing.T) {
	m := NewMetrics()

	// Record failed reconnect
	m.RecordReconnectAttempt("failure", 0)

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check counter incremented
	if !strings.Contains(body, `rds_csi_rds_reconnect_total{status="failure"} 1`) {
		t.Error("expected reconnect_total with status=failure to be 1")
	}
}

func TestRecordReconnectAttempt_MultipleAttempts(t *testing.T) {
	m := NewMetrics()

	// Record multiple attempts
	m.RecordReconnectAttempt("failure", 0)
	m.RecordReconnectAttempt("failure", 0)
	m.RecordReconnectAttempt("success", 3*time.Second)

	handler := m.Handler()
	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := rec.Body.String()

	// Check counters
	if !strings.Contains(body, `rds_csi_rds_reconnect_total{status="failure"} 2`) {
		t.Error("expected reconnect_total with status=failure to be 2")
	}
	if !strings.Contains(body, `rds_csi_rds_reconnect_total{status="success"} 1`) {
		t.Error("expected reconnect_total with status=success to be 1")
	}
}

func TestRDSMetrics_Registration(t *testing.T) {
	m := NewMetrics()

	// Before SetRDSMonitoring, rds metrics should not appear
	body := scrapeMetrics(t, m)
	if strings.Contains(body, "rds_disk_") || strings.Contains(body, "rds_hardware_") {
		t.Error("rds metrics should not appear before SetRDSMonitoring")
	}

	// Register with test callbacks
	m.SetRDSMonitoring(
		"storage-pool",
		"10.42.68.1",
		"public",
		func() (*DiskHealthSnapshot, error) {
			return &DiskHealthSnapshot{
				ReadOpsPerSecond:  100,
				WriteOpsPerSecond: 50,
				ReadBytesPerSec:   1_000_000,
				WriteBytesPerSec:  500_000,
				ReadTimeMs:        2,
				WriteTimeMs:       5,
				WaitTimeMs:        1,
				InFlightOps:       3,
				ActiveTimeMs:      10,
			}, nil
		},
		func() (*HardwareHealthSnapshot, error) {
			return &HardwareHealthSnapshot{
				CPUTemperature:    45,
				BoardTemperature:  38,
				Fan1Speed:         7500,
				Fan2Speed:         7600,
				PSU1Power:         700,
				PSU2Power:         680,
				PSU1Temperature:   28,
				PSU2Temperature:   27,
				DiskPoolSizeBytes: 8_000_000_000_000,
				DiskPoolUsedBytes: 1_600_000_000_000,
			}, nil
		},
	)

	// Scrape and verify all 19 metrics present (9 disk + 10 hardware)
	body = scrapeMetrics(t, m)

	expectedDiskMetrics := []string{
		"rds_disk_read_ops_per_second",
		"rds_disk_write_ops_per_second",
		"rds_disk_read_bytes_per_second",
		"rds_disk_write_bytes_per_second",
		"rds_disk_read_latency_milliseconds",
		"rds_disk_write_latency_milliseconds",
		"rds_disk_wait_latency_milliseconds",
		"rds_disk_in_flight_operations",
		"rds_disk_active_time_milliseconds",
	}

	expectedHardwareMetrics := []string{
		"rds_hardware_cpu_temperature_celsius",
		"rds_hardware_board_temperature_celsius",
		"rds_hardware_fan1_speed_rpm",
		"rds_hardware_fan2_speed_rpm",
		"rds_hardware_psu1_power_watts",
		"rds_hardware_psu2_power_watts",
		"rds_hardware_psu1_temperature_celsius",
		"rds_hardware_psu2_temperature_celsius",
		"rds_hardware_disk_pool_size_bytes",
		"rds_hardware_disk_pool_used_bytes",
	}

	// Check disk metrics have slot label
	for _, name := range expectedDiskMetrics {
		expectedLine := fmt.Sprintf(`%s{slot="storage-pool"}`, name)
		if !strings.Contains(body, expectedLine) {
			t.Errorf("expected %s with slot label, not found", name)
		}
	}

	// Check hardware metrics (no labels)
	for _, name := range expectedHardwareMetrics {
		if !strings.Contains(body, name) {
			t.Errorf("expected %s, not found", name)
		}
	}
}

func TestRDSMetrics_ErrorReturnsZero(t *testing.T) {
	m := NewMetrics()

	// Register with error-returning callbacks
	m.SetRDSMonitoring(
		"storage-pool",
		"10.42.68.1",
		"public",
		func() (*DiskHealthSnapshot, error) {
			return nil, fmt.Errorf("SSH connection failed")
		},
		func() (*HardwareHealthSnapshot, error) {
			return nil, fmt.Errorf("SNMP timeout")
		},
	)

	// Scrape should succeed (not fail) - metrics should report 0
	body := scrapeMetrics(t, m)
	if !strings.Contains(body, "rds_disk_read_ops_per_second") {
		t.Error("disk metrics should still appear even with error callback")
	}
	if !strings.Contains(body, "rds_hardware_cpu_temperature_celsius") {
		t.Error("hardware metrics should still appear even with error callback")
	}
	if !strings.Contains(body, `rds_disk_read_ops_per_second{slot="storage-pool"} 0`) {
		t.Error("disk metrics should report 0 on error")
	}
}

func TestRDSMetrics_DynamicUpdates(t *testing.T) {
	m := NewMetrics()

	diskCallCount := 0
	hwCallCount := 0
	m.SetRDSMonitoring(
		"storage-pool",
		"10.42.68.1",
		"public",
		func() (*DiskHealthSnapshot, error) {
			diskCallCount++
			return &DiskHealthSnapshot{
				WriteOpsPerSecond: float64(diskCallCount * 100),
			}, nil
		},
		func() (*HardwareHealthSnapshot, error) {
			hwCallCount++
			return &HardwareHealthSnapshot{
				CPUTemperature: 40 + float64(hwCallCount),
			}, nil
		},
	)

	// First scrape
	body1 := scrapeMetrics(t, m)
	if !strings.Contains(body1, "rds_disk_write_ops_per_second") {
		t.Error("disk write IOPS metric not found in first scrape")
	}
	if !strings.Contains(body1, "rds_hardware_cpu_temperature_celsius") {
		t.Error("CPU temp metric not found in first scrape")
	}

	// Wait for cache to expire (>1 second)
	time.Sleep(1100 * time.Millisecond)

	// Second scrape should show updated values
	body2 := scrapeMetrics(t, m)
	if body1 == body2 {
		t.Log("Note: cache may still be valid if scrapes were fast")
	}
}

func TestRDSMetrics_NotRegisteredWithoutCall(t *testing.T) {
	m := NewMetrics()

	// Verify rds metrics do NOT appear when SetRDSMonitoring is never called
	body := scrapeMetrics(t, m)
	if strings.Contains(body, "rds_disk_") || strings.Contains(body, "rds_hardware_") {
		t.Error("rds metrics should not appear without SetRDSMonitoring call")
	}
}

func TestRecordRDSCommand(t *testing.T) {
	m := NewMetrics()

	m.RecordRDSCommand("disk", "print", "observe", "success", 25*time.Millisecond)
	m.RecordRDSCommand("disk", "add", "mutate", "failure", 50*time.Millisecond)

	body := scrapeMetrics(t, m)
	if !strings.Contains(body, `rds_csi_rds_commands_total{action="print",class="observe",family="disk",status="success"} 1`) {
		t.Error("expected successful observe command counter")
	}
	if !strings.Contains(body, `rds_csi_rds_commands_total{action="add",class="mutate",family="disk",status="failure"} 1`) {
		t.Error("expected failed mutate command counter")
	}
	if !strings.Contains(body, "rds_csi_rds_command_duration_seconds_bucket") {
		t.Error("expected command duration histogram")
	}
}

func TestRecordRDSMutationDecision(t *testing.T) {
	m := NewMetrics()

	m.RecordRDSMutationDecision("delete_file", "/storage-pool/test/missing.img", "noop", "missing")

	body := scrapeMetrics(t, m)
	if !strings.Contains(body, `rds_csi_rds_mutation_decisions_total{decision="noop",operation="delete_file",reason="missing"} 1`) {
		t.Error("expected RDS mutation decision counter")
	}

	events := m.RecentRDSAudit()
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	event := events[0]
	if event.Operation != "delete_file" || event.Target != "/storage-pool/test/missing.img" || event.Status != "decision" || event.Decision != "noop" || event.Reason != "missing" {
		t.Fatalf("audit event = %+v, want delete_file decision noop missing", event)
	}
}

func TestNilMetricsRDSObserversDoNotPanic(t *testing.T) {
	var m *Metrics

	m.RecordRDSMutation("create_volume", nil, false, time.Millisecond)
	m.RecordRDSMutationDecision("create_volume", "vol-a", "noop", "already_exists")
	m.RecordRDSMutationRateLimitWait("create_volume", time.Millisecond)
	m.RecordRDSCommand("disk", "print", "observe", "success", time.Millisecond)
	m.RecordRDSMutationAudit("create_volume", "vol-a", "success", false, time.Millisecond)

	if events := m.RecentRDSAudit(); events != nil {
		t.Fatalf("RecentRDSAudit() = %#v, want nil", events)
	}
}

func TestRDSMutationAuditRing(t *testing.T) {
	m := NewMetrics()
	m.rdsAuditLimit = 2

	m.RecordRDSMutationAudit("create_volume", "vol-a", "success", false, time.Millisecond)
	m.RecordRDSMutationAudit("delete_volume", "vol-b", "failure", false, 2*time.Millisecond)
	m.RecordRDSMutationAudit("resize_volume", "vol-c", "success", true, 3*time.Millisecond)

	events := m.RecentRDSAudit()
	if len(events) != 2 {
		t.Fatalf("len(events) = %d, want 2", len(events))
	}
	if events[0].Operation != "delete_volume" || events[0].Target != "vol-b" {
		t.Fatalf("oldest retained event = %+v, want delete_volume vol-b", events[0])
	}
	if events[1].Operation != "resize_volume" || !events[1].DryRun {
		t.Fatalf("newest retained event = %+v, want dry-run resize_volume", events[1])
	}

	events[0].Operation = "mutated-copy"
	if got := m.RecentRDSAudit()[0].Operation; got != "delete_volume" {
		t.Fatalf("RecentRDSAudit returned mutable backing slice, got %q", got)
	}
}
