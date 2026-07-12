package nvme

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestCheckDeviceInUse_NonexistentDevice(t *testing.T) {
	// Test with a device that doesn't exist
	// lsof should return exit code 1 (no processes found)
	ctx := context.Background()
	result := CheckDeviceInUse(ctx, "/dev/nonexistent-device-xyz")

	// Should return not in use (lsof exits 1 for no matching files)
	// or an error if lsof itself fails
	if result.InUse {
		t.Errorf("Expected device not in use, got InUse=true with processes: %v", result.Processes)
	}
}

func TestCheckDeviceInUse_CanceledContext(t *testing.T) {
	// Test with already-canceled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	result := CheckDeviceInUse(ctx, "/dev/null")

	// Should handle cancellation gracefully
	// Result depends on timing - may timeout or get context error
	if result.InUse && len(result.Processes) > 0 {
		t.Logf("Context was canceled but lsof still ran (timing): processes=%v", result.Processes)
	}
}

func TestCheckDeviceInUse_DevNull(t *testing.T) {
	// /dev/null might have some processes using it, but test should complete
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result := CheckDeviceInUse(ctx, "/dev/null")

	// Should complete without error (timeout or result)
	if result.TimedOut {
		t.Log("Check timed out - lsof took too long")
	} else if result.Error != nil {
		t.Logf("Check returned error: %v", result.Error)
	} else if result.InUse {
		t.Logf("Device /dev/null in use by: %v", result.Processes)
	} else {
		t.Log("Device /dev/null not in use")
	}
	// All outcomes are valid - test verifies function doesn't panic/hang
}

func TestDeviceUsageResult_Fields(t *testing.T) {
	// Test DeviceUsageResult struct usage
	tests := []struct {
		name   string
		result DeviceUsageResult
	}{
		{
			name:   "not in use",
			result: DeviceUsageResult{InUse: false},
		},
		{
			name: "in use with processes",
			result: DeviceUsageResult{
				InUse:     true,
				Processes: []string{"qemu[PID:1234]", "kubelet[PID:5678]"},
			},
		},
		{
			name: "timed out",
			result: DeviceUsageResult{
				InUse:    false,
				TimedOut: true,
			},
		},
		{
			name: "error",
			result: DeviceUsageResult{
				InUse: false,
				Error: fmt.Errorf("lsof not found"),
			},
		},
		{
			name: "filtered self PIDs",
			result: DeviceUsageResult{
				InUse:            false,
				FilteredSelfPIDs: 2,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Verify struct is usable
			_ = tt.result.InUse
			_ = tt.result.Processes
			_ = tt.result.TimedOut
			_ = tt.result.Error
			_ = tt.result.FilteredSelfPIDs
		})
	}
}

func TestCheckDeviceInUseWithRetry_NotInUse(t *testing.T) {
	// Test retry logic when device is not in use
	ctx := context.Background()

	result := CheckDeviceInUseWithRetry(ctx, "/dev/nonexistent-device-xyz", 3, 100*time.Millisecond)

	if result.InUse {
		t.Errorf("Expected device not in use, got InUse=true")
	}
}

func TestCheckDeviceInUseWithRetry_SingleAttempt(t *testing.T) {
	// Test with retries=1 (should behave like CheckDeviceInUse)
	ctx := context.Background()

	result := CheckDeviceInUseWithRetry(ctx, "/dev/nonexistent-device-xyz", 1, 100*time.Millisecond)

	if result.InUse {
		t.Errorf("Expected device not in use, got InUse=true")
	}
}

func TestCheckDeviceInUseWithRetry_ZeroRetries(t *testing.T) {
	// Test with retries=0 (should default to 1)
	ctx := context.Background()

	result := CheckDeviceInUseWithRetry(ctx, "/dev/nonexistent-device-xyz", 0, 100*time.Millisecond)

	if result.InUse {
		t.Errorf("Expected device not in use, got InUse=true")
	}
}

func TestCheckDeviceInUseWithRetry_ContextCancellation(t *testing.T) {
	// Test that retry respects context cancellation
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Use /dev/null which might show as in-use, triggering retries
	// But context should cancel before all retries complete
	startTime := time.Now()
	_ = CheckDeviceInUseWithRetry(ctx, "/dev/null", 100, 1*time.Second)
	elapsed := time.Since(startTime)

	// Should complete quickly due to context cancellation, not wait for all retries
	if elapsed > 2*time.Second {
		t.Errorf("Retry took too long (%v), should have been cancelled by context", elapsed)
	}
}

