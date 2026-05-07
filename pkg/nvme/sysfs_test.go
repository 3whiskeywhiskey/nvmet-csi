package nvme

import (
	"os"
	"path/filepath"
	"testing"
)

// mockController represents a mock NVMe controller for testing
type mockController struct {
	name         string   // e.g., "nvme0"
	nqn          string   // NQN value
	namespaces   []string // e.g., ["nvme0n1", "nvme0c1n1"]
	blockDevices []string // e.g., ["nvme0n1"]
}

// createMockSysfs creates a mock sysfs structure in a temp directory
// Returns the temp dir path (use with NewSysfsScannerWithRoot)
func createMockSysfs(t *testing.T, controllers []mockController) string {
	t.Helper()
	tmpDir := t.TempDir()

	for _, ctrl := range controllers {
		// Create controller directory: {tmpDir}/class/nvme/nvme0
		ctrlDir := filepath.Join(tmpDir, "class", "nvme", ctrl.name)
		if err := os.MkdirAll(ctrlDir, 0755); err != nil {
			t.Fatalf("Failed to create controller dir: %v", err)
		}

		// Write subsysnqn file
		if ctrl.nqn != "" {
			nqnPath := filepath.Join(ctrlDir, "subsysnqn")
			if err := os.WriteFile(nqnPath, []byte(ctrl.nqn+"\n"), 0644); err != nil {
				t.Fatalf("Failed to write subsysnqn: %v", err)
			}
		}

		// Create namespace directories under the controller
		for _, ns := range ctrl.namespaces {
			nsDir := filepath.Join(ctrlDir, ns)
			if err := os.MkdirAll(nsDir, 0755); err != nil {
				t.Fatalf("Failed to create namespace dir: %v", err)
			}
		}

		// Create block device entries in /sys/class/block
		for _, bd := range ctrl.blockDevices {
			bdDir := filepath.Join(tmpDir, "class", "block", bd)
			if err := os.MkdirAll(bdDir, 0755); err != nil {
				t.Fatalf("Failed to create block device dir: %v", err)
			}
		}
	}

	return tmpDir
}

