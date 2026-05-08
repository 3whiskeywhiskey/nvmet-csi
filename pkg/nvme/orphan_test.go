package nvme

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/observability"
)

// MockConnector implements the Connector interface for testing
type MockConnector struct {
	disconnectCalls   []string        // Track NQNs passed to DisconnectWithContext
	disconnectError   error           // Error to return from DisconnectWithContext
	resolver          *DeviceResolver // The resolver to return
	isConnectedResult map[string]bool // Results for IsConnected calls
	isConnectedError  error           // Error for IsConnected
}

func NewMockConnector() *MockConnector {
	return &MockConnector{
		disconnectCalls:   make([]string, 0),
		isConnectedResult: make(map[string]bool),
	}
}

func (m *MockConnector) Connect(target Target) (string, error) {
	return "", nil
}

func (m *MockConnector) ConnectWithContext(ctx context.Context, target Target) (string, error) {
	return "", nil
}

func (m *MockConnector) ConnectWithConfig(ctx context.Context, target Target, config ConnectionConfig) (string, error) {
	return "", nil
}

func (m *MockConnector) ConnectWithRetry(ctx context.Context, target Target, config ConnectionConfig) (string, error) {
	return "", nil
}

func (m *MockConnector) Disconnect(nqn string) error {
	return nil
}

func (m *MockConnector) DisconnectWithContext(ctx context.Context, nqn string) error {
	m.disconnectCalls = append(m.disconnectCalls, nqn)
	return m.disconnectError
}

func (m *MockConnector) IsConnected(nqn string) (bool, error) {
	if m.isConnectedError != nil {
		return false, m.isConnectedError
	}
	return m.isConnectedResult[nqn], nil
}

func requireMetricLine(t *testing.T, body string, labels ...string) {
	t.Helper()

	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, "nvmet_csi_orphan_cleanup_total{") || !strings.HasSuffix(line, " 1") {
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

	t.Fatalf("expected orphan cleanup metric with labels %v, got:\n%s", labels, body)
}

func (m *MockConnector) IsConnectedWithContext(ctx context.Context, nqn string) (bool, error) {
	return m.IsConnected(nqn)
}

func (m *MockConnector) GetDevicePath(nqn string) (string, error) {
	return "", errors.New("not implemented")
}

func (m *MockConnector) WaitForDevice(nqn string, timeout time.Duration) (string, error) {
	return "", errors.New("not implemented")
}

func (m *MockConnector) GetMetrics() *Metrics {
	return nil
}

func (m *MockConnector) GetConfig() Config {
	return DefaultConfig()
}

func (m *MockConnector) GetResolver() *DeviceResolver {
	return m.resolver
}

func (m *MockConnector) SetPromMetrics(metrics *observability.Metrics) {
	// No-op for mock
}

func (m *MockConnector) Close() error {
	// No-op for mock - no background goroutines to clean up
	return nil
}

// testableOrphanCleaner wraps OrphanCleaner with controlled behavior
type testableOrphanCleaner struct {
	connector      *MockConnector
	connectedNQNs  []string
	orphanedNQNs   map[string]bool
	listError      error
	orphanCheckErr map[string]error
}

func (oc *testableOrphanCleaner) CleanupOrphanedConnections(ctx context.Context) error {
	// Get connected subsystems
	if oc.listError != nil {
		return oc.listError
	}

	for _, nqn := range oc.connectedNQNs {
		// Check context
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Check if orphaned
		if err, ok := oc.orphanCheckErr[nqn]; ok && err != nil {
			continue // Skip on error checking orphan status
		}

		if orphaned, ok := oc.orphanedNQNs[nqn]; ok && orphaned {
			// Try to disconnect
			_ = oc.connector.DisconnectWithContext(ctx, nqn)
		}
	}

	return nil
}

func TestCleanupOrphanedConnections_NoOrphans(t *testing.T) {
	mockConnector := NewMockConnector()

	cleaner := &testableOrphanCleaner{
		connector: mockConnector,
		connectedNQNs: []string{
			"nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-1",
			"nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-2",
		},
		orphanedNQNs: map[string]bool{
			"nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-1": false,
			"nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-2": false,
		},
	}

	ctx := context.Background()
	err := cleaner.CleanupOrphanedConnections(ctx)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify no disconnect calls were made
	if len(mockConnector.disconnectCalls) != 0 {
		t.Errorf("Expected no disconnect calls, got %d: %v",
			len(mockConnector.disconnectCalls), mockConnector.disconnectCalls)
	}
}

