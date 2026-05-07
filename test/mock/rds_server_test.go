package mock

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/rds"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/utils"
)

// TestLoadConfigFromEnv_Defaults validates default configuration values when no env vars are set
func TestLoadConfigFromEnv_Defaults(t *testing.T) {
	// Clear all mock RDS env vars to ensure clean defaults
	os.Unsetenv("MOCK_RDS_REALISTIC_TIMING")
	os.Unsetenv("MOCK_RDS_SSH_LATENCY_MS")
	os.Unsetenv("MOCK_RDS_SSH_LATENCY_JITTER_MS")
	os.Unsetenv("MOCK_RDS_DISK_ADD_DELAY_MS")
	os.Unsetenv("MOCK_RDS_DISK_REMOVE_DELAY_MS")
	os.Unsetenv("MOCK_RDS_ERROR_MODE")
	os.Unsetenv("MOCK_RDS_ERROR_AFTER_N")
	os.Unsetenv("MOCK_RDS_ENABLE_HISTORY")
	os.Unsetenv("MOCK_RDS_HISTORY_DEPTH")
	os.Unsetenv("MOCK_RDS_ROUTEROS_VERSION")

	config := LoadConfigFromEnv()

	// Validate defaults
	if config.RealisticTiming != false {
		t.Errorf("expected RealisticTiming=false, got %v", config.RealisticTiming)
	}
	if config.SSHLatencyMs != 200 {
		t.Errorf("expected SSHLatencyMs=200, got %d", config.SSHLatencyMs)
	}
	if config.SSHLatencyJitterMs != 50 {
		t.Errorf("expected SSHLatencyJitterMs=50, got %d", config.SSHLatencyJitterMs)
	}
	if config.DiskAddDelayMs != 500 {
		t.Errorf("expected DiskAddDelayMs=500, got %d", config.DiskAddDelayMs)
	}
	if config.DiskRemoveDelayMs != 300 {
		t.Errorf("expected DiskRemoveDelayMs=300, got %d", config.DiskRemoveDelayMs)
	}
	if config.ErrorMode != "none" {
		t.Errorf("expected ErrorMode=none, got %s", config.ErrorMode)
	}
	if config.ErrorAfterN != 0 {
		t.Errorf("expected ErrorAfterN=0, got %d", config.ErrorAfterN)
	}
	if config.EnableHistory != true {
		t.Errorf("expected EnableHistory=true, got %v", config.EnableHistory)
	}
	if config.HistoryDepth != 100 {
		t.Errorf("expected HistoryDepth=100, got %d", config.HistoryDepth)
	}
	if config.RouterOSVersion != "7.16" {
		t.Errorf("expected RouterOSVersion=7.16, got %s", config.RouterOSVersion)
	}
}

// TestLoadConfigFromEnv_RealisticTiming tests realistic timing configuration
func TestLoadConfigFromEnv_RealisticTiming(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected bool
	}{
		{"true string", "true", true},
		{"1 as true", "1", true},
		{"yes as true", "yes", true},
		{"false string", "false", false},
		{"0 as false", "0", false},
		{"empty as false", "", false},
		{"invalid as false", "invalid", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				os.Unsetenv("MOCK_RDS_REALISTIC_TIMING")
			} else {
				t.Setenv("MOCK_RDS_REALISTIC_TIMING", tt.value)
			}

			config := LoadConfigFromEnv()

			if config.RealisticTiming != tt.expected {
				t.Errorf("expected RealisticTiming=%v, got %v", tt.expected, config.RealisticTiming)
			}
		})
	}
}

// TestLoadConfigFromEnv_ErrorMode tests error mode configuration
func TestLoadConfigFromEnv_ErrorMode(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		expected string
	}{
		{"none", "none", "none"},
		{"disk_full", "disk_full", "disk_full"},
		{"ssh_timeout", "ssh_timeout", "ssh_timeout"},
		{"command_fail", "command_fail", "command_fail"},
		{"empty defaults to none", "", "none"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				os.Unsetenv("MOCK_RDS_ERROR_MODE")
			} else {
				t.Setenv("MOCK_RDS_ERROR_MODE", tt.value)
			}

			config := LoadConfigFromEnv()

			if config.ErrorMode != tt.expected {
				t.Errorf("expected ErrorMode=%s, got %s", tt.expected, config.ErrorMode)
			}
		})
	}
}

