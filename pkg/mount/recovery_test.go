package mount

import (
	"context"
	"fmt"
	"testing"
	"time"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/nvme"
)

// mockMounter implements Mounter interface for testing
type mockMounter struct {
	forceUnmountErr    error
	forceUnmountCalls  int
	mountErr           error
	mountCalls         int
	isMountInUseResult bool
	isMountInUsePids   []int
	isMountInUseErr    error

	// Track calls for verification
	lastMountSource  string
	lastMountTarget  string
	lastMountFSType  string
	lastMountOptions []string
}

func (m *mockMounter) ForceUnmount(target string, timeout time.Duration) error {
	m.forceUnmountCalls++
	return m.forceUnmountErr
}

func (m *mockMounter) Mount(source, target, fsType string, options []string) error {
	m.mountCalls++
	m.lastMountSource = source
	m.lastMountTarget = target
	m.lastMountFSType = fsType
	m.lastMountOptions = options
	return m.mountErr
}

func (m *mockMounter) IsMountInUse(path string) (bool, []int, error) {
	return m.isMountInUseResult, m.isMountInUsePids, m.isMountInUseErr
}

// Unused interface methods (required for Mounter interface)
func (m *mockMounter) Unmount(target string) error {
	return nil
}

func (m *mockMounter) IsLikelyMountPoint(path string) (bool, error) {
	return false, nil
}

func (m *mockMounter) Format(device, fsType string) error {
	return nil
}

func (m *mockMounter) IsFormatted(device string) (bool, error) {
	return true, nil
}

func (m *mockMounter) ResizeFilesystem(device, volumePath string) error {
	return nil
}

func (m *mockMounter) GetDeviceStats(path string) (*DeviceStats, error) {
	return nil, nil
}

func (m *mockMounter) MakeFile(pathname string) error {
	return nil
}

// TestRecover_SucceedsFirstAttempt tests successful recovery on first try
func TestRecover_SucceedsFirstAttempt(t *testing.T) {
	nqn := "nqn.2000-02.com.mikrotik:pvc-test"
	newDevice := "/dev/nvme1n1"

	// Create mock resolver
	resolver := createMockResolver(t, nqn, newDevice, false)

	// Create mock mounter that succeeds
	mounter := &mockMounter{
		forceUnmountErr: nil,
		mountErr:        nil,
	}

	// Create mock checker
	checker := NewStaleMountChecker(resolver)

	// Create recoverer
	config := DefaultRecoveryConfig()
	config.MaxAttempts = 3
	config.InitialBackoff = 10 * time.Millisecond // Fast for testing
	recoverer := NewMountRecoverer(config, mounter, checker, resolver)

	// Perform recovery
	mountPath := "/var/lib/kubelet/pods/test"
	fsType := "ext4"
	options := []string{"rw"}

	ctx := context.Background()
	result, err := recoverer.Recover(ctx, mountPath, nqn, fsType, options)

	// Should succeed
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if !result.Recovered {
		t.Error("Expected Recovered to be true")
	}

	if result.Attempts != 1 {
		t.Errorf("Expected 1 attempt, got %d", result.Attempts)
	}

	if result.FinalError != nil {
		t.Errorf("Expected nil FinalError, got %v", result.FinalError)
	}

	// Verify mounter was called
	if mounter.forceUnmountCalls != 1 {
		t.Errorf("Expected 1 ForceUnmount call, got %d", mounter.forceUnmountCalls)
	}

	if mounter.mountCalls != 1 {
		t.Errorf("Expected 1 Mount call, got %d", mounter.mountCalls)
	}

	// Verify mount parameters
	if mounter.lastMountTarget != mountPath {
		t.Errorf("Expected mount target %s, got %s", mountPath, mounter.lastMountTarget)
	}

	if mounter.lastMountFSType != fsType {
		t.Errorf("Expected fsType %s, got %s", fsType, mounter.lastMountFSType)
	}
}

