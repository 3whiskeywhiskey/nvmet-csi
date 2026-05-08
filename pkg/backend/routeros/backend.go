// Package routeros adapts the existing RouterOS/RDS client to the generic
// controller backend contract.
package routeros

import (
	"context"
	"errors"
	"fmt"

	"git.srvlab.io/whiskey/nvmet-csi/pkg/backend"
	"git.srvlab.io/whiskey/nvmet-csi/pkg/rds"
	"git.srvlab.io/whiskey/nvmet-csi/pkg/reconciler"
	"git.srvlab.io/whiskey/nvmet-csi/pkg/utils"
)

type Backend struct {
	client rds.RDSClient
}

func New(client rds.RDSClient) *Backend {
	return &Backend{client: client}
}

func (b *Backend) Name() string {
	return backend.TypeRouterOSRDS
}

func (b *Backend) Address() string {
	if b == nil || b.client == nil {
		return ""
	}
	return b.client.GetAddress()
}

func (b *Backend) BuildEnsureVolumePlan(ctx context.Context, desired backend.CreateVolumeRequest) (*backend.VolumePlan, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	plan, err := reconciler.BuildEnsureVolumePlan(b.client, toRDSCreateVolumeOptions(desired))
	if err != nil {
		return nil, mapError(err)
	}
	return fromRDSVolumePlan(plan, desired), nil
}

func (b *Backend) BuildDeleteVolumePlan(ctx context.Context, volumeID string) (*backend.VolumePlan, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	plan, err := reconciler.BuildDeleteVolumePlan(b.client, volumeID)
	if err != nil {
		return nil, mapError(err)
	}
	return fromRDSVolumePlan(plan, backend.CreateVolumeRequest{}), nil
}

func (b *Backend) ApplyVolumePlan(ctx context.Context, plan *backend.VolumePlan, opts backend.VolumeApplyOptions) (*backend.VolumeApplyResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("volume plan is required")
	}

	rdsPlan, ok := plan.Native.(*reconciler.VolumePlan)
	if !ok || rdsPlan == nil {
		rdsPlan = toRDSVolumePlan(plan)
	}

	result, err := reconciler.ApplyVolumePlan(ctx, b.client, rdsPlan, reconciler.VolumeApplyOptions{DryRun: opts.DryRun})
	if err != nil {
		return nil, mapError(err)
	}
	return &backend.VolumeApplyResult{
		Action:   backend.VolumeAction(result.Action),
		VolumeID: result.Slot,
		Applied:  result.Applied,
		DryRun:   result.DryRun,
		Backend:  b.Name(),
		Reason:   plan.Reason,
		Commands: append([]string(nil), plan.Commands...),
	}, nil
}

func (b *Backend) GetVolume(ctx context.Context, volumeID string) (*backend.Volume, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	volume, err := b.client.GetVolume(volumeID)
	if err != nil {
		return nil, mapError(err)
	}
	return fromRDSVolume(volume), nil
}

func (b *Backend) ListVolumes(ctx context.Context) ([]backend.Volume, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	volumes, err := b.client.ListVolumes()
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]backend.Volume, 0, len(volumes))
	for i := range volumes {
		out = append(out, *fromRDSVolume(&volumes[i]))
	}
	return out, nil
}

func (b *Backend) ResizeVolume(ctx context.Context, volumeID string, sizeBytes int64) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	return mapError(b.client.ResizeVolume(volumeID, sizeBytes))
}

func (b *Backend) GetCapacity(ctx context.Context, basePath string) (*backend.Capacity, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	capacity, err := b.client.GetCapacity(basePath)
	if err != nil {
		return nil, mapError(err)
	}
	return &backend.Capacity{
		TotalBytes:     capacity.TotalBytes,
		FreeBytes:      capacity.FreeBytes,
		UsedBytes:      capacity.UsedBytes,
		LastUpdateTime: capacity.LastUpdateTime,
	}, nil
}

func (b *Backend) CreateSnapshot(ctx context.Context, req backend.CreateSnapshotRequest) (*backend.Snapshot, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	snapshot, err := b.client.CreateSnapshot(rds.CreateSnapshotOptions{
		Name:         req.ID,
		SourceVolume: req.SourceVolumeID,
		BasePath:     req.BasePath,
	})
	if err != nil {
		return nil, mapError(err)
	}
	return fromRDSSnapshot(snapshot), nil
}

func (b *Backend) DeleteSnapshot(ctx context.Context, snapshotID string) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	return mapError(b.client.DeleteSnapshot(snapshotID))
}

func (b *Backend) GetSnapshot(ctx context.Context, snapshotID string) (*backend.Snapshot, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	snapshot, err := b.client.GetSnapshot(snapshotID)
	if err != nil {
		return nil, mapError(err)
	}
	return fromRDSSnapshot(snapshot), nil
}

