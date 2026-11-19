package mount

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestHelperProcess is used by mockExecCommand to simulate command execution
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	// Suppress any coverage warnings or other test infrastructure output
	// by reopening stderr to /dev/null if we're not explicitly writing to it
	if os.Getenv("STDERR") == "" {
		devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
		if err == nil {
			os.Stderr = devNull
			defer devNull.Close()
		}
	}

	// Output mock data
	_, _ = os.Stdout.WriteString(os.Getenv("STDOUT"))
	_, _ = os.Stderr.WriteString(os.Getenv("STDERR"))

	// Exit with specified code
	exitCode, _ := strconv.Atoi(os.Getenv("EXIT_CODE"))
	os.Exit(exitCode)
}

func TestMount(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		target      string
		fsType      string
		options     []string
		expectError bool
	}{
		{
			name:        "basic mount",
			source:      "/dev/nvme0n1",
			target:      "/mnt/test",
			fsType:      "ext4",
			options:     []string{},
			expectError: false,
		},
		{
			name:        "mount with options",
			source:      "/dev/nvme0n1",
			target:      "/mnt/test",
			fsType:      "ext4",
			options:     []string{"ro", "noatime"},
			expectError: false,
		},
		{
			name:        "bind mount",
			source:      "/mnt/staging",
			target:      "/mnt/target",
			fsType:      "",
			options:     []string{"bind"},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &mounter{
				execCommand: mockExecCommand("", "", 0),
			}

			// Create temporary target directory
			tmpTarget := t.TempDir()

			err := m.Mount(tt.source, tmpTarget, tt.fsType, tt.options)
			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestUnmount(t *testing.T) {
	tests := []struct {
		name         string
		target       string
		isMountPoint bool
		expectError  bool
	}{
		{
			name:         "unmount mounted path",
			target:       "/mnt/test",
			isMountPoint: true,
			expectError:  false,
		},
		{
			name:         "unmount not mounted path",
			target:       "/mnt/test",
			isMountPoint: false,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock findmnt output based on mount status
			findmntOutput := ""
			if tt.isMountPoint {
				findmntOutput = tt.target
			}

			m := &mounter{
				execCommand: mockExecCommand(findmntOutput, "", 0),
			}

			err := m.Unmount(tt.target)
			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestIsLikelyMountPoint(t *testing.T) {
	tests := []struct {
		name            string
		createPath      bool
		findmntOutput   string
		findmntExitCode int
		expectedResult  bool
		expectError     bool
	}{
		{
			name:            "is mount point",
			createPath:      true,
			findmntOutput:   "/mnt/test\n", // findmnt returns path with newline
			findmntExitCode: 0,
			expectedResult:  true,
			expectError:     false,
		},
		{
			name:            "not mount point",
			createPath:      true,
			findmntOutput:   "",
			findmntExitCode: 1,
			expectedResult:  false,
			expectError:     false,
		},
		{
			name:            "path does not exist",
			createPath:      false,
			findmntOutput:   "",
			findmntExitCode: 0,
			expectedResult:  false,
			expectError:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a temporary path for testing
			var testPath string
			if tt.createPath {
				testPath = t.TempDir()
			} else {
				testPath = "/nonexistent-path-for-testing"
			}

			m := &mounter{
				execCommand: mockExecCommand(tt.findmntOutput, "", tt.findmntExitCode),
			}

			result, err := m.IsLikelyMountPoint(testPath)
			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if result != tt.expectedResult {
				t.Errorf("Expected result %v, got %v", tt.expectedResult, result)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	tests := []struct {
		name        string
		device      string
		fsType      string
		isFormatted bool
		expectError bool
	}{
		{
			name:        "skip already formatted",
			device:      "/dev/nvme0n1",
			fsType:      "ext4",
			isFormatted: true,
			expectError: false,
		},
		{
			name:        "unsupported filesystem",
			device:      "/dev/nvme0n1",
			fsType:      "btrfs",
			isFormatted: false,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock blkid output based on formatted status
			blkidOutput := ""
			blkidExitCode := 0
			if tt.isFormatted {
				blkidOutput = tt.fsType
			} else {
				blkidExitCode = 2 // blkid returns 2 when no filesystem found
			}

			m := &mounter{
				execCommand: mockExecCommand(blkidOutput, "", blkidExitCode),
			}

			err := m.Format(tt.device, tt.fsType)
			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}

	// Note: Testing actual format operations with ext4/xfs requires more complex mocking
	// where different commands (blkid vs mkfs) return different exit codes.
	// These cases are covered by integration tests.
}

func TestIsFormatted(t *testing.T) {
	tests := []struct {
		name           string
		device         string
		blkidOutput    string
		blkidExitCode  int
		expectedResult bool
		expectError    bool
	}{
		{
			name:           "formatted ext4",
			device:         "/dev/nvme0n1",
			blkidOutput:    "ext4",
			blkidExitCode:  0,
			expectedResult: true,
			expectError:    false,
		},
		{
			name:           "formatted xfs",
			device:         "/dev/nvme0n1",
			blkidOutput:    "xfs",
			blkidExitCode:  0,
			expectedResult: true,
			expectError:    false,
		},
		{
			name:           "not formatted",
			device:         "/dev/nvme0n1",
			blkidOutput:    "",
			blkidExitCode:  2,
			expectedResult: false,
			expectError:    false,
		},
		{
			name:           "device error exit 1",
			device:         "/dev/nvme0n1",
			blkidOutput:    "",
			blkidExitCode:  1,
			expectedResult: false,
			expectError:    true,
		},
		{
			name:           "unknown exit code",
			device:         "/dev/nvme0n1",
			blkidOutput:    "",
			blkidExitCode:  3,
			expectedResult: false,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &mounter{
				execCommand: mockExecCommand(tt.blkidOutput, "", tt.blkidExitCode),
			}

			result, err := m.IsFormatted(tt.device)
			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if result != tt.expectedResult {
				t.Errorf("Expected result %v, got %v", tt.expectedResult, result)
			}
		})
	}
}

func TestGetDeviceStats(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		dfOutput      string
		expectError   bool
		expectedStats *DeviceStats
	}{
		{
			name:        "valid stats",
			path:        "/mnt/test",
			dfOutput:    "Size Used Avail Inodes IUsed IFree\n107374182400 21474836480 85899345920 6553600 131072 6422528",
			expectError: false,
			expectedStats: &DeviceStats{
				TotalBytes:      107374182400,
				UsedBytes:       21474836480,
				AvailableBytes:  85899345920,
				TotalInodes:     6553600,
				UsedInodes:      131072,
				AvailableInodes: 6422528,
			},
		},
		{
			name:          "invalid output",
			path:          "/mnt/test",
			dfOutput:      "invalid output",
			expectError:   true,
			expectedStats: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &mounter{
				execCommand: mockExecCommand(tt.dfOutput, "", 0),
			}

			stats, err := m.GetDeviceStats(tt.path)
			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if tt.expectedStats != nil && stats != nil {
				if stats.TotalBytes != tt.expectedStats.TotalBytes {
					t.Errorf("Expected TotalBytes %d, got %d", tt.expectedStats.TotalBytes, stats.TotalBytes)
				}
				if stats.UsedBytes != tt.expectedStats.UsedBytes {
					t.Errorf("Expected UsedBytes %d, got %d", tt.expectedStats.UsedBytes, stats.UsedBytes)
				}
				if stats.AvailableBytes != tt.expectedStats.AvailableBytes {
					t.Errorf("Expected AvailableBytes %d, got %d", tt.expectedStats.AvailableBytes, stats.AvailableBytes)
				}
				if stats.TotalInodes != tt.expectedStats.TotalInodes {
					t.Errorf("Expected TotalInodes %d, got %d", tt.expectedStats.TotalInodes, stats.TotalInodes)
				}
				if stats.UsedInodes != tt.expectedStats.UsedInodes {
					t.Errorf("Expected UsedInodes %d, got %d", tt.expectedStats.UsedInodes, stats.UsedInodes)
				}
				if stats.AvailableInodes != tt.expectedStats.AvailableInodes {
					t.Errorf("Expected AvailableInodes %d, got %d", tt.expectedStats.AvailableInodes, stats.AvailableInodes)
				}
			}
		})
	}
}

func TestNewMounter(t *testing.T) {
	m := NewMounter()
	if m == nil {
		t.Fatal("NewMounter returned nil")
	}

	// Verify it implements the interface
	var _ = Mounter(m)
}

func TestMountCreateTargetDirectory(t *testing.T) {
	m := &mounter{
		execCommand: mockExecCommand("", "", 0),
	}

	// Create a temporary directory for testing
	tmpDir := t.TempDir()
	target := tmpDir + "/subdir/deep/path"

	err := m.Mount("/dev/test", target, "ext4", nil)
	if err != nil {
		t.Errorf("Mount failed: %v", err)
	}

	// Verify directory was created
	if _, err := os.Stat(target); os.IsNotExist(err) {
		t.Error("Target directory was not created")
	}
}

func TestFormatUnsupportedFilesystem(t *testing.T) {
	m := &mounter{
		execCommand: mockExecCommand("", "", 2),
	}

	err := m.Format("/dev/test", "unsupported-fs")
	if err == nil {
		t.Error("Expected error for unsupported filesystem")
	}
	if !strings.Contains(err.Error(), "unsupported filesystem type") {
		t.Errorf("Expected 'unsupported filesystem type' error, got: %v", err)
	}
}

func TestValidateMountOptions(t *testing.T) {
	tests := []struct {
		name      string
		options   []string
		expectErr bool
	}{
		// Valid options
		{
			name:      "no options",
			options:   []string{},
			expectErr: false,
		},
		{
			name:      "safe options",
			options:   []string{"nosuid", "nodev", "noexec"},
			expectErr: false,
		},
		{
			name:      "read-only option",
			options:   []string{"ro"},
			expectErr: false,
		},
		{
			name:      "relatime option",
			options:   []string{"relatime"},
			expectErr: false,
		},
		{
			name:      "bind mount options",
			options:   []string{"bind", "ro"},
			expectErr: false,
		},
		// Dangerous options
		{
			name:      "suid not allowed",
			options:   []string{"suid"},
			expectErr: true,
		},
		{
			name:      "dev not allowed",
			options:   []string{"dev"},
			expectErr: true,
		},
		{
			name:      "exec not allowed",
			options:   []string{"exec"},
			expectErr: true,
		},
		{
			name:      "mixed with dangerous option",
			options:   []string{"ro", "suid", "nosuid"},
			expectErr: true,
		},
		// Non-whitelisted options
		{
			name:      "non-whitelisted option",
			options:   []string{"custom-option"},
			expectErr: true,
		},
		{
			name:      "acl not whitelisted",
			options:   []string{"acl"},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateMountOptions(tt.options)
			if (err != nil) != tt.expectErr {
				t.Errorf("ValidateMountOptions() error = %v, expectErr %v", err, tt.expectErr)
			}
		})
	}
}

func TestSanitizeMountOptions(t *testing.T) {
	tests := []struct {
		name        string
		options     []string
		isBindMount bool
		expectErr   bool
		expectOpts  []string
	}{
		{
			name:        "regular mount - no changes",
			options:     []string{"ro"},
			isBindMount: false,
			expectErr:   false,
			expectOpts:  []string{"ro"},
		},
		{
			name:        "bind mount - add secure defaults",
			options:     []string{"bind"},
			isBindMount: true,
			expectErr:   false,
			expectOpts:  []string{"nosuid", "nodev", "noexec", "bind"},
		},
		{
			name:        "bind mount - already has nosuid",
			options:     []string{"bind", "nosuid"},
			isBindMount: true,
			expectErr:   false,
			expectOpts:  []string{"nodev", "noexec", "bind", "nosuid"},
		},
		{
			name:        "bind mount - dangerous option rejected",
			options:     []string{"bind", "suid"},
			isBindMount: true,
			expectErr:   true,
			expectOpts:  nil,
		},
		{
			name:        "empty options on bind mount",
			options:     []string{},
			isBindMount: true,
			expectErr:   false,
			expectOpts:  []string{"nosuid", "nodev", "noexec"},
		},
		{
			name:        "bind mount with ro",
			options:     []string{"bind", "ro"},
			isBindMount: true,
			expectErr:   false,
			expectOpts:  []string{"nosuid", "nodev", "noexec", "bind", "ro"},
		},
		{
			name:        "bind mount allowed exec - no noexec, marker removed",
			options:     []string{"bind", AllowExecOption},
			isBindMount: true,
			expectErr:   false,
			expectOpts:  []string{"nosuid", "nodev", "bind"},
		},
		{
			name:        "regular mount - marker removed",
			options:     []string{AllowExecOption},
			isBindMount: false,
			expectErr:   false,
			expectOpts:  []string{},
		},
		{
			name:        "exec itself still rejected",
			options:     []string{"bind", "exec"},
			isBindMount: true,
			expectErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := SanitizeMountOptions(tt.options, tt.isBindMount)
			if (err != nil) != tt.expectErr {
				t.Errorf("SanitizeMountOptions() error = %v, expectErr %v", err, tt.expectErr)
				return
			}
			if !tt.expectErr {
				// Check that all expected options are present
				// Order doesn't matter, so convert to map
				resultMap := make(map[string]bool)
				for _, opt := range result {
					resultMap[opt] = true
				}
				for _, expected := range tt.expectOpts {
					if !resultMap[expected] {
						t.Errorf("SanitizeMountOptions() missing expected option: %s, got: %v", expected, result)
					}
				}
				// Check no extra options
				if len(result) != len(tt.expectOpts) {
					t.Errorf("SanitizeMountOptions() got %d options, expected %d: %v", len(result), len(tt.expectOpts), result)
				}
			}
		})
	}
}

func TestMountWithValidation(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		target    string
		fsType    string
		options   []string
		expectErr bool
		errString string
	}{
		{
			name:      "mount with dangerous suid option",
			source:    "/dev/nvme0n1",
			target:    "/mnt/test",
			fsType:    "ext4",
			options:   []string{"suid"},
			expectErr: true,
			errString: "dangerous mount option",
		},
		{
			name:      "mount with dangerous dev option",
			source:    "/dev/nvme0n1",
			target:    "/mnt/test",
			fsType:    "ext4",
			options:   []string{"dev"},
			expectErr: true,
			errString: "dangerous mount option",
		},
		{
			name:      "mount with non-whitelisted option",
			source:    "/dev/nvme0n1",
			target:    "/mnt/test",
			fsType:    "ext4",
			options:   []string{"custom-unsafe-option"},
			expectErr: true,
			errString: "not in whitelist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMounter()
			err := m.Mount(tt.source, tt.target, tt.fsType, tt.options)

			if tt.expectErr {
				if err == nil {
					t.Errorf("Mount() expected error but got nil")
				} else if !strings.Contains(err.Error(), tt.errString) {
					t.Errorf("Mount() error = %v, expected to contain %q", err, tt.errString)
				}
			}
		})
	}
}

func TestMakeFile(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(t *testing.T) string
		expectError bool
		validate    func(t *testing.T, path string)
	}{
		{
			name: "create new file",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				return tmpDir + "/testfile"
			},
			expectError: false,
			validate: func(t *testing.T, path string) {
				// Verify file exists and is regular file
				info, err := os.Stat(path)
				if err != nil {
					t.Errorf("File was not created: %v", err)
				}
				if !info.Mode().IsRegular() {
					t.Errorf("Path is not a regular file")
				}
			},
		},
		{
			name: "idempotent - file already exists",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				path := tmpDir + "/existing"
				// Create file first
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				f.Close()
				return path
			},
			expectError: false,
			validate: func(t *testing.T, path string) {
				// Verify file still exists
				if _, err := os.Stat(path); err != nil {
					t.Errorf("File should still exist: %v", err)
				}
			},
		},
		{
			name: "create with nested parent directories",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				return tmpDir + "/level1/level2/level3/testfile"
			},
			expectError: false,
			validate: func(t *testing.T, path string) {
				// Verify file exists
				info, err := os.Stat(path)
				if err != nil {
					t.Errorf("File was not created: %v", err)
				}
				if !info.Mode().IsRegular() {
					t.Errorf("Path is not a regular file")
				}
				// Verify parent directories were created
				parent := filepath.Dir(path)
				if _, err := os.Stat(parent); err != nil {
					t.Errorf("Parent directory was not created: %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMounter()
			path := tt.setup(t)

			err := m.MakeFile(path)

			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}

			if !tt.expectError && tt.validate != nil {
				tt.validate(t, path)
			}
		})
	}
}

