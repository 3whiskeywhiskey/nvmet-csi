package reconciler

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/backend"
)

type fakeExportBackend struct {
	name    string
	addr    string
	volumes []backend.Volume
	listErr error

	buildPlans map[string]*backend.VolumePlan
	buildErrs  map[string]error
	applyRes   map[string]*backend.VolumeApplyResult
	applyErrs  map[string]error

	buildCalls []backend.CreateVolumeRequest
	applyCalls []*backend.VolumePlan
}

func (f *fakeExportBackend) Name() string    { return f.name }
func (f *fakeExportBackend) Address() string { return f.addr }

func (f *fakeExportBackend) ListVolumes(ctx context.Context) ([]backend.Volume, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	cp := make([]backend.Volume, len(f.volumes))
	copy(cp, f.volumes)
	return cp, nil
}

func (f *fakeExportBackend) BuildEnsureVolumePlan(ctx context.Context, desired backend.CreateVolumeRequest) (*backend.VolumePlan, error) {
	f.buildCalls = append(f.buildCalls, desired)
	if err, ok := f.buildErrs[desired.ID]; ok && err != nil {
		return nil, err
	}
	if p, ok := f.buildPlans[desired.ID]; ok {
		return p, nil
	}
	return nil, errors.New("no plan scripted for " + desired.ID)
}

func (f *fakeExportBackend) ApplyVolumePlan(ctx context.Context, plan *backend.VolumePlan, opts backend.VolumeApplyOptions) (*backend.VolumeApplyResult, error) {
	f.applyCalls = append(f.applyCalls, plan)
	id := plan.VolumeID
	if err, ok := f.applyErrs[id]; ok && err != nil {
		return nil, err
	}
	if r, ok := f.applyRes[id]; ok {
		return r, nil
	}
	return &backend.VolumeApplyResult{
		Action:   plan.Action,
		VolumeID: id,
		Applied:  true,
	}, nil
}

func newTestReconciler(t *testing.T, lister ExportTargetLister, dryRun bool) *ExportReconciler {
	t.Helper()
	cfg := ExportReconcilerConfig{
		ListTargets: lister,
		DryRun:      dryRun,
		// CheckInterval will default
	}
	r, err := NewExportReconciler(cfg)
	if err != nil {
		t.Fatalf("NewExportReconciler() error = %v", err)
	}
	return r
}

func TestExportReconciler_Reconcile_PostRebootRepair(t *testing.T) {
	ctx := context.Background()
	fb := &fakeExportBackend{
		name: "linux-zol-nvmet",
		addr: "203.0.113.10",
		volumes: []backend.Volume{
			{ID: "vol-a", SizeBytes: 10 << 30, NVMETCPExport: false, NVMETCPNQN: "nqn.a", NVMETCPPort: 4420},
			{ID: "vol-b", SizeBytes: 20 << 30, NVMETCPExport: false, NVMETCPNQN: "nqn.b", NVMETCPPort: 4420},
		},
		buildPlans: map[string]*backend.VolumePlan{
			"vol-a": {Action: backend.VolumeActionRepair, VolumeID: "vol-a", Reason: "export incomplete"},
			"vol-b": {Action: backend.VolumeActionRepair, VolumeID: "vol-b", Reason: "export incomplete"},
		},
	}
	targets := []ExportTarget{{
		Key:     "linux-zol-nvmet/203.0.113.10/tank",
		Backend: fb,
		Desired: []DesiredExport{{VolumeID: "vol-a"}, {VolumeID: "vol-b"}},
	}}
	lister := func(context.Context) ([]ExportTarget, error) { return targets, nil }
	r := newTestReconciler(t, lister, false)

	report, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if got := report.RepairedCount(); got != 2 {
		t.Errorf("RepairedCount() = %d, want 2", got)
	}
	if len(report.Targets) != 1 {
		t.Fatalf("len(Targets) = %d, want 1", len(report.Targets))
	}
	tr := report.Targets[0]
	if len(tr.Repairs) != 2 {
		t.Fatalf("len(Repairs) = %d, want 2", len(tr.Repairs))
	}
	if !tr.Repairs[0].Applied || !tr.Repairs[1].Applied {
		t.Errorf("expected both repairs Applied=true, got %+v", tr.Repairs)
	}
	if tr.Repairs[0].Action != backend.VolumeActionRepair || tr.Repairs[1].Action != backend.VolumeActionRepair {
		t.Errorf("expected repair actions, got %+v", tr.Repairs)
	}
	if len(fb.buildCalls) != 2 {
		t.Errorf("BuildEnsure calls = %d, want 2", len(fb.buildCalls))
	}
	if len(fb.applyCalls) != 2 {
		t.Errorf("Apply calls = %d, want 2", len(fb.applyCalls))
	}
	if report.ConflictCount() != 0 {
		t.Errorf("ConflictCount() = %d, want 0", report.ConflictCount())
	}
}

