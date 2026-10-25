package rds

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
)

// setupTestBasePaths configures allowed base paths for testing
func setupTestBasePaths(t *testing.T) {
	t.Helper()
	utils.ResetAllowedBasePaths()
	if err := utils.SetAllowedBasePath("/storage-pool/metal-csi"); err != nil {
		t.Fatalf("failed to set test base path: %v", err)
	}
	t.Cleanup(utils.ResetAllowedBasePaths)
}

// mockCommandRunner is a function type for mocking runCommand behavior
type mockCommandRunner func(command string) (string, error)

// testableSSHClient wraps sshClient for testing command execution
type testableSSHClient struct {
	*sshClient
	mockRunner mockCommandRunner
}

// Override runCommand to use mock
func (t *testableSSHClient) runCommand(command string) (string, error) {
	if t.mockRunner != nil {
		return t.mockRunner(command)
	}
	return "", fmt.Errorf("no mock runner configured")
}

// newTestableSSHClient creates a client for testing
func newTestableSSHClient(runner mockCommandRunner) *testableSSHClient {
	base := &sshClient{
		address: "test-rds",
		port:    22,
		user:    "admin",
	}
	return &testableSSHClient{
		sshClient:  base,
		mockRunner: runner,
	}
}

func TestParseVolumeInfo(t *testing.T) {
	// Real RouterOS output format (multi-line)
	output := `type=file slot="pvc-test-123" slot-default="" parent="" fs=-
               model="/storage-pool/test.img"
               size=53 687 091 200 mount-filesystem=yes mount-read-only=no
               compress=no sector-size=512 raid-master=none
               nvme-tcp-export=yes nvme-tcp-server-port=4420
               nvme-tcp-server-nqn="nqn.2000-02.com.mikrotik:pvc-test-123"
               nvme-tcp-server-allow-host-name="" iscsi-export=no
               nfs-sharing=no smb-sharing=no media-sharing=no
               media-interface=none swap=no
               file-path=/storage-pool/test.img
               file-size=50.0GiB file-offset=0`

	volume, err := parseVolumeInfo(output)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if volume.Slot != "pvc-test-123" {
		t.Errorf("Expected slot pvc-test-123, got %s", volume.Slot)
	}

	if volume.Type != "file" {
		t.Errorf("Expected type file, got %s", volume.Type)
	}

	if volume.FilePath != "/storage-pool/test.img" {
		t.Errorf("Expected path /storage-pool/test.img, got %s", volume.FilePath)
	}

	if !volume.Mount {
		t.Error("Expected mount-filesystem=yes to parse as Mount=true")
	}

	// file-size=50.0GiB = 50 * 1024^3 bytes = 53687091200
	expectedSize := int64(50 * 1024 * 1024 * 1024)
	if volume.FileSizeBytes != expectedSize {
		t.Errorf("Expected size %d, got %d", expectedSize, volume.FileSizeBytes)
	}

	if !volume.NVMETCPExport {
		t.Error("Expected NVMETCPExport to be true")
	}

	if volume.NVMETCPPort != 4420 {
		t.Errorf("Expected port 4420, got %d", volume.NVMETCPPort)
	}

	if volume.NVMETCPNQN != "nqn.2000-02.com.mikrotik:pvc-test-123" {
		t.Errorf("Expected NQN nqn.2000-02.com.mikrotik:pvc-test-123, got %s", volume.NVMETCPNQN)
	}

	// Status should be "ready" for file-type volumes with nvme-tcp-export=yes
	if volume.Status != "ready" {
		t.Errorf("Expected status ready, got %s", volume.Status)
	}
}

func TestRouterOSMountParam(t *testing.T) {
	if got := routerOSMountParam(false); got != "mount=no" {
		t.Fatalf("routerOSMountParam(false) = %q, want mount=no", got)
	}
	if got := routerOSMountParam(true); got != "mount=yes" {
		t.Fatalf("routerOSMountParam(true) = %q, want mount=yes", got)
	}
}

func TestParseVolumeList(t *testing.T) {
	// Real RouterOS /disk print output with multiple volumes (multi-line format)
	output := ` 0  type=file slot="pvc-test-1" size=53 687 091 200
               file-path=/storage-pool/test1.img file-size=50.0GiB
               nvme-tcp-export=yes nvme-tcp-server-port=4420
               nvme-tcp-server-nqn="nqn.2000-02.com.mikrotik:pvc-test-1"

 1  type=file slot="pvc-test-2" size=107 374 182 400
               file-path=/storage-pool/test2.img file-size=100.0GiB
               nvme-tcp-export=yes nvme-tcp-server-port=4420
               nvme-tcp-server-nqn="nqn.2000-02.com.mikrotik:pvc-test-2"`

	volumes, err := parseVolumeList(output)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if len(volumes) != 2 {
		t.Errorf("Expected 2 volumes, got %d", len(volumes))
	}

	if len(volumes) > 0 && volumes[0].Slot != "pvc-test-1" {
		t.Errorf("Expected first volume slot pvc-test-1, got %s", volumes[0].Slot)
	}

	if len(volumes) > 1 && volumes[1].Slot != "pvc-test-2" {
		t.Errorf("Expected second volume slot pvc-test-2, got %s", volumes[1].Slot)
	}

	// Verify first volume details
	if len(volumes) > 0 {
		expectedSize := int64(50 * 1024 * 1024 * 1024) // 50 GiB
		if volumes[0].FileSizeBytes != expectedSize {
			t.Errorf("Expected first volume size %d, got %d", expectedSize, volumes[0].FileSizeBytes)
		}
		if !volumes[0].NVMETCPExport {
			t.Error("Expected first volume NVMETCPExport to be true")
		}
	}
}

func TestParseCapacityInfo(t *testing.T) {
	// Real RouterOS /file print detail output format with space-separated numbers
	output := `name=/storage-pool type=directory size=7 681 574 174 720
               free=5 632 440 000 000 use=27%
               creation-time=jan/01/2025 00:00:00`

	capacity, err := parseCapacityInfo(output)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	// Expected values from space-separated numbers
	expectedTotal := int64(7681574174720) // size=7 681 574 174 720
	if capacity.TotalBytes != expectedTotal {
		t.Errorf("Expected total bytes %d, got %d", expectedTotal, capacity.TotalBytes)
	}

	expectedFree := int64(5632440000000) // free=5 632 440 000 000
	if capacity.FreeBytes != expectedFree {
		t.Errorf("Expected free bytes %d, got %d", expectedFree, capacity.FreeBytes)
	}

	// Used should be calculated as Total - Free
	expectedUsed := expectedTotal - expectedFree
	if capacity.UsedBytes != expectedUsed {
		t.Errorf("Expected used bytes %d, got %d", expectedUsed, capacity.UsedBytes)
	}
}

