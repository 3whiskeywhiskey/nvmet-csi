package linuxzol

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/backend"
)

type Host interface {
	GetVolume(ctx context.Context, volumeID string) (*VolumeState, error)
	ListVolumes(ctx context.Context) ([]VolumeState, error)
	CreateVolume(ctx context.Context, spec VolumeSpec) error
	RepairVolume(ctx context.Context, spec VolumeSpec) error
	DeleteVolume(ctx context.Context, volumeID string) error
	ResizeVolume(ctx context.Context, volumeID string, sizeBytes int64) error
	GetCapacity(ctx context.Context) (*backend.Capacity, error)
}

type CommandHost struct {
	cfg  Config
	exec Executor
}

func NewCommandHost(cfg Config, executor Executor) (*CommandHost, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if executor == nil {
		executor = LocalExecutor{}
	}
	return &CommandHost{cfg: cfg, exec: executor}, nil
}

// zfsDatasetProps carries the durable per-zvol export intent stored as ZFS
// properties, plus the observed volume size.
type zfsDatasetProps struct {
	sizeBytes     int64
	ownerVolumeID string
	nqn           string
	port          int
}

var zfsObservedProperties = "volsize," + zfsPropVolumeID + "," + zfsPropNQN + "," + zfsPropPort

func (h *CommandHost) GetVolume(ctx context.Context, volumeID string) (*VolumeState, error) {
	dataset := h.cfg.datasetFor(volumeID)
	out, err := h.run(ctx, Command{
		Name: "zfs",
		Args: []string{"get", "-H", "-p", "-o", "property,value", zfsObservedProperties, dataset},
	})
	if err != nil {
		if isZFSNotFound(err) {
			return nil, &backend.VolumeNotFoundError{ID: volumeID}
		}
		return nil, err
	}
	props, err := parseZFSDatasetProps(volumeID, out)
	if err != nil {
		return nil, err
	}
	probe, err := h.probeExportState(ctx, props.nqn, props.port)
	if err != nil {
		return nil, err
	}
	state := h.volumeState(volumeID, dataset, props, probe)
	return &state, nil
}

func (h *CommandHost) ListVolumes(ctx context.Context) ([]VolumeState, error) {
	out, err := h.run(ctx, Command{
		Name: "zfs",
		Args: []string{"get", "-H", "-p", "-o", "name,property,value", "-t", "volume", "-r", zfsObservedProperties, h.cfg.Pool},
	})
	if err != nil {
		if isZFSNotFound(err) {
			return []VolumeState{}, nil
		}
		return nil, err
	}

	rowsByDataset := map[string]string{}
	datasets := []string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			return nil, fmt.Errorf("unexpected zfs get row %q", line)
		}
		dataset := fields[0]
		if _, seen := rowsByDataset[dataset]; !seen {
			datasets = append(datasets, dataset)
		}
		rowsByDataset[dataset] += fields[1] + "\t" + fields[2] + "\n"
	}

	volumes := make([]VolumeState, 0, len(datasets))
	for _, dataset := range datasets {
		id := strings.TrimPrefix(dataset, h.cfg.Pool+"/")
		props, err := parseZFSDatasetProps(id, rowsByDataset[dataset])
		if err != nil {
			return nil, err
		}
		probe, err := h.probeExportState(ctx, props.nqn, props.port)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, h.volumeState(id, dataset, props, probe))
	}
	return volumes, nil
}

func parseZFSDatasetProps(volumeID string, out string) (zfsDatasetProps, error) {
	props := zfsDatasetProps{}
	sizeSeen := false
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) < 2 {
			return props, fmt.Errorf("unexpected zfs property row for %s: %q", volumeID, line)
		}
		property := strings.TrimSpace(fields[0])
		value := strings.TrimSpace(fields[1])
		if value == "-" {
			value = ""
		}
		switch property {
		case "volsize":
			sizeBytes, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return props, fmt.Errorf("parse zvol size for %s: %w", volumeID, err)
			}
			props.sizeBytes = sizeBytes
			sizeSeen = true
		case zfsPropVolumeID:
			props.ownerVolumeID = value
		case zfsPropNQN:
			props.nqn = value
		case zfsPropPort:
			props.port, _ = strconv.Atoi(value)
		}
	}
	if !sizeSeen {
		return props, fmt.Errorf("zfs did not report volsize for %s", volumeID)
	}
	return props, nil
}

