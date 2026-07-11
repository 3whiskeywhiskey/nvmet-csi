package driver

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	storagebackend "gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/backend"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/reconciler"
)

// listExportTargets builds the desired export topology for the export
// reconciler: every PV bound to this driver whose backend owns kernel nvmet
// exports, grouped per backend selection.
//
// Only linux-zol-nvmet selections participate. The RouterOS/RDS backend keeps
// its own persistent export state and must not receive background mutation
// traffic (see docs/routeros-rds-hardening.md).
func (d *Driver) listExportTargets(ctx context.Context) ([]reconciler.ExportTarget, error) {
	if d == nil || d.k8sClient == nil {
		return nil, fmt.Errorf("kubernetes client is required to list export targets")
	}

	pvs, err := d.k8sClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list persistent volumes for export reconciliation: %w", err)
	}

	type targetGroup struct {
		selection BackendSelection
		desired   []reconciler.DesiredExport
	}
	groups := map[string]*targetGroup{}

	for i := range pvs.Items {
		pv := &pvs.Items[i]
		if !persistentVolumeBelongsToDriver(pv, d.name) {
			continue
		}
		if pv.DeletionTimestamp != nil {
			continue
		}

		selection, err := ParseBackendSelection(pv.Spec.CSI.VolumeAttributes)
		if err != nil {
			klog.Errorf("Export reconciliation: skipping PV %s with unparseable backend selection: %v", pv.Name, err)
			continue
		}
		if selection.Type != storagebackend.TypeLinuxZOLNVMET {
			continue
		}

		key := selection.cacheKey()
		group, ok := groups[key]
		if !ok {
			group = &targetGroup{selection: selection}
			groups[key] = group
		}

		attrs := pv.Spec.CSI.VolumeAttributes
		port := 0
		if raw, ok := attrs[paramNVMEPort]; ok && raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				port = parsed
			}
		}
		group.desired = append(group.desired, reconciler.DesiredExport{
			VolumeID: pv.Spec.CSI.VolumeHandle,
			NQN:      attrs["nqn"],
			Port:     port,
		})
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	targets := make([]reconciler.ExportTarget, 0, len(keys))
	for _, key := range keys {
		group := groups[key]
		resolved, err := d.controllerBackendForSelection(group.selection)
		if err != nil {
			return nil, fmt.Errorf("resolve backend for export target %s: %w", exportTargetKey(group.selection), err)
		}
		targets = append(targets, reconciler.ExportTarget{
			Key:     exportTargetKey(group.selection),
			Backend: resolved,
			Desired: group.desired,
		})
	}
	return targets, nil
}

func exportTargetKey(selection BackendSelection) string {
	return selection.Type + "/" + selection.TargetAddr + "/" + selection.ZFSPool
}