func TestExportReconciler_Reconcile_Idempotency(t *testing.T) {
	ctx := context.Background()
	fb := &fakeExportBackend{
		name: "linux-zol-nvmet",
		addr: "203.0.113.10",
		volumes: []backend.Volume{
			{ID: "vol-a", SizeBytes: 10 << 30, NVMETCPExport: true, NVMETCPNQN: "nqn.a", NVMETCPPort: 4420},
			{ID: "vol-b", SizeBytes: 20 << 30, NVMETCPExport: true, NVMETCPNQN: "nqn.b", NVMETCPPort: 4420},
		},
		// no buildPlans: should not be called
	}
	targets := []ExportTarget{{
		Key:     "linux-zol-nvmet/203.0.113.10/tank",
		Backend: fb,
		Desired: []DesiredExport{{VolumeID: "vol-a"}, {VolumeID: "vol-b"}},
	}}
	lister := func(context.Context) ([]ExportTarget, error) { return targets, nil }
	r := newTestReconciler(t, lister, false)

	report, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	tr := report.Targets[0]
	if tr.ExportedCount != 2 {
		t.Errorf("ExportedCount = %d, want 2", tr.ExportedCount)
	}
	if report.RepairedCount() != 0 {
		t.Errorf("RepairedCount() = %d, want 0", report.RepairedCount())
	}
	if len(fb.buildCalls) != 0 {
		t.Errorf("BuildEnsure calls = %d, want 0", len(fb.buildCalls))
	}
	if len(fb.applyCalls) != 0 {
		t.Errorf("Apply calls = %d, want 0", len(fb.applyCalls))
	}
	if len(tr.Repairs) != 0 {
		t.Errorf("unexpected repairs: %+v", tr.Repairs)
	}
}

func TestExportReconciler_Reconcile_ConflictFromPlan(t *testing.T) {
	ctx := context.Background()
	fb := &fakeExportBackend{
		name: "linux-zol-nvmet",
		addr: "203.0.113.10",
		volumes: []backend.Volume{
			{ID: "vol-conf", SizeBytes: 10 << 30, NVMETCPExport: false, NVMETCPNQN: "nqn.conf", NVMETCPPort: 4420},
		},
		buildPlans: map[string]*backend.VolumePlan{
			"vol-conf": {Action: backend.VolumeActionConflict, VolumeID: "vol-conf", Reason: "port in use by other"},
		},
	}
	targets := []ExportTarget{{
		Key:     "linux-zol-nvmet/203.0.113.10/tank",
		Backend: fb,
		Desired: []DesiredExport{{VolumeID: "vol-conf"}},
	}}
	lister := func(context.Context) ([]ExportTarget, error) { return targets, nil }
	r := newTestReconciler(t, lister, false)

	report, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	tr := report.Targets[0]
	if len(tr.Repairs) != 1 {
		t.Fatalf("len(Repairs)=%d want 1", len(tr.Repairs))
	}
	rep := tr.Repairs[0]
	if rep.Action != backend.VolumeActionConflict {
		t.Errorf("repair Action=%s want conflict", rep.Action)
	}
	if rep.Applied {
		t.Error("conflict repair must not be Applied")
	}
	if len(fb.applyCalls) != 0 {
		t.Errorf("ApplyVolumePlan called %d times on conflict, want 0", len(fb.applyCalls))
	}
	if report.ConflictCount() == 0 {
		t.Error("ConflictCount() == 0, want >0")
	}
}