func (b *Backend) ListSnapshots(ctx context.Context) ([]backend.Snapshot, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	snapshots, err := b.client.ListSnapshots()
	if err != nil {
		return nil, mapError(err)
	}
	out := make([]backend.Snapshot, 0, len(snapshots))
	for i := range snapshots {
		out = append(out, *fromRDSSnapshot(&snapshots[i]))
	}
	return out, nil
}

func (b *Backend) RestoreSnapshot(ctx context.Context, snapshotID string, req backend.CreateVolumeRequest) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	return mapError(b.client.RestoreSnapshot(snapshotID, toRDSCreateVolumeOptions(req)))
}

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func toRDSCreateVolumeOptions(req backend.CreateVolumeRequest) rds.CreateVolumeOptions {
	return rds.CreateVolumeOptions{
		Slot:          req.ID,
		FilePath:      req.Path,
		FileSizeBytes: req.SizeBytes,
		NVMETCPPort:   req.NVMETCPPort,
		NVMETCPNQN:    req.NVMETCPNQN,
	}
}

func fromRDSVolume(volume *rds.VolumeInfo) *backend.Volume {
	if volume == nil {
		return nil
	}
	return &backend.Volume{
		ID:            volume.Slot,
		Path:          volume.FilePath,
		SizeBytes:     volume.FileSizeBytes,
		NVMETCPExport: volume.NVMETCPExport,
		NVMETCPPort:   volume.NVMETCPPort,
		NVMETCPNQN:    volume.NVMETCPNQN,
		Status:        volume.Status,
		Attributes: map[string]string{
			"routeros.mount": fmt.Sprintf("%t", volume.Mount),
			"routeros.type":  volume.Type,
		},
	}
}

func fromRDSSnapshot(snapshot *rds.SnapshotInfo) *backend.Snapshot {
	if snapshot == nil {
		return nil
	}
	return &backend.Snapshot{
		ID:             snapshot.Name,
		SourceVolumeID: snapshot.SourceVolume,
		SizeBytes:      snapshot.FileSizeBytes,
		CreatedAt:      snapshot.CreatedAt,
		Path:           snapshot.FilePath,
	}
}

func fromRDSVolumePlan(plan *reconciler.VolumePlan, desired backend.CreateVolumeRequest) *backend.VolumePlan {
	if plan == nil {
		return nil
	}
	out := &backend.VolumePlan{
		Action:   backend.VolumeAction(plan.Action),
		VolumeID: plan.Slot,
		Reason:   plan.Reason,
		Backend:  backend.TypeRouterOSRDS,
		Actual:   fromRDSVolume(plan.Actual),
		Native:   plan,
	}
	if plan.Desired != nil {
		converted := backend.CreateVolumeRequest{
			ID:          plan.Desired.Slot,
			Path:        plan.Desired.FilePath,
			SizeBytes:   plan.Desired.FileSizeBytes,
			NVMETCPPort: plan.Desired.NVMETCPPort,
			NVMETCPNQN:  plan.Desired.NVMETCPNQN,
			Parameters:  desired.Parameters,
		}
		out.Desired = &converted
	}
	return out
}

func toRDSVolumePlan(plan *backend.VolumePlan) *reconciler.VolumePlan {
	out := &reconciler.VolumePlan{
		Action: reconciler.VolumeAction(plan.Action),
		Slot:   plan.VolumeID,
		Reason: plan.Reason,
	}
	if plan.Desired != nil {
		desired := toRDSCreateVolumeOptions(*plan.Desired)
		out.Desired = &desired
	}
	if plan.Actual != nil {
		out.Actual = &rds.VolumeInfo{
			Slot:          plan.Actual.ID,
			FilePath:      plan.Actual.Path,
			FileSizeBytes: plan.Actual.SizeBytes,
			NVMETCPExport: plan.Actual.NVMETCPExport,
			NVMETCPPort:   plan.Actual.NVMETCPPort,
			NVMETCPNQN:    plan.Actual.NVMETCPNQN,
			Status:        plan.Actual.Status,
		}
	}
	return out
}

func mapError(err error) error {
	if err == nil {
		return nil
	}

	var volumeNotFound *rds.VolumeNotFoundError
	if errors.As(err, &volumeNotFound) || errors.Is(err, utils.ErrVolumeNotFound) {
		id := ""
		if volumeNotFound != nil {
			id = volumeNotFound.Slot
		}
		return &backend.VolumeNotFoundError{ID: id}
	}

	var snapshotNotFound *rds.SnapshotNotFoundError
	if errors.As(err, &snapshotNotFound) {
		id := ""
		if snapshotNotFound != nil {
			id = snapshotNotFound.Name
		}
		return &backend.SnapshotNotFoundError{ID: id}
	}

	return err
}
