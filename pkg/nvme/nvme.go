package nvme

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/observability"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
)

// Connector handles NVMe/TCP connections
type Connector interface {
	// Connect establishes connection to NVMe/TCP target
	Connect(target Target) (string, error)

	// ConnectWithContext establishes connection with context for timeout/cancellation
	ConnectWithContext(ctx context.Context, target Target) (string, error)

	// ConnectWithConfig establishes connection with custom connection config
	ConnectWithConfig(ctx context.Context, target Target, config ConnectionConfig) (string, error)

	// ConnectWithRetry connects with exponential backoff retry on transient failures
	ConnectWithRetry(ctx context.Context, target Target, config ConnectionConfig) (string, error)

	// Disconnect terminates connection to NVMe/TCP target
	Disconnect(nqn string) error

	// DisconnectWithContext terminates connection with context
	DisconnectWithContext(ctx context.Context, nqn string) error

	// IsConnected checks if NVMe target is connected
	IsConnected(nqn string) (bool, error)

	// IsConnectedWithContext checks connection status with context
	IsConnectedWithContext(ctx context.Context, nqn string) (bool, error)

	// GetDevicePath returns block device path for connected target
	GetDevicePath(nqn string) (string, error)

	// WaitForDevice waits for device to appear after connection
	WaitForDevice(nqn string, timeout time.Duration) (string, error)

	// GetMetrics returns operation metrics
	GetMetrics() *Metrics

	// GetConfig returns current configuration
	GetConfig() Config

	// GetResolver returns the device resolver for NQN to device path resolution
	GetResolver() *DeviceResolver

	// SetPromMetrics sets the Prometheus metrics instance for recording operations
	SetPromMetrics(metrics *observability.Metrics)

	// Close stops background goroutines and cleans up resources
	Close() error
}

// Target represents an NVMe/TCP connection target
type Target struct {
	// Transport type (always "tcp" for NVMe/TCP)
	Transport string

	// NQN (NVMe Qualified Name) of the subsystem
	NQN string

	// TargetAddress is the IP address of the NVMe/TCP target
	TargetAddress string

	// TargetPort is the port number of the NVMe/TCP target
	TargetPort int

	// HostNQN is the NQN of the host initiator (optional)
	HostNQN string
}

// Config holds configuration for NVMe operations
type Config struct {
	// ConnectTimeout is the timeout for nvme connect operations
	ConnectTimeout time.Duration

	// DisconnectTimeout is the timeout for nvme disconnect operations
	DisconnectTimeout time.Duration

	// ListTimeout is the timeout for nvme list operations
	ListTimeout time.Duration

	// DeviceWaitTimeout is the timeout for waiting for device to appear
	DeviceWaitTimeout time.Duration

	// CommandTimeout is the default timeout for nvme-cli commands
	CommandTimeout time.Duration

	// EnableHealthcheck enables monitoring for stuck operations
	EnableHealthcheck bool

	// HealthcheckInterval is how often to check for stuck operations
	HealthcheckInterval time.Duration

	// ManagedNQNPrefix is the safety filter for disconnect operations.
	ManagedNQNPrefix string
}

// DefaultConfig returns default configuration
func DefaultConfig() Config {
	return Config{
		ConnectTimeout:      30 * time.Second,
		DisconnectTimeout:   15 * time.Second,
		ListTimeout:         10 * time.Second,
		DeviceWaitTimeout:   30 * time.Second,
		CommandTimeout:      20 * time.Second,
		EnableHealthcheck:   true,
		HealthcheckInterval: 5 * time.Second,
		ManagedNQNPrefix:    utils.ManagedNQNPrefix,
	}
}

// Metrics tracks NVMe operation statistics
type Metrics struct {
	mu                      sync.RWMutex
	connectCount            int64
	disconnectCount         int64
	connectErrors           int64
	disconnectErrors        int64
	connectDurationTotal    time.Duration
	disconnectDurationTotal time.Duration
	timeoutCount            int64
	stuckOperations         int64
	activeOperations        int
}