func TestValidateSlotName(t *testing.T) {
	tests := []struct {
		name      string
		slot      string
		expectErr bool
	}{
		{"valid alphanumeric", "pvc-abc123", false},
		{"valid with hyphens", "pvc-a1b2-c3d4", false},
		{"valid storage pool slot", "storage-pool", false},
		{"valid numeric disk id", "0", false},
		{"empty slot", "", true},
		{"with space", "pvc abc", true},
		{"with semicolon", "pvc-abc; rm -rf", true},
		{"with pipe", "pvc|evil", true},
		{"with dollar", "pvc$var", true},
		{"with backtick", "pvc`cmd`", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSlotName(tt.slot)
			if tt.expectErr && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    int64
		expected string
	}{
		{1024, "1K"},
		{1024 * 1024, "1M"},
		{1024 * 1024 * 1024, "1G"},
		{50 * 1024 * 1024 * 1024, "50G"},
		{1024 * 1024 * 1024 * 1024, "1T"},
		{512, "512"},
	}

	for _, tt := range tests {
		result := formatBytes(tt.bytes)
		if result != tt.expected {
			t.Errorf("formatBytes(%d) = %s, expected %s", tt.bytes, result, tt.expected)
		}
	}
}

func TestParseSize(t *testing.T) {
	tib := int64(1024 * 1024 * 1024 * 1024)
	tests := []struct {
		value    string
		unit     string
		expected int64
	}{
		{"50", "G", 50 * 1024 * 1024 * 1024},
		{"100", "GB", 100 * 1024 * 1024 * 1024},
		{"7.23", "TiB", int64(7.23 * float64(tib))},
		{"1024", "M", 1024 * 1024 * 1024},
		{"1", "K", 1024},
	}

	for _, tt := range tests {
		result, err := parseSize(tt.value, tt.unit)
		if err != nil {
			t.Errorf("parseSize(%s, %s) returned error: %v", tt.value, tt.unit, err)
			continue
		}

		// Allow small margin for floating point
		diff := result - tt.expected
		if diff < 0 {
			diff = -diff
		}
		if diff > 1024*1024 { // 1 MB tolerance
			t.Errorf("parseSize(%s, %s) = %d, expected %d", tt.value, tt.unit, result, tt.expected)
		}
	}
}

func TestParseFileInfo(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		expectedPath string
		expectedName string
		expectError  bool
	}{
		{
			name: "file path with leading slash",
			output: `name=/storage-pool/metal-csi/test.img type=file
                    size=53 687 091 200`,
			expectedPath: "/storage-pool/metal-csi/test.img",
			expectedName: "test.img",
			expectError:  false,
		},
		{
			name: "file path without leading slash (real RouterOS /file print format)",
			output: `name=storage-pool/metal-csi/test.img type=file
                    size=53 687 091 200`,
			expectedPath: "/storage-pool/metal-csi/test.img", // Should be normalized to absolute path
			expectedName: "test.img",
			expectError:  false,
		},
		{
			name: "file path with quoted name without leading slash",
			output: `name="storage-pool/metal-csi/test-volume.img" type=file
                    size=107 374 182 400`,
			expectedPath: "/storage-pool/metal-csi/test-volume.img", // Should be normalized
			expectedName: "test-volume.img",
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := parseFileInfo(tt.output)
			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if file.Path != tt.expectedPath {
				t.Errorf("Expected path %s, got %s", tt.expectedPath, file.Path)
			}

			if file.Name != tt.expectedName {
				t.Errorf("Expected name %s, got %s", tt.expectedName, file.Name)
			}

			// Verify path always has leading slash (critical for path comparison)
			if file.Path != "" && file.Path[0] != '/' {
				t.Errorf("Path should be normalized to absolute format with leading /, got: %s", file.Path)
			}
		})
	}
}

func TestValidateCreateVolumeOptions(t *testing.T) {
	setupTestBasePaths(t)
	tests := []struct {
		name      string
		opts      CreateVolumeOptions
		expectErr bool
	}{
		{
			name: "valid options",
			opts: CreateVolumeOptions{
				Slot:          "pvc-test-123",
				FilePath:      "/storage-pool/metal-csi/volumes/test.img",
				FileSizeBytes: 50 * 1024 * 1024 * 1024,
				NVMETCPPort:   4420,
				NVMETCPNQN:    "nqn.2000-02.com.mikrotik:pvc-test-123",
			},
			expectErr: false,
		},
		{
			name: "missing slot",
			opts: CreateVolumeOptions{
				FilePath:      "/storage-pool/metal-csi/volumes/test.img",
				FileSizeBytes: 50 * 1024 * 1024 * 1024,
				NVMETCPNQN:    "nqn.2000-02.com.mikrotik:pvc-test-123",
			},
			expectErr: true,
		},
		{
			name: "missing file path",
			opts: CreateVolumeOptions{
				Slot:          "pvc-test-123",
				FileSizeBytes: 50 * 1024 * 1024 * 1024,
				NVMETCPNQN:    "nqn.2000-02.com.mikrotik:pvc-test-123",
			},
			expectErr: true,
		},
		{
			name: "zero size",
			opts: CreateVolumeOptions{
				Slot:       "pvc-test-123",
				FilePath:   "/storage-pool/metal-csi/volumes/test.img",
				NVMETCPNQN: "nqn.2000-02.com.mikrotik:pvc-test-123",
			},
			expectErr: true,
		},
		{
			name: "missing NQN",
			opts: CreateVolumeOptions{
				Slot:          "pvc-test-123",
				FilePath:      "/storage-pool/metal-csi/volumes/test.img",
				FileSizeBytes: 50 * 1024 * 1024 * 1024,
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCreateVolumeOptions(tt.opts)
			if tt.expectErr && err == nil {
				t.Error("Expected error but got nil")
			}
			if !tt.expectErr && err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		})
	}
}

func TestParseFileInfo_FileSizeFormats(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		expectedSize int64
		expectError  bool
	}{
		{
			name: "file size in GiB",
			output: `name=storage-pool/metal-csi/test.img type=.img
                    file size=10.0GiB last-modified=2025-11-11 14:32:41`,
			expectedSize: 10 * 1024 * 1024 * 1024,
			expectError:  false,
		},
		{
			name: "file size in MiB",
			output: `name=storage-pool/metal-csi/test.img type=.img
                    file size=1024.0MiB last-modified=2025-11-11 14:32:41`,
			expectedSize: 1024 * 1024 * 1024,
			expectError:  false,
		},
		{
			name: "file size in TiB",
			output: `name=storage-pool/metal-csi/test.img type=.img
                    file size=5.5TiB last-modified=2025-11-11 14:32:41`,
			expectedSize: int64(5.5 * 1024 * 1024 * 1024 * 1024),
			expectError:  false,
		},
		{
			name: "file size in KiB",
			output: `name=storage-pool/metal-csi/test.img type=.img
                    file size=512.0KiB last-modified=2025-11-11 14:32:41`,
			expectedSize: 512 * 1024,
			expectError:  false,
		},
		{
			name: "raw size with spaces (fallback)",
			output: `name=storage-pool/metal-csi/test.img type=.img
                    size=10 737 418 240 last-modified=2025-11-11 14:32:41`,
			expectedSize: 10737418240,
			expectError:  false,
		},
		{
			name: "file size with GB (not GiB)",
			output: `name=storage-pool/metal-csi/test.img type=.img
                    file size=50GB last-modified=2025-11-11 14:32:41`,
			expectedSize: 50 * 1024 * 1024 * 1024, // parseSize treats GB as GiB
			expectError:  false,
		},
		{
			name: "small file size",
			output: `name=storage-pool/metal-csi/test.img type=.img
                    file size=100.0MiB last-modified=2025-11-11 14:32:41`,
			expectedSize: 100 * 1024 * 1024,
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := parseFileInfo(tt.output)
			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			// Allow 1 MB tolerance for floating point calculations
			diff := file.SizeBytes - tt.expectedSize
			if diff < 0 {
				diff = -diff
			}
			if diff > 1024*1024 {
				t.Errorf("Expected size %d bytes, got %d bytes (diff: %d)",
					tt.expectedSize, file.SizeBytes, diff)
			}
		})
	}
}

