package mount

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

// Dangerous mount options that should never be allowed
var dangerousMountOptions = map[string]bool{
	"suid": true, // Allow set-user-ID/set-group-ID bits
	"dev":  true, // Interpret character or block special devices
	"exec": true, // Permit execution of binaries
}

// AllowExecOption is a driver-internal marker, never passed to mount(8): a bind mount that carries
// it is published without the noexec default (nosuid and nodev stay). The node server adds it only
// for a volume whose StorageClass sets allowExec: "true" (VolumeContextAllowExec); it is the one
// way to get exec, since "exec" itself stays a dangerous option.
const AllowExecOption = "x-nvmet-csi.allow-exec"

// Default secure mount options enforced for bind mounts
var defaultSecureMountOptions = []string{
	"nosuid", // Ignore set-user-ID/set-group-ID bits
	"nodev",  // Do not interpret character or block special devices
	"noexec", // Do not allow direct execution of binaries
}

// Whitelist of allowed mount options (beyond the defaults)
var allowedMountOptions = map[string]bool{
	// Security options
	"nosuid":     true,
	"nodev":      true,
	"noexec":     true,
	"ro":         true,
	"rw":         true,
	"relatime":   true,
	"noatime":    true,
	"nodiratime": true,

	// Filesystem-specific options that are generally safe
	"defaults": true,
	"sync":     true,
	"async":    true,
	"auto":     true,
	"noauto":   true,
	"user":     true,
	"nouser":   true,
	"_netdev":  true,

	// Bind mount options
	"bind":    true,
	"rbind":   true,
	"remount": true,

	// Additional safe options
	"strictatime": true,
	"lazytime":    true,
	"nolazytime":  true,
}

// Mounter handles filesystem operations
type Mounter interface {
	// Mount mounts source to target with the given fsType and options
	Mount(source, target, fsType string, options []string) error

	// Unmount unmounts the target
	Unmount(target string) error

	// IsLikelyMountPoint checks if a path is a mount point
	IsLikelyMountPoint(path string) (bool, error)

	// Format formats the device with the given filesystem type
	Format(device, fsType string) error

	// IsFormatted checks if device has a filesystem
	IsFormatted(device string) (bool, error)

	// ResizeFilesystem resizes the filesystem on the device to use available space
	ResizeFilesystem(device, volumePath string) error

	// GetDeviceStats returns filesystem statistics
	GetDeviceStats(path string) (*DeviceStats, error)

	// ForceUnmount attempts normal unmount, then escalates to lazy unmount if needed
	// Returns error if mount is in use (refuses to force unmount in-use mounts)
	ForceUnmount(target string, timeout time.Duration) error

	// IsMountInUse checks if any processes have open file handles under the mount path
	// Returns (inUse bool, pids []int, err error)
	IsMountInUse(path string) (bool, []int, error)

	// MakeFile creates an empty file at the given path
	// Used for block volume target paths where target must be a file, not directory
	MakeFile(pathname string) error
}

// DeviceStats represents filesystem statistics
type DeviceStats struct {
	// Total size in bytes
	TotalBytes int64

	// Used bytes
	UsedBytes int64

	// Available bytes
	AvailableBytes int64

	// Total inodes
	TotalInodes int64

	// Used inodes
	UsedInodes int64

	// Available inodes
	AvailableInodes int64
}

// mounter implements Mounter interface using system commands
type mounter struct {
	execCommand func(name string, args ...string) *exec.Cmd
}

// NewMounter creates a new filesystem mounter
func NewMounter() Mounter {
	return &mounter{
		execCommand: exec.Command,
	}
}

// ValidateMountOptions validates mount options against security policies
// Returns an error if any dangerous options are found or if options are not whitelisted
func ValidateMountOptions(options []string) error {
	if len(options) == 0 {
		// No options is safe
		return nil
	}

	for _, opt := range options {
		// Remove value if option has one (e.g., "uid=1000" -> "uid")
		optName := strings.Split(opt, "=")[0]

		// SECURITY: Check for dangerous options
		if dangerousMountOptions[optName] {
			return fmt.Errorf("dangerous mount option not allowed: %s", optName)
		}

		// Check if option is in whitelist
		if !allowedMountOptions[optName] {
			return fmt.Errorf("mount option not in whitelist: %s", optName)
		}
	}

	return nil
}

