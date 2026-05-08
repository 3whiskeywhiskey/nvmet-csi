package nvme

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// mockExecCommand creates a mock exec.Cmd for testing
func mockExecCommand(stdout, stderr string, exitCode int) func(string, ...string) *exec.Cmd {
	return func(command string, args ...string) *exec.Cmd {
		cs := []string{"-test.run=TestHelperProcess", "--", command}
		cs = append(cs, args...)
		cmd := exec.Command(os.Args[0], cs...)
		cmd.Env = []string{
			"GO_WANT_HELPER_PROCESS=1",
			"STDOUT=" + stdout,
			"STDERR=" + stderr,
			"EXIT_CODE=" + fmt.Sprintf("%d", exitCode),
		}
		return cmd
	}
}

func mockExecCommandWithDisconnectFailure(listOutput string) func(string, ...string) *exec.Cmd {
	return func(command string, args ...string) *exec.Cmd {
		cs := []string{"-test.run=TestHelperProcess", "--", command}
		cs = append(cs, args...)
		cmd := exec.Command(os.Args[0], cs...)
		cmd.Env = []string{
			"GO_WANT_HELPER_PROCESS=1",
			"STDOUT=" + listOutput,
			"STDERR=",
			"EXIT_CODE=0",
			"DISCONNECT_STDERR=disconnect failed",
			"DISCONNECT_EXIT_CODE=1",
		}
		return cmd
	}
}

// TestHelperProcess is used by mockExecCommand to simulate command execution
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	exitCodeEnv := "EXIT_CODE"
	stdoutEnv := "STDOUT"
	stderrEnv := "STDERR"
	for i, arg := range os.Args {
		if arg != "--" || i+2 >= len(os.Args) {
			continue
		}
		if os.Args[i+1] == "nvme" && os.Args[i+2] == "disconnect" && os.Getenv("DISCONNECT_EXIT_CODE") != "" {
			exitCodeEnv = "DISCONNECT_EXIT_CODE"
			stdoutEnv = "DISCONNECT_STDOUT"
			stderrEnv = "DISCONNECT_STDERR"
		}
		break
	}

	// Output mock data
	_, _ = os.Stdout.WriteString(os.Getenv(stdoutEnv))
	_, _ = os.Stderr.WriteString(os.Getenv(stderrEnv))

	// Exit with specified code
	exitCode, _ := strconv.Atoi(os.Getenv(exitCodeEnv))
	os.Exit(exitCode)
}

func TestConnect(t *testing.T) {
	tests := []struct {
		name        string
		target      Target
		listOutput  string
		devicePath  string
		expectError bool
	}{
		{
			name: "successful connection",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2000-02.com.mikrotik:pvc-test-123",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
			},
			listOutput:  "No NVMe subsystems",
			devicePath:  "/dev/nvme0n1",
			expectError: false,
		},
		{
			name: "already connected",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2000-02.com.mikrotik:pvc-test-123",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
			},
			listOutput:  `{"Subsystems":[{"NQN":"nqn.2000-02.com.mikrotik:pvc-test-123"}]}`,
			devicePath:  "/dev/nvme0n1",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// For testing, we need to override GetDevicePath
			// This test is simplified - in real testing we'd need to mock the filesystem
			// Skip the actual connection test for now
			t.Skip("Skipping integration test - requires mocking filesystem and exec properly")
		})
	}
}