func (h *CommandHost) volumeState(volumeID string, dataset string, props zfsDatasetProps, probe exportProbe) VolumeState {
	devicePath := h.cfg.devicePathFor(volumeID)
	portReady := h.portReady(probe.portAddress, probe.portServiceID, probe.portTransport, probe.portAddressFam, props.port)
	portLinkReady := h.cfg.portLinkTargetMatches(props.port, props.nqn, probe.portLink)
	exported := probe.namespaceReady() && probe.nsDevicePath == devicePath && portLinkReady && portReady

	return VolumeState{
		ID:              volumeID,
		OwnerVolumeID:   props.ownerVolumeID,
		Dataset:         dataset,
		DevicePath:      devicePath,
		SizeBytes:       props.sizeBytes,
		NQN:             props.nqn,
		Port:            props.port,
		Exported:        exported,
		NamespaceReady:  probe.namespaceReady(),
		ExportDevice:    probe.nsDevicePath,
		PortEntryExists: probe.portEntry,
		PortLinkTarget:  probe.portLink,
		PortAddress:     probe.portAddress,
		PortServiceID:   probe.portServiceID,
		PortTransport:   probe.portTransport,
		PortAddressFam:  probe.portAddressFam,
		Status:          statusFromExport(exported),
	}
}

func (h *CommandHost) CreateVolume(ctx context.Context, spec VolumeSpec) error {
	if err := h.runAll(ctx, h.CreateVolumeCommands(spec)); err != nil {
		if rollbackErr := h.runAll(ctx, h.RollbackPartialCreateCommands(spec)); rollbackErr != nil {
			return fmt.Errorf("create volume %s failed: %w; rollback failed: %w", spec.ID, err, rollbackErr)
		}
		return err
	}
	return nil
}

func (h *CommandHost) CreateVolumeCommands(spec VolumeSpec) []Command {
	commands := []Command{}
	commands = append(commands, h.EnsurePortCommands(spec.Port)...)
	commands = append(commands,
		Command{
			Name: "zfs",
			Args: []string{
				"create", "-V", strconv.FormatInt(spec.SizeBytes, 10),
				"-o", "volmode=dev",
				"-o", zfsPropVolumeID + "=" + spec.ID,
				"-o", zfsPropNQN + "=" + spec.NQN,
				"-o", zfsPropPort + "=" + strconv.Itoa(spec.Port),
				spec.Dataset,
			},
		},
		waitForPathCommand(spec.DevicePath, "zvol device"),
		Command{Name: "mkdir", Args: []string{"-p", h.cfg.namespacePath(spec.NQN)}},
		waitForPathCommand(h.cfg.namespacePath(spec.NQN)+"/device_path", "nvmet namespace device_path"),
		waitForPathCommand(h.cfg.namespacePath(spec.NQN)+"/enable", "nvmet namespace enable"),
		writeIfDifferentCommand(h.cfg.subsystemPath(spec.NQN)+"/attr_allow_any_host", "1"),
		setNamespaceDevicePathCommand(h.cfg.namespacePath(spec.NQN), spec.DevicePath),
		enableNamespaceCommand(h.cfg.namespacePath(spec.NQN)),
		verifyFileEqualsCommand(h.cfg.namespacePath(spec.NQN)+"/enable", "1", "nvmet namespace enable"),
		linkIfAbsentOrEqualCommand(h.cfg.subsystemPath(spec.NQN), h.cfg.portSubsystemPath(spec.Port, spec.NQN)),
	)
	return commands
}

func (h *CommandHost) RollbackPartialCreateCommands(spec VolumeSpec) []Command {
	return []Command{
		disableIfPresentCommand(h.cfg.namespacePath(spec.NQN) + "/enable"),
		removeSymlinkIfPresentCommand(h.cfg.portSubsystemPath(spec.Port, spec.NQN)),
		rmdirIfPresentCommand(h.cfg.namespacePath(spec.NQN)),
		rmdirIfPresentCommand(h.cfg.subsystemPath(spec.NQN)),
		destroyDatasetIfOwnedCommand(spec.Dataset, spec.ID, spec.NQN),
	}
}

func (h *CommandHost) RepairVolume(ctx context.Context, spec VolumeSpec) error {
	return h.runAll(ctx, h.RepairVolumeCommands(spec))
}