func TestParseFileList_MultipleFiles(t *testing.T) {
	output := ` 0   name=storage-pool/metal-csi type=directory
     last-modified=2025-11-11 16:47:07

 1   name=storage-pool/metal-csi/pvc-ccdecfad-a8bf-572e-9120-464c4d99f12f.img
     type=.img file size=10.0GiB last-modified=2025-11-11 14:32:41

 2   name=storage-pool/metal-csi/pvc-0f923194-922a-5dd8-b376-c2c6ccb56dd8.img
     type=.img file size=1024.0MiB last-modified=2025-11-11 16:47:07`

	files, err := parseFileList(output)
	if err != nil {
		t.Fatalf("parseFileList failed: %v", err)
	}

	// Should find 2 .img files (directory is also parsed but that's ok)
	imgFiles := 0
	for _, file := range files {
		if file.Type == ".img" {
			imgFiles++

			// Verify sizes are parsed correctly
			switch file.Name {
			case "pvc-ccdecfad-a8bf-572e-9120-464c4d99f12f.img":
				expectedSize := int64(10 * 1024 * 1024 * 1024)
				if file.SizeBytes != expectedSize {
					t.Errorf("File 1: expected size %d, got %d", expectedSize, file.SizeBytes)
				}
			case "pvc-0f923194-922a-5dd8-b376-c2c6ccb56dd8.img":
				expectedSize := int64(1024 * 1024 * 1024)
				if file.SizeBytes != expectedSize {
					t.Errorf("File 2: expected size %d, got %d", expectedSize, file.SizeBytes)
				}
			}
		}
	}

	if imgFiles != 2 {
		t.Errorf("Expected 2 .img files, found %d", imgFiles)
	}
}

func TestParseRouterOSTime(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectedYear int
		expectedDay  int
		expectZero   bool
	}{
		{
			name:         "ISO format with last-modified",
			input:        `name=test.img type=file last-modified=2025-11-12 00:36:13`,
			expectedYear: 2025,
			expectedDay:  12,
			expectZero:   false,
		},
		{
			name:         "ISO format with creation-time",
			input:        `name=test.img type=file creation-time=2025-12-25 10:30:45`,
			expectedYear: 2025,
			expectedDay:  25,
			expectZero:   false,
		},
		{
			name:         "RouterOS month format with creation-time",
			input:        `name=test.img type=file creation-time=jan/02/2025 00:00:00`,
			expectedYear: 2025,
			expectedDay:  2,
			expectZero:   false,
		},
		{
			name:         "RouterOS month format with last-modified",
			input:        `name=test.img type=file last-modified=nov/15/2025 14:32:41`,
			expectedYear: 2025,
			expectedDay:  15,
			expectZero:   false,
		},
		{
			name:       "no time field",
			input:      `name=test.img type=file size=1024`,
			expectZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseRouterOSTime(tt.input)

			if tt.expectZero {
				if !result.IsZero() {
					t.Errorf("Expected zero time, got %v", result)
				}
				return
			}

			if result.IsZero() {
				t.Errorf("Expected non-zero time, got zero")
				return
			}

			if result.Year() != tt.expectedYear {
				t.Errorf("Expected year %d, got %d", tt.expectedYear, result.Year())
			}

			if result.Day() != tt.expectedDay {
				t.Errorf("Expected day %d, got %d", tt.expectedDay, result.Day())
			}
		})
	}
}

func TestParseFileInfo_CreatedAtParsing(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		expectedYear int
		expectZero   bool
	}{
		{
			name: "ISO format last-modified",
			output: `name=storage-pool/metal-csi/pvc-test.img type=.img
                    file size=10.0GiB last-modified=2025-11-12 00:36:13`,
			expectedYear: 2025,
			expectZero:   false,
		},
		{
			name: "RouterOS format creation-time",
			output: `name=storage-pool/metal-csi/pvc-test.img type=.img
                    file size=10.0GiB creation-time=jan/01/2025 00:00:00`,
			expectedYear: 2025,
			expectZero:   false,
		},
		{
			name: "no time field",
			output: `name=storage-pool/metal-csi/pvc-test.img type=.img
                    file size=10.0GiB`,
			expectZero: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := parseFileInfo(tt.output)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if tt.expectZero {
				if !file.CreatedAt.IsZero() {
					t.Errorf("Expected zero CreatedAt, got %v", file.CreatedAt)
				}
				return
			}

			if file.CreatedAt.IsZero() {
				t.Errorf("Expected non-zero CreatedAt, got zero time")
				return
			}

			if file.CreatedAt.Year() != tt.expectedYear {
				t.Errorf("Expected year %d, got %d", tt.expectedYear, file.CreatedAt.Year())
			}
		})
	}
}

func TestTestableSSHClientInfrastructure(t *testing.T) {
	// Verify the mock infrastructure works
	expectedOutput := "mock output"
	commandReceived := ""

	runner := func(cmd string) (string, error) {
		commandReceived = cmd
		return expectedOutput, nil
	}

	client := newTestableSSHClient(runner)

	// Test that mock runner is called
	output, err := client.runCommand("test command")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if output != expectedOutput {
		t.Errorf("Expected output %q, got %q", expectedOutput, output)
	}
	if commandReceived != "test command" {
		t.Errorf("Expected command %q, got %q", "test command", commandReceived)
	}

	// Test error propagation
	errorRunner := func(cmd string) (string, error) {
		return "", fmt.Errorf("mock error")
	}
	errorClient := newTestableSSHClient(errorRunner)
	_, err = errorClient.runCommand("any")
	if err == nil || !strings.Contains(err.Error(), "mock error") {
		t.Errorf("Expected mock error to propagate")
	}
}