// mockMultiExecCommand returns different results for sequential calls
// This is needed to mock complex operations like ForceUnmount that make multiple exec calls
func mockMultiExecCommand(results []struct {
	stdout, stderr string
	exitCode       int
}) func(string, ...string) *exec.Cmd {
	callCount := 0
	return func(command string, args ...string) *exec.Cmd {
		if callCount >= len(results) {
			// Repeat last result if we run out
			callCount = len(results) - 1
		}
		r := results[callCount]
		callCount++
		return mockExecCommand(r.stdout, r.stderr, r.exitCode)(command, args...)
	}
}

func TestIsMountInUse(t *testing.T) {
	// IsMountInUse requires /proc filesystem (Linux-specific)
	if runtime.GOOS != "linux" {
		t.Skipf("IsMountInUse requires Linux /proc filesystem, skipping on %s", runtime.GOOS)
	}

	tests := []struct {
		name          string
		path          string
		expectInUse   bool
		expectPIDsLen int
		expectError   bool
	}{
		{
			name:          "mount not in use - no processes",
			path:          t.TempDir(),
			expectInUse:   false,
			expectPIDsLen: 0,
			expectError:   false,
		},
		{
			name:          "nonexistent path returns not in use",
			path:          "/nonexistent-mount-path-12345",
			expectInUse:   false,
			expectPIDsLen: 0,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMounter()

			inUse, pids, err := m.IsMountInUse(tt.path)

			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if inUse != tt.expectInUse {
				t.Errorf("Expected inUse %v, got %v", tt.expectInUse, inUse)
			}
			if len(pids) != tt.expectPIDsLen {
				t.Errorf("Expected %d PIDs, got %d: %v", tt.expectPIDsLen, len(pids), pids)
			}
		})
	}
}

