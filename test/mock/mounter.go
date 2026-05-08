package mock

import (
	"fmt"
	"os"
	"sync"
	"time"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/mount"
)

// MockMounter is a mock implementation of mount.Mounter for testing
type MockMounter struct {
	mu sync.RWMutex

	// Mounted filesystems: target path -> source device
	mounted map[string]string

	// Formatted devices: device path -> filesystem type
	formatted map[string]string

	// Error injection
	mountErr   error
	unmountErr error
	formatErr  error

	// Call tracking
	mountCalls   []MountCall
	unmountCalls []string
	formatCalls  []FormatCall
}

// MountCall tracks a Mount operation
type MountCall struct {
	Source  string
	Target  string
	FSType  string
	Options []string
}

// FormatCall tracks a Format operation
type FormatCall struct {
	Device string
	FSType string
}

// NewMockMounter creates a new mock mounter
func NewMockMounter() *MockMounter {
	return &MockMounter{
		mounted:   make(map[string]string),
		formatted: make(map[string]string),
	}
}

// Mount implements mount.Mounter
func (m *MockMounter) Mount(source, target, fsType string, options []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Track call
	m.mountCalls = append(m.mountCalls, MountCall{
		Source:  source,
		Target:  target,
		FSType:  fsType,
		Options: options,
	})

	// Check for error injection
	if m.mountErr != nil {
		return m.mountErr
	}

	// Create target directory if it doesn't exist (simulate mount behavior)
	if err := os.MkdirAll(target, 0755); err != nil {
		return fmt.Errorf("failed to create target directory: %w", err)
	}

	// Record mount
	m.mounted[target] = source

	return nil
}

// Unmount implements mount.Mounter
func (m *MockMounter) Unmount(target string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Track call
	m.unmountCalls = append(m.unmountCalls, target)

	// Check for error injection
	if m.unmountErr != nil {
		return m.unmountErr
	}

	// Remove from mounted map
	delete(m.mounted, target)

	return nil
}

// IsLikelyMountPoint implements mount.Mounter
func (m *MockMounter) IsLikelyMountPoint(path string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, mounted := m.mounted[path]
	return mounted, nil
}

// Format implements mount.Mounter
func (m *MockMounter) Format(device, fsType string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Track call
	m.formatCalls = append(m.formatCalls, FormatCall{
		Device: device,
		FSType: fsType,
	})

	// Check for error injection
	if m.formatErr != nil {
		return m.formatErr
	}

	// Record formatted device
	m.formatted[device] = fsType

	return nil
}

// IsFormatted implements mount.Mounter
func (m *MockMounter) IsFormatted(device string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, formatted := m.formatted[device]
	return formatted, nil
}

// ResizeFilesystem implements mount.Mounter
func (m *MockMounter) ResizeFilesystem(device, volumePath string) error {
	// Mock implementation - just return success
	return nil
}

// GetDeviceStats implements mount.Mounter
func (m *MockMounter) GetDeviceStats(path string) (*mount.DeviceStats, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check if path is mounted
	if _, mounted := m.mounted[path]; !mounted {
		return nil, fmt.Errorf("not mounted: %s", path)
	}

	// Return fake stats
	return &mount.DeviceStats{
		TotalBytes:      10 * 1024 * 1024 * 1024, // 10 GiB
		UsedBytes:       1 * 1024 * 1024 * 1024,  // 1 GiB
		AvailableBytes:  9 * 1024 * 1024 * 1024,  // 9 GiB
		TotalInodes:     1000000,
		UsedInodes:      100000,
		AvailableInodes: 900000,
	}, nil
}

// ForceUnmount implements mount.Mounter
func (m *MockMounter) ForceUnmount(target string, timeout time.Duration) error {
	// Use regular unmount for mock
	return m.Unmount(target)
}

// IsMountInUse implements mount.Mounter
func (m *MockMounter) IsMountInUse(path string) (bool, []int, error) {
	// Mock implementation - return not in use
	return false, nil, nil
}

// MakeFile implements mount.Mounter
func (m *MockMounter) MakeFile(pathname string) error {
	// Create an empty file
	f, err := os.Create(pathname)
	if err != nil {
		return err
	}
	return f.Close()
}

// Test helper methods

// SetMountError sets an error to return on Mount operations
func (m *MockMounter) SetMountError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mountErr = err
}

// SetUnmountError sets an error to return on Unmount operations
func (m *MockMounter) SetUnmountError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unmountErr = err
}

// SetFormatError sets an error to return on Format operations
func (m *MockMounter) SetFormatError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.formatErr = err
}

// ClearErrors clears all error injection
func (m *MockMounter) ClearErrors() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mountErr = nil
	m.unmountErr = nil
	m.formatErr = nil
}

// GetMountCalls returns the history of Mount calls
func (m *MockMounter) GetMountCalls() []MountCall {
	m.mu.RLock()
	defer m.mu.RUnlock()
	calls := make([]MountCall, len(m.mountCalls))
	copy(calls, m.mountCalls)
	return calls
}

// GetUnmountCalls returns the history of Unmount calls
func (m *MockMounter) GetUnmountCalls() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	calls := make([]string, len(m.unmountCalls))
	copy(calls, m.unmountCalls)
	return calls
}

// IsMounted checks if a path is currently mounted
func (m *MockMounter) IsMounted(path string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, mounted := m.mounted[path]
	return mounted
}

// GetMountDevice returns the source device for a mounted path
// This is used for mock getMountDev injection in stale mount checker
func (m *MockMounter) GetMountDevice(path string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	device, mounted := m.mounted[path]
	if !mounted {
		return "", fmt.Errorf("path %s is not mounted", path)
	}
	return device, nil
}

// Reset clears all state for test isolation
func (m *MockMounter) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mounted = make(map[string]string)
	m.formatted = make(map[string]string)
	m.mountCalls = nil
	m.unmountCalls = nil
	m.formatCalls = nil
	m.ClearErrors()
}