// SanitizeMountOptions adds default secure options and validates user-provided options
// For bind mounts, always enforces nosuid, nodev, noexec unless explicitly overridden
func SanitizeMountOptions(options []string, isBindMount bool) ([]string, error) {
	// The allow-exec marker is the driver's own, not an option for mount(8): take it out first.
	allowExec := false
	kept := make([]string, 0, len(options))
	for _, opt := range options {
		if opt == AllowExecOption {
			allowExec = true
			continue
		}
		kept = append(kept, opt)
	}
	options = kept

	// Validate provided options first
	if err := ValidateMountOptions(options); err != nil {
		return nil, err
	}

	// For bind mounts, enforce secure defaults
	if isBindMount {
		// Create a set to track existing options
		existingOpts := make(map[string]bool)
		for _, opt := range options {
			optName := strings.Split(opt, "=")[0]
			existingOpts[optName] = true
		}

		// Add secure defaults if not already present
		secureOpts := make([]string, 0, len(options)+len(defaultSecureMountOptions))
		for _, secureOpt := range defaultSecureMountOptions {
			if secureOpt == "noexec" && allowExec {
				continue
			}
			// Don't add if user explicitly specified the opposite
			opposite := ""
			switch secureOpt {
			case "nosuid":
				opposite = "suid"
			case "nodev":
				opposite = "dev"
			case "noexec":
				opposite = "exec"
			}

			if !existingOpts[secureOpt] && !existingOpts[opposite] {
				secureOpts = append(secureOpts, secureOpt)
			}
		}

		// Combine secure options with user options
		options = append(secureOpts, options...)
	}

	return options, nil
}