// String returns human-readable metrics
func (m *Metrics) String() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	avgConnect := time.Duration(0)
	if m.connectCount > 0 {
		avgConnect = m.connectDurationTotal / time.Duration(m.connectCount)
	}

	avgDisconnect := time.Duration(0)
	if m.disconnectCount > 0 {
		avgDisconnect = m.disconnectDurationTotal / time.Duration(m.disconnectCount)
	}

	return fmt.Sprintf("Connects(total=%d, errors=%d, avg=%v) Disconnects(total=%d, errors=%d, avg=%v) Timeouts=%d Stuck=%d Active=%d",
		m.connectCount, m.connectErrors, avgConnect,
		m.disconnectCount, m.disconnectErrors, avgDisconnect,
		m.timeoutCount, m.stuckOperations, m.activeOperations)
}

// operationTracker tracks active operations for healthcheck
type operationTracker struct {
	nqn       string
	operation string
	startTime time.Time
}

// connector implements Connector interface using nvme-cli
type connector struct {
	execCommand       func(name string, args ...string) *exec.Cmd
	config            Config
	metrics           *Metrics
	promMetrics       *observability.Metrics // Prometheus metrics (optional)
	activeOperations  map[string]*operationTracker
	activeOpsMu       sync.Mutex
	healthcheckDone   chan struct{}
	healthcheckCancel context.CancelFunc
	resolver          *DeviceResolver // Caching resolver for device path lookups
}

// NewConnector creates a new NVMe connector with default configuration
func NewConnector() Connector {
	return NewConnectorWithConfig(DefaultConfig())
}

// Connect establishes NVMe/TCP connection and returns device path
func (c *connector) Connect(target Target) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.config.ConnectTimeout)
	defer cancel()
	return c.ConnectWithContext(ctx, target)
}

