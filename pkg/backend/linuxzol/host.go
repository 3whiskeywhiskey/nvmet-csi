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
	exported := h.namespaceEnabled(ctx, nqn)

	return &VolumeState{
		ID:         volumeID,
		Dataset:    dataset,
		DevicePath: h.cfg.devicePathFor(volumeID),
		SizeBytes:  sizeBytes,
		NQN:        nqn,
		Port:       port,
		Exported:   exported,
		Status:     statusFromExport(exported),
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
		exported := h.namespaceEnabled(ctx, strings.TrimSpace(nqn))
		volumes = append(volumes, VolumeState{
			ID:         id,
			Dataset:    fields[0],
			DevicePath: h.cfg.devicePathFor(id),
			SizeBytes:  sizeBytes,
			NQN:        strings.TrimSpace(nqn),
			Port:       port,
			Exported:   exported,
			Status:     statusFromExport(exported),
		})
	}
	return volumes, nil
}

func (h *CommandHost) CreateVolume(ctx context.Context, spec VolumeSpec) error {
	return h.runAll(ctx, h.CreateVolumeCommands(spec))
}

func (h *CommandHost) CreateVolumeCommands(spec VolumeSpec) []Command {
	return []Command{
		{
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
		{Name: "mkdir", Args: []string{"-p", h.cfg.namespacePath(spec.NQN)}},
		writeCommand(h.cfg.subsystemPath(spec.NQN)+"/attr_allow_any_host", "1\n"),
		writeCommand(h.cfg.namespacePath(spec.NQN)+"/device_path", spec.DevicePath+"\n"),
		writeCommand(h.cfg.namespacePath(spec.NQN)+"/enable", "1\n"),
		{Name: "mkdir", Args: []string{"-p", filepathJoin(h.cfg.portPath(spec.Port), "subsystems")}},
		{Name: "ln", Args: []string{"-s", h.cfg.subsystemPath(spec.NQN), h.cfg.portSubsystemPath(spec.Port, spec.NQN)}},
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

	return h.runAll(ctx, h.DeleteVolumeCommands(*state))
}

func (h *CommandHost) DeleteVolumeCommands(state VolumeState) []Command {
	return []Command{
		writeCommand(h.cfg.namespacePath(state.NQN)+"/enable", "0\n"),
		{Name: "rm", Args: []string{"-f", h.cfg.portSubsystemPath(state.Port, state.NQN)}},
		{Name: "rmdir", Args: []string{h.cfg.namespacePath(state.NQN)}},
		{Name: "rmdir", Args: []string{h.cfg.subsystemPath(state.NQN)}},
		{Name: "zfs", Args: []string{"destroy", state.Dataset}},
	}
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
