package rds

import (
	"fmt"
	"sync"
	"time"
)

// MockClient is a mock implementation of RDSClient for testing
type MockClient struct {
	mu             sync.RWMutex
	volumes        map[string]*VolumeInfo
	snapshots      map[string]*SnapshotInfo
	mutationCounts map[string]int
	address        string
	connected      bool                   // Connection state (for testing connection manager)
	nextError      error                  // Error to return on next operation
	persistentErr  error                  // Error to return on all operations until cleared
	diskMetrics    *DiskMetrics           // Configurable disk metrics response (test helper)
	hardwareHealth *HardwareHealthMetrics // Configurable hardware health response (test helper)
}

// NewMockClient creates a new MockClient for testing
func NewMockClient() *MockClient {
	return &MockClient{
		volumes:        make(map[string]*VolumeInfo),
		snapshots:      make(map[string]*SnapshotInfo),
		mutationCounts: make(map[string]int),
		address:        "mock-rds-server",
		connected:      true, // Default to connected
	}
}

// AddVolume adds a volume to the mock (test helper)
func (m *MockClient) AddVolume(v *VolumeInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.volumes[v.Slot] = v
}

// RemoveVolume removes a volume from the mock (test helper)
func (m *MockClient) RemoveVolume(slot string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.volumes, slot)
}

// AddSnapshot adds a snapshot to the mock (test helper)
func (m *MockClient) AddSnapshot(s *SnapshotInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.snapshots[s.Name] = s
}

// RemoveSnapshot removes a snapshot from the mock (test helper)
func (m *MockClient) RemoveSnapshot(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.snapshots, name)
}

// SetAddress sets the mock address (test helper)
func (m *MockClient) SetAddress(addr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.address = addr
}

// SetError sets an error to be returned on the next operation (test helper)
func (m *MockClient) SetError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextError = err
}

// SetPersistentError sets an error to be returned on ALL operations until cleared (test helper)
func (m *MockClient) SetPersistentError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persistentErr = err
}

// ClearError clears any pending error (test helper)
func (m *MockClient) ClearError() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextError = nil
	m.persistentErr = nil
}

// MutationCount returns how many times a mutating operation was attempted.
func (m *MockClient) MutationCount(operation string) int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mutationCounts[operation]
}

// SetConnected sets the connection state (test helper)
func (m *MockClient) SetConnected(connected bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = connected
}

// checkError checks for and clears pending error
func (m *MockClient) checkError() error {
	// Check persistent error first
	if m.persistentErr != nil {
		return m.persistentErr
	}
	// Then check one-time error
	if m.nextError != nil {
		err := m.nextError
		m.nextError = nil
		return err
	}
	return nil
}

// Connect implements RDSClient
func (m *MockClient) Connect() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for persistent error (simulates connection failure)
	if m.persistentErr != nil {
		return m.persistentErr
	}

	// Mark as connected on successful connect
	m.connected = true
	return nil
}

// Close implements RDSClient
func (m *MockClient) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = false
	return nil
}

// IsConnected implements RDSClient
func (m *MockClient) IsConnected() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.connected
}

// GetAddress implements RDSClient
func (m *MockClient) GetAddress() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.address
}

// CreateVolume implements RDSClient
func (m *MockClient) CreateVolume(opts CreateVolumeOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for pending error
	if err := m.checkError(); err != nil {
		return err
	}
	m.mutationCounts["create_volume"]++

	if _, exists := m.volumes[opts.Slot]; exists {
		return fmt.Errorf("volume %s already exists", opts.Slot)
	}

	m.volumes[opts.Slot] = &VolumeInfo{
		Slot:          opts.Slot,
		Type:          "file",
		FilePath:      opts.FilePath,
		FileSizeBytes: opts.FileSizeBytes,
		Mount:         opts.Mount,
		NVMETCPExport: true,
		NVMETCPPort:   opts.NVMETCPPort,
		NVMETCPNQN:    opts.NVMETCPNQN,
		Status:        "ready",
	}
	return nil
}

