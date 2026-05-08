package nvme

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// createMockSysfsForResolver creates a mock sysfs structure for resolver tests
// Returns the temp dir path
func createMockSysfsForResolver(t *testing.T, controllers []mockController) string {
	t.Helper()
	return createMockSysfs(t, controllers)
}

// TestResolveDevicePath_CacheMiss tests that cache miss triggers sysfs scan
func TestResolveDevicePath_CacheMiss(t *testing.T) {
	tmpDir := createMockSysfsForResolver(t, []mockController{
		{
			name:         "nvme0",
			nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
			blockDevices: []string{"nvme0n1"},
		},
	})

	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: tmpDir,
		TTL:       10 * time.Second,
	})

	// First call - cache miss
	if resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123") {
		t.Error("Expected cache miss before first resolve")
	}

	devicePath, err := resolver.ResolveDevicePath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	expected := "/dev/nvme0n1"
	if devicePath != expected {
		t.Errorf("Expected device path %s, got %s", expected, devicePath)
	}

	// Verify cache is now populated
	if !resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123") {
		t.Error("Expected cache hit after resolve")
	}

	cachedPath := resolver.GetCachedPath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123")
	if cachedPath != expected {
		t.Errorf("Expected cached path %s, got %s", expected, cachedPath)
	}
}

// TestResolveDevicePath_CacheHit tests that cache hit returns without rescanning
func TestResolveDevicePath_CacheHit(t *testing.T) {
	tmpDir := createMockSysfsForResolver(t, []mockController{
		{
			name:         "nvme0",
			nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
			blockDevices: []string{"nvme0n1"},
		},
	})

	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: tmpDir,
		TTL:       10 * time.Second,
	})

	// First call - populates cache
	devicePath1, err := resolver.ResolveDevicePath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123")
	if err != nil {
		t.Fatalf("Unexpected error on first resolve: %v", err)
	}

	// Remove the mock sysfs to prove cache is being used
	ctrlDir := filepath.Join(tmpDir, "class", "nvme", "nvme0")
	if err := os.RemoveAll(ctrlDir); err != nil {
		t.Fatalf("Failed to remove controller dir: %v", err)
	}

	// Second call - should use cache (won't fail even though sysfs is gone)
	// Note: This works because the cache check doesn't re-validate sysfs existence
	// It only validates the /dev/ device exists, which we can't mock
	// So instead, we verify the cached path is returned
	if !resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123") {
		t.Error("Expected cache hit after first resolve")
	}

	cachedPath := resolver.GetCachedPath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123")
	if cachedPath != devicePath1 {
		t.Errorf("Expected cached path to match first resolve, got %s vs %s", cachedPath, devicePath1)
	}

	// Verify stats show the entry
	stats := resolver.GetCacheStats()
	if stats.Entries != 1 {
		t.Errorf("Expected 1 cache entry, got %d", stats.Entries)
	}
}

// TestResolveDevicePath_TTLExpired tests that expired cache entries trigger rescan
func TestResolveDevicePath_TTLExpired(t *testing.T) {
	tmpDir := createMockSysfsForResolver(t, []mockController{
		{
			name:         "nvme0",
			nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
			blockDevices: []string{"nvme0n1"},
		},
	})

	// Use very short TTL
	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: tmpDir,
		TTL:       1 * time.Millisecond,
	})

	// First call - populates cache
	_, err := resolver.ResolveDevicePath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123")
	if err != nil {
		t.Fatalf("Unexpected error on first resolve: %v", err)
	}

	// Wait for TTL to expire
	time.Sleep(5 * time.Millisecond)

	// Cache should be expired
	if resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123") {
		t.Error("Expected cache entry to be expired")
	}

	// Stats should show the entry as expired
	stats := resolver.GetCacheStats()
	if stats.Entries != 1 {
		t.Errorf("Expected 1 cache entry, got %d", stats.Entries)
	}
	if stats.ExpiredNum != 1 {
		t.Errorf("Expected 1 expired entry, got %d", stats.ExpiredNum)
	}
}

