package rds

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/utils"
)

// CreateVolume creates a file-backed NVMe/TCP volume on RDS
func (c *sshClient) CreateVolume(opts CreateVolumeOptions) error {
	// Validate options
	if err := validateCreateVolumeOptions(opts); err != nil {
		return fmt.Errorf("invalid volume options: %w", err)
	}

	// Convert size to human-readable format (e.g., "50G", "100G")
	sizeStr := formatBytes(opts.FileSizeBytes)

	// Build /disk add command
	cmd := fmt.Sprintf(
		`/disk add type=file file-path=%s file-size=%s slot=%s %s nvme-tcp-export=yes nvme-tcp-server-port=%d nvme-tcp-server-nqn=%s`,
		opts.FilePath,
		sizeStr,
		opts.Slot,
		routerOSMountParam(opts.Mount),
		opts.NVMETCPPort,
		opts.NVMETCPNQN,
	)

	// Execute command with retry
	_, err := c.runCommandWithRetry(cmd, 3)
	if err != nil {
		return fmt.Errorf("failed to create volume: %w", err)
	}

	// Verify volume was created
	if err := c.VerifyVolumeExists(opts.Slot); err != nil {
		return fmt.Errorf("volume creation verification failed: %w", err)
	}

	klog.V(2).Infof("Created volume %s", opts.Slot)
	klog.V(4).Infof("Created volume %s (path=%s, size=%d, nqn=%s)", opts.Slot, opts.FilePath, opts.FileSizeBytes, opts.NVMETCPNQN)
	return nil
}

// ResizeVolume resizes an existing volume on RDS
func (c *sshClient) ResizeVolume(slot string, newSizeBytes int64) error {
	// Validate slot name
	if err := validateSlotName(slot); err != nil {
		return err
	}

	// Validate new size
	if newSizeBytes <= 0 {
		return fmt.Errorf("new size must be positive")
	}

	// Get current volume to check it exists and get current size
	currentVolume, err := c.GetVolume(slot)
	if err != nil {
		return fmt.Errorf("failed to get current volume info: %w", err)
	}

	// Verify we're expanding (not shrinking)
	if newSizeBytes < currentVolume.FileSizeBytes {
		return fmt.Errorf("shrinking volumes is not supported (current: %d bytes, requested: %d bytes)",
			currentVolume.FileSizeBytes, newSizeBytes)
	}

	// If size is the same, nothing to do
	if newSizeBytes == currentVolume.FileSizeBytes {
		klog.V(4).Infof("Volume %s is already at requested size, skipping resize", slot)
		return nil
	}

	// Convert size to human-readable format
	sizeStr := formatBytes(newSizeBytes)

	// Build /disk set command
	cmd := fmt.Sprintf(`/disk set [find slot=%s] file-size=%s`, slot, sizeStr)

	// Execute command with retry
	_, err = c.runCommandWithRetry(cmd, 3)
	if err != nil {
		return fmt.Errorf("failed to resize volume: %w", err)
	}

	// Verify new size
	updatedVolume, err := c.GetVolume(slot)
	if err != nil {
		return fmt.Errorf("failed to verify resize: %w", err)
	}

	klog.V(2).Infof("Resized volume %s (%d -> %d bytes)", slot, currentVolume.FileSizeBytes, updatedVolume.FileSizeBytes)
	return nil
}

// DeleteVolume removes a volume from RDS, including both the disk slot and backing file
func (c *sshClient) DeleteVolume(slot string) error {
	// Validate slot name
	if err := validateSlotName(slot); err != nil {
		return err
	}

	// Get volume info first to find the backing file path
	volume, err := c.GetVolume(slot)
	if err != nil {
		// If volume doesn't exist, that's okay (idempotent)
		if errors.Is(err, utils.ErrVolumeNotFound) {
			klog.V(4).Infof("Volume %s already deleted", slot)
			return nil
		}
		return fmt.Errorf("failed to get volume info before deletion: %w", err)
	}

	filePath := volume.FilePath
	klog.V(4).Infof("Volume %s has backing file: %s", slot, filePath)

	// Step 1: Remove the disk slot
	cmd := fmt.Sprintf(`/disk remove [find slot=%s]`, slot)
	_, err = c.runCommandWithRetry(cmd, 3)
	if err != nil {
		// If volume doesn't exist, that's okay (idempotent)
		if strings.Contains(err.Error(), "no such item") {
			klog.V(4).Infof("Volume %s disk slot does not exist, continuing to file cleanup", slot)
		} else {
			return fmt.Errorf("failed to remove disk slot: %w", err)
		}
	}
	klog.V(4).Infof("Successfully removed disk slot for volume %s", slot)

	// Step 2: Delete the backing file
	if filePath != "" {
		if err := c.DeleteFile(filePath); err != nil {
			// Log but don't fail - the disk slot is already removed
			// The orphan reconciler can clean up the file later if needed
			klog.Warningf("Failed to delete backing file %s for volume %s: %v", filePath, slot, err)
		} else {
			klog.V(4).Infof("Successfully deleted backing file %s for volume %s", filePath, slot)
		}
	}

	klog.V(2).Infof("Deleted volume %s", slot)
	return nil
}

