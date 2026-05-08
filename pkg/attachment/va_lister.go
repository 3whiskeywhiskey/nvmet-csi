package attachment

import (
	"context"

	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// ListDriverVolumeAttachments lists all VolumeAttachments for our driver (nvmet.csi.whiskey.works).
// Returns empty slice (not nil) if no attachments found.
func ListDriverVolumeAttachments(ctx context.Context, k8sClient kubernetes.Interface) ([]*storagev1.VolumeAttachment, error) {
	// List all VolumeAttachments in the cluster
	vaList, err := k8sClient.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}

	// Filter for our driver
	result := make([]*storagev1.VolumeAttachment, 0, len(vaList.Items))
	for i := range vaList.Items {
		va := &vaList.Items[i]
		if va.Spec.Attacher == driverName {
			result = append(result, va)
		}
	}

	klog.V(2).Infof("Listed VolumeAttachments: total=%d, driver=%d", len(vaList.Items), len(result))
	return result, nil
}

// FilterAttachedVolumeAttachments filters VolumeAttachments to only those with status.attached=true.
// Returns empty slice (not nil) if no attached volumes found.
func FilterAttachedVolumeAttachments(attachments []*storagev1.VolumeAttachment) []*storagev1.VolumeAttachment {
	result := make([]*storagev1.VolumeAttachment, 0, len(attachments))
	for _, va := range attachments {
		if va.Status.Attached {
			result = append(result, va)
		}
	}
	return result
}

// GroupVolumeAttachmentsByVolume groups VolumeAttachments by volume ID (PersistentVolumeName).
// Skips VolumeAttachments with nil PersistentVolumeName (logs warning).
// Returns empty map (not nil) if no valid attachments found.
func GroupVolumeAttachmentsByVolume(attachments []*storagev1.VolumeAttachment) map[string][]*storagev1.VolumeAttachment {
	result := make(map[string][]*storagev1.VolumeAttachment)

	for _, va := range attachments {
		// Skip if PersistentVolumeName is nil or empty
		if va.Spec.Source.PersistentVolumeName == nil || *va.Spec.Source.PersistentVolumeName == "" {
			klog.Warningf("VolumeAttachment %s has nil or empty PersistentVolumeName, skipping", va.Name)
			continue
		}

		volumeID := *va.Spec.Source.PersistentVolumeName
		result[volumeID] = append(result[volumeID], va)
	}

	return result
}