func TestCleanupOrphanedConnections_WithOrphans(t *testing.T) {
	mockConnector := NewMockConnector()

	orphan1 := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-1"
	orphan2 := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-2"
	healthy := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-healthy"

	cleaner := &testableOrphanCleaner{
		connector: mockConnector,
		connectedNQNs: []string{
			orphan1,
			healthy,
			orphan2,
		},
		orphanedNQNs: map[string]bool{
			orphan1: true,
			healthy: false,
			orphan2: true,
		},
	}

	ctx := context.Background()
	err := cleaner.CleanupOrphanedConnections(ctx)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify exactly 2 disconnect calls were made (for the orphans)
	if len(mockConnector.disconnectCalls) != 2 {
		t.Errorf("Expected 2 disconnect calls, got %d: %v",
			len(mockConnector.disconnectCalls), mockConnector.disconnectCalls)
	}

	// Verify correct NQNs were disconnected
	disconnected := make(map[string]bool)
	for _, nqn := range mockConnector.disconnectCalls {
		disconnected[nqn] = true
	}

	if !disconnected[orphan1] {
		t.Errorf("Expected orphan1 to be disconnected")
	}
	if !disconnected[orphan2] {
		t.Errorf("Expected orphan2 to be disconnected")
	}
	if disconnected[healthy] {
		t.Errorf("Healthy subsystem should not be disconnected")
	}
}

func TestCleanupOrphanedConnections_DisconnectError(t *testing.T) {
	mockConnector := NewMockConnector()
	mockConnector.disconnectError = errors.New("disconnect failed")

	orphan1 := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-1"
	orphan2 := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-2"

	cleaner := &testableOrphanCleaner{
		connector: mockConnector,
		connectedNQNs: []string{
			orphan1,
			orphan2,
		},
		orphanedNQNs: map[string]bool{
			orphan1: true,
			orphan2: true,
		},
	}

	ctx := context.Background()
	err := cleaner.CleanupOrphanedConnections(ctx)

	// Cleanup is best-effort - should return nil even if disconnects fail
	if err != nil {
		t.Fatalf("Expected nil (best-effort), got error: %v", err)
	}

	// Verify both disconnect calls were made (continues after error)
	if len(mockConnector.disconnectCalls) != 2 {
		t.Errorf("Expected 2 disconnect attempts (continue after error), got %d: %v",
			len(mockConnector.disconnectCalls), mockConnector.disconnectCalls)
	}
}

func TestCleanupOrphanedConnections_ContextCanceled(t *testing.T) {
	mockConnector := NewMockConnector()

	cleaner := &testableOrphanCleaner{
		connector: mockConnector,
		connectedNQNs: []string{
			"nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-1",
			"nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-2",
			"nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-3",
		},
		orphanedNQNs: map[string]bool{
			"nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-1": true,
			"nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-2": true,
			"nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-3": true,
		},
	}

	// Cancel context immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := cleaner.CleanupOrphanedConnections(ctx)

	// Should return context error
	if err == nil {
		t.Fatal("Expected context error but got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Expected context.Canceled, got: %v", err)
	}

	// No disconnects should happen since context was pre-canceled
	if len(mockConnector.disconnectCalls) != 0 {
		t.Errorf("Expected no disconnect calls with canceled context, got %d",
			len(mockConnector.disconnectCalls))
	}
}

func TestCleanupOrphanedConnections_EmptyList(t *testing.T) {
	mockConnector := NewMockConnector()

	cleaner := &testableOrphanCleaner{
		connector:     mockConnector,
		connectedNQNs: []string{}, // No connected subsystems
		orphanedNQNs:  map[string]bool{},
	}

	ctx := context.Background()
	err := cleaner.CleanupOrphanedConnections(ctx)

	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(mockConnector.disconnectCalls) != 0 {
		t.Errorf("Expected no disconnect calls for empty list, got %d",
			len(mockConnector.disconnectCalls))
	}
}

