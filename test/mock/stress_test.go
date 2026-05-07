package mock

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/rds"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/utils"
)

// setupStressTestBasePaths configures allowed base paths for stress tests
func setupStressTestBasePaths(t *testing.T) {
	t.Helper()
	utils.ResetAllowedBasePaths()
	if err := utils.SetAllowedBasePath("/storage-pool/test"); err != nil {
		t.Fatalf("failed to set test base path: %v", err)
	}
	t.Cleanup(utils.ResetAllowedBasePaths)
}

// TestConcurrentConnections validates MOCK-07: concurrent SSH connections without state corruption
func TestConcurrentConnections(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	// Set up allowed base paths for testing
	setupStressTestBasePaths(t)

	// Start mock server on random available port
	server, err := NewMockRDSServer(0)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer server.Stop()

	// Create SSH client using production rds.Client
	client, err := rds.NewClient(rds.ClientConfig{
		Address:            server.Address(),
		Port:               server.Port(),
		User:               "admin",
		InsecureSkipVerify: true, // Mock server doesn't need host key verification
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("failed to connect client: %v", err)
	}
	defer client.Close()

	// Run concurrent CreateVolume operations
	const numGoroutines = 10
	const opsPerGoroutine = 5

	var wg sync.WaitGroup
	var successCount, failCount atomic.Int32
	var mu sync.Mutex
	createdVolumes := make([]string, 0)

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for op := 0; op < opsPerGoroutine; op++ {
				volumeID := fmt.Sprintf("test-vol-%d-%d", goroutineID, op)

				err := client.CreateVolume(rds.CreateVolumeOptions{
					Slot:          volumeID,
					FilePath:      fmt.Sprintf("/storage-pool/test/%s.img", volumeID),
					FileSizeBytes: 1 * 1024 * 1024 * 1024, // 1 GiB
					NVMETCPPort:   4420,
					NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", volumeID),
				})
				if err != nil {
					failCount.Add(1)
					t.Logf("CreateVolume %s failed: %v", volumeID, err)
				} else {
					successCount.Add(1)
					mu.Lock()
					createdVolumes = append(createdVolumes, volumeID)
					mu.Unlock()
				}
			}
		}(g)
	}

	wg.Wait()

	// Verify results
	t.Logf("Success: %d, Failed: %d", successCount.Load(), failCount.Load())

	// All operations should succeed (no state corruption)
	expectedTotal := numGoroutines * opsPerGoroutine
	if int(successCount.Load()) != expectedTotal {
		t.Errorf("expected %d successes, got %d", expectedTotal, successCount.Load())
	}

	// Verify state consistency
	volumes := server.ListVolumes()
	if len(volumes) != expectedTotal {
		t.Errorf("expected %d volumes in server state, got %d", expectedTotal, len(volumes))
	}

	// Cleanup - delete all created volumes
	for _, volID := range createdVolumes {
		if err := client.DeleteVolume(volID); err != nil {
			t.Logf("DeleteVolume %s failed: %v", volID, err)
		}
	}

	// Verify cleanup
	volumes = server.ListVolumes()
	if len(volumes) != 0 {
		t.Errorf("expected 0 volumes after cleanup, got %d", len(volumes))
	}
}

// TestConcurrentSameVolume validates idempotency: concurrent CreateVolume with same ID
func TestConcurrentSameVolume(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	setupStressTestBasePaths(t)

	server, err := NewMockRDSServer(0)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer server.Stop()

	client, err := rds.NewClient(rds.ClientConfig{
		Address:            server.Address(),
		Port:               server.Port(),
		User:               "admin",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("failed to connect client: %v", err)
	}
	defer client.Close()

	const numGoroutines = 10
	volumeID := "concurrent-same-vol"

	var wg sync.WaitGroup
	var successCount, failCount atomic.Int32

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := client.CreateVolume(rds.CreateVolumeOptions{
				Slot:          volumeID,
				FilePath:      fmt.Sprintf("/storage-pool/test/%s.img", volumeID),
				FileSizeBytes: 1 * 1024 * 1024 * 1024, // 1 GiB
				NVMETCPPort:   4420,
				NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", volumeID),
			})
			if err != nil {
				failCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}()
	}

	wg.Wait()

	if successCount.Load() != int32(numGoroutines) {
		t.Errorf("expected all concurrent idempotent creates to succeed, got %d successes", successCount.Load())
	}
	if failCount.Load() != 0 {
		t.Errorf("expected 0 failures, got %d", failCount.Load())
	}

	adds := 0
	for _, entry := range server.GetCommandHistory() {
		if strings.HasPrefix(entry.Command, "/disk add ") && strings.Contains(entry.Command, "slot="+volumeID) {
			adds++
		}
	}
	if adds != 1 {
		t.Errorf("expected exactly 1 /disk add for idempotent concurrent create, got %d", adds)
	}

	// Verify state: exactly one volume
	volumes := server.ListVolumes()
	if len(volumes) != 1 {
		t.Errorf("expected exactly 1 volume in state, got %d", len(volumes))
	}

	// Cleanup
	_ = client.DeleteVolume(volumeID)
}

