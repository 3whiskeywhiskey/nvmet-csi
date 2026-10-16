package linuxzol

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/backend"
)

func TestBuildEnsureVolumePlanCreateAndApply(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)

	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionCreate {
		t.Fatalf("expected create plan, got %s", plan.Action)
	}
	if plan.Desired.Path != "/dev/zvol/tank/nvmet/pvc-test-volume" {
		t.Fatalf("expected normalized zvol path, got %q", plan.Desired.Path)
	}

	result, err := storageBackend.ApplyVolumePlan(context.Background(), plan, backend.VolumeApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyVolumePlan() error = %v", err)
	}
	if !result.Applied {
		t.Fatal("expected plan to be applied")
	}

	volume, err := storageBackend.GetVolume(context.Background(), desired.ID)
	if err != nil {
		t.Fatalf("GetVolume() error = %v", err)
	}
	if volume.Path != "/dev/zvol/tank/nvmet/pvc-test-volume" || !volume.NVMETCPExport {
		t.Fatalf("unexpected volume after create: %+v", volume)
	}
}

func TestApplyCreatePlanRepairsOwnedPartialStateAfterCreateFailure(t *testing.T) {
	host := &partialCreateHost{
		memoryHost: newMemoryHost(),
		createErr:  errors.New("namespace enable failed"),
	}
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)

	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionCreate {
		t.Fatalf("expected create plan, got %s", plan.Action)
	}

	result, err := storageBackend.ApplyVolumePlan(context.Background(), plan, backend.VolumeApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyVolumePlan() should repair owned partial create state, got %v", err)
	}
	if !result.Applied {
		t.Fatal("expected repaired create plan to be applied")
	}

	volume, err := storageBackend.GetVolume(context.Background(), desired.ID)
	if err != nil {
		t.Fatalf("GetVolume() error = %v", err)
	}
	if !volume.NVMETCPExport || volume.Status != "ready" {
		t.Fatalf("expected partial create to be repaired, got %+v", volume)
	}
}

func TestApplyCreatePlanRefusesUnsafePartialStateAfterCreateFailure(t *testing.T) {
	host := &partialCreateHost{
		memoryHost: newMemoryHost(),
		createErr:  errors.New("namespace enable failed"),
		mut: func(state *VolumeState) {
			state.OwnerVolumeID = "other-volume"
		},
	}
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)

	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}

	_, err = storageBackend.ApplyVolumePlan(context.Background(), plan, backend.VolumeApplyOptions{})
	if err == nil {
		t.Fatal("expected unsafe partial create state to fail")
	}
	if !strings.Contains(err.Error(), "namespace enable failed") {
		t.Fatalf("expected original create error in failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "unsafe to repair") {
		t.Fatalf("expected unsafe repair error, got %v", err)
	}
}

func TestBuildEnsureVolumePlanNoopResizeAndConflict(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	mustApplyPlan(t, storageBackend, desired)

	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionNoop {
		t.Fatalf("expected noop plan, got %s", plan.Action)
	}

	larger := desired
	larger.SizeBytes = 12 << 30
	plan, err = storageBackend.BuildEnsureVolumePlan(context.Background(), larger)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionResize {
		t.Fatalf("expected resize plan, got %s", plan.Action)
	}

	conflicting := desired
	conflicting.NVMETCPPort = 4421
	plan, err = storageBackend.BuildEnsureVolumePlan(context.Background(), conflicting)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionConflict {
		t.Fatalf("expected conflict plan, got %s", plan.Action)
	}
}

func TestBuildEnsureVolumePlanAcceptsRelativeConfigFSPortLink(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)

	host.volumes[desired.ID] = readyVolumeState(spec)
	state := host.volumes[desired.ID]
	state.PortLinkTarget = "../../../../nvmet/subsystems/" + spec.NQN
	host.volumes[desired.ID] = state

	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionNoop {
		t.Fatalf("expected noop for relative configfs symlink, got %s: %s", plan.Action, plan.Reason)
	}
}

func TestBuildEnsureVolumePlanRepairsIncompleteExport(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)

	host.volumes[desired.ID] = VolumeState{
		ID:             spec.ID,
		OwnerVolumeID:  spec.ID,
		Dataset:        spec.Dataset,
		DevicePath:     spec.DevicePath,
		SizeBytes:      spec.SizeBytes,
		NQN:            spec.NQN,
		Port:           spec.Port,
		Exported:       false,
		NamespaceReady: false,
		Status:         "not-exported",
	}

	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionRepair {
		t.Fatalf("expected repair plan, got %s", plan.Action)
	}
	if !strings.Contains(plan.Reason, "nvmet export is incomplete") {
		t.Fatalf("unexpected repair reason: %q", plan.Reason)
	}

	result, err := storageBackend.ApplyVolumePlan(context.Background(), plan, backend.VolumeApplyOptions{})
	if err != nil {
		t.Fatalf("ApplyVolumePlan() error = %v", err)
	}
	if !result.Applied {
		t.Fatal("expected repair to be applied")
	}

	volume, err := storageBackend.GetVolume(context.Background(), desired.ID)
	if err != nil {
		t.Fatalf("GetVolume() error = %v", err)
	}
	if !volume.NVMETCPExport {
		t.Fatalf("expected repaired export, got %+v", volume)
	}
}

func TestBuildEnsureVolumePlanHandlesPartialOwnedExports(t *testing.T) {
	storageBackend := newTestBackend(t, newMemoryHost())
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)
	expectedSubsystem := defaultConfigFSRoot + "/subsystems/" + spec.NQN

	tests := []struct {
		name string
		mut  func(*VolumeState)
	}{
		{
			name: "namespace absent",
			mut:  func(state *VolumeState) {},
		},
		{
			name: "namespace disabled after device path write",
			mut: func(state *VolumeState) {
				state.ExportDevice = spec.DevicePath
				state.PortLinkTarget = expectedSubsystem
			},
		},
		{
			name: "namespace ready but port symlink absent",
			mut: func(state *VolumeState) {
				state.NamespaceReady = true
				state.ExportDevice = spec.DevicePath
				state.PortAddress = "203.0.113.10"
				state.PortServiceID = "4420"
				state.PortTransport = "tcp"
				state.PortAddressFam = "ipv4"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := newMemoryHost()
			storageBackend := newTestBackend(t, host)
			state := VolumeState{
				ID:            spec.ID,
				OwnerVolumeID: spec.ID,
				Dataset:       spec.Dataset,
				DevicePath:    spec.DevicePath,
				SizeBytes:     spec.SizeBytes,
				NQN:           spec.NQN,
				Port:          spec.Port,
				Status:        "not-exported",
			}
			tt.mut(&state)
			host.volumes[desired.ID] = state

			plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
			if err != nil {
				t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
			}
			if plan.Action != backend.VolumeActionRepair {
				t.Fatalf("expected repair plan, got %s: %s", plan.Action, plan.Reason)
			}
		})
	}
}