func TestCleanupOrphanedConnections_ListError(t *testing.T) {
	mockConnector := NewMockConnector()
	listErr := errors.New("failed to list subsystems")

	cleaner := &testableOrphanCleaner{
		connector: mockConnector,
		listError: listErr,
	}

	ctx := context.Background()
	err := cleaner.CleanupOrphanedConnections(ctx)

	if err == nil {
		t.Fatal("Expected error but got nil")
	}
	if !errors.Is(err, listErr) {
		t.Errorf("Expected list error, got: %v", err)
	}

	if len(mockConnector.disconnectCalls) != 0 {
		t.Errorf("Expected no disconnect calls after list error, got %d",
			len(mockConnector.disconnectCalls))
	}
}

func TestCleanupOrphanedConnections_RejectsEmptyManagedPrefix(t *testing.T) {
	mockConnector := NewMockConnector()
	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: t.TempDir(),
		TTL:       10 * time.Second,
	})
	mockConnector.resolver = resolver

	cleaner := NewOrphanCleaner(mockConnector, "")

	err := cleaner.CleanupOrphanedConnections(context.Background())
	if err == nil {
		t.Fatal("Expected invalid prefix error")
	}
	if len(mockConnector.disconnectCalls) != 0 {
		t.Fatalf("disconnectCalls = %v, want none", mockConnector.disconnectCalls)
	}
}

func TestOrphanCleanerActualImplementationFiltersAndDisconnectsOnlyManagedOrphans(t *testing.T) {
	managedOrphan := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan"
	managedHealthy := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-healthy"
	systemOrphan := "nqn.2014-08.org.nixos:root-disk"

	sysfsRoot := createMockSysfs(t, []mockController{
		{
			name:         "nvme0",
			nqn:          managedHealthy,
			blockDevices: []string{"nvme0n1"},
		},
	})
	createMockSubsystemEntries(t, sysfsRoot, managedOrphan, managedHealthy, systemOrphan)

	mockConnector := NewMockConnector()
	mockConnector.isConnectedResult[managedOrphan] = true
	mockConnector.isConnectedResult[managedHealthy] = true
	mockConnector.isConnectedResult[systemOrphan] = true
	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: sysfsRoot,
		TTL:       10 * time.Second,
	})
	resolver.SetIsConnectedFn(mockConnector.IsConnected)
	mockConnector.resolver = resolver

	cleaner := NewOrphanCleaner(mockConnector, "nqn.2026-05.works.whiskey.nvmet-csi:pvc-")
	if err := cleaner.CleanupOrphanedConnections(context.Background()); err != nil {
		t.Fatalf("CleanupOrphanedConnections() failed: %v", err)
	}

	if len(mockConnector.disconnectCalls) != 1 || mockConnector.disconnectCalls[0] != managedOrphan {
		t.Fatalf("disconnectCalls = %v, want [%s]", mockConnector.disconnectCalls, managedOrphan)
	}
}

func TestOrphanCleanerActualImplementationRespectsCanceledContextBeforeDisconnect(t *testing.T) {
	managedOrphan := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan"
	sysfsRoot := createMockSysfs(t, nil)
	createMockSubsystemEntries(t, sysfsRoot, managedOrphan)

	mockConnector := NewMockConnector()
	mockConnector.isConnectedResult[managedOrphan] = true
	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: sysfsRoot,
		TTL:       10 * time.Second,
	})
	resolver.SetIsConnectedFn(mockConnector.IsConnected)
	mockConnector.resolver = resolver

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cleaner := NewOrphanCleaner(mockConnector, "nqn.2026-05.works.whiskey.nvmet-csi:pvc-")
	err := cleaner.CleanupOrphanedConnections(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CleanupOrphanedConnections() error = %v, want context.Canceled", err)
	}
	if len(mockConnector.disconnectCalls) != 0 {
		t.Fatalf("disconnectCalls = %v, want none", mockConnector.disconnectCalls)
	}
}