// GetVolume retrieves information about a specific volume
func (c *sshClient) GetVolume(slot string) (*VolumeInfo, error) {
	klog.V(4).Infof("Getting volume info for %s", slot)

	// Validate slot name
	if err := validateSlotName(slot); err != nil {
		return nil, err
	}

	// Build /disk print command
	cmd := fmt.Sprintf(`/disk print detail where slot=%s`, slot)

	// Execute command
	output, err := c.runCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to get volume info: %w", err)
	}

	// Normalize output and check if volume exists
	// RouterOS returns flags header even when volume doesn't exist
	normalized := normalizeRouterOSOutput(output)
	if strings.TrimSpace(normalized) == "" {
		return nil, utils.WrapVolumeError(utils.ErrVolumeNotFound, slot, "")
	}

	volume, err := parseVolumeInfo(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse volume info: %w", err)
	}

	// Additional check: if slot is empty, volume wasn't found
	if volume.Slot == "" {
		return nil, utils.WrapVolumeError(utils.ErrVolumeNotFound, slot, "")
	}

	return volume, nil
}

// VerifyVolumeExists checks if a volume exists and is ready
func (c *sshClient) VerifyVolumeExists(slot string) error {
	volume, err := c.GetVolume(slot)
	if err != nil {
		return err
	}

	if volume.Status != "ready" {
		return fmt.Errorf("volume %s is not ready (status: %s)", slot, volume.Status)
	}

	return nil
}

// GetCapacity queries the available storage capacity on RDS
func (c *sshClient) GetCapacity(basePath string) (*CapacityInfo, error) {
	klog.V(4).Infof("Getting capacity for %s", basePath)

	// SECURITY: Validate base path
	if basePath != "" {
		sanitized, err := utils.SanitizeBasePath(basePath)
		if err != nil {
			return nil, fmt.Errorf("invalid base path: %w", err)
		}
		basePath = sanitized
	}

	// Extract mount point from base path
	// Examples:
	//   /storage-pool/metal-csi/volumes → storage-pool
	//   /nvme1/kubernetes → nvme1
	mountPoint := extractMountPoint(basePath)
	klog.V(4).Infof("Extracted mount point: %s", mountPoint)

	// Query disk capacity using mount point
	// Use /disk print to get filesystem capacity information
	cmd := fmt.Sprintf(`/disk print detail where mount-point="%s"`, mountPoint)

	// Execute command
	output, err := c.runCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to get capacity: %w", err)
	}

	// Parse capacity info
	capacity, err := parseCapacityInfo(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse capacity info: %w", err)
	}

	return capacity, nil
}

// ListVolumes lists all volumes on RDS
// ONLY volumes that are pvc- prefixed are returned
func (c *sshClient) ListVolumes() ([]VolumeInfo, error) {
	klog.V(4).Info("Listing all volumes")

	// Build /disk print command
	cmd := `/disk print detail where slot~"pvc"`

	// Execute command
	output, err := c.runCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}

	// Parse all volumes
	volumes, err := parseVolumeList(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse volume list: %w", err)
	}

	return volumes, nil
}

// ListFiles lists files in a directory on RDS
func (c *sshClient) ListFiles(path string) ([]FileInfo, error) {
	klog.V(4).Infof("Listing files in %s", path)

	// SECURITY: Validate path to prevent command injection
	if err := utils.ValidateFilePath(path); err != nil {
		return nil, fmt.Errorf("invalid path: %w", err)
	}

	// Build /file print command
	// Use "where name~" for pattern matching (~ is RouterOS regex match operator)
	// RouterOS file paths don't include leading /, so strip it if present
	searchPath := strings.TrimPrefix(path, "/")
	cmd := fmt.Sprintf(`/file print detail where name~"%s"`, regexp.QuoteMeta(searchPath))

	// Execute command
	output, err := c.runCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}

	// Parse file list
	files, err := parseFileList(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse file list: %w", err)
	}

	return files, nil
}

// DeleteFile deletes a file on RDS
func (c *sshClient) DeleteFile(path string) error {
	klog.V(4).Infof("Deleting file: %s", path)

	// SECURITY: Validate path to prevent command injection
	if err := utils.ValidateFilePath(path); err != nil {
		return fmt.Errorf("invalid path: %w", err)
	}

	// RouterOS file paths don't include leading / in commands
	searchPath := strings.TrimPrefix(path, "/")

	// Build /file remove command
	cmd := fmt.Sprintf(`/file remove [find name="%s"]`, searchPath)

	// Execute command
	output, err := c.runCommand(cmd)
	if err != nil {
		return fmt.Errorf("failed to delete file: %w", err)
	}

	// RouterOS doesn't return output on successful delete, but check for errors
	if strings.Contains(strings.ToLower(output), "error") || strings.Contains(strings.ToLower(output), "failure") {
		return fmt.Errorf("error deleting file: %s", output)
	}

	klog.V(4).Infof("Successfully deleted file: %s", path)
	return nil
}

