package nvme

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// SetMaxSectorsKB caps the size of a single I/O to an NVMe namespace by writing
// queue/max_sectors_kb on its block device and, under native multipath, on each of
// its path devices (block/<dev>/multipath/*). kb is clamped to the device's
// max_hw_sectors_kb. kb <= 0 leaves the kernel's value alone.
//
// A target that advertises no MDTS lets the host send transfers as large as its
// own limit (32 MiB by default); a Linux 6.18 nvmet-tcp target refuses data SGLs
// over 4 MiB with "Data SGL Length Invalid", which fails mkfs and fsck.
func SetMaxSectorsKB(sysfsRoot, devicePath string, kb int) error {
	if kb <= 0 {
		return nil
	}
	if sysfsRoot == "" {
		sysfsRoot = DefaultSysfsRoot
	}
	name := filepath.Base(devicePath)
	if !strings.HasPrefix(name, "nvme") {
		return fmt.Errorf("not an NVMe block device: %s", devicePath)
	}
	devices := []string{}
	paths, err := os.ReadDir(filepath.Join(sysfsRoot, "block", name, "multipath"))
	if err == nil {
		for _, p := range paths {
			devices = append(devices, p.Name())
		}
	}
	// Paths first: the head's limit is what bios are split to.
	devices = append(devices, name)
	for _, dev := range devices {
		if err := setDeviceMaxSectorsKB(filepath.Join(sysfsRoot, "block", dev, "queue"), kb); err != nil {
			return fmt.Errorf("%s: %w", dev, err)
		}
	}
	return nil
}

func setDeviceMaxSectorsKB(queueDir string, kb int) error {
	want := kb
	if raw, err := os.ReadFile(filepath.Join(queueDir, "max_hw_sectors_kb")); err == nil {
		if hw, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && hw > 0 && hw < want {
			want = hw
		}
	}
	return os.WriteFile(filepath.Join(queueDir, "max_sectors_kb"), []byte(strconv.Itoa(want)), 0o644)
}