func TestVerifyVolumeExistsCommandConstruction(t *testing.T) {
	setupTestBasePaths(t)

	// Test that VerifyVolumeExists constructs correct command
	// This tests the validation and command pattern without SSH
	tests := []struct {
		name        string
		slot        string
		expectError bool
	}{
		{"valid slot", "pvc-test-123", false},
		{"empty slot", "", true},
		{"dangerous slot", "pvc;evil", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSlotName(tt.slot)
			if tt.expectError && err == nil {
				t.Error("Expected validation error")
			}
			if !tt.expectError && err != nil {
				t.Errorf("Unexpected validation error: %v", err)
			}
		})
	}
}

func TestExtractMountPoint(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "mount point with leading slash",
			input:    "/storage-pool/metal-csi/volumes",
			expected: "storage-pool",
		},
		{
			name:     "mount point without leading slash",
			input:    "storage-pool/metal-csi/volumes",
			expected: "storage-pool",
		},
		{
			name:     "single component path",
			input:    "/nvme1",
			expected: "nvme1",
		},
		{
			name:     "multi-level path",
			input:    "/nvme1/kubernetes/volumes",
			expected: "nvme1",
		},
		{
			name:     "empty path",
			input:    "",
			expected: "",
		},
		{
			name:     "root path",
			input:    "/",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractMountPoint(tt.input)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestNormalizeRouterOSOutputEdgeCases(t *testing.T) {
	tests := []struct {
		name             string
		input            string
		expectedContains string
		description      string
	}{
		{
			name:             "carriage returns",
			input:            "line1\r\nline2\r\n",
			expectedContains: "line1",
			description:      "should remove \\r characters",
		},
		{
			name:             "RouterOS flags header",
			input:            "Flags: X - disabled\ntype=file slot=test",
			expectedContains: "type=file slot=test",
			description:      "should skip Flags: header lines",
		},
		{
			name:             "continuation lines with tabs",
			input:            "type=file\n\tsize=1000",
			expectedContains: "type=file size=1000",
			description:      "should join continuation lines starting with tab",
		},
		{
			name:             "continuation lines with spaces",
			input:            "type=file\n   size=1000",
			expectedContains: "type=file size=1000",
			description:      "should join continuation lines starting with spaces",
		},
		{
			name:             "multiple continuation lines",
			input:            "type=file\n  size=1000\n  path=/test",
			expectedContains: "type=file size=1000 path=/test",
			description:      "should join multiple continuation lines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeRouterOSOutput(tt.input)
			if !strings.Contains(result, tt.expectedContains) {
				t.Errorf("Expected normalized output to contain %q, got %q\nDescription: %s",
					tt.expectedContains, result, tt.description)
			}
		})
	}
}

// Snapshot parsing tests

func TestParseSnapshotInfo(t *testing.T) {
	tests := []struct {
		name               string
		output             string
		expectName         string
		expectSourceVolume string
		expectFilePath     string
		expectSizeBytes    int64
		expectError        bool
	}{
		{
			name: "valid snapshot with /disk print format - all fields",
			// /disk print detail output format (same key=value format as volumes)
			// Snapshots have NO nvme-tcp-export fields.
			// NOTE: Real RouterOS does not emit source-volume; SourceVolume will be empty
			// without the source-volume field. Source volume is tracked by the controller.
			output: `type=file slot="snap-a1b2c3d4-e5f6-7890-abcd-ef1234567890-at-1739800000"
                    file-path=/storage-pool/metal-csi/snap-a1b2c3d4-e5f6-7890-abcd-ef1234567890-at-1739800000.img
                    file-size=50.0GiB`,
			expectName:         "snap-a1b2c3d4-e5f6-7890-abcd-ef1234567890-at-1739800000",
			expectSourceVolume: "", // No source-volume field in output; slot name UUID no longer encodes source
			expectFilePath:     "/storage-pool/metal-csi/snap-a1b2c3d4-e5f6-7890-abcd-ef1234567890-at-1739800000.img",
			expectSizeBytes:    50 * 1024 * 1024 * 1024,
		},
		{
			name: "snapshot with mock source-volume field",
			// Mock server provides source-volume for testing without real RouterOS
			output: `type=file slot="snap-a1b2c3d4-e5f6-7890-abcd-ef1234567890-at-1739800000"
                    file-path=/storage-pool/metal-csi/snap-a1b2c3d4-e5f6-7890-abcd-ef1234567890-at-1739800000.img
                    file-size=50.0GiB
                    source-volume=pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890`,
			expectName:         "snap-a1b2c3d4-e5f6-7890-abcd-ef1234567890-at-1739800000",
			expectSourceVolume: "pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890",
			expectFilePath:     "/storage-pool/metal-csi/snap-a1b2c3d4-e5f6-7890-abcd-ef1234567890-at-1739800000.img",
			expectSizeBytes:    50 * 1024 * 1024 * 1024,
		},
		{
			name: "snapshot with quoted slot name",
			output: `type=file slot="snap-11111111-2222-3333-4444-555555555555-at-1700000000"
                    file-path="/storage-pool/metal-csi/snap-11111111-2222-3333-4444-555555555555-at-1700000000.img"
                    file-size=100.0GiB`,
			expectName:         "snap-11111111-2222-3333-4444-555555555555-at-1700000000",
			expectSourceVolume: "", // No source-volume field; slot UUID no longer encodes source
			expectFilePath:     "/storage-pool/metal-csi/snap-11111111-2222-3333-4444-555555555555-at-1700000000.img",
			expectSizeBytes:    100 * 1024 * 1024 * 1024,
		},
		{
			name:        "empty output",
			output:      "",
			expectError: false, // parseSnapshotInfo returns empty SnapshotInfo, not error
		},
		{
			name: "partial output (missing file-size)",
			// Missing file-size is OK - will be zero
			output: `type=file slot="snap-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee-at-1739900000"
                    file-path=/storage-pool/metal-csi/snap-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee-at-1739900000.img`,
			expectName:         "snap-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee-at-1739900000",
			expectSourceVolume: "", // No source-volume field; slot UUID no longer encodes source
			expectFilePath:     "/storage-pool/metal-csi/snap-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee-at-1739900000.img",
			expectSizeBytes:    0, // Missing, expected zero
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := parseSnapshotInfo(tt.output)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if snapshot.Name != tt.expectName {
				t.Errorf("Expected name %q, got %q", tt.expectName, snapshot.Name)
			}

			if tt.expectSourceVolume != "" && snapshot.SourceVolume != tt.expectSourceVolume {
				t.Errorf("Expected source volume %q, got %q", tt.expectSourceVolume, snapshot.SourceVolume)
			}

			if tt.expectFilePath != "" && snapshot.FilePath != tt.expectFilePath {
				t.Errorf("Expected file path %q, got %q", tt.expectFilePath, snapshot.FilePath)
			}

			if tt.expectSizeBytes > 0 {
				// Allow 1 MB tolerance for floating point
				diff := snapshot.FileSizeBytes - tt.expectSizeBytes
				if diff < 0 {
					diff = -diff
				}
				if diff > 1024*1024 {
					t.Errorf("Expected size %d bytes, got %d bytes", tt.expectSizeBytes, snapshot.FileSizeBytes)
				}
			}
		})
	}
}