// parseVolumeInfo parses RouterOS disk print output for a single volume
func parseVolumeInfo(output string) (*VolumeInfo, error) {
	volume := &VolumeInfo{}

	// Normalize multi-line output: join continuation lines (lines starting with spaces)
	normalized := normalizeRouterOSOutput(output)

	// Extract slot
	if match := regexp.MustCompile(`slot="([^"]+)"`).FindStringSubmatch(normalized); len(match) > 1 {
		volume.Slot = match[1]
	} else if match := regexp.MustCompile(`slot=([^\s]+)`).FindStringSubmatch(normalized); len(match) > 1 {
		volume.Slot = match[1]
	}

	// Extract type
	if match := regexp.MustCompile(`type="?([^"\s]+)"?`).FindStringSubmatch(normalized); len(match) > 1 {
		volume.Type = match[1]
	}

	// Extract file-path (can be quoted or span multiple lines after normalization)
	// RouterOS output often splits long paths across lines, which normalizeRouterOSOutput
	// joins with spaces. We need to handle: file-path=/path/to/pvc-xxx .img (with space)
	if match := regexp.MustCompile(`file-path="([^"]+)"`).FindStringSubmatch(normalized); len(match) > 1 {
		volume.FilePath = match[1]
	} else if match := regexp.MustCompile(`file-path=(\S+\.img)`).FindStringSubmatch(normalized); len(match) > 1 {
		// Simple case: path doesn't have spaces
		volume.FilePath = match[1]
	} else if match := regexp.MustCompile(`file-path=(\S+\s+\S+\.img)`).FindStringSubmatch(normalized); len(match) > 1 {
		// Path was split across lines and has a space after normalization
		// Remove the space to reconstruct the original path
		volume.FilePath = strings.ReplaceAll(match[1], " ", "")
	}
	// Normalize to absolute path format
	if volume.FilePath != "" && !strings.HasPrefix(volume.FilePath, "/") {
		volume.FilePath = "/" + volume.FilePath
	}

	// Extract RouterOS mount setting. RouterOS detail output has used both
	// mount=<yes|no> and mount-filesystem=<yes|no> forms across versions.
	if match := regexp.MustCompile(`mount(?:-filesystem)?=(yes|no)`).FindStringSubmatch(normalized); len(match) > 1 {
		volume.Mount = match[1] == "yes"
	}

	// Extract file-size (human-readable format like "50.0GiB" or "1024.0MiB")
	// This is more reliable than the raw size field with spaces
	if match := regexp.MustCompile(`file-size=([\d.]+)\s*([KMGT]i?B)`).FindStringSubmatch(normalized); len(match) > 2 {
		if bytes, err := parseSize(match[1], match[2]); err == nil {
			volume.FileSizeBytes = bytes
		}
	} else {
		// Fallback: try to parse raw size field (with spaces removed)
		if match := regexp.MustCompile(`size=([\d\s]+)`).FindStringSubmatch(normalized); len(match) > 1 {
			// Remove all spaces from the number
			sizeStr := strings.ReplaceAll(match[1], " ", "")
			if size, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
				volume.FileSizeBytes = size
			}
		}
	}

	// Extract nvme-tcp-export
	if match := regexp.MustCompile(`nvme-tcp-export=(yes|no)`).FindStringSubmatch(normalized); len(match) > 1 {
		volume.NVMETCPExport = match[1] == "yes"
	}

	// Extract nvme-tcp-server-port
	if match := regexp.MustCompile(`nvme-tcp-server-port=(\d+)`).FindStringSubmatch(normalized); len(match) > 1 {
		if port, err := strconv.Atoi(match[1]); err == nil {
			volume.NVMETCPPort = port
		}
	}

	// Extract nvme-tcp-server-nqn
	if match := regexp.MustCompile(`nvme-tcp-server-nqn="([^"]+)"`).FindStringSubmatch(normalized); len(match) > 1 {
		volume.NVMETCPNQN = match[1]
	}

	// Extract status (if available)
	// Note: Real RouterOS doesn't always provide a status field for file-backed disks
	if match := regexp.MustCompile(`status="?([^"\s]+)"?`).FindStringSubmatch(normalized); len(match) > 1 {
		volume.Status = match[1]
	} else {
		// For file-backed volumes with nvme-tcp-export=yes, assume "ready"
		if volume.Type == "file" && volume.NVMETCPExport {
			volume.Status = "ready"
		} else {
			volume.Status = "unknown"
		}
	}

	return volume, nil
}

// parseVolumeList parses RouterOS disk print output for multiple volumes
func parseVolumeList(output string) ([]VolumeInfo, error) {
	var volumes []VolumeInfo

	// Split by volume entries (each starts with a number)
	entries := regexp.MustCompile(`(?m)^\s*\d+\s+`).Split(output, -1)

	for _, entry := range entries {
		if strings.TrimSpace(entry) == "" {
			continue
		}

		volume, err := parseVolumeInfo(entry)
		if err != nil {
			klog.V(4).Infof("Skipping unparseable volume entry: %v", err)
			continue
		}

		volumes = append(volumes, *volume)
	}

	return volumes, nil
}

// parseCapacityInfo parses RouterOS file print output for capacity
func parseCapacityInfo(output string) (*CapacityInfo, error) {
	capacity := &CapacityInfo{}

	// Normalize multi-line output
	normalized := normalizeRouterOSOutput(output)

	// RouterOS /file print detail output format uses space-separated numbers:
	// size=7 681 574 174 720 free=7 301 927 047 168 use=5%

	// Extract size (total capacity) - numbers may have spaces
	if match := regexp.MustCompile(`size=([\d\s]+)`).FindStringSubmatch(normalized); len(match) > 1 {
		sizeStr := strings.ReplaceAll(match[1], " ", "")
		if size, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
			capacity.TotalBytes = size
		}
	}

	// Extract free capacity - numbers may have spaces
	if match := regexp.MustCompile(`free=([\d\s]+)`).FindStringSubmatch(normalized); len(match) > 1 {
		freeStr := strings.ReplaceAll(match[1], " ", "")
		if free, err := strconv.ParseInt(freeStr, 10, 64); err == nil {
			capacity.FreeBytes = free
		}
	}

	// Calculate used capacity
	if capacity.TotalBytes > 0 && capacity.FreeBytes > 0 {
		capacity.UsedBytes = capacity.TotalBytes - capacity.FreeBytes
	}

	// If we didn't get values, return error
	if capacity.TotalBytes == 0 {
		return nil, fmt.Errorf("could not parse capacity from output")
	}

	return capacity, nil
}