func TestOrphanCleanerActualImplementationLimitsDisconnectAttempts(t *testing.T) {
	orphans := []string{
		"nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-1",
		"nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-2",
		"nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan-3",
	}
	sysfsRoot := createMockSysfs(t, nil)
	createMockSubsystemEntries(t, sysfsRoot, orphans...)

	mockConnector := NewMockConnector()
	for _, nqn := range orphans {
		mockConnector.isConnectedResult[nqn] = true
	}
	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: sysfsRoot,
		TTL:       10 * time.Second,
	})
	resolver.SetIsConnectedFn(mockConnector.IsConnected)
	mockConnector.resolver = resolver

	cleaner := NewOrphanCleaner(mockConnector, "nqn.2026-05.works.whiskey.nvmet-csi:pvc-")
	cleaner.SetMaxDisconnects(2)
	if err := cleaner.CleanupOrphanedConnections(context.Background()); err != nil {
		t.Fatalf("CleanupOrphanedConnections() failed: %v", err)
	}

	if len(mockConnector.disconnectCalls) != 2 {
		t.Fatalf("disconnectCalls = %v, want exactly 2 attempts", mockConnector.disconnectCalls)
	}
}

func TestOrphanCleanerActualImplementationDryRunDoesNotDisconnect(t *testing.T) {
	managedOrphan := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-orphan"
	sysfsRoot := createMockSysfs(t, nil)
	createMockSubsystemEntries(t, sysfsRoot, managedOrphan)

	mockConnector := NewMockConnector()
	mockConnector.isConnectedResult[managedOrphan] = true
	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: sysfsRoot,
		TTL:       10 * time.Second,
	})
	resolver.SetIsConnectedFn(mockConnector.IsConnected)
	mockConnector.resolver = resolver

	metrics := observability.NewMetrics()
	cleaner := NewOrphanCleaner(mockConnector, "nqn.2026-05.works.whiskey.nvmet-csi:pvc-")
	cleaner.SetMetrics(metrics)
	cleaner.SetDryRun(true)
	if err := cleaner.CleanupOrphanedConnections(context.Background()); err != nil {
		t.Fatalf("CleanupOrphanedConnections() failed: %v", err)
	}
	if len(mockConnector.disconnectCalls) != 0 {
		t.Fatalf("disconnectCalls = %v, want none in dry-run", mockConnector.disconnectCalls)
	}

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()
	requireMetricLine(t, body, `action="detect"`, `result="orphan"`, `dry_run="true"`)
	requireMetricLine(t, body, `action="disconnect"`, `result="dry_run"`, `dry_run="true"`)
	if strings.Contains(body, "nvmet_csi_orphans_cleaned_total 1") {
		t.Fatalf("dry-run must not record successful cleanup, got:\n%s", body)
	}
}

func createMockSubsystemEntries(t *testing.T, sysfsRoot string, nqns ...string) {
	t.Helper()
	subsystemsDir := filepath.Join(sysfsRoot, "class", "nvme-subsystem")
	for i, nqn := range nqns {
		subsystemDir := filepath.Join(subsystemsDir, fmt.Sprintf("nvme-subsys%d", i))
		if err := os.MkdirAll(subsystemDir, 0755); err != nil {
			t.Fatalf("failed to create subsystem dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(subsystemDir, "subsysnqn"), []byte(nqn+"\n"), 0644); err != nil {
			t.Fatalf("failed to write subsystem NQN: %v", err)
		}
	}
}

// TestNewOrphanCleaner verifies the constructor works correctly
func TestNewOrphanCleaner(t *testing.T) {
	mockConnector := NewMockConnector()

	// Create a real resolver to attach to the mock
	tmpDir := t.TempDir()
	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: tmpDir,
		TTL:       10 * time.Second,
	})
	mockConnector.resolver = resolver

	prefix := "nqn.2026-05.works.whiskey.nvmet-csi:pvc-"
	cleaner := NewOrphanCleaner(mockConnector, prefix)

	if cleaner == nil {
		t.Fatal("NewOrphanCleaner returned nil")
		return
	}
	if cleaner.connector != mockConnector {
		t.Error("OrphanCleaner has wrong connector")
	}
	if cleaner.resolver != resolver {
		t.Error("OrphanCleaner has wrong resolver")
	}
	if cleaner.managedNQNPrefix != prefix {
		t.Errorf("OrphanCleaner has wrong managedNQNPrefix: got %q, want %q", cleaner.managedNQNPrefix, prefix)
	}
	if cleaner.maxDisconnects != DefaultOrphanCleanupMaxDisconnects {
		t.Errorf("OrphanCleaner has wrong maxDisconnects: got %d, want %d", cleaner.maxDisconnects, DefaultOrphanCleanupMaxDisconnects)
	}
}