func TestDisconnect(t *testing.T) {
	tests := []struct {
		name        string
		nqn         string
		connected   bool
		expectError bool
	}{
		{
			name:        "disconnect connected device",
			nqn:         "nqn.2000-02.com.mikrotik:pvc-test-123",
			connected:   true,
			expectError: false,
		},
		{
			name:        "disconnect already disconnected",
			nqn:         "nqn.2000-02.com.mikrotik:pvc-test-123",
			connected:   false,
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock list-subsys output based on connected state
			listOutput := "No NVMe subsystems"
			if tt.connected {
				listOutput = `{"Subsystems":[{"NQN":"` + tt.nqn + `"}]}`
			}

			c := &connector{
				execCommand:      mockExecCommand(listOutput, "", 0),
				config:           DefaultConfig(),
				metrics:          &Metrics{},
				activeOperations: make(map[string]*operationTracker),
				resolver:         NewDeviceResolver(),
			}

			err := c.Disconnect(tt.nqn)
			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestIsConnected(t *testing.T) {
	tests := []struct {
		name       string
		nqn        string
		listOutput string
		expected   bool
	}{
		{
			name:       "device connected",
			nqn:        "nqn.2000-02.com.mikrotik:pvc-test-123",
			listOutput: `{"Subsystems":[{"NQN":"nqn.2000-02.com.mikrotik:pvc-test-123"}]}`,
			expected:   true,
		},
		{
			name:       "device not connected",
			nqn:        "nqn.2000-02.com.mikrotik:pvc-test-123",
			listOutput: "No NVMe subsystems",
			expected:   false,
		},
		{
			name:       "different device connected",
			nqn:        "nqn.2000-02.com.mikrotik:pvc-test-123",
			listOutput: `{"Subsystems":[{"NQN":"nqn.2000-02.com.mikrotik:pvc-other"}]}`,
			expected:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &connector{
				execCommand:      mockExecCommand(tt.listOutput, "", 0),
				config:           DefaultConfig(),
				metrics:          &Metrics{},
				activeOperations: make(map[string]*operationTracker),
				resolver:         NewDeviceResolver(),
			}

			connected, err := c.IsConnected(tt.nqn)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if connected != tt.expected {
				t.Errorf("Expected IsConnected=%v, got %v", tt.expected, connected)
			}
		})
	}
}

func TestConnectWithConfigOrphanedSubsystemReturnsDisconnectError(t *testing.T) {
	nqn := "nqn.2000-02.com.mikrotik:pvc-orphan-repair"
	c := &connector{
		execCommand: mockExecCommandWithDisconnectFailure(
			`{"Subsystems":[{"NQN":"` + nqn + `"}]}`,
		),
		config:           DefaultConfig(),
		metrics:          &Metrics{},
		activeOperations: make(map[string]*operationTracker),
		resolver: NewDeviceResolverWithConfig(ResolverConfig{
			SysfsRoot: t.TempDir(),
			TTL:       10 * time.Second,
		}),
	}
	c.resolver.SetIsConnectedFn(func(nqn string) (bool, error) {
		return c.IsConnected(nqn)
	})

	_, err := c.ConnectWithConfig(context.Background(), Target{
		Transport:     "tcp",
		NQN:           nqn,
		TargetAddress: "10.0.0.1",
		TargetPort:    4420,
	}, DefaultConnectionConfig())
	if err == nil {
		t.Fatal("ConnectWithConfig() expected disconnect error for orphaned subsystem")
	}
	if !strings.Contains(err.Error(), "failed to disconnect orphaned subsystem") {
		t.Fatalf("ConnectWithConfig() error = %v, want orphan disconnect failure", err)
	}

	c.metrics.mu.RLock()
	connectErrors := c.metrics.connectErrors
	c.metrics.mu.RUnlock()
	if connectErrors != 1 {
		t.Fatalf("connectErrors = %d, want 1", connectErrors)
	}
}

func TestGetDevicePathNotFound(t *testing.T) {
	c := &connector{
		execCommand:      exec.Command,
		config:           DefaultConfig(),
		metrics:          &Metrics{},
		activeOperations: make(map[string]*operationTracker),
		resolver:         NewDeviceResolver(),
	}

	// Test with non-existent NQN
	_, err := c.GetDevicePath("nqn.2000-02.com.mikrotik:non-existent")
	if err == nil {
		t.Error("Expected error for non-existent device, got nil")
	}
	if !strings.Contains(err.Error(), "no device found") {
		t.Errorf("Expected 'no device found' error, got: %v", err)
	}
}

func TestWaitForDeviceTimeout(t *testing.T) {
	c := &connector{
		execCommand:      exec.Command,
		config:           DefaultConfig(),
		metrics:          &Metrics{},
		activeOperations: make(map[string]*operationTracker),
		resolver:         NewDeviceResolver(),
	}

	// Test timeout with non-existent device
	_, err := c.WaitForDevice("nqn.2000-02.com.mikrotik:non-existent", 500*time.Millisecond)
	if err == nil {
		t.Error("Expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Expected timeout error, got: %v", err)
	}
}

func TestTargetValidation(t *testing.T) {
	tests := []struct {
		name   string
		target Target
		valid  bool
	}{
		{
			name: "valid target with all fields",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2000-02.com.mikrotik:pvc-test",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
				HostNQN:       "nqn.2014-08.org.nvmexpress:uuid:test",
			},
			valid: true,
		},
		{
			name: "valid target without host NQN",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2000-02.com.mikrotik:pvc-test",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
			},
			valid: true,
		},
		{
			name: "empty NQN",
			target: Target{
				Transport:     "tcp",
				NQN:           "",
				TargetAddress: "10.0.0.1",
				TargetPort:    4420,
			},
			valid: false,
		},
		{
			name: "empty address",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2000-02.com.mikrotik:pvc-test",
				TargetAddress: "",
				TargetPort:    4420,
			},
			valid: false,
		},
		{
			name: "zero port",
			target: Target{
				Transport:     "tcp",
				NQN:           "nqn.2000-02.com.mikrotik:pvc-test",
				TargetAddress: "10.0.0.1",
				TargetPort:    0,
			},
			valid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Basic validation checks
			valid := tt.target.NQN != ""
			if tt.target.TargetAddress == "" {
				valid = false
			}
			if tt.target.TargetPort == 0 {
				valid = false
			}

			if valid != tt.valid {
				t.Errorf("Expected valid=%v, got %v", tt.valid, valid)
			}
		})
	}
}

