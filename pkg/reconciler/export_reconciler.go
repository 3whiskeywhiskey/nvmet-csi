// Export reconciliation rebuilds missing NVMe/TCP target exports for volumes
// that Kubernetes says should exist.
//
// The kernel nvmet configfs tree is runtime state: a target host reboot wipes
// every port, subsystem, and namespace while the backing zvols (and the
// desired-state metadata stored on them as ZFS user properties) survive.
// Without this reconciler the controller only rebuilds exports when a CSI
// CreateVolume call happens to arrive for the volume, which never happens for
// already-provisioned PVs — so retained volumes stay unmountable until an
// operator repairs configfs by hand.
//
// The reconciler is deliberately repair-only:
//
//   - Desired set   = PVs bound to this CSI driver (per backend selection).
//   - Actual set    = Backend.ListVolumes() (ZFS properties + configfs probes).
//   - Desired specs are built from *observed* volume size, so the resulting
//     plans can only be Noop, Repair, or Conflict. The reconciler structurally
//     cannot create, resize, or delete backend volumes.
//   - Conflicts are surfaced as explicit repair-required errors, never
//     rewritten. Volumes without a PV are reported, never touched. PVs without
//     a backing volume are reported loudly, never recreated.
package reconciler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/backend"
)

const (
	// DefaultExportCheckInterval is how often exports are re-verified after
	// the startup pass.
	DefaultExportCheckInterval = 10 * time.Minute
)

// ExportBackend is the narrow backend surface the export reconciler needs.
// *linuxzol.Backend (and any backend.Backend) satisfies it.
type ExportBackend interface {
	Name() string
	Address() string
	ListVolumes(ctx context.Context) ([]backend.Volume, error)
	BuildEnsureVolumePlan(ctx context.Context, desired backend.CreateVolumeRequest) (*backend.VolumePlan, error)
	ApplyVolumePlan(ctx context.Context, plan *backend.VolumePlan, opts backend.VolumeApplyOptions) (*backend.VolumeApplyResult, error)
}

// DesiredExport names one volume that must be exported, as recorded in the
// Kubernetes PV. NQN and Port are optional cross-checks: when empty the
// durable ZFS metadata observed on the backend is authoritative.
type DesiredExport struct {
	VolumeID string
	NQN      string
	Port     int
}

// ExportTarget binds one backend instance to the set of volumes Kubernetes
// expects it to export.
type ExportTarget struct {
	// Key identifies the backend for logs and reports,
	// e.g. "linux-zol-nvmet/198.51.100.23/tank".
	Key     string
	Backend ExportBackend
	Desired []DesiredExport
}

// ExportTargetLister returns the current desired export topology. It is
// re-evaluated every cycle so newly provisioned volumes and StorageClasses are
// picked up without a restart.
type ExportTargetLister func(ctx context.Context) ([]ExportTarget, error)

// ExportReconcilerConfig configures the export reconciler.
type ExportReconcilerConfig struct {
	ListTargets ExportTargetLister

	// CheckInterval between periodic verification passes.
	CheckInterval time.Duration

	// RunOnStart runs a reconciliation immediately on Start. This is the
	// entire point of the reconciler (reboot recovery), so callers should
	// leave it enabled outside of tests.
	RunOnStart bool

	// DryRun reports planned repairs without applying them.
	DryRun bool

	// LockVolume, when set, serializes per-volume repair with in-flight CSI
	// operations (Create/Delete/Expand) so the reconciler never observes or
	// mutates a volume mid-operation. Must return the unlock func.
	LockVolume func(volumeID string) (unlock func())

	Enabled bool
}

// ExportRepair records one repair decision for one volume.
type ExportRepair struct {
	VolumeID string
	Action   backend.VolumeAction
	Reason   string
	Applied  bool
	Err      string
}