func TestParseSnapshotList(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		expectCount int
		expectNames []string
		expectError bool
	}{
		{
			name: "multiple snapshots in /disk print format",
			output: ` 0  type=file slot="snap-12345678-1234-1234-1234-123456789abc-at-1739800000"
                    file-path=/storage-pool/metal-csi/snap-12345678-1234-1234-1234-123456789abc-at-1739800000.img
                    file-size=50.0GiB

 1  type=file slot="snap-abcdef12-3456-7890-abcd-ef1234567890-at-1739900000"
                    file-path=/storage-pool/metal-csi/snap-abcdef12-3456-7890-abcd-ef1234567890-at-1739900000.img
                    file-size=100.0GiB

 2  type=file slot="snap-test-sanity"
                    file-path=/storage-pool/metal-csi/snap-test-sanity.img
                    file-size=10.0GiB`,
			expectCount: 3,
			expectNames: []string{
				"snap-12345678-1234-1234-1234-123456789abc-at-1739800000",
				"snap-abcdef12-3456-7890-abcd-ef1234567890-at-1739900000",
				"snap-test-sanity",
			},
		},
		{
			name:        "empty list",
			output:      "",
			expectCount: 0,
			expectNames: []string{},
		},
		{
			name: "mixed snap- and non-snap entries (should filter)",
			// The ListSnapshots command uses where slot~"snap-" so this case is rare
			// but parseSnapshotList also filters at parse level
			output: ` 0  type=file slot="snap-12345678-1234-1234-1234-123456789abc-at-1739800000"
                    file-path=/storage-pool/metal-csi/snap-12345678-1234-1234-1234-123456789abc-at-1739800000.img
                    file-size=50.0GiB

 1  type=file slot="pvc-not-a-snapshot"
                    file-path=/storage-pool/metal-csi/pvc-not-a-snapshot.img
                    file-size=50.0GiB nvme-tcp-export=yes

 2  type=file slot="snap-test-123"
                    file-path=/storage-pool/metal-csi/snap-test-123.img
                    file-size=50.0GiB`,
			expectCount: 2, // Only snap-* entries
			expectNames: []string{
				"snap-12345678-1234-1234-1234-123456789abc-at-1739800000",
				"snap-test-123",
			},
		},
		{
			name: "no snap- prefix (all filtered out)",
			output: ` 0  type=file slot="pvc-backup-volume"
                    file-path=/storage-pool/metal-csi/pvc-backup-volume.img
                    file-size=50.0GiB nvme-tcp-export=yes

 1  type=file slot="pvc-clone-volume"
                    file-path=/storage-pool/metal-csi/pvc-clone-volume.img
                    file-size=50.0GiB nvme-tcp-export=yes`,
			expectCount: 0,
			expectNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshots, err := parseSnapshotList(tt.output)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(snapshots) != tt.expectCount {
				t.Errorf("Expected %d snapshots, got %d", tt.expectCount, len(snapshots))
			}

			// Verify specific names if provided
			if len(tt.expectNames) > 0 {
				for i, expectedName := range tt.expectNames {
					if i >= len(snapshots) {
						t.Errorf("Missing expected snapshot %q at index %d", expectedName, i)
						continue
					}
					if snapshots[i].Name != expectedName {
						t.Errorf("Snapshot %d: expected name %q, got %q", i, expectedName, snapshots[i].Name)
					}
				}
			}

			// Ensure empty list returns empty slice, not nil
			if tt.expectCount == 0 && snapshots == nil {
				t.Error("Expected empty slice, got nil")
			}
		})
	}
}

// MockClient snapshot operation tests

func TestMockClientSnapshotOperations(t *testing.T) {
	mock := NewMockClient()

	// Create a test volume first
	volOpts := CreateVolumeOptions{
		Slot:          "pvc-aabbccdd-eeff-0011-2233-445566778899",
		FilePath:      "/storage-pool/metal-csi/pvc-aabbccdd-eeff-0011-2233-445566778899.img",
		FileSizeBytes: 10 * 1024 * 1024 * 1024, // 10GB
		NVMETCPPort:   4420,
		NVMETCPNQN:    "nqn.2000-02.com.mikrotik:pvc-aabbccdd-eeff-0011-2233-445566778899",
	}
	if err := mock.CreateVolume(volOpts); err != nil {
		t.Fatalf("Failed to create test volume: %v", err)
	}

	// Test 1: Create snapshot from volume using new BasePath-based options
	snapOpts := CreateSnapshotOptions{
		Name:         "snap-aabbccdd-eeff-0011-2233-445566778899-at-1739800000",
		SourceVolume: "pvc-aabbccdd-eeff-0011-2233-445566778899",
		BasePath:     "/storage-pool/metal-csi",
	}

	snapshot, err := mock.CreateSnapshot(snapOpts)
	if err != nil {
		t.Fatalf("CreateSnapshot failed: %v", err)
	}

	if snapshot.Name != snapOpts.Name {
		t.Errorf("Expected snapshot name %s, got %s", snapOpts.Name, snapshot.Name)
	}
	if snapshot.SourceVolume != "pvc-aabbccdd-eeff-0011-2233-445566778899" {
		t.Errorf("Expected source volume pvc-aabbccdd-eeff-0011-2233-445566778899, got %s", snapshot.SourceVolume)
	}
	// Snapshots are NOT ReadOnly field anymore - no NVMe export, just a backing file
	// Verify FilePath is set correctly
	expectedFilePath := "/storage-pool/metal-csi/" + snapOpts.Name + ".img"
	if snapshot.FilePath != expectedFilePath {
		t.Errorf("Expected file path %s, got %s", expectedFilePath, snapshot.FilePath)
	}
	// Verify size was copied from source volume
	if snapshot.FileSizeBytes != volOpts.FileSizeBytes {
		t.Errorf("Expected size %d (from source), got %d", volOpts.FileSizeBytes, snapshot.FileSizeBytes)
	}

	// Test 2: GetSnapshot returns correct snapshot
	retrieved, err := mock.GetSnapshot(snapOpts.Name)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if retrieved.Name != snapshot.Name {
		t.Errorf("Retrieved snapshot name mismatch: expected %s, got %s", snapshot.Name, retrieved.Name)
	}

	// Test 3: Create duplicate snapshot with same name and source (idempotent)
	duplicate, err := mock.CreateSnapshot(snapOpts)
	if err != nil {
		t.Fatalf("CreateSnapshot (duplicate) failed: %v", err)
	}
	if duplicate.Name != snapshot.Name {
		t.Error("Expected idempotent CreateSnapshot to return existing snapshot")
	}

	// Test 4: Create duplicate snapshot with same name but different source (error)
	badOpts := CreateSnapshotOptions{
		Name:         snapOpts.Name,
		SourceVolume: "pvc-different-volume",
		BasePath:     "/storage-pool/metal-csi",
	}
	_, err = mock.CreateSnapshot(badOpts)
	if err == nil {
		t.Error("Expected error when creating snapshot with same name but different source")
	}

	// Test 5: ListSnapshots returns all snapshots
	// Create another snapshot first
	snapOpts2 := CreateSnapshotOptions{
		Name:         "snap-aabbccdd-eeff-0011-2233-445566778899-at-1739900000",
		SourceVolume: "pvc-aabbccdd-eeff-0011-2233-445566778899",
		BasePath:     "/storage-pool/metal-csi",
	}
	if _, err := mock.CreateSnapshot(snapOpts2); err != nil {
		t.Fatalf("Failed to create second snapshot: %v", err)
	}

	snapshots, err := mock.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots failed: %v", err)
	}
	if len(snapshots) != 2 {
		t.Errorf("Expected 2 snapshots, got %d", len(snapshots))
	}

	// Test 6: DeleteSnapshot removes snapshot
	if err := mock.DeleteSnapshot(snapOpts.Name); err != nil {
		t.Fatalf("DeleteSnapshot failed: %v", err)
	}

	// Verify snapshot is gone
	_, err = mock.GetSnapshot(snapOpts.Name)
	if err == nil {
		t.Error("Expected SnapshotNotFoundError after deletion")
	}
	var notFoundErr1 *SnapshotNotFoundError
	if !errors.As(err, &notFoundErr1) {
		t.Errorf("Expected SnapshotNotFoundError, got %T: %v", err, err)
	}

	// Test 7: Delete non-existent snapshot (idempotent)
	if err := mock.DeleteSnapshot("snap-nonexistent"); err != nil {
		t.Errorf("Expected DeleteSnapshot to be idempotent, got error: %v", err)
	}

	// Test 8: GetSnapshot on non-existent snapshot returns error
	_, err = mock.GetSnapshot("snap-nonexistent")
	if err == nil {
		t.Error("Expected error when getting non-existent snapshot")
	}
	var notFoundErr2 *SnapshotNotFoundError
	if !errors.As(err, &notFoundErr2) {
		t.Errorf("Expected SnapshotNotFoundError, got %T: %v", err, err)
	}

	// Test 9: ListSnapshots with no snapshots returns empty slice
	// Delete remaining snapshot
	if err := mock.DeleteSnapshot(snapOpts2.Name); err != nil {
		t.Fatalf("Failed to delete second snapshot: %v", err)
	}

	snapshots, err = mock.ListSnapshots()
	if err != nil {
		t.Fatalf("ListSnapshots failed: %v", err)
	}
	if snapshots == nil {
		t.Error("Expected empty slice, got nil")
	}
	if len(snapshots) != 0 {
		t.Errorf("Expected 0 snapshots, got %d", len(snapshots))
	}
}

