package driver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagebackend "git.srvlab.io/whiskey/rds-csi-driver/pkg/backend"
)

type resolvedControllerBackend struct {
	backend   storagebackend.Backend
	selection BackendSelection
}

type resolvedSnapshotBackend struct {
	backend   storagebackend.Backend
	selection BackendSelection
	snapshot  *storagebackend.Snapshot
}

func (d *Driver) controllerBackendForParams(params map[string]string) (storagebackend.Backend, BackendSelection, error) {
	selection, err := ParseBackendSelection(params)
	if err != nil {
		return nil, selection, err
	}
	backend, err := d.controllerBackendForSelection(selection)
	return backend, selection, err
}

func (d *Driver) controllerBackendForVolume(ctx context.Context, volumeID string) (storagebackend.Backend, BackendSelection, error) {
	if selection, ok := d.rememberedBackendSelection(volumeID); ok {
		backend, err := d.controllerBackendForSelection(selection)
		return backend, selection, err
	}

	if selection, ok, err := d.backendSelectionFromPersistentVolume(ctx, volumeID); err != nil {
		return nil, selection, err
	} else if ok {
		d.rememberVolumeBackendSelection(volumeID, selection)
		backend, err := d.controllerBackendForSelection(selection)
		return backend, selection, err
	}

	selection := DefaultBackendSelection()
	backend, err := d.controllerBackendForSelection(selection)
	return backend, selection, err
}

func (d *Driver) controllerBackendsForKnownSelections(ctx context.Context) ([]resolvedControllerBackend, error) {
	if d == nil {
		return nil, fmt.Errorf("driver is nil")
	}

	selections := map[string]BackendSelection{}
	add := func(selection BackendSelection) {
		if selection.Type == "" {
			return
		}
		selections[selection.cacheKey()] = selection
	}

	if d.controllerBackend() != nil {
		add(DefaultBackendSelection())
	}

	d.volumeBackendMu.Lock()
	for _, selection := range d.volumeBackendSelections {
		add(selection)
	}
	d.volumeBackendMu.Unlock()

	d.snapshotBackendMu.Lock()
	for _, selection := range d.snapshotBackendSelections {
		add(selection)
	}
	d.snapshotBackendMu.Unlock()

	pvSelections, err := d.backendSelectionsFromPersistentVolumes(ctx)
	if err != nil {
		return nil, err
	}
	for _, selection := range pvSelections {
		add(selection)
	}

	keys := make([]string, 0, len(selections))
	for key := range selections {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	backends := make([]resolvedControllerBackend, 0, len(keys))
	for _, key := range keys {
		selection := selections[key]
		backend, err := d.controllerBackendForSelection(selection)
		if err != nil {
			return nil, err
		}
		if backend == nil {
			continue
		}
		backends = append(backends, resolvedControllerBackend{backend: backend, selection: selection})
	}
	return backends, nil
}

func (d *Driver) controllerBackendForSnapshot(ctx context.Context, snapshotID string) (resolvedSnapshotBackend, bool, error) {
	if d == nil {
		return resolvedSnapshotBackend{}, false, fmt.Errorf("driver is nil")
	}

	if selection, ok := d.rememberedSnapshotBackendSelection(snapshotID); ok {
		backend, err := d.controllerBackendForSelection(selection)
		if err != nil {
			return resolvedSnapshotBackend{}, false, err
		}
		snapshot, err := backend.GetSnapshot(ctx, snapshotID)
		if err != nil {
			if storagebackend.IsSnapshotNotFound(err) {
				d.forgetSnapshotBackendSelection(snapshotID)
				return resolvedSnapshotBackend{}, false, nil
			}
			return resolvedSnapshotBackend{}, false, err
		}
		return resolvedSnapshotBackend{backend: backend, selection: selection, snapshot: snapshot}, true, nil
	}

	backends, err := d.controllerBackendsForKnownSelections(ctx)
	if err != nil {
		return resolvedSnapshotBackend{}, false, err
	}

	var found *resolvedSnapshotBackend
	for _, resolved := range backends {
		snapshot, err := resolved.backend.GetSnapshot(ctx, snapshotID)
		if err != nil {
			if storagebackend.IsSnapshotNotFound(err) {
				continue
			}
			return resolvedSnapshotBackend{}, false, err
		}
		current := resolvedSnapshotBackend{
			backend:   resolved.backend,
			selection: resolved.selection,
			snapshot:  snapshot,
		}
		if found != nil {
			return resolvedSnapshotBackend{}, false, fmt.Errorf("snapshot %s exists on multiple backends (%s/%s and %s/%s)",
				snapshotID,
				found.backend.Name(), found.backend.Address(),
				current.backend.Name(), current.backend.Address())
		}
		found = &current
	}

	if found == nil {
		return resolvedSnapshotBackend{}, false, nil
	}
	d.rememberSnapshotBackendSelection(snapshotID, found.selection)
	return *found, true, nil
}

func (d *Driver) controllerBackendForSelection(selection BackendSelection) (storagebackend.Backend, error) {
	if d == nil {
		return nil, fmt.Errorf("driver is nil")
	}
	key := selection.cacheKey()

	d.backendsMu.Lock()
	if d.backends == nil {
		d.backends = map[string]storagebackend.Backend{}
	}
	if backend := d.backends[key]; backend != nil {
		d.backendsMu.Unlock()
		return backend, nil
	}
	d.backendsMu.Unlock()

	factory := d.backendFactory
	if factory == nil {
		factory = d.newBackendForSelection
	}
	created, err := factory(selection)
	if err != nil {
		return nil, err
	}

	d.backendsMu.Lock()
	if existing := d.backends[key]; existing != nil {
		d.backendsMu.Unlock()
		return existing, nil
	}
	d.backends[key] = created
	d.backendsMu.Unlock()
	return created, nil
}

func (d *Driver) rememberVolumeBackendSelection(volumeID string, selection BackendSelection) {
	if d == nil || volumeID == "" {
		return
	}
	d.volumeBackendMu.Lock()
	defer d.volumeBackendMu.Unlock()
	if d.volumeBackendSelections == nil {
		d.volumeBackendSelections = map[string]BackendSelection{}
	}
	d.volumeBackendSelections[volumeID] = selection
}

func (d *Driver) forgetVolumeBackendSelection(volumeID string) {
	if d == nil || volumeID == "" {
		return
	}
	d.volumeBackendMu.Lock()
	defer d.volumeBackendMu.Unlock()
	delete(d.volumeBackendSelections, volumeID)
}

func (d *Driver) rememberSnapshotBackendSelection(snapshotID string, selection BackendSelection) {
	if d == nil || snapshotID == "" {
		return
	}
	d.snapshotBackendMu.Lock()
	defer d.snapshotBackendMu.Unlock()
	if d.snapshotBackendSelections == nil {
		d.snapshotBackendSelections = map[string]BackendSelection{}
	}
	d.snapshotBackendSelections[snapshotID] = selection
}

func (d *Driver) forgetSnapshotBackendSelection(snapshotID string) {
	if d == nil || snapshotID == "" {
		return
	}
	d.snapshotBackendMu.Lock()
	defer d.snapshotBackendMu.Unlock()
	delete(d.snapshotBackendSelections, snapshotID)
}

func (d *Driver) rememberedBackendSelection(volumeID string) (BackendSelection, bool) {
	if d == nil || volumeID == "" {
		return BackendSelection{}, false
	}
	d.volumeBackendMu.Lock()
	defer d.volumeBackendMu.Unlock()
	selection, ok := d.volumeBackendSelections[volumeID]
	return selection, ok
}

func (d *Driver) rememberedSnapshotBackendSelection(snapshotID string) (BackendSelection, bool) {
	if d == nil || snapshotID == "" {
		return BackendSelection{}, false
	}
	d.snapshotBackendMu.Lock()
	defer d.snapshotBackendMu.Unlock()
	selection, ok := d.snapshotBackendSelections[snapshotID]
	return selection, ok
}

func (d *Driver) backendSelectionFromPersistentVolume(ctx context.Context, volumeID string) (BackendSelection, bool, error) {
	if d == nil || d.k8sClient == nil || volumeID == "" {
		return BackendSelection{}, false, nil
	}
	pvs, err := d.k8sClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return BackendSelection{}, false, fmt.Errorf("list persistent volumes for backend selection: %w", err)
	}
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		if !persistentVolumeMatchesDriver(pv, d.name, volumeID) {
			continue
		}
		selection, err := ParseBackendSelection(pv.Spec.CSI.VolumeAttributes)
		if err != nil {
			return selection, true, fmt.Errorf("parse backend selection from persistent volume %s: %w", pv.Name, err)
		}
		return selection, true, nil
	}
	return BackendSelection{}, false, nil
}