// Mount mounts source to target with the given filesystem type and options
func (m *mounter) Mount(source, target, fsType string, options []string) error {
	klog.V(4).Infof("Mounting %s to %s (fsType: %s, options: %v)", source, target, fsType, options)

	// MOUNT STORM PROTECTION: Check for excessive duplicate mounts BEFORE attempting mount
	// This prevents adding to an existing mount storm and wedging the node
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mounts, err := GetMountsWithTimeout(ctx)
	if err != nil {
		// If we can't read mounts, log warning but proceed (don't block legitimate operations)
		klog.Warningf("Failed to check for mount storm before mounting %s: %v (proceeding)", source, err)
	} else {
		// Check if source device already has too many mounts
		count, stormErr := DetectDuplicateMounts(mounts, source)
		if stormErr != nil {
			// Mount storm detected - refuse to proceed
			klog.Errorf("MOUNT STORM DETECTED: refusing to mount %s to %s: %v", source, target, stormErr)
			return fmt.Errorf("mount operation aborted due to mount storm: %w", stormErr)
		}
		if count > 0 {
			klog.V(4).Infof("Pre-mount check: %s has %d existing mount(s)", source, count)
		}
	}

	// SECURITY: Validate and sanitize mount options
	// Detect if this is a bind mount
	isBindMount := false
	for _, opt := range options {
		if opt == "bind" || opt == "rbind" {
			isBindMount = true
			break
		}
	}

	sanitizedOptions, err := SanitizeMountOptions(options, isBindMount)
	if err != nil {
		return fmt.Errorf("mount options validation failed: %w", err)
	}
	options = sanitizedOptions

	klog.V(4).Infof("Sanitized mount options: %v", options)

	// Create target directory if it doesn't exist (unless target is already a file for block volumes)
	if stat, err := os.Stat(target); err != nil {
		// Target doesn't exist - create directory
		if os.IsNotExist(err) {
			if err := os.MkdirAll(target, 0750); err != nil {
				return fmt.Errorf("failed to create target directory: %w", err)
			}
		} else {
			return fmt.Errorf("failed to stat target: %w", err)
		}
	} else if stat.IsDir() {
		// Target exists and is a directory - this is fine
		klog.V(4).Infof("Target %s is an existing directory", target)
	} else {
		// Target exists and is a file - this is expected for block volumes
		klog.V(4).Infof("Target %s is an existing file (block volume)", target)
	}

	// Build mount command arguments
	args := []string{}

	// Add filesystem type if specified
	if fsType != "" {
		args = append(args, "-t", fsType)
	}

	// Add mount options if specified
	if len(options) > 0 {
		args = append(args, "-o", strings.Join(options, ","))
	}

	// Add source and target
	args = append(args, source, target)

	// Execute mount command
	cmd := m.execCommand("mount", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mount failed: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("mount output: %s", string(output))
	klog.V(2).Infof("Mounted %s to %s", source, target)
	return nil
}

// Unmount unmounts the target path
func (m *mounter) Unmount(target string) error {
	klog.V(4).Infof("Unmounting %s", target)

	// Check if it's actually mounted
	mounted, err := m.IsLikelyMountPoint(target)
	if err != nil {
		return fmt.Errorf("failed to check if mounted: %w", err)
	}

	if !mounted {
		klog.V(2).Infof("Path %s is not mounted, nothing to unmount", target)
		return nil
	}

	// Execute umount command
	cmd := m.execCommand("umount", target)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("umount failed: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("umount output: %s", string(output))
	klog.V(2).Infof("Unmounted %s", target)
	return nil
}

// IsLikelyMountPoint checks if a path is a mount point
func (m *mounter) IsLikelyMountPoint(path string) (bool, error) {
	// Check if path exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	}

	// Use findmnt to check if path is a mount point
	cmd := m.execCommand("findmnt", "-o", "TARGET", "-n", "-M", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// findmnt returns non-zero if not a mount point
		if strings.Contains(string(output), "not found") {
			return false, nil
		}
		klog.V(5).Infof("findmnt failed: %v, output: %s", err, string(output))
		return false, nil
	}

	// If findmnt succeeded and returned output, it's a mount point
	return len(output) > 0, nil
}

// Format formats a device with the specified filesystem type
func (m *mounter) Format(device, fsType string) error {
	klog.V(4).Infof("Formatting device %s with %s", device, fsType)

	// Check if already formatted
	formatted, err := m.IsFormatted(device)
	if err != nil {
		return fmt.Errorf("failed to check if device is formatted: %w", err)
	}

	if formatted {
		klog.V(4).Infof("Device %s is already formatted, skipping", device)
		return nil
	}

	// Log the format decision for audit trail
	klog.V(2).Infof("Format: device %s confirmed unformatted by blkid, proceeding with mkfs.%s", device, fsType)

	// Build mkfs command based on filesystem type
	var cmd *exec.Cmd
	switch fsType {
	case "ext4":
		// mkfs.ext4 -F (force) device
		cmd = m.execCommand("mkfs.ext4", "-F", device)
	case "ext3":
		cmd = m.execCommand("mkfs.ext3", "-F", device)
	case "xfs":
		cmd = m.execCommand("mkfs.xfs", "-f", device)
	default:
		return fmt.Errorf("unsupported filesystem type: %s", fsType)
	}

	// Execute mkfs command
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("mkfs.%s failed: %w, output: %s", fsType, err, string(output))
	}

	klog.V(4).Infof("mkfs output: %s", string(output))
	klog.V(2).Infof("Formatted %s with %s", device, fsType)
	return nil
}

// IsFormatted checks if a device has a filesystem
func (m *mounter) IsFormatted(device string) (bool, error) {
	// Use blkid to check for filesystem
	cmd := m.execCommand("blkid", "-o", "value", "-s", "TYPE", device)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// Parse exit code to distinguish between different blkid failure modes:
		// - Exit 0: success, filesystem found
		// - Exit 2: no filesystem found (definitive - safe to format)
		// - Exit 1: device error (I/O error, device not ready, device not found)
		//           CRITICAL: Do NOT treat as "not formatted" - would cause data loss
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			switch exitErr.ExitCode() {
			case 2:
				// blkid exit 2 = no filesystem found on device
				klog.V(4).Infof("IsFormatted: device %s has no filesystem (blkid exit 2)", device)
				return false, nil
			case 1:
				// blkid exit 1 = device error (I/O error, device not found, device not ready)
				// CRITICAL: Do NOT treat this as "not formatted" - this would cause data loss
				klog.Warningf("IsFormatted: blkid cannot read device %s (exit 1, output: %q) - device may not be ready", device, string(output))
				return false, fmt.Errorf("blkid cannot read device %s (exit status 1): device may not be ready or has I/O errors", device)
			default:
				return false, fmt.Errorf("blkid failed on %s with exit code %d: %w", device, exitErr.ExitCode(), err)
			}
		}
		return false, fmt.Errorf("blkid failed: %w", err)
	}

	// If blkid returned a filesystem type, device is formatted
	fsType := strings.TrimSpace(string(output))
	if len(fsType) > 0 {
		klog.V(4).Infof("IsFormatted: device %s has filesystem type %s", device, fsType)
		return true, nil
	}
	return false, nil
}