// Snapshot ID validation tests (in utils package, but test here for completeness)

func TestValidateSnapshotID(t *testing.T) {
	tests := []struct {
		name        string
		snapshotID  string
		expectError bool
	}{
		{
			name:        "valid new format: snap-<uuid>-at-<timestamp>",
			snapshotID:  "snap-12345678-1234-1234-1234-123456789abc-at-1739800000",
			expectError: false,
		},
		{
			name:        "valid legacy format: snap-<uuid>",
			snapshotID:  "snap-12345678-1234-1234-1234-123456789abc",
			expectError: false,
		},
		{
			name:        "valid alphanumeric (sanity test ID)",
			snapshotID:  "test-snapshot-123",
			expectError: false,
		},
		{
			name:        "empty string",
			snapshotID:  "",
			expectError: true,
		},
		{
			name:        "command injection: semicolon",
			snapshotID:  "snap-test;rm -rf /",
			expectError: true,
		},
		{
			name:        "command injection: pipe",
			snapshotID:  "snap-test|evil",
			expectError: true,
		},
		{
			name:        "command injection: dollar",
			snapshotID:  "snap-test$var",
			expectError: true,
		},
		{
			name:        "command injection: backtick",
			snapshotID:  "snap-test`cmd`",
			expectError: true,
		},
		{
			name:        "snap- prefix with invalid format (not uuid, not uuid-at-ts)",
			snapshotID:  "snap-INVALID",
			expectError: true,
		},
		{
			name:        "snap- prefix with uppercase UUID",
			snapshotID:  "snap-12345678-1234-1234-1234-123456789ABC",
			expectError: true,
		},
		{
			name:        "UUID without snap- prefix",
			snapshotID:  "12345678-1234-1234-1234-123456789abc",
			expectError: true,
		},
		{
			name:        "too long (>250 chars)",
			snapshotID:  "snap-" + strings.Repeat("a", 250),
			expectError: true,
		},
		{
			name:        "new format with timestamp zero",
			snapshotID:  "snap-12345678-1234-1234-1234-123456789abc-at-0",
			expectError: false, // 0 is a valid Unix timestamp
		},
		{
			name:        "deterministic hash format: snap-<uuid>-at-<10-hex>",
			snapshotID:  "snap-12345678-1234-1234-1234-123456789abc-at-3a9f8c02d1",
			expectError: false, // deterministic hash suffix
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := utils.ValidateSnapshotID(tt.snapshotID)

			if tt.expectError && err == nil {
				t.Error("Expected validation error but got none")
			}

			if !tt.expectError && err != nil {
				t.Errorf("Unexpected validation error: %v", err)
			}
		})
	}
}

