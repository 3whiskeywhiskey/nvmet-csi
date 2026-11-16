package nvme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeQueue(t *testing.T, root, dev, hw, cur string) {
	t.Helper()
	q := filepath.Join(root, "block", dev, "queue")
	if err := os.MkdirAll(q, 0o755); err != nil {
		t.Fatal(err)
	}
	for f, v := range map[string]string{"max_hw_sectors_kb": hw, "max_sectors_kb": cur} {
		if err := os.WriteFile(filepath.Join(q, f), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readMax(t *testing.T, root, dev string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, "block", dev, "queue", "max_sectors_kb"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func TestSetMaxSectorsKBMultipath(t *testing.T) {
	root := t.TempDir()
	writeQueue(t, root, "nvme3n1", "2147483647", "32768")
	writeQueue(t, root, "nvme3c3n1", "2147483647", "32768")
	if err := os.MkdirAll(filepath.Join(root, "block", "nvme3n1", "multipath", "nvme3c3n1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SetMaxSectorsKB(root, "/dev/nvme3n1", 4096); err != nil {
		t.Fatal(err)
	}
	for _, dev := range []string{"nvme3n1", "nvme3c3n1"} {
		if got := readMax(t, root, dev); got != "4096" {
			t.Errorf("%s: max_sectors_kb = %s, want 4096", dev, got)
		}
	}
}

func TestSetMaxSectorsKBClampsToHardware(t *testing.T) {
	root := t.TempDir()
	writeQueue(t, root, "nvme0n1", "1024", "1024")
	if err := SetMaxSectorsKB(root, "/dev/nvme0n1", 4096); err != nil {
		t.Fatal(err)
	}
	if got := readMax(t, root, "nvme0n1"); got != "1024" {
		t.Errorf("max_sectors_kb = %s, want 1024", got)
	}
}

func TestSetMaxSectorsKBZeroIsNoop(t *testing.T) {
	root := t.TempDir()
	writeQueue(t, root, "nvme0n1", "2147483647", "32768")
	if err := SetMaxSectorsKB(root, "/dev/nvme0n1", 0); err != nil {
		t.Fatal(err)
	}
	if got := readMax(t, root, "nvme0n1"); got != "32768" {
		t.Errorf("max_sectors_kb = %s, want 32768 untouched", got)
	}
}

func TestSetMaxSectorsKBRejectsNonNVMe(t *testing.T) {
	if err := SetMaxSectorsKB(t.TempDir(), "/dev/sda", 4096); err == nil {
		t.Error("expected an error for /dev/sda")
	}
}