// TestConcurrentSameResize validates idempotency: concurrent ResizeVolume with same target size
func TestConcurrentSameResize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	setupStressTestBasePaths(t)

	server, err := NewMockRDSServer(0)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer server.Stop()

	client, err := rds.NewClient(rds.ClientConfig{
		Address:            server.Address(),
		Port:               server.Port(),
		User:               "admin",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("failed to connect client: %v", err)
	}
	defer client.Close()

	volumeID := "concurrent-same-resize"
	if err := client.CreateVolume(rds.CreateVolumeOptions{
		Slot:          volumeID,
		FilePath:      fmt.Sprintf("/storage-pool/test/%s.img", volumeID),
		FileSizeBytes: 1 * 1024 * 1024 * 1024,
		NVMETCPPort:   4420,
		NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", volumeID),
	}); err != nil {
		t.Fatalf("failed to create volume: %v", err)
	}
	server.ClearCommandHistory()

	const numGoroutines = 10
	const targetSize = 2 * 1024 * 1024 * 1024

	var wg sync.WaitGroup
	var successCount, failCount atomic.Int32

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.ResizeVolume(volumeID, targetSize); err != nil {
				failCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}()
	}

	wg.Wait()

	if successCount.Load() != int32(numGoroutines) {
		t.Errorf("expected all concurrent idempotent resizes to succeed, got %d successes", successCount.Load())
	}
	if failCount.Load() != 0 {
		t.Errorf("expected 0 failures, got %d", failCount.Load())
	}

	sets := 0
	for _, entry := range server.GetCommandHistory() {
		if strings.HasPrefix(entry.Command, "/disk set ") && strings.Contains(entry.Command, "slot="+volumeID) {
			sets++
		}
	}
	if sets != 1 {
		t.Errorf("expected exactly 1 /disk set for idempotent concurrent resize, got %d", sets)
	}

	volume, err := client.GetVolume(volumeID)
	if err != nil {
		t.Fatalf("failed to get resized volume: %v", err)
	}
	if volume.FileSizeBytes != targetSize {
		t.Errorf("expected volume size %d, got %d", targetSize, volume.FileSizeBytes)
	}

	_ = client.DeleteVolume(volumeID)
}

// TestConcurrentSameDelete validates idempotency: concurrent DeleteVolume with same target slot
func TestConcurrentSameDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	setupStressTestBasePaths(t)

	server, err := NewMockRDSServer(0)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer server.Stop()

	client, err := rds.NewClient(rds.ClientConfig{
		Address:            server.Address(),
		Port:               server.Port(),
		User:               "admin",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("failed to connect client: %v", err)
	}
	defer client.Close()

	volumeID := "concurrent-same-delete"
	if err := client.CreateVolume(rds.CreateVolumeOptions{
		Slot:          volumeID,
		FilePath:      fmt.Sprintf("/storage-pool/test/%s.img", volumeID),
		FileSizeBytes: 1 * 1024 * 1024 * 1024,
		NVMETCPPort:   4420,
		NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", volumeID),
	}); err != nil {
		t.Fatalf("failed to create volume: %v", err)
	}
	server.ClearCommandHistory()

	const numGoroutines = 10

	var wg sync.WaitGroup
	var successCount, failCount atomic.Int32

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.DeleteVolume(volumeID); err != nil {
				failCount.Add(1)
			} else {
				successCount.Add(1)
			}
		}()
	}

	wg.Wait()

	if successCount.Load() != int32(numGoroutines) {
		t.Errorf("expected all concurrent idempotent deletes to succeed, got %d successes", successCount.Load())
	}
	if failCount.Load() != 0 {
		t.Errorf("expected 0 failures, got %d", failCount.Load())
	}

	removes := 0
	for _, entry := range server.GetCommandHistory() {
		if strings.HasPrefix(entry.Command, "/disk remove ") && strings.Contains(entry.Command, "slot="+volumeID) {
			removes++
		}
	}
	if removes != 1 {
		t.Errorf("expected exactly 1 /disk remove for idempotent concurrent delete, got %d", removes)
	}

	volumes := server.ListVolumes()
	if len(volumes) != 0 {
		t.Errorf("expected 0 volumes after concurrent delete, got %d", len(volumes))
	}
}