func TestExportReconciler_Reconcile_MissingBackingVolume(t *testing.T) {
	ctx := context.Background()
	fb := &fakeExportBackend{
		name:    "linux-zol-nvmet",
		addr:    "203.0.113.10",
		volumes: []backend.Volume{}, // no vol-miss
	}
	targets := []ExportTarget{{
		Key:     "linux-zol-nvmet/203.0.113.10/tank",
		Backend: fb,
		Desired: []DesiredExport{{VolumeID: "vol-miss"}},
	}}
	lister := func(context.Context) ([]ExportTarget, error) { return targets, nil }
	r := newTestReconciler(t, lister, false)

	report, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	tr := report.Targets[0]
	if len(tr.MissingBackingVolumes) != 1 || tr.MissingBackingVolumes[0] != "vol-miss" {
		t.Errorf("MissingBackingVolumes=%v want [vol-miss]", tr.MissingBackingVolumes)
	}
	if len(tr.Repairs) != 0 {
		t.Errorf("unexpected repairs for missing: %+v", tr.Repairs)
	}
	if len(fb.buildCalls) != 0 {
		t.Errorf("BuildEnsure called for missing backing, got %d calls", len(fb.buildCalls))
	}
	if report.ConflictCount() == 0 {
		t.Error("expected ConflictCount >0 due to missing")
	}
}

func TestExportReconciler_Reconcile_DryRun(t *testing.T) {
	ctx := context.Background()
	fb := &fakeExportBackend{
		name: "linux-zol-nvmet",
		addr: "203.0.113.10",
		volumes: []backend.Volume{
			{ID: "vol-dry", SizeBytes: 10 << 30, NVMETCPExport: false, NVMETCPNQN: "nqn.dry", NVMETCPPort: 4420},
		},
		buildPlans: map[string]*backend.VolumePlan{
			"vol-dry": {Action: backend.VolumeActionRepair, VolumeID: "vol-dry", Reason: "incomplete"},
		},
	}
	targets := []ExportTarget{{
		Key:     "linux-zol-nvmet/203.0.113.10/tank",
		Backend: fb,
		Desired: []DesiredExport{{VolumeID: "vol-dry"}},
	}}
	lister := func(context.Context) ([]ExportTarget, error) { return targets, nil }
	r := newTestReconciler(t, lister, true) // dry run

	report, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	tr := report.Targets[0]
	if len(tr.Repairs) != 1 {
		t.Fatalf("len(Repairs)=%d want 1", len(tr.Repairs))
	}
	rep := tr.Repairs[0]
	if rep.Applied {
		t.Error("dry-run repair must have Applied=false")
	}
	if rep.Action != backend.VolumeActionRepair {
		t.Errorf("action=%s want repair", rep.Action)
	}
	if len(fb.applyCalls) != 0 {
		t.Errorf("Apply called in dry-run: %d", len(fb.applyCalls))
	}
	if report.RepairedCount() != 0 {
		t.Errorf("RepairedCount in dryrun = %d want 0", report.RepairedCount())
	}
}

func TestExportReconciler_Reconcile_UnmanagedVolumes(t *testing.T) {
	ctx := context.Background()
	fb := &fakeExportBackend{
		name: "linux-zol-nvmet",
		addr: "203.0.113.10",
		volumes: []backend.Volume{
			{ID: "managed", SizeBytes: 10 << 30, NVMETCPExport: true, NVMETCPNQN: "nqn.m", NVMETCPPort: 4420},
			{ID: "orphan1", SizeBytes: 5 << 30, NVMETCPExport: true, NVMETCPNQN: "nqn.o", NVMETCPPort: 4420},
		},
	}
	targets := []ExportTarget{{
		Key:     "linux-zol-nvmet/203.0.113.10/tank",
		Backend: fb,
		Desired: []DesiredExport{{VolumeID: "managed"}},
	}}
	lister := func(context.Context) ([]ExportTarget, error) { return targets, nil }
	r := newTestReconciler(t, lister, false)

	report, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	tr := report.Targets[0]
	if len(tr.UnmanagedVolumes) != 1 || tr.UnmanagedVolumes[0] != "orphan1" {
		t.Errorf("UnmanagedVolumes=%v want [orphan1]", tr.UnmanagedVolumes)
	}
	// no mutations to unmanaged
	if len(fb.buildCalls) != 0 || len(fb.applyCalls) != 0 {
		t.Error("unexpected build/apply for unmanaged only case")
	}
}