// TestRecover_SucceedsAfterRetry tests successful recovery after retries
func TestRecover_SucceedsAfterRetry(t *testing.T) {
	nqn := "nqn.2000-02.com.mikrotik:pvc-test"
	newDevice := "/dev/nvme1n1"

	resolver := createMockResolver(t, nqn, newDevice, false)

	// Create mock mounter that fails first mount, succeeds second
	mounter := &mockMounter{
		forceUnmountErr: nil,
	}

	checker := NewStaleMountChecker(resolver)

	config := DefaultRecoveryConfig()
	config.MaxAttempts = 3
	config.InitialBackoff = 10 * time.Millisecond // Fast for testing
	config.BackoffMultiplier = 2.0
	recoverer := NewMountRecoverer(config, mounter, checker, resolver)

	// Track mount calls to simulate failure then success
	callCount := 0
	mounter.mountErr = fmt.Errorf("mount failed")

	// We need to modify the mock to succeed on second attempt
	// This requires a different approach - use a closure
	originalMountCalls := 0
	recoverer.mounter = &mockMounterWithRetry{
		forceUnmountErr: nil,
		shouldFailUntil: 1, // Fail first attempt
		mountCalls:      &originalMountCalls,
	}

	mountPath := "/var/lib/kubelet/pods/test"
	fsType := "ext4"
	options := []string{"rw"}

	ctx := context.Background()
	startTime := time.Now()
	result, err := recoverer.Recover(ctx, mountPath, nqn, fsType, options)
	duration := time.Since(startTime)

	// Should succeed
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	if !result.Recovered {
		t.Error("Expected Recovered to be true")
	}

	if result.Attempts != 2 {
		t.Errorf("Expected 2 attempts, got %d", result.Attempts)
	}

	if result.FinalError != nil {
		t.Errorf("Expected nil FinalError, got %v", result.FinalError)
	}

	// Verify backoff was applied (at least one backoff period)
	minExpectedDuration := config.InitialBackoff
	if duration < minExpectedDuration {
		t.Errorf("Expected duration >= %v (for backoff), got %v", minExpectedDuration, duration)
	}

	// callCount is not accessible here, but we verified attempts
	_ = callCount
}

// mockMounterWithRetry allows simulating failures until a threshold
type mockMounterWithRetry struct {
	forceUnmountErr error
	shouldFailUntil int
	mountCalls      *int
}

func (m *mockMounterWithRetry) ForceUnmount(target string, timeout time.Duration) error {
	return m.forceUnmountErr
}

func (m *mockMounterWithRetry) Mount(source, target, fsType string, options []string) error {
	*m.mountCalls++
	if *m.mountCalls <= m.shouldFailUntil {
		return fmt.Errorf("mount failed (attempt %d)", *m.mountCalls)
	}
	return nil
}

func (m *mockMounterWithRetry) IsMountInUse(path string) (bool, []int, error) {
	return false, nil, nil
}

func (m *mockMounterWithRetry) Unmount(target string) error                      { return nil }
func (m *mockMounterWithRetry) IsLikelyMountPoint(path string) (bool, error)     { return false, nil }
func (m *mockMounterWithRetry) Format(device, fsType string) error               { return nil }
func (m *mockMounterWithRetry) IsFormatted(device string) (bool, error)          { return true, nil }
func (m *mockMounterWithRetry) ResizeFilesystem(device, volumePath string) error { return nil }
func (m *mockMounterWithRetry) GetDeviceStats(path string) (*DeviceStats, error) { return nil, nil }
func (m *mockMounterWithRetry) MakeFile(pathname string) error                   { return nil }

