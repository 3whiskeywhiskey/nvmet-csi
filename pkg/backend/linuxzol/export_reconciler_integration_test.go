package linuxzol

import (
	"context"
	"testing"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/reconciler"
)

// countingHost wraps memoryHost to count RepairVolume invocations (to prove
// idempotency / no further mutations on second reconcile).
type countingHost struct {
	*memoryHost
	repairCalls int
}

func (h *countingHost) RepairVolume(ctx context.Context, spec VolumeSpec) error {
	h.repairCalls++
	return h.memoryHost.RepairVolume(ctx, spec)
}

func TestExportReconciler_RebootRecovery_RedGreen(t *testing.T) {
	ctx := context.Background()

	host := newMemoryHost()
	chost := &countingHost{memoryHost: host}
	be := newTestBackend(t, chost)

	// Seed two volumes that have backing zvol + metadata but export state lost
	// (post-reboot simulation: Exported=false, export fields cleared).
	// Use readyVolumeState then consistently clear export indicators.
	desired1 := testCreateRequest("reboot-vol-1", 10<<30)
	spec1 := be.specFromRequest(desired1)
	state1 := readyVolumeState(spec1)
	state1.Exported = false
	state1.NamespaceReady = false
	state1.ExportDevice = ""
	state1.PortLinkTarget = ""
	state1.PortEntryExists = false
	state1.PortAddress = ""
	state1.PortServiceID = ""
	state1.PortTransport = ""
	state1.PortAddressFam = ""
	state1.Status = "not-exported"
	host.volumes[desired1.ID] = state1

	desired2 := testCreateRequest("reboot-vol-2", 20<<30)
	spec2 := be.specFromRequest(desired2)
	state2 := readyVolumeState(spec2)
	state2.Exported = false
	state2.NamespaceReady = false
	state2.ExportDevice = ""
	state2.PortLinkTarget = ""
	state2.PortEntryExists = false
	state2.PortAddress = ""
	state2.PortServiceID = ""
	state2.PortTransport = ""
	state2.PortAddressFam = ""
	state2.Status = "not-exported"
	host.volumes[desired2.ID] = state2

	// Build desired from PV perspective (NQN/Port may be present or empty; here we include to match).
	des := []reconciler.DesiredExport{
		{VolumeID: desired1.ID, NQN: desired1.NVMETCPNQN, Port: desired1.NVMETCPPort},
		{VolumeID: desired2.ID, NQN: desired2.NVMETCPNQN, Port: desired2.NVMETCPPort},
	}
	tgt := reconciler.ExportTarget{
		Key:     "linux-zol-nvmet/203.0.113.10/tank",
		Backend: be, // *Backend satisfies reconciler.ExportBackend
		Desired: des,
	}
	lister := func(context.Context) ([]reconciler.ExportTarget, error) {
		return []reconciler.ExportTarget{tgt}, nil
	}

	rec, err := reconciler.NewExportReconciler(reconciler.ExportReconcilerConfig{
		ListTargets: lister,
		// defaults: no dry-run
	})
	if err != nil {
		t.Fatalf("NewExportReconciler() error = %v", err)
	}

	// First reconcile: should repair both (post-reboot case).
	report, err := rec.Reconcile(ctx)
	if err != nil {
		t.Fatalf("first Reconcile() error = %v", err)
	}
	if got := report.RepairedCount(); got != 2 {
		t.Fatalf("first RepairedCount() = %d, want 2; report=%+v", got, report)
	}
	tr := report.Targets[0]
	if len(tr.Repairs) != 2 {
		t.Fatalf("first len(Repairs)=%d, want 2", len(tr.Repairs))
	}
	for i, rep := range tr.Repairs {
		if !rep.Applied {
			t.Errorf("repair[%d] Applied=false, want true: %+v", i, rep)
		}
	}
	if chost.repairCalls != 2 {
		t.Errorf("host repairCalls after first = %d, want 2", chost.repairCalls)
	}

	// Verify both now report as exported (memoryHost.RepairVolume mutated them).
	for _, id := range []string{desired1.ID, desired2.ID} {
		v, err := be.GetVolume(ctx, id)
		if err != nil {
			t.Fatalf("GetVolume(%s) after repair: %v", id, err)
		}
		if !v.NVMETCPExport {
			t.Fatalf("after first reconcile %s not Exported: %+v", id, v)
		}
	}

	// Second reconcile: now idempotent, no repairs, no further host mutations.
	report2, err := rec.Reconcile(ctx)
	if err != nil {
		t.Fatalf("second Reconcile() error = %v", err)
	}
	if got := report2.RepairedCount(); got != 0 {
		t.Fatalf("second RepairedCount() = %d, want 0", got)
	}
	tr2 := report2.Targets[0]
	if len(tr2.Repairs) != 0 {
		t.Errorf("second repairs should be empty, got %+v", tr2.Repairs)
	}
	if tr2.ExportedCount != 2 {
		t.Errorf("second ExportedCount = %d, want 2", tr2.ExportedCount)
	}
	if chost.repairCalls != 2 {
		t.Errorf("host repairCalls after second = %d (no further mutations), want still 2", chost.repairCalls)
	}

	// Still exported.
	for _, id := range []string{desired1.ID, desired2.ID} {
		v, err := be.GetVolume(ctx, id)
		if err != nil || !v.NVMETCPExport {
			t.Fatalf("after second reconcile %s lost export: err=%v v=%+v", id, err, v)
		}
	}
}