func TestExportReconciler_Reconcile_ListVolumesError(t *testing.T) {
	ctx := context.Background()
	bad := &fakeExportBackend{
		name:    "bad",
		addr:    "1.1.1.1",
		listErr: errors.New("list failed"),
	}
	good := &fakeExportBackend{
		name: "good",
		addr: "2.2.2.2",
		volumes: []backend.Volume{
			{ID: "vol-good", SizeBytes: 1 << 30, NVMETCPExport: true, NVMETCPNQN: "nqn.g", NVMETCPPort: 4420},
		},
	}
	targets := []ExportTarget{
		{Key: "bad", Backend: bad, Desired: []DesiredExport{{VolumeID: "vol-bad"}}},
		{Key: "good", Backend: good, Desired: []DesiredExport{{VolumeID: "vol-good"}}},
	}
	lister := func(context.Context) ([]ExportTarget, error) { return targets, nil }
	r := newTestReconciler(t, lister, false)

	report, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	if len(report.Targets) != 2 {
		t.Fatalf("len(Targets)=%d want 2", len(report.Targets))
	}
	if report.Targets[0].Err == "" {
		t.Error("target 0 should have Err set")
	}
	if report.Targets[1].Err != "" {
		t.Error("target 1 should not have Err")
	}
	if report.Targets[1].ExportedCount != 1 {
		t.Error("good target not reconciled")
	}
}