func (d *Driver) backendSelectionsFromPersistentVolumes(ctx context.Context) ([]BackendSelection, error) {
	if d == nil || d.k8sClient == nil {
		return nil, nil
	}
	pvs, err := d.k8sClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list persistent volumes for backend selection: %w", err)
	}

	seen := map[string]BackendSelection{}
	for i := range pvs.Items {
		pv := &pvs.Items[i]
		if pv == nil || pv.Spec.CSI == nil {
			continue
		}
		if !persistentVolumeBelongsToDriver(pv, d.name) {
			continue
		}
		selection, err := ParseBackendSelection(pv.Spec.CSI.VolumeAttributes)
		if err != nil {
			return nil, fmt.Errorf("parse backend selection from persistent volume %s: %w", pv.Name, err)
		}
		seen[selection.cacheKey()] = selection
		d.rememberVolumeBackendSelection(pv.Spec.CSI.VolumeHandle, selection)
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	selections := make([]BackendSelection, 0, len(keys))
	for _, key := range keys {
		selections = append(selections, seen[key])
	}
	return selections, nil
}

func persistentVolumeBelongsToDriver(pv *corev1.PersistentVolume, driverName string) bool {
	if pv == nil || pv.Spec.CSI == nil {
		return false
	}
	if driverName == "" {
		driverName = DriverName
	}
	return pv.Spec.CSI.Driver == driverName
}

func persistentVolumeMatchesDriver(pv *corev1.PersistentVolume, driverName string, volumeID string) bool {
	if !persistentVolumeBelongsToDriver(pv, driverName) {
		return false
	}
	if pv.Spec.CSI.VolumeHandle != volumeID {
		return false
	}
	return true
}

func (s BackendSelection) cacheKey() string {
	return strings.Join([]string{
		s.Type,
		s.Executor,
		s.ZFSPool,
		s.TargetAddr,
		s.SSHTarget,
		s.DeviceRoot,
		s.ConfigFSRoot,
	}, "\x00")
}