// DEPRECATED: Old implementation kept for reference
//
//nolint:unused // kept for reference during migration
func (c *connector) connectLegacy(target Target) (string, error) {
	klog.V(2).Infof("Connecting to NVMe/TCP target: %s at %s:%d",
		target.NQN, target.TargetAddress, target.TargetPort)

	// SECURITY: Validate NQN format before using in commands
	if err := utils.ValidateNQN(target.NQN); err != nil {
		return "", fmt.Errorf("invalid target NQN: %w", err)
	}

	// Validate host NQN if specified
	if target.HostNQN != "" {
		if err := utils.ValidateNQN(target.HostNQN); err != nil {
			return "", fmt.Errorf("invalid host NQN: %w", err)
		}
	}

	// Check if already connected
	connected, err := c.IsConnected(target.NQN)
	if err != nil {
		return "", fmt.Errorf("failed to check connection status: %w", err)
	}

	if connected {
		klog.V(2).Infof("Already connected to NQN: %s", target.NQN)
		return c.GetDevicePath(target.NQN)
	}

	// Build nvme connect command
	args := []string{
		"connect",
		"-t", target.Transport,
		"-a", target.TargetAddress,
		"-s", fmt.Sprintf("%d", target.TargetPort),
		"-n", target.NQN,
	}

	// Add host NQN if specified
	if target.HostNQN != "" {
		args = append(args, "-q", target.HostNQN)
	}

	// Execute nvme connect
	cmd := c.execCommand("nvme", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("nvme connect failed: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("nvme connect output: %s", string(output))

	// Wait for device to appear
	devicePath, err := c.WaitForDevice(target.NQN, 30*time.Second)
	if err != nil {
		// Cleanup: disconnect on failure
		_ = c.Disconnect(target.NQN)
		return "", fmt.Errorf("device did not appear: %w", err)
	}

	// Wait for device node to be accessible in /dev
	// Give udev time to create the device node and set permissions
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(devicePath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Final check that device is accessible
	if _, err := os.Stat(devicePath); err != nil {
		_ = c.Disconnect(target.NQN)
		return "", fmt.Errorf("device %s not accessible: %w", devicePath, err)
	}

	klog.V(2).Infof("Successfully connected to NVMe target, device: %s", devicePath)
	return devicePath, nil
}

// Disconnect terminates NVMe/TCP connection
func (c *connector) Disconnect(nqn string) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.config.DisconnectTimeout)
	defer cancel()
	return c.DisconnectWithContext(ctx, nqn)
}

// DEPRECATED: Old implementation kept for reference
//
//nolint:unused // kept for reference during migration
func (c *connector) disconnectLegacy(nqn string) error {
	klog.V(2).Infof("Disconnecting from NVMe target: %s", nqn)

	// SECURITY: Validate NQN format before using in commands
	if err := utils.ValidateNQN(nqn); err != nil {
		return fmt.Errorf("invalid NQN: %w", err)
	}

	// Check if connected
	connected, err := c.IsConnected(nqn)
	if err != nil {
		return fmt.Errorf("failed to check connection status: %w", err)
	}

	if !connected {
		klog.V(2).Infof("Not connected to NQN: %s, nothing to disconnect", nqn)
		return nil
	}

	// Execute nvme disconnect
	cmd := c.execCommand("nvme", "disconnect", "-n", nqn)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nvme disconnect failed: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("nvme disconnect output: %s", string(output))
	klog.V(2).Infof("Successfully disconnected from NVMe target: %s", nqn)
	return nil
}

// IsConnected checks if NVMe target is currently connected
func (c *connector) IsConnected(nqn string) (bool, error) {
	// SECURITY: Validate NQN format
	if err := utils.ValidateNQN(nqn); err != nil {
		return false, fmt.Errorf("invalid NQN: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.config.ListTimeout)
	defer cancel()
	return c.IsConnectedWithContext(ctx, nqn)
}

// DEPRECATED: Old implementation kept for reference
//
//nolint:unused // kept for reference during migration
func (c *connector) isConnectedLegacy(nqn string) (bool, error) {
	// List all NVMe subsystems
	cmd := c.execCommand("nvme", "list-subsys", "-o", "json")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// nvme list-subsys may fail if no devices, that's ok
		if strings.Contains(string(output), "No NVMe subsystems") {
			return false, nil
		}
		klog.V(4).Infof("nvme list-subsys failed (may be normal): %v, output: %s", err, string(output))
		return false, nil
	}

	// Simple string search for NQN
	// TODO: Parse JSON for more robust checking in future
	return strings.Contains(string(output), nqn), nil
}

// GetDevicePath returns the block device path for a connected NVMe target.
//
// Contract:
//   - Returns (devicePath, nil) when device is connected and found
//   - Returns ("", error) when device is NOT connected or lookup fails
//   - Never returns ("", nil) - empty path always comes with an error
//
// Callers should check error first, not empty string:
//
//	devicePath, err := conn.GetDevicePath(nqn)
//	if err != nil {
//	    // Device not connected or lookup failed
//	}
//
// The error will be from DeviceResolver.ResolveDevicePath which returns
// "device not found for NQN" when the NQN is not connected.
func (c *connector) GetDevicePath(nqn string) (string, error) {
	return c.resolver.ResolveDevicePath(nqn)
}

// DEPRECATED: getDevicePathLegacy is the old inline sysfs scanning implementation
// kept for reference during migration
//
//nolint:unused // kept for reference during migration
func (c *connector) getDevicePathLegacy(nqn string) (string, error) {
	// Scan /sys/class/nvme for controllers
	controllers, err := filepath.Glob("/sys/class/nvme/nvme*")
	if err != nil {
		return "", fmt.Errorf("failed to scan nvme devices: %w", err)
	}

	for _, controller := range controllers {
		// Read subsystem NQN
		nqnPath := filepath.Join(controller, "subsysnqn")
		data, err := os.ReadFile(nqnPath)
		if err != nil {
			klog.V(5).Infof("Failed to read %s: %v", nqnPath, err)
			continue
		}

		deviceNQN := strings.TrimSpace(string(data))
		if deviceNQN == nqn {
			// Found matching controller, now find the block device
			// For NVMe-oF, namespaces appear as subdirectories under the controller
			// with names like nvme2c1n2 (subsystem 2, controller 1, namespace 2)
			// The corresponding block device may be either:
			// - /dev/nvme2n2 (subsystem-based, preferred for multipath)
			// - /dev/nvme2c1n2 (controller-based)

			// First, look for namespace directories directly under the controller
			namespaces, err := filepath.Glob(filepath.Join(controller, "nvme*n*"))
			if err != nil {
				klog.V(5).Infof("Failed to scan namespaces under %s: %v", controller, err)
			}

			for _, ns := range namespaces {
				nsName := filepath.Base(ns)
				// Check if this namespace exists as a block device
				if _, err := os.Stat("/dev/" + nsName); err == nil {
					return "/dev/" + nsName, nil
				}

				// For controller-based paths (nvmeXcYnZ), also check subsystem-based path (nvmeXnZ)
				// Extract subsystem number and namespace number
				if strings.Contains(nsName, "c") {
					// Parse nvmeXcYnZ to get X and Z
					var subsys, ctrl, namespace int
					if _, err := fmt.Sscanf(nsName, "nvme%dc%dn%d", &subsys, &ctrl, &namespace); err == nil {
						subsysDevice := fmt.Sprintf("nvme%dn%d", subsys, namespace)
						if _, err := os.Stat("/dev/" + subsysDevice); err == nil {
							return "/dev/" + subsysDevice, nil
						}
					}
				}
			}

			// Fallback: try the old method (controller name + n*)
			controllerName := filepath.Base(controller)
			blockDevices, err := filepath.Glob("/sys/class/block/" + controllerName + "n*")
			if err != nil {
				return "", fmt.Errorf("failed to scan block devices: %w", err)
			}

			// Return first namespace found
			for _, blockDev := range blockDevices {
				deviceName := filepath.Base(blockDev)
				// Skip controller-specific paths like nvme1c1n1, prefer nvme1n1
				if !strings.Contains(deviceName, "c") {
					return "/dev/" + deviceName, nil
				}
			}

			// If no simple path found, use any available
			if len(blockDevices) > 0 {
				deviceName := filepath.Base(blockDevices[0])
				return "/dev/" + deviceName, nil
			}
		}
	}

	return "", fmt.Errorf("no device found for NQN: %s", nqn)
}

// WaitForDevice waits for block device to appear after connection
func (c *connector) WaitForDevice(nqn string, timeout time.Duration) (string, error) {
	klog.V(4).Infof("Waiting for device with NQN: %s (timeout: %v)", nqn, timeout)

	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			devicePath, err := c.GetDevicePath(nqn)
			if err == nil {
				klog.V(4).Infof("Device appeared: %s", devicePath)
				return devicePath, nil
			}

			if time.Now().After(deadline) {
				return "", fmt.Errorf("timeout waiting for device with NQN %s", nqn)
			}

		case <-time.After(timeout):
			return "", fmt.Errorf("timeout waiting for device with NQN %s", nqn)
		}
	}
}