func TestDesiredRequestFromObserved(t *testing.T) {
	makeVol := func(nqn string, port int, exported bool) *backend.Volume {
		return &backend.Volume{
			ID:            "v1",
			SizeBytes:     10 << 30,
			NVMETCPNQN:    nqn,
			NVMETCPPort:   port,
			NVMETCPExport: exported,
		}
	}

	tests := []struct {
		name     string
		desired  DesiredExport
		observed *backend.Volume
		want     backend.CreateVolumeRequest
		wantErr  string
	}{
		{
			name:     "PV provides NQN/port",
			desired:  DesiredExport{VolumeID: "v1", NQN: "nqn.pv", Port: 4420},
			observed: makeVol("", 0, false),
			want: backend.CreateVolumeRequest{
				ID: "v1", SizeBytes: 10 << 30, NVMETCPNQN: "nqn.pv", NVMETCPPort: 4420,
			},
		},
		{
			name:     "fallback to observed when PV empty",
			desired:  DesiredExport{VolumeID: "v1", NQN: "", Port: 0},
			observed: makeVol("nqn.obs", 4420, false),
			want: backend.CreateVolumeRequest{
				ID: "v1", SizeBytes: 10 << 30, NVMETCPNQN: "nqn.obs", NVMETCPPort: 4420,
			},
		},
		{
			name:     "PV NQN mismatch with backend",
			desired:  DesiredExport{VolumeID: "v1", NQN: "nqn.pv", Port: 4420},
			observed: makeVol("nqn.other", 4420, false),
			wantErr:  "PV records NQN \"nqn.pv\" but backend metadata records \"nqn.other\"",
		},
		{
			name:     "PV port mismatch with backend",
			desired:  DesiredExport{VolumeID: "v1", NQN: "nqn.pv", Port: 4420},
			observed: makeVol("nqn.pv", 9999, false),
			wantErr:  "PV records NVMe/TCP port 4420 but backend metadata records 9999",
		},
		{
			name:     "both empty NQN -> conflict",
			desired:  DesiredExport{VolumeID: "v1", NQN: "", Port: 0},
			observed: makeVol("", 0, false),
			wantErr:  "no NQN recorded on PV or backend metadata",
		},
		{
			name:     "empty port after fallback",
			desired:  DesiredExport{VolumeID: "v1", NQN: "nqn.ok", Port: 0},
			observed: makeVol("nqn.ok", 0, false),
			wantErr:  "no NVMe/TCP port recorded on PV or backend metadata",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := desiredRequestFromObserved(tt.desired, tt.observed)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestExportReconciler_Reconcile_NoopRace(t *testing.T) {
	ctx := context.Background()
	fb := &fakeExportBackend{
		name: "linux-zol-nvmet",
		addr: "203.0.113.10",
		volumes: []backend.Volume{
			{ID: "vol-race", SizeBytes: 10 << 30, NVMETCPExport: false, NVMETCPNQN: "nqn.r", NVMETCPPort: 4420},
		},
		buildPlans: map[string]*backend.VolumePlan{
			"vol-race": {Action: backend.VolumeActionNoop, VolumeID: "vol-race", Reason: "race won by other"},
		},
	}
	targets := []ExportTarget{{
		Key:     "linux-zol-nvmet/203.0.113.10/tank",
		Backend: fb,
		Desired: []DesiredExport{{VolumeID: "vol-race"}},
	}}
	lister := func(context.Context) ([]ExportTarget, error) { return targets, nil }
	r := newTestReconciler(t, lister, false)

	report, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	tr := report.Targets[0]
	if tr.ExportedCount != 1 {
		t.Errorf("ExportedCount = %d (noop counts as exported), want 1", tr.ExportedCount)
	}
	if len(tr.Repairs) != 0 {
		t.Errorf("noop should not record repair: %+v", tr.Repairs)
	}
	if len(fb.applyCalls) != 0 {
		t.Errorf("noop must not call apply, got %d", len(fb.applyCalls))
	}
	if report.RepairedCount() != 0 {
		t.Error("noop not counted as repaired")
	}
}

// verify conflict from desiredRequest mismatch leads to repair conflict and no build/apply
func TestExportReconciler_Reconcile_ConflictFromDesiredMismatch(t *testing.T) {
	ctx := context.Background()
	fb := &fakeExportBackend{
		name: "linux-zol-nvmet",
		addr: "203.0.113.10",
		volumes: []backend.Volume{
			{ID: "vol-mis", SizeBytes: 10 << 30, NVMETCPExport: false, NVMETCPNQN: "nqn.real", NVMETCPPort: 4420},
		},
		// no need for plans, since desiredRequest errs before Build
	}
	targets := []ExportTarget{{
		Key:     "linux-zol-nvmet/203.0.113.10/tank",
		Backend: fb,
		Desired: []DesiredExport{{VolumeID: "vol-mis", NQN: "nqn.wrong", Port: 4420}},
	}}
	lister := func(context.Context) ([]ExportTarget, error) { return targets, nil }
	r := newTestReconciler(t, lister, false)

	report, err := r.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
	tr := report.Targets[0]
	if len(tr.Repairs) != 1 {
		t.Fatalf("len(Repairs)=%d want 1", len(tr.Repairs))
	}
	rep := tr.Repairs[0]
	if rep.Action != backend.VolumeActionConflict {
		t.Errorf("Action=%s want conflict", rep.Action)
	}
	if !stringsContains(rep.Reason, "NQN") {
		t.Errorf("reason should mention NQN mismatch: %s", rep.Reason)
	}
	if len(fb.buildCalls) != 0 {
		t.Errorf("BuildEnsure must not be called on desiredRequest conflict, got %d", len(fb.buildCalls))
	}
	if len(fb.applyCalls) != 0 {
		t.Error("Apply must not be called on conflict")
	}
	if report.ConflictCount() == 0 {
		t.Error("ConflictCount must >0")
	}
}

func stringsContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || (len(s) > 0 && searchIn(s, sub)))
}

func searchIn(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
