package mock

import (
	"context"
	"fmt"
	"sync"
	"time"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/nvme"
	"git.srvlab.io/whiskey/nvmet-csi/pkg/observability"
)

// MockNVMEConnector is a mock implementation of nvme.Connector for testing
type MockNVMEConnector struct {
	mu sync.RWMutex

	// Connected volumes: NQN -> device path
	connected map[string]string

	// Device counter for generating unique device paths
	deviceCounter int

	// Error injection
	connectErr       error // Error to return on Connect operations
	disconnectErr    error // Error to return on Disconnect operations
	getDevicePathErr error // Error to return on GetDevicePath operations
	persistentErr    error // Error to return on ALL operations until cleared

	// Call tracking for verification
	connectCalls    []nvme.Target
	disconnectCalls []string

	// Mock configuration
	config   nvme.Config
	metrics  *nvme.Metrics
	resolver *nvme.DeviceResolver
}

// NewMockNVMEConnector creates a new mock NVMe connector for testing
func NewMockNVMEConnector() *MockNVMEConnector {
	return &MockNVMEConnector{
		connected:     make(map[string]string),
		deviceCounter: 0,
		config:        nvme.DefaultConfig(),
		metrics:       &nvme.Metrics{},
		resolver:      nil, // Explicitly nil - recovery/stale checking code handles this gracefully
	}
}

// SetConnectError sets an error to return on Connect operations (test helper)
func (m *MockNVMEConnector) SetConnectError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connectErr = err
}

// SetDisconnectError sets an error to return on Disconnect operations (test helper)
func (m *MockNVMEConnector) SetDisconnectError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnectErr = err
}

// SetGetDevicePathError sets an error to return on GetDevicePath operations (test helper)
func (m *MockNVMEConnector) SetGetDevicePathError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getDevicePathErr = err
}

// SetPersistentError sets an error to return on ALL operations until cleared (test helper)
func (m *MockNVMEConnector) SetPersistentError(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persistentErr = err
}

// ClearErrors clears all error injection (test helper)
func (m *MockNVMEConnector) ClearErrors() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connectErr = nil
	m.disconnectErr = nil
	m.getDevicePathErr = nil
	m.persistentErr = nil
}

// GetConnectCalls returns the history of Connect calls (test helper)
func (m *MockNVMEConnector) GetConnectCalls() []nvme.Target {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Return a copy to avoid race conditions
	calls := make([]nvme.Target, len(m.connectCalls))
	copy(calls, m.connectCalls)
	return calls
}

// GetDisconnectCalls returns the history of Disconnect calls (test helper)
func (m *MockNVMEConnector) GetDisconnectCalls() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	// Return a copy to avoid race conditions
	calls := make([]string, len(m.disconnectCalls))
	copy(calls, m.disconnectCalls)
	return calls
}

// IsConnectedNQN checks if a specific NQN is currently connected (test helper)
func (m *MockNVMEConnector) IsConnectedNQN(nqn string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.connected[nqn]
	return ok
}

// Reset clears all state for test isolation (test helper)
func (m *MockNVMEConnector) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = make(map[string]string)
	m.deviceCounter = 0
	m.connectCalls = nil
	m.disconnectCalls = nil
	m.ClearErrors()
}

// checkError checks for pending errors (internal helper)
func (m *MockNVMEConnector) checkError(specificErr error) error {
	if m.persistentErr != nil {
		return m.persistentErr
	}
	return specificErr
}

// Connect implements nvme.Connector
func (m *MockNVMEConnector) Connect(target nvme.Target) (string, error) {
	return m.ConnectWithContext(context.Background(), target)
}

// ConnectWithContext implements nvme.Connector
func (m *MockNVMEConnector) ConnectWithContext(ctx context.Context, target nvme.Target) (string, error) {
	return m.ConnectWithConfig(ctx, target, nvme.DefaultConnectionConfig())
}

// ConnectWithConfig implements nvme.Connector
func (m *MockNVMEConnector) ConnectWithConfig(ctx context.Context, target nvme.Target, config nvme.ConnectionConfig) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Track call
	m.connectCalls = append(m.connectCalls, target)

	// Check for errors
	if err := m.checkError(m.connectErr); err != nil {
		return "", err
	}

	// Check context cancellation
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	// Already connected?
	if devicePath, ok := m.connected[target.NQN]; ok {
		return devicePath, nil
	}

	// Generate fake device path
	devicePath := fmt.Sprintf("/dev/nvme%dn1", m.deviceCounter)
	m.deviceCounter++

	// Store connection
	m.connected[target.NQN] = devicePath

	return devicePath, nil
}

// ConnectWithRetry implements nvme.Connector
func (m *MockNVMEConnector) ConnectWithRetry(ctx context.Context, target nvme.Target, config nvme.ConnectionConfig) (string, error) {
	// For the mock, just call ConnectWithConfig without actual retry logic
	return m.ConnectWithConfig(ctx, target, config)
}

// Disconnect implements nvme.Connector
func (m *MockNVMEConnector) Disconnect(nqn string) error {
	return m.DisconnectWithContext(context.Background(), nqn)
}

// DisconnectWithContext implements nvme.Connector
func (m *MockNVMEConnector) DisconnectWithContext(ctx context.Context, nqn string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Track call
	m.disconnectCalls = append(m.disconnectCalls, nqn)

	// Check for errors
	if err := m.checkError(m.disconnectErr); err != nil {
		return err
	}

	// Check context cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Remove from connected map
	delete(m.connected, nqn)

	return nil
}

// IsConnected implements nvme.Connector
func (m *MockNVMEConnector) IsConnected(nqn string) (bool, error) {
	return m.IsConnectedWithContext(context.Background(), nqn)
}

// IsConnectedWithContext implements nvme.Connector
func (m *MockNVMEConnector) IsConnectedWithContext(ctx context.Context, nqn string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check for persistent errors
	if m.persistentErr != nil {
		return false, m.persistentErr
	}

	// Check context cancellation
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	default:
	}

	_, ok := m.connected[nqn]
	return ok, nil
}

// GetDevicePath implements nvme.Connector
func (m *MockNVMEConnector) GetDevicePath(nqn string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check for errors
	if err := m.checkError(m.getDevicePathErr); err != nil {
		return "", err
	}

	devicePath, ok := m.connected[nqn]
	if !ok {
		return "", fmt.Errorf("device not found for NQN %s", nqn)
	}

	return devicePath, nil
}

// WaitForDevice implements nvme.Connector
func (m *MockNVMEConnector) WaitForDevice(nqn string, timeout time.Duration) (string, error) {
	// For mock, just return immediately if connected
	return m.GetDevicePath(nqn)
}

// GetMetrics implements nvme.Connector
func (m *MockNVMEConnector) GetMetrics() *nvme.Metrics {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.metrics
}

// GetConfig implements nvme.Connector
func (m *MockNVMEConnector) GetConfig() nvme.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config
}

// GetResolver implements nvme.Connector
// Returns nil for test mode - recovery and stale checking code handles this gracefully
func (m *MockNVMEConnector) GetResolver() *nvme.DeviceResolver {
	return nil
}

// SetPromMetrics implements nvme.Connector
func (m *MockNVMEConnector) SetPromMetrics(metrics *observability.Metrics) {
	// No-op for mock
}

// Close implements nvme.Connector
func (m *MockNVMEConnector) Close() error {
	// No background goroutines in mock, so just return nil
	return nil
}