func TestBuildEnsureVolumePlanConflictsOnUnownedZvol(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)

	for name, ownerVolumeID := range map[string]string{
		"missing owner metadata": "",
		"wrong owner metadata":   "other-volume",
	} {
		t.Run(name, func(t *testing.T) {
			host.volumes[desired.ID] = VolumeState{
				ID:            spec.ID,
				OwnerVolumeID: ownerVolumeID,
				Dataset:       spec.Dataset,
				DevicePath:    spec.DevicePath,
				SizeBytes:     spec.SizeBytes,
				NQN:           spec.NQN,
				Port:          spec.Port,
				Status:        "not-exported",
			}

			plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
			if err != nil {
				t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
			}
			if plan.Action != backend.VolumeActionConflict {
				t.Fatalf("expected conflict plan, got %s", plan.Action)
			}
			if !strings.Contains(plan.Reason, "owner volume ID") {
				t.Fatalf("unexpected conflict reason: %q", plan.Reason)
			}
		})
	}
}

func TestBuildEnsureVolumePlanConflictsOnWrongExportDevice(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)

	host.volumes[desired.ID] = VolumeState{
		ID:            spec.ID,
		OwnerVolumeID: spec.ID,
		Dataset:       spec.Dataset,
		DevicePath:    spec.DevicePath,
		SizeBytes:     spec.SizeBytes,
		NQN:           spec.NQN,
		Port:          spec.Port,
		ExportDevice:  "/dev/zvol/tank/nvmet/other-volume",
		Status:        "not-exported",
	}

	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionConflict {
		t.Fatalf("expected conflict plan, got %s", plan.Action)
	}
	if !strings.Contains(plan.Reason, "namespace device path differs") {
		t.Fatalf("unexpected conflict reason: %q", plan.Reason)
	}
}

func TestBuildEnsureVolumePlanConflictsOnWrongPortAddress(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)

	host.volumes[desired.ID] = VolumeState{
		ID:             spec.ID,
		OwnerVolumeID:  spec.ID,
		Dataset:        spec.Dataset,
		DevicePath:     spec.DevicePath,
		SizeBytes:      spec.SizeBytes,
		NQN:            spec.NQN,
		Port:           spec.Port,
		ExportDevice:   spec.DevicePath,
		PortLinkTarget: defaultConfigFSRoot + "/subsystems/" + spec.NQN,
		PortAddress:    "203.0.113.99",
	}

	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionConflict {
		t.Fatalf("expected conflict plan, got %s", plan.Action)
	}
	if !strings.Contains(plan.Reason, "target address") {
		t.Fatalf("unexpected conflict reason: %q", plan.Reason)
	}
}

func TestApplyVolumePlanDryRunDoesNotMutate(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}

	result, err := storageBackend.ApplyVolumePlan(context.Background(), plan, backend.VolumeApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("ApplyVolumePlan() error = %v", err)
	}
	if result.Applied {
		t.Fatal("dry-run result should not report applied")
	}
	if _, err := storageBackend.GetVolume(context.Background(), desired.ID); !backend.IsVolumeNotFound(err) {
		t.Fatalf("expected volume to remain absent after dry-run, got %v", err)
	}
}

func TestCommandHostPlansReportCommands(t *testing.T) {
	recorder := &recordingExecutor{
		outputs: map[string]string{},
		errs: map[string]error{
			"zfs get -H -p -o property,value " + zfsObservedProperties + " tank/nvmet/pvc-test-volume": errors.New("cannot open 'tank/nvmet/pvc-test-volume': dataset does not exist"),
		},
	}
	commandHost, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10", ConfigFSRoot: "/config/nvmet"}, recorder)
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}
	storageBackend := newTestBackend(t, commandHost)
	desired := testCreateRequest("pvc-test-volume", 10<<30)

	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Backend != backend.TypeLinuxZOLNVMET {
		t.Fatalf("expected backend metadata %q, got %q", backend.TypeLinuxZOLNVMET, plan.Backend)
	}
	if plan.Action != backend.VolumeActionCreate {
		t.Fatalf("expected create plan, got %s", plan.Action)
	}
	if len(plan.Commands) != 15 {
		t.Fatalf("expected 15 create commands, got %d: %v", len(plan.Commands), plan.Commands)
	}
	if plan.Commands[0] != "mkdir -p /config/nvmet/ports/4420/subsystems" {
		t.Fatalf("unexpected first command: %q", plan.Commands[0])
	}
	if plan.Commands[5] != "zfs create -V 10737418240 -o volmode=dev -o org.nvmet-csi:volume-id=pvc-test-volume -o org.nvmet-csi:nqn=nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume -o org.nvmet-csi:port=4420 tank/nvmet/pvc-test-volume" {
		t.Fatalf("unexpected zfs create command: %q", plan.Commands[5])
	}

	result, err := storageBackend.ApplyVolumePlan(context.Background(), plan, backend.VolumeApplyOptions{DryRun: true})
	if err != nil {
		t.Fatalf("ApplyVolumePlan(dry-run) error = %v", err)
	}
	if result.Applied {
		t.Fatal("dry-run result should not report applied")
	}
	if result.Backend != backend.TypeLinuxZOLNVMET || result.Reason != plan.Reason {
		t.Fatalf("unexpected result metadata: %+v", result)
	}
	if !reflect.DeepEqual(result.Commands, plan.Commands) {
		t.Fatalf("dry-run commands differ:\nplan=%v\nresult=%v", plan.Commands, result.Commands)
	}
}

func TestCommandHostPlansReportRepairResizeAndDeleteCommands(t *testing.T) {
	nqn := "nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume"
	state := VolumeState{
		ID:              "pvc-test-volume",
		OwnerVolumeID:   "pvc-test-volume",
		Dataset:         "tank/nvmet/pvc-test-volume",
		DevicePath:      "/dev/zvol/tank/nvmet/pvc-test-volume",
		SizeBytes:       10 << 30,
		NQN:             nqn,
		Port:            4420,
		ExportDevice:    "/dev/zvol/tank/nvmet/pvc-test-volume",
		PortEntryExists: true,
		PortLinkTarget:  defaultConfigFSRoot + "/subsystems/" + nqn,
	}
	host := newMemoryHost()
	storageBackend := newTestBackend(t, &planningMemoryHost{memoryHost: host})
	desired := testCreateRequest(state.ID, state.SizeBytes)

	host.volumes[state.ID] = state
	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionRepair {
		t.Fatalf("expected repair plan, got %s", plan.Action)
	}
	if len(plan.Commands) == 0 || !strings.Contains(plan.Commands[len(plan.Commands)-1], "refusing to rewrite nvmet port symlink") {
		t.Fatalf("expected repair command report, got %v", plan.Commands)
	}

	larger := desired
	larger.SizeBytes = 12 << 30
	host.volumes[state.ID] = VolumeState{
		ID:              state.ID,
		OwnerVolumeID:   state.OwnerVolumeID,
		Dataset:         state.Dataset,
		DevicePath:      state.DevicePath,
		SizeBytes:       state.SizeBytes,
		NQN:             state.NQN,
		Port:            state.Port,
		Exported:        true,
		NamespaceReady:  true,
		ExportDevice:    state.DevicePath,
		PortEntryExists: true,
		PortLinkTarget:  defaultConfigFSRoot + "/subsystems/" + state.NQN,
		PortAddress:     "203.0.113.10",
		PortServiceID:   "4420",
		PortTransport:   "tcp",
		PortAddressFam:  "ipv4",
		Status:          "ready",
	}
	plan, err = storageBackend.BuildEnsureVolumePlan(context.Background(), larger)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan(resize) error = %v", err)
	}
	if plan.Action != backend.VolumeActionResize {
		t.Fatalf("expected resize plan, got %s", plan.Action)
	}
	if len(plan.Commands) != 1 || plan.Commands[0] != "zfs set volsize=12884901888 tank/nvmet/pvc-test-volume" {
		t.Fatalf("unexpected resize command report: %v", plan.Commands)
	}

	plan, err = storageBackend.BuildDeleteVolumePlan(context.Background(), state.ID)
	if err != nil {
		t.Fatalf("BuildDeleteVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionDelete {
		t.Fatalf("expected delete plan, got %s", plan.Action)
	}
	if len(plan.Commands) != 5 || plan.Commands[len(plan.Commands)-1] != "zfs destroy tank/nvmet/pvc-test-volume" {
		t.Fatalf("unexpected delete command report: %v", plan.Commands)
	}
}