func (h *CommandHost) RepairVolumeCommands(spec VolumeSpec) []Command {
	commands := []Command{}
	commands = append(commands, h.EnsurePortCommands(spec.Port)...)
	commands = append(commands,
		Command{Name: "mkdir", Args: []string{"-p", h.cfg.namespacePath(spec.NQN)}},
		waitForPathCommand(h.cfg.namespacePath(spec.NQN)+"/device_path", "nvmet namespace device_path"),
		waitForPathCommand(h.cfg.namespacePath(spec.NQN)+"/enable", "nvmet namespace enable"),
		writeIfDifferentCommand(h.cfg.subsystemPath(spec.NQN)+"/attr_allow_any_host", "1"),
		setNamespaceDevicePathCommand(h.cfg.namespacePath(spec.NQN), spec.DevicePath),
		enableNamespaceCommand(h.cfg.namespacePath(spec.NQN)),
		verifyFileEqualsCommand(h.cfg.namespacePath(spec.NQN)+"/enable", "1", "nvmet namespace enable"),
		linkIfAbsentOrEqualCommand(h.cfg.subsystemPath(spec.NQN), h.cfg.portSubsystemPath(spec.Port, spec.NQN)),
	)
	return commands
}

func (h *CommandHost) EnsurePortCommands(port int) []Command {
	return []Command{
		{Name: "mkdir", Args: []string{"-p", filepathJoin(h.cfg.portPath(port), "subsystems")}},
		writeIfEmptyOrEqualCommand(h.cfg.portPath(port)+"/addr_trtype", h.cfg.portTransport()),
		writeIfEmptyOrEqualCommand(h.cfg.portPath(port)+"/addr_adrfam", h.cfg.portAddressFamily()),
		writeIfEmptyOrEqualCommand(h.cfg.portPath(port)+"/addr_traddr", h.cfg.Address),
		writeIfEmptyOrEqualCommand(h.cfg.portPath(port)+"/addr_trsvcid", h.cfg.portServiceID(port)),
	}
}

func (h *CommandHost) DeleteVolume(ctx context.Context, volumeID string) error {
	state, err := h.GetVolume(ctx, volumeID)
	if err != nil {
		if backend.IsVolumeNotFound(err) {
			return nil
		}
		return err
	}
	if err := h.validateDeleteState(*state); err != nil {
		return err
	}

	return h.runAll(ctx, h.DeleteVolumeCommands(*state))
}

func (h *CommandHost) DeleteVolumeCommands(state VolumeState) []Command {
	return []Command{
		disableIfPresentCommand(h.cfg.namespacePath(state.NQN) + "/enable"),
		removeSymlinkIfPresentCommand(h.cfg.portSubsystemPath(state.Port, state.NQN)),
		rmdirIfPresentCommand(h.cfg.namespacePath(state.NQN)),
		rmdirIfPresentCommand(h.cfg.subsystemPath(state.NQN)),
		{Name: "zfs", Args: []string{"destroy", state.Dataset}},
	}
}

func (h *CommandHost) validateDeleteState(state VolumeState) error {
	if state.ID == "" {
		return fmt.Errorf("refusing to delete volume: volume ID is missing")
	}
	if state.OwnerVolumeID == "" {
		return fmt.Errorf("refusing to delete %s: ZFS owner volume ID metadata is missing", state.ID)
	}
	if state.OwnerVolumeID != state.ID {
		return fmt.Errorf("refusing to delete %s: ZFS owner volume ID metadata %q differs from expected %q", state.ID, state.OwnerVolumeID, state.ID)
	}
	if state.Dataset != h.cfg.datasetFor(state.ID) {
		return fmt.Errorf("refusing to delete %s: ZFS dataset %q differs from expected %q", state.ID, state.Dataset, h.cfg.datasetFor(state.ID))
	}
	if state.DevicePath != h.cfg.devicePathFor(state.ID) {
		return fmt.Errorf("refusing to delete %s: zvol device path %q differs from expected %q", state.ID, state.DevicePath, h.cfg.devicePathFor(state.ID))
	}
	if state.NQN == "" {
		return fmt.Errorf("refusing to delete %s: NVMe/TCP NQN metadata is missing", state.ID)
	}
	if state.Port <= 0 {
		return fmt.Errorf("refusing to delete %s: NVMe/TCP port metadata is missing", state.ID)
	}
	if state.ExportDevice != "" && state.ExportDevice != state.DevicePath {
		return fmt.Errorf("refusing to delete %s: nvmet namespace device path %q differs from expected %q", state.ID, state.ExportDevice, state.DevicePath)
	}
	expectedSubsystem := h.cfg.subsystemPath(state.NQN)
	if state.PortLinkTarget != "" && !h.cfg.portLinkTargetMatches(state.Port, state.NQN, state.PortLinkTarget) {
		return fmt.Errorf("refusing to delete %s: nvmet port link target %q differs from expected %q", state.ID, state.PortLinkTarget, expectedSubsystem)
	}
	if state.PortEntryExists && state.PortLinkTarget == "" {
		return fmt.Errorf("refusing to delete %s: nvmet port entry exists but is not an expected symlink", state.ID)
	}
	return nil
}