// parseFileList parses RouterOS file print output for multiple files
func parseFileList(output string) ([]FileInfo, error) {
	var files []FileInfo

	// Normalize multi-line output
	normalized := normalizeRouterOSOutput(output)

	// Split by file entries (each starts with a number)
	entries := regexp.MustCompile(`(?m)^\s*\d+\s+`).Split(normalized, -1)

	for _, entry := range entries {
		if strings.TrimSpace(entry) == "" {
			continue
		}

		file, err := parseFileInfo(entry)
		if err != nil {
			klog.V(4).Infof("Skipping unparseable file entry: %v", err)
			continue
		}

		files = append(files, *file)
	}

	return files, nil
}

// parseFileInfo parses RouterOS file print output for a single file
func parseFileInfo(output string) (*FileInfo, error) {
	file := &FileInfo{}

	// Normalize multi-line output
	normalized := normalizeRouterOSOutput(output)

	// Extract name (path)
	if match := regexp.MustCompile(`name="([^"]+)"`).FindStringSubmatch(normalized); len(match) > 1 {
		file.Path = match[1]
	} else if match := regexp.MustCompile(`name=([^\s]+)`).FindStringSubmatch(normalized); len(match) > 1 {
		file.Path = match[1]
	}

	// Normalize to absolute path format
	if file.Path != "" && !strings.HasPrefix(file.Path, "/") {
		file.Path = "/" + file.Path
	}

	// Extract filename from path
	if file.Path != "" {
		parts := strings.Split(file.Path, "/")
		if len(parts) > 0 {
			file.Name = parts[len(parts)-1]
		}
	}

	// Extract type
	if match := regexp.MustCompile(`type="?([^"\s]+)"?`).FindStringSubmatch(normalized); len(match) > 1 {
		file.Type = match[1]
	}

	// Extract size from "file size=X.XGiB" (human-readable) or "size=NNN NNN NNN" (raw bytes)
	// Try human-readable format first (e.g., "file size=10.0GiB")
	if match := regexp.MustCompile(`file size=([\d.]+)\s*([KMGT]i?B)`).FindStringSubmatch(normalized); len(match) > 2 {
		if bytes, err := parseSize(match[1], match[2]); err == nil {
			file.SizeBytes = bytes
		}
	} else if match := regexp.MustCompile(`size=([\d\s]+)`).FindStringSubmatch(normalized); len(match) > 1 {
		// Fallback to raw bytes format (numbers may have spaces like "10 737 418 240")
		sizeStr := strings.ReplaceAll(match[1], " ", "")
		if size, err := strconv.ParseInt(sizeStr, 10, 64); err == nil {
			file.SizeBytes = size
		}
	}

	// Extract creation/modification time (if available)
	// RouterOS uses different field names and date formats:
	// - creation-time or last-modified as field names
	// - YYYY-MM-DD HH:MM:SS (ISO-like format, e.g., 2025-11-12 00:36:13)
	// - mon/dd/yyyy HH:MM:SS (month/day/year format, e.g., jan/02/2025 10:30:45)
	file.CreatedAt = parseRouterOSTime(normalized)

	return file, nil
}

// validateCreateVolumeOptions validates volume creation options
func validateCreateVolumeOptions(opts CreateVolumeOptions) error {
	if opts.Slot == "" {
		return fmt.Errorf("slot is required")
	}
	if err := validateSlotName(opts.Slot); err != nil {
		return err
	}
	if opts.FilePath == "" {
		return fmt.Errorf("file path is required")
	}

	// SECURITY: Validate file path to prevent command injection and path traversal
	if err := utils.ValidateFilePath(opts.FilePath); err != nil {
		return fmt.Errorf("security validation failed for file path: %w", err)
	}

	if opts.FileSizeBytes <= 0 {
		return fmt.Errorf("file size must be positive")
	}
	if opts.NVMETCPPort == 0 {
		opts.NVMETCPPort = 4420
	}
	if opts.NVMETCPNQN == "" {
		return fmt.Errorf("NVMe/TCP NQN is required")
	}
	return nil
}

// validateSlotName ensures slot name is safe (prevents command injection)
func validateSlotName(slot string) error {
	if slot == "" {
		return fmt.Errorf("slot name cannot be empty")
	}

	// Only allow alphanumeric and hyphen (pvc-<uuid> format)
	if !regexp.MustCompile(`^[a-zA-Z0-9-]+$`).MatchString(slot) {
		return fmt.Errorf("invalid slot name format: %s (only alphanumeric and hyphen allowed)", slot)
	}

	return nil
}

// formatBytes converts bytes to human-readable format (50G, 100G, 1T)
func formatBytes(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)

	switch {
	case bytes >= TB:
		return fmt.Sprintf("%dT", bytes/TB)
	case bytes >= GB:
		return fmt.Sprintf("%dG", bytes/GB)
	case bytes >= MB:
		return fmt.Sprintf("%dM", bytes/MB)
	case bytes >= KB:
		return fmt.Sprintf("%dK", bytes/KB)
	default:
		return fmt.Sprintf("%d", bytes)
	}
}

