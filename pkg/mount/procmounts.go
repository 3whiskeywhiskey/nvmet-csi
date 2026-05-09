package mount

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/moby/sys/mountinfo"
	"k8s.io/klog/v2"
)

const (
	// ProcmountsTimeout is the maximum time to wait for /proc/mounts parsing
	ProcmountsTimeout = 10 * time.Second

	// MaxDuplicateMountsPerDevice is the threshold for mount storm detection
	MaxDuplicateMountsPerDevice = 100
)

// MountInfo represents a single mount point entry from /proc/self/mountinfo
type MountInfo struct {
	// Source is the device or source path (field 10)
	Source string

	// Target is the mount point path (field 5)
	Target string

	// FSType is the filesystem type (field 9)
	FSType string

	// Options are the mount options (field 6)
	Options string
}

// GetMounts parses /proc/self/mountinfo and returns all mount points.
// Deprecated: Use GetMountsWithTimeout for production code to prevent hangs.
//
// Format: ID PARENT_ID MAJOR:MINOR ROOT MOUNT_POINT OPTIONS OPTIONAL_FIELDS - FSTYPE SOURCE SUPER_OPTIONS
// Example: 36 35 0:34 / /sys/fs/cgroup/memory rw,nosuid,nodev,noexec,relatime - cgroup cgroup rw,memory
func GetMounts() ([]MountInfo, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("failed to open /proc/self/mountinfo: %w", err)
	}
	defer file.Close()

	var mounts []MountInfo
	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		line := scanner.Text()
		klog.V(6).Infof("Parsing mountinfo line: %s", line)

		mountInfo, err := parseMountInfoLine(line)
		if err != nil {
			klog.V(4).Infof("Failed to parse mountinfo line: %v", err)
			continue
		}

		mounts = append(mounts, mountInfo)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading /proc/self/mountinfo: %w", err)
	}

	klog.V(6).Infof("Parsed %d mount points from /proc/self/mountinfo", len(mounts))
	return mounts, nil
}

// parseMountInfoLine parses a single line from /proc/self/mountinfo
func parseMountInfoLine(line string) (MountInfo, error) {
	// Split line into fields
	fields := strings.Fields(line)
	if len(fields) < 10 {
		return MountInfo{}, fmt.Errorf("invalid mountinfo line: expected at least 10 fields, got %d", len(fields))
	}

	// Find the separator "-" which separates the first part from the filesystem info
	separatorIndex := -1
	for i, field := range fields {
		if field == "-" {
			separatorIndex = i
			break
		}
	}

	if separatorIndex == -1 || separatorIndex+2 >= len(fields) {
		return MountInfo{}, fmt.Errorf("invalid mountinfo line: missing separator '-'")
	}

	// Extract fields (0-indexed):
	// 4: mount point (target)
	// 5: mount options
	// separatorIndex+1: filesystem type
	// separatorIndex+2: source device/path
	target := unescapePath(fields[4])
	options := fields[5]
	fsType := fields[separatorIndex+1]
	source := unescapePath(fields[separatorIndex+2])

	return MountInfo{
		Source:  source,
		Target:  target,
		FSType:  fsType,
		Options: options,
	}, nil
}

// unescapePath handles escaped characters in mount paths
// Spaces are encoded as \040, tabs as \011, newlines as \012, backslashes as \134
func unescapePath(path string) string {
	// Common escape sequences in /proc/self/mountinfo
	replacer := strings.NewReplacer(
		`\040`, " ", // space
		`\011`, "\t", // tab
		`\012`, "\n", // newline
		`\134`, `\`, // backslash
	)
	return replacer.Replace(path)
}

// GetMountDevice returns the source device for a given mount path
// Returns the device path or an error if the mount point is not found
func GetMountDevice(mountPath string) (string, error) {
	mounts, err := GetMounts()
	if err != nil {
		return "", fmt.Errorf("failed to get mounts: %w", err)
	}

	for _, mount := range mounts {
		if mount.Target == mountPath {
			klog.V(4).Infof("Found mount device for %s: %s", mountPath, mount.Source)
			return mount.Source, nil
		}
	}

	return "", fmt.Errorf("mount point not found: %s", mountPath)
}

// GetMountInfo returns the full MountInfo struct for a given mount path
// Returns the MountInfo or an error if the mount point is not found
func GetMountInfo(mountPath string) (*MountInfo, error) {
	mounts, err := GetMounts()
	if err != nil {
		return nil, fmt.Errorf("failed to get mounts: %w", err)
	}

	for _, mount := range mounts {
		if mount.Target == mountPath {
			klog.V(4).Infof("Found mount info for %s: source=%s, fstype=%s, options=%s",
				mountPath, mount.Source, mount.FSType, mount.Options)
			return &mount, nil
		}
	}

	return nil, fmt.Errorf("mount point not found: %s", mountPath)
}

// GetMountsWithTimeout parses mount information with a timeout to prevent hangs
// on corrupted filesystems. Returns error if parsing takes longer than ProcmountsTimeout.
func GetMountsWithTimeout(ctx context.Context) ([]*mountinfo.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, ProcmountsTimeout)
	defer cancel()

	type result struct {
		mounts []*mountinfo.Info
		err    error
	}
	resultCh := make(chan result, 1)

	go func() {
		mounts, err := mountinfo.GetMounts(nil)
		resultCh <- result{mounts: mounts, err: err}
	}()

	select {
	case res := <-resultCh:
		return res.mounts, res.err
	case <-ctx.Done():
		return nil, fmt.Errorf("procmounts parsing timed out after %v: %w "+
			"This may indicate filesystem corruption or an excessive number of mount entries. "+
			"Check /proc/mounts manually and consider unmounting stale entries",
			ProcmountsTimeout, ctx.Err())
	}
}

// DetectDuplicateMounts checks if a device has an excessive number of mount entries,
// indicating a mount storm (often caused by filesystem corruption).
// Returns (count, error) where error is non-nil if threshold exceeded.
func DetectDuplicateMounts(mounts []*mountinfo.Info, devicePath string) (int, error) {
	count := 0
	for _, mount := range mounts {
		if mount.Source == devicePath {
			count++
		}
	}

	if count >= MaxDuplicateMountsPerDevice {
		return count, fmt.Errorf(
			"mount storm detected: device %s has %d mount entries (threshold: %d) "+
				"This indicates filesystem corruption or a runaway mount loop "+
				"Manual cleanup required: identify and unmount duplicate entries with 'findmnt' and 'umount'",
			devicePath, count, MaxDuplicateMountsPerDevice)
	}

	return count, nil
}

// ConvertMobyMount converts moby/sys/mountinfo.Info to our MountInfo type
func ConvertMobyMount(m *mountinfo.Info) MountInfo {
	return MountInfo{
		Source:  m.Source,
		Target:  m.Mountpoint,
		FSType:  m.FSType,
		Options: m.Options,
	}
}