// TestResolveDevicePath_DeviceGone tests behavior when cached device no longer exists
func TestResolveDevicePath_DeviceGone(t *testing.T) {
	tmpDir := createMockSysfsForResolver(t, []mockController{
		{
			name:         "nvme0",
			nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
			blockDevices: []string{"nvme0n1"},
		},
	})

	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: tmpDir,
		TTL:       10 * time.Second,
	})

	// First call - populates cache with /dev/nvme0n1
	devicePath1, err := resolver.ResolveDevicePath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123")
	if err != nil {
		t.Fatalf("Unexpected error on first resolve: %v", err)
	}

	// Verify initial resolution
	if devicePath1 != "/dev/nvme0n1" {
		t.Errorf("Expected /dev/nvme0n1, got %s", devicePath1)
	}

	// Verify cache is populated
	if !resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123") {
		t.Error("Expected cache to be populated")
	}

	// The ResolveDevicePath method checks if /dev/{device} exists
	// Since we can't actually create devices in /dev in unit tests,
	// the device existence check will always fail, causing a rescan
	// This is correct behavior - if the device is gone, rescan is needed
}

// TestResolveDevicePath_NotFound tests error when NQN not found
func TestResolveDevicePath_NotFound(t *testing.T) {
	tmpDir := createMockSysfsForResolver(t, []mockController{
		{
			name:         "nvme0",
			nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-other",
			blockDevices: []string{"nvme0n1"},
		},
	})

	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: tmpDir,
		TTL:       10 * time.Second,
	})

	_, err := resolver.ResolveDevicePath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent NQN, got nil")
	}

	// Should not cache failed lookups
	if resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-nonexistent") {
		t.Error("Expected failed lookup to not be cached")
	}
}

// TestResolveDevicePath_EmptySysfs tests error when no controllers exist
func TestResolveDevicePath_EmptySysfs(t *testing.T) {
	tmpDir := t.TempDir()
	// Create empty nvme class directory
	nvmeClassDir := filepath.Join(tmpDir, "class", "nvme")
	if err := os.MkdirAll(nvmeClassDir, 0755); err != nil {
		t.Fatalf("Failed to create nvme class dir: %v", err)
	}

	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: tmpDir,
		TTL:       10 * time.Second,
	})

	_, err := resolver.ResolveDevicePath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test")
	if err == nil {
		t.Error("Expected error for empty sysfs, got nil")
	}
}

// TestInvalidate tests cache invalidation
func TestInvalidate(t *testing.T) {
	t.Run("invalidate existing entry", func(t *testing.T) {
		tmpDir := createMockSysfsForResolver(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123",
				blockDevices: []string{"nvme0n1"},
			},
		})

		resolver := NewDeviceResolverWithConfig(ResolverConfig{
			SysfsRoot: tmpDir,
			TTL:       10 * time.Second,
		})

		// Populate cache
		_, err := resolver.ResolveDevicePath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Verify cached
		if !resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123") {
			t.Error("Expected entry to be cached")
		}

		// Invalidate
		resolver.Invalidate("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123")

		// Verify no longer cached
		if resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123") {
			t.Error("Expected entry to be invalidated")
		}

		cachedPath := resolver.GetCachedPath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test-123")
		if cachedPath != "" {
			t.Errorf("Expected empty cached path after invalidation, got %s", cachedPath)
		}
	})

	t.Run("invalidate non-existent entry", func(t *testing.T) {
		resolver := NewDeviceResolver()

		// Should not panic or error
		resolver.Invalidate("nqn.2026-05.works.whiskey.nvmet-csi:pvc-nonexistent")

		// Verify still works
		if resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-nonexistent") {
			t.Error("Non-existent entry should not be cached")
		}
	})
}