// TestLoadConfigFromEnv_IntegerParsing tests integer environment variable parsing
func TestLoadConfigFromEnv_IntegerParsing(t *testing.T) {
	tests := []struct {
		name        string
		envVar      string
		value       string
		expectedVal int
		defaultVal  int
	}{
		{"valid SSH latency", "MOCK_RDS_SSH_LATENCY_MS", "300", 300, 200},
		{"invalid SSH latency uses default", "MOCK_RDS_SSH_LATENCY_MS", "invalid", 200, 200},
		{"empty SSH latency uses default", "MOCK_RDS_SSH_LATENCY_MS", "", 200, 200},
		{"valid error after N", "MOCK_RDS_ERROR_AFTER_N", "5", 5, 0},
		{"invalid error after N uses default", "MOCK_RDS_ERROR_AFTER_N", "abc", 0, 0},
		{"valid history depth", "MOCK_RDS_HISTORY_DEPTH", "50", 50, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.value == "" {
				os.Unsetenv(tt.envVar)
			} else {
				t.Setenv(tt.envVar, tt.value)
			}

			config := LoadConfigFromEnv()

			var actualVal int
			switch tt.envVar {
			case "MOCK_RDS_SSH_LATENCY_MS":
				actualVal = config.SSHLatencyMs
			case "MOCK_RDS_ERROR_AFTER_N":
				actualVal = config.ErrorAfterN
			case "MOCK_RDS_HISTORY_DEPTH":
				actualVal = config.HistoryDepth
			}

			if actualVal != tt.expectedVal {
				t.Errorf("expected %s=%d, got %d", tt.envVar, tt.expectedVal, actualVal)
			}
		})
	}
}

// TestTimingSimulator_Disabled validates timing is instant when disabled
func TestTimingSimulator_Disabled(t *testing.T) {
	config := MockRDSConfig{RealisticTiming: false}
	timing := NewTimingSimulator(config)

	// Should return immediately
	start := time.Now()
	timing.SimulateSSHLatency()
	elapsed := time.Since(start)

	// Allow 10ms for overhead (shouldn't sleep at all)
	if elapsed > 10*time.Millisecond {
		t.Errorf("timing should be disabled, but took %v", elapsed)
	}
}

// TestTimingSimulator_Enabled validates realistic timing delays
func TestTimingSimulator_Enabled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing test in short mode")
	}

	config := MockRDSConfig{
		RealisticTiming:    true,
		SSHLatencyMs:       200,
		SSHLatencyJitterMs: 50,
	}
	timing := NewTimingSimulator(config)

	start := time.Now()
	timing.SimulateSSHLatency()
	elapsed := time.Since(start)

	// Should be between 150-250ms (200ms ± 50ms)
	if elapsed < 150*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Errorf("expected latency 150-250ms, got %v", elapsed)
	}
}

// TestTimingSimulator_DiskOperations validates disk operation delays
func TestTimingSimulator_DiskOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing test in short mode")
	}

	config := MockRDSConfig{
		RealisticTiming:   true,
		DiskAddDelayMs:    100,
		DiskRemoveDelayMs: 50,
	}
	timing := NewTimingSimulator(config)

	// Test disk add delay
	start := time.Now()
	timing.SimulateDiskOperation("add")
	elapsed := time.Since(start)

	// Should be approximately 100ms (allow ±10ms for overhead)
	if elapsed < 90*time.Millisecond || elapsed > 110*time.Millisecond {
		t.Errorf("expected disk add delay ~100ms, got %v", elapsed)
	}

	// Test disk remove delay
	start = time.Now()
	timing.SimulateDiskOperation("remove")
	elapsed = time.Since(start)

	// Should be approximately 50ms (allow ±10ms for overhead)
	if elapsed < 40*time.Millisecond || elapsed > 60*time.Millisecond {
		t.Errorf("expected disk remove delay ~50ms, got %v", elapsed)
	}

	// Test unknown operation (should be instant)
	start = time.Now()
	timing.SimulateDiskOperation("unknown")
	elapsed = time.Since(start)

	if elapsed > 10*time.Millisecond {
		t.Errorf("unknown operation should be instant, but took %v", elapsed)
	}
}