// ResizeFilesystem resizes the filesystem on the device to use available space
func (m *mounter) ResizeFilesystem(device, volumePath string) error {
	klog.V(4).Infof("Resizing filesystem on device %s (volume path: %s)", device, volumePath)

	// Detect filesystem type using blkid
	cmd := m.execCommand("blkid", "-o", "value", "-s", "TYPE", device)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to detect filesystem type: %w, output: %s", err, string(output))
	}

	fsType := strings.TrimSpace(string(output))
	if fsType == "" {
		return fmt.Errorf("could not detect filesystem type for device %s", device)
	}

	klog.V(4).Infof("Detected filesystem type: %s", fsType)

	// Execute appropriate resize command based on filesystem type
	var resizeCmd *exec.Cmd
	switch fsType {
	case "ext4", "ext3", "ext2":
		// resize2fs works for ext2/ext3/ext4
		// It can be run on mounted filesystems
		resizeCmd = m.execCommand("resize2fs", device)
	case "xfs":
		// xfs_growfs requires the mount point, not the device
		// It must be run on a mounted filesystem
		if volumePath == "" {
			return fmt.Errorf("volume path is required for xfs filesystem resize")
		}
		resizeCmd = m.execCommand("xfs_growfs", volumePath)
	default:
		return fmt.Errorf("unsupported filesystem type for resize: %s", fsType)
	}

	// Execute resize command
	output, err = resizeCmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("filesystem resize failed: %w, output: %s", err, string(output))
	}

	klog.V(4).Infof("resize output: %s", string(output))
	klog.V(2).Infof("Resized filesystem on %s", device)
	return nil
}

// GetDeviceStats returns filesystem statistics for the given path
func (m *mounter) GetDeviceStats(path string) (*DeviceStats, error) {
	// Use df to get filesystem statistics
	cmd := m.execCommand("df", "--output=size,used,avail,itotal,iused,iavail", "-B1", path)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("df failed: %w, output: %s", err, string(output))
	}

	// Parse df output
	// Format: Size Used Avail Inodes IUsed IFree
	// Skip header line
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("unexpected df output: %s", string(output))
	}

	fields := strings.Fields(lines[1])
	if len(fields) < 6 {
		return nil, fmt.Errorf("unexpected df output format: %s", lines[1])
	}

	stats := &DeviceStats{}

	// Parse size fields
	_, _ = fmt.Sscanf(fields[0], "%d", &stats.TotalBytes)
	_, _ = fmt.Sscanf(fields[1], "%d", &stats.UsedBytes)
	_, _ = fmt.Sscanf(fields[2], "%d", &stats.AvailableBytes)

	// Parse inode fields
	_, _ = fmt.Sscanf(fields[3], "%d", &stats.TotalInodes)
	_, _ = fmt.Sscanf(fields[4], "%d", &stats.UsedInodes)
	_, _ = fmt.Sscanf(fields[5], "%d", &stats.AvailableInodes)

	return stats, nil
}