func TestForceUnmount(t *testing.T) {
	tests := []struct {
		name        string
		target      string
		setupTarget bool
		mockResults []struct {
			stdout, stderr string
			exitCode       int
		}
		expectError bool
		errContains string
	}{
		{
			name:        "normal unmount succeeds immediately",
			target:      "/mnt/test",
			setupTarget: true,
			mockResults: []struct {
				stdout, stderr string
				exitCode       int
			}{
				// findmnt check - is mounted
				{stdout: "/mnt/test\n", stderr: "", exitCode: 0},
				// umount succeeds
				{stdout: "", stderr: "", exitCode: 0},
			},
			expectError: false,
		},
		{
			name:        "target not mounted - succeeds idempotently",
			target:      "/mnt/test",
			setupTarget: true,
			mockResults: []struct {
				stdout, stderr string
				exitCode       int
			}{
				// findmnt check - not mounted
				{stdout: "", stderr: "", exitCode: 1},
			},
			expectError: false,
		},
		{
			name:        "unmount fails then lazy unmount succeeds",
			target:      "/mnt/test",
			setupTarget: true,
			mockResults: []struct {
				stdout, stderr string
				exitCode       int
			}{
				// First unmount attempt: findmnt check - is mounted
				{stdout: "/mnt/test\n", stderr: "", exitCode: 0},
				// umount fails (device busy)
				{stdout: "", stderr: "target is busy", exitCode: 1},
				// Poll check: findmnt - still mounted
				{stdout: "/mnt/test\n", stderr: "", exitCode: 0},
				// After timeout, no fuser output (not in use - note: IsMountInUse doesn't use fuser in real impl)
				// Lazy unmount succeeds
				{stdout: "", stderr: "", exitCode: 0},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var target string
			if tt.setupTarget {
				target = t.TempDir()
			} else {
				target = tt.target
			}

			m := &mounter{
				execCommand: mockMultiExecCommand(tt.mockResults),
			}

			err := m.ForceUnmount(target, 100*time.Millisecond)

			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if tt.expectError && err != nil && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error to contain %q, got: %v", tt.errContains, err)
				}
			}
		})
	}
}