func TestBuildDeleteVolumePlanIdempotent(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)

	plan, err := storageBackend.BuildDeleteVolumePlan(context.Background(), "pvc-missing")
	if err != nil {
		t.Fatalf("BuildDeleteVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionNoop {
		t.Fatalf("expected noop for missing volume, got %s", plan.Action)
	}
}

func TestBuildDeleteVolumePlanConflictsOnUnsafeExportState(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)

	host.volumes[desired.ID] = VolumeState{
		ID:            spec.ID,
		OwnerVolumeID: spec.ID,
		Dataset:       spec.Dataset,
		DevicePath:    spec.DevicePath,
		SizeBytes:     spec.SizeBytes,
		NQN:           spec.NQN,
		Port:          spec.Port,
		ExportDevice:  "/dev/zvol/tank/nvmet/other-volume",
	}
	plan, err := storageBackend.BuildDeleteVolumePlan(context.Background(), desired.ID)
	if err != nil {
		t.Fatalf("BuildDeleteVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionConflict {
		t.Fatalf("expected conflict for wrong export device, got %s", plan.Action)
	}
	if !strings.Contains(plan.Reason, "namespace device path differs") {
		t.Fatalf("unexpected conflict reason: %q", plan.Reason)
	}

	host.volumes[desired.ID] = VolumeState{
		ID:              spec.ID,
		OwnerVolumeID:   spec.ID,
		Dataset:         spec.Dataset,
		DevicePath:      spec.DevicePath,
		SizeBytes:       spec.SizeBytes,
		NQN:             spec.NQN,
		Port:            spec.Port,
		PortEntryExists: true,
	}
	plan, err = storageBackend.BuildDeleteVolumePlan(context.Background(), desired.ID)
	if err != nil {
		t.Fatalf("BuildDeleteVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionConflict {
		t.Fatalf("expected conflict for non-symlink port entry, got %s", plan.Action)
	}
	if !strings.Contains(plan.Reason, "not an expected symlink") {
		t.Fatalf("unexpected conflict reason: %q", plan.Reason)
	}
}

func TestBuildDeleteVolumePlanConflictsOnUnownedZvolState(t *testing.T) {
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	tests := []struct {
		name string
		mut  func(*VolumeState)
		want string
	}{
		{
			name: "missing owner metadata",
			mut:  func(state *VolumeState) { state.OwnerVolumeID = "" },
			want: "owner volume ID",
		},
		{
			name: "wrong owner metadata",
			mut:  func(state *VolumeState) { state.OwnerVolumeID = "other-volume" },
			want: "owner volume ID",
		},
		{
			name: "wrong dataset",
			mut:  func(state *VolumeState) { state.Dataset = "tank/nvmet/other-volume" },
			want: "dataset differs",
		},
		{
			name: "wrong device path",
			mut:  func(state *VolumeState) { state.DevicePath = "/dev/zvol/tank/nvmet/other-volume" },
			want: "device path differs",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := newMemoryHost()
			storageBackend := newTestBackend(t, host)
			spec := storageBackend.specFromRequest(desired)
			host.volumes[desired.ID] = VolumeState{
				ID:             spec.ID,
				OwnerVolumeID:  spec.ID,
				Dataset:        spec.Dataset,
				DevicePath:     spec.DevicePath,
				SizeBytes:      spec.SizeBytes,
				NQN:            spec.NQN,
				Port:           spec.Port,
				ExportDevice:   spec.DevicePath,
				PortLinkTarget: defaultConfigFSRoot + "/subsystems/" + spec.NQN,
			}
			state := host.volumes[desired.ID]
			tt.mut(&state)
			host.volumes[desired.ID] = state

			plan, err := storageBackend.BuildDeleteVolumePlan(context.Background(), desired.ID)
			if err != nil {
				t.Fatalf("BuildDeleteVolumePlan() error = %v", err)
			}
			if plan.Action != backend.VolumeActionConflict {
				t.Fatalf("expected conflict plan, got %s", plan.Action)
			}
			if !strings.Contains(plan.Reason, tt.want) {
				t.Fatalf("expected reason containing %q, got %q", tt.want, plan.Reason)
			}
		})
	}
}

func TestBuildDeleteVolumePlanAllowsPartialExpectedExport(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)

	host.volumes[desired.ID] = VolumeState{
		ID:             spec.ID,
		OwnerVolumeID:  spec.ID,
		Dataset:        spec.Dataset,
		DevicePath:     spec.DevicePath,
		SizeBytes:      spec.SizeBytes,
		NQN:            spec.NQN,
		Port:           spec.Port,
		ExportDevice:   spec.DevicePath,
		PortLinkTarget: defaultConfigFSRoot + "/subsystems/" + spec.NQN,
	}

	plan, err := storageBackend.BuildDeleteVolumePlan(context.Background(), desired.ID)
	if err != nil {
		t.Fatalf("BuildDeleteVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionDelete {
		t.Fatalf("expected delete for partial expected export, got %s", plan.Action)
	}
}

func TestBuildDeleteVolumePlanAcceptsRelativeConfigFSPortLink(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)

	host.volumes[desired.ID] = readyVolumeState(spec)
	state := host.volumes[desired.ID]
	state.PortLinkTarget = "../../../../nvmet/subsystems/" + spec.NQN
	host.volumes[desired.ID] = state

	plan, err := storageBackend.BuildDeleteVolumePlan(context.Background(), desired.ID)
	if err != nil {
		t.Fatalf("BuildDeleteVolumePlan() error = %v", err)
	}
	if plan.Action != backend.VolumeActionDelete {
		t.Fatalf("expected delete for relative configfs symlink, got %s: %s", plan.Action, plan.Reason)
	}
}

