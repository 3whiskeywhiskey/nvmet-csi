package linuxzol

import (
	"context"
	"fmt"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/backend"
)

type Backend struct {
	cfg  Config
	host Host
}

func New(cfg Config, host Host) (*Backend, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if host == nil {
		return nil, fmt.Errorf("linux ZoL host is required")
	}
	return &Backend{cfg: cfg, host: host}, nil
}

func (b *Backend) Name() string {
	return backend.TypeLinuxZOLNVMET
}

func (b *Backend) Address() string {
	if b == nil {
		return ""
	}
	return b.cfg.Address
}

func (b *Backend) BuildEnsureVolumePlan(ctx context.Context, desired backend.CreateVolumeRequest) (*backend.VolumePlan, error) {
	if err := validateDesired(desired); err != nil {
		return nil, err
	}
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	spec := b.specFromRequest(desired)
	actual, err := b.host.GetVolume(ctx, desired.ID)
	if err != nil {
		if backend.IsVolumeNotFound(err) {
			normalized := requestFromSpec(spec, desired.Parameters)
			return &backend.VolumePlan{
				Action:   backend.VolumeActionCreate,
				VolumeID: desired.ID,
				Reason:   "zvol missing",
				Desired:  &normalized,
				Native:   spec,
			}, nil
		}
		return nil, fmt.Errorf("failed to observe volume %s: %w", desired.ID, err)
	}

	if actual.Dataset != spec.Dataset {
		return conflictPlan(spec, actual, "ZFS dataset differs"), nil
	}
	if actual.DevicePath != spec.DevicePath {
		return conflictPlan(spec, actual, "zvol device path differs"), nil
	}
	if actual.NQN != spec.NQN {
		return conflictPlan(spec, actual, "NVMe/TCP NQN differs"), nil
	}
	if actual.Port != spec.Port {
		return conflictPlan(spec, actual, "NVMe/TCP port differs"), nil
	}
	if actual.ExportDevice != "" && actual.ExportDevice != spec.DevicePath {
		return conflictPlan(spec, actual, "nvmet namespace device path differs"), nil
	}
	if actual.PortLinkTarget != "" && actual.PortLinkTarget != b.cfg.subsystemPath(spec.NQN) {
		return conflictPlan(spec, actual, "nvmet port link target differs"), nil
	}
	if actual.SizeBytes > spec.SizeBytes {
		return conflictPlan(spec, actual, "actual zvol is larger than desired"), nil
	}
	if actual.SizeBytes < spec.SizeBytes {
		normalized := requestFromSpec(spec, desired.Parameters)
		return &backend.VolumePlan{
			Action:   backend.VolumeActionResize,
			VolumeID: desired.ID,
			Reason:   "zvol exists and is smaller than desired",
			Desired:  &normalized,
			Actual:   volumeFromState(actual),
			Native:   spec,
		}, nil
	}
	if !actual.Exported {
		normalized := requestFromSpec(spec, desired.Parameters)
		return &backend.VolumePlan{
			Action:   backend.VolumeActionRepair,
			VolumeID: desired.ID,
			Reason:   "zvol exists but nvmet export is incomplete",
			Desired:  &normalized,
			Actual:   volumeFromState(actual),
			Native:   spec,
		}, nil
	}

	normalized := requestFromSpec(spec, desired.Parameters)
	return &backend.VolumePlan{
		Action:   backend.VolumeActionNoop,
		VolumeID: desired.ID,
		Reason:   "zvol and nvmet export already match desired state",
		Desired:  &normalized,
		Actual:   volumeFromState(actual),
		Native:   spec,
	}, nil
}

func (b *Backend) BuildDeleteVolumePlan(ctx context.Context, volumeID string) (*backend.VolumePlan, error) {
	if volumeID == "" {
		return nil, fmt.Errorf("volume ID is required")
	}
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	actual, err := b.host.GetVolume(ctx, volumeID)
	if err != nil {
		if backend.IsVolumeNotFound(err) {
			return &backend.VolumePlan{
				Action:   backend.VolumeActionNoop,
				VolumeID: volumeID,
				Reason:   "zvol already absent",
			}, nil
		}
		return nil, fmt.Errorf("failed to observe volume %s: %w", volumeID, err)
	}
	if actual.NQN == "" {
		return conflictPlanFromState(volumeID, actual, "NVMe/TCP NQN metadata is missing"), nil
	}
	if actual.Port <= 0 {
		return conflictPlanFromState(volumeID, actual, "NVMe/TCP port metadata is missing"), nil
	}
	if actual.ExportDevice != "" && actual.ExportDevice != actual.DevicePath {
		return conflictPlanFromState(volumeID, actual, "nvmet namespace device path differs"), nil
	}
	if actual.PortLinkTarget != "" && actual.PortLinkTarget != b.cfg.subsystemPath(actual.NQN) {
		return conflictPlanFromState(volumeID, actual, "nvmet port link target differs"), nil
	}
	if actual.PortEntryExists && actual.PortLinkTarget == "" {
		return conflictPlanFromState(volumeID, actual, "nvmet port entry exists but is not an expected symlink"), nil
	}
	return &backend.VolumePlan{
		Action:   backend.VolumeActionDelete,
		VolumeID: volumeID,
		Reason:   "zvol exists",
		Actual:   volumeFromState(actual),
	}, nil
}

