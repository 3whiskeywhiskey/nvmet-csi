package rds

import (
	"fmt"
	"time"
)

// RDSClient defines the interface for interacting with MikroTik RDS servers
// This interface allows for multiple implementations (SSH, API, mock, etc.)
type RDSClient interface {
	// Connection management
	Connect() error
	Close() error
	IsConnected() bool

	// Volume operations
	CreateVolume(opts CreateVolumeOptions) error
	DeleteVolume(slot string) error
	ResizeVolume(slot string, newSizeBytes int64) error
	GetVolume(slot string) (*VolumeInfo, error)
	VerifyVolumeExists(slot string) error
	ListVolumes() ([]VolumeInfo, error)

	// File operations
	ListFiles(path string) ([]FileInfo, error)
	DeleteFile(path string) error

	// Capacity queries
	GetCapacity(basePath string) (*CapacityInfo, error)

	// GetAddress returns the RDS server address (for logging/debugging)
	GetAddress() string

	// Snapshot operations
	CreateSnapshot(opts CreateSnapshotOptions) (*SnapshotInfo, error)
	DeleteSnapshot(snapshotID string) error
	GetSnapshot(snapshotID string) (*SnapshotInfo, error)
	ListSnapshots() ([]SnapshotInfo, error)
	RestoreSnapshot(snapshotID string, newVolumeOpts CreateVolumeOptions) error

	// Monitoring operations
	GetDiskMetrics(slot string) (*DiskMetrics, error)
	GetHardwareHealth(snmpHost string, snmpCommunity string) (*HardwareHealthMetrics, error)
}

// ClientConfig holds configuration for creating an RDS client
type ClientConfig struct {
	Protocol   string        // Protocol to use: "ssh" (default), "api" (future)
	Address    string        // RDS IP address
	Port       int           // Port number (default: 22 for SSH, 8728/8729 for API)
	User       string        // Username (typically "admin")
	PrivateKey []byte        // SSH private key content (for SSH protocol)
	Password   string        // Password (for API protocol, future)
	Timeout    time.Duration // Connection timeout (default 10s)
	UseTLS     bool          // Use TLS for API protocol (future)

	// SSH Security Options
	HostKey            []byte      // SSH host public key for verification (required for production)
	HostKeyCallback    interface{} // ssh.HostKeyCallback - custom host key verification (for SSH)
	InsecureSkipVerify bool        // Skip host key verification (INSECURE - for testing only)

	// CommandObserver receives per-command RouterOS SSH telemetry.
	CommandObserver CommandObserver

	// MutationDecisionObserver receives high-level observe-before-mutate decisions.
	MutationDecisionObserver MutationDecisionObserver
}

// NewClient creates a new RDS client based on the configuration
// Currently only SSH protocol is supported. API protocol support is planned for the future.
func NewClient(config ClientConfig) (RDSClient, error) {
	// Set protocol default
	if config.Protocol == "" {
		config.Protocol = "ssh"
	}

	// Route to appropriate implementation
	switch config.Protocol {
	case "ssh":
		return newSSHClient(config)
	case "api":
		return nil, fmt.Errorf("API protocol not yet implemented - use 'ssh' protocol")
	default:
		return nil, fmt.Errorf("unsupported protocol: %s (supported: ssh)", config.Protocol)
	}
}