func TestCommandHostCreateVolumeCommands(t *testing.T) {
	recorder := &recordingExecutor{outputs: map[string]string{}}
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10", ConfigFSRoot: "/config/nvmet"}, recorder)
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}

	spec := VolumeSpec{
		ID:         "pvc-test-volume",
		Dataset:    "tank/nvmet/pvc-test-volume",
		DevicePath: "/dev/zvol/tank/nvmet/pvc-test-volume",
		SizeBytes:  10 << 30,
		NQN:        "nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume",
		Port:       4420,
	}

	if err := host.CreateVolume(context.Background(), spec); err != nil {
		t.Fatalf("CreateVolume() error = %v", err)
	}

	got := commandStrings(recorder.commands)
	want := expectedPortCommands("/config/nvmet", 4420, "203.0.113.10")
	want = append(want,
		"zfs create -V 10737418240 -o volmode=dev -o org.nvmet-csi:volume-id=pvc-test-volume -o org.nvmet-csi:nqn=nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume -o org.nvmet-csi:port=4420 tank/nvmet/pvc-test-volume",
		waitForPathCommand("/dev/zvol/tank/nvmet/pvc-test-volume", "zvol device").String(),
		"mkdir -p /config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1",
		waitForPathCommand("/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1/device_path", "nvmet namespace device_path").String(),
		waitForPathCommand("/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1/enable", "nvmet namespace enable").String(),
		writeIfDifferentCommand("/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/attr_allow_any_host", "1").String(),
		setNamespaceDevicePathCommand("/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1", "/dev/zvol/tank/nvmet/pvc-test-volume").String(),
		enableNamespaceCommand("/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1").String(),
		verifyFileEqualsCommand("/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1/enable", "1", "nvmet namespace enable").String(),
		expectedLinkCommand("/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume", "/config/nvmet/ports/4420/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume"),
	)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected commands:\nwant=%v\n got=%v", want, got)
	}
	for _, cmd := range recorder.commands {
		if cmd.Stdin != "" {
			t.Fatalf("expected no stdin-backed commands after conditional-write refactor, got %+v", cmd)
		}
	}
}

func TestCommandHostCreateVolumeRollsBackOwnedPartialState(t *testing.T) {
	spec := VolumeSpec{
		ID:         "pvc-test-volume",
		Dataset:    "tank/nvmet/pvc-test-volume",
		DevicePath: "/dev/zvol/tank/nvmet/pvc-test-volume",
		SizeBytes:  10 << 30,
		NQN:        "nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume",
		Port:       4420,
	}
	recorder := &recordingExecutor{outputs: map[string]string{}}
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10", ConfigFSRoot: "/config/nvmet"}, recorder)
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}
	createCommands := host.CreateVolumeCommands(spec)
	recorder.errs = map[string]error{
		createCommands[12].String(): errors.New("enable disappeared"),
	}

	err = host.CreateVolume(context.Background(), spec)
	if err == nil {
		t.Fatal("CreateVolume() should fail when namespace enable write fails")
	}
	if !strings.Contains(err.Error(), "enable disappeared") {
		t.Fatalf("expected original create error, got %v", err)
	}

	got := commandStrings(recorder.commands)
	if len(got) != 18 {
		t.Fatalf("expected create through failing enable plus 5 rollback commands, got %d: %v", len(got), got)
	}
	wantRollback := commandStrings(host.RollbackPartialCreateCommands(spec))
	if !reflect.DeepEqual(got[len(got)-len(wantRollback):], wantRollback) {
		t.Fatalf("unexpected rollback commands:\nwant=%v\n got=%v", wantRollback, got[len(got)-len(wantRollback):])
	}
	if !strings.Contains(got[len(got)-1], "refusing to rollback unowned zvol tank/nvmet/pvc-test-volume") {
		t.Fatalf("rollback destroy command should guard ownership, got %q", got[len(got)-1])
	}
}

func TestCommandHostDryRunCommandPlanning(t *testing.T) {
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10", ConfigFSRoot: "/config/nvmet"}, &recordingExecutor{})
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}
	state := VolumeState{
		ID:            "pvc-test-volume",
		OwnerVolumeID: "pvc-test-volume",
		Dataset:       "tank/nvmet/pvc-test-volume",
		DevicePath:    "/dev/zvol/tank/nvmet/pvc-test-volume",
		NQN:           "nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume",
		Port:          4420,
	}

	resize := commandStrings(host.ResizeVolumeCommands(state.ID, 12<<30))
	if len(resize) != 1 || resize[0] != "zfs set volsize=12884901888 tank/nvmet/pvc-test-volume" {
		t.Fatalf("unexpected resize commands: %v", resize)
	}

	deletion := commandStrings(host.DeleteVolumeCommands(state))
	wantDeletion := []string{
		"sh -c if [ -e '/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1/enable' ]; then printf '%s\\n' 0 > '/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1/enable'; fi",
		"sh -c if [ -L '/config/nvmet/ports/4420/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume' ]; then rm '/config/nvmet/ports/4420/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume'; fi",
		"sh -c if [ -d '/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1' ]; then rmdir '/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1'; fi",
		"sh -c if [ -d '/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume' ]; then rmdir '/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume'; fi",
		"zfs destroy tank/nvmet/pvc-test-volume",
	}
	if !reflect.DeepEqual(deletion, wantDeletion) {
		t.Fatalf("unexpected delete commands:\nwant=%v\n got=%v", wantDeletion, deletion)
	}

	repair := commandStrings(host.RepairVolumeCommands(stateToSpec(state)))
	if len(repair) == 0 {
		t.Fatal("expected repair commands")
	}
	if repair[0] != "mkdir -p /config/nvmet/ports/4420/subsystems" {
		t.Fatalf("unexpected first repair command: %q", repair[0])
	}
	if repair[len(repair)-1] != expectedLinkCommand("/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume", "/config/nvmet/ports/4420/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume") {
		t.Fatalf("unexpected final repair command: %q", repair[len(repair)-1])
	}
}

func TestCommandHostValidateDeleteStateRequiresExpectedOwnerAndPaths(t *testing.T) {
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10"}, &recordingExecutor{})
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}
	base := VolumeState{
		ID:             "pvc-test-volume",
		OwnerVolumeID:  "pvc-test-volume",
		Dataset:        "tank/nvmet/pvc-test-volume",
		DevicePath:     "/dev/zvol/tank/nvmet/pvc-test-volume",
		NQN:            "nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume",
		Port:           4420,
		ExportDevice:   "/dev/zvol/tank/nvmet/pvc-test-volume",
		PortLinkTarget: defaultConfigFSRoot + "/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume",
	}

	tests := []struct {
		name string
		mut  func(*VolumeState)
		want string
	}{
		{
			name: "missing owner",
			mut:  func(state *VolumeState) { state.OwnerVolumeID = "" },
			want: "owner volume ID",
		},
		{
			name: "wrong owner",
			mut:  func(state *VolumeState) { state.OwnerVolumeID = "other-volume" },
			want: "owner volume ID",
		},
		{
			name: "wrong dataset",
			mut:  func(state *VolumeState) { state.Dataset = "tank/nvmet/other-volume" },
			want: "dataset",
		},
		{
			name: "wrong device path",
			mut:  func(state *VolumeState) { state.DevicePath = "/dev/zvol/tank/nvmet/other-volume" },
			want: "device path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := base
			tt.mut(&state)
			err := host.validateDeleteState(state)
			if err == nil {
				t.Fatal("expected delete validation to fail")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("expected error containing %q, got %v", tt.want, err)
			}
		})
	}
}

func TestCommandHostValidateDeleteStateAcceptsRelativeConfigFSPortLink(t *testing.T) {
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10"}, &recordingExecutor{})
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}
	state := readyVolumeState(VolumeSpec{
		ID:         "pvc-test-volume",
		Dataset:    "tank/nvmet/pvc-test-volume",
		DevicePath: "/dev/zvol/tank/nvmet/pvc-test-volume",
		SizeBytes:  10 << 30,
		NQN:        "nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume",
		Port:       4420,
	})
	state.PortLinkTarget = "../../../../nvmet/subsystems/" + state.NQN

	if err := host.validateDeleteState(state); err != nil {
		t.Fatalf("validateDeleteState() should accept relative configfs symlink target: %v", err)
	}
}