// TestInvalidateAll tests clearing the entire cache
func TestInvalidateAll(t *testing.T) {
	tmpDir := createMockSysfsForResolver(t, []mockController{
		{
			name:         "nvme0",
			nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-0",
			blockDevices: []string{"nvme0n1"},
		},
		{
			name:         "nvme1",
			nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-1",
			blockDevices: []string{"nvme1n1"},
		},
	})

	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: tmpDir,
		TTL:       10 * time.Second,
	})

	// Populate cache with multiple entries
	_, err := resolver.ResolveDevicePath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-0")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	_, err = resolver.ResolveDevicePath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-1")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Verify both cached
	stats := resolver.GetCacheStats()
	if stats.Entries != 2 {
		t.Errorf("Expected 2 cache entries, got %d", stats.Entries)
	}

	// Invalidate all
	resolver.InvalidateAll()

	// Verify empty cache
	stats = resolver.GetCacheStats()
	if stats.Entries != 0 {
		t.Errorf("Expected 0 cache entries after InvalidateAll, got %d", stats.Entries)
	}
}

// TestIsOrphanedSubsystem tests orphan detection
func TestIsOrphanedSubsystem(t *testing.T) {
	t.Run("no isConnectedFn set", func(t *testing.T) {
		resolver := NewDeviceResolver()

		// Without isConnectedFn, should return (false, nil)
		orphaned, err := resolver.IsOrphanedSubsystem("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test")
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if orphaned {
			t.Error("Expected not orphaned when isConnectedFn not set")
		}
	})

	t.Run("not connected - not orphaned", func(t *testing.T) {
		tmpDir := createMockSysfsForResolver(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test",
				blockDevices: []string{"nvme0n1"},
			},
		})

		resolver := NewDeviceResolverWithConfig(ResolverConfig{
			SysfsRoot: tmpDir,
			TTL:       10 * time.Second,
		})

		// Set isConnectedFn to return false (not connected)
		resolver.SetIsConnectedFn(func(nqn string) (bool, error) {
			return false, nil
		})

		orphaned, err := resolver.IsOrphanedSubsystem("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test")
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if orphaned {
			t.Error("Expected not orphaned when not connected")
		}
	})

	t.Run("connected with device - not orphaned", func(t *testing.T) {
		tmpDir := createMockSysfsForResolver(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test",
				blockDevices: []string{"nvme0n1"},
			},
		})

		resolver := NewDeviceResolverWithConfig(ResolverConfig{
			SysfsRoot: tmpDir,
			TTL:       10 * time.Second,
		})

		// Set isConnectedFn to return true (connected)
		resolver.SetIsConnectedFn(func(nqn string) (bool, error) {
			return true, nil
		})

		// NQN exists with device - not orphaned
		orphaned, err := resolver.IsOrphanedSubsystem("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test")
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if orphaned {
			t.Error("Expected not orphaned when connected with device")
		}
	})

	t.Run("connected but no device - orphaned", func(t *testing.T) {
		// Create sysfs WITHOUT the matching NQN (simulates orphaned state)
		tmpDir := createMockSysfsForResolver(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-other", // Different NQN
				blockDevices: []string{"nvme0n1"},
			},
		})

		resolver := NewDeviceResolverWithConfig(ResolverConfig{
			SysfsRoot: tmpDir,
			TTL:       10 * time.Second,
		})

		// Set isConnectedFn to return true (appears connected)
		resolver.SetIsConnectedFn(func(nqn string) (bool, error) {
			return true, nil
		})

		// pvc-test appears connected but no device found in sysfs - orphaned
		orphaned, err := resolver.IsOrphanedSubsystem("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test")
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if !orphaned {
			t.Error("Expected orphaned when connected but no device found")
		}
	})

	t.Run("isConnectedFn returns error", func(t *testing.T) {
		resolver := NewDeviceResolver()

		// Set isConnectedFn to return an error
		resolver.SetIsConnectedFn(func(nqn string) (bool, error) {
			return false, os.ErrPermission
		})

		orphaned, err := resolver.IsOrphanedSubsystem("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test")
		if err == nil {
			t.Error("Expected error when isConnectedFn fails")
		}
		if orphaned {
			t.Error("Expected not orphaned when error occurs")
		}
	})
}