func TestParseDiskMetrics(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected *DiskMetrics
	}{
		{
			name: "full output with active writes",
			output: `                  slot:    storage-pool
              read-ops:         243 401
   read-ops-per-second:               0
            read-bytes:  33 131 503 616
             read-rate:            0bps
           read-merges:               0
             read-time:             0ms
             write-ops:      17 667 231
  write-ops-per-second:              76
           write-bytes: 515 659 673 600
            write-rate:        12.8Mbps
          write-merges:               0
            write-time:             0ms
         in-flight-ops:               0
           active-time:             0ms
             wait-time:             0ms
           discard-ops:               0
         discard-bytes:               0
        discard-merges:               0
          discard-time:             0ms
             flush-ops:               0
            flush-time:             0ms`,
			expected: &DiskMetrics{
				ReadOpsPerSecond:  0,
				WriteOpsPerSecond: 76,
				ReadBytesPerSec:   0,
				WriteBytesPerSec:  1_600_000, // 12.8Mbps = 12.8 * 1_000_000 / 8 = 1_600_000
				ReadTimeMs:        0,
				WriteTimeMs:       0,
				WaitTimeMs:        0,
				InFlightOps:       0,
				ActiveTimeMs:      0,
			},
		},
		{
			name: "active reads with Gbps throughput",
			output: `   read-ops-per-second:            1500
  write-ops-per-second:             200
             read-rate:         1.5Gbps
            write-rate:       100.0Mbps
             read-time:             2ms
            write-time:             5ms
         in-flight-ops:               8
           active-time:            10ms
             wait-time:             1ms`,
			expected: &DiskMetrics{
				ReadOpsPerSecond:  1500,
				WriteOpsPerSecond: 200,
				ReadBytesPerSec:   187_500_000, // 1.5Gbps = 1.5 * 1_000_000_000 / 8
				WriteBytesPerSec:  12_500_000,  // 100Mbps = 100 * 1_000_000 / 8
				ReadTimeMs:        2,
				WriteTimeMs:       5,
				WaitTimeMs:        1,
				InFlightOps:       8,
				ActiveTimeMs:      10,
			},
		},
		{
			name: "idle disk - all zeros",
			output: `   read-ops-per-second:               0
  write-ops-per-second:               0
             read-rate:            0bps
            write-rate:            0bps
             read-time:             0ms
            write-time:             0ms
         in-flight-ops:               0
           active-time:             0ms
             wait-time:             0ms`,
			expected: &DiskMetrics{},
		},
		{
			name: "kbps throughput",
			output: `   read-ops-per-second:              10
  write-ops-per-second:               5
             read-rate:          500kbps
            write-rate:          250kbps
             read-time:             0ms
            write-time:             0ms
         in-flight-ops:               1
           active-time:             0ms
             wait-time:             0ms`,
			expected: &DiskMetrics{
				ReadOpsPerSecond:  10,
				WriteOpsPerSecond: 5,
				ReadBytesPerSec:   62_500, // 500kbps = 500 * 1000 / 8
				WriteBytesPerSec:  31_250, // 250kbps = 250 * 1000 / 8
				InFlightOps:       1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseDiskMetrics(tt.output)
			if err != nil {
				t.Fatalf("parseDiskMetrics failed: %v", err)
			}

			if result.ReadOpsPerSecond != tt.expected.ReadOpsPerSecond {
				t.Errorf("ReadOpsPerSecond: got %v, want %v", result.ReadOpsPerSecond, tt.expected.ReadOpsPerSecond)
			}
			if result.WriteOpsPerSecond != tt.expected.WriteOpsPerSecond {
				t.Errorf("WriteOpsPerSecond: got %v, want %v", result.WriteOpsPerSecond, tt.expected.WriteOpsPerSecond)
			}
			if result.ReadBytesPerSec != tt.expected.ReadBytesPerSec {
				t.Errorf("ReadBytesPerSec: got %v, want %v", result.ReadBytesPerSec, tt.expected.ReadBytesPerSec)
			}
			if result.WriteBytesPerSec != tt.expected.WriteBytesPerSec {
				t.Errorf("WriteBytesPerSec: got %v, want %v", result.WriteBytesPerSec, tt.expected.WriteBytesPerSec)
			}
			if result.ReadTimeMs != tt.expected.ReadTimeMs {
				t.Errorf("ReadTimeMs: got %v, want %v", result.ReadTimeMs, tt.expected.ReadTimeMs)
			}
			if result.WriteTimeMs != tt.expected.WriteTimeMs {
				t.Errorf("WriteTimeMs: got %v, want %v", result.WriteTimeMs, tt.expected.WriteTimeMs)
			}
			if result.WaitTimeMs != tt.expected.WaitTimeMs {
				t.Errorf("WaitTimeMs: got %v, want %v", result.WaitTimeMs, tt.expected.WaitTimeMs)
			}
			if result.InFlightOps != tt.expected.InFlightOps {
				t.Errorf("InFlightOps: got %v, want %v", result.InFlightOps, tt.expected.InFlightOps)
			}
			if result.ActiveTimeMs != tt.expected.ActiveTimeMs {
				t.Errorf("ActiveTimeMs: got %v, want %v", result.ActiveTimeMs, tt.expected.ActiveTimeMs)
			}
		})
	}
}

func TestBuildDiskMonitorTrafficCommand(t *testing.T) {
	tests := []struct {
		name string
		slot string
		want string
	}{
		{
			name: "storage pool slot uses keyed disk parameter",
			slot: "storage-pool",
			want: "/disk monitor-traffic disk=storage-pool once",
		},
		{
			name: "numeric disk id uses keyed disk parameter",
			slot: "0",
			want: "/disk monitor-traffic disk=0 once",
		},
		{
			name: "pvc slot uses keyed disk parameter",
			slot: "pvc-test-123",
			want: "/disk monitor-traffic disk=pvc-test-123 once",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateSlotName(tt.slot); err != nil {
				t.Fatalf("test slot should be valid: %v", err)
			}
			if got := buildDiskMonitorTrafficCommand(tt.slot); got != tt.want {
				t.Fatalf("buildDiskMonitorTrafficCommand(%q) = %q, want %q", tt.slot, got, tt.want)
			}
		})
	}
}

func TestConvertRateToBytesPerSec(t *testing.T) {
	tests := []struct {
		value    float64
		unit     string
		expected float64
	}{
		{0, "bps", 0},
		{8, "bps", 1},
		{500, "kbps", 62_500},
		{12.8, "Mbps", 1_600_000},
		{1.5, "Gbps", 187_500_000},
		{100, "unknown", 100}, // Unknown unit returns raw value
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%v_%s", tt.value, tt.unit), func(t *testing.T) {
			result := convertRateToBytesPerSec(tt.value, tt.unit)
			if result != tt.expected {
				t.Errorf("convertRateToBytesPerSec(%v, %q) = %v, want %v", tt.value, tt.unit, result, tt.expected)
			}
		})
	}
}

// ============================================================================
// RouterOS 7.23+ compatibility regression tests for runDiskAdd mount param rename
// and waitForVolumeReady error semantics. These defend the live-validated behavior.
// ============================================================================