// IsMountInUse checks if any processes have open file handles under the mount path
// Returns (inUse bool, pids []int, err error)
func (m *mounter) IsMountInUse(path string) (bool, []int, error) {
	klog.V(4).Infof("Checking if mount %s is in use", path)

	// Resolve mount path to canonical form
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		// If path doesn't exist, it's not in use
		if os.IsNotExist(err) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("failed to resolve symlinks for %s: %w", path, err)
	}

	// Ensure canonical path ends without trailing slash for prefix matching
	canonicalPath = strings.TrimSuffix(canonicalPath, "/")

	var pidsWithOpenFiles []int

	// Scan /proc directory for all PIDs
	procDir, err := os.Open("/proc")
	if err != nil {
		return false, nil, fmt.Errorf("failed to open /proc: %w", err)
	}
	defer procDir.Close()

	entries, err := procDir.Readdirnames(-1)
	if err != nil {
		return false, nil, fmt.Errorf("failed to read /proc: %w", err)
	}

	for _, entry := range entries {
		// Skip non-numeric entries (only interested in PIDs)
		pid, err := strconv.Atoi(entry)
		if err != nil {
			continue
		}

		// Check if this PID has open files under the mount path
		fdPath := fmt.Sprintf("/proc/%d/fd", pid)
		fdDir, err := os.Open(fdPath)
		if err != nil {
			// Permission denied is expected for processes of other users
			// Also expected if process exited while we're scanning
			continue
		}

		fdEntries, err := fdDir.Readdirnames(-1)
		fdDir.Close()
		if err != nil {
			continue
		}

		// Check each file descriptor
		for _, fdEntry := range fdEntries {
			fdLink := filepath.Join(fdPath, fdEntry)
			target, err := os.Readlink(fdLink)
			if err != nil {
				continue
			}

			// Check if target is under the mount path
			if target == canonicalPath || strings.HasPrefix(target, canonicalPath+"/") {
				klog.V(4).Infof("Process %d has open file handle: %s", pid, target)
				pidsWithOpenFiles = append(pidsWithOpenFiles, pid)
				break // Found at least one open file for this PID, no need to check more
			}
		}
	}

	inUse := len(pidsWithOpenFiles) > 0
	if inUse {
		klog.V(2).Infof("Mount %s is in use by %d process(es): %v", path, len(pidsWithOpenFiles), pidsWithOpenFiles)
	} else {
		klog.V(4).Infof("Mount %s is not in use", path)
	}

	return inUse, pidsWithOpenFiles, nil
}

// MakeFile creates an empty file at the given path
// Used for block volume target paths where target must be a file, not directory
func (m *mounter) MakeFile(pathname string) error {
	klog.V(4).Infof("Creating file at %s", pathname)

	// Create parent directory if needed
	parent := filepath.Dir(pathname)
	if err := os.MkdirAll(parent, 0750); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	// Create empty file - use O_CREATE|O_EXCL for atomic creation
	f, err := os.OpenFile(pathname, os.O_CREATE|os.O_EXCL, 0640)
	if err != nil {
		if os.IsExist(err) {
			// File already exists - this is OK for idempotency
			klog.V(4).Infof("File %s already exists", pathname)
			return nil
		}
		return fmt.Errorf("failed to create file: %w", err)
	}
	f.Close()

	klog.V(4).Infof("Successfully created file at %s", pathname)
	return nil
}

// ForceUnmount attempts normal unmount, then escalates to lazy unmount if needed
// Returns error if mount is in use (refuses to force unmount in-use mounts)
func (m *mounter) ForceUnmount(target string, timeout time.Duration) error {
	klog.V(4).Infof("ForceUnmount: attempting to unmount %s with timeout %v", target, timeout)

	// Try normal unmount first
	err := m.Unmount(target)
	if err == nil {
		klog.V(2).Infof("ForceUnmount: unmounted %s", target)
		return nil
	}

	klog.V(4).Infof("ForceUnmount: normal unmount failed for %s: %v, waiting for mount to clear", target, err)

	// Wait for mount to clear with polling
	startTime := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Check if still mounted
			mounted, err := m.IsLikelyMountPoint(target)
			if err != nil {
				klog.V(4).Infof("ForceUnmount: error checking if mounted: %v", err)
			}
			if !mounted {
				klog.V(2).Infof("ForceUnmount: mount cleared for %s", target)
				return nil
			}

			// Check if timeout exceeded
			if time.Since(startTime) >= timeout {
				klog.V(4).Infof("ForceUnmount: timeout exceeded for %s, escalating to lazy unmount", target)
				goto escalate
			}

		case <-time.After(timeout):
			goto escalate
		}
	}

escalate:
	// Check if mount is in use before forcing
	inUse, pids, err := m.IsMountInUse(target)
	if err != nil {
		klog.Warningf("ForceUnmount: failed to check if mount is in use: %v", err)
		// Continue with lazy unmount despite error
	}

	if inUse {
		return fmt.Errorf("refusing to force unmount %s: mount is in use by processes: %v", target, pids)
	}

	// Execute lazy unmount (umount -l)
	klog.Warningf("ForceUnmount: escalating to lazy unmount for %s", target)
	cmd := m.execCommand("umount", "-l", target)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("lazy unmount failed for %s: %w, output: %s", target, err, string(output))
	}

	klog.V(4).Infof("ForceUnmount: lazy unmount output: %s", string(output))
	klog.Warningf("ForceUnmount: lazy unmount succeeded for %s", target)
	return nil
}