// DeleteVolume implements RDSClient
func (m *MockClient) DeleteVolume(slot string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for pending error
	if err := m.checkError(); err != nil {
		return err
	}
	m.mutationCounts["delete_volume"]++

	if _, exists := m.volumes[slot]; !exists {
		// Idempotent - not an error if doesn't exist
		return nil
	}

	delete(m.volumes, slot)
	return nil
}

// ResizeVolume implements RDSClient
func (m *MockClient) ResizeVolume(slot string, newSizeBytes int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for pending error
	if err := m.checkError(); err != nil {
		return err
	}
	m.mutationCounts["resize_volume"]++

	vol, exists := m.volumes[slot]
	if !exists {
		return &VolumeNotFoundError{Slot: slot}
	}

	vol.FileSizeBytes = newSizeBytes
	return nil
}

// GetVolume implements RDSClient
func (m *MockClient) GetVolume(slot string) (*VolumeInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for pending error
	if err := m.checkError(); err != nil {
		return nil, err
	}

	vol, exists := m.volumes[slot]
	if !exists {
		return nil, &VolumeNotFoundError{Slot: slot}
	}

	// Return a copy to prevent mutation
	copy := *vol
	return &copy, nil
}

// VerifyVolumeExists implements RDSClient
func (m *MockClient) VerifyVolumeExists(slot string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if _, exists := m.volumes[slot]; !exists {
		return &VolumeNotFoundError{Slot: slot}
	}
	return nil
}

// ListVolumes implements RDSClient
func (m *MockClient) ListVolumes() ([]VolumeInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]VolumeInfo, 0, len(m.volumes))
	for _, vol := range m.volumes {
		result = append(result, *vol)
	}
	return result, nil
}

// ListFiles implements RDSClient
func (m *MockClient) ListFiles(path string) ([]FileInfo, error) {
	return nil, nil
}

// DeleteFile implements RDSClient
func (m *MockClient) DeleteFile(path string) error {
	return nil
}

// GetCapacity implements RDSClient
func (m *MockClient) GetCapacity(basePath string) (*CapacityInfo, error) {
	return &CapacityInfo{
		TotalBytes: 1024 * 1024 * 1024 * 1024, // 1 TiB
		FreeBytes:  512 * 1024 * 1024 * 1024,  // 512 GiB
		UsedBytes:  512 * 1024 * 1024 * 1024,  // 512 GiB
	}, nil
}

// CreateSnapshot implements RDSClient
func (m *MockClient) CreateSnapshot(opts CreateSnapshotOptions) (*SnapshotInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for pending error
	if err := m.checkError(); err != nil {
		return nil, err
	}
	m.mutationCounts["create_snapshot"]++

	// Verify source volume exists
	if _, exists := m.volumes[opts.SourceVolume]; !exists {
		return nil, &VolumeNotFoundError{Slot: opts.SourceVolume}
	}

	// Check idempotency: if snapshot with same name already exists
	if existing, exists := m.snapshots[opts.Name]; exists {
		// If same source volume, return existing snapshot
		if existing.SourceVolume == opts.SourceVolume {
			copy := *existing
			return &copy, nil
		}
		// Different source volume - this is an error
		return nil, fmt.Errorf("snapshot %s already exists with different source volume (existing: %s, requested: %s)",
			opts.Name, existing.SourceVolume, opts.SourceVolume)
	}

	// Get source volume to copy size
	sourceVol := m.volumes[opts.SourceVolume]

	// Create snapshot
	filePath := ""
	if opts.BasePath != "" {
		filePath = opts.BasePath + "/" + opts.Name + ".img"
	}
	snapshot := &SnapshotInfo{
		Name:          opts.Name,
		SourceVolume:  opts.SourceVolume,
		FileSizeBytes: sourceVol.FileSizeBytes,
		CreatedAt:     time.Now(),
		FilePath:      filePath,
	}
	m.snapshots[opts.Name] = snapshot

	// Return copy to prevent mutation
	copy := *snapshot
	return &copy, nil
}

