package nvme

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

const (
	// DeviceCheckTimeout is the maximum time to wait for lsof to respond.
	// If exceeded, assume device is unresponsive and proceed with disconnect.
	DeviceCheckTimeout = 5 * time.Second
)

// DeviceUsageResult contains the result of a device-in-use check.
type DeviceUsageResult struct {
	// InUse is true if processes have the device open
	InUse bool
	// Processes is a list of "command[PID]" strings holding the device open
	Processes []string
	// TimedOut is true if the check timed out (device may be unresponsive)
	TimedOut bool
	// Error is set if the check failed for reasons other than timeout
	Error error
	// FilteredSelfPIDs is the count of entries removed because they matched the driver's own PID
	FilteredSelfPIDs int
}

// parseLsofLine extracts process identity using the format established from
// the complete lsof output. BusyBox/Alpine puts PID first; traditional lsof
// puts COMMAND first and emits a header. Header and malformed lines are
// ignored.
func parseLsofLine(line string, pidFirst bool) (pid int, command string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, "", false
	}

	pidField, commandField := 0, 1
	if !pidFirst {
		pidField, commandField = 1, 0
	}

	pid, err := strconv.Atoi(fields[pidField])
	if err != nil {
		return 0, "", false
	}
	return pid, fields[commandField], true
}

// CheckDeviceInUse checks if a device has open file descriptors using lsof.
// Returns InUse=true if processes are holding the device open.
// Returns TimedOut=true if lsof didn't respond within DeviceCheckTimeout (device may be unresponsive).
// Uses context for cancellation.
func CheckDeviceInUse(ctx context.Context, devicePath string) DeviceUsageResult {
	// Create timeout context
	checkCtx, cancel := context.WithTimeout(ctx, DeviceCheckTimeout)
	defer cancel()

	// Run lsof with timeout
	cmd := exec.CommandContext(checkCtx, "lsof", devicePath)
	out, err := cmd.Output()

	// Check for timeout
	if checkCtx.Err() == context.DeadlineExceeded {
		klog.Warningf("Device busy check timed out for %s after %v (device may be unresponsive)",
			devicePath, DeviceCheckTimeout)
		return DeviceUsageResult{
			InUse:    false, // Treat as not busy - device unresponsive, proceed with disconnect
			TimedOut: true,
		}
	}

	if err != nil {
		// lsof returns exit code 1 if no processes using device
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			// No processes using device - this is the normal "not busy" case
			return DeviceUsageResult{InUse: false}
		}

		// Other error (lsof not found, permission denied, etc.)
		klog.Warningf("lsof command failed for %s: %v", devicePath, err)
		return DeviceUsageResult{
			InUse: false, // Can't determine - proceed with caution
			Error: fmt.Errorf("lsof failed: %w", err),
		}
	}

	// Get our own PID to filter out false positives from driver's temporary operations
	ownPID := os.Getpid()

	// Parse lsof output
	// Format: COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	// Log raw lsof output for debugging
	if klog.V(4).Enabled() {
		klog.Infof("CheckDeviceInUse: lsof output for %s (%d lines):\n%s", devicePath, len(lines), string(out))
	}

	// Extract process info from both traditional lsof output (which includes a
	// header) and BusyBox output (which is headerless and puts the PID first).
	// Filter out the driver's own PID to avoid false positives from:
	// - Temporary FDs during /proc scans in IsMountInUse()
	// - Sysfs reads during device path resolution
	// - The lsof check operation itself
	processes := make([]string, 0, len(lines))
	filteredCount := 0
	pidFirst := true
	if len(lines) > 0 {
		fields := strings.Fields(lines[0])
		if len(fields) >= 2 && strings.EqualFold(fields[0], "COMMAND") && strings.EqualFold(fields[1], "PID") {
			pidFirst = false
		}
	}

	for _, line := range lines {
		pid, command, ok := parseLsofLine(line, pidFirst)
		if !ok {
			continue
		}

		// Filter out driver's own PID.
		if pid == ownPID {
			filteredCount++
			klog.V(4).Infof("CheckDeviceInUse: filtered out driver's own PID %d from device-in-use check (command: %s)",
				pid, command)
			continue
		}

		// Format as "command[PID]" for readable error messages.
		processes = append(processes, fmt.Sprintf("%s[PID:%d]", command, pid))
	}

	if filteredCount > 0 {
		klog.V(2).Infof("CheckDeviceInUse: filtered %d driver self-reference(s) from lsof output for %s",
			filteredCount, devicePath)
	}

	if len(processes) > 0 {
		klog.V(2).Infof("Device %s in use by %d external process(es): %v", devicePath, len(processes), processes)
		return DeviceUsageResult{
			InUse:            true,
			Processes:        processes,
			FilteredSelfPIDs: filteredCount,
		}
	}

	klog.V(4).Infof("Device %s not in use (filtered %d self-references)", devicePath, filteredCount)
	return DeviceUsageResult{
		InUse:            false,
		FilteredSelfPIDs: filteredCount,
	}
}

// CheckDeviceInUseWithRetry checks if a device is in use, retrying multiple times
// to avoid transient false positives from momentary file descriptor operations.
// This is particularly useful for filtering out the driver's own temporary FD operations
// during /proc scans or sysfs reads.
func CheckDeviceInUseWithRetry(ctx context.Context, devicePath string, retries int, retryDelay time.Duration) DeviceUsageResult {
	if retries < 1 {
		retries = 1
	}

	var lastResult DeviceUsageResult
	for attempt := 1; attempt <= retries; attempt++ {
		lastResult = CheckDeviceInUse(ctx, devicePath)

		// If device is not in use, no need to retry
		if !lastResult.InUse {
			if attempt > 1 {
				klog.V(2).Infof("Device %s not in use after %d attempt(s)", devicePath, attempt)
			}
			return lastResult
		}

		// If timed out or errored, return immediately (no point retrying)
		if lastResult.TimedOut || lastResult.Error != nil {
			return lastResult
		}

		// Device reported as in use - retry if we have attempts left
		if attempt < retries {
			klog.V(4).Infof("Device %s reported in use (attempt %d/%d), retrying after %v to confirm. Processes: %v",
				devicePath, attempt, retries, retryDelay, lastResult.Processes)

			// Wait before retry (check context cancellation)
			select {
			case <-ctx.Done():
				// Context cancelled - return current result
				return lastResult
			case <-time.After(retryDelay):
				// Continue to next retry
			}
		}
	}

	// All retries exhausted, device consistently in use
	klog.V(2).Infof("Device %s confirmed in use after %d attempt(s). Final processes: %v",
		devicePath, retries, lastResult.Processes)
	return lastResult
}