func TestResizeFilesystem(t *testing.T) {
	tests := []struct {
		name        string
		device      string
		volumePath  string
		fsType      string // What blkid should return
		blkidExit   int
		resizeExit  int
		expectError bool
		errContains string
	}{
		{
			name:        "ext4 resize success",
			device:      "/dev/nvme0n1",
			volumePath:  "/mnt/volume",
			fsType:      "ext4",
			blkidExit:   0,
			resizeExit:  0,
			expectError: false,
		},
		{
			name:        "xfs resize success",
			device:      "/dev/nvme0n2",
			volumePath:  "/mnt/volume",
			fsType:      "xfs",
			blkidExit:   0,
			resizeExit:  0,
			expectError: false,
		},
		{
			name:        "ext4 resize fails",
			device:      "/dev/nvme0n1",
			volumePath:  "/mnt/volume",
			fsType:      "ext4",
			blkidExit:   0,
			resizeExit:  1,
			expectError: true,
			errContains: "resize failed",
		},
		{
			name:        "xfs resize fails",
			device:      "/dev/nvme0n2",
			volumePath:  "/mnt/volume",
			fsType:      "xfs",
			blkidExit:   0,
			resizeExit:  1,
			expectError: true,
			errContains: "resize failed",
		},
		{
			name:        "unsupported filesystem",
			device:      "/dev/nvme0n1",
			volumePath:  "/mnt/volume",
			fsType:      "ntfs",
			blkidExit:   0,
			resizeExit:  0,
			expectError: true,
			errContains: "unsupported filesystem type",
		},
		{
			name:        "blkid fails - device not found",
			device:      "/dev/nonexistent",
			volumePath:  "/mnt/volume",
			fsType:      "",
			blkidExit:   1,
			resizeExit:  0,
			expectError: true,
			errContains: "failed to detect filesystem type",
		},
		{
			name:        "blkid returns empty - no filesystem",
			device:      "/dev/nvme0n1",
			volumePath:  "/mnt/volume",
			fsType:      "",
			blkidExit:   0,
			resizeExit:  0,
			expectError: true,
			errContains: "could not detect filesystem type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create command-aware mock that returns different results based on command
			m := &mounter{
				execCommand: func(name string, args ...string) *exec.Cmd {
					switch name {
					case "blkid":
						return mockExecCommand(tt.fsType, "", tt.blkidExit)(name, args...)
					case "resize2fs", "xfs_growfs":
						return mockExecCommand("", "", tt.resizeExit)(name, args...)
					default:
						return mockExecCommand("", "", 0)(name, args...)
					}
				},
			}

			err := m.ResizeFilesystem(tt.device, tt.volumePath)

			if tt.expectError && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
			if tt.expectError && err != nil && tt.errContains != "" {
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("Expected error to contain %q, got: %v", tt.errContains, err)
				}
			}
		})
	}
}