func routerOSMountParam(mount bool) string {
	if mount {
		return "mount=yes"
	}
	return "mount=no"
}

// parseSize converts human-readable size to bytes
func parseSize(value, unit string) (int64, error) {
	num, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}

	multiplier := int64(1)
	switch strings.ToUpper(unit) {
	case "KIB", "KB", "K":
		multiplier = 1024
	case "MIB", "MB", "M":
		multiplier = 1024 * 1024
	case "GIB", "GB", "G":
		multiplier = 1024 * 1024 * 1024
	case "TIB", "TB", "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	}

	return int64(num * float64(multiplier)), nil
}

// normalizeRouterOSOutput normalizes multi-line RouterOS output by joining continuation lines
// RouterOS CLI output often spans multiple lines with properties wrapped across lines.
// Continuation lines start with whitespace. This function joins them into a single line.
func normalizeRouterOSOutput(output string) string {
	lines := strings.Split(output, "\n")
	var normalized strings.Builder

	for _, line := range lines {
		// Remove \r if present (Windows-style line endings)
		line = strings.TrimRight(line, "\r")

		// Skip the "Flags:" header lines
		if strings.HasPrefix(line, "Flags:") || strings.Contains(line, "disabled") {
			continue
		}

		// If line starts with whitespace (continuation line), append to current line with space
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			normalized.WriteString(" ")
			normalized.WriteString(strings.TrimSpace(line))
		} else {
			// New entry - add newline before (except for first line)
			if normalized.Len() > 0 {
				normalized.WriteString("\n")
			}
			normalized.WriteString(line)
		}
	}

	return normalized.String()
}

// extractMountPoint extracts the mount point from a full path
// Examples:
//
//	/storage-pool/metal-csi/volumes → storage-pool
//	/nvme1/kubernetes → nvme1
//	storage-pool/volumes → storage-pool
func extractMountPoint(path string) string {
	// Remove leading slash if present
	path = strings.TrimPrefix(path, "/")

	// Split by slash and take first component
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		return parts[0]
	}

	return path
}

// parseRouterOSTime extracts and parses time from RouterOS output
// Handles multiple field names (creation-time, last-modified) and date formats
func parseRouterOSTime(normalized string) time.Time {
	// Try different field names that RouterOS might use
	fieldPatterns := []string{
		`creation-time=`,
		`last-modified=`,
	}

	for _, fieldPrefix := range fieldPatterns {
		// Try ISO-like format first: YYYY-MM-DD HH:MM:SS (e.g., 2025-11-12 00:36:13)
		isoPattern := fieldPrefix + `(\d{4}-\d{2}-\d{2}\s+\d+:\d+:\d+)`
		if match := regexp.MustCompile(isoPattern).FindStringSubmatch(normalized); len(match) > 1 {
			if t, err := time.Parse("2006-01-02 15:04:05", match[1]); err == nil {
				return t
			}
		}

		// Try RouterOS month/day/year format: mon/dd/yyyy HH:MM:SS (e.g., jan/02/2025 10:30:45)
		// Note: RouterOS uses lowercase month abbreviations, so we need to title-case the month
		// for Go's time.Parse which expects "Jan" format
		monthPattern := fieldPrefix + `([a-zA-Z]+/\d+/\d+\s+\d+:\d+:\d+)`
		if match := regexp.MustCompile(monthPattern).FindStringSubmatch(normalized); len(match) > 1 {
			// Title-case the month part for Go's time.Parse (e.g., "jan" -> "Jan", "nov" -> "Nov")
			timeStr := match[1]
			if len(timeStr) >= 3 {
				timeStr = strings.ToUpper(timeStr[:1]) + strings.ToLower(timeStr[1:3]) + timeStr[3:]
			}
			if t, err := time.Parse("Jan/02/2006 15:04:05", timeStr); err == nil {
				return t
			}
		}
	}

	// Return zero time if no match found
	return time.Time{}
}

// Snapshot operations