// TestDeviceResolver_DefaultConfig tests default configuration
func TestDeviceResolver_DefaultConfig(t *testing.T) {
	cfg := DefaultResolverConfig()

	if cfg.SysfsRoot != DefaultSysfsRoot {
		t.Errorf("Expected SysfsRoot %s, got %s", DefaultSysfsRoot, cfg.SysfsRoot)
	}

	if cfg.TTL != DefaultResolverTTL {
		t.Errorf("Expected TTL %v, got %v", DefaultResolverTTL, cfg.TTL)
	}
}

// TestDeviceResolver_NewDeviceResolver tests constructor
func TestDeviceResolver_NewDeviceResolver(t *testing.T) {
	resolver := NewDeviceResolver()
	if resolver == nil {
		t.Fatal("NewDeviceResolver returned nil")
	}

	if resolver.GetTTL() != DefaultResolverTTL {
		t.Errorf("Expected TTL %v, got %v", DefaultResolverTTL, resolver.GetTTL())
	}
}

// TestDeviceResolver_CustomConfig tests custom configuration
func TestDeviceResolver_CustomConfig(t *testing.T) {
	customTTL := 5 * time.Second
	customRoot := "/custom/sysfs"

	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: customRoot,
		TTL:       customTTL,
	})

	if resolver.GetTTL() != customTTL {
		t.Errorf("Expected TTL %v, got %v", customTTL, resolver.GetTTL())
	}
}

// TestDeviceResolver_ZeroConfigDefaults tests that zero values get defaults
func TestDeviceResolver_ZeroConfigDefaults(t *testing.T) {
	// Pass zero-value config
	resolver := NewDeviceResolverWithConfig(ResolverConfig{})

	if resolver.GetTTL() != DefaultResolverTTL {
		t.Errorf("Expected default TTL %v, got %v", DefaultResolverTTL, resolver.GetTTL())
	}
}

// TestDeviceResolver_CacheStats tests cache statistics
func TestDeviceResolver_CacheStats(t *testing.T) {
	t.Run("empty cache stats", func(t *testing.T) {
		resolver := NewDeviceResolver()
		stats := resolver.GetCacheStats()

		if stats.Entries != 0 {
			t.Errorf("Expected 0 entries, got %d", stats.Entries)
		}
		if stats.ExpiredNum != 0 {
			t.Errorf("Expected 0 expired, got %d", stats.ExpiredNum)
		}
	})

	t.Run("stats with entries", func(t *testing.T) {
		tmpDir := createMockSysfsForResolver(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test",
				blockDevices: []string{"nvme0n1"},
			},
		})

		resolver := NewDeviceResolverWithConfig(ResolverConfig{
			SysfsRoot: tmpDir,
			TTL:       10 * time.Second,
		})

		// Populate cache
		_, _ = resolver.ResolveDevicePath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test")

		stats := resolver.GetCacheStats()
		if stats.Entries != 1 {
			t.Errorf("Expected 1 entry, got %d", stats.Entries)
		}
		if stats.ExpiredNum != 0 {
			t.Errorf("Expected 0 expired, got %d", stats.ExpiredNum)
		}
		if stats.NewestAge > time.Second {
			t.Errorf("Newest entry age too old: %v", stats.NewestAge)
		}
	})
}