func (b *Backend) ApplyVolumePlan(ctx context.Context, plan *backend.VolumePlan, opts backend.VolumeApplyOptions) (*backend.VolumeApplyResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("volume plan is required")
	}
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}

	result := &backend.VolumeApplyResult{
		Action:   plan.Action,
		VolumeID: plan.VolumeID,
		DryRun:   opts.DryRun,
	}

	switch plan.Action {
	case backend.VolumeActionNoop:
		return result, nil
	case backend.VolumeActionConflict:
		return result, fmt.Errorf("refusing unsafe Linux ZoL reconciliation for %s: %s", plan.VolumeID, plan.Reason)
	case backend.VolumeActionCreate:
		spec, err := b.specFromPlan(plan)
		if err != nil {
			return nil, err
		}
		if opts.DryRun {
			return result, nil
		}
		if err := b.host.CreateVolume(ctx, spec); err != nil {
			return nil, err
		}
	case backend.VolumeActionResize:
		spec, err := b.specFromPlan(plan)
		if err != nil {
			return nil, err
		}
		if opts.DryRun {
			return result, nil
		}
		if err := b.host.ResizeVolume(ctx, spec.ID, spec.SizeBytes); err != nil {
			return nil, err
		}
	case backend.VolumeActionRepair:
		spec, err := b.specFromPlan(plan)
		if err != nil {
			return nil, err
		}
		if opts.DryRun {
			return result, nil
		}
		if err := b.host.RepairVolume(ctx, spec); err != nil {
			return nil, err
		}
	case backend.VolumeActionDelete:
		if opts.DryRun {
			return result, nil
		}
		if err := b.host.DeleteVolume(ctx, plan.VolumeID); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown volume action %q", plan.Action)
	}

	result.Applied = true
	return result, nil
}

func (b *Backend) GetVolume(ctx context.Context, volumeID string) (*backend.Volume, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	volume, err := b.host.GetVolume(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	return volumeFromState(volume), nil
}

func (b *Backend) ListVolumes(ctx context.Context) ([]backend.Volume, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	volumes, err := b.host.ListVolumes(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]backend.Volume, 0, len(volumes))
	for i := range volumes {
		out = append(out, *volumeFromState(&volumes[i]))
	}
	return out, nil
}

func (b *Backend) ResizeVolume(ctx context.Context, volumeID string, sizeBytes int64) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	return b.host.ResizeVolume(ctx, volumeID, sizeBytes)
}

func (b *Backend) GetCapacity(ctx context.Context, _ string) (*backend.Capacity, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	return b.host.GetCapacity(ctx)
}

func (b *Backend) CreateSnapshot(context.Context, backend.CreateSnapshotRequest) (*backend.Snapshot, error) {
	return nil, fmt.Errorf("linux ZoL snapshots are not implemented yet")
}

func (b *Backend) DeleteSnapshot(context.Context, string) error {
	return fmt.Errorf("linux ZoL snapshots are not implemented yet")
}

func (b *Backend) GetSnapshot(_ context.Context, snapshotID string) (*backend.Snapshot, error) {
	return nil, &backend.SnapshotNotFoundError{ID: snapshotID}
}

func (b *Backend) ListSnapshots(context.Context) ([]backend.Snapshot, error) {
	return []backend.Snapshot{}, nil
}

func (b *Backend) RestoreSnapshot(context.Context, string, backend.CreateVolumeRequest) error {
	return fmt.Errorf("linux ZoL snapshot restore is not implemented yet")
}

func (b *Backend) specFromRequest(req backend.CreateVolumeRequest) VolumeSpec {
	return VolumeSpec{
		ID:         req.ID,
		Dataset:    b.cfg.datasetFor(req.ID),
		DevicePath: b.cfg.devicePathFor(req.ID),
		SizeBytes:  req.SizeBytes,
		NQN:        req.NVMETCPNQN,
		Port:       req.NVMETCPPort,
	}
}

func (b *Backend) specFromPlan(plan *backend.VolumePlan) (VolumeSpec, error) {
	if spec, ok := plan.Native.(VolumeSpec); ok {
		return spec, nil
	}
	if plan.Desired == nil {
		return VolumeSpec{}, fmt.Errorf("%s plan for %s has no desired state", plan.Action, plan.VolumeID)
	}
	return b.specFromRequest(*plan.Desired), nil
}

func requestFromSpec(spec VolumeSpec, params map[string]string) backend.CreateVolumeRequest {
	return backend.CreateVolumeRequest{
		ID:          spec.ID,
		Path:        spec.DevicePath,
		SizeBytes:   spec.SizeBytes,
		NVMETCPPort: spec.Port,
		NVMETCPNQN:  spec.NQN,
		Parameters:  params,
	}
}

func validateDesired(desired backend.CreateVolumeRequest) error {
	if desired.ID == "" {
		return fmt.Errorf("desired volume ID is required")
	}
	if desired.SizeBytes <= 0 {
		return fmt.Errorf("desired volume size must be positive")
	}
	if desired.NVMETCPNQN == "" {
		return fmt.Errorf("desired NVMe/TCP NQN is required")
	}
	if desired.NVMETCPPort <= 0 {
		return fmt.Errorf("desired NVMe/TCP port must be positive")
	}
	return nil
}

func conflictPlan(desired VolumeSpec, actual *VolumeState, reason string) *backend.VolumePlan {
	normalized := requestFromSpec(desired, nil)
	return &backend.VolumePlan{
		Action:   backend.VolumeActionConflict,
		VolumeID: desired.ID,
		Reason:   reason,
		Desired:  &normalized,
		Actual:   volumeFromState(actual),
		Native:   desired,
	}
}

func conflictPlanFromState(volumeID string, actual *VolumeState, reason string) *backend.VolumePlan {
	return &backend.VolumePlan{
		Action:   backend.VolumeActionConflict,
		VolumeID: volumeID,
		Reason:   reason,
		Actual:   volumeFromState(actual),
	}
}

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