// CreateSnapshot creates a CoW copy of a volume disk entry on RDS using /disk add copy-from.
// The snapshot disk is NOT NVMe-exported (snapshots are immutable backing files only).
func (c *sshClient) CreateSnapshot(opts CreateSnapshotOptions) (*SnapshotInfo, error) {
	// Validate options
	if err := utils.ValidateSnapshotID(opts.Name); err != nil {
		return nil, fmt.Errorf("invalid snapshot name: %w", err)
	}
	if err := validateSlotName(opts.SourceVolume); err != nil {
		return nil, fmt.Errorf("invalid source volume: %w", err)
	}
	if opts.BasePath == "" {
		return nil, fmt.Errorf("base path is required for snapshot file placement")
	}

	// Get source volume info to verify it exists and determine file size
	sourceVol, err := c.GetVolume(opts.SourceVolume)
	if err != nil {
		return nil, fmt.Errorf("failed to get source volume %s: %w", opts.SourceVolume, err)
	}

	// Build snapshot file path: <basePath>/<snapshot-name>.img
	snapFilePath := fmt.Sprintf("%s/%s.img", opts.BasePath, opts.Name)

	// Build /disk add copy-from command.
	// - Reference source by slot name using [find slot=<name>] (slot is unique and validated).
	// - Omit file-size: copy-from determines size from source automatically.
	// - NO nvme-tcp-export, nvme-tcp-server-port, nvme-tcp-server-nqn (snapshots not NVMe-exported).
	cmd := fmt.Sprintf(
		`/disk add type=file copy-from=[find slot=%s] file-path=%s slot=%s mount=no`,
		opts.SourceVolume,
		snapFilePath,
		opts.Name,
	)

	// Execute command with retry
	_, err = c.runCommandWithRetry(cmd, 3)
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot: %w", err)
	}

	// Verify snapshot was created by retrieving it
	snapshot, err := c.GetSnapshot(opts.Name)
	if err != nil {
		return nil, fmt.Errorf("snapshot creation verification failed: %w", err)
	}

	// Ensure SourceVolume is populated (RDS may not echo it back)
	if snapshot.SourceVolume == "" {
		snapshot.SourceVolume = opts.SourceVolume
	}

	// Ensure FileSizeBytes is populated (use source volume size as ground truth)
	if snapshot.FileSizeBytes == 0 {
		snapshot.FileSizeBytes = sourceVol.FileSizeBytes
	}

	klog.V(2).Infof("Created snapshot %s from volume %s", opts.Name, opts.SourceVolume)
	klog.V(4).Infof("Created snapshot %s (source=%s, file=%s, size=%d)", opts.Name, opts.SourceVolume, snapFilePath, snapshot.FileSizeBytes)
	return snapshot, nil
}

// DeleteSnapshot removes a snapshot disk entry and its backing file from RDS.
// Idempotent: returns nil if snapshot does not exist (per CSI spec).
// Belt-and-suspenders: removes both the /disk entry AND the backing .img file.
func (c *sshClient) DeleteSnapshot(snapshotID string) error {
	// Validate snapshot ID
	if err := utils.ValidateSnapshotID(snapshotID); err != nil {
		return err
	}

	// Get snapshot info to find the backing file path (for file cleanup)
	snapshot, err := c.GetSnapshot(snapshotID)
	if err != nil {
		var notFoundErr *SnapshotNotFoundError
		if errors.As(err, &notFoundErr) {
			klog.V(4).Infof("Snapshot %s already deleted (not found)", snapshotID)
			return nil // Idempotent: not found = success
		}
		return fmt.Errorf("failed to check snapshot existence: %w", err)
	}

	filePath := snapshot.FilePath

	// Step 1: Remove the disk entry
	cmd := fmt.Sprintf(`/disk remove [find slot=%s]`, snapshotID)
	_, err = c.runCommandWithRetry(cmd, 3)
	if err != nil {
		// Idempotent: treat "no such item" as success
		if strings.Contains(err.Error(), "no such item") {
			klog.V(4).Infof("Snapshot %s disk entry does not exist, continuing to file cleanup", snapshotID)
		} else {
			return fmt.Errorf("failed to remove snapshot disk entry: %w", err)
		}
	}
	klog.V(4).Infof("Removed disk entry for snapshot %s", snapshotID)

	// Step 2: Delete the backing file (belt and suspenders)
	if filePath != "" {
		if err := c.DeleteFile(filePath); err != nil {
			// Log warning but don't fail - disk slot is already removed
			// Orphan reconciler can clean up the file later if needed
			klog.Warningf("Failed to delete backing file %s for snapshot %s: %v", filePath, snapshotID, err)
		} else {
			klog.V(4).Infof("Deleted backing file %s for snapshot %s", filePath, snapshotID)
		}
	}

	klog.V(2).Infof("Deleted snapshot %s", snapshotID)
	return nil
}

// GetSnapshot retrieves information about a specific snapshot using /disk print.
func (c *sshClient) GetSnapshot(snapshotID string) (*SnapshotInfo, error) {
	klog.V(4).Infof("Getting snapshot info for %s", snapshotID)

	// Validate snapshot ID
	if err := utils.ValidateSnapshotID(snapshotID); err != nil {
		return nil, err
	}

	// Build /disk print command (same format as GetVolume, but for snapshots)
	cmd := fmt.Sprintf(`/disk print detail where slot=%s`, snapshotID)

	// Execute command
	output, err := c.runCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to get snapshot info: %w", err)
	}

	// Normalize output and check if snapshot exists
	normalized := normalizeRouterOSOutput(output)
	if strings.TrimSpace(normalized) == "" {
		return nil, &SnapshotNotFoundError{Name: snapshotID}
	}

	// Parse snapshot info using /disk print output format
	snapshot, err := parseSnapshotInfo(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse snapshot info: %w", err)
	}

	// Additional check: if name is empty, snapshot wasn't found
	if snapshot.Name == "" {
		return nil, &SnapshotNotFoundError{Name: snapshotID}
	}

	return snapshot, nil
}

// ListSnapshots lists all CSI-managed snapshots (snap-* prefix) on RDS.
// Uses /disk print with slot prefix filter to enumerate snapshot disk entries.
func (c *sshClient) ListSnapshots() ([]SnapshotInfo, error) {
	klog.V(4).Info("Listing all snapshots")

	// Use slot~ prefix match to find all snap-* entries
	cmd := `/disk print detail where slot~"snap-"`

	// Execute command
	output, err := c.runCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to list snapshots: %w", err)
	}

	// Parse all snapshots
	snapshots, err := parseSnapshotList(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse snapshot list: %w", err)
	}

	return snapshots, nil
}

