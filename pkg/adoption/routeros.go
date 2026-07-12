package adoption

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/backend"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
)

const (
	ImportActionConflict       = "conflict"
	ImportActionWaitForDetach  = "wait-for-detach"
	ImportActionReleaseSource  = "release-source-pv"
	ImportActionReplaceSource  = "replace-source-pv"
	ImportActionAlreadyAdopted = "already-adopted"
)

type Check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type RouterOSImportPlan struct {
	VolumeID    string          `json:"volumeID"`
	Action      string          `json:"action"`
	Ready       bool            `json:"ready"`
	SourcePV    string          `json:"sourcePV,omitempty"`
	TargetPV    string          `json:"targetPV,omitempty"`
	Claim       string          `json:"claim,omitempty"`
	Path        string          `json:"path,omitempty"`
	SizeBytes   int64           `json:"sizeBytes,omitempty"`
	NQN         string          `json:"nqn,omitempty"`
	Port        int             `json:"port,omitempty"`
	Attachments []string        `json:"attachments,omitempty"`
	Checks      []Check         `json:"checks"`
	Volume      *backend.Volume `json:"volume,omitempty"`
}

// BuildRouterOSImportPlan validates a zero-copy ownership transfer without
// mutating Kubernetes or RouterOS. The legacy PV is the durable import manifest:
// its handle, connection attributes, capacity, and Retain policy must agree with
// the observed RouterOS disk before the old CSI identity is released.
func BuildRouterOSImportPlan(
	volumeID, sourceDriver, targetDriver, managedNQNPrefix string,
	observed *backend.Volume,
	pvs []corev1.PersistentVolume,
	attachments []storagev1.VolumeAttachment,
) RouterOSImportPlan {
	plan := RouterOSImportPlan{
		VolumeID: volumeID,
		Action:   ImportActionConflict,
		Checks:   make([]Check, 0, 16),
		Volume:   observed,
	}
	check := func(name string, ok bool, detail string) {
		plan.Checks = append(plan.Checks, Check{Name: name, OK: ok, Detail: detail})
	}

	check("volume-id", volumeID != "", fmt.Sprintf("volumeHandle=%q", volumeID))
	if err := utils.ValidateVolumeID(volumeID); err != nil {
		check("volume-id-format", false, err.Error())
	} else {
		check("volume-id-format", true, "valid CSI volume handle")
	}

	var sourcePV, targetPV *corev1.PersistentVolume
	foreignPVs := make([]string, 0)
	for i := range pvs {
		pv := &pvs[i]
		if pv.Spec.CSI == nil || pv.Spec.CSI.VolumeHandle != volumeID {
			continue
		}
		switch pv.Spec.CSI.Driver {
		case sourceDriver:
			if sourcePV == nil {
				sourcePV = pv
			} else {
				foreignPVs = append(foreignPVs, pv.Name)
			}
		case targetDriver:
			if targetPV == nil {
				targetPV = pv
			} else {
				foreignPVs = append(foreignPVs, pv.Name)
			}
		default:
			foreignPVs = append(foreignPVs, fmt.Sprintf("%s(%s)", pv.Name, pv.Spec.CSI.Driver))
		}
	}
	if sourcePV != nil {
		plan.SourcePV = sourcePV.Name
		if sourcePV.Spec.ClaimRef != nil {
			plan.Claim = sourcePV.Spec.ClaimRef.Namespace + "/" + sourcePV.Spec.ClaimRef.Name
		}
	}
	if targetPV != nil {
		plan.TargetPV = targetPV.Name
		if plan.Claim == "" && targetPV.Spec.ClaimRef != nil {
			plan.Claim = targetPV.Spec.ClaimRef.Namespace + "/" + targetPV.Spec.ClaimRef.Name
		}
	}
	check("single-owner-pv", len(foreignPVs) == 0 && (sourcePV == nil || targetPV == nil),
		fmt.Sprintf("source=%q target=%q unexpected=%v", plan.SourcePV, plan.TargetPV, foreignPVs))

	manifestPV := sourcePV
	if manifestPV == nil {
		manifestPV = targetPV
	}
	if manifestPV == nil || manifestPV.Spec.CSI == nil {
		check("import-manifest", false, "no source or target PV records this volume handle")
	} else {
		attrs := manifestPV.Spec.CSI.VolumeAttributes
		plan.Path = attrs["volumePath"]
		plan.NQN = attrs["nqn"]
		if port, err := utils.ValidatePortString(attrs["nvmePort"], true); err == nil {
			plan.Port = port
		}
		if quantity, ok := manifestPV.Spec.Capacity[corev1.ResourceStorage]; ok {
			plan.SizeBytes = quantity.Value()
		}
		check("retain-policy", manifestPV.Spec.PersistentVolumeReclaimPolicy == corev1.PersistentVolumeReclaimRetain,
			fmt.Sprintf("PV %s reclaimPolicy=%s", manifestPV.Name, manifestPV.Spec.PersistentVolumeReclaimPolicy))
		check("manifest-path", plan.Path != "", fmt.Sprintf("volumePath=%q", plan.Path))
		check("manifest-nqn", plan.NQN != "", fmt.Sprintf("nqn=%q", plan.NQN))
		check("manifest-port", plan.Port > 0, fmt.Sprintf("nvmePort=%q", attrs["nvmePort"]))
		check("manifest-size", plan.SizeBytes > 0, fmt.Sprintf("sizeBytes=%d", plan.SizeBytes))
	}

	if observed == nil {
		check("routeros-volume", false, "RouterOS disk slot was not observed")
	} else {
		check("routeros-volume", observed.ID == volumeID, fmt.Sprintf("observed slot=%q", observed.ID))
		check("routeros-ready", strings.EqualFold(observed.Status, "ready"), fmt.Sprintf("status=%q", observed.Status))
		check("routeros-export", observed.NVMETCPExport, fmt.Sprintf("nvmeTCPExport=%t", observed.NVMETCPExport))
		check("path-match", plan.Path != "" && observed.Path == plan.Path,
			fmt.Sprintf("manifest=%q observed=%q", plan.Path, observed.Path))
		check("size-match", plan.SizeBytes > 0 && observed.SizeBytes == plan.SizeBytes,
			fmt.Sprintf("manifest=%d observed=%d", plan.SizeBytes, observed.SizeBytes))
		check("nqn-match", plan.NQN != "" && observed.NVMETCPNQN == plan.NQN,
			fmt.Sprintf("manifest=%q observed=%q", plan.NQN, observed.NVMETCPNQN))
		check("port-match", plan.Port > 0 && observed.NVMETCPPort == plan.Port,
			fmt.Sprintf("manifest=%d observed=%d", plan.Port, observed.NVMETCPPort))
	}

	ownedNQN, nqnErr := utils.VolumeIDToNQNWithPrefix(volumeID, managedNQNPrefix)
	check("target-nqn-ownership", nqnErr == nil && plan.NQN == ownedNQN,
		fmt.Sprintf("manifest=%q target-owned=%q", plan.NQN, ownedNQN))

	for i := range attachments {
		va := &attachments[i]
		if va.Spec.Source.PersistentVolumeName == nil {
			continue
		}
		pvName := *va.Spec.Source.PersistentVolumeName
		if (sourcePV != nil && pvName == sourcePV.Name) || (targetPV != nil && pvName == targetPV.Name) {
			plan.Attachments = append(plan.Attachments, fmt.Sprintf("%s:%s:%s", va.Name, va.Spec.Attacher, va.Spec.NodeName))
		}
	}
	check("detached", len(plan.Attachments) == 0, fmt.Sprintf("attachments=%v", plan.Attachments))

	for _, result := range plan.Checks {
		if !result.OK && result.Name != "detached" {
			return plan
		}
	}
	if len(plan.Attachments) > 0 {
		plan.Action = ImportActionWaitForDetach
		return plan
	}
	if targetPV != nil {
		plan.Action = ImportActionAlreadyAdopted
		plan.Ready = true
		return plan
	}
	if sourcePV == nil {
		return plan
	}
	if sourcePV.Status.Phase == corev1.VolumeBound {
		plan.Action = ImportActionReleaseSource
		return plan
	}
	plan.Action = ImportActionReplaceSource
	plan.Ready = true
	return plan
}