func (h *CommandHost) ResizeVolume(ctx context.Context, volumeID string, sizeBytes int64) error {
	return h.runAll(ctx, h.ResizeVolumeCommands(volumeID, sizeBytes))
}

func (h *CommandHost) ResizeVolumeCommands(volumeID string, sizeBytes int64) []Command {
	return []Command{
		{
			Name: "zfs",
			Args: []string{"set", "volsize=" + strconv.FormatInt(sizeBytes, 10), h.cfg.datasetFor(volumeID)},
		},
	}
}

func (h *CommandHost) GetCapacity(ctx context.Context) (*backend.Capacity, error) {
	out, err := h.run(ctx, Command{
		Name: "zfs",
		Args: []string{"list", "-H", "-p", "-o", "avail,used", h.cfg.Pool},
	})
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return nil, fmt.Errorf("unexpected zfs capacity row %q", strings.TrimSpace(out))
	}
	freeBytes, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse zfs available capacity: %w", err)
	}
	usedBytes, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse zfs used capacity: %w", err)
	}
	return &backend.Capacity{
		FreeBytes:  freeBytes,
		UsedBytes:  usedBytes,
		TotalBytes: freeBytes + usedBytes,
	}, nil
}

// exportProbe is the observed nvmet configfs state for one volume, gathered
// by a single remote script so observation cost stays at two executor
// round-trips per volume instead of one per attribute.
type exportProbe struct {
	nsEnable       string
	nsDevicePath   string
	portEntry      bool
	portLink       string
	portAddress    string
	portServiceID  string
	portTransport  string
	portAddressFam string
}

func (p exportProbe) namespaceReady() bool {
	return p.nsEnable == "1"
}

func (h *CommandHost) probeExportState(ctx context.Context, nqn string, port int) (exportProbe, error) {
	if nqn == "" && port <= 0 {
		return exportProbe{}, nil
	}
	out, err := h.run(ctx, h.probeExportStateCommand(nqn, port))
	if err != nil {
		return exportProbe{}, fmt.Errorf("probe nvmet export state for %s: %w", nqn, err)
	}
	return parseExportProbe(out), nil
}

// probeExportStateCommand emits one `key=value` line per observed attribute.
// Every read tolerates absence (empty value) so the script never fails on a
// fresh or partially-built configfs tree.
func (h *CommandHost) probeExportStateCommand(nqn string, port int) Command {
	var script strings.Builder
	probeFile := func(key string, path string) {
		script.WriteString("printf '" + key + "=%s\\n' \"$(cat " + shellQuote(path) + " 2>/dev/null | tr -d '\\n')\"; ")
	}
	if nqn != "" {
		namespacePath := h.cfg.namespacePath(nqn)
		probeFile("ns_enable", namespacePath+"/enable")
		probeFile("ns_device_path", namespacePath+"/device_path")
	}
	if port > 0 {
		portPath := h.cfg.portPath(port)
		probeFile("addr_traddr", portPath+"/addr_traddr")
		probeFile("addr_trsvcid", portPath+"/addr_trsvcid")
		probeFile("addr_trtype", portPath+"/addr_trtype")
		probeFile("addr_adrfam", portPath+"/addr_adrfam")
		if nqn != "" {
			link := h.cfg.portSubsystemPath(port, nqn)
			script.WriteString("if [ -e " + shellQuote(link) + " ] || [ -L " + shellQuote(link) + " ]; then printf 'port_entry=1\\n'; else printf 'port_entry=0\\n'; fi; ")
			script.WriteString("printf 'port_link=%s\\n' \"$(readlink " + shellQuote(link) + " 2>/dev/null | tr -d '\\n')\"; ")
		}
	}
	script.WriteString("exit 0")
	return Command{Name: "sh", Args: []string{"-c", script.String()}}
}