// NewConnectorWithConfig creates a connector with custom configuration
func NewConnectorWithConfig(config Config) Connector {
	ctx, cancel := context.WithCancel(context.Background())

	c := &connector{
		execCommand:       exec.Command,
		config:            config,
		metrics:           &Metrics{},
		activeOperations:  make(map[string]*operationTracker),
		healthcheckDone:   make(chan struct{}),
		healthcheckCancel: cancel,
		resolver:          NewDeviceResolver(),
	}

	// Wire up connection check for orphan detection
	c.resolver.SetIsConnectedFn(func(nqn string) (bool, error) {
		return c.IsConnected(nqn)
	})

	// Start healthcheck if enabled
	if config.EnableHealthcheck {
		go c.runHealthcheck(ctx)
	}

	return c
}

// ConnectWithContext establishes NVMe/TCP connection with context
// Uses default connection configuration for backward compatibility
func (c *connector) ConnectWithContext(ctx context.Context, target Target) (string, error) {
	return c.ConnectWithConfig(ctx, target, DefaultConnectionConfig())
}

// ConnectWithConfig establishes NVMe/TCP connection with custom connection config
func (c *connector) ConnectWithConfig(ctx context.Context, target Target, config ConnectionConfig) (devicePath string, err error) {
	// SECURITY: Validate NQN format before using in commands
	if err := utils.ValidateNQN(target.NQN); err != nil {
		return "", fmt.Errorf("invalid target NQN: %w", err)
	}

	// Validate host NQN if specified
	if target.HostNQN != "" {
		if err := utils.ValidateNQN(target.HostNQN); err != nil {
			return "", fmt.Errorf("invalid host NQN: %w", err)
		}
	}

	// Apply timeout from config if no deadline set
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.config.ConnectTimeout)
		defer cancel()
	}

	// Track operation
	opID := c.trackOperation(target.NQN, "connect")
	defer c.untrackOperation(opID)

	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime)
		c.metrics.mu.Lock()
		c.metrics.connectCount++
		c.metrics.connectDurationTotal += duration
		c.metrics.mu.Unlock()
		// Record Prometheus metrics
		if c.promMetrics != nil {
			c.promMetrics.RecordNVMeConnect(err, duration)
		}
	}()

	// Check if already connected
	connected, err := c.IsConnectedWithContext(ctx, target.NQN)
	if err != nil {
		return "", fmt.Errorf("failed to check connection status: %w", err)
	}

	if connected {
		klog.V(2).Infof("Already connected to NQN: %s", target.NQN)

		// Check for orphaned subsystem using resolver
		orphaned, err := c.resolver.IsOrphanedSubsystem(target.NQN)
		if err != nil {
			klog.Warningf("Failed to check orphan status for %s: %v", target.NQN, err)
		}

		if orphaned {
			klog.Warningf("NQN %s is orphaned, forcing disconnect and reconnect", target.NQN)
			c.resolver.Invalidate(target.NQN)
			if err := c.DisconnectWithContext(ctx, target.NQN); err != nil {
				c.metrics.mu.Lock()
				c.metrics.connectErrors++
				c.metrics.mu.Unlock()
				return "", fmt.Errorf("failed to disconnect orphaned subsystem %s before reconnect: %w", target.NQN, err)
			}
			// Fall through to connect logic below
		} else {
			// Not orphaned - return device path via resolver
			return c.resolver.ResolveDevicePath(target.NQN)
		}
	}

	// Build nvme connect command with connection parameters
	args := BuildConnectArgs(target, config)

	// Execute with context
	// Use execCommand for test mocking if set, otherwise use exec.CommandContext
	var cmd *exec.Cmd
	if c.execCommand != nil {
		// For testing: use the mock execCommand (no context support)
		cmd = c.execCommand("nvme", args...)
	} else {
		cmd = exec.CommandContext(ctx, "nvme", args...)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		c.metrics.mu.Lock()
		c.metrics.connectErrors++
		c.metrics.mu.Unlock()

		if ctx.Err() != nil {
			c.metrics.mu.Lock()
			c.metrics.timeoutCount++
			c.metrics.mu.Unlock()
			return "", fmt.Errorf("nvme connect timed out: %w", ctx.Err())
		}
		return "", fmt.Errorf("nvme connect failed: %w, output: %s", err, string(output))
	}

	// Wait for device with context
	devicePath, err = c.waitForDeviceWithContext(ctx, target.NQN)
	if err != nil {
		c.metrics.mu.Lock()
		c.metrics.connectErrors++
		c.metrics.mu.Unlock()

		if cleanupErr := c.DisconnectWithContext(context.Background(), target.NQN); cleanupErr != nil {
			return "", fmt.Errorf("device did not appear for subsystem %s and cleanup disconnect failed: wait error: %w; cleanup error: %w", target.NQN, err, cleanupErr)
		}
		return "", fmt.Errorf("device did not appear: %w", err)
	}

	klog.V(2).Infof("Successfully connected to NVMe target, device: %s", devicePath)
	return devicePath, nil
}