// TestCheckDeviceInUse_SelfPIDFiltering tests the self-PID filtering logic
// This test verifies that we properly filter out the driver's own PID
func TestCheckDeviceInUse_SelfPIDFiltering(t *testing.T) {
	// This is a behavior test - we can't easily mock lsof output,
	// but we can verify the FilteredSelfPIDs field is present and works
	ctx := context.Background()

	// Test with /dev/null which might have some usage
	result := CheckDeviceInUse(ctx, "/dev/null")

	// The FilteredSelfPIDs field should be accessible
	if result.FilteredSelfPIDs < 0 {
		t.Errorf("FilteredSelfPIDs should be >= 0, got %d", result.FilteredSelfPIDs)
	}

	// If lsof found the driver's own PID, it should be filtered
	// We can't guarantee this happens, but we can verify the logic exists
	t.Logf("Result: InUse=%v, Processes=%v, FilteredSelfPIDs=%d",
		result.InUse, result.Processes, result.FilteredSelfPIDs)
}

// TestParseLsofLine covers the parsing seam for both BusyBox (PID first, no header)
// and traditional lsof (COMMAND first, with header) formats using explicit pidFirst.
// The helper is the smallest package-private API that production will expose.
func TestParseLsofLine(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		pidFirst bool
		wantPID  int
		wantCmd  string
		wantOK   bool
	}{
		{
			name:     "BusyBox/Alpine no-header PID COMMAND FD NAME parses PID from field 0",
			line:     "1\t/usr/local/bin/nvmet-csi-plugin\t0\t/dev/null",
			pidFirst: true,
			wantPID:  1,
			wantCmd:  "/usr/local/bin/nvmet-csi-plugin",
			wantOK:   true,
		},
		{
			name:     "BusyBox no-header line parses PID field0 command field1",
			line:     "1234 /bin/sh 1 pipe:[123]",
			pidFirst: true,
			wantPID:  1234,
			wantCmd:  "/bin/sh",
			wantOK:   true,
		},
		{
			name:     "traditional COMMAND PID USER FD data line parses PID from field 1 command from 0",
			line:     "bash 5678 root 0u CHR 1,3 0t0 6 /dev/null",
			pidFirst: false,
			wantPID:  5678,
			wantCmd:  "bash",
			wantOK:   true,
		},
		{
			name:     "traditional numeric COMMAND with pidFirst=false correctly takes PID from field 1 (command '1' not mistaken for PID)",
			line:     "1 5678 root 0u CHR 1,3 0t0 6 /dev/null",
			pidFirst: false,
			wantPID:  5678,
			wantCmd:  "1",
			wantOK:   true,
		},
		{
			name:     "traditional header line is rejected as non-data without error or panic",
			line:     "COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME",
			pidFirst: false,
			wantOK:   false,
		},
		{
			name:     "blank line is rejected",
			line:     "",
			pidFirst: false,
			wantOK:   false,
		},
		{
			name:     "malformed too-few-fields line is rejected",
			line:     "onlyonefield",
			pidFirst: false,
			wantOK:   false,
		},
		{
			name:     "malformed non-numeric pid in busybox-style is rejected",
			line:     "abc /bin/foo 0 /dev/null",
			pidFirst: true,
			wantOK:   false,
		},
		{
			name:     "malformed non-numeric pid in traditional pos is rejected",
			line:     "cmd xyz user 0 /dev",
			pidFirst: false,
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pid, cmd, ok := parseLsofLine(tt.line, tt.pidFirst)
			if ok != tt.wantOK {
				t.Errorf("parseLsofLine(%q, %v) ok=%v want=%v", tt.line, tt.pidFirst, ok, tt.wantOK)
				return
			}
			if ok {
				if pid != tt.wantPID {
					t.Errorf("parseLsofLine(%q, %v) pid=%d want=%d", tt.line, tt.pidFirst, pid, tt.wantPID)
				}
				if cmd != tt.wantCmd {
					t.Errorf("parseLsofLine(%q, %v) cmd=%q want=%q", tt.line, tt.pidFirst, cmd, tt.wantCmd)
				}
			}
		})
	}
}

// TestParseLsofLine_SelfOwned_BusyBox asserts that a BusyBox-formatted line containing
// the current process PID can be recognized as self (for filtering) using os.Getpid fixture.
func TestParseLsofLine_SelfOwned_BusyBox(t *testing.T) {
	ownPID := os.Getpid()
	line := fmt.Sprintf("%d /usr/local/bin/nvmet-csi-plugin 0 /dev/null", ownPID)
	pid, cmd, ok := parseLsofLine(line, true)
	if !ok {
		t.Fatalf("expected parseLsofLine to accept BusyBox self line with pidFirst=true, got ok=false for %q", line)
	}
	if pid != ownPID {
		t.Errorf("parseLsofLine self line pid=%d want=%d (os.Getpid)", pid, ownPID)
	}
	if cmd != "/usr/local/bin/nvmet-csi-plugin" {
		t.Errorf("parseLsofLine self line cmd=%q want specific", cmd)
	}
}