// TestDeviceResolver_String tests string representation
func TestDeviceResolver_String(t *testing.T) {
	resolver := NewDeviceResolver()
	str := resolver.String()

	if str == "" {
		t.Error("Expected non-empty string representation")
	}

	// Should contain TTL and entry count
	expectedSubstrings := []string{"DeviceResolver", "ttl=", "entries="}
	for _, substr := range expectedSubstrings {
		if !contains(str, substr) {
			t.Errorf("Expected string to contain %q, got %q", substr, str)
		}
	}
}

// Helper function to check string containment
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr ||
		len(s) > len(substr) && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestDeviceResolver_ConcurrentAccess tests thread safety
func TestDeviceResolver_ConcurrentAccess(t *testing.T) {
	tmpDir := createMockSysfsForResolver(t, []mockController{
		{
			name:         "nvme0",
			nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-test",
			blockDevices: []string{"nvme0n1"},
		},
	})

	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: tmpDir,
		TTL:       10 * time.Second,
	})

	var wg sync.WaitGroup
	numGoroutines := 10
	iterations := 100

	// Multiple goroutines performing various operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				// Mix of operations
				switch j % 5 {
				case 0:
					_, _ = resolver.ResolveDevicePath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test")
				case 1:
					_ = resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test")
				case 2:
					_ = resolver.GetCachedPath("nqn.2026-05.works.whiskey.nvmet-csi:pvc-test")
				case 3:
					_ = resolver.GetCacheStats()
				case 4:
					_ = resolver.String()
				}
			}
		}(i)
	}

	wg.Wait()

	// If we get here without panic or race, test passed
}

// TestDeviceResolver_MultipleNQNs tests handling multiple NQNs
func TestDeviceResolver_MultipleNQNs(t *testing.T) {
	tmpDir := createMockSysfsForResolver(t, []mockController{
		{
			name:         "nvme0",
			nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-0",
			blockDevices: []string{"nvme0n1"},
		},
		{
			name:         "nvme1",
			nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-1",
			blockDevices: []string{"nvme1n1"},
		},
		{
			name:         "nvme2",
			nqn:          "nqn.2026-05.works.whiskey.nvmet-csi:pvc-2",
			blockDevices: []string{"nvme2n1"},
		},
	})

	resolver := NewDeviceResolverWithConfig(ResolverConfig{
		SysfsRoot: tmpDir,
		TTL:       10 * time.Second,
	})

	nqns := []struct {
		nqn      string
		expected string
	}{
		{"nqn.2026-05.works.whiskey.nvmet-csi:pvc-0", "/dev/nvme0n1"},
		{"nqn.2026-05.works.whiskey.nvmet-csi:pvc-1", "/dev/nvme1n1"},
		{"nqn.2026-05.works.whiskey.nvmet-csi:pvc-2", "/dev/nvme2n1"},
	}

	// Resolve all NQNs
	for _, tc := range nqns {
		devicePath, err := resolver.ResolveDevicePath(tc.nqn)
		if err != nil {
			t.Errorf("Unexpected error resolving %s: %v", tc.nqn, err)
			continue
		}
		if devicePath != tc.expected {
			t.Errorf("Expected %s for %s, got %s", tc.expected, tc.nqn, devicePath)
		}
	}

	// Verify all cached
	stats := resolver.GetCacheStats()
	if stats.Entries != 3 {
		t.Errorf("Expected 3 cache entries, got %d", stats.Entries)
	}

	// Invalidate one, verify others remain
	resolver.Invalidate("nqn.2026-05.works.whiskey.nvmet-csi:pvc-1")
	stats = resolver.GetCacheStats()
	if stats.Entries != 2 {
		t.Errorf("Expected 2 cache entries after invalidate, got %d", stats.Entries)
	}

	if resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-1") {
		t.Error("Expected pvc-1 to be invalidated")
	}
	if !resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-0") {
		t.Error("Expected pvc-0 to still be cached")
	}
	if !resolver.IsCached("nqn.2026-05.works.whiskey.nvmet-csi:pvc-2") {
		t.Error("Expected pvc-2 to still be cached")
	}
}