// RestoreSnapshot creates a new NVMe-exported volume from a snapshot using /disk add copy-from.
// The restored volume is an independent writable copy — modifying it does not affect the snapshot.
func (c *sshClient) RestoreSnapshot(snapshotID string, newVolumeOpts CreateVolumeOptions) error {
	// Validate snapshot ID
	if err := utils.ValidateSnapshotID(snapshotID); err != nil {
		return err
	}

	// Validate new volume options
	if err := validateCreateVolumeOptions(newVolumeOpts); err != nil {
		return fmt.Errorf("invalid volume options: %w", err)
	}

	// Verify snapshot exists
	_, err := c.GetSnapshot(snapshotID)
	if err != nil {
		return fmt.Errorf("snapshot not found: %w", err)
	}

	klog.V(4).Infof("Restoring snapshot %s to new volume %s", snapshotID, newVolumeOpts.Slot)

	// Create new NVMe-exported volume using /disk add copy-from.
	// This is essentially CreateVolume but with copy-from to populate data from the snapshot.
	// file-size is included to allow larger-than-snapshot restores (per CSI spec).
	sizeStr := formatBytes(newVolumeOpts.FileSizeBytes)
	cmd := fmt.Sprintf(
		`/disk add type=file copy-from=[find slot=%s] file-path=%s file-size=%s slot=%s %s nvme-tcp-export=yes nvme-tcp-server-port=%d nvme-tcp-server-nqn=%s`,
		snapshotID,
		newVolumeOpts.FilePath,
		sizeStr,
		newVolumeOpts.Slot,
		routerOSMountParam(newVolumeOpts.Mount),
		newVolumeOpts.NVMETCPPort,
		newVolumeOpts.NVMETCPNQN,
	)

	_, err = c.runCommandWithRetry(cmd, 3)
	if err != nil {
		return fmt.Errorf("failed to restore snapshot to new volume: %w", err)
	}

	// Verify restored volume exists
	if err := c.VerifyVolumeExists(newVolumeOpts.Slot); err != nil {
		return fmt.Errorf("restore verification failed: %w", err)
	}

	klog.V(2).Infof("Restored snapshot %s to new volume %s", snapshotID, newVolumeOpts.Slot)
	return nil
}

// GetDiskMetrics retrieves real-time disk performance metrics via /disk monitor-traffic
// Uses "once" modifier to get a single snapshot instead of continuous stream output.
// The slot parameter is the disk slot name (e.g., "storage-pool") or disk number.
func (c *sshClient) GetDiskMetrics(slot string) (*DiskMetrics, error) {
	klog.V(4).Infof("Getting disk metrics for %s", slot)

	// Validate slot name to prevent command injection
	if err := validateSlotName(slot); err != nil {
		return nil, err
	}

	// Use "once" to get snapshot, not continuous stream
	// Continuous output uses terminal control sequences that break parsing
	cmd := fmt.Sprintf(`/disk monitor-traffic %s once`, slot)

	output, err := c.runCommand(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to get disk metrics: %w", err)
	}

	metrics, err := parseDiskMetrics(output)
	if err != nil {
		return nil, fmt.Errorf("failed to parse disk metrics: %w", err)
	}

	metrics.Slot = slot
	return metrics, nil
}

// parseDiskMetrics parses /disk monitor-traffic output into DiskMetrics
// Expected format (from RouterOS):
//
//	slot: storage-pool
//	read-ops-per-second:               0
//	write-ops-per-second:             76
//	read-rate:            0bps
//	write-rate:        12.8Mbps
//	read-time:             0ms
//	write-time:             0ms
//	in-flight-ops:               0
//	active-time:             0ms
//	wait-time:             0ms
func parseDiskMetrics(output string) (*DiskMetrics, error) {
	metrics := &DiskMetrics{}

	// Parse integer fields (IOPS, in-flight-ops)
	intFields := map[string]*float64{
		`read-ops-per-second:\s+(\d+)`:  &metrics.ReadOpsPerSecond,
		`write-ops-per-second:\s+(\d+)`: &metrics.WriteOpsPerSecond,
		`in-flight-ops:\s+(\d+)`:        &metrics.InFlightOps,
	}

	for pattern, field := range intFields {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(output); len(matches) > 1 {
			value, err := strconv.ParseFloat(matches[1], 64)
			if err == nil {
				*field = value
			}
		}
	}

	// Parse rate fields with units (e.g., "0bps", "12.8Mbps", "1.5Gbps")
	rateFields := map[string]*float64{
		`read-rate:\s+([\d.]+)\s*(\w+)`:  &metrics.ReadBytesPerSec,
		`write-rate:\s+([\d.]+)\s*(\w+)`: &metrics.WriteBytesPerSec,
	}

	for pattern, field := range rateFields {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(output); len(matches) > 2 {
			value, err := strconv.ParseFloat(matches[1], 64)
			if err == nil {
				*field = convertRateToBytesPerSec(value, matches[2])
			}
		}
	}

	// Parse time fields (ms suffix)
	timeFields := map[string]*float64{
		`read-time:\s+([\d.]+)\s*ms`:   &metrics.ReadTimeMs,
		`write-time:\s+([\d.]+)\s*ms`:  &metrics.WriteTimeMs,
		`wait-time:\s+([\d.]+)\s*ms`:   &metrics.WaitTimeMs,
		`active-time:\s+([\d.]+)\s*ms`: &metrics.ActiveTimeMs,
	}

	for pattern, field := range timeFields {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(output); len(matches) > 1 {
			value, err := strconv.ParseFloat(matches[1], 64)
			if err == nil {
				*field = value
			}
		}
	}

	return metrics, nil
}

