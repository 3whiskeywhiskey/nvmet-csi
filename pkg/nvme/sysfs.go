package nvme

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"k8s.io/klog/v2"
)

const (
	// DefaultSysfsRoot is the default root path for sysfs
	DefaultSysfsRoot = "/sys"
)

// SysfsScanner provides configurable sysfs access for testing
type SysfsScanner struct {
	Root string // "/sys" in production, temp dir in tests
}

// NewSysfsScanner creates scanner with default root
func NewSysfsScanner() *SysfsScanner {
	return &SysfsScanner{
		Root: DefaultSysfsRoot,
	}
}

// NewSysfsScannerWithRoot creates scanner with custom root (for testing)
func NewSysfsScannerWithRoot(root string) *SysfsScanner {
	return &SysfsScanner{
		Root: root,
	}
}

// ScanControllers returns all NVMe controller paths
// e.g., ["/sys/class/nvme/nvme0", "/sys/class/nvme/nvme1"]
func (s *SysfsScanner) ScanControllers() ([]string, error) {
	pattern := filepath.Join(s.Root, "class", "nvme", "nvme*")
	controllers, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to scan nvme controllers at %s: %w", pattern, err)
	}

	klog.V(6).Infof("ScanControllers: found %d controllers at %s", len(controllers), pattern)
	return controllers, nil
}

// ReadSubsysNQN reads the subsysnqn file from a controller path
func (s *SysfsScanner) ReadSubsysNQN(controllerPath string) (string, error) {
	nqnPath := filepath.Join(controllerPath, "subsysnqn")
	data, err := os.ReadFile(nqnPath)
	if err != nil {
		return "", fmt.Errorf("failed to read subsysnqn from %s: %w", nqnPath, err)
	}

	nqn := strings.TrimSpace(string(data))
	if nqn == "" {
		return "", fmt.Errorf("empty subsysnqn at %s", nqnPath)
	}

	klog.V(6).Infof("ReadSubsysNQN: %s -> %s", controllerPath, nqn)
	return nqn, nil
}

// FindBlockDevice finds the block device for a controller
// Handles both nvmeXnY (preferred) and nvmeXcYnZ (fallback) naming
func (s *SysfsScanner) FindBlockDevice(controllerPath string) (string, error) {
	controllerName := filepath.Base(controllerPath)

	// Strategy 1: Look for namespace directories directly under the controller
	// For NVMe-oF, namespaces appear as subdirectories with names like nvme2c1n2
	namespacePattern := filepath.Join(controllerPath, "nvme*n*")
	namespaces, err := filepath.Glob(namespacePattern)
	if err != nil {
		klog.V(6).Infof("FindBlockDevice: failed to scan namespaces under %s: %v", controllerPath, err)
	}

	for _, ns := range namespaces {
		nsName := filepath.Base(ns)

		// Check if this namespace exists as a block device directly
		devPath := "/dev/" + nsName
		if _, err := os.Stat(devPath); err == nil {
			klog.V(4).Infof("FindBlockDevice: found direct device %s", devPath)
			return devPath, nil
		}

		// For controller-based paths (nvmeXcYnZ), also check subsystem-based path (nvmeXnZ)
		// This is preferred for multipath scenarios
		if strings.Contains(nsName, "c") {
			// Parse nvmeXcYnZ to get X and Z
			var subsys, ctrl, namespace int
			if _, scanErr := fmt.Sscanf(nsName, "nvme%dc%dn%d", &subsys, &ctrl, &namespace); scanErr == nil {
				subsysDevice := fmt.Sprintf("nvme%dn%d", subsys, namespace)
				devPath := "/dev/" + subsysDevice
				if _, err := os.Stat(devPath); err == nil {
					klog.V(4).Infof("FindBlockDevice: found subsystem-based device %s (from %s)", devPath, nsName)
					return devPath, nil
				}
			}
		}
	}

	// Strategy 2: Fallback - scan /sys/class/block for controller-named devices
	blockPattern := filepath.Join(s.Root, "class", "block", controllerName+"n*")
	blockDevices, err := filepath.Glob(blockPattern)
	if err != nil {
		return "", fmt.Errorf("failed to scan block devices at %s: %w", blockPattern, err)
	}

	// Prefer nvmeXnY format (no "c" in name) over nvmeXcYnZ
	for _, blockDev := range blockDevices {
		deviceName := filepath.Base(blockDev)
		if !strings.Contains(deviceName, "c") {
			devPath := "/dev/" + deviceName
			klog.V(4).Infof("FindBlockDevice: found block device %s (preferred, no controller)", devPath)
			return devPath, nil
		}
	}

	// If no simple path found, use any available
	if len(blockDevices) > 0 {
		deviceName := filepath.Base(blockDevices[0])
		devPath := "/dev/" + deviceName
		klog.V(4).Infof("FindBlockDevice: found block device %s (fallback)", devPath)
		return devPath, nil
	}

	return "", fmt.Errorf("no block device found for controller %s", controllerName)
}

// ListSubsystemNQNs returns all NQNs from /sys/class/nvme-subsystem/*/subsysnqn
// This provides enumeration of all connected NVMe subsystems for orphan detection.
func (s *SysfsScanner) ListSubsystemNQNs() ([]string, error) {
	subsysDir := filepath.Join(s.Root, "class", "nvme-subsystem")

	entries, err := os.ReadDir(subsysDir)
	if err != nil {
		if os.IsNotExist(err) {
			klog.V(4).Infof("ListSubsystemNQNs: nvme-subsystem directory does not exist")
			return nil, nil // No subsystems connected
		}
		return nil, fmt.Errorf("failed to read nvme-subsystem directory: %w", err)
	}

	var nqns []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		nqnPath := filepath.Join(subsysDir, entry.Name(), "subsysnqn")
		data, err := os.ReadFile(nqnPath)
		if err != nil {
			klog.V(4).Infof("ListSubsystemNQNs: could not read NQN from %s: %v", nqnPath, err)
			continue
		}
		nqn := strings.TrimSpace(string(data))
		if nqn != "" {
			nqns = append(nqns, nqn)
		}
	}

	klog.V(4).Infof("ListSubsystemNQNs: found %d subsystems", len(nqns))
	return nqns, nil
}

// FindDeviceByNQN scans all controllers to find the device path for a given NQN
// This is a convenience function that combines ScanControllers, ReadSubsysNQN, and FindBlockDevice
func (s *SysfsScanner) FindDeviceByNQN(nqn string) (string, error) {
	controllers, err := s.ScanControllers()
	if err != nil {
		return "", err
	}

	for _, controller := range controllers {
		controllerNQN, err := s.ReadSubsysNQN(controller)
		if err != nil {
			klog.V(6).Infof("FindDeviceByNQN: skipping controller %s: %v", controller, err)
			continue
		}

		if controllerNQN == nqn {
			devicePath, err := s.FindBlockDevice(controller)
			if err != nil {
				return "", fmt.Errorf("found controller for NQN %s but no block device: %w", nqn, err)
			}
			klog.V(4).Infof("FindDeviceByNQN: resolved NQN %s -> %s", nqn, devicePath)
			return devicePath, nil
		}
	}

	return "", fmt.Errorf("no device found for NQN: %s", nqn)
}