func parseExportProbe(out string) exportProbe {
	probe := exportProbe{}
	for _, line := range strings.Split(out, "\n") {
		key, value, found := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if !found {
			continue
		}
		switch key {
		case "ns_enable":
			probe.nsEnable = value
		case "ns_device_path":
			probe.nsDevicePath = value
		case "port_entry":
			probe.portEntry = value == "1"
		case "port_link":
			probe.portLink = value
		case "addr_traddr":
			probe.portAddress = value
		case "addr_trsvcid":
			probe.portServiceID = value
		case "addr_trtype":
			probe.portTransport = value
		case "addr_adrfam":
			probe.portAddressFam = value
		}
	}
	return probe
}

func (h *CommandHost) portReady(address, serviceID, transport, addressFamily string, port int) bool {
	return address == h.cfg.Address &&
		serviceID == h.cfg.portServiceID(port) &&
		transport == h.cfg.portTransport() &&
		addressFamily == h.cfg.portAddressFamily()
}

func (h *CommandHost) run(ctx context.Context, cmd Command) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	out, err := h.exec.Run(ctx, cmd)
	if err != nil {
		return out, err
	}
	return out, nil
}

func (h *CommandHost) runAll(ctx context.Context, commands []Command) error {
	for _, cmd := range commands {
		if _, err := h.run(ctx, cmd); err != nil {
			return err
		}
	}
	return nil
}

// writeIfDifferentCommand writes value only when the current content differs.
// Safe for mutable nvmet attributes (e.g. attr_allow_any_host) where a
// matching value must not be rewritten pointlessly and a differing value is
// legal to change.
func writeIfDifferentCommand(path string, value string) Command {
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"current=$(cat " + shellQuote(path) + " 2>/dev/null | tr -d '\\n'); " +
				"if [ \"$current\" = " + shellQuote(value) + " ]; then exit 0; fi; " +
				"printf '%s\\n' " + shellQuote(value) + " > " + shellQuote(path),
		},
	}
}

// setNamespaceDevicePathCommand writes the namespace device_path with
// read-before-write semantics. nvmet rejects device_path writes while the
// namespace is enabled (-EBUSY), so a matching value is a no-op, a differing
// value on a disabled namespace is written, and a differing value on an
// enabled namespace is surfaced as an explicit repair-required error instead
// of a confusing kernel write failure.
func setNamespaceDevicePathCommand(namespacePath string, devicePath string) Command {
	target := namespacePath + "/device_path"
	enable := namespacePath + "/enable"
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"current=$(cat " + shellQuote(target) + " 2>/dev/null | tr -d '\\n'); " +
				"if [ \"$current\" = " + shellQuote(devicePath) + " ]; then exit 0; fi; " +
				"enabled=$(cat " + shellQuote(enable) + " 2>/dev/null | tr -d '\\n'); " +
				"if [ \"$enabled\" = '1' ]; then printf '%s\\n' " + shellQuote("refusing to rewrite device_path of enabled nvmet namespace "+namespacePath) + " >&2; exit 1; fi; " +
				"printf '%s\\n' " + shellQuote(devicePath) + " > " + shellQuote(target),
		},
	}
}

// enableNamespaceCommand enables the namespace only when it is not already
// enabled, so repeated create/repair passes stay idempotent on kernels that
// reject re-enabling an enabled namespace.
func enableNamespaceCommand(namespacePath string) Command {
	enable := namespacePath + "/enable"
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"current=$(cat " + shellQuote(enable) + " 2>/dev/null | tr -d '\\n'); " +
				"if [ \"$current\" = '1' ]; then exit 0; fi; " +
				"printf '%s\\n' 1 > " + shellQuote(enable),
		},
	}
}

