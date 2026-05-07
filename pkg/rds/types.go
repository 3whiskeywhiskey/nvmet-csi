package rds

import (
	"fmt"
	"time"
)

// VolumeInfo represents an RDS volume
type VolumeInfo struct {
	Slot          string // Volume identifier (slot name)
	Type          string // "file" for file-backed volumes
	FilePath      string // Path to backing file
	FileSizeBytes int64  // Size in bytes
	Mount         bool   // Whether RouterOS should auto-mount/scan the disk
	NVMETCPExport bool   // Whether NVMe/TCP export is enabled
	NVMETCPPort   int    // NVMe/TCP server port
	NVMETCPNQN    string // NVMe Qualified Name
	Status        string // "ready", "formatting", "error"
}

// CapacityInfo represents filesystem capacity information
type CapacityInfo struct {
	TotalBytes     int64
	FreeBytes      int64
	UsedBytes      int64
	LastUpdateTime time.Time
}

// CreateVolumeOptions contains parameters for creating a volume
type CreateVolumeOptions struct {
	Slot          string // Unique identifier (pvc-<uuid>)
	FilePath      string // Full path to backing file
	FileSizeBytes int64  // Size in bytes
	Mount         bool   // RouterOS disk mount setting; false emits mount=no
	NVMETCPPort   int    // NVMe/TCP port (default 4420)
	NVMETCPNQN    string // NVMe Qualified Name
}

// FileInfo represents a file on the RDS filesystem
type FileInfo struct {
	Name      string    // File name
	Path      string    // Full path to file
	SizeBytes int64     // Size in bytes
	Type      string    // "file" or "directory"
	CreatedAt time.Time // Creation time (if available)
}

// VolumeNotFoundError is returned when a volume is not found
type VolumeNotFoundError struct {
	Slot string
}

func (e *VolumeNotFoundError) Error() string {
	return fmt.Sprintf("volume not found: %s", e.Slot)
}

// SnapshotInfo represents an RDS disk snapshot created via /disk add copy-from
type SnapshotInfo struct {
	Name          string    // Snapshot slot name (snap-<source-uuid>-at-<timestamp>)
	SourceVolume  string    // Source volume slot (pvc-<uuid>)
	FileSizeBytes int64     // Size of snapshot (copied from source volume)
	CreatedAt     time.Time // Creation timestamp (parsed from slot name or RDS output)
	FilePath      string    // Backing file path on RDS (e.g., /storage-pool/metal-csi/snap-xxx.img)
}

// CreateSnapshotOptions contains parameters for creating a snapshot
type CreateSnapshotOptions struct {
	Name         string // snap-<source-uuid>-at-<timestamp>
	SourceVolume string // pvc-<uuid> (source volume slot)
	BasePath     string // Base directory for snapshot files (e.g., /storage-pool/metal-csi)
}

// SnapshotNotFoundError is returned when a snapshot is not found
type SnapshotNotFoundError struct {
	Name string
}

func (e *SnapshotNotFoundError) Error() string {
	return fmt.Sprintf("snapshot not found: %s", e.Name)
}

// DiskMetrics represents real-time disk performance metrics from /disk monitor-traffic
type DiskMetrics struct {
	Slot              string  // Disk slot name (e.g., "storage-pool")
	ReadOpsPerSecond  float64 // Current read IOPS
	WriteOpsPerSecond float64 // Current write IOPS
	ReadBytesPerSec   float64 // Read throughput in bytes/sec (converted from bps)
	WriteBytesPerSec  float64 // Write throughput in bytes/sec (converted from bps)
	ReadTimeMs        float64 // Read latency in milliseconds
	WriteTimeMs       float64 // Write latency in milliseconds
	WaitTimeMs        float64 // Wait/queue latency in milliseconds
	InFlightOps       float64 // Current queue depth (in-flight operations)
	ActiveTimeMs      float64 // Disk active/busy time in milliseconds
}

// HardwareHealthMetrics represents hardware health status from SNMP
type HardwareHealthMetrics struct {
	CPUTemperature    float64 // CPU temperature in Celsius
	BoardTemperature  float64 // Board temperature in Celsius
	Fan1Speed         float64 // Fan 1 RPM
	Fan2Speed         float64 // Fan 2 RPM
	PSU1Power         float64 // PSU 1 power draw in watts
	PSU2Power         float64 // PSU 2 power draw in watts
	PSU1Temperature   float64 // PSU 1 temperature in Celsius
	PSU2Temperature   float64 // PSU 2 temperature in Celsius
	DiskPoolSizeBytes float64 // RAID6 pool total size in bytes
	DiskPoolUsedBytes float64 // RAID6 pool used space in bytes
}
