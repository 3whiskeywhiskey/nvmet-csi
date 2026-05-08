package linuxzol

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/backend"
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

func (h *CommandHost) GetVolume(ctx context.Context, volumeID string) (*VolumeState, error) {
	dataset := h.cfg.datasetFor(volumeID)
	sizeOut, err := h.run(ctx, Command{
		Name: "zfs",
		Args: []string{"get", "-H", "-p", "-o", "value", "volsize", dataset},
	})
	if err != nil {
		if isZFSNotFound(err) {
			return nil, &backend.VolumeNotFoundError{ID: volumeID}
		}
		return nil, err
	}
	sizeBytes, err := strconv.ParseInt(strings.TrimSpace(sizeOut), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse zvol size for %s: %w", volumeID, err)
	}

	nqn, _ := h.getZFSProperty(ctx, dataset, zfsPropNQN)
	portValue, _ := h.getZFSProperty(ctx, dataset, zfsPropPort)
	port, _ := strconv.Atoi(strings.TrimSpace(portValue))
	nqn = strings.TrimSpace(nqn)
	exportDevice := h.namespaceDevicePath(ctx, nqn)
	portEntryExists := h.portEntryExists(ctx, port, nqn)
	portLinkTarget := h.portLinkTarget(ctx, port, nqn)
	portAddress := h.portAttribute(ctx, port, "addr_traddr")
	portServiceID := h.portAttribute(ctx, port, "addr_trsvcid")
	portTransport := h.portAttribute(ctx, port, "addr_trtype")
	portAddressFam := h.portAttribute(ctx, port, "addr_adrfam")
	namespaceReady := h.namespaceEnabled(ctx, nqn)
	portReady := h.portReady(portAddress, portServiceID, portTransport, portAddressFam, port)
	exported := namespaceReady && exportDevice == h.cfg.devicePathFor(volumeID) && portLinkTarget == h.cfg.subsystemPath(nqn) && portReady

	return &VolumeState{
		ID:              volumeID,
		Dataset:         dataset,
		DevicePath:      h.cfg.devicePathFor(volumeID),
		SizeBytes:       sizeBytes,
		NQN:             nqn,
		Port:            port,
		Exported:        exported,
		NamespaceReady:  namespaceReady,
		ExportDevice:    exportDevice,
		PortEntryExists: portEntryExists,
		PortLinkTarget:  portLinkTarget,
		PortAddress:     portAddress,
		PortServiceID:   portServiceID,
		PortTransport:   portTransport,
		PortAddressFam:  portAddressFam,
		Status:          statusFromExport(exported),
	}, nil
}

func (h *CommandHost) ListVolumes(ctx context.Context) ([]VolumeState, error) {
	out, err := h.run(ctx, Command{
		Name: "zfs",
		Args: []string{"list", "-H", "-p", "-o", "name,volsize", "-t", "volume", "-r", h.cfg.Pool},
	})
	if err != nil {
		if isZFSNotFound(err) {
			return []VolumeState{}, nil
		}
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	volumes := make([]VolumeState, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("unexpected zfs list row %q", line)
		}
		id := strings.TrimPrefix(fields[0], h.cfg.Pool+"/")
		sizeBytes, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse zvol size for %s: %w", id, err)
		}
		nqn, _ := h.getZFSProperty(ctx, fields[0], zfsPropNQN)
		portValue, _ := h.getZFSProperty(ctx, fields[0], zfsPropPort)
		port, _ := strconv.Atoi(strings.TrimSpace(portValue))
		nqn = strings.TrimSpace(nqn)
		exportDevice := h.namespaceDevicePath(ctx, nqn)
		portEntryExists := h.portEntryExists(ctx, port, nqn)
		portLinkTarget := h.portLinkTarget(ctx, port, nqn)
		portAddress := h.portAttribute(ctx, port, "addr_traddr")
		portServiceID := h.portAttribute(ctx, port, "addr_trsvcid")
		portTransport := h.portAttribute(ctx, port, "addr_trtype")
		portAddressFam := h.portAttribute(ctx, port, "addr_adrfam")
		devicePath := h.cfg.devicePathFor(id)
		namespaceReady := h.namespaceEnabled(ctx, nqn)
		portReady := h.portReady(portAddress, portServiceID, portTransport, portAddressFam, port)
		exported := namespaceReady && exportDevice == devicePath && portLinkTarget == h.cfg.subsystemPath(nqn) && portReady
		volumes = append(volumes, VolumeState{
			ID:              id,
			Dataset:         fields[0],
			DevicePath:      devicePath,
			SizeBytes:       sizeBytes,
			NQN:             nqn,
			Port:            port,
			Exported:        exported,
			NamespaceReady:  namespaceReady,
			ExportDevice:    exportDevice,
			PortEntryExists: portEntryExists,
			PortLinkTarget:  portLinkTarget,
			PortAddress:     portAddress,
			PortServiceID:   portServiceID,
			PortTransport:   portTransport,
			PortAddressFam:  portAddressFam,
			Status:          statusFromExport(exported),
		})
	}
	return volumes, nil
}