func TestRouterOSMountParamFallback_RunDiskAdd(t *testing.T) {
	t.Run("first mount=no rejected with bad parameter triggers retry with mount-filesystem=no", func(t *testing.T) {
		var received []string
		srv := startMockSSHServer(t, func(channel ssh.Channel, requests <-chan *ssh.Request) {
			defer func() { _ = channel.Close() }()
			for req := range requests {
				if req.Type == "exec" {
					_ = req.Reply(true, nil)
					cmdLen := int(req.Payload[3])
					cmd := string(req.Payload[4 : 4+cmdLen])
					received = append(received, cmd)

					if strings.Contains(cmd, " mount=") && !strings.Contains(cmd, "mount-filesystem") {
						// RouterOS rejects old mount param (exit 0 + error text in stdout -> CommandOutputError)
						_, _ = channel.Write([]byte("bad parameter mount\n"))
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
						return
					}
					// success response for /disk add
					_, _ = channel.Write([]byte(" 0 name=added slot=test-pvc"))
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
					return
				}
			}
		})

		client := createConnectedTestClient(t, srv)

		cmd := `/disk add type=file file-path=/test.img file-size=1G slot=test-pvc mount=no nvme-tcp-export=yes nvme-tcp-server-port=4420 nvme-tcp-server-nqn=nqn.test`
		out, err := client.runDiskAdd(cmd, false)
		if err != nil {
			t.Fatalf("runDiskAdd failed: %v", err)
		}
		if !strings.Contains(out, "name=added") {
			t.Errorf("expected success output, got %q", out)
		}

		if len(received) != 2 {
			t.Fatalf("expected exactly 2 commands (fail + retry), got %d: %v", len(received), received)
		}
		if !strings.Contains(received[0], "mount=no") || strings.Contains(received[0], "mount-filesystem") {
			t.Errorf("first cmd should have had old mount=no, got %q", received[0])
		}
		if !strings.Contains(received[1], "mount-filesystem=no") || strings.Contains(received[1], " mount=no") {
			t.Errorf("retry cmd must use mount-filesystem=no not mount=no, got %q", received[1])
		}

		if !client.diskAddMountRenamed.Load() {
			t.Error("diskAddMountRenamed should be set after rejection")
		}
	})

	t.Run("subsequent calls use mount-filesystem directly with no failed first attempt", func(t *testing.T) {
		var received []string
		srv := startMockSSHServer(t, func(channel ssh.Channel, requests <-chan *ssh.Request) {
			defer func() { _ = channel.Close() }()
			for req := range requests {
				if req.Type == "exec" {
					_ = req.Reply(true, nil)
					cmdLen := int(req.Payload[3])
					cmd := string(req.Payload[4 : 4+cmdLen])
					received = append(received, cmd)

					// after rename learned, no rejection even if old name in caller cmd
					_, _ = channel.Write([]byte(" 0 name=added2"))
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
					return
				}
			}
		})

		client := createConnectedTestClient(t, srv)

		// simulate prior learning by setting the flag (as would happen in real lifetime)
		client.diskAddMountRenamed.Store(true)

		cmd := `/disk add type=file file-path=/test.img file-size=1G slot=test-pvc2 mount=yes nvme-tcp-export=yes nvme-tcp-server-port=4420 nvme-tcp-server-nqn=nqn.test2`
		_, err := client.runDiskAdd(cmd, true)
		if err != nil {
			t.Fatalf("runDiskAdd with flag set should succeed: %v", err)
		}
		if len(received) != 1 {
			t.Fatalf("expected 1 command (no failed attempt), got %d", len(received))
		}
		if !strings.Contains(received[0], "mount-filesystem=yes") || strings.Contains(received[0], " mount=yes") {
			t.Errorf("cmd must be rewritten to mount-filesystem=yes, got %q", received[0])
		}
	})
}

func TestRunDiskAdd_MountYesRejectionPath(t *testing.T) {
	var received []string
	srv := startMockSSHServer(t, func(channel ssh.Channel, requests <-chan *ssh.Request) {
		defer func() { _ = channel.Close() }()
		for req := range requests {
			if req.Type == "exec" {
				_ = req.Reply(true, nil)
				cmdLen := int(req.Payload[3])
				cmd := string(req.Payload[4 : 4+cmdLen])
				received = append(received, cmd)

				if strings.Contains(cmd, " mount=") && !strings.Contains(cmd, "mount-filesystem") {
					_, _ = channel.Write([]byte("bad parameter mount\n"))
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
					return
				}
				_, _ = channel.Write([]byte(" 0 name=restored"))
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
				return
			}
		}
	})

	client := createConnectedTestClient(t, srv)

	cmd := `/disk add type=file copy-from=... file-path=/r.img file-size=2G slot=rest mount=yes nvme-tcp-export=yes ...`
	out, err := client.runDiskAdd(cmd, true)
	if err != nil {
		t.Fatalf("expected retry success for mount=yes case, got err: %v", err)
	}
	if !strings.Contains(out, "name=restored") {
		t.Errorf("expected success out, got %q", out)
	}
	if len(received) != 2 {
		t.Fatalf("expected 2 cmds for fallback, got %d", len(received))
	}
	if !strings.Contains(received[1], "mount-filesystem=yes") {
		t.Errorf("second must use mount-filesystem=yes, got %q", received[1])
	}
}

func TestWaitForVolumeReady(t *testing.T) {
	t.Run("returns nil promptly when volume becomes ready on later poll", func(t *testing.T) {
		attempt := 0
		srv := startMockSSHServer(t, func(channel ssh.Channel, requests <-chan *ssh.Request) {
			defer func() { _ = channel.Close() }()
			for req := range requests {
				if req.Type == "exec" {
					_ = req.Reply(true, nil)
					cmdLen := int(req.Payload[3])
					cmd := string(req.Payload[4 : 4+cmdLen])
					attempt++

					if strings.Contains(cmd, "/disk print detail where slot=") {
						if attempt < 3 {
							// simulate not visible yet -> empty output -> GetVolume returns notfound
							_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
							return
						}
						// return parsable volume that will have Status=ready (assumed for file+export)
						_, _ = channel.Write([]byte(` 0 slot=test-ready type=file nvme-tcp-export=yes file-size=1G`))
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
						return
					}
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
					return
				}
			}
		})

		client := createConnectedTestClient(t, srv)
		err := client.waitForVolumeReady("test-ready", 10*time.Second)
		if err != nil {
			t.Fatalf("waitForVolumeReady should succeed when becomes ready: %v", err)
		}
		if attempt < 2 {
			t.Errorf("expected at least 2 polls, got %d", attempt)
		}
	})

	t.Run("returns error containing slot name and no typed VolumeNotFoundError in chain on timeout", func(t *testing.T) {
		srv := startMockSSHServer(t, func(channel ssh.Channel, requests <-chan *ssh.Request) {
			defer func() { _ = channel.Close() }()
			for req := range requests {
				if req.Type == "exec" {
					_ = req.Reply(true, nil)
					cmdLen := int(req.Payload[3])
					cmd := string(req.Payload[4 : 4+cmdLen])

					if strings.Contains(cmd, "/disk print detail where slot=") {
						// always not found: empty stdout + exit 0
						_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
						return
					}
					_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{0}))
					return
				}
			}
		})

		client := createConnectedTestClient(t, srv)
		err := client.waitForVolumeReady("never-appears", 1*time.Second)
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if !strings.Contains(err.Error(), "never-appears") || !strings.Contains(err.Error(), "timed out") {
			t.Errorf("error should contain slot and timeout info, got: %v", err)
		}
		// must be plain error, not carrying typed not-found (per contract for mutation callers)
		if errors.Is(err, utils.ErrVolumeNotFound) {
			t.Error("timeout error must not be Is(ErrVolumeNotFound)")
		}
		var volNotFound *VolumeNotFoundError
		if errors.As(err, &volNotFound) {
			t.Error("timeout error must not As(*VolumeNotFoundError)")
		}
	})
}