// ConnectWithRetry connects with exponential backoff retry on transient failures
func (c *connector) ConnectWithRetry(ctx context.Context, target Target, config ConnectionConfig) (string, error) {
	var devicePath string
	var lastErr error

	backoff := utils.DefaultBackoffConfig()

	err := utils.RetryWithBackoff(ctx, backoff, func() error {
		path, connectErr := c.ConnectWithConfig(ctx, target, config)
		if connectErr != nil {
			lastErr = connectErr
			klog.V(2).Infof("Connection attempt failed for NQN %s: %v (will retry if transient)", target.NQN, connectErr)
			return connectErr
		}
		devicePath = path
		return nil
	})

	if err != nil {
		if lastErr != nil {
			return "", fmt.Errorf("connection failed after retries: %w", lastErr)
		}
		return "", err
	}

	return devicePath, nil
}

// DisconnectWithContext terminates NVMe/TCP connection with context
func (c *connector) DisconnectWithContext(ctx context.Context, nqn string) error {
	// SECURITY: Validate NQN format before using in commands
	if err := utils.ValidateNQN(nqn); err != nil {
		return fmt.Errorf("invalid NQN: %w", err)
	}

	managedNQNPrefix := c.config.ManagedNQNPrefix
	if managedNQNPrefix == "" {
		managedNQNPrefix = utils.ManagedNQNPrefix
	}
	if err := ValidateNQNPrefix(managedNQNPrefix); err != nil {
		return fmt.Errorf("invalid managed NQN prefix: %w", err)
	}

	// CRITICAL: Only disconnect CSI-managed volumes to prevent bricking nodes.
	if !strings.HasPrefix(nqn, managedNQNPrefix) {
		klog.Warningf("Refusing to disconnect non-CSI volume: %s (expected prefix %s)", nqn, managedNQNPrefix)
		return fmt.Errorf("refusing to disconnect non-CSI volume: %s (only volumes with prefix %s are managed by this driver)", nqn, managedNQNPrefix)
	}

	// Apply timeout from config if no deadline set
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.config.DisconnectTimeout)
		defer cancel()
	}

	// Track operation
	opID := c.trackOperation(nqn, "disconnect")
	defer c.untrackOperation(opID)

	startTime := time.Now()
	defer func() {
		duration := time.Since(startTime)
		c.metrics.mu.Lock()
		c.metrics.disconnectCount++
		c.metrics.disconnectDurationTotal += duration
		c.metrics.mu.Unlock()
	}()

	// Check if connected
	connected, err := c.IsConnectedWithContext(ctx, nqn)
	if err != nil {
		return fmt.Errorf("failed to check connection status: %w", err)
	}

	if !connected {
		klog.V(2).Infof("Not connected to NQN: %s, nothing to disconnect", nqn)
		return nil
	}

	// Execute with context
	// Use execCommand for test mocking if set, otherwise use exec.CommandContext
	var cmd *exec.Cmd
	if c.execCommand != nil {
		// For testing: use the mock execCommand (no context support)
		cmd = c.execCommand("nvme", "disconnect", "-n", nqn)
	} else {
		cmd = exec.CommandContext(ctx, "nvme", "disconnect", "-n", nqn)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		c.metrics.mu.Lock()
		c.metrics.disconnectErrors++
		c.metrics.mu.Unlock()

		if ctx.Err() != nil {
			c.metrics.mu.Lock()
			c.metrics.timeoutCount++
			c.metrics.mu.Unlock()
			return fmt.Errorf("nvme disconnect timed out: %w", ctx.Err())
		}
		return fmt.Errorf("nvme disconnect failed: %w, output: %s", err, string(output))
	}

	// Invalidate resolver cache after successful disconnect
	c.resolver.Invalidate(nqn)

	// Record Prometheus metrics for successful disconnect
	if c.promMetrics != nil {
		c.promMetrics.RecordNVMeDisconnect()
	}

	klog.V(2).Infof("Successfully disconnected from NVMe target: %s", nqn)
	return nil
}