// TestRecover_FailsAllAttempts tests that recovery fails after max attempts
func TestRecover_FailsAllAttempts(t *testing.T) {
	nqn := "nqn.2000-02.com.mikrotik:pvc-test"
	newDevice := "/dev/nvme1n1"

	resolver := createMockResolver(t, nqn, newDevice, false)

	// Create mock mounter that always fails mount
	mounter := &mockMounter{
		forceUnmountErr: nil,
		mountErr:        fmt.Errorf("mount failed"),
	}

	checker := NewStaleMountChecker(resolver)

	config := DefaultRecoveryConfig()
	config.MaxAttempts = 3
	config.InitialBackoff = 10 * time.Millisecond // Fast for testing
	recoverer := NewMountRecoverer(config, mounter, checker, resolver)

	mountPath := "/var/lib/kubelet/pods/test"
	fsType := "ext4"
	options := []string{"rw"}

	ctx := context.Background()
	result, err := recoverer.Recover(ctx, mountPath, nqn, fsType, options)

	// Should fail
	if err == nil {
		t.Error("Expected error after all attempts failed")
	}

	if result.Recovered {
		t.Error("Expected Recovered to be false")
	}

	if result.Attempts != config.MaxAttempts {
		t.Errorf("Expected %d attempts, got %d", config.MaxAttempts, result.Attempts)
	}

	if result.FinalError == nil {
		t.Error("Expected FinalError to be set")
	}

	// Verify all attempts were made
	if mounter.forceUnmountCalls != config.MaxAttempts {
		t.Errorf("Expected %d ForceUnmount calls, got %d", config.MaxAttempts, mounter.forceUnmountCalls)
	}

	if mounter.mountCalls != config.MaxAttempts {
		t.Errorf("Expected %d Mount calls, got %d", config.MaxAttempts, mounter.mountCalls)
	}
}

// TestRecover_RefusesMountInUse tests that recovery refuses to unmount in-use mounts
func TestRecover_RefusesMountInUse(t *testing.T) {
	nqn := "nqn.2000-02.com.mikrotik:pvc-test"
	newDevice := "/dev/nvme1n1"

	resolver := createMockResolver(t, nqn, newDevice, false)

	// Create mock mounter that fails unmount and reports mount in use
	mounter := &mockMounter{
		forceUnmountErr:    fmt.Errorf("unmount failed"),
		isMountInUseResult: true,
		isMountInUsePids:   []int{1234, 5678},
	}

	checker := NewStaleMountChecker(resolver)

	config := DefaultRecoveryConfig()
	recoverer := NewMountRecoverer(config, mounter, checker, resolver)

	mountPath := "/var/lib/kubelet/pods/test"
	fsType := "ext4"
	options := []string{"rw"}

	ctx := context.Background()
	result, err := recoverer.Recover(ctx, mountPath, nqn, fsType, options)

	// Should fail immediately
	if err == nil {
		t.Error("Expected error when mount is in use")
	}

	if result.Recovered {
		t.Error("Expected Recovered to be false")
	}

	// Should only attempt once (not retry when in use)
	if result.Attempts != 1 {
		t.Errorf("Expected 1 attempt, got %d", result.Attempts)
	}

	// Error message should mention mount is in use
	if !contains(err.Error(), "in use") {
		t.Errorf("Expected error to mention 'in use', got: %v", err)
	}

	// Should not have attempted mount
	if mounter.mountCalls != 0 {
		t.Errorf("Expected 0 Mount calls when in use, got %d", mounter.mountCalls)
	}
}

// TestRecover_RespectsContext tests that recovery respects context cancellation
func TestRecover_RespectsContext(t *testing.T) {
	nqn := "nqn.2000-02.com.mikrotik:pvc-test"
	newDevice := "/dev/nvme1n1"

	resolver := createMockResolver(t, nqn, newDevice, false)

	// Create mock mounter that succeeds
	mounter := &mockMounter{
		forceUnmountErr: nil,
		mountErr:        nil,
	}

	checker := NewStaleMountChecker(resolver)

	config := DefaultRecoveryConfig()
	recoverer := NewMountRecoverer(config, mounter, checker, resolver)

	mountPath := "/var/lib/kubelet/pods/test"
	fsType := "ext4"
	options := []string{"rw"}

	// Create cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result, err := recoverer.Recover(ctx, mountPath, nqn, fsType, options)

	// Should return context error
	if err == nil {
		t.Error("Expected error when context is cancelled")
	}

	// Should contain context.Canceled
	if !contains(err.Error(), "cancel") {
		t.Errorf("Expected error to mention cancellation, got: %v", err)
	}

	if result.Recovered {
		t.Error("Expected Recovered to be false")
	}

	if result.FinalError == nil {
		t.Error("Expected FinalError to be set")
	}
}