// TestErrorInjector_None validates no error injection in none mode
func TestErrorInjector_None(t *testing.T) {
	config := MockRDSConfig{ErrorMode: "none"}
	injector := NewErrorInjector(config)

	shouldFail, _ := injector.ShouldFailDiskAdd()
	if shouldFail {
		t.Error("expected no error injection in none mode")
	}

	shouldFail, _ = injector.ShouldFailDiskRemove()
	if shouldFail {
		t.Error("expected no error injection in none mode")
	}
}

// TestErrorInjector_DiskFull validates disk full error injection
func TestErrorInjector_DiskFull(t *testing.T) {
	config := MockRDSConfig{ErrorMode: "disk_full", ErrorAfterN: 0}
	injector := NewErrorInjector(config)

	shouldFail, errMsg := injector.ShouldFailDiskAdd()
	if !shouldFail {
		t.Error("expected disk full error")
	}
	if errMsg != "failure: not enough space\n" {
		t.Errorf("unexpected error message: %s", errMsg)
	}
}

// TestErrorInjector_CommandFail validates command failure error injection
func TestErrorInjector_CommandFail(t *testing.T) {
	config := MockRDSConfig{ErrorMode: "command_fail", ErrorAfterN: 0}
	injector := NewErrorInjector(config)

	// Test disk add failure
	shouldFail, errMsg := injector.ShouldFailDiskAdd()
	if !shouldFail {
		t.Error("expected command fail error on disk add")
	}
	if errMsg != "failure: execution error\n" {
		t.Errorf("unexpected error message: %s", errMsg)
	}

	// Test disk remove failure
	shouldFail, errMsg = injector.ShouldFailDiskRemove()
	if !shouldFail {
		t.Error("expected command fail error on disk remove")
	}
	if errMsg != "failure: execution error\n" {
		t.Errorf("unexpected error message: %s", errMsg)
	}
}

// TestErrorInjector_AfterN validates trigger after N operations
func TestErrorInjector_AfterN(t *testing.T) {
	config := MockRDSConfig{ErrorMode: "disk_full", ErrorAfterN: 2}
	injector := NewErrorInjector(config)

	// First call - should not fail
	shouldFail, _ := injector.ShouldFailDiskAdd()
	if shouldFail {
		t.Error("first call should not fail (ErrorAfterN=2)")
	}

	// Second call - should not fail
	shouldFail, _ = injector.ShouldFailDiskAdd()
	if shouldFail {
		t.Error("second call should not fail (ErrorAfterN=2)")
	}

	// Third call - should fail
	shouldFail, _ = injector.ShouldFailDiskAdd()
	if !shouldFail {
		t.Error("third call should fail (ErrorAfterN=2)")
	}
}

// TestErrorInjector_Reset validates operation counter reset
func TestErrorInjector_Reset(t *testing.T) {
	config := MockRDSConfig{ErrorMode: "disk_full", ErrorAfterN: 1}
	injector := NewErrorInjector(config)

	// First call - should not fail
	shouldFail, _ := injector.ShouldFailDiskAdd()
	if shouldFail {
		t.Error("first call should not fail")
	}

	// Second call - should fail
	shouldFail, _ = injector.ShouldFailDiskAdd()
	if !shouldFail {
		t.Error("second call should fail")
	}

	// Reset
	injector.Reset()

	// After reset - should not fail again (counter reset)
	shouldFail, _ = injector.ShouldFailDiskAdd()
	if shouldFail {
		t.Error("after reset, first call should not fail")
	}
}