// IsConnectedWithContext checks connection status with context
func (c *connector) IsConnectedWithContext(ctx context.Context, nqn string) (bool, error) {
	// SECURITY: Validate NQN format
	if err := utils.ValidateNQN(nqn); err != nil {
		return false, fmt.Errorf("invalid NQN: %w", err)
	}

	// Apply timeout from config if no deadline set
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.config.ListTimeout)
		defer cancel()
	}

	// Use execCommand for test mocking if set, otherwise use exec.CommandContext
	var cmd *exec.Cmd
	if c.execCommand != nil {
		// For testing: use the mock execCommand (no context support)
		cmd = c.execCommand("nvme", "list-subsys", "-o", "json")
	} else {
		cmd = exec.CommandContext(ctx, "nvme", "list-subsys", "-o", "json")
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(output), "No NVMe subsystems") {
			return false, nil
		}
		if ctx.Err() != nil {
			return false, nil // Timeout is not fatal for this check
		}
		klog.V(4).Infof("nvme list-subsys failed (may be normal): %v", err)
		return false, nil
	}

	return strings.Contains(string(output), nqn), nil
}

// GetMetrics returns current operation metrics
func (c *connector) GetMetrics() *Metrics {
	return c.metrics
}

// GetConfig returns current configuration
func (c *connector) GetConfig() Config {
	return c.config
}

