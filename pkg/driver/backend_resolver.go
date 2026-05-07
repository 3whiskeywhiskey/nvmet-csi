package driver

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	storagebackend "git.srvlab.io/whiskey/rds-csi-driver/pkg/backend"
)

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

func (d *Driver) rememberedBackendSelection(volumeID string) (BackendSelection, bool) {
	if d == nil || volumeID == "" {
		return BackendSelection{}, false
	}
	d.volumeBackendMu.Lock()
	defer d.volumeBackendMu.Unlock()
	selection, ok := d.volumeBackendSelections[volumeID]
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

func persistentVolumeMatchesDriver(pv *corev1.PersistentVolume, driverName string, volumeID string) bool {
	if pv == nil || pv.Spec.CSI == nil {
		return false
	}
	if pv.Spec.CSI.VolumeHandle != volumeID {
		return false
	}
	if driverName == "" {
		driverName = DriverName
	}
	return pv.Spec.CSI.Driver == driverName
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
