package reconciler

import (
	"context"
	"errors"
	"fmt"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/rds"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/utils"
)

// VolumeAction is the mutation needed to bring an RDS volume to the desired state.
type VolumeAction string

const (
	VolumeActionNoop     VolumeAction = "noop"
	VolumeActionCreate   VolumeAction = "create"
	VolumeActionDelete   VolumeAction = "delete"
	VolumeActionResize   VolumeAction = "resize"
	VolumeActionConflict VolumeAction = "conflict"
)

// VolumePlan describes the observed state and the smallest safe action to take.
type VolumePlan struct {
	Action  VolumeAction
	Slot    string
	Reason  string
	Desired *rds.CreateVolumeOptions
	Actual  *rds.VolumeInfo
}

// VolumeApplyOptions controls how a volume plan is applied.
type VolumeApplyOptions struct {
	DryRun bool
}

// VolumeApplyResult captures what happened when applying a plan.
type VolumeApplyResult struct {
	Action  VolumeAction
	Slot    string
	Applied bool
	DryRun  bool
}

// BuildEnsureVolumePlan observes RDS first, then decides whether a create, resize,
// or no-op is needed. It deliberately treats unsafe shape differences as conflicts
// so callers do not delete/recreate backing disks during churn.
func BuildEnsureVolumePlan(client rds.RDSClient, desired rds.CreateVolumeOptions) (*VolumePlan, error) {
	if client == nil {
		return nil, fmt.Errorf("RDS client is required")
	}
	if desired.Slot == "" {
		return nil, fmt.Errorf("desired volume slot is required")
	}

	actual, exists, err := observeVolume(client, desired.Slot)
	if err != nil {
		return nil, err
	}
	if !exists {
		return &VolumePlan{
			Action:  VolumeActionCreate,
			Slot:    desired.Slot,
			Reason:  "volume missing",
			Desired: &desired,
		}, nil
	}

	if actual.FilePath != desired.FilePath {
		return conflictPlan(desired, actual, "backing file path differs"), nil
	}
	if actual.NVMETCPNQN != desired.NVMETCPNQN {
		return conflictPlan(desired, actual, "NVMe/TCP NQN differs"), nil
	}
	if actual.NVMETCPPort != desired.NVMETCPPort {
		return conflictPlan(desired, actual, "NVMe/TCP port differs"), nil
	}
	if !actual.NVMETCPExport {
		return conflictPlan(desired, actual, "NVMe/TCP export is disabled"), nil
	}
	if actual.Status != "" && actual.Status != "ready" {
		return conflictPlan(desired, actual, fmt.Sprintf("volume status is %q", actual.Status)), nil
	}
	if actual.FileSizeBytes > desired.FileSizeBytes {
		return conflictPlan(desired, actual, "actual volume is larger than desired"), nil
	}
	if actual.FileSizeBytes < desired.FileSizeBytes {
		return &VolumePlan{
			Action:  VolumeActionResize,
			Slot:    desired.Slot,
			Reason:  "volume exists and is smaller than desired",
			Desired: &desired,
			Actual:  actual,
		}, nil
	}

	return &VolumePlan{
		Action:  VolumeActionNoop,
		Slot:    desired.Slot,
		Reason:  "volume already matches desired state",
		Desired: &desired,
		Actual:  actual,
	}, nil
}

// BuildDeleteVolumePlan observes RDS first, then plans a delete only when the
// volume exists. Missing volumes are idempotent no-ops.
func BuildDeleteVolumePlan(client rds.RDSClient, slot string) (*VolumePlan, error) {
	if client == nil {
		return nil, fmt.Errorf("RDS client is required")
	}
	if slot == "" {
		return nil, fmt.Errorf("volume slot is required")
	}

	actual, exists, err := observeVolume(client, slot)
	if err != nil {
		return nil, err
	}
	if !exists {
		return &VolumePlan{
			Action: VolumeActionNoop,
			Slot:   slot,
			Reason: "volume already absent",
		}, nil
	}

	return &VolumePlan{
		Action: VolumeActionDelete,
		Slot:   slot,
		Reason: "volume exists",
		Actual: actual,
	}, nil
}

// ApplyVolumePlan executes a previously built plan. Dry-run mode reports the
// intended mutation without changing RDS.
func ApplyVolumePlan(ctx context.Context, client rds.RDSClient, plan *VolumePlan, opts VolumeApplyOptions) (*VolumeApplyResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if client == nil {
		return nil, fmt.Errorf("RDS client is required")
	}
	if plan == nil {
		return nil, fmt.Errorf("volume plan is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result := &VolumeApplyResult{
		Action: plan.Action,
		Slot:   plan.Slot,
		DryRun: opts.DryRun,
	}

	switch plan.Action {
	case VolumeActionNoop:
		return result, nil
	case VolumeActionConflict:
		return result, fmt.Errorf("refusing unsafe volume reconciliation for %s: %s", plan.Slot, plan.Reason)
	case VolumeActionCreate:
		if plan.Desired == nil {
			return nil, fmt.Errorf("create plan for %s has no desired state", plan.Slot)
		}
		if opts.DryRun {
			return result, nil
		}
		if err := client.CreateVolume(*plan.Desired); err != nil {
			return nil, err
		}
	case VolumeActionResize:
		if plan.Desired == nil {
			return nil, fmt.Errorf("resize plan for %s has no desired state", plan.Slot)
		}
		if opts.DryRun {
			return result, nil
		}
		if err := client.ResizeVolume(plan.Slot, plan.Desired.FileSizeBytes); err != nil {
			return nil, err
		}
	case VolumeActionDelete:
		if opts.DryRun {
			return result, nil
		}
		if err := client.DeleteVolume(plan.Slot); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown volume action %q", plan.Action)
	}

	result.Applied = true
	return result, nil
}

func observeVolume(client rds.RDSClient, slot string) (*rds.VolumeInfo, bool, error) {
	actual, err := client.GetVolume(slot)
	if err == nil {
		return actual, true, nil
	}

	var notFoundErr *rds.VolumeNotFoundError
	if errors.As(err, &notFoundErr) || errors.Is(err, utils.ErrVolumeNotFound) {
		return nil, false, nil
	}
	return nil, false, fmt.Errorf("failed to observe volume %s: %w", slot, err)
}

func conflictPlan(desired rds.CreateVolumeOptions, actual *rds.VolumeInfo, reason string) *VolumePlan {
	return &VolumePlan{
		Action:  VolumeActionConflict,
		Slot:    desired.Slot,
		Reason:  reason,
		Desired: &desired,
		Actual:  actual,
	}
}