// ExportTargetReport summarizes one backend's reconciliation outcome.
type ExportTargetReport struct {
	Key           string
	DesiredCount  int
	ObservedCount int
	ExportedCount int

	// Repairs lists every non-noop decision (applied, dry-run, conflicted,
	// or failed).
	Repairs []ExportRepair

	// MissingBackingVolumes are PV-backed volume IDs with no backend volume
	// at all. This is a data-loss-class condition the reconciler must never
	// paper over by creating an empty volume.
	MissingBackingVolumes []string

	// UnmanagedVolumes exist on the backend without a matching PV. They are
	// candidates for orphan cleanup and are never exported or deleted here.
	UnmanagedVolumes []string

	// Err is set when the target could not be reconciled at all
	// (e.g. ListVolumes failed).
	Err string
}

// ExportReconcileReport is the outcome of one reconciliation cycle.
type ExportReconcileReport struct {
	ObservedAt time.Time
	Targets    []ExportTargetReport
}

// RepairedCount returns the number of successfully applied repairs.
func (r *ExportReconcileReport) RepairedCount() int {
	count := 0
	for _, target := range r.Targets {
		for _, repair := range target.Repairs {
			if repair.Applied {
				count++
			}
		}
	}
	return count
}

// ConflictCount returns the number of volumes needing operator attention.
func (r *ExportReconcileReport) ConflictCount() int {
	count := 0
	for _, target := range r.Targets {
		for _, repair := range target.Repairs {
			if repair.Action == backend.VolumeActionConflict || repair.Err != "" {
				count++
			}
		}
		count += len(target.MissingBackingVolumes)
	}
	return count
}

// ExportReconciler keeps backend NVMe/TCP exports converged with the desired
// state recorded in Kubernetes PVs.
type ExportReconciler struct {
	config ExportReconcilerConfig

	stopCh    chan struct{}
	triggerCh chan struct{}
	wg        sync.WaitGroup
}

// NewExportReconciler validates config and creates the reconciler.
func NewExportReconciler(config ExportReconcilerConfig) (*ExportReconciler, error) {
	if config.ListTargets == nil {
		return nil, fmt.Errorf("ListTargets is required")
	}
	if config.CheckInterval <= 0 {
		config.CheckInterval = DefaultExportCheckInterval
	}
	return &ExportReconciler{
		config:    config,
		stopCh:    make(chan struct{}),
		triggerCh: make(chan struct{}, 1),
	}, nil
}

// Start begins the reconciliation loop.
func (r *ExportReconciler) Start(ctx context.Context) error {
	if !r.config.Enabled {
		klog.Info("Export reconciler is disabled")
		return nil
	}

	klog.Infof("Starting export reconciler (interval=%v, dry_run=%v, run_on_start=%v)",
		r.config.CheckInterval, r.config.DryRun, r.config.RunOnStart)

	r.wg.Add(1)
	go r.run(ctx)
	return nil
}

// Stop stops the reconciliation loop.
func (r *ExportReconciler) Stop() {
	if !r.config.Enabled {
		return
	}
	klog.Info("Stopping export reconciler")
	close(r.stopCh)
	r.wg.Wait()
	klog.Info("Export reconciler stopped")
}

// TriggerReconcile requests an immediate reconciliation pass. Non-blocking;
// coalesces with an already-pending trigger.
func (r *ExportReconciler) TriggerReconcile() {
	select {
	case r.triggerCh <- struct{}{}:
	default:
	}
}