func TestCommandHostGetVolumeMapsNotFound(t *testing.T) {
	recorder := &recordingExecutor{
		errs: map[string]error{
			"zfs get -H -p -o property,value " + zfsObservedProperties + " tank/nvmet/pvc-missing": errors.New("cannot open 'tank/nvmet/pvc-missing': dataset does not exist"),
		},
	}
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10"}, recorder)
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}

	_, err = host.GetVolume(context.Background(), "pvc-missing")
	if !backend.IsVolumeNotFound(err) {
		t.Fatalf("expected volume not found, got %T %v", err, err)
	}
}

func TestSSHExecutorRequiresTarget(t *testing.T) {
	_, err := SSHExecutor{}.Run(context.Background(), Command{Name: "true"})
	if err == nil || !strings.Contains(err.Error(), "SSH target is required") {
		t.Fatalf("expected target validation error, got %v", err)
	}
}

func TestRemoteCommandStringQuotesArgumentsForSSH(t *testing.T) {
	got := remoteCommandString(Command{
		Name: "sh",
		Args: []string{
			"-c",
			"test -L '/config/nvmet/ports/4420/subsystems/nqn.test' || ln -s '/config/nvmet/subsystems/nqn.test' '/config/nvmet/ports/4420/subsystems/nqn.test'",
		},
	})
	want := "'sh' '-c' 'test -L '\"'\"'/config/nvmet/ports/4420/subsystems/nqn.test'\"'\"' || ln -s '\"'\"'/config/nvmet/subsystems/nqn.test'\"'\"' '\"'\"'/config/nvmet/ports/4420/subsystems/nqn.test'\"'\"''"
	if got != want {
		t.Fatalf("unexpected remote command:\nwant=%s\n got=%s", want, got)
	}
}

func TestRemoteCommandStringPreservesStdinCommandShape(t *testing.T) {
	got := remoteCommandString(Command{
		Name:  "tee",
		Args:  []string{"/sys/kernel/config/nvmet/subsystems/nqn.test/attr_allow_any_host"},
		Stdin: "1\n",
	})
	want := "'tee' '/sys/kernel/config/nvmet/subsystems/nqn.test/attr_allow_any_host'"
	if got != want {
		t.Fatalf("unexpected remote tee command: %q", got)
	}
}

