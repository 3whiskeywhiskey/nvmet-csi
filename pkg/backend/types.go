// Package backend defines controller-side storage backend contracts.
//
// The CSI node service intentionally stays backend-agnostic: it receives NVMe/TCP
// connection details and mounts the resulting block device. Backends are used by
// the controller to provision storage and publish those generic NVMe target
// details.
package backend

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	TypeRouterOSRDS   = "routeros-rds"
	TypeLinuxZOLNVMET = "linux-zol-nvmet"

	ExecutorLocal = "local"
	ExecutorSSH   = "ssh"
)

var (
	ErrVolumeNotFound   = errors.New("volume not found")
	ErrSnapshotNotFound = errors.New("snapshot not found")
)

type VolumeAction string

const (
	VolumeActionNoop     VolumeAction = "noop"
	VolumeActionCreate   VolumeAction = "create"
	VolumeActionDelete   VolumeAction = "delete"
	VolumeActionResize   VolumeAction = "resize"
	VolumeActionRepair   VolumeAction = "repair"
	VolumeActionConflict VolumeAction = "conflict"
)

// Volume describes a backend-provisioned NVMe/TCP-exported block device.
type Volume struct {
	ID        string
	Path      string
	SizeBytes int64

	NVMETCPExport bool
	NVMETCPPort   int
	NVMETCPNQN    string
	Status        string

	Attributes map[string]string
}

type Capacity struct {
	TotalBytes     int64
	FreeBytes      int64
	UsedBytes      int64
	LastUpdateTime time.Time
}

type CreateVolumeRequest struct {
	ID        string
	Path      string
	SizeBytes int64

	NVMETCPPort int
	NVMETCPNQN  string

	Parameters map[string]string
}

type Snapshot struct {
	ID             string
	SourceVolumeID string
	SizeBytes      int64
	CreatedAt      time.Time
	Path           string
}

type CreateSnapshotRequest struct {
	ID             string
	SourceVolumeID string
	BasePath       string
	Parameters     map[string]string
}

type VolumePlan struct {
	Action   VolumeAction
	VolumeID string
	Reason   string
	Desired  *CreateVolumeRequest
	Actual   *Volume

	// Native lets an adapter carry backend-specific planning state between
	// Build*Plan and ApplyVolumePlan without leaking that state into callers.
	Native any
}

type VolumeApplyOptions struct {
	DryRun bool
}

type VolumeApplyResult struct {
	Action   VolumeAction
	VolumeID string
	Applied  bool
	DryRun   bool
}

// Backend is the controller-side provisioning contract.
type Backend interface {
	Name() string
	Address() string

	BuildEnsureVolumePlan(ctx context.Context, desired CreateVolumeRequest) (*VolumePlan, error)
	BuildDeleteVolumePlan(ctx context.Context, volumeID string) (*VolumePlan, error)
	ApplyVolumePlan(ctx context.Context, plan *VolumePlan, opts VolumeApplyOptions) (*VolumeApplyResult, error)

	GetVolume(ctx context.Context, volumeID string) (*Volume, error)
	ListVolumes(ctx context.Context) ([]Volume, error)
	ResizeVolume(ctx context.Context, volumeID string, sizeBytes int64) error
	GetCapacity(ctx context.Context, basePath string) (*Capacity, error)

	CreateSnapshot(ctx context.Context, req CreateSnapshotRequest) (*Snapshot, error)
	DeleteSnapshot(ctx context.Context, snapshotID string) error
	GetSnapshot(ctx context.Context, snapshotID string) (*Snapshot, error)
	ListSnapshots(ctx context.Context) ([]Snapshot, error)
	RestoreSnapshot(ctx context.Context, snapshotID string, req CreateVolumeRequest) error
}

func IsVolumeNotFound(err error) bool {
	return errors.Is(err, ErrVolumeNotFound)
}

func IsSnapshotNotFound(err error) bool {
	return errors.Is(err, ErrSnapshotNotFound)
}

type VolumeNotFoundError struct {
	ID string
}

func (e *VolumeNotFoundError) Error() string {
	return fmt.Sprintf("%s: %s", ErrVolumeNotFound, e.ID)
}

func (e *VolumeNotFoundError) Unwrap() error {
	return ErrVolumeNotFound
}

type SnapshotNotFoundError struct {
	ID string
}

func (e *SnapshotNotFoundError) Error() string {
	return fmt.Sprintf("%s: %s", ErrSnapshotNotFound, e.ID)
}

func (e *SnapshotNotFoundError) Unwrap() error {
	return ErrSnapshotNotFound
}