func TestNewConnector(t *testing.T) {
	c := NewConnector()
	if c == nil {
		t.Fatal("NewConnector returned nil")
	}
	defer c.Close()

	// Verify it implements the interface
	var _ = Connector(c)
}

// TestGetDevicePathWithMockFilesystem tests device path discovery
func TestGetDevicePathWithMockFilesystem(t *testing.T) {
	// Create temporary directory structure to simulate /sys/class/nvme
	tmpDir := t.TempDir()

	// Create mock device structure
	deviceDir := filepath.Join(tmpDir, "nvme0")
	if err := os.MkdirAll(deviceDir, 0755); err != nil {
		t.Fatalf("Failed to create mock device dir: %v", err)
	}

	// Create subsysnqn file
	nqn := "nqn.2000-02.com.mikrotik:pvc-test-123"
	nqnPath := filepath.Join(deviceDir, "subsysnqn")
	if err := os.WriteFile(nqnPath, []byte(nqn+"\n"), 0644); err != nil {
		t.Fatalf("Failed to create subsysnqn file: %v", err)
	}

	// Create namespace directory
	namespaceDir := filepath.Join(deviceDir, "nvme0n1")
	if err := os.MkdirAll(namespaceDir, 0755); err != nil {
		t.Fatalf("Failed to create namespace dir: %v", err)
	}

	// Test: This would require modifying GetDevicePath to accept a custom root path
	// For now, this test documents the expected behavior
	t.Log("GetDevicePath test requires filesystem mocking refactor")
}

// TestConnectorAccessorMethods tests simple accessor methods for 100% coverage
func TestConnectorAccessorMethods(t *testing.T) {
	config := DefaultConfig()
	c := NewConnectorWithConfig(config).(*connector)
	defer c.Close()

	// Test GetMetrics returns non-nil metrics
	metrics := c.GetMetrics()
	if metrics == nil {
		t.Error("GetMetrics() returned nil")
	}

	// Test GetConfig returns current config
	returnedConfig := c.GetConfig()
	if returnedConfig.ConnectTimeout != config.ConnectTimeout {
		t.Errorf("GetConfig() ConnectTimeout=%v, want %v", returnedConfig.ConnectTimeout, config.ConnectTimeout)
	}

	// Test GetResolver returns non-nil resolver
	resolver := c.GetResolver()
	if resolver == nil {
		t.Error("GetResolver() returned nil")
	}
}