// TestParseErrorMode validates error mode string parsing
func TestParseErrorMode(t *testing.T) {
	tests := []struct {
		input    string
		expected ErrorMode
	}{
		{"none", ErrorModeNone},
		{"", ErrorModeNone},
		{"disk_full", ErrorModeDiskFull},
		{"ssh_timeout", ErrorModeSSHTimeout},
		{"command_fail", ErrorModeCommandFail},
		{"invalid", ErrorModeNone}, // Unknown defaults to none
		{"INVALID", ErrorModeNone}, // Case sensitive
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := ParseErrorMode(tt.input)
			if got != tt.expected {
				t.Errorf("ParseErrorMode(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

// TestErrorInjector_SSHTimeout validates SSH timeout error injection
func TestErrorInjector_SSHTimeout(t *testing.T) {
	config := MockRDSConfig{ErrorMode: "ssh_timeout", ErrorAfterN: 1}
	injector := NewErrorInjector(config)

	// First call - should not fail
	shouldFail := injector.ShouldFailSSHConnect()
	if shouldFail {
		t.Error("first SSH connection should not fail")
	}

	// Second call - should fail
	shouldFail = injector.ShouldFailSSHConnect()
	if !shouldFail {
		t.Error("second SSH connection should fail")
	}
}

// setupSnapshotTestClient creates a mock server and rds.RDSClient for snapshot tests.
// The returned cleanup function should be deferred.
func setupSnapshotTestClient(t *testing.T) (*MockRDSServer, rds.RDSClient, func()) {
	t.Helper()

	// Configure allowed base paths for testing
	utils.ResetAllowedBasePaths()
	if err := utils.SetAllowedBasePath("/storage-pool/metal-csi"); err != nil {
		t.Fatalf("failed to set base path: %v", err)
	}

	server, err := NewMockRDSServer(0)
	if err != nil {
		t.Fatalf("failed to create mock server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start mock server: %v", err)
	}

	client, err := rds.NewClient(rds.ClientConfig{
		Address:            server.Address(),
		Port:               server.Port(),
		User:               "admin",
		InsecureSkipVerify: true,
	})
	if err != nil {
		_ = server.Stop()
		t.Fatalf("failed to create rds client: %v", err)
	}
	if err := client.Connect(); err != nil {
		_ = server.Stop()
		t.Fatalf("failed to connect rds client: %v", err)
	}

	cleanup := func() {
		_ = client.Close()
		_ = server.Stop()
		utils.ResetAllowedBasePaths()
	}
	return server, client, cleanup
}

// TestMockRDS_SnapshotCopyFrom tests the copy-from snapshot semantics in the mock RDS server.
func TestMockRDS_SnapshotCopyFrom(t *testing.T) {
	// Use a fixed source volume UUID so generated snapshot IDs are deterministic.
	// Format: pvc-<uuid>
	const sourceUUID = "a1b2c3d4-e5f6-7890-abcd-ef1234567890"

	t.Run("create snapshot via copy-from", func(t *testing.T) {
		server, client, cleanup := setupSnapshotTestClient(t)
		defer cleanup()

		// Create source volume with known UUID
		sourceSlot := "pvc-" + sourceUUID
		snapName := utils.GenerateSnapshotID("test-snap-copy-1", sourceSlot)

		err := client.CreateVolume(rds.CreateVolumeOptions{
			Slot:          sourceSlot,
			FilePath:      fmt.Sprintf("/storage-pool/metal-csi/%s.img", sourceSlot),
			FileSizeBytes: 10 * 1024 * 1024 * 1024, // 10 GiB
			NVMETCPPort:   4420,
			NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", sourceSlot),
		})
		if err != nil {
			t.Fatalf("CreateVolume failed: %v", err)
		}
		if sourceVol, ok := server.GetVolume(sourceSlot); !ok {
			t.Fatalf("source volume %s not found in server state after creation", sourceSlot)
		} else if sourceVol.Mount {
			t.Error("source volume should be created with mount=no")
		}

		// Create snapshot from source volume via CreateSnapshot
		snapOpts := rds.CreateSnapshotOptions{
			Name:         snapName,
			SourceVolume: sourceSlot,
			BasePath:     "/storage-pool/metal-csi",
		}
		snap, err := client.CreateSnapshot(snapOpts)
		if err != nil {
			t.Fatalf("CreateSnapshot failed: %v", err)
		}

		// Verify snapshot state in server
		mockSnap, ok := server.GetSnapshot(snapName)
		if !ok {
			t.Fatalf("snapshot %s not found in server state after creation", snapOpts.Name)
		}

		// Snapshot should reference source volume
		if mockSnap.SourceVolume != sourceSlot {
			t.Errorf("expected SourceVolume=%s, got %s", sourceSlot, mockSnap.SourceVolume)
		}

		// Snapshot should have file path set
		if mockSnap.FilePath == "" {
			t.Error("snapshot FilePath should not be empty")
		}

		// Snapshot size should equal source volume size
		if mockSnap.FileSizeBytes != 10*1024*1024*1024 {
			t.Errorf("expected snapshot size 10GiB, got %d", mockSnap.FileSizeBytes)
		}
		if mockSnap.Mount {
			t.Error("snapshot disk should be created with mount=no")
		}

		// Snapshot must NOT be in volumes (not an NVMe-exported volume)
		if _, ok := server.GetVolume(snapName); ok {
			t.Error("snapshot should NOT be in volumes map (it is not an NVMe-exported volume)")
		}
		if !commandHistoryContains(server.GetCommandHistory(), "/disk add", "mount=no", sourceSlot) {
			t.Error("expected source volume /disk add command to include mount=no")
		}
		if !commandHistoryContains(server.GetCommandHistory(), "/disk add", "mount=no", snapName) {
			t.Error("expected snapshot /disk add command to include mount=no")
		}

		// Return value from CreateSnapshot should have name set
		if snap.Name == "" {
			t.Error("CreateSnapshot return value should have Name set")
		}
	})

	t.Run("snapshot independent of source", func(t *testing.T) {
		server, client, cleanup := setupSnapshotTestClient(t)
		defer cleanup()

		// Create source volume
		sourceSlot := "pvc-" + sourceUUID
		snapName := utils.GenerateSnapshotID("test-snap-independent-1", sourceSlot)

		err := client.CreateVolume(rds.CreateVolumeOptions{
			Slot:          sourceSlot,
			FilePath:      fmt.Sprintf("/storage-pool/metal-csi/%s.img", sourceSlot),
			FileSizeBytes: 5 * 1024 * 1024 * 1024, // 5 GiB
			NVMETCPPort:   4420,
			NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", sourceSlot),
		})
		if err != nil {
			t.Fatalf("CreateVolume failed: %v", err)
		}

		// Create snapshot
		snapOpts := rds.CreateSnapshotOptions{
			Name:         snapName,
			SourceVolume: sourceSlot,
			BasePath:     "/storage-pool/metal-csi",
		}
		_, err = client.CreateSnapshot(snapOpts)
		if err != nil {
			t.Fatalf("CreateSnapshot failed: %v", err)
		}

		// Delete source volume
		if err := client.DeleteVolume(sourceSlot); err != nil {
			t.Fatalf("DeleteVolume failed: %v", err)
		}

		// Snapshot must still exist (independent copy semantics)
		if _, ok := server.GetSnapshot(snapName); !ok {
			t.Errorf("snapshot %s should still exist after source volume deletion", snapName)
		}

		// Source volume should be gone
		if _, ok := server.GetVolume(sourceSlot); ok {
			t.Error("source volume should have been deleted")
		}
	})

	t.Run("query snapshot via disk print detail", func(t *testing.T) {
		server, client, cleanup := setupSnapshotTestClient(t)
		defer cleanup()

		// Create source volume
		sourceSlot := "pvc-" + sourceUUID
		const volSize = 8 * 1024 * 1024 * 1024 // 8 GiB
		snapName := utils.GenerateSnapshotID("test-snap-query-1", sourceSlot)

		err := client.CreateVolume(rds.CreateVolumeOptions{
			Slot:          sourceSlot,
			FilePath:      fmt.Sprintf("/storage-pool/metal-csi/%s.img", sourceSlot),
			FileSizeBytes: volSize,
			NVMETCPPort:   4420,
			NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", sourceSlot),
		})
		if err != nil {
			t.Fatalf("CreateVolume failed: %v", err)
		}

		// Create snapshot
		snapOpts := rds.CreateSnapshotOptions{
			Name:         snapName,
			SourceVolume: sourceSlot,
			BasePath:     "/storage-pool/metal-csi",
		}
		_, err = client.CreateSnapshot(snapOpts)
		if err != nil {
			t.Fatalf("CreateSnapshot failed: %v", err)
		}

		// Query snapshot via GetSnapshot (uses /disk print detail where slot=...)
		snap, err := client.GetSnapshot(snapName)
		if err != nil {
			t.Fatalf("GetSnapshot failed: %v", err)
		}

		// Verify output fields
		if snap.Name != snapName {
			t.Errorf("expected Name=%s, got %s", snapName, snap.Name)
		}
		if snap.FilePath == "" {
			t.Error("FilePath should not be empty in GetSnapshot result")
		}
		if snap.FileSizeBytes == 0 {
			t.Error("FileSizeBytes should not be 0 in GetSnapshot result")
		}

		// Verify output does NOT include nvme-tcp-export (use server state directly)
		mockSnap, ok := server.GetSnapshot(snapName)
		if !ok {
			t.Fatalf("snapshot not found in server state")
		}
		// Format the output and check it doesn't have NVMe export fields
		output := server.formatSnapshotDetail(mockSnap)
		if strings.Contains(output, "nvme-tcp-export") {
			t.Errorf("snapshot disk print output should NOT contain nvme-tcp-export, got: %s", output)
		}
		if strings.Contains(output, "nvme-tcp-server-port") {
			t.Errorf("snapshot disk print output should NOT contain nvme-tcp-server-port, got: %s", output)
		}
		if strings.Contains(output, "nvme-tcp-server-nqn") {
			t.Errorf("snapshot disk print output should NOT contain nvme-tcp-server-nqn, got: %s", output)
		}
	})

	t.Run("delete snapshot via disk remove", func(t *testing.T) {
		server, client, cleanup := setupSnapshotTestClient(t)
		defer cleanup()

		// Create source volume
		sourceSlot := "pvc-" + sourceUUID
		snapName := utils.GenerateSnapshotID("test-snap-delete-1", sourceSlot)

		err := client.CreateVolume(rds.CreateVolumeOptions{
			Slot:          sourceSlot,
			FilePath:      fmt.Sprintf("/storage-pool/metal-csi/%s.img", sourceSlot),
			FileSizeBytes: 10 * 1024 * 1024 * 1024,
			NVMETCPPort:   4420,
			NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", sourceSlot),
		})
		if err != nil {
			t.Fatalf("CreateVolume failed: %v", err)
		}

		// Create snapshot
		snapOpts := rds.CreateSnapshotOptions{
			Name:         snapName,
			SourceVolume: sourceSlot,
			BasePath:     "/storage-pool/metal-csi",
		}
		snap, err := client.CreateSnapshot(snapOpts)
		if err != nil {
			t.Fatalf("CreateSnapshot failed: %v", err)
		}

		snapFilePath := snap.FilePath
		if snapFilePath == "" {
			t.Fatal("snapshot FilePath should not be empty")
		}

		// Delete snapshot via DeleteSnapshot
		if err := client.DeleteSnapshot(snapName); err != nil {
			t.Fatalf("DeleteSnapshot failed: %v", err)
		}

		// Snapshot should be removed from server state
		if _, ok := server.GetSnapshot(snapName); ok {
			t.Errorf("snapshot %s should have been removed from server state", snapName)
		}

		// Backing file should also be removed
		if _, ok := server.GetFile(snapFilePath); ok {
			t.Errorf("backing file %s should have been removed with snapshot", snapFilePath)
		}
	})

	t.Run("copy-from nonexistent source fails", func(t *testing.T) {
		_, client, cleanup := setupSnapshotTestClient(t)
		defer cleanup()

		// Use a valid snapshot ID (with proper UUID format) but nonexistent source
		nonexistentSource := "pvc-" + sourceUUID
		snapName := utils.GenerateSnapshotID("test-snap-fail-1", nonexistentSource)

		// Attempt to create snapshot from nonexistent source (never created the volume)
		snapOpts := rds.CreateSnapshotOptions{
			Name:         snapName,
			SourceVolume: nonexistentSource,
			BasePath:     "/storage-pool/metal-csi",
		}
		_, err := client.CreateSnapshot(snapOpts)
		if err == nil {
			t.Fatal("expected CreateSnapshot to fail when source volume does not exist")
		}

		// Error should mention not found or failure
		if !strings.Contains(err.Error(), "no such item") &&
			!strings.Contains(err.Error(), "not found") &&
			!strings.Contains(err.Error(), "failed") {
			t.Errorf("expected error mentioning failure, got: %v", err)
		}
	})

	t.Run("restore from snapshot creates NVMe volume", func(t *testing.T) {
		server, client, cleanup := setupSnapshotTestClient(t)
		defer cleanup()

		// Create source volume
		sourceSlot := "pvc-" + sourceUUID
		const volSize = 10 * 1024 * 1024 * 1024 // 10 GiB
		snapName := utils.GenerateSnapshotID("test-snap-restore-1", sourceSlot)

		err := client.CreateVolume(rds.CreateVolumeOptions{
			Slot:          sourceSlot,
			FilePath:      fmt.Sprintf("/storage-pool/metal-csi/%s.img", sourceSlot),
			FileSizeBytes: volSize,
			NVMETCPPort:   4420,
			NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", sourceSlot),
		})
		if err != nil {
			t.Fatalf("CreateVolume failed: %v", err)
		}

		// Create snapshot
		snapOpts := rds.CreateSnapshotOptions{
			Name:         snapName,
			SourceVolume: sourceSlot,
			BasePath:     "/storage-pool/metal-csi",
		}
		_, err = client.CreateSnapshot(snapOpts)
		if err != nil {
			t.Fatalf("CreateSnapshot failed: %v", err)
		}

		// Restore snapshot to a new volume — use a different UUID for the restored volume
		const restoredUUID = "b2c3d4e5-f6a7-8901-bcde-f01234567891"
		restoredSlot := "pvc-" + restoredUUID
		restoreOpts := rds.CreateVolumeOptions{
			Slot:          restoredSlot,
			FilePath:      fmt.Sprintf("/storage-pool/metal-csi/%s.img", restoredSlot),
			FileSizeBytes: volSize,
			NVMETCPPort:   4420,
			NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", restoredSlot),
		}
		if err := client.RestoreSnapshot(snapName, restoreOpts); err != nil {
			t.Fatalf("RestoreSnapshot failed: %v", err)
		}

		// Restored volume must be in volumes map (NVMe-exported)
		restoredVol, ok := server.GetVolume(restoredSlot)
		if !ok {
			t.Fatalf("restored volume %s not found in server volumes", restoredSlot)
		}
		if !restoredVol.Exported {
			t.Error("restored volume should have NVMe export enabled")
		}
		if restoredVol.Mount {
			t.Error("restored volume should be created with mount=no")
		}
		if restoredVol.Slot != restoredSlot {
			t.Errorf("expected Slot=%s, got %s", restoredSlot, restoredVol.Slot)
		}
		if !commandHistoryContains(server.GetCommandHistory(), "/disk add", "mount=no", restoredSlot) {
			t.Error("expected restore /disk add command to include mount=no")
		}

		// Original snapshot should still exist (restore is non-destructive)
		if _, ok := server.GetSnapshot(snapName); !ok {
			t.Errorf("original snapshot %s should still exist after restore", snapName)
		}
	})
}

func commandHistoryContains(history []CommandLog, parts ...string) bool {
	for _, entry := range history {
		matches := true
		for _, part := range parts {
			if !strings.Contains(entry.Command, part) {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}