// DeleteSnapshot implements RDSClient
func (m *MockClient) DeleteSnapshot(snapshotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for pending error
	if err := m.checkError(); err != nil {
		return err
	}
	m.mutationCounts["delete_snapshot"]++

	// Idempotent - not an error if doesn't exist
	delete(m.snapshots, snapshotID)
	return nil
}

// GetSnapshot implements RDSClient
func (m *MockClient) GetSnapshot(snapshotID string) (*SnapshotInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for pending error
	if err := m.checkError(); err != nil {
		return nil, err
	}

	snapshot, exists := m.snapshots[snapshotID]
	if !exists {
		return nil, &SnapshotNotFoundError{Name: snapshotID}
	}

	// Return a copy to prevent mutation
	copy := *snapshot
	return &copy, nil
}

// ListSnapshots implements RDSClient
func (m *MockClient) ListSnapshots() ([]SnapshotInfo, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check for pending error
	if err := m.checkError(); err != nil {
		return nil, err
	}

	result := make([]SnapshotInfo, 0, len(m.snapshots))
	for _, snapshot := range m.snapshots {
		result = append(result, *snapshot)
	}
	return result, nil
}

// RestoreSnapshot implements RDSClient
func (m *MockClient) RestoreSnapshot(snapshotID string, newVolumeOpts CreateVolumeOptions) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for pending error
	if err := m.checkError(); err != nil {
		return err
	}
	m.mutationCounts["restore_snapshot"]++

	// Verify snapshot exists
	if _, exists := m.snapshots[snapshotID]; !exists {
		return &SnapshotNotFoundError{Name: snapshotID}
	}

	// Create new volume using provided options (same pattern as CreateVolume)
	if _, exists := m.volumes[newVolumeOpts.Slot]; exists {
		return fmt.Errorf("volume %s already exists", newVolumeOpts.Slot)
	}

	m.volumes[newVolumeOpts.Slot] = &VolumeInfo{
		Slot:          newVolumeOpts.Slot,
		Type:          "file",
		FilePath:      newVolumeOpts.FilePath,
		FileSizeBytes: newVolumeOpts.FileSizeBytes,
		Mount:         newVolumeOpts.Mount,
		NVMETCPExport: true,
		NVMETCPPort:   newVolumeOpts.NVMETCPPort,
		NVMETCPNQN:    newVolumeOpts.NVMETCPNQN,
		Status:        "ready",
	}
	return nil
}

// SetDiskMetrics sets the disk metrics response for testing
func (m *MockClient) SetDiskMetrics(metrics *DiskMetrics) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.diskMetrics = metrics
}

// GetDiskMetrics implements RDSClient
func (m *MockClient) GetDiskMetrics(slot string) (*DiskMetrics, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for pending error
	if err := m.checkError(); err != nil {
		return nil, err
	}

	if m.diskMetrics != nil {
		copy := *m.diskMetrics
		copy.Slot = slot
		return &copy, nil
	}

	// Return zero metrics by default (idle disk)
	return &DiskMetrics{Slot: slot}, nil
}

// SetHardwareHealth sets the hardware health metrics response for testing
func (m *MockClient) SetHardwareHealth(metrics *HardwareHealthMetrics) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hardwareHealth = metrics
}

// GetHardwareHealth implements RDSClient
func (m *MockClient) GetHardwareHealth(snmpHost string, snmpCommunity string) (*HardwareHealthMetrics, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check for pending error
	if err := m.checkError(); err != nil {
		return nil, err
	}

	if m.hardwareHealth != nil {
		copy := *m.hardwareHealth
		return &copy, nil
	}

	// Return reasonable defaults (healthy system)
	return &HardwareHealthMetrics{
		CPUTemperature:    40,
		BoardTemperature:  35,
		Fan1Speed:         7500,
		Fan2Speed:         7600,
		PSU1Power:         700,
		PSU2Power:         680,
		PSU1Temperature:   25,
		PSU2Temperature:   25,
		DiskPoolSizeBytes: 8_000_000_000_000, // 8TB
		DiskPoolUsedBytes: 1_600_000_000_000, // 1.6TB (20% used)
	}, nil
}
