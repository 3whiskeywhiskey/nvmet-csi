package mount

import (
	"fmt"
	"os"
	"path/filepath"

	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/nvme"
)

// StaleReason describes why a mount is considered stale
type StaleReason string

const (
	StaleReasonNotStale          StaleReason = ""
	StaleReasonMountNotFound     StaleReason = "mount_not_found"
	StaleReasonDeviceDisappeared StaleReason = "device_disappeared"
	StaleReasonDeviceMismatch    StaleReason = "device_path_mismatch"
)

// StaleInfo contains detailed information about a stale mount check
type StaleInfo struct {
	MountDevice     string // Device path from /proc/mountinfo
	ResolvedMount   string // Resolved symlinks for mount device
	CurrentDevice   string // Device path from NQN resolution
	ResolvedCurrent string // Resolved symlinks for current device
	IsStale         bool
	Reason          StaleReason
}

// StaleMountChecker detects stale mounts by comparing mount device with NQN resolution
type StaleMountChecker struct {
	resolver    *nvme.DeviceResolver
	getMountDev func(path string) (string, error) // Injected for testing
}

// NewStaleMountChecker creates a new stale mount checker
func NewStaleMountChecker(resolver *nvme.DeviceResolver) *StaleMountChecker {
	return &StaleMountChecker{
		resolver:    resolver,
		getMountDev: GetMountDevice, // Use default implementation
	}
}

// SetMountDeviceFunc allows overriding the mount device lookup function for testing
func (c *StaleMountChecker) SetMountDeviceFunc(fn func(path string) (string, error)) {
	c.getMountDev = fn
}

// IsMountStale checks if a mount is stale by comparing the mount device with the current NQN-resolved device
// Returns (stale bool, reason StaleReason, err error)
//
// A mount is considered stale if:
// 1. The mount point is not found (mount disappeared)
// 2. The mount device no longer exists (device disappeared)
// 3. The mount device path differs from the current NQN-resolved device (device renumbered)
func (c *StaleMountChecker) IsMountStale(mountPath string, nqn string) (bool, StaleReason, error) {
	klog.V(4).Infof("Checking if mount %s is stale (NQN: %s)", mountPath, nqn)

	// Step 1: Get current mount device
	mountDevice, err := c.getMountDev(mountPath)
	if err != nil {
		// Mount not found - this is a stale condition
		klog.V(4).Infof("Mount %s not found in /proc/mountinfo: %v", mountPath, err)
		return true, StaleReasonMountNotFound, nil
	}

	klog.V(4).Infof("Mount %s device from mountinfo: %s", mountPath, mountDevice)

	// Step 2: Resolve mount device symlinks to canonical path
	resolvedMount, err := filepath.EvalSymlinks(mountDevice)
	if err != nil {
		// Device disappeared - this is a stale condition
		if os.IsNotExist(err) {
			klog.Warningf("Mount device %s no longer exists (mount %s)", mountDevice, mountPath)
			return true, StaleReasonDeviceDisappeared, nil
		}
		// Other errors (permission denied, etc.) should be propagated
		return false, "", fmt.Errorf("failed to resolve mount device symlinks for %s: %w", mountDevice, err)
	}

	klog.V(4).Infof("Resolved mount device %s -> %s", mountDevice, resolvedMount)

	// Step 3: Resolve NQN to current device path
	// If resolver is nil (test environment), skip staleness check
	if c.resolver == nil {
		klog.V(4).Infof("Resolver is nil, skipping staleness check for %s", mountPath)
		return false, "", nil
	}

	currentDevice, err := c.resolver.ResolveDevicePath(nqn)
	if err != nil {
		// Cannot resolve NQN - this is an error, not a stale condition
		return false, "", fmt.Errorf("failed to resolve NQN %s: %w", nqn, err)
	}

	klog.V(4).Infof("Current device for NQN %s: %s", nqn, currentDevice)

	// Step 4: Resolve current device symlinks to canonical path
	resolvedCurrent, err := filepath.EvalSymlinks(currentDevice)
	if err != nil {
		// Current device should exist since we just resolved it
		// If it doesn't, this is an error condition
		return false, "", fmt.Errorf("failed to resolve current device symlinks for %s: %w", currentDevice, err)
	}

	klog.V(4).Infof("Resolved current device %s -> %s", currentDevice, resolvedCurrent)

	// Step 5: Compare resolved paths
	if resolvedMount != resolvedCurrent {
		klog.Warningf("Stale mount detected: mount %s device %s (resolved: %s) differs from current NQN %s device %s (resolved: %s)",
			mountPath, mountDevice, resolvedMount, nqn, currentDevice, resolvedCurrent)
		return true, StaleReasonDeviceMismatch, nil
	}

	klog.V(4).Infof("Mount %s is not stale: device %s matches current NQN %s device %s",
		mountPath, mountDevice, nqn, currentDevice)
	return false, StaleReasonNotStale, nil
}

// GetStaleInfo returns detailed information about a stale mount check
// This is useful for debugging and logging
func (c *StaleMountChecker) GetStaleInfo(mountPath string, nqn string) (*StaleInfo, error) {
	info := &StaleInfo{}

	// If resolver is nil (test environment), skip staleness checking entirely
	// This prevents EvalSymlinks from failing on non-existent mock devices
	if c.resolver == nil {
		klog.V(4).Infof("Resolver is nil, skipping staleness check for %s (test mode)", mountPath)
		info.IsStale = false
		info.Reason = StaleReasonNotStale
		return info, nil
	}

	// Get mount device
	mountDevice, err := c.getMountDev(mountPath)
	if err != nil {
		info.IsStale = true
		info.Reason = StaleReasonMountNotFound
		return info, nil
	}
	info.MountDevice = mountDevice

	// Resolve mount device
	resolvedMount, err := filepath.EvalSymlinks(mountDevice)
	if err != nil {
		if os.IsNotExist(err) {
			info.IsStale = true
			info.Reason = StaleReasonDeviceDisappeared
			return info, nil
		}
		return nil, fmt.Errorf("failed to resolve mount device symlinks: %w", err)
	}
	info.ResolvedMount = resolvedMount

	currentDevice, err := c.resolver.ResolveDevicePath(nqn)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve NQN: %w", err)
	}
	info.CurrentDevice = currentDevice

	// Resolve current device
	resolvedCurrent, err := filepath.EvalSymlinks(currentDevice)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve current device symlinks: %w", err)
	}
	info.ResolvedCurrent = resolvedCurrent

	// Compare
	if resolvedMount != resolvedCurrent {
		info.IsStale = true
		info.Reason = StaleReasonDeviceMismatch
	} else {
		info.IsStale = false
		info.Reason = StaleReasonNotStale
	}

	return info, nil
}