func TestHostNamespaceExecutorCommandUsesHostPath(t *testing.T) {
	cmd := HostNamespaceExecutor{}.command(Command{
		Name:  "zfs",
		Args:  []string{"list", "tank/nvmet"},
		Stdin: "ignored\n",
	})

	want := Command{
		Name: "nsenter",
		Args: []string{
			"-t", "1",
			"-m",
			"-n",
			"--",
			"zfs", "list", "tank/nvmet",
		},
		Env:   []string{"PATH=/run/current-system/sw/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Stdin: "ignored\n",
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("unexpected host namespace command:\nwant=%+v\n got=%+v", want, cmd)
	}
}

func TestNewCommandBackendValidatesExecutor(t *testing.T) {
	_, err := NewCommandBackend(CommandBackendOptions{
		Config:   Config{Pool: "tank/nvmet"},
		Executor: backend.ExecutorSSH,
	})
	if err == nil || !strings.Contains(err.Error(), "SSH target is required") {
		t.Fatalf("expected SSH target validation error, got %v", err)
	}

	_, err = NewCommandBackend(CommandBackendOptions{
		Config:   Config{Pool: "tank/nvmet"},
		Executor: "container",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported Linux ZoL executor") {
		t.Fatalf("expected unsupported executor error, got %v", err)
	}

	_, err = NewCommandBackend(CommandBackendOptions{
		Config:   Config{Pool: "tank/nvmet", Address: "203.0.113.10"},
		Executor: backend.ExecutorHostNamespace,
	})
	if err != nil {
		t.Fatalf("expected host namespace executor to be supported, got %v", err)
	}
}

func TestCommandHostPreflightAllowsAbsentPort(t *testing.T) {
	recorder := &recordingExecutor{
		outputs: map[string]string{
			"zfs list -H -o name tank/nvmet": "tank/nvmet\n",
			"ip -j addr show":                `[{"ifname":"v68","addr_info":[{"local":"203.0.113.10"}]}]`,
		},
		errs: map[string]error{
			"test -d /sys/kernel/config/nvmet/ports/4420": errors.New("missing"),
		},
	}
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10"}, recorder)
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}

	checks := host.Preflight(context.Background(), 4420)
	if !PreflightChecksOK(checks) {
		t.Fatalf("expected absent port preflight to pass, got %+v", checks)
	}
	if checks[len(checks)-1].Name != "nvmet port" || !strings.Contains(checks[len(checks)-1].Detail, "will create it") {
		t.Fatalf("unexpected port check: %+v", checks[len(checks)-1])
	}
}

func TestCommandHostPreflightFailsMismatchedPortAttribute(t *testing.T) {
	recorder := &recordingExecutor{
		outputs: map[string]string{
			"zfs list -H -o name tank/nvmet":                       "tank/nvmet\n",
			"ip -j addr show":                                      `[{"ifname":"v68","addr_info":[{"local":"203.0.113.10"}]}]`,
			"cat /sys/kernel/config/nvmet/ports/4420/addr_trtype":  "tcp\n",
			"cat /sys/kernel/config/nvmet/ports/4420/addr_adrfam":  "ipv4\n",
			"cat /sys/kernel/config/nvmet/ports/4420/addr_traddr":  "203.0.113.99\n",
			"cat /sys/kernel/config/nvmet/ports/4420/addr_trsvcid": "4420\n",
		},
	}
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10"}, recorder)
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}

	checks := host.Preflight(context.Background(), 4420)
	if PreflightChecksOK(checks) {
		t.Fatalf("expected preflight to fail, got %+v", checks)
	}
	found := false
	for _, check := range checks {
		if check.Name == "nvmet port addr_traddr" {
			found = true
			if check.OK || !strings.Contains(check.Detail, "203.0.113.99") {
				t.Fatalf("unexpected addr_traddr check: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("missing addr_traddr check: %+v", checks)
	}
}

func TestCommandHostPreflightFailsMissingTargetAddress(t *testing.T) {
	recorder := &recordingExecutor{
		outputs: map[string]string{
			"zfs list -H -o name tank/nvmet": "tank/nvmet\n",
			"ip -j addr show":                `[{"ifname":"v67","addr_info":[{"local":"203.0.113.4"}]}]`,
		},
		errs: map[string]error{
			"test -d /sys/kernel/config/nvmet/ports/4420": errors.New("missing"),
		},
	}
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "198.51.100.23"}, recorder)
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}

	checks := host.Preflight(context.Background(), 4420)
	if PreflightChecksOK(checks) {
		t.Fatalf("expected preflight to fail, got %+v", checks)
	}
	found := false
	for _, check := range checks {
		if check.Name == "target address" {
			found = true
			if check.OK || !strings.Contains(check.Detail, "198.51.100.23 is not assigned") {
				t.Fatalf("unexpected target address check: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("missing target address check: %+v", checks)
	}
}

func TestCommandHostPreflightFailsTargetAddressMTUTooSmall(t *testing.T) {
	recorder := &recordingExecutor{
		outputs: map[string]string{
			"zfs list -H -o name tank/nvmet": "tank/nvmet\n",
			"ip -j addr show":                `[{"ifname":"v68","mtu":1500,"addr_info":[{"local":"198.51.100.23"}]}]`,
		},
		errs: map[string]error{
			"test -d /sys/kernel/config/nvmet/ports/4420": errors.New("missing"),
		},
	}
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "198.51.100.23", MinMTU: 9000}, recorder)
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}

	checks := host.Preflight(context.Background(), 4420)
	if PreflightChecksOK(checks) {
		t.Fatalf("expected preflight to fail, got %+v", checks)
	}
	found := false
	for _, check := range checks {
		if check.Name == "target address" {
			found = true
			if check.OK || !strings.Contains(check.Detail, "MTU 1500 is below required 9000") {
				t.Fatalf("unexpected target address check: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("missing target address check: %+v", checks)
	}
}

func TestCommandHostPreflightAcceptsTargetAddressMTU(t *testing.T) {
	recorder := &recordingExecutor{
		outputs: map[string]string{
			"zfs list -H -o name tank/nvmet": "tank/nvmet\n",
			"ip -j addr show":                `[{"ifname":"v68","mtu":9000,"addr_info":[{"local":"198.51.100.23"}]}]`,
		},
		errs: map[string]error{
			"test -d /sys/kernel/config/nvmet/ports/4420": errors.New("missing"),
		},
	}
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", Address: "198.51.100.23", MinMTU: 9000}, recorder)
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}

	checks := host.Preflight(context.Background(), 4420)
	if !PreflightChecksOK(checks) {
		t.Fatalf("expected preflight to pass, got %+v", checks)
	}
	found := false
	for _, check := range checks {
		if check.Name == "target address" {
			found = true
			if !strings.Contains(check.Detail, "with MTU 9000") {
				t.Fatalf("unexpected target address check: %+v", check)
			}
		}
	}
	if !found {
		t.Fatalf("missing target address check: %+v", checks)
	}
}

func TestGetSnapshotReturnsTypedNotFound(t *testing.T) {
	storageBackend := newTestBackend(t, newMemoryHost())

	_, err := storageBackend.GetSnapshot(context.Background(), "snap-missing")
	if !backend.IsSnapshotNotFound(err) {
		t.Fatalf("expected snapshot not found, got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "snap-missing") {
		t.Fatalf("expected snapshot ID in error, got %v", err)
	}
}

func newTestBackend(t *testing.T, host Host) *Backend {
	t.Helper()
	storageBackend, err := New(Config{Pool: "tank/nvmet", Address: "203.0.113.10"}, host)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return storageBackend
}

func testCreateRequest(id string, sizeBytes int64) backend.CreateVolumeRequest {
	return backend.CreateVolumeRequest{
		ID:          id,
		Path:        "/routeros-style/path/ignored.img",
		SizeBytes:   sizeBytes,
		NVMETCPPort: 4420,
		NVMETCPNQN:  "nqn.2014-08.org.nvmexpress:uuid:" + id,
	}
}

func mustApplyPlan(t *testing.T, storageBackend *Backend, desired backend.CreateVolumeRequest) {
	t.Helper()
	plan, err := storageBackend.BuildEnsureVolumePlan(context.Background(), desired)
	if err != nil {
		t.Fatalf("BuildEnsureVolumePlan() error = %v", err)
	}
	if _, err := storageBackend.ApplyVolumePlan(context.Background(), plan, backend.VolumeApplyOptions{}); err != nil {
		t.Fatalf("ApplyVolumePlan() error = %v", err)
	}
}

type memoryHost struct {
	volumes map[string]VolumeState
}

type partialCreateHost struct {
	*memoryHost
	createErr error
	mut       func(*VolumeState)
}

type planningMemoryHost struct {
	*memoryHost
}

func newMemoryHost() *memoryHost {
	return &memoryHost{volumes: map[string]VolumeState{}}
}

func readyVolumeState(spec VolumeSpec) VolumeState {
	return VolumeState{
		ID:              spec.ID,
		OwnerVolumeID:   spec.ID,
		Dataset:         spec.Dataset,
		DevicePath:      spec.DevicePath,
		SizeBytes:       spec.SizeBytes,
		NQN:             spec.NQN,
		Port:            spec.Port,
		Exported:        true,
		NamespaceReady:  true,
		ExportDevice:    spec.DevicePath,
		PortEntryExists: true,
		PortLinkTarget:  defaultConfigFSRoot + "/subsystems/" + spec.NQN,
		PortAddress:     "203.0.113.10",
		PortServiceID:   "4420",
		PortTransport:   "tcp",
		PortAddressFam:  "ipv4",
		Status:          "ready",
	}
}

func (h *memoryHost) GetVolume(_ context.Context, volumeID string) (*VolumeState, error) {
	volume, ok := h.volumes[volumeID]
	if !ok {
		return nil, &backend.VolumeNotFoundError{ID: volumeID}
	}
	return &volume, nil
}

func (h *memoryHost) ListVolumes(context.Context) ([]VolumeState, error) {
	out := make([]VolumeState, 0, len(h.volumes))
	for _, volume := range h.volumes {
		out = append(out, volume)
	}
	return out, nil
}

func (h *memoryHost) CreateVolume(_ context.Context, spec VolumeSpec) error {
	h.volumes[spec.ID] = readyVolumeState(spec)
	return nil
}

func (h *partialCreateHost) CreateVolume(_ context.Context, spec VolumeSpec) error {
	state := VolumeState{
		ID:             spec.ID,
		OwnerVolumeID:  spec.ID,
		Dataset:        spec.Dataset,
		DevicePath:     spec.DevicePath,
		SizeBytes:      spec.SizeBytes,
		NQN:            spec.NQN,
		Port:           spec.Port,
		PortAddress:    "203.0.113.10",
		PortServiceID:  "4420",
		PortTransport:  "tcp",
		PortAddressFam: "ipv4",
		Status:         "partial",
	}
	if h.mut != nil {
		h.mut(&state)
	}
	h.volumes[spec.ID] = state
	if h.createErr != nil {
		return h.createErr
	}
	return errors.New("create failed after partial state")
}

func (h *memoryHost) RepairVolume(_ context.Context, spec VolumeSpec) error {
	volume, ok := h.volumes[spec.ID]
	if !ok {
		return &backend.VolumeNotFoundError{ID: spec.ID}
	}
	volume.Exported = true
	volume.NamespaceReady = true
	volume.ExportDevice = spec.DevicePath
	volume.PortEntryExists = true
	volume.PortLinkTarget = defaultConfigFSRoot + "/subsystems/" + spec.NQN
	volume.PortAddress = "203.0.113.10"
	volume.PortServiceID = "4420"
	volume.PortTransport = "tcp"
	volume.PortAddressFam = "ipv4"
	volume.Status = "ready"
	h.volumes[spec.ID] = volume
	return nil
}

func (h *memoryHost) DeleteVolume(_ context.Context, volumeID string) error {
	delete(h.volumes, volumeID)
	return nil
}

func (h *memoryHost) ResizeVolume(_ context.Context, volumeID string, sizeBytes int64) error {
	volume, ok := h.volumes[volumeID]
	if !ok {
		return &backend.VolumeNotFoundError{ID: volumeID}
	}
	volume.SizeBytes = sizeBytes
	h.volumes[volumeID] = volume
	return nil
}

func (h *memoryHost) GetCapacity(context.Context) (*backend.Capacity, error) {
	return &backend.Capacity{TotalBytes: 100 << 30, FreeBytes: 80 << 30, UsedBytes: 20 << 30}, nil
}

func (h *planningMemoryHost) CreateVolumeCommands(spec VolumeSpec) []Command {
	commandHost, _ := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10"}, &recordingExecutor{})
	return commandHost.CreateVolumeCommands(spec)
}

func (h *planningMemoryHost) RepairVolumeCommands(spec VolumeSpec) []Command {
	commandHost, _ := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10"}, &recordingExecutor{})
	return commandHost.RepairVolumeCommands(spec)
}

func (h *planningMemoryHost) DeleteVolumeCommands(state VolumeState) []Command {
	commandHost, _ := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10"}, &recordingExecutor{})
	return commandHost.DeleteVolumeCommands(state)
}

func (h *planningMemoryHost) ResizeVolumeCommands(volumeID string, sizeBytes int64) []Command {
	commandHost, _ := NewCommandHost(Config{Pool: "tank/nvmet", Address: "203.0.113.10"}, &recordingExecutor{})
	return commandHost.ResizeVolumeCommands(volumeID, sizeBytes)
}

type recordingExecutor struct {
	commands []Command
	outputs  map[string]string
	errs     map[string]error
}

func (e *recordingExecutor) Run(_ context.Context, cmd Command) (string, error) {
	e.commands = append(e.commands, cmd)
	key := cmd.String()
	if err := e.errs[key]; err != nil {
		return "", err
	}
	return e.outputs[key], nil
}

func commandStrings(commands []Command) []string {
	out := make([]string, 0, len(commands))
	for _, cmd := range commands {
		out = append(out, cmd.String())
	}
	return out
}

func expectedPortCommands(configFSRoot string, port int, address string) []string {
	cfg := Config{Address: address, ConfigFSRoot: configFSRoot}.withDefaults()
	portPath := cfg.portPath(port)
	return []string{
		"mkdir -p " + portPath + "/subsystems",
		writeIfEmptyOrEqualCommand(portPath+"/addr_trtype", "tcp").String(),
		writeIfEmptyOrEqualCommand(portPath+"/addr_adrfam", cfg.portAddressFamily()).String(),
		writeIfEmptyOrEqualCommand(portPath+"/addr_traddr", address).String(),
		writeIfEmptyOrEqualCommand(portPath+"/addr_trsvcid", cfg.portServiceID(port)).String(),
	}
}

func expectedLinkCommand(target string, link string) string {
	return linkIfAbsentOrEqualCommand(target, link).String()
}

func stateToSpec(state VolumeState) VolumeSpec {
	return VolumeSpec{
		ID:         state.ID,
		Dataset:    state.Dataset,
		DevicePath: state.DevicePath,
		SizeBytes:  state.SizeBytes,
		NQN:        state.NQN,
		Port:       state.Port,
	}
}

func ExampleCommand_String() {
	fmt.Println(Command{Name: "zfs", Args: []string{"list", "tank/nvmet"}}.String())
	// Output: zfs list tank/nvmet
}
func TestSetNamespaceDevicePathCommand_SemanticsInShell(t *testing.T) {
	ns := "/config/nvmet/subsystems/nqn.test/namespaces/1"
	dev := "/dev/zvol/tank/nvmet/testvol"

	// equal: exits 0 before write
	eq := setNamespaceDevicePathCommand(ns, dev)
	eqStr := eq.String()
	if !strings.Contains(eqStr, "current=$(cat") {
		t.Fatalf("equal case missing current check: %s", eqStr)
	}
	if strings.Contains(eqStr, "printf") && strings.Index(eqStr, "printf '%s\\n' '"+dev+"'") < strings.Index(eqStr, "current=") {
		t.Fatal("write appears before current check on equal")
	}
	// must have the exit 0 path for match
	if !strings.Contains(eqStr, "if [ \"$current\" = '"+dev+"' ]; then exit 0; fi;") {
		t.Fatalf("equal should early exit 0: %s", eqStr)
	}

	// enabled + differ: error mentioning ns path, no write executed
	differ := setNamespaceDevicePathCommand(ns, "/dev/other")
	dStr := differ.String()
	if !strings.Contains(dStr, "refusing to rewrite device_path of enabled nvmet namespace "+ns) {
		t.Fatalf("enabled-differ must emit specific refusal mentioning namespace: %s", dStr)
	}
	// ordering: current check precedes enable check precedes write
	curIdx := strings.Index(dStr, "current=$(cat")
	enIdx := strings.Index(dStr, "enabled=$(cat")
	wrIdx := strings.Index(dStr, "printf '%s\\n' '/dev/other'")
	if curIdx == -1 || enIdx == -1 || wrIdx == -1 {
		t.Fatalf("missing check parts: %s", dStr)
	}
	if curIdx >= enIdx || enIdx >= wrIdx {
		t.Fatalf("checks not in order current < enable < write: cur=%d en=%d wr=%d\n%s", curIdx, enIdx, wrIdx, dStr)
	}

	// disabled (implicit): will reach write (no early exit on enabled)
	if !strings.Contains(dStr, "if [ \"$enabled\" = '1' ]; then") {
		t.Fatal("must check enabled before deciding to write or error")
	}
}

func TestEnableNamespaceCommand_Idempotent(t *testing.T) {
	ns := "/config/nvmet/subsystems/nqn.foo/namespaces/1"
	cmd := enableNamespaceCommand(ns)
	s := cmd.String()
	if !strings.Contains(s, "current=$(cat") || !strings.Contains(s, "if [ \"$current\" = '1' ]; then exit 0; fi;") {
		t.Fatalf("enable must no-op check for '1': %s", s)
	}
	if !strings.Contains(s, "printf '%s\\n' 1 > '"+ns+"/enable'") {
		t.Fatalf("enable must write 1 when not set: %s", s)
	}
}

func TestProbeExportStateCommand_ParseExportProbe_RoundTrip(t *testing.T) {
	cfg := Config{Pool: "tank/nvmet", Address: "203.0.113.10", ConfigFSRoot: "/config/nvmet"}
	_, err := NewCommandHost(cfg, &recordingExecutor{})
	if err != nil {
		t.Fatalf("NewCommandHost error: %v", err)
	}

	// (a) fully exported state a real script would emit
	full := "ns_enable=1\n" +
		"ns_device_path=/dev/zvol/tank/nvmet/vol1\n" +
		"addr_traddr=203.0.113.10\n" +
		"addr_trsvcid=4420\n" +
		"addr_trtype=tcp\n" +
		"addr_adrfam=ipv4\n" +
		"port_entry=1\n" +
		"port_link=/config/nvmet/subsystems/nqn.test:vol1\n" +
		"exit 0"
	p := parseExportProbe(full)
	if p.nsEnable != "1" || p.nsDevicePath != "/dev/zvol/tank/nvmet/vol1" || !p.portEntry || p.portLink != "/config/nvmet/subsystems/nqn.test:vol1" {
		t.Fatalf("parse full mismatch: %+v", p)
	}
	if !p.namespaceReady() {
		t.Fatal("namespaceReady must be true")
	}

	// (b) post-reboot empty configfs
	empty := "ns_enable=\nns_device_path=\naddr_traddr=\naddr_trsvcid=\naddr_trtype=\naddr_adrfam=\nport_entry=0\nport_link=\n"
	p2 := parseExportProbe(empty)
	if p2.nsEnable != "" || p2.nsDevicePath != "" || p2.portEntry || p2.portLink != "" {
		t.Fatalf("parse empty mismatch: %+v", p2)
	}

	// empty nqn + port<=0 returns zero probe, no command executed
	rec := &recordingExecutor{}
	h2, _ := NewCommandHost(cfg, rec)
	pr, err := h2.probeExportState(context.Background(), "", 0)
	if err != nil {
		t.Fatalf("probe empty should not err: %v", err)
	}
	if pr != (exportProbe{}) {
		t.Fatalf("expected zero probe, got %+v", pr)
	}
	if len(rec.commands) != 0 {
		t.Fatalf("no executor calls expected for empty nqn/port, got %d: %v", len(rec.commands), rec.commands)
	}
}

func TestCommandHostGetVolume_AssemblesVolumeStateFromBatched(t *testing.T) {
	cfg := Config{Pool: "tank/nvmet", Address: "203.0.113.10", ConfigFSRoot: "/config/nvmet"}
	cfg = cfg.withDefaults()
	rec := &recordingExecutor{}
	host, err := NewCommandHost(cfg, rec)
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}

	volID := "vol1"
	nqn := "nqn.test:vol1"
	port := 4420
	dataset := cfg.datasetFor(volID)
	devPath := cfg.devicePathFor(volID)

	zfsOut := "volsize\t10737418240\n" +
		"org.nvmet-csi:volume-id\tvol1\n" +
		"org.nvmet-csi:nqn\t" + nqn + "\n" +
		"org.nvmet-csi:port\t" + strconv.Itoa(port) + "\n"

	zfsKey := "zfs get -H -p -o property,value " + zfsObservedProperties + " " + dataset

	// probe output for matching fully exported
	probeOutMatch := "ns_enable=1\n" +
		"ns_device_path=" + devPath + "\n" +
		"addr_traddr=203.0.113.10\n" +
		"addr_trsvcid=4420\n" +
		"addr_trtype=tcp\n" +
		"addr_adrfam=ipv4\n" +
		"port_entry=1\n" +
		"port_link=/config/nvmet/subsystems/" + linkNameForNQN(nqn) + "\n"

	probeCmd := host.probeExportStateCommand(nqn, port)
	probeKey := probeCmd.String()

	rec.outputs = map[string]string{
		zfsKey:   zfsOut,
		probeKey: probeOutMatch,
	}

	state, err := host.GetVolume(context.Background(), volID)
	if err != nil {
		t.Fatalf("GetVolume error: %v", err)
	}
	if !state.Exported {
		t.Fatalf("expected Exported=true for matching probe, got %+v", state)
	}
	if state.NQN != nqn || state.Port != port || state.ExportDevice != devPath {
		t.Fatalf("bad assembled state: %+v", state)
	}
	if len(rec.commands) != 2 {
		t.Fatalf("expected exactly 2 executor commands (zfs+probe), got %d", len(rec.commands))
	}

	// now differ on device_path -> Exported=false
	rec2 := &recordingExecutor{}
	host2, _ := NewCommandHost(cfg, rec2)
	probeOutDiffer := strings.Replace(probeOutMatch, devPath, "/dev/wrong", 1)
	probeCmd2 := host2.probeExportStateCommand(nqn, port)
	rec2.outputs = map[string]string{
		zfsKey:             zfsOut,
		probeCmd2.String(): probeOutDiffer,
	}
	state2, err := host2.GetVolume(context.Background(), volID)
	if err != nil {
		t.Fatalf("GetVolume differ error: %v", err)
	}
	if state2.Exported {
		t.Fatalf("expected Exported=false when device_path differs, got %+v", state2)
	}
	if len(rec2.commands) != 2 {
		t.Fatalf("expected 2 cmds on differ case, got %d", len(rec2.commands))
	}
}

func TestCommandHostListVolumes_ParsesMultiDatasetAndIssuesProbes(t *testing.T) {
	cfg := Config{Pool: "tank/nvmet", Address: "203.0.113.10", ConfigFSRoot: "/config/nvmet"}
	rec := &recordingExecutor{}
	host, err := NewCommandHost(cfg, rec)
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}

	// multi dataset zfs get -r output: name\tprop\tvalue
	zfsListOut := "" +
		"tank/nvmet/vol1\tvolsize\t10737418240\n" +
		"tank/nvmet/vol1\torg.nvmet-csi:volume-id\tvol1\n" +
		"tank/nvmet/vol1\torg.nvmet-csi:nqn\tnqn.test:vol1\n" +
		"tank/nvmet/vol1\torg.nvmet-csi:port\t4420\n" +
		"tank/nvmet/vol2\tvolsize\t21474836480\n" +
		"tank/nvmet/vol2\torg.nvmet-csi:volume-id\tvol2\n" +
		"tank/nvmet/vol2\torg.nvmet-csi:nqn\tnqn.test:vol2\n" +
		"tank/nvmet/vol2\torg.nvmet-csi:port\t4421\n"

	zfsListKey := "zfs get -H -p -o name,property,value -t volume -r " + zfsObservedProperties + " tank/nvmet"

	// precompute probes that List will issue
	probe1 := host.probeExportStateCommand("nqn.test:vol1", 4420)
	probe2 := host.probeExportStateCommand("nqn.test:vol2", 4421)

	// minimal matching probes so no err
	probeOut := "ns_enable=1\nns_device_path=/dev/zvol/tank/nvmet/volX\naddr_traddr=203.0.113.10\naddr_trsvcid=4420\naddr_trtype=tcp\naddr_adrfam=ipv4\nport_entry=1\nport_link=/config/nvmet/subsystems/nqn.test:volX\n"
	// adjust per vol not critical for count/parse
	rec.outputs = map[string]string{
		zfsListKey:      zfsListOut,
		probe1.String(): strings.ReplaceAll(probeOut, "volX", "vol1"),
		probe2.String(): strings.ReplaceAll(probeOut, "volX", "vol2"),
	}

	vols, err := host.ListVolumes(context.Background())
	if err != nil {
		t.Fatalf("ListVolumes error: %v", err)
	}
	if len(vols) != 2 {
		t.Fatalf("expected 2 volumes, got %d", len(vols))
	}
	if vols[0].ID != "vol1" || vols[1].ID != "vol2" {
		t.Fatalf("IDs not stripped of pool prefix: %+v", vols)
	}
	if vols[0].SizeBytes == 0 || vols[1].SizeBytes == 0 {
		t.Fatal("sizes not parsed")
	}
	// 1 zfs + 2 probes
	if len(rec.commands) != 3 {
		t.Fatalf("expected 1 + 2 = 3 executor commands, got %d", len(rec.commands))
	}
}