// Benchmark mount option validation
func BenchmarkValidateMountOptions(b *testing.B) {
	options := []string{"nosuid", "nodev", "noexec", "ro"}
	for i := 0; i < b.N; i++ {
		_ = ValidateMountOptions(options)
	}
}

func BenchmarkSanitizeMountOptions(b *testing.B) {
	options := []string{"bind", "ro"}
	for i := 0; i < b.N; i++ {
		_, _ = SanitizeMountOptions(options, true)
	}
}

// TestMount_ErrorScenarios tests mount error path handling
func TestMount_ErrorScenarios(t *testing.T) {
	tests := []struct {
		name        string
		source      string
		target      string
		fsType      string
		options     []string
		setupTarget func(string) error
		expectError bool
		errContains string
	}{
		{
			name:   "mount target exists as file - not directory",
			source: "/dev/nvme0n1",
			fsType: "ext4",
			setupTarget: func(target string) error {
				// Create target as file instead of directory
				return os.WriteFile(target, []byte{}, 0600)
			},
			expectError: false, // Mount handles files for block volumes
		},
		{
			name:    "mount with read-only flag",
			source:  "/dev/nvme0n1",
			fsType:  "ext4",
			options: []string{"ro"},
			setupTarget: func(target string) error {
				return nil // Normal setup
			},
			expectError: false,
		},
		{
			name:    "dangerous mount option rejected",
			source:  "/dev/nvme0n1",
			fsType:  "ext4",
			options: []string{"suid"},
			setupTarget: func(target string) error {
				return nil
			},
			expectError: true,
			errContains: "dangerous",
		},
		{
			name:    "non-whitelisted mount option rejected",
			source:  "/dev/nvme0n1",
			fsType:  "ext4",
			options: []string{"custom-bad-option"},
			setupTarget: func(target string) error {
				return nil
			},
			expectError: true,
			errContains: "whitelist",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			target := filepath.Join(tmpDir, "target")

			// Setup target based on test scenario
			if tt.setupTarget != nil {
				if err := tt.setupTarget(target); err != nil {
					t.Fatalf("failed to setup target: %v", err)
				}
			}

			// Create mounter with mock command that succeeds
			m := &mounter{
				execCommand: mockExecCommand("", "", 0),
			}

			err := m.Mount(tt.source, target, tt.fsType, tt.options)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got nil")
				} else if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error to contain %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestFormat_ErrorScenarios tests format error path handling
func TestFormat_ErrorScenarios(t *testing.T) {
	tests := []struct {
		name           string
		device         string
		fsType         string
		isFormatted    bool
		formatExitCode int
		expectError    bool
		errContains    string
	}{
		{
			name:           "device not formatted - format ext4 success",
			device:         "/dev/nvme0n1",
			fsType:         "ext4",
			isFormatted:    false,
			formatExitCode: 0,
			expectError:    false,
		},
		{
			name:           "device not formatted - format xfs success",
			device:         "/dev/nvme0n1",
			fsType:         "xfs",
			isFormatted:    false,
			formatExitCode: 0,
			expectError:    false,
		},
		{
			name:           "device not formatted - format ext4 fails",
			device:         "/dev/nvme0n1",
			fsType:         "ext4",
			isFormatted:    false,
			formatExitCode: 1,
			expectError:    true,
			errContains:    "mkfs.ext4 failed",
		},
		{
			name:           "device not formatted - format xfs fails",
			device:         "/dev/nvme0n1",
			fsType:         "xfs",
			isFormatted:    false,
			formatExitCode: 1,
			expectError:    true,
			errContains:    "mkfs.xfs failed",
		},
		{
			name:        "device not formatted - unsupported filesystem",
			device:      "/dev/nvme0n1",
			fsType:      "ntfs",
			isFormatted: false,
			expectError: true,
			errContains: "unsupported filesystem type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create command-aware mock that returns different results based on command
			m := &mounter{
				execCommand: func(name string, args ...string) *exec.Cmd {
					switch name {
					case "blkid":
						if tt.isFormatted {
							return mockExecCommand(tt.fsType, "", 0)(name, args...)
						}
						return mockExecCommand("", "", 2)(name, args...) // Exit 2 = not formatted
					case "mkfs.ext4", "mkfs.ext3", "mkfs.xfs":
						return mockExecCommand("", "", tt.formatExitCode)(name, args...)
					default:
						return mockExecCommand("", "", 0)(name, args...)
					}
				},
			}

			err := m.Format(tt.device, tt.fsType)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got nil")
				} else if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("expected error to contain %q, got: %v", tt.errContains, err)
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		})
	}
}

// TestFormat_BlkidDeviceError tests that Format refuses to mkfs when blkid exits with status 1
func TestFormat_BlkidDeviceError(t *testing.T) {
	m := &mounter{
		execCommand: func(name string, args ...string) *exec.Cmd {
			switch name {
			case "blkid":
				// Simulate blkid exit 1 (device error)
				return mockExecCommand("", "", 1)(name, args...)
			default:
				// If this is called, Format did not respect the blkid error
				t.Errorf("mkfs should not be called when blkid exits with status 1")
				return mockExecCommand("", "", 0)(name, args...)
			}
		},
	}

	err := m.Format("/dev/nvme0n1", "ext4")
	if err == nil {
		t.Fatal("expected error when blkid exits with status 1, got nil")
	}
	if !strings.Contains(err.Error(), "blkid cannot read device") && !strings.Contains(err.Error(), "failed to check if device is formatted") {
		t.Errorf("expected error about blkid failure, got: %v", err)
	}
}