func (r *ExportReconciler) run(ctx context.Context) {
	defer r.wg.Done()

	ticker := time.NewTicker(r.config.CheckInterval)
	defer ticker.Stop()

	if r.config.RunOnStart {
		r.reconcileAndLog(ctx)
	} else {
		klog.Infof("Export reconciler startup pass skipped; first pass scheduled in %v", r.config.CheckInterval)
	}

	for {
		select {
		case <-ticker.C:
			r.reconcileAndLog(ctx)
		case <-r.triggerCh:
			r.reconcileAndLog(ctx)
		case <-r.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (r *ExportReconciler) reconcileAndLog(ctx context.Context) {
	report, err := r.Reconcile(ctx)
	if err != nil {
		klog.Errorf("Export reconciliation failed: %v", err)
		return
	}
	if conflicts := report.ConflictCount(); conflicts > 0 {
		klog.Errorf("Export reconciliation needs operator attention: %d volume(s) conflicted or missing backing storage", conflicts)
	}
}

// Reconcile performs one full reconciliation cycle and returns the report.
func (r *ExportReconciler) Reconcile(ctx context.Context) (*ExportReconcileReport, error) {
	start := time.Now()
	targets, err := r.config.ListTargets(ctx)
	if err != nil {
		return nil, fmt.Errorf("list export targets: %w", err)
	}

	report := &ExportReconcileReport{ObservedAt: start}
	for _, target := range targets {
		report.Targets = append(report.Targets, r.reconcileTarget(ctx, target))
	}

	klog.Infof("Export reconciliation cycle complete (duration=%v, targets=%d, repaired=%d, conflicts=%d)",
		time.Since(start), len(report.Targets), report.RepairedCount(), report.ConflictCount())
	return report, nil
}

func (r *ExportReconciler) reconcileTarget(ctx context.Context, target ExportTarget) ExportTargetReport {
	targetReport := ExportTargetReport{
		Key:          target.Key,
		DesiredCount: len(target.Desired),
	}
	if target.Backend == nil {
		targetReport.Err = "no backend resolved"
		return targetReport
	}

	observed, err := target.Backend.ListVolumes(ctx)
	if err != nil {
		targetReport.Err = fmt.Sprintf("list backend volumes: %v", err)
		klog.Errorf("Export reconciliation for %s failed: %s", target.Key, targetReport.Err)
		return targetReport
	}
	targetReport.ObservedCount = len(observed)

	observedByID := make(map[string]*backend.Volume, len(observed))
	for i := range observed {
		observedByID[observed[i].ID] = &observed[i]
	}

	desiredIDs := make(map[string]struct{}, len(target.Desired))
	for _, desired := range target.Desired {
		desiredIDs[desired.VolumeID] = struct{}{}

		volume, ok := observedByID[desired.VolumeID]
		if !ok {
			klog.Errorf("Export reconciliation for %s: volume %s has a PV but no backing volume on the target; refusing to create storage, operator intervention required",
				target.Key, desired.VolumeID)
			targetReport.MissingBackingVolumes = append(targetReport.MissingBackingVolumes, desired.VolumeID)
			continue
		}
		if volume.NVMETCPExport {
			targetReport.ExportedCount++
			continue
		}

		repair := r.repairVolume(ctx, target, desired, volume)
		if repair == nil {
			targetReport.ExportedCount++
			continue
		}
		targetReport.Repairs = append(targetReport.Repairs, *repair)
	}

	for _, volume := range observed {
		if _, ok := desiredIDs[volume.ID]; !ok {
			targetReport.UnmanagedVolumes = append(targetReport.UnmanagedVolumes, volume.ID)
		}
	}
	if len(targetReport.UnmanagedVolumes) > 0 {
		klog.V(2).Infof("Export reconciliation for %s: %d backend volume(s) without a PV left untouched: %v",
			target.Key, len(targetReport.UnmanagedVolumes), targetReport.UnmanagedVolumes)
	}
	return targetReport
}

// repairVolume plans and (unless dry-run) applies the export repair for one
// unexported volume. Returns nil when the volume turned out to be healthy.
func (r *ExportReconciler) repairVolume(ctx context.Context, target ExportTarget, desired DesiredExport, observed *backend.Volume) *ExportRepair {
	if r.config.LockVolume != nil {
		unlock := r.config.LockVolume(desired.VolumeID)
		defer unlock()
	}

	spec, err := desiredRequestFromObserved(desired, observed)
	if err != nil {
		return &ExportRepair{
			VolumeID: desired.VolumeID,
			Action:   backend.VolumeActionConflict,
			Reason:   err.Error(),
			Err:      err.Error(),
		}
	}

	plan, err := target.Backend.BuildEnsureVolumePlan(ctx, spec)
	if err != nil {
		return &ExportRepair{
			VolumeID: desired.VolumeID,
			Err:      fmt.Sprintf("build repair plan: %v", err),
		}
	}

	switch plan.Action {
	case backend.VolumeActionNoop:
		// Export converged between ListVolumes and the per-volume observe.
		return nil
	case backend.VolumeActionRepair:
		repair := &ExportRepair{VolumeID: desired.VolumeID, Action: plan.Action, Reason: plan.Reason}
		if r.config.DryRun {
			klog.Infof("Export reconciliation for %s (dry-run): would repair %s: %s (commands=%d)",
				target.Key, desired.VolumeID, plan.Reason, len(plan.Commands))
			return repair
		}
		if _, err := target.Backend.ApplyVolumePlan(ctx, plan, backend.VolumeApplyOptions{}); err != nil {
			repair.Err = fmt.Sprintf("apply repair: %v", err)
			klog.Errorf("Export reconciliation for %s: repairing %s failed: %v", target.Key, desired.VolumeID, err)
			return repair
		}
		repair.Applied = true
		klog.Infof("Export reconciliation for %s: repaired export for %s (%s)", target.Key, desired.VolumeID, plan.Reason)
		return repair
	case backend.VolumeActionConflict:
		klog.Errorf("Export reconciliation for %s: volume %s is in conflict, refusing repair: %s",
			target.Key, desired.VolumeID, plan.Reason)
		return &ExportRepair{VolumeID: desired.VolumeID, Action: plan.Action, Reason: plan.Reason}
	default:
		// Desired specs are derived from observed size and metadata, so
		// Create/Resize/Delete indicate a planner invariant violation.
		reason := fmt.Sprintf("unexpected %s plan during export repair: %s", plan.Action, plan.Reason)
		klog.Errorf("Export reconciliation for %s: volume %s: %s", target.Key, desired.VolumeID, reason)
		return &ExportRepair{VolumeID: desired.VolumeID, Action: plan.Action, Reason: reason, Err: reason}
	}
}

// desiredRequestFromObserved builds the repair-only desired spec: size always
// mirrors the observed volume, and NQN/port come from the PV when recorded,
// falling back to the durable metadata observed on the backend. A PV/backend
// disagreement is a conflict, not a rewrite.
func desiredRequestFromObserved(desired DesiredExport, observed *backend.Volume) (backend.CreateVolumeRequest, error) {
	nqn := desired.NQN
	if nqn == "" {
		nqn = observed.NVMETCPNQN
	} else if observed.NVMETCPNQN != "" && observed.NVMETCPNQN != nqn {
		return backend.CreateVolumeRequest{}, fmt.Errorf(
			"PV records NQN %q but backend metadata records %q", nqn, observed.NVMETCPNQN)
	}

	port := desired.Port
	if port <= 0 {
		port = observed.NVMETCPPort
	} else if observed.NVMETCPPort > 0 && observed.NVMETCPPort != port {
		return backend.CreateVolumeRequest{}, fmt.Errorf(
			"PV records NVMe/TCP port %d but backend metadata records %d", port, observed.NVMETCPPort)
	}

	if nqn == "" {
		return backend.CreateVolumeRequest{}, fmt.Errorf("no NQN recorded on PV or backend metadata")
	}
	if port <= 0 {
		return backend.CreateVolumeRequest{}, fmt.Errorf("no NVMe/TCP port recorded on PV or backend metadata")
	}

	return backend.CreateVolumeRequest{
		ID:          desired.VolumeID,
		SizeBytes:   observed.SizeBytes,
		NVMETCPNQN:  nqn,
		NVMETCPPort: port,
	}, nil
}