func (h *CommandHost) CreateVolume(ctx context.Context, spec VolumeSpec) error {
	return h.runAll(ctx, h.CreateVolumeCommands(spec))
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
		Command{Name: "mkdir", Args: []string{"-p", h.cfg.namespacePath(spec.NQN)}},
		writeCommand(h.cfg.subsystemPath(spec.NQN)+"/attr_allow_any_host", "1\n"),
		writeCommand(h.cfg.namespacePath(spec.NQN)+"/device_path", spec.DevicePath+"\n"),
		writeCommand(h.cfg.namespacePath(spec.NQN)+"/enable", "1\n"),
		Command{Name: "ln", Args: []string{"-s", h.cfg.subsystemPath(spec.NQN), h.cfg.portSubsystemPath(spec.Port, spec.NQN)}},
	)
	return commands
}

func (h *CommandHost) RepairVolume(ctx context.Context, spec VolumeSpec) error {
	return h.runAll(ctx, h.RepairVolumeCommands(spec))
}

func (h *CommandHost) RepairVolumeCommands(spec VolumeSpec) []Command {
	commands := []Command{}
	commands = append(commands, h.EnsurePortCommands(spec.Port)...)
	commands = append(commands,
		Command{Name: "mkdir", Args: []string{"-p", h.cfg.namespacePath(spec.NQN)}},
		writeCommand(h.cfg.subsystemPath(spec.NQN)+"/attr_allow_any_host", "1\n"),
		writeCommand(h.cfg.namespacePath(spec.NQN)+"/device_path", spec.DevicePath+"\n"),
		writeCommand(h.cfg.namespacePath(spec.NQN)+"/enable", "1\n"),
		linkIfMissingCommand(h.cfg.subsystemPath(spec.NQN), h.cfg.portSubsystemPath(spec.Port, spec.NQN)),
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
	if state.PortLinkTarget != "" && state.PortLinkTarget != expectedSubsystem {
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

func (h *CommandHost) getZFSProperty(ctx context.Context, dataset string, property string) (string, error) {
	return h.run(ctx, Command{
		Name: "zfs",
		Args: []string{"get", "-H", "-p", "-o", "value", property, dataset},
	})
}

func (h *CommandHost) namespaceEnabled(ctx context.Context, nqn string) bool {
	if nqn == "" {
		return false
	}
	out, err := h.run(ctx, Command{
		Name: "cat",
		Args: []string{h.cfg.namespacePath(nqn) + "/enable"},
	})
	return err == nil && strings.TrimSpace(out) == "1"
}

func (h *CommandHost) namespaceDevicePath(ctx context.Context, nqn string) string {
	if nqn == "" {
		return ""
	}
	out, err := h.run(ctx, Command{
		Name: "cat",
		Args: []string{h.cfg.namespacePath(nqn) + "/device_path"},
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (h *CommandHost) portLinkTarget(ctx context.Context, port int, nqn string) string {
	if port <= 0 || nqn == "" {
		return ""
	}
	out, err := h.run(ctx, Command{
		Name: "readlink",
		Args: []string{h.cfg.portSubsystemPath(port, nqn)},
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (h *CommandHost) portEntryExists(ctx context.Context, port int, nqn string) bool {
	if port <= 0 || nqn == "" {
		return false
	}
	path := h.cfg.portSubsystemPath(port, nqn)
	if _, err := h.run(ctx, Command{Name: "test", Args: []string{"-e", path}}); err == nil {
		return true
	}
	_, err := h.run(ctx, Command{Name: "test", Args: []string{"-L", path}})
	return err == nil
}

func (h *CommandHost) portAttribute(ctx context.Context, port int, name string) string {
	if port <= 0 {
		return ""
	}
	out, err := h.run(ctx, Command{
		Name: "cat",
		Args: []string{h.cfg.portPath(port) + "/" + name},
	})
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
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

func writeCommand(path string, value string) Command {
	return Command{
		Name:  "tee",
		Args:  []string{path},
		Stdin: value,
	}
}

func writeIfEmptyOrEqualCommand(path string, value string) Command {
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"current=$(cat " + shellQuote(path) + " 2>/dev/null | tr -d '\\n'); " +
				"if [ -z \"$current\" ] || [ \"$current\" = " + shellQuote(value) + " ]; then " +
				"printf '%s\\n' " + shellQuote(value) + " > " + shellQuote(path) + "; " +
				"else printf '%s\\n' 'refusing to rewrite nvmet port attribute " + path + "' >&2; exit 1; fi",
		},
	}
}

func linkIfMissingCommand(target string, link string) Command {
	return Command{
		Name: "sh",
		Args: []string{
			"-c",
			"test -L " + shellQuote(link) + " || ln -s " + shellQuote(target) + " " + shellQuote(link),
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