// TestRecover_UnmountFailureRetries tests that unmount failures are retried
func TestRecover_UnmountFailureRetries(t *testing.T) {
	nqn := "nqn.2000-02.com.mikrotik:pvc-test"
	newDevice := "/dev/nvme1n1"

	resolver := createMockResolver(t, nqn, newDevice, false)

	// Create mock mounter that fails unmount
	mounter := &mockMounter{
		forceUnmountErr:    fmt.Errorf("unmount failed"),
		isMountInUseResult: false, // Not in use, just failed
	}

	checker := NewStaleMountChecker(resolver)

	config := DefaultRecoveryConfig()
	config.MaxAttempts = 2
	config.InitialBackoff = 10 * time.Millisecond
	recoverer := NewMountRecoverer(config, mounter, checker, resolver)

	mountPath := "/var/lib/kubelet/pods/test"
	fsType := "ext4"
	options := []string{"rw"}

	ctx := context.Background()
	result, err := recoverer.Recover(ctx, mountPath, nqn, fsType, options)

	// Should fail after retries
	if err == nil {
		t.Error("Expected error after unmount failures")
	}

	if result.Recovered {
		t.Error("Expected Recovered to be false")
	}

	// Should have attempted all retries
	if result.Attempts != config.MaxAttempts {
		t.Errorf("Expected %d attempts, got %d", config.MaxAttempts, result.Attempts)
	}

	// Should have called ForceUnmount multiple times
	if mounter.forceUnmountCalls != config.MaxAttempts {
		t.Errorf("Expected %d ForceUnmount calls, got %d", config.MaxAttempts, mounter.forceUnmountCalls)
	}

	// Should not have called Mount (unmount never succeeded)
	if mounter.mountCalls != 0 {
		t.Errorf("Expected 0 Mount calls, got %d", mounter.mountCalls)
	}
}

// TestNewMountRecoverer tests the constructor
func TestNewMountRecoverer(t *testing.T) {
	config := DefaultRecoveryConfig()
	mounter := &mockMounter{}
	resolver := nvme.NewDeviceResolver()
	checker := NewStaleMountChecker(resolver)

	recoverer := NewMountRecoverer(config, mounter, checker, resolver)

	if recoverer == nil {
		t.Fatal("Expected non-nil recoverer")
		return
	}

	if recoverer.config.MaxAttempts != config.MaxAttempts {
		t.Errorf("Expected MaxAttempts %d, got %d", config.MaxAttempts, recoverer.config.MaxAttempts)
	}

	if recoverer.mounter == nil {
		t.Error("Expected mounter to be set")
	}

	if recoverer.checker == nil {
		t.Error("Expected checker to be set")
	}

	if recoverer.resolver == nil {
		t.Error("Expected resolver to be set")
	}
}

// TestDefaultRecoveryConfig tests the default configuration
func TestDefaultRecoveryConfig(t *testing.T) {
	config := DefaultRecoveryConfig()

	if config.MaxAttempts != 3 {
		t.Errorf("Expected MaxAttempts 3, got %d", config.MaxAttempts)
	}

	if config.InitialBackoff != 1*time.Second {
		t.Errorf("Expected InitialBackoff 1s, got %v", config.InitialBackoff)
	}

	if config.BackoffMultiplier != 2.0 {
		t.Errorf("Expected BackoffMultiplier 2.0, got %f", config.BackoffMultiplier)
	}

	if config.NormalUnmountWait != 10*time.Second {
		t.Errorf("Expected NormalUnmountWait 10s, got %v", config.NormalUnmountWait)
	}
}
