package linuxzol

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/backend"
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

func TestBuildEnsureVolumePlanRepairsIncompleteExport(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)

	host.volumes[desired.ID] = VolumeState{
		ID:             spec.ID,
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

func TestBuildEnsureVolumePlanConflictsOnWrongExportDevice(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)

	host.volumes[desired.ID] = VolumeState{
		ID:           spec.ID,
		Dataset:      spec.Dataset,
		DevicePath:   spec.DevicePath,
		SizeBytes:    spec.SizeBytes,
		NQN:          spec.NQN,
		Port:         spec.Port,
		ExportDevice: "/dev/zvol/tank/nvmet/other-volume",
		Status:       "not-exported",
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
		ID:           spec.ID,
		Dataset:      spec.Dataset,
		DevicePath:   spec.DevicePath,
		SizeBytes:    spec.SizeBytes,
		NQN:          spec.NQN,
		Port:         spec.Port,
		ExportDevice: "/dev/zvol/tank/nvmet/other-volume",
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

func TestBuildDeleteVolumePlanAllowsPartialExpectedExport(t *testing.T) {
	host := newMemoryHost()
	storageBackend := newTestBackend(t, host)
	desired := testCreateRequest("pvc-test-volume", 10<<30)
	spec := storageBackend.specFromRequest(desired)

	host.volumes[desired.ID] = VolumeState{
		ID:             spec.ID,
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

func TestCommandHostCreateVolumeCommands(t *testing.T) {
	recorder := &recordingExecutor{outputs: map[string]string{}}
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", ConfigFSRoot: "/config/nvmet"}, recorder)
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
	want := []string{
		"zfs create -V 10737418240 -o volmode=dev -o org.nvmet-csi:volume-id=pvc-test-volume -o org.nvmet-csi:nqn=nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume -o org.nvmet-csi:port=4420 tank/nvmet/pvc-test-volume",
		"mkdir -p /config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1",
		"tee /config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/attr_allow_any_host",
		"tee /config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1/device_path",
		"tee /config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1/enable",
		"mkdir -p /config/nvmet/ports/4420/subsystems",
		"ln -s /config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume /config/nvmet/ports/4420/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected commands:\nwant=%v\n got=%v", want, got)
	}
	if recorder.commands[2].Stdin != "1\n" || recorder.commands[3].Stdin != spec.DevicePath+"\n" || recorder.commands[4].Stdin != "1\n" {
		t.Fatalf("unexpected tee stdin: %+v", recorder.commands)
	}
}

func TestCommandHostDryRunCommandPlanning(t *testing.T) {
	host, err := NewCommandHost(Config{Pool: "tank/nvmet", ConfigFSRoot: "/config/nvmet"}, &recordingExecutor{})
	if err != nil {
		t.Fatalf("NewCommandHost() error = %v", err)
	}
	state := VolumeState{
		ID:         "pvc-test-volume",
		Dataset:    "tank/nvmet/pvc-test-volume",
		DevicePath: "/dev/zvol/tank/nvmet/pvc-test-volume",
		NQN:        "nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume",
		Port:       4420,
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
	if repair[0] != "mkdir -p /config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume/namespaces/1" {
		t.Fatalf("unexpected first repair command: %q", repair[0])
	}
	if repair[len(repair)-1] != "sh -c test -L '/config/nvmet/ports/4420/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume' || ln -s '/config/nvmet/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume' '/config/nvmet/ports/4420/subsystems/nqn.2014-08.org.nvmexpress:uuid:pvc-test-volume'" {
		t.Fatalf("unexpected final repair command: %q", repair[len(repair)-1])
	}
}

func TestCommandHostGetVolumeMapsNotFound(t *testing.T) {
	recorder := &recordingExecutor{
		errs: map[string]error{
			"zfs get -H -p -o value volsize tank/nvmet/pvc-missing": errors.New("cannot open 'tank/nvmet/pvc-missing': dataset does not exist"),
		},
	}
	host, err := NewCommandHost(Config{Pool: "tank/nvmet"}, recorder)
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
	storageBackend, err := New(Config{Pool: "tank/nvmet", Address: "10.42.0.10"}, host)
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

func newMemoryHost() *memoryHost {
	return &memoryHost{volumes: map[string]VolumeState{}}
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
	h.volumes[spec.ID] = VolumeState{
		ID:              spec.ID,
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
		Status:          "ready",
	}
	return nil
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