func waitForPathCommand(path string, description string) Command {
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"for i in $(seq 1 50); do " +
				"if [ -e " + shellQuote(path) + " ]; then exit 0; fi; " +
				"sleep 0.1; " +
				"done; " +
				"printf '%s\\n' " + shellQuote("timed out waiting for "+description+" "+path) + " >&2; exit 1",
		},
	}
}

func verifyFileEqualsCommand(path string, expected string, description string) Command {
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"current=$(cat " + shellQuote(path) + " 2>/dev/null | tr -d '\\n'); " +
				"if [ \"$current\" = " + shellQuote(expected) + " ]; then exit 0; fi; " +
				"printf '%s\\n' " + shellQuote("unexpected "+description+" value at "+path) + " >&2; exit 1",
		},
	}
}

func writeIfEmptyOrEqualCommand(path string, value string) Command {
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"current=$(cat " + shellQuote(path) + " 2>/dev/null | tr -d '\\n'); " +
				"if [ \"$current\" = " + shellQuote(value) + " ]; then exit 0; fi; " +
				"if [ -z \"$current\" ]; then printf '%s\\n' " + shellQuote(value) + " > " + shellQuote(path) + "; " +
				"else printf '%s\\n' 'refusing to rewrite nvmet port attribute " + path + "' >&2; exit 1; fi",
		},
	}
}

func linkIfAbsentOrEqualCommand(target string, link string) Command {
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"if [ -L " + shellQuote(link) + " ]; then " +
				"current=$(readlink " + shellQuote(link) + "); " +
				"current_abs=$(readlink -f " + shellQuote(link) + " 2>/dev/null || true); " +
				"target_abs=$(readlink -f " + shellQuote(target) + " 2>/dev/null || printf '%s\\n' " + shellQuote(target) + "); " +
				"if [ \"$current\" = " + shellQuote(target) + " ] || [ \"$current_abs\" = \"$target_abs\" ]; then exit 0; fi; " +
				"printf '%s\\n' " + shellQuote("refusing to rewrite nvmet port symlink "+link) + " >&2; exit 1; " +
				"elif [ -e " + shellQuote(link) + " ]; then " +
				"printf '%s\\n' " + shellQuote("refusing to replace non-symlink nvmet port entry "+link) + " >&2; exit 1; " +
				"else ln -s " + shellQuote(target) + " " + shellQuote(link) + "; fi",
		},
	}
}

func disableIfPresentCommand(path string) Command {
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"if [ -e " + shellQuote(path) + " ]; then printf '%s\\n' 0 > " + shellQuote(path) + "; fi",
		},
	}
}

func removeSymlinkIfPresentCommand(path string) Command {
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"if [ -L " + shellQuote(path) + " ]; then rm " + shellQuote(path) + "; fi",
		},
	}
}

func rmdirIfPresentCommand(path string) Command {
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"if [ -d " + shellQuote(path) + " ]; then rmdir " + shellQuote(path) + "; fi",
		},
	}
}

func destroyDatasetIfOwnedCommand(dataset string, volumeID string, nqn string) Command {
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"if ! zfs list -H " + shellQuote(dataset) + " >/dev/null 2>&1; then exit 0; fi; " +
				"owner=$(zfs get -H -o value " + shellQuote(zfsPropVolumeID) + " " + shellQuote(dataset) + " 2>/dev/null | tr -d '\\n' || true); " +
				"actual_nqn=$(zfs get -H -o value " + shellQuote(zfsPropNQN) + " " + shellQuote(dataset) + " 2>/dev/null | tr -d '\\n' || true); " +
				"if [ \"$owner\" = " + shellQuote(volumeID) + " ] && [ \"$actual_nqn\" = " + shellQuote(nqn) + " ]; then " +
				"zfs destroy " + shellQuote(dataset) + "; " +
				"else printf '%s\\n' " + shellQuote("refusing to rollback unowned zvol "+dataset) + " >&2; exit 1; fi",
		},
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func isZFSNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "dataset does not exist") ||
		strings.Contains(msg, "filesystem does not exist") ||
		strings.Contains(msg, "could not find")
}

func statusFromExport(exported bool) string {
	if exported {
		return "ready"
	}
	return "not-exported"
}

func filepathJoin(elem ...string) string {
	out := elem[0]
	for _, item := range elem[1:] {
		out = strings.TrimRight(out, "/") + "/" + strings.TrimLeft(item, "/")
	}
	return out
}