// convertRateToBytesPerSec converts RouterOS rate units to bytes per second
// RouterOS reports rates in bits per second with unit suffixes: bps, kbps, Mbps, Gbps
func convertRateToBytesPerSec(value float64, unit string) float64 {
	switch unit {
	case "bps":
		return value / 8
	case "kbps", "Kbps":
		return (value * 1000) / 8
	case "Mbps":
		return (value * 1_000_000) / 8
	case "Gbps":
		return (value * 1_000_000_000) / 8
	default:
		return value
	}
}

// parseSnapshotInfo parses RouterOS /disk print detail output for a single snapshot entry.
// Snapshot entries have the same key=value format as volume entries but WITHOUT nvme-tcp-export
// fields (snapshots are not NVMe-exported). Source volume lineage is recovered from the slot name.
func parseSnapshotInfo(output string) (*SnapshotInfo, error) {
	snapshot := &SnapshotInfo{}

	// Normalize multi-line output (same as parseVolumeInfo)
	normalized := normalizeRouterOSOutput(output)

	// Extract slot name (used as snapshot Name)
	if match := regexp.MustCompile(`slot="([^"]+)"`).FindStringSubmatch(normalized); len(match) > 1 {
		snapshot.Name = match[1]
	} else if match := regexp.MustCompile(`slot=([^\s]+)`).FindStringSubmatch(normalized); len(match) > 1 {
		snapshot.Name = match[1]
	}

	// Extract file-path (backing file on RDS)
	if match := regexp.MustCompile(`file-path="([^"]+)"`).FindStringSubmatch(normalized); len(match) > 1 {
		snapshot.FilePath = match[1]
	} else if match := regexp.MustCompile(`file-path=(\S+\.img)`).FindStringSubmatch(normalized); len(match) > 1 {
		snapshot.FilePath = match[1]
	} else if match := regexp.MustCompile(`file-path=(\S+\s+\S+\.img)`).FindStringSubmatch(normalized); len(match) > 1 {
		// Path was split across lines and joined with space after normalization
		snapshot.FilePath = strings.ReplaceAll(match[1], " ", "")
	}
	// Normalize to absolute path
	if snapshot.FilePath != "" && !strings.HasPrefix(snapshot.FilePath, "/") {
		snapshot.FilePath = "/" + snapshot.FilePath
	}

	// Extract file-size (human-readable format like "50.0GiB")
	if match := regexp.MustCompile(`file-size=([\d.]+)\s*([KMGT]i?B)`).FindStringSubmatch(normalized); len(match) > 2 {
		if bytes, err := parseSize(match[1], match[2]); err == nil {
			snapshot.FileSizeBytes = bytes
		}
	} else {
		// Fallback: try raw size field (with spaces removed)
		if match := regexp.MustCompile(`file-size=(\d+)`).FindStringSubmatch(normalized); len(match) > 1 {
			if size, err := strconv.ParseInt(match[1], 10, 64); err == nil {
				snapshot.FileSizeBytes = size
			}
		}
	}

	// Extract source-volume if present in the output.
	// The mock server always emits this field; real RouterOS /disk print does not.
	// NOTE: The snapshot slot name (snap-<uuid5-of-csiName>-at-<suffix>) no longer embeds
	// the source volume UUID — the UUID is derived from the CSI name, not the source.
	// Therefore the slot name cannot be used to recover the source volume ID.
	if match := regexp.MustCompile(`source-volume="([^"]+)"`).FindStringSubmatch(normalized); len(match) > 1 {
		snapshot.SourceVolume = match[1]
	} else if match := regexp.MustCompile(`source-volume=([^\s]+)`).FindStringSubmatch(normalized); len(match) > 1 {
		snapshot.SourceVolume = match[1]
	}

	// Extract creation time from creation-time= field in disk output.
	// No fallback — the slot name suffix is a name-derived hash (not a Unix timestamp)
	// for deterministic snapshot IDs, so ExtractTimestampFromSnapshotID would fail.
	snapshot.CreatedAt = parseRouterOSTime(normalized)

	return snapshot, nil
}

// parseSnapshotList parses RouterOS /disk print detail output for multiple snapshot entries.
// Only entries with slot names starting with "snap-" are included in the result.
func parseSnapshotList(output string) ([]SnapshotInfo, error) {
	var snapshots []SnapshotInfo

	// Split by disk entries (each starts with a number like "0  " or " 1  ")
	entries := regexp.MustCompile(`(?m)^\s*\d+\s+`).Split(output, -1)

	for _, entry := range entries {
		if strings.TrimSpace(entry) == "" {
			continue
		}

		snapshot, err := parseSnapshotInfo(entry)
		if err != nil {
			klog.V(4).Infof("Skipping unparseable snapshot entry: %v", err)
			continue
		}

		// Only include entries with snap- prefix (filter out non-snapshot disk entries)
		if strings.HasPrefix(snapshot.Name, utils.SnapshotIDPrefix) {
			snapshots = append(snapshots, *snapshot)
		}
	}

	// Return empty slice (not nil) if no snapshots found
	if snapshots == nil {
		return []SnapshotInfo{}, nil
	}

	return snapshots, nil
}