// TestConcurrentCreateDelete validates concurrent create and delete operations
func TestConcurrentCreateDelete(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	setupStressTestBasePaths(t)

	server, err := NewMockRDSServer(0)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer server.Stop()

	client, err := rds.NewClient(rds.ClientConfig{
		Address:            server.Address(),
		Port:               server.Port(),
		User:               "admin",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("failed to connect client: %v", err)
	}
	defer client.Close()

	const numPairs = 5
	var wg sync.WaitGroup

	for i := 0; i < numPairs; i++ {
		volumeID := fmt.Sprintf("pair-vol-%d", i)

		// Create goroutine
		wg.Add(1)
		go func(volID string) {
			defer wg.Done()
			_ = client.CreateVolume(rds.CreateVolumeOptions{
				Slot:          volID,
				FilePath:      fmt.Sprintf("/storage-pool/test/%s.img", volID),
				FileSizeBytes: 1 * 1024 * 1024 * 1024,
				NVMETCPPort:   4420,
				NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", volID),
			})
		}(volumeID)

		// Delete goroutine (may run before create completes)
		wg.Add(1)
		go func(volID string) {
			defer wg.Done()
			_ = client.DeleteVolume(volID)
		}(volumeID)
	}

	wg.Wait()

	// State should be consistent (no panics, no data corruption)
	// Final state depends on race but should be valid
	volumes := server.ListVolumes()
	t.Logf("Final volume count: %d (depends on timing)", len(volumes))

	// Verify no state corruption: volume count should be between 0 and numPairs
	if len(volumes) > numPairs {
		t.Errorf("unexpected volume count: %d (expected 0-%d)", len(volumes), numPairs)
	}

	// Cleanup remaining volumes
	for _, vol := range volumes {
		_ = client.DeleteVolume(vol.Slot)
	}
}

// TestConcurrentMixedOperations validates concurrent create, delete, and query operations
func TestConcurrentMixedOperations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	setupStressTestBasePaths(t)

	server, err := NewMockRDSServer(0)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer server.Stop()

	client, err := rds.NewClient(rds.ClientConfig{
		Address:            server.Address(),
		Port:               server.Port(),
		User:               "admin",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("failed to connect client: %v", err)
	}
	defer client.Close()

	const numOperations = 20
	var wg sync.WaitGroup
	var opCount atomic.Int32

	for i := 0; i < numOperations; i++ {
		volumeID := fmt.Sprintf("mixed-vol-%d", i)

		// Create operation
		wg.Add(1)
		go func(volID string) {
			defer wg.Done()
			_ = client.CreateVolume(rds.CreateVolumeOptions{
				Slot:          volID,
				FilePath:      fmt.Sprintf("/storage-pool/test/%s.img", volID),
				FileSizeBytes: 1 * 1024 * 1024 * 1024,
				NVMETCPPort:   4420,
				NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", volID),
			})
			opCount.Add(1)
		}(volumeID)

		// Query operation (may run before create)
		wg.Add(1)
		go func(volID string) {
			defer wg.Done()
			_, _ = client.GetVolume(volID)
			opCount.Add(1)
		}(volumeID)

		// Delete operation (may run before create)
		wg.Add(1)
		go func(volID string) {
			defer wg.Done()
			_ = client.DeleteVolume(volID)
			opCount.Add(1)
		}(volumeID)
	}

	wg.Wait()

	// Verify all operations completed
	expectedOps := numOperations * 3 // create + query + delete
	if int(opCount.Load()) != expectedOps {
		t.Errorf("expected %d operations, got %d", expectedOps, opCount.Load())
	}

	// Verify state consistency (no panics or corruption)
	volumes := server.ListVolumes()
	t.Logf("Final volume count: %d (depends on timing)", len(volumes))

	// Cleanup remaining volumes
	for _, vol := range volumes {
		_ = client.DeleteVolume(vol.Slot)
	}
}

// TestConcurrentCommandHistory validates command history tracking with concurrent operations
func TestConcurrentCommandHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	setupStressTestBasePaths(t)

	server, err := NewMockRDSServer(0)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer server.Stop()

	client, err := rds.NewClient(rds.ClientConfig{
		Address:            server.Address(),
		Port:               server.Port(),
		User:               "admin",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	if err := client.Connect(); err != nil {
		t.Fatalf("failed to connect client: %v", err)
	}
	defer client.Close()

	const numGoroutines = 10
	const opsPerGoroutine = 3

	var wg sync.WaitGroup

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for op := 0; op < opsPerGoroutine; op++ {
				volumeID := fmt.Sprintf("history-vol-%d-%d", goroutineID, op)
				_ = client.CreateVolume(rds.CreateVolumeOptions{
					Slot:          volumeID,
					FilePath:      fmt.Sprintf("/storage-pool/test/%s.img", volumeID),
					FileSizeBytes: 1 * 1024 * 1024 * 1024,
					NVMETCPPort:   4420,
					NVMETCPNQN:    fmt.Sprintf("nqn.2000-02.com.mikrotik:%s", volumeID),
				})
				_ = client.DeleteVolume(volumeID)
			}
		}(g)
	}

	wg.Wait()

	// Verify command history is consistent (no data races)
	history := server.GetCommandHistory()
	t.Logf("Command history contains %d entries", len(history))

	// Should have recorded all create and delete operations
	expectedCommands := numGoroutines * opsPerGoroutine * 2 // create + delete
	if len(history) < expectedCommands {
		t.Logf("Warning: Expected at least %d commands, got %d (may be truncated by history depth)", expectedCommands, len(history))
	}

	// Verify history entries are valid
	for i, cmd := range history {
		if cmd.Command == "" {
			t.Errorf("history entry %d has empty command", i)
		}
		if cmd.ExitCode < 0 || cmd.ExitCode > 1 {
			t.Errorf("history entry %d has invalid exit code: %d", i, cmd.ExitCode)
		}
	}
}