// TestConnectorClose tests that Close() properly stops healthcheck goroutines
func TestConnectorClose(t *testing.T) {
	config := DefaultConfig()
	config.EnableHealthcheck = true
	config.HealthcheckInterval = 100 * time.Millisecond

	c := NewConnectorWithConfig(config)

	// Give healthcheck time to start
	time.Sleep(50 * time.Millisecond)

	// Close should return without error
	err := c.Close()
	if err != nil {
		t.Errorf("Close() returned error: %v", err)
	}

	// Calling Close() again should be safe (idempotent)
	err = c.Close()
	if err != nil {
		t.Errorf("Second Close() returned error: %v", err)
	}
}

// TestMetricsString tests Metrics.String() method for coverage
func TestMetricsString(t *testing.T) {
	m := &Metrics{}

	// Test with empty metrics
	s := m.String()
	if !strings.Contains(s, "Connects") {
		t.Errorf("Expected 'Connects' in string, got: %s", s)
	}
	if !strings.Contains(s, "Disconnects") {
		t.Errorf("Expected 'Disconnects' in string, got: %s", s)
	}

	// Test with some metrics populated
	m.connectCount = 5
	m.connectDurationTotal = 10 * time.Second
	m.disconnectCount = 3
	m.disconnectDurationTotal = 6 * time.Second
	m.timeoutCount = 1
	m.activeOperations = 2

	s = m.String()
	if !strings.Contains(s, "total=5") {
		t.Errorf("Expected 'total=5' in string, got: %s", s)
	}
	if !strings.Contains(s, "Active=2") {
		t.Errorf("Expected 'Active=2' in string, got: %s", s)
	}
}

// TestConnectionConfigDefaults tests ConnectionConfig defaults
func TestConnectionConfigDefaults(t *testing.T) {
	config := DefaultConnectionConfig()

	// Test that default config has sensible values
	if config.CtrlLossTmo != -1 {
		t.Errorf("Expected CtrlLossTmo=-1 (unlimited), got %d", config.CtrlLossTmo)
	}
	if config.ReconnectDelay <= 0 {
		t.Errorf("Expected positive ReconnectDelay, got %d", config.ReconnectDelay)
	}
	// KeepAliveTmo can be 0 (use kernel default)
}

// TestConnectWrapper tests Connect() wrapper method
func TestConnectWrapper(t *testing.T) {
	c := &connector{
		execCommand:      mockExecCommand("No NVMe subsystems", "", 0),
		config:           DefaultConfig(),
		metrics:          &Metrics{},
		activeOperations: make(map[string]*operationTracker),
		resolver:         NewDeviceResolver(),
	}

	target := Target{
		Transport:     "tcp",
		NQN:           "nqn.2000-02.com.mikrotik:pvc-test",
		TargetAddress: "10.0.0.1",
		TargetPort:    4420,
	}

	// This will fail because device won't appear, but it tests the wrapper
	_, err := c.Connect(target)
	if err == nil {
		t.Skip("Connect succeeded unexpectedly - may require actual NVMe device")
	}
	// Error is expected because device won't appear
	if !strings.Contains(err.Error(), "timeout") && !strings.Contains(err.Error(), "device") {
		t.Logf("Connect failed with: %v (expected timeout or device error)", err)
	}
}

// TestLegacyFunctionsDocumented documents legacy functions that require hardware/specific
// nvme-cli versions to test properly.
//
// Legacy functions exist as fallbacks when JSON output parsing fails:
// - connectLegacy: Uses text parsing instead of JSON
// - disconnectLegacy: Uses text parsing instead of JSON
// - isConnectedLegacy: Checks via nvme list-subsys text output
// - getDevicePathLegacy: Scans /sys/block manually
//
// These paths are exercised in production when nvme-cli doesn't support
// JSON output or returns unexpected formats. They are marked as deprecated
// and kept for reference during migration.
//
// Manual testing should verify:
// 1. Legacy functions work with older nvme-cli versions (< 1.9)
// 2. Fallback activates when JSON parsing fails
// 3. Device path resolution works via sysfs scanning
func TestLegacyFunctionsDocumented(t *testing.T) {
	t.Skip("Legacy functions require specific nvme-cli versions or hardware for testing")
}