// TestSysfsScanner_ScanControllers tests the ScanControllers method
func TestSysfsScanner_ScanControllers(t *testing.T) {
	t.Run("empty sysfs - no controllers", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create the class/nvme directory but no controllers
		nvmeClassDir := filepath.Join(tmpDir, "class", "nvme")
		if err := os.MkdirAll(nvmeClassDir, 0755); err != nil {
			t.Fatalf("Failed to create nvme class dir: %v", err)
		}

		scanner := NewSysfsScannerWithRoot(tmpDir)
		controllers, err := scanner.ScanControllers()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(controllers) != 0 {
			t.Errorf("Expected 0 controllers, got %d", len(controllers))
		}
	})

	t.Run("single controller", func(t *testing.T) {
		tmpDir := createMockSysfs(t, []mockController{
			{name: "nvme0", nqn: "nqn.2000-02.com.mikrotik:pvc-test-123"},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		controllers, err := scanner.ScanControllers()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(controllers) != 1 {
			t.Errorf("Expected 1 controller, got %d", len(controllers))
		}

		expected := filepath.Join(tmpDir, "class", "nvme", "nvme0")
		if controllers[0] != expected {
			t.Errorf("Expected controller path %s, got %s", expected, controllers[0])
		}
	})

	t.Run("multiple controllers", func(t *testing.T) {
		tmpDir := createMockSysfs(t, []mockController{
			{name: "nvme0", nqn: "nqn.2000-02.com.mikrotik:pvc-0"},
			{name: "nvme1", nqn: "nqn.2000-02.com.mikrotik:pvc-1"},
			{name: "nvme2", nqn: "nqn.2000-02.com.mikrotik:pvc-2"},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		controllers, err := scanner.ScanControllers()
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if len(controllers) != 3 {
			t.Errorf("Expected 3 controllers, got %d", len(controllers))
		}
	})

	t.Run("missing nvme class directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Don't create any directories

		scanner := NewSysfsScannerWithRoot(tmpDir)
		controllers, err := scanner.ScanControllers()
		// Glob returns nil (not error) when pattern matches nothing
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
		if len(controllers) != 0 {
			t.Errorf("Expected 0 controllers, got %d", len(controllers))
		}
	})
}

// TestSysfsScanner_ReadSubsysNQN tests the ReadSubsysNQN method
func TestSysfsScanner_ReadSubsysNQN(t *testing.T) {
	t.Run("valid NQN file", func(t *testing.T) {
		tmpDir := createMockSysfs(t, []mockController{
			{name: "nvme0", nqn: "nqn.2000-02.com.mikrotik:pvc-test-123"},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		controllerPath := filepath.Join(tmpDir, "class", "nvme", "nvme0")

		nqn, err := scanner.ReadSubsysNQN(controllerPath)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		expected := "nqn.2000-02.com.mikrotik:pvc-test-123"
		if nqn != expected {
			t.Errorf("Expected NQN %q, got %q", expected, nqn)
		}
	})

	t.Run("NQN with trailing whitespace", func(t *testing.T) {
		tmpDir := t.TempDir()
		ctrlDir := filepath.Join(tmpDir, "class", "nvme", "nvme0")
		if err := os.MkdirAll(ctrlDir, 0755); err != nil {
			t.Fatalf("Failed to create controller dir: %v", err)
		}

		// Write NQN with extra whitespace
		nqnPath := filepath.Join(ctrlDir, "subsysnqn")
		if err := os.WriteFile(nqnPath, []byte("  nqn.2000-02.com.mikrotik:pvc-test  \n\t"), 0644); err != nil {
			t.Fatalf("Failed to write subsysnqn: %v", err)
		}

		scanner := NewSysfsScannerWithRoot(tmpDir)
		nqn, err := scanner.ReadSubsysNQN(ctrlDir)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		expected := "nqn.2000-02.com.mikrotik:pvc-test"
		if nqn != expected {
			t.Errorf("Expected NQN %q, got %q", expected, nqn)
		}
	})

	t.Run("missing subsysnqn file", func(t *testing.T) {
		tmpDir := t.TempDir()
		ctrlDir := filepath.Join(tmpDir, "class", "nvme", "nvme0")
		if err := os.MkdirAll(ctrlDir, 0755); err != nil {
			t.Fatalf("Failed to create controller dir: %v", err)
		}
		// Don't create the subsysnqn file

		scanner := NewSysfsScannerWithRoot(tmpDir)
		_, err := scanner.ReadSubsysNQN(ctrlDir)
		if err == nil {
			t.Error("Expected error for missing subsysnqn file, got nil")
		}
	})

	t.Run("empty subsysnqn file", func(t *testing.T) {
		tmpDir := t.TempDir()
		ctrlDir := filepath.Join(tmpDir, "class", "nvme", "nvme0")
		if err := os.MkdirAll(ctrlDir, 0755); err != nil {
			t.Fatalf("Failed to create controller dir: %v", err)
		}

		// Write empty NQN file
		nqnPath := filepath.Join(ctrlDir, "subsysnqn")
		if err := os.WriteFile(nqnPath, []byte("   \n"), 0644); err != nil {
			t.Fatalf("Failed to write subsysnqn: %v", err)
		}

		scanner := NewSysfsScannerWithRoot(tmpDir)
		_, err := scanner.ReadSubsysNQN(ctrlDir)
		if err == nil {
			t.Error("Expected error for empty subsysnqn file, got nil")
		}
	})

	t.Run("invalid controller path", func(t *testing.T) {
		scanner := NewSysfsScannerWithRoot("/nonexistent")
		_, err := scanner.ReadSubsysNQN("/nonexistent/controller")
		if err == nil {
			t.Error("Expected error for invalid path, got nil")
		}
	})
}

// TestSysfsScanner_FindBlockDevice tests the FindBlockDevice method
func TestSysfsScanner_FindBlockDevice(t *testing.T) {
	t.Run("block device via sysfs class block", func(t *testing.T) {
		tmpDir := createMockSysfs(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2000-02.com.mikrotik:pvc-test-123",
				namespaces:   []string{},          // No namespace dirs under controller
				blockDevices: []string{"nvme0n1"}, // Block device exists in /sys/class/block
			},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		controllerPath := filepath.Join(tmpDir, "class", "nvme", "nvme0")

		devicePath, err := scanner.FindBlockDevice(controllerPath)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		expected := "/dev/nvme0n1"
		if devicePath != expected {
			t.Errorf("Expected device path %s, got %s", expected, devicePath)
		}
	})

	t.Run("prefer nvmeXnY over nvmeXcYnZ in block class", func(t *testing.T) {
		tmpDir := createMockSysfs(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2000-02.com.mikrotik:pvc-test-123",
				namespaces:   []string{},
				blockDevices: []string{"nvme0c1n1", "nvme0n1"}, // Both exist
			},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		controllerPath := filepath.Join(tmpDir, "class", "nvme", "nvme0")

		devicePath, err := scanner.FindBlockDevice(controllerPath)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Should prefer nvme0n1 (no 'c' in name)
		expected := "/dev/nvme0n1"
		if devicePath != expected {
			t.Errorf("Expected device path %s, got %s", expected, devicePath)
		}
	})

	// Note: The nvmeXcYnZ fallback path (Strategy 1) requires real /dev devices
	// which cannot be mocked in unit tests without root access.
	// The block pattern in Strategy 2 only matches nvme{N}n* (not nvme{N}c{X}n{Y}).
	// This is by design - the simple naming (nvmeXnY) is preferred.

	t.Run("controller-based name only - returns error", func(t *testing.T) {
		// When only nvme0c1n1 exists in /sys/class/block (not nvme0n*),
		// the pattern match fails because it looks for "nvme0n*"
		tmpDir := createMockSysfs(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2000-02.com.mikrotik:pvc-test-123",
				namespaces:   []string{},
				blockDevices: []string{"nvme0c1n1"}, // Pattern nvme0n* won't match this
			},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		controllerPath := filepath.Join(tmpDir, "class", "nvme", "nvme0")

		_, err := scanner.FindBlockDevice(controllerPath)
		// This should error because nvme0c1n1 doesn't match pattern nvme0n*
		if err == nil {
			t.Error("Expected error when only controller-based name exists, got nil")
		}
	})

	t.Run("no matching block device", func(t *testing.T) {
		tmpDir := createMockSysfs(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2000-02.com.mikrotik:pvc-test-123",
				namespaces:   []string{},
				blockDevices: []string{}, // No block devices
			},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		controllerPath := filepath.Join(tmpDir, "class", "nvme", "nvme0")

		_, err := scanner.FindBlockDevice(controllerPath)
		if err == nil {
			t.Error("Expected error when no block device found, got nil")
		}
	})

	t.Run("multiple block devices returns first simple name", func(t *testing.T) {
		tmpDir := createMockSysfs(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2000-02.com.mikrotik:pvc-test-123",
				namespaces:   []string{},
				blockDevices: []string{"nvme0n1", "nvme0n2"}, // Multiple namespaces
			},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		controllerPath := filepath.Join(tmpDir, "class", "nvme", "nvme0")

		devicePath, err := scanner.FindBlockDevice(controllerPath)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		// Should return one of the simple names (order depends on glob)
		if devicePath != "/dev/nvme0n1" && devicePath != "/dev/nvme0n2" {
			t.Errorf("Expected /dev/nvme0n1 or /dev/nvme0n2, got %s", devicePath)
		}
	})
}

// TestSysfsScanner_FindDeviceByNQN tests the FindDeviceByNQN method
func TestSysfsScanner_FindDeviceByNQN(t *testing.T) {
	t.Run("find device by NQN - single controller", func(t *testing.T) {
		tmpDir := createMockSysfs(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2000-02.com.mikrotik:pvc-test-123",
				blockDevices: []string{"nvme0n1"},
			},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		devicePath, err := scanner.FindDeviceByNQN("nqn.2000-02.com.mikrotik:pvc-test-123")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		expected := "/dev/nvme0n1"
		if devicePath != expected {
			t.Errorf("Expected device path %s, got %s", expected, devicePath)
		}
	})

	t.Run("find device by NQN - multiple controllers", func(t *testing.T) {
		tmpDir := createMockSysfs(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2000-02.com.mikrotik:pvc-other",
				blockDevices: []string{"nvme0n1"},
			},
			{
				name:         "nvme1",
				nqn:          "nqn.2000-02.com.mikrotik:pvc-target",
				blockDevices: []string{"nvme1n1"},
			},
			{
				name:         "nvme2",
				nqn:          "nqn.2000-02.com.mikrotik:pvc-another",
				blockDevices: []string{"nvme2n1"},
			},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		devicePath, err := scanner.FindDeviceByNQN("nqn.2000-02.com.mikrotik:pvc-target")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		expected := "/dev/nvme1n1"
		if devicePath != expected {
			t.Errorf("Expected device path %s, got %s", expected, devicePath)
		}
	})

	t.Run("NQN not found", func(t *testing.T) {
		tmpDir := createMockSysfs(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2000-02.com.mikrotik:pvc-other",
				blockDevices: []string{"nvme0n1"},
			},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		_, err := scanner.FindDeviceByNQN("nqn.2000-02.com.mikrotik:pvc-nonexistent")
		if err == nil {
			t.Error("Expected error for non-existent NQN, got nil")
		}
	})

	t.Run("NQN found but no block device", func(t *testing.T) {
		tmpDir := createMockSysfs(t, []mockController{
			{
				name:         "nvme0",
				nqn:          "nqn.2000-02.com.mikrotik:pvc-test",
				blockDevices: []string{}, // No block device
			},
		})

		scanner := NewSysfsScannerWithRoot(tmpDir)
		_, err := scanner.FindDeviceByNQN("nqn.2000-02.com.mikrotik:pvc-test")
		if err == nil {
			t.Error("Expected error when NQN found but no block device, got nil")
		}
	})

	t.Run("empty sysfs", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create empty nvme class directory
		nvmeClassDir := filepath.Join(tmpDir, "class", "nvme")
		if err := os.MkdirAll(nvmeClassDir, 0755); err != nil {
			t.Fatalf("Failed to create nvme class dir: %v", err)
		}

		scanner := NewSysfsScannerWithRoot(tmpDir)
		_, err := scanner.FindDeviceByNQN("nqn.2000-02.com.mikrotik:pvc-test")
		if err == nil {
			t.Error("Expected error for empty sysfs, got nil")
		}
	})

	t.Run("controller without subsysnqn skipped", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create controller without subsysnqn (should be skipped)
		ctrl0Dir := filepath.Join(tmpDir, "class", "nvme", "nvme0")
		if err := os.MkdirAll(ctrl0Dir, 0755); err != nil {
			t.Fatalf("Failed to create controller dir: %v", err)
		}
		// Don't write subsysnqn file

		// Create controller with subsysnqn
		ctrl1Dir := filepath.Join(tmpDir, "class", "nvme", "nvme1")
		if err := os.MkdirAll(ctrl1Dir, 0755); err != nil {
			t.Fatalf("Failed to create controller dir: %v", err)
		}
		nqnPath := filepath.Join(ctrl1Dir, "subsysnqn")
		if err := os.WriteFile(nqnPath, []byte("nqn.2000-02.com.mikrotik:pvc-test\n"), 0644); err != nil {
			t.Fatalf("Failed to write subsysnqn: %v", err)
		}

		// Create block device for nvme1
		bdDir := filepath.Join(tmpDir, "class", "block", "nvme1n1")
		if err := os.MkdirAll(bdDir, 0755); err != nil {
			t.Fatalf("Failed to create block device dir: %v", err)
		}

		scanner := NewSysfsScannerWithRoot(tmpDir)
		devicePath, err := scanner.FindDeviceByNQN("nqn.2000-02.com.mikrotik:pvc-test")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		expected := "/dev/nvme1n1"
		if devicePath != expected {
			t.Errorf("Expected device path %s, got %s", expected, devicePath)
		}
	})
}

// TestSysfsScanner_NewSysfsScanner tests constructor functions
func TestSysfsScanner_NewSysfsScanner(t *testing.T) {
	t.Run("default scanner", func(t *testing.T) {
		scanner := NewSysfsScanner()
		if scanner == nil {
			t.Fatal("NewSysfsScanner returned nil")
			return
		}
		if scanner.Root != DefaultSysfsRoot {
			t.Errorf("Expected root %s, got %s", DefaultSysfsRoot, scanner.Root)
		}
	})

	t.Run("custom root scanner", func(t *testing.T) {
		customRoot := "/custom/sysfs"
		scanner := NewSysfsScannerWithRoot(customRoot)
		if scanner == nil {
			t.Fatal("NewSysfsScannerWithRoot returned nil")
			return
		}
		if scanner.Root != customRoot {
			t.Errorf("Expected root %s, got %s", customRoot, scanner.Root)
		}
	})
}