// GetResolver returns the device resolver
func (c *connector) GetResolver() *DeviceResolver {
	return c.resolver
}

// SetPromMetrics sets the Prometheus metrics instance for recording operations
func (c *connector) SetPromMetrics(metrics *observability.Metrics) {
	c.promMetrics = metrics
}

// trackOperation records an active operation
func (c *connector) trackOperation(nqn, operation string) string {
	c.activeOpsMu.Lock()
	defer c.activeOpsMu.Unlock()

	opID := fmt.Sprintf("%s-%s-%d", operation, nqn, time.Now().UnixNano())
	c.activeOperations[opID] = &operationTracker{
		nqn:       nqn,
		operation: operation,
		startTime: time.Now(),
	}

	c.metrics.mu.Lock()
	c.metrics.activeOperations++
	c.metrics.mu.Unlock()

	return opID
}

// untrackOperation removes an operation from tracking
func (c *connector) untrackOperation(opID string) {
	c.activeOpsMu.Lock()
	defer c.activeOpsMu.Unlock()

	delete(c.activeOperations, opID)

	c.metrics.mu.Lock()
	c.metrics.activeOperations--
	c.metrics.mu.Unlock()
}

// runHealthcheck monitors for stuck operations
func (c *connector) runHealthcheck(ctx context.Context) {
	ticker := time.NewTicker(c.config.HealthcheckInterval)
	defer ticker.Stop()
	defer close(c.healthcheckDone)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.checkStuckOperations()
		}
	}
}

// checkStuckOperations identifies and logs stuck operations
func (c *connector) checkStuckOperations() {
	c.activeOpsMu.Lock()
	defer c.activeOpsMu.Unlock()

	now := time.Now()
	for _, op := range c.activeOperations {
		duration := now.Sub(op.startTime)

		// Warn if operation is taking longer than expected
		var threshold time.Duration
		switch op.operation {
		case "connect":
			threshold = c.config.ConnectTimeout * 2
		case "disconnect":
			threshold = c.config.DisconnectTimeout * 2
		default:
			threshold = c.config.CommandTimeout * 2
		}

		if duration > threshold {
			klog.Warningf("Stuck NVMe operation detected: %s on NQN %s (duration: %v)",
				op.operation, op.nqn, duration)

			c.metrics.mu.Lock()
			c.metrics.stuckOperations++
			c.metrics.mu.Unlock()
		}
	}
}

// Close stops background goroutines and cleans up resources
func (c *connector) Close() error {
	// Cancel healthcheck goroutine if running
	if c.healthcheckCancel != nil {
		c.healthcheckCancel()
		// Wait for healthcheck to finish (with timeout to avoid hanging)
		select {
		case <-c.healthcheckDone:
			// Healthcheck stopped cleanly
		case <-time.After(5 * time.Second):
			klog.Warning("Healthcheck goroutine did not stop within timeout")
		}
	}
	return nil
}

// waitForDeviceWithContext waits for device to appear with context support
func (c *connector) waitForDeviceWithContext(ctx context.Context, nqn string) (string, error) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timeout waiting for device with NQN %s: %w", nqn, ctx.Err())
		case <-ticker.C:
			devicePath, err := c.GetDevicePath(nqn)
			if err == nil {
				// Wait for device node to be accessible
				for i := 0; i < 10; i++ {
					if _, err := os.Stat(devicePath); err == nil {
						return devicePath, nil
					}
					select {
					case <-ctx.Done():
						return "", ctx.Err()
					case <-time.After(100 * time.Millisecond):
					}
				}
				return devicePath, nil
			}
		}
	}
}
