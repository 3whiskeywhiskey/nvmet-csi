// Package linuxzol provides a Linux ZFS-on-Linux + kernel nvmet backend.
//
// This package is intentionally controller-side only. The CSI node service stays
// backend-agnostic and consumes the NVMe/TCP target details produced here.
package linuxzol

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/backend"
)

const (
	defaultDeviceRoot   = "/dev/zvol"
	defaultConfigFSRoot = "/sys/kernel/config/nvmet"

	zfsPropVolumeID = "org.nvmet-csi:volume-id"
	zfsPropNQN      = "org.nvmet-csi:nqn"
	zfsPropPort     = "org.nvmet-csi:port"
)

type Config struct {
	Pool         string
	Address      string
	DeviceRoot   string
	ConfigFSRoot string
}

func (c Config) withDefaults() Config {
	if c.DeviceRoot == "" {
		c.DeviceRoot = defaultDeviceRoot
	}
	if c.ConfigFSRoot == "" {
		c.ConfigFSRoot = defaultConfigFSRoot
	}
	return c
}

func (c Config) validate() error {
	if c.Pool == "" {
		return fmt.Errorf("ZFS pool is required")
	}
	if c.Address == "" {
		return fmt.Errorf("NVMe/TCP target address is required")
	}
	return nil
}

func (c Config) datasetFor(volumeID string) string {
	return c.Pool + "/" + volumeID
}

func (c Config) devicePathFor(volumeID string) string {
	return filepath.Join(c.DeviceRoot, c.Pool, volumeID)
}

func (c Config) subsystemPath(nqn string) string {
	return filepath.Join(c.ConfigFSRoot, "subsystems", nqn)
}

func (c Config) namespacePath(nqn string) string {
	return filepath.Join(c.subsystemPath(nqn), "namespaces", "1")
}

func (c Config) portPath(port int) string {
	return filepath.Join(c.ConfigFSRoot, "ports", strconv.Itoa(port))
}

func (c Config) portSubsystemPath(port int, nqn string) string {
	return filepath.Join(c.portPath(port), "subsystems", linkNameForNQN(nqn))
}

func (c Config) portLinkTargetMatches(port int, nqn string, target string) bool {
	if port <= 0 || nqn == "" || target == "" {
		return false
	}

	expected := filepath.Clean(c.subsystemPath(nqn))
	if filepath.Clean(target) == expected {
		return true
	}
	if filepath.IsAbs(target) {
		return false
	}

	linkDir := filepath.Dir(c.portSubsystemPath(port, nqn))
	return filepath.Clean(filepath.Join(linkDir, target)) == expected
}

func (c Config) portServiceID(port int) string {
	return strconv.Itoa(port)
}

func (c Config) portTransport() string {
	return "tcp"
}

func (c Config) portAddressFamily() string {
	if ip := net.ParseIP(c.Address); ip != nil && ip.To4() == nil {
		return "ipv6"
	}
	return "ipv4"
}

func linkNameForNQN(nqn string) string {
	return strings.NewReplacer("/", "_").Replace(nqn)
}

type VolumeSpec struct {
	ID         string
	Dataset    string
	DevicePath string
	SizeBytes  int64
	NQN        string
	Port       int
}

type VolumeState struct {
	ID              string
	OwnerVolumeID   string
	Dataset         string
	DevicePath      string
	SizeBytes       int64
	NQN             string
	Port            int
	Exported        bool
	NamespaceReady  bool
	ExportDevice    string
	PortEntryExists bool
	PortLinkTarget  string
	PortAddress     string
	PortServiceID   string
	PortTransport   string
	PortAddressFam  string
	Status          string
}

func volumeFromState(state *VolumeState) *backend.Volume {
	if state == nil {
		return nil
	}
	return &backend.Volume{
		ID:            state.ID,
		Path:          state.DevicePath,
		SizeBytes:     state.SizeBytes,
		NVMETCPExport: state.Exported,
		NVMETCPPort:   state.Port,
		NVMETCPNQN:    state.NQN,
		Status:        state.Status,
		Attributes: map[string]string{
			"linux.zfs.owner_volume_id":   state.OwnerVolumeID,
			"linux.zfs.dataset":           state.Dataset,
			"linux.nvmet.port.address":    state.PortAddress,
			"linux.nvmet.port.service_id": state.PortServiceID,
			"linux.nvmet.port.transport":  state.PortTransport,
			"linux.nvmet.port.adrfam":     state.PortAddressFam,
		},
	}
}
