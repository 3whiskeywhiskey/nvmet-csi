package driver

import (
	"context"
	stderrors "errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/attachment"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/backend"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/reconciler"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/security"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/utils"
)

const (
	// Default values for storage parameters
	defaultVolumeBasePath = "/storage-pool/metal-csi"
	defaultNVMETCPPort    = 4420

	// Parameter keys for StorageClass
	paramRDSAddress  = "rdsAddress"
	paramNVMEAddress = "nvmeAddress"
	paramNVMEPort    = "nvmePort"
	paramSSHPort     = "sshPort"
	paramFSType      = "fsType"
	paramVolumePath  = "volumePath"
	paramNQNPrefix   = "nqnPrefix"

	// Minimum/maximum volume sizes
	minVolumeSizeBytes = 1 * 1024 * 1024 * 1024         // 1 GiB
	maxVolumeSizeBytes = 16 * 1024 * 1024 * 1024 * 1024 // 16 TiB
)

// ControllerServer implements the CSI Controller service
type ControllerServer struct {
	csi.UnimplementedControllerServer
	driver *Driver
}

// NewControllerServer creates a new Controller service
func NewControllerServer(driver *Driver) *ControllerServer {
	return &ControllerServer{
		driver: driver,
	}
}

func (cs *ControllerServer) lockControllerVolume(volumeID string) func() {
	if cs == nil || cs.driver == nil || volumeID == "" {
		return func() {}
	}
	cs.driver.controllerVolumeLocksMu.Lock()
	if cs.driver.controllerVolumeLocks == nil {
		cs.driver.controllerVolumeLocks = attachment.NewVolumeLockManager()
	}
	locks := cs.driver.controllerVolumeLocks
	cs.driver.controllerVolumeLocksMu.Unlock()

	locks.Lock(volumeID)
	return func() {
		locks.Unlock(volumeID)
	}
}

// CreateVolume provisions a new volume on the selected storage backend.
func (cs *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("CreateVolume CSI call for %s", req.GetName())

	// Validate request
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "volume name is required")
	}

	if req.GetVolumeCapabilities() == nil || len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities are required")
	}

	// Validate volume capabilities
	if err := cs.validateVolumeCapabilities(req.GetVolumeCapabilities()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volume capabilities: %v", err)
	}

	// Get required capacity
	requiredBytes := req.GetCapacityRange().GetRequiredBytes()
	if requiredBytes == 0 {
		requiredBytes = minVolumeSizeBytes
	}

	// Enforce size limits
	if requiredBytes < minVolumeSizeBytes {
		requiredBytes = minVolumeSizeBytes
	}

	limitBytes := req.GetCapacityRange().GetLimitBytes()
	if limitBytes > 0 && requiredBytes > limitBytes {
		return nil, status.Errorf(codes.OutOfRange, "required bytes %d exceeds limit bytes %d", requiredBytes, limitBytes)
	}

	if requiredBytes > maxVolumeSizeBytes {
		return nil, status.Errorf(codes.OutOfRange, "required bytes %d exceeds maximum %d", requiredBytes, maxVolumeSizeBytes)
	}

	// Use the volume name directly as the volume ID
	// The external-provisioner passes the PV name (pvc-<uuid>) which is already unique and deterministic
	volumeID := req.GetName()

	// Validate the volume ID format
	if err := utils.ValidateVolumeID(volumeID); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volume name format: %v", err)
	}
	klog.V(4).Infof("Using volume ID: %s (from volume name: %s)", volumeID, req.GetName())

	unlockVolume := cs.lockControllerVolume(volumeID)
	defer unlockVolume()

	// Get parameters from StorageClass
	params := req.GetParameters()
	storageBackend, selection, err := cs.driver.controllerBackendForParams(params)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid backend selection: %v", err)
	}
	volumeBasePath := defaultVolumeBasePath
	if path, ok := params[paramVolumePath]; ok {
		volumeBasePath = path
	}

	nvmePort := defaultNVMETCPPort
	if portStr, ok := params[paramNVMEPort]; ok {
		// Parse port number
		var port int
		if _, err := fmt.Sscanf(portStr, "%d", &port); err == nil {
			nvmePort = port
		}
	}

	// Parse NVMe connection parameters from StorageClass
	nvmeParams, err := ParseNVMEConnectionParams(params)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid NVMe connection parameters: %v", err)
	}

	// Parse migration timeout
	migrationTimeout := ParseMigrationTimeout(params)

	// Generate NQN
	nqn, err := utils.VolumeIDToNQN(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate NQN: %v", err)
	}

	// Generate file path
	filePath, err := utils.VolumeIDToFilePath(volumeID, volumeBasePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate file path: %v", err)
	}

	if storageBackend == nil {
		return nil, status.Error(codes.Internal, "storage backend not initialized")
	}

	// Create volume on the configured backend.
	klog.V(4).Infof("Creating volume %s on backend %s (size: %d bytes, path: %s, nqn: %s)",
		volumeID, storageBackend.Name(), requiredBytes, filePath, nqn)

	// Log volume create request
	secLogger := security.GetLogger()
	secLogger.LogVolumeCreate(volumeID, req.GetName(), security.OutcomeUnknown, nil, 0)

	createOpts := backend.CreateVolumeRequest{
		ID:          volumeID,
		Path:        filePath,
		SizeBytes:   requiredBytes,
		NVMETCPPort: nvmePort,
		NVMETCPNQN:  nqn,
		Parameters:  params,
	}

	plan, err := storageBackend.BuildEnsureVolumePlan(ctx, createOpts)
	if err != nil {
		return nil, createVolumeStatusError(err)
	}
	klog.V(3).Infof("CreateVolume plan for %s: action=%s reason=%q", volumeID, plan.Action, plan.Reason)

	if contentSource := req.GetVolumeContentSource(); contentSource != nil && plan.Action == backend.VolumeActionCreate {
		if snapshotSource := contentSource.GetSnapshot(); snapshotSource != nil {
			return cs.createVolumeFromSnapshot(ctx, req, volumeID, snapshotSource.GetSnapshotId(), requiredBytes)
		}
		if contentSource.GetVolume() != nil {
			return nil, status.Error(codes.InvalidArgument, "volume cloning is not supported, use snapshot restore instead")
		}
	}

	if plan.Action == backend.VolumeActionNoop && plan.Actual != nil {
		klog.V(2).Infof("Volume %s already matches desired state (idempotent)", volumeID)
		cs.driver.rememberVolumeBackendSelection(volumeID, selection)
		return cs.createVolumeResponse(volumeID, plan.Actual.SizeBytes, params, selection, nvmeParams, migrationTimeout, plan.Actual.NVMETCPPort, plan.Actual.NVMETCPNQN, plan.Actual.Path), nil
	}

	if plan.Action == backend.VolumeActionResize && plan.Actual != nil {
		return nil, status.Errorf(codes.AlreadyExists,
			"volume %s already exists with different capacity (existing: %d, requested: %d)",
			volumeID, plan.Actual.SizeBytes, requiredBytes)
	}

	if plan.Action == backend.VolumeActionConflict {
		return nil, status.Errorf(codes.FailedPrecondition, "unsafe volume reconciliation for %s: %s", volumeID, plan.Reason)
	}

	startTime := time.Now()
	if _, err := storageBackend.ApplyVolumePlan(ctx, plan, backend.VolumeApplyOptions{}); err != nil {
		// Log volume create failure
		secLogger.LogVolumeCreate(volumeID, req.GetName(), security.OutcomeFailure, err, time.Since(startTime))
		return nil, createVolumeStatusError(err)
	}

	// Backend layer already logged "Created volume X" at V(2) - no duplicate needed.
	klog.V(4).Infof("CreateVolume CSI call completed for %s", volumeID)

	// Log volume create success
	secLogger.LogVolumeCreate(volumeID, req.GetName(), security.OutcomeSuccess, nil, time.Since(startTime))
	cs.driver.rememberVolumeBackendSelection(volumeID, selection)

	// Return volume information
	if plan.Desired != nil {
		requiredBytes = plan.Desired.SizeBytes
		nvmePort = plan.Desired.NVMETCPPort
		nqn = plan.Desired.NVMETCPNQN
		filePath = plan.Desired.Path
	}
	return cs.createVolumeResponse(volumeID, requiredBytes, params, selection, nvmeParams, migrationTimeout, nvmePort, nqn, filePath), nil
}

func (cs *ControllerServer) createVolumeResponse(volumeID string, capacityBytes int64, params map[string]string, selection BackendSelection, nvmeParams NVMEConnectionParams, migrationTimeout time.Duration, nvmePort int, nqn, filePath string) *csi.CreateVolumeResponse {
	volumeContext := map[string]string{
		"rdsAddress":              cs.getRDSAddress(params),
		"nvmeAddress":             cs.getNVMEAddress(params),
		"nvmePort":                fmt.Sprintf("%d", nvmePort),
		"nqn":                     nqn,
		"volumePath":              filePath,
		"ctrlLossTmo":             fmt.Sprintf("%d", nvmeParams.CtrlLossTmo),
		"reconnectDelay":          fmt.Sprintf("%d", nvmeParams.ReconnectDelay),
		"keepAliveTmo":            fmt.Sprintf("%d", nvmeParams.KeepAliveTmo),
		"migrationTimeoutSeconds": fmt.Sprintf("%.0f", migrationTimeout.Seconds()),
	}
	for key, value := range selection.volumeContext() {
		volumeContext[key] = value
	}

	return &csi.CreateVolumeResponse{
		Volume: &csi.Volume{
			VolumeId:      volumeID,
			CapacityBytes: capacityBytes,
			VolumeContext: volumeContext,
		},
	}
}

// createVolumeFromSnapshot handles CreateVolume with a snapshot source (restore workflow)
func (cs *ControllerServer) createVolumeFromSnapshot(
	ctx context.Context,
	req *csi.CreateVolumeRequest,
	volumeID string,
	snapshotID string,
	requiredBytes int64,
) (*csi.CreateVolumeResponse, error) {
	klog.V(4).Infof("Creating volume %s from snapshot %s", volumeID, snapshotID)

	// Validate snapshot ID
	if err := utils.ValidateSnapshotID(snapshotID); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid snapshot ID: %v", err)
	}

	// Get parameters
	params := req.GetParameters()
	storageBackend, selection, err := cs.driver.controllerBackendForParams(params)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid backend selection: %v", err)
	}
	if storageBackend == nil {
		return nil, status.Error(codes.Internal, "storage backend not initialized")
	}

	// Verify snapshot exists
	snapshotInfo, err := storageBackend.GetSnapshot(ctx, snapshotID)
	if err != nil {
		if backend.IsSnapshotNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "snapshot %s not found", snapshotID)
		}
		return nil, status.Errorf(codes.Internal, "failed to get snapshot: %v", err)
	}

	// CSI spec: volume size must not be less than snapshot size
	if requiredBytes < snapshotInfo.SizeBytes {
		requiredBytes = snapshotInfo.SizeBytes
	}

	volumeBasePath := defaultVolumeBasePath
	if path, ok := params[paramVolumePath]; ok {
		volumeBasePath = path
	}

	nvmePort := defaultNVMETCPPort
	if portStr, ok := params[paramNVMEPort]; ok {
		var port int
		if _, err := fmt.Sscanf(portStr, "%d", &port); err == nil {
			nvmePort = port
		}
	}

	// Parse NVMe connection parameters
	nvmeParams, err := ParseNVMEConnectionParams(params)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid NVMe connection parameters: %v", err)
	}
	migrationTimeout := ParseMigrationTimeout(params)

	// Generate NQN and file path for new volume
	nqn, err := utils.VolumeIDToNQN(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate NQN: %v", err)
	}
	filePath, err := utils.VolumeIDToFilePath(volumeID, volumeBasePath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to generate file path: %v", err)
	}

	// Restore: create new volume from snapshot via backend.
	restoreOpts := backend.CreateVolumeRequest{
		ID:          volumeID,
		Path:        filePath,
		SizeBytes:   requiredBytes,
		NVMETCPPort: nvmePort,
		NVMETCPNQN:  nqn,
		Parameters:  params,
	}

	if err := storageBackend.RestoreSnapshot(ctx, snapshotID, restoreOpts); err != nil {
		if stderrors.Is(err, utils.ErrConnectionFailed) || stderrors.Is(err, utils.ErrOperationTimeout) {
			return nil, status.Errorf(codes.Unavailable, "storage backend unavailable: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "failed to restore snapshot: %v", err)
	}

	klog.V(2).Infof("Restored volume %s from snapshot %s", volumeID, snapshotID)
	cs.driver.rememberVolumeBackendSelection(volumeID, selection)

	resp := cs.createVolumeResponse(volumeID, requiredBytes, params, selection, nvmeParams, migrationTimeout, nvmePort, nqn, filePath)
	resp.Volume.ContentSource = &csi.VolumeContentSource{
		Type: &csi.VolumeContentSource_Snapshot{
			Snapshot: &csi.VolumeContentSource_SnapshotSource{
				SnapshotId: snapshotID,
			},
		},
	}
	return resp, nil
}

// DeleteVolume removes a volume from the selected storage backend.
func (cs *ControllerServer) DeleteVolume(ctx context.Context, req *csi.DeleteVolumeRequest) (*csi.DeleteVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	klog.V(4).Infof("DeleteVolume CSI call for %s", volumeID)

	// Validate request
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	// Validate volume ID format
	if err := utils.ValidateVolumeID(volumeID); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volume ID: %v", err)
	}

	unlockVolume := cs.lockControllerVolume(volumeID)
	defer unlockVolume()

	storageBackend, _, err := cs.driver.controllerBackendForVolume(ctx, volumeID)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid backend selection: %v", err)
	}
	if storageBackend == nil {
		return nil, status.Error(codes.Internal, "storage backend not initialized")
	}

	plan, err := storageBackend.BuildDeleteVolumePlan(ctx, volumeID)
	if err != nil {
		return nil, deleteVolumeStatusError(err)
	}
	klog.V(3).Infof("DeleteVolume plan for %s: action=%s reason=%q", volumeID, plan.Action, plan.Reason)

	if plan.Action == backend.VolumeActionNoop {
		klog.V(4).Infof("Volume %s not found on backend %s, assuming already deleted", volumeID, storageBackend.Name())
		return &csi.DeleteVolumeResponse{}, nil
	}

	// Log volume details for audit trail
	klog.V(4).Infof("Deleting volume %s (path=%s, size=%d bytes, nvme_export=%v)",
		volumeID, plan.Actual.Path, plan.Actual.SizeBytes, plan.Actual.NVMETCPExport)

	// Log volume delete request
	secLogger := security.GetLogger()
	secLogger.LogVolumeDelete(volumeID, "", security.OutcomeUnknown, nil, 0)

	// Delete volume from backend (idempotent)
	startTime := time.Now()
	if _, err := storageBackend.ApplyVolumePlan(ctx, plan, backend.VolumeApplyOptions{}); err != nil {
		klog.Errorf("Failed to delete volume %s: %v", volumeID, err)

		// Log volume delete failure
		secLogger.LogVolumeDelete(volumeID, "", security.OutcomeFailure, err, time.Since(startTime))
		return nil, deleteVolumeStatusError(err)
	}

	// Backend layer already logged "Deleted volume X" at V(2) - no duplicate needed.
	klog.V(4).Infof("DeleteVolume CSI call completed for %s", volumeID)

	// Log volume delete success
	secLogger.LogVolumeDelete(volumeID, "", security.OutcomeSuccess, nil, time.Since(startTime))
	cs.driver.forgetVolumeBackendSelection(volumeID)

	return &csi.DeleteVolumeResponse{}, nil
}

func createVolumeStatusError(err error) error {
	if stderrors.Is(err, utils.ErrConnectionFailed) || stderrors.Is(err, utils.ErrOperationTimeout) {
		return status.Errorf(codes.Unavailable, "storage backend unavailable: %v", err)
	}
	if stderrors.Is(err, utils.ErrResourceExhausted) {
		return status.Errorf(codes.ResourceExhausted, "insufficient storage on backend: %v", err)
	}
	return status.Errorf(codes.Internal, "failed to create volume on backend: %v", err)
}

func deleteVolumeStatusError(err error) error {
	if stderrors.Is(err, utils.ErrConnectionFailed) || stderrors.Is(err, utils.ErrOperationTimeout) {
		return status.Errorf(codes.Unavailable, "storage backend unavailable: %v", err)
	}
	return status.Errorf(codes.Internal, "failed to verify volume existence: %v", err)
}

// ValidateVolumeCapabilities validates that the requested capabilities are supported
func (cs *ControllerServer) ValidateVolumeCapabilities(ctx context.Context, req *csi.ValidateVolumeCapabilitiesRequest) (*csi.ValidateVolumeCapabilitiesResponse, error) {
	volumeID := req.GetVolumeId()
	klog.V(4).Infof("ValidateVolumeCapabilities called for volume: %s", volumeID)

	// Validate request
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	if req.GetVolumeCapabilities() == nil || len(req.GetVolumeCapabilities()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume capabilities are required")
	}

	// Check if volume exists
	storageBackend, _, err := cs.driver.controllerBackendForVolume(ctx, volumeID)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid backend selection: %v", err)
	}
	if storageBackend == nil {
		return nil, status.Error(codes.Internal, "storage backend not initialized")
	}
	if _, err := storageBackend.GetVolume(ctx, volumeID); err != nil {
		return nil, status.Errorf(codes.NotFound, "volume %s not found: %v", volumeID, err)
	}

	// Validate capabilities
	if err := cs.validateVolumeCapabilities(req.GetVolumeCapabilities()); err != nil {
		return &csi.ValidateVolumeCapabilitiesResponse{
			Message: err.Error(),
		}, nil
	}

	return &csi.ValidateVolumeCapabilitiesResponse{
		Confirmed: &csi.ValidateVolumeCapabilitiesResponse_Confirmed{
			VolumeCapabilities: req.GetVolumeCapabilities(),
		},
	}, nil
}

// GetCapacity returns the available storage capacity on the selected backend.
func (cs *ControllerServer) GetCapacity(ctx context.Context, req *csi.GetCapacityRequest) (*csi.GetCapacityResponse, error) {
	klog.V(4).Info("GetCapacity called")

	// Get parameters
	params := req.GetParameters()
	storageBackend, _, err := cs.driver.controllerBackendForParams(params)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid backend selection: %v", err)
	}
	volumeBasePath := defaultVolumeBasePath
	if path, ok := params[paramVolumePath]; ok {
		volumeBasePath = path
	}

	if storageBackend == nil {
		return nil, status.Error(codes.Internal, "storage backend not initialized")
	}

	// Query capacity from backend
	capacity, err := storageBackend.GetCapacity(ctx, volumeBasePath)
	if err != nil {
		klog.Errorf("Failed to get capacity from backend %s: %v", storageBackend.Name(), err)
		return nil, status.Errorf(codes.Internal, "failed to query capacity: %v", err)
	}

	klog.V(4).Infof("Backend capacity: backend=%s total=%d free=%d used=%d", storageBackend.Name(), capacity.TotalBytes, capacity.FreeBytes, capacity.UsedBytes)

	return &csi.GetCapacityResponse{
		AvailableCapacity: capacity.FreeBytes,
	}, nil
}

// ControllerGetCapabilities returns the capabilities of the controller service
func (cs *ControllerServer) ControllerGetCapabilities(ctx context.Context, req *csi.ControllerGetCapabilitiesRequest) (*csi.ControllerGetCapabilitiesResponse, error) {
	klog.V(5).Info("ControllerGetCapabilities called")

	return &csi.ControllerGetCapabilitiesResponse{
		Capabilities: cs.driver.cscaps,
	}, nil
}

// validateBlockingNodeExists checks if a node still exists in Kubernetes.
// Returns (exists, error) - if node is deleted, exists=false, error=nil.
// Used for self-healing when blocking node is deleted without cleanup.
func (cs *ControllerServer) validateBlockingNodeExists(ctx context.Context, nodeID string) (bool, error) {
	if cs.driver.k8sClient == nil {
		// No k8s client = can't validate, assume node exists (fail-closed)
		return true, nil
	}
	_, err := cs.driver.k8sClient.CoreV1().Nodes().Get(ctx, nodeID, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return false, nil // Node deleted
		}
		return false, err // API error
	}
	return true, nil
}

// buildPublishContext creates the publish_context map with NVMe connection parameters.
// Uses snake_case keys to match existing volumeContext conventions.
func (cs *ControllerServer) buildPublishContext(volume *backend.Volume, params map[string]string) map[string]string {
	fsType := "ext4"
	if fs, ok := params[paramFSType]; ok && fs != "" {
		fsType = fs
	}

	return map[string]string{
		"nvme_address": cs.getNVMEAddress(params),
		"nvme_port":    fmt.Sprintf("%d", volume.NVMETCPPort),
		"nvme_nqn":     volume.NVMETCPNQN,
		"fs_type":      fsType,
	}
}

// postAttachmentConflictEvent posts a K8s event for an attachment conflict.
// Best effort - failures are logged but don't affect the main operation.
func (cs *ControllerServer) postAttachmentConflictEvent(ctx context.Context, req *csi.ControllerPublishVolumeRequest, attachedNode string) {
	// Extract PVC info from volume context if available
	volCtx := req.GetVolumeContext()
	pvcNamespace := volCtx["csi.storage.k8s.io/pvc/namespace"]
	pvcName := volCtx["csi.storage.k8s.io/pvc/name"]

	if pvcNamespace == "" || pvcName == "" {
		klog.V(4).Infof("Cannot post attachment conflict event: PVC info not in volume context")
		return
	}

	// Create temporary EventPoster if we have k8s client
	if cs.driver.k8sClient == nil {
		return
	}

	poster := NewEventPoster(cs.driver.k8sClient)
	if err := poster.PostAttachmentConflict(ctx, pvcNamespace, pvcName, req.GetVolumeId(), req.GetNodeId(), attachedNode); err != nil {
		klog.Warningf("Failed to post attachment conflict event: %v", err)
	}
}

// postVolumeAttachedEvent posts a K8s event when a volume is attached.
// Best effort - failures are logged but don't affect the main operation.
func (cs *ControllerServer) postVolumeAttachedEvent(ctx context.Context, req *csi.ControllerPublishVolumeRequest, duration time.Duration) {
	volCtx := req.GetVolumeContext()
	pvcNamespace := volCtx["csi.storage.k8s.io/pvc/namespace"]
	pvcName := volCtx["csi.storage.k8s.io/pvc/name"]

	if pvcNamespace == "" || pvcName == "" {
		klog.V(4).Infof("Cannot post volume attached event: PVC info not in volume context")
		return
	}

	if cs.driver.k8sClient == nil {
		return
	}

	poster := NewEventPoster(cs.driver.k8sClient)
	if err := poster.PostVolumeAttached(ctx, pvcNamespace, pvcName, req.GetVolumeId(), req.GetNodeId(), duration); err != nil {
		klog.Warningf("Failed to post volume attached event: %v", err)
	}
}

// postVolumeDetachedEvent posts a K8s event when a volume is detached.
// Best effort - failures are logged but don't affect the main operation.
func (cs *ControllerServer) postVolumeDetachedEvent(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) {
	if cs.driver.k8sClient == nil {
		return
	}

	// For unpublish, we don't have volume context with PVC info
	// We need to look up the PV to get the claimRef
	pv, err := cs.driver.k8sClient.CoreV1().PersistentVolumes().Get(ctx, req.GetVolumeId(), metav1.GetOptions{})
	if err != nil {
		klog.V(4).Infof("Cannot get PV %s for detached event: %v", req.GetVolumeId(), err)
		return
	}

	claimRef := pv.Spec.ClaimRef
	if claimRef == nil {
		klog.V(4).Infof("PV %s has no claimRef for detached event", req.GetVolumeId())
		return
	}

	poster := NewEventPoster(cs.driver.k8sClient)
	if err := poster.PostVolumeDetached(ctx, claimRef.Namespace, claimRef.Name, req.GetVolumeId(), req.GetNodeId()); err != nil {
		klog.Warningf("Failed to post volume detached event: %v", err)
	}
}

// ControllerPublishVolume tracks volume attachment to a node and enforces RWO semantics.
// Returns publish_context with NVMe connection parameters for NodeStageVolume.
func (cs *ControllerServer) ControllerPublishVolume(ctx context.Context, req *csi.ControllerPublishVolumeRequest) (*csi.ControllerPublishVolumeResponse, error) {
	startTime := time.Now()
	volumeID := req.GetVolumeId()
	nodeID := req.GetNodeId()

	klog.V(2).Infof("ControllerPublishVolume called for volume %s to node %s", volumeID, nodeID)

	// Validate request
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if nodeID == "" {
		return nil, status.Error(codes.InvalidArgument, "node ID is required")
	}

	// Validate volume ID format (security: prevent injection)
	if err := utils.ValidateVolumeID(volumeID); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volume ID: %v", err)
	}

	// Validate volume capability is provided (required by CSI spec)
	// Check this before node validation so InvalidArgument takes precedence
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability is required")
	}

	// Validate node exists if we have k8s client
	// For sanity tests without k8s, only accept the driver's own node ID
	if cs.driver.k8sClient != nil {
		// Check if node exists in Kubernetes
		_, err := cs.driver.k8sClient.CoreV1().Nodes().Get(ctx, nodeID, metav1.GetOptions{})
		if err != nil {
			return nil, status.Errorf(codes.NotFound, "node %s not found: %v", nodeID, err)
		}
	} else {
		// Without k8s client (test mode), only accept the driver's configured node ID
		// This allows sanity tests to validate node existence checking
		if cs.driver.nodeID != "" && nodeID != cs.driver.nodeID {
			return nil, status.Errorf(codes.NotFound, "node %s does not exist (driver node: %s)", nodeID, cs.driver.nodeID)
		}
	}

	// Determine access mode from VolumeCapability (singular, not slice)
	// ControllerPublishVolume uses GetVolumeCapability() which returns a single capability
	accessMode := "RWO"
	isRWX := false
	if cap := req.GetVolumeCapability(); cap != nil {
		if cap.GetAccessMode().GetMode() == csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER {
			accessMode = "RWX"
			isRWX = true
		}
	}

	// Acquire per-VMI lock if serialization is enabled
	// This prevents concurrent volume operations on the same VMI from racing
	if vmiGrouper := cs.driver.GetVMIGrouper(); vmiGrouper != nil {
		volCtx := req.GetVolumeContext()
		pvcNamespace := volCtx["csi.storage.k8s.io/pvc/namespace"]
		pvcName := volCtx["csi.storage.k8s.io/pvc/name"]
		if pvcNamespace != "" && pvcName != "" {
			vmiKey, unlock := vmiGrouper.LockVMI(ctx, pvcNamespace, pvcName)
			if vmiKey != "" {
				defer unlock()
				klog.V(4).Infof("VMI serialization active for volume %s (VMI: %s)", volumeID, vmiKey)
			}
		}
	}

	storageBackend, selection, err := cs.driver.controllerBackendForParams(req.GetVolumeContext())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid backend selection: %v", err)
	}
	if storageBackend == nil {
		return nil, status.Error(codes.Internal, "storage backend not initialized")
	}
	cs.driver.rememberVolumeBackendSelection(volumeID, selection)

	// Verify volume exists on backend
	volume, err := storageBackend.GetVolume(ctx, volumeID)
	if err != nil {
		if stderrors.Is(err, utils.ErrConnectionFailed) || stderrors.Is(err, utils.ErrOperationTimeout) {
			return nil, status.Errorf(codes.Unavailable, "storage backend unavailable: %v", err)
		}
		return nil, status.Errorf(codes.NotFound, "volume %s not found: %v", volumeID, err)
	}

	// Get attachment manager
	am := cs.driver.GetAttachmentManager()
	if am == nil {
		// No attachment manager = skip tracking (single-node scenario or disabled)
		klog.V(4).Infof("Attachment manager not available, skipping tracking for volume %s", volumeID)
		return &csi.ControllerPublishVolumeResponse{
			PublishContext: cs.buildPublishContext(volume, req.GetVolumeContext()),
		}, nil
	}

	existing, exists := am.GetAttachment(volumeID)
	var blockingNodeExists *bool
	withinGracePeriod := false
	if exists && existing != nil && !existing.IsAttachedToNode(nodeID) && !isRWX {
		gracePeriod := cs.driver.GetAttachmentGracePeriod()
		if gracePeriod > 0 && am.IsWithinGracePeriod(volumeID, gracePeriod) {
			withinGracePeriod = true
		} else {
			nodeExists, err := cs.validateBlockingNodeExists(ctx, existing.NodeID)
			if err != nil {
				klog.Errorf("Failed to verify node %s existence: %v", existing.NodeID, err)
				return nil, status.Errorf(codes.Internal, "failed to verify node %s: %v", existing.NodeID, err)
			}
			blockingNodeExists = &nodeExists
		}
	}

	plan, err := reconciler.BuildPublishAttachmentPlan(reconciler.PublishAttachmentInput{
		VolumeID:           volumeID,
		NodeID:             nodeID,
		AccessMode:         accessMode,
		AllowMultiNode:     isRWX,
		WithinGracePeriod:  withinGracePeriod,
		BlockingNodeExists: blockingNodeExists,
		Existing:           existing,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to plan attachment: %v", err)
	}
	cs.recordAttachmentPlan("publish", plan.Action)
	klog.V(3).Infof("ControllerPublishVolume plan for %s to %s: action=%s reason=%q", volumeID, nodeID, plan.Action, plan.Reason)

	switch plan.Action {
	case reconciler.AttachmentActionNoop:
		klog.V(2).Infof("Volume %s already attached to node %s (idempotent)", volumeID, nodeID)
		return &csi.ControllerPublishVolumeResponse{
			PublishContext: cs.buildPublishContext(volume, req.GetVolumeContext()),
		}, nil
	case reconciler.AttachmentActionAttachSecondary:
		return cs.applySecondaryAttachmentPlan(ctx, req, volume, plan, startTime)
	case reconciler.AttachmentActionClearGraceAttach:
		klog.V(2).Infof("Volume %s within grace period, allowing attachment handoff from %s to %s",
			volumeID, plan.BlockingNodeID, nodeID)
		if cs.driver.metrics != nil {
			cs.driver.metrics.RecordGracePeriodUsed()
		}
		if err := am.UntrackAttachment(ctx, volumeID); err != nil {
			klog.Warningf("Failed to clear old attachment for volume %s during grace period handoff: %v", volumeID, err)
		}
		am.ClearDetachTimestamp(volumeID)
	case reconciler.AttachmentActionClearStaleAttach:
		klog.Warningf("Volume %s attached to deleted node %s, clearing stale attachment", volumeID, plan.BlockingNodeID)
		if err := am.UntrackAttachment(ctx, volumeID); err != nil {
			klog.Warningf("Failed to clear stale attachment for volume %s: %v", volumeID, err)
		}
		if cs.driver.metrics != nil {
			cs.driver.metrics.RecordStaleAttachmentCleared()
			cs.driver.metrics.RecordReconcileAction("clear_stale")
		}
	case reconciler.AttachmentActionConflict:
		return nil, cs.attachmentConflictStatus(ctx, req, plan)
	case reconciler.AttachmentActionAttach:
		// Fall through to primary attachment tracking below.
	default:
		return nil, status.Errorf(codes.Internal, "unknown attachment plan action %q", plan.Action)
	}

	// No existing attachment - track new primary attachment with access mode
	if err := am.TrackAttachmentWithMode(ctx, volumeID, nodeID, accessMode); err != nil {
		// Check if this is a conflict (race condition - another request won)
		if existing, exists := am.GetAttachment(volumeID); exists && !am.IsAttachedToNode(volumeID, nodeID) {
			if cs.driver.metrics != nil {
				cs.driver.metrics.RecordAttachmentConflict()
			}
			return nil, status.Errorf(codes.FailedPrecondition,
				"volume %s already attached to node %s, cannot attach to %s",
				volumeID, existing.NodeID, nodeID)
		}
		return nil, status.Errorf(codes.Internal, "failed to track attachment: %v", err)
	}

	// Record attachment success metric
	duration := time.Since(startTime)
	if cs.driver.metrics != nil {
		cs.driver.metrics.RecordAttachmentOp("attach", nil, duration)
	}

	// Post attachment event (best effort)
	cs.postVolumeAttachedEvent(ctx, req, duration)

	klog.V(2).Infof("Successfully published volume %s to node %s", volumeID, nodeID)

	return &csi.ControllerPublishVolumeResponse{
		PublishContext: cs.buildPublishContext(volume, req.GetVolumeContext()),
	}, nil
}

func (cs *ControllerServer) applySecondaryAttachmentPlan(ctx context.Context, req *csi.ControllerPublishVolumeRequest, volume *backend.Volume, plan *reconciler.AttachmentPlan, startTime time.Time) (*csi.ControllerPublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	nodeID := req.GetNodeId()
	am := cs.driver.GetAttachmentManager()
	if am == nil || plan == nil || plan.Existing == nil {
		return nil, status.Error(codes.Internal, "attachment manager or plan state missing")
	}
	if len(plan.Existing.Nodes) == 0 {
		return nil, status.Errorf(codes.Internal,
			"Volume %s has empty node list (corrupted state). Detach and reattach volume to recover.",
			volumeID)
	}

	volCtx := req.GetVolumeContext()
	migrationTimeoutParams := map[string]string{
		"migrationTimeoutSeconds": volCtx["migrationTimeoutSeconds"],
	}
	migrationTimeout := ParseMigrationTimeout(migrationTimeoutParams)

	klog.V(2).Infof("Allowing second attachment of RWX volume %s to node %s (migration target, timeout=%v)", volumeID, nodeID, migrationTimeout)
	sourceNode := plan.Existing.Nodes[0].NodeID

	if err := am.AddSecondaryAttachment(ctx, volumeID, nodeID, migrationTimeout); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to track secondary attachment: %v", err)
	}

	if cs.driver.metrics != nil {
		cs.driver.metrics.RecordAttachmentOp("attach_secondary", nil, time.Since(startTime))
	}

	if cs.driver.k8sClient != nil {
		pvcNamespace := volCtx["csi.storage.k8s.io/pvc/namespace"]
		pvcName := volCtx["csi.storage.k8s.io/pvc/name"]
		if pvcNamespace != "" && pvcName != "" {
			eventPoster := NewEventPoster(cs.driver.k8sClient)
			if err := eventPoster.PostMigrationStarted(ctx, pvcNamespace, pvcName, volumeID, sourceNode, nodeID, migrationTimeout); err != nil {
				klog.Warningf("Failed to post migration started event: %v", err)
			}
		}
	}

	return &csi.ControllerPublishVolumeResponse{
		PublishContext: cs.buildPublishContext(volume, req.GetVolumeContext()),
	}, nil
}

func (cs *ControllerServer) attachmentConflictStatus(ctx context.Context, req *csi.ControllerPublishVolumeRequest, plan *reconciler.AttachmentPlan) error {
	volumeID := req.GetVolumeId()
	nodeID := req.GetNodeId()

	if cs.driver.metrics != nil {
		cs.driver.metrics.RecordAttachmentConflict()
	}

	if plan != nil && plan.Existing != nil && plan.Existing.AccessMode == "RWX" {
		if plan.Existing.NodeCount() == 0 {
			return status.Errorf(codes.Internal,
				"Volume %s has empty node list (corrupted state). Detach and reattach volume to recover.",
				volumeID)
		}
		if plan.Existing.IsMigrationTimedOut() {
			elapsed := time.Since(*plan.Existing.MigrationStartedAt)
			klog.Warningf("RWX volume %s migration timed out (%v elapsed, %v max), rejecting new secondary attachment",
				volumeID, elapsed, plan.Existing.MigrationTimeout)
			if cs.driver.metrics != nil {
				cs.driver.metrics.RecordMigrationResult("timeout", elapsed)
			}
			cs.postMigrationFailedEvent(ctx, req, plan.Existing, elapsed)
			return status.Errorf(codes.FailedPrecondition,
				"Volume %s migration timeout exceeded (%v elapsed, %v max). "+
					"Previous migration may be stuck. Detach source node to reset, or adjust migrationTimeoutSeconds in StorageClass.",
				volumeID, elapsed.Round(time.Second), plan.Existing.MigrationTimeout)
		}
		if plan.Existing.NodeCount() >= 2 {
			klog.Warningf("RWX volume %s already attached to 2 nodes, rejecting 3rd attachment to %s",
				volumeID, nodeID)
			return status.Errorf(codes.FailedPrecondition,
				"Volume %s already attached to 2 nodes (migration limit). Wait for migration to complete. Attached nodes: %v",
				volumeID, plan.Existing.GetNodeIDs())
		}
	}

	blockingNode := ""
	if plan != nil {
		blockingNode = plan.BlockingNodeID
	}
	klog.Warningf("RWO volume %s already attached to node %s, rejecting attachment to node %s",
		volumeID, blockingNode, nodeID)
	cs.postAttachmentConflictEvent(ctx, req, blockingNode)
	return status.Errorf(codes.FailedPrecondition,
		"Volume %s already attached to node %s. For multi-node access, use RWX with block volumes.",
		volumeID, blockingNode)
}

func (cs *ControllerServer) postMigrationFailedEvent(ctx context.Context, req *csi.ControllerPublishVolumeRequest, existing *attachment.AttachmentState, elapsed time.Duration) {
	if cs.driver.k8sClient == nil || existing == nil || len(existing.Nodes) < 2 {
		return
	}
	volCtx := req.GetVolumeContext()
	pvcNamespace := volCtx["csi.storage.k8s.io/pvc/namespace"]
	pvcName := volCtx["csi.storage.k8s.io/pvc/name"]
	if pvcNamespace == "" || pvcName == "" {
		return
	}
	sourceNode := existing.Nodes[0].NodeID
	targetNode := existing.Nodes[1].NodeID
	eventPoster := NewEventPoster(cs.driver.k8sClient)
	if err := eventPoster.PostMigrationFailed(ctx, pvcNamespace, pvcName, req.GetVolumeId(), sourceNode, targetNode, "timeout", elapsed); err != nil {
		klog.Warningf("Failed to post migration failed event: %v", err)
	}
}

func (cs *ControllerServer) recordAttachmentPlan(operation string, action reconciler.AttachmentAction) {
	if cs.driver.metrics == nil {
		return
	}
	cs.driver.metrics.RecordReconcileAction(operation + "_" + string(action))
}

// ControllerUnpublishVolume removes volume attachment tracking for a node.
// This method is idempotent - returns success even if volume not currently attached.
func (cs *ControllerServer) ControllerUnpublishVolume(ctx context.Context, req *csi.ControllerUnpublishVolumeRequest) (*csi.ControllerUnpublishVolumeResponse, error) {
	startTime := time.Now()
	volumeID := req.GetVolumeId()
	nodeID := req.GetNodeId()

	klog.V(2).Infof("ControllerUnpublishVolume called for volume %s from node %s", volumeID, nodeID)

	// Validate request
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	// Note: nodeID can be empty for force-detach scenarios per CSI spec

	// Validate volume ID format (security: prevent injection)
	if err := utils.ValidateVolumeID(volumeID); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volume ID: %v", err)
	}

	// Get attachment manager
	am := cs.driver.GetAttachmentManager()
	if am == nil {
		// No attachment manager = nothing to untrack
		klog.V(4).Infof("Attachment manager not available, skipping untrack for volume %s", volumeID)
		return &csi.ControllerUnpublishVolumeResponse{}, nil
	}

	existing, _ := am.GetAttachment(volumeID)
	plan, err := reconciler.BuildUnpublishAttachmentPlan(reconciler.UnpublishAttachmentInput{
		VolumeID: volumeID,
		NodeID:   nodeID,
		Existing: existing,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to plan detachment: %v", err)
	}
	cs.recordAttachmentPlan("unpublish", plan.Action)
	klog.V(3).Infof("ControllerUnpublishVolume plan for %s from %s: action=%s reason=%q", volumeID, nodeID, plan.Action, plan.Reason)

	if plan.Action == reconciler.AttachmentActionNoop {
		klog.V(2).Infof("Volume %s attachment already absent for node %s (idempotent)", volumeID, nodeID)
		return &csi.ControllerUnpublishVolumeResponse{}, nil
	}

	// Before removing attachment, capture migration state for event posting
	var wasMigrating bool
	var sourceNode, targetNode string
	var migrationStartedAt time.Time
	if existing != nil && existing.IsMigrating() {
		wasMigrating = true
		// Identify which node is being removed (source) and which remains (target)
		if len(existing.Nodes) == 2 {
			// Node being removed is the source
			if existing.Nodes[0].NodeID == nodeID {
				sourceNode = existing.Nodes[0].NodeID
				targetNode = existing.Nodes[1].NodeID
			} else if existing.Nodes[1].NodeID == nodeID {
				sourceNode = existing.Nodes[1].NodeID
				targetNode = existing.Nodes[0].NodeID
			}
			// Copy migration start time before it's cleared by RemoveNodeAttachment
			if existing.MigrationStartedAt != nil {
				migrationStartedAt = *existing.MigrationStartedAt
			}
		}
	}

	var fullyDetached bool
	switch plan.Action {
	case reconciler.AttachmentActionDetachAll:
		if err := am.UntrackAttachment(ctx, volumeID); err != nil {
			klog.Warningf("Error force-detaching volume %s: %v (returning success)", volumeID, err)
		}
		fullyDetached = true
	case reconciler.AttachmentActionDetach, reconciler.AttachmentActionDetachNode:
		var err error
		fullyDetached, err = am.RemoveNodeAttachment(ctx, volumeID, nodeID)
		if err != nil {
			klog.Warningf("Error removing node attachment for volume %s: %v (returning success)", volumeID, err)
		}
	default:
		return nil, status.Errorf(codes.Internal, "unknown detachment plan action %q", plan.Action)
	}

	if fullyDetached {
		// Record detachment metric
		if cs.driver.metrics != nil {
			cs.driver.metrics.RecordAttachmentOp("detach", nil, time.Since(startTime))
		}
		cs.postVolumeDetachedEvent(ctx, req)
	} else {
		// Partial detach (RWX migration - one node still attached)
		klog.V(2).Infof("Volume %s partially detached from node %s, other node(s) still attached", volumeID, nodeID)
		if cs.driver.metrics != nil {
			cs.driver.metrics.RecordAttachmentOp("detach_partial", nil, time.Since(startTime))
		}

		// Check if this was a migration completion (partial detach = source node removed, target remains)
		if wasMigrating && cs.driver.k8sClient != nil && sourceNode != "" && targetNode != "" {
			duration := time.Since(migrationStartedAt)

			// Look up PV to get PVC reference
			pv, err := cs.driver.k8sClient.CoreV1().PersistentVolumes().Get(ctx, volumeID, metav1.GetOptions{})
			if err == nil && pv.Spec.ClaimRef != nil {
				pvcNamespace := pv.Spec.ClaimRef.Namespace
				pvcName := pv.Spec.ClaimRef.Name
				if pvcNamespace != "" && pvcName != "" {
					eventPoster := NewEventPoster(cs.driver.k8sClient)
					if err := eventPoster.PostMigrationCompleted(ctx, pvcNamespace, pvcName, volumeID, sourceNode, targetNode, duration); err != nil {
						klog.Warningf("Failed to post migration completed event: %v", err)
					}
				}
			} else {
				klog.V(4).Infof("Could not get PVC for migration completed event: %v", err)
			}
		}
	}

	klog.V(2).Infof("Successfully unpublished volume %s from node %s", volumeID, nodeID)
	return &csi.ControllerUnpublishVolumeResponse{}, nil
}

// CreateSnapshot creates a file-based CoW snapshot of a volume via /disk add copy-from.
func (cs *ControllerServer) CreateSnapshot(ctx context.Context, req *csi.CreateSnapshotRequest) (*csi.CreateSnapshotResponse, error) {
	klog.V(4).Infof("CreateSnapshot CSI call for name=%s source=%s", req.GetName(), req.GetSourceVolumeId())

	// 1. Validate request
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot name is required")
	}
	if req.GetSourceVolumeId() == "" {
		return nil, status.Error(codes.InvalidArgument, "source volume ID is required")
	}

	sourceVolumeID := req.GetSourceVolumeId()

	// Validate source volume ID format
	if err := utils.ValidateVolumeID(sourceVolumeID); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid source volume ID: %v", err)
	}

	params := req.GetParameters()
	storageBackend, selection, err := cs.driver.controllerBackendForParams(params)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid backend selection: %v", err)
	}
	if storageBackend == nil {
		return nil, status.Error(codes.Internal, "storage backend not initialized")
	}

	// 2. Generate deterministic snapshot ID from CSI name + source volume for idempotency.
	// Format: snap-<source-uuid>-at-<10-char-hash-of-csi-name>
	// The same (name, sourceVolumeID) pair always produces the same snapshot ID,
	// satisfying CSI idempotency requirements (external-snapshotter won't re-call
	// CreateSnapshot, but CSI sanity tests and retries need this determinism).
	snapshotID := utils.GenerateSnapshotID(req.GetName(), sourceVolumeID)

	// 3. Check idempotency: does a snapshot with this ID already exist?
	// Since the ID is deterministic, a retry with the same (name, source) returns the same
	// snapshot rather than creating a duplicate.
	existingSnapshot, err := storageBackend.GetSnapshot(ctx, snapshotID)
	if err == nil {
		// Snapshot exists -- check if same source volume (idempotent) or different (conflict)
		if existingSnapshot.SourceVolumeID == sourceVolumeID {
			klog.V(2).Infof("Snapshot %s already exists for source %s (idempotent)", snapshotID, sourceVolumeID)
			cs.driver.rememberSnapshotBackendSelection(snapshotID, selection)
			return &csi.CreateSnapshotResponse{
				Snapshot: &csi.Snapshot{
					SnapshotId:     snapshotID,
					SourceVolumeId: existingSnapshot.SourceVolumeID,
					CreationTime:   timestamppb.New(existingSnapshot.CreatedAt),
					SizeBytes:      existingSnapshot.SizeBytes,
					ReadyToUse:     true,
				},
			}, nil
		}
		return nil, status.Errorf(codes.AlreadyExists,
			"snapshot %s already exists with different source volume (existing: %s, requested: %s)",
			snapshotID, existingSnapshot.SourceVolumeID, sourceVolumeID)
	}

	// 4. Verify source volume exists on backend
	sourceVolume, err := storageBackend.GetVolume(ctx, sourceVolumeID)
	if err != nil {
		if backend.IsVolumeNotFound(err) {
			return nil, status.Errorf(codes.NotFound, "source volume %s not found", sourceVolumeID)
		}
		return nil, status.Errorf(codes.Internal, "failed to get source volume: %v", err)
	}

	// 5. Determine base path for snapshot file storage
	// Snapshots are stored in the same base directory as volumes (paramVolumePath).
	volumeBasePath := defaultVolumeBasePath
	if path, ok := params[paramVolumePath]; ok && path != "" {
		volumeBasePath = path
	}

	// 6. Create snapshot via backend
	createOpts := backend.CreateSnapshotRequest{
		ID:             snapshotID,
		SourceVolumeID: sourceVolumeID,
		BasePath:       volumeBasePath,
		Parameters:     params,
	}

	snapshotInfo, err := storageBackend.CreateSnapshot(ctx, createOpts)
	if err != nil {
		// Map connection errors to Unavailable
		if stderrors.Is(err, utils.ErrConnectionFailed) || stderrors.Is(err, utils.ErrOperationTimeout) {
			return nil, status.Errorf(codes.Unavailable, "storage backend unavailable: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "failed to create snapshot: %v", err)
	}

	klog.V(2).Infof("Created snapshot %s from volume %s", snapshotID, sourceVolumeID)
	cs.driver.rememberSnapshotBackendSelection(snapshotID, selection)

	// 7. Return response — /disk add copy-from is atomic (CoW), so ready_to_use is always true.
	return &csi.CreateSnapshotResponse{
		Snapshot: &csi.Snapshot{
			SnapshotId:     snapshotID,
			SourceVolumeId: sourceVolumeID,
			CreationTime:   timestamppb.New(snapshotInfo.CreatedAt),
			SizeBytes:      sourceVolume.SizeBytes,
			ReadyToUse:     true,
		},
	}, nil
}

// DeleteSnapshot removes a file-based CoW snapshot (disk entry + backing file)
func (cs *ControllerServer) DeleteSnapshot(ctx context.Context, req *csi.DeleteSnapshotRequest) (*csi.DeleteSnapshotResponse, error) {
	snapshotID := req.GetSnapshotId()
	klog.V(4).Infof("DeleteSnapshot CSI call for %s", snapshotID)

	// 1. Validate request
	if snapshotID == "" {
		return nil, status.Error(codes.InvalidArgument, "snapshot ID is required")
	}

	// Validate snapshot ID format
	if err := utils.ValidateSnapshotID(snapshotID); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid snapshot ID: %v", err)
	}

	resolved, found, err := cs.driver.controllerBackendForSnapshot(ctx, snapshotID)
	if err != nil {
		return nil, mapBackendError("resolve snapshot backend", err)
	}
	if !found {
		klog.V(4).Infof("Snapshot %s not found on any known backend (idempotent delete)", snapshotID)
		return &csi.DeleteSnapshotResponse{}, nil
	}

	// 2. Delete snapshot via backend (idempotent)
	if err := resolved.backend.DeleteSnapshot(ctx, snapshotID); err != nil {
		// Map connection errors
		if stderrors.Is(err, utils.ErrConnectionFailed) || stderrors.Is(err, utils.ErrOperationTimeout) {
			return nil, status.Errorf(codes.Unavailable, "storage backend unavailable: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "failed to delete snapshot: %v", err)
	}

	cs.driver.forgetSnapshotBackendSelection(snapshotID)
	klog.V(2).Infof("Deleted snapshot %s", snapshotID)
	return &csi.DeleteSnapshotResponse{}, nil
}

// ListSnapshots lists snapshots with CSI-compliant pagination
func (cs *ControllerServer) ListSnapshots(ctx context.Context, req *csi.ListSnapshotsRequest) (*csi.ListSnapshotsResponse, error) {
	klog.V(4).Infof("ListSnapshots CSI call (snapshot_id=%s, source_volume_id=%s, max_entries=%d, starting_token=%s)",
		req.GetSnapshotId(), req.GetSourceVolumeId(), req.GetMaxEntries(), req.GetStartingToken())

	// Handle single snapshot lookup by ID
	if req.GetSnapshotId() != "" {
		snapshotID := req.GetSnapshotId()
		if err := utils.ValidateSnapshotID(snapshotID); err != nil {
			// Invalid ID format -- return empty (not error) per CSI spec
			return &csi.ListSnapshotsResponse{}, nil
		}

		resolved, found, err := cs.driver.controllerBackendForSnapshot(ctx, snapshotID)
		if err != nil {
			return nil, mapBackendError("resolve snapshot backend", err)
		}
		if !found {
			return &csi.ListSnapshotsResponse{}, nil
		}
		snap := resolved.snapshot

		return &csi.ListSnapshotsResponse{
			Entries: []*csi.ListSnapshotsResponse_Entry{
				{
					Snapshot: &csi.Snapshot{
						SnapshotId:     snap.ID,
						SourceVolumeId: snap.SourceVolumeID,
						CreationTime:   timestamppb.New(snap.CreatedAt),
						SizeBytes:      snap.SizeBytes,
						ReadyToUse:     true,
					},
				},
			},
		}, nil
	}

	var allSnapshots []backend.Snapshot
	if req.GetSourceVolumeId() != "" {
		storageBackend, _, err := cs.driver.controllerBackendForVolume(ctx, req.GetSourceVolumeId())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid backend selection: %v", err)
		}
		if storageBackend == nil {
			return nil, status.Error(codes.Internal, "storage backend not initialized")
		}
		snapshots, err := storageBackend.ListSnapshots(ctx)
		if err != nil {
			if stderrors.Is(err, utils.ErrConnectionFailed) || stderrors.Is(err, utils.ErrOperationTimeout) {
				return nil, status.Errorf(codes.Unavailable, "storage backend unavailable: %v", err)
			}
			return nil, status.Errorf(codes.Internal, "failed to list snapshots: %v", err)
		}
		allSnapshots = snapshots
	} else {
		knownBackends, err := cs.driver.controllerBackendsForKnownSelections(ctx)
		if err != nil {
			return nil, mapBackendError("resolve known backends", err)
		}
		for _, resolved := range knownBackends {
			snapshots, err := resolved.backend.ListSnapshots(ctx)
			if err != nil {
				if stderrors.Is(err, utils.ErrConnectionFailed) || stderrors.Is(err, utils.ErrOperationTimeout) {
					return nil, status.Errorf(codes.Unavailable, "storage backend unavailable: %v", err)
				}
				return nil, status.Errorf(codes.Internal, "failed to list snapshots from backend %s: %v", resolved.backend.Name(), err)
			}
			for _, snapshot := range snapshots {
				cs.driver.rememberSnapshotBackendSelection(snapshot.ID, resolved.selection)
				allSnapshots = append(allSnapshots, snapshot)
			}
		}
	}

	// SourceVolume is populated by parseSnapshotInfo from the source-volume= field in
	// backend output. Snapshots without a source-volume field are excluded from
	// source-based filtering (they cannot be matched to a source volume).
	if req.GetSourceVolumeId() != "" {
		filtered := make([]backend.Snapshot, 0, len(allSnapshots))
		for _, snapshot := range allSnapshots {
			if snapshot.SourceVolumeID == req.GetSourceVolumeId() {
				filtered = append(filtered, snapshot)
			}
		}
		allSnapshots = filtered
	}

	// Sort by name for deterministic pagination
	sort.Slice(allSnapshots, func(i, j int) bool {
		return allSnapshots[i].ID < allSnapshots[j].ID
	})

	// Handle pagination
	startIndex := 0
	if req.GetStartingToken() != "" {
		idx, err := strconv.Atoi(req.GetStartingToken())
		if err != nil {
			return nil, status.Errorf(codes.Aborted, "invalid starting_token: %s", req.GetStartingToken())
		}
		if idx < 0 || idx > len(allSnapshots) {
			return nil, status.Errorf(codes.Aborted, "starting_token out of range: %d (total: %d)", idx, len(allSnapshots))
		}
		startIndex = idx
	}

	// Build response entries
	maxEntries := len(allSnapshots) - startIndex
	if req.GetMaxEntries() > 0 && int(req.GetMaxEntries()) < maxEntries {
		maxEntries = int(req.GetMaxEntries())
	}

	entries := make([]*csi.ListSnapshotsResponse_Entry, 0, maxEntries)
	for i := startIndex; i < startIndex+maxEntries && i < len(allSnapshots); i++ {
		s := allSnapshots[i]
		entries = append(entries, &csi.ListSnapshotsResponse_Entry{
			Snapshot: &csi.Snapshot{
				SnapshotId:     s.ID,
				SourceVolumeId: s.SourceVolumeID,
				CreationTime:   timestamppb.New(s.CreatedAt),
				SizeBytes:      s.SizeBytes,
				ReadyToUse:     true,
			},
		})
	}

	// Set next token if more entries exist
	var nextToken string
	if startIndex+maxEntries < len(allSnapshots) {
		nextToken = strconv.Itoa(startIndex + maxEntries)
	}

	return &csi.ListSnapshotsResponse{
		Entries:   entries,
		NextToken: nextToken,
	}, nil
}

// ControllerExpandVolume expands a volume on the backend storage
func (cs *ControllerServer) ControllerExpandVolume(ctx context.Context, req *csi.ControllerExpandVolumeRequest) (*csi.ControllerExpandVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	klog.V(4).Infof("ControllerExpandVolume CSI call for %s", volumeID)

	// Validate request
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}

	// Validate volume ID format
	if err := utils.ValidateVolumeID(volumeID); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid volume ID: %v", err)
	}

	// Get required capacity
	requiredBytes := req.GetCapacityRange().GetRequiredBytes()
	if requiredBytes == 0 {
		return nil, status.Error(codes.InvalidArgument, "required capacity is required")
	}

	// Enforce size limits
	if requiredBytes < minVolumeSizeBytes {
		return nil, status.Errorf(codes.OutOfRange, "required bytes %d is less than minimum %d", requiredBytes, minVolumeSizeBytes)
	}

	limitBytes := req.GetCapacityRange().GetLimitBytes()
	if limitBytes > 0 && requiredBytes > limitBytes {
		return nil, status.Errorf(codes.OutOfRange, "required bytes %d exceeds limit bytes %d", requiredBytes, limitBytes)
	}

	if requiredBytes > maxVolumeSizeBytes {
		return nil, status.Errorf(codes.OutOfRange, "required bytes %d exceeds maximum %d", requiredBytes, maxVolumeSizeBytes)
	}

	unlockVolume := cs.lockControllerVolume(volumeID)
	defer unlockVolume()

	storageBackend, _, err := cs.driver.controllerBackendForVolume(ctx, volumeID)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid backend selection: %v", err)
	}
	if storageBackend == nil {
		return nil, status.Error(codes.Internal, "storage backend not initialized")
	}

	// Check if volume exists
	existingVolume, err := storageBackend.GetVolume(ctx, volumeID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "volume %s not found: %v", volumeID, err)
	}

	// Check if expansion is needed
	if existingVolume.SizeBytes >= requiredBytes {
		klog.V(4).Infof("Volume %s already at or above requested size (%d >= %d), no expansion needed",
			volumeID, existingVolume.SizeBytes, requiredBytes)
		return &csi.ControllerExpandVolumeResponse{
			CapacityBytes:         existingVolume.SizeBytes,
			NodeExpansionRequired: false,
		}, nil
	}

	// Resize volume on backend
	klog.V(4).Infof("Expanding volume %s from %d to %d bytes", volumeID, existingVolume.SizeBytes, requiredBytes)

	if err := storageBackend.ResizeVolume(ctx, volumeID, requiredBytes); err != nil {
		// Check if this is a capacity error
		if stderrors.Is(err, utils.ErrResourceExhausted) {
			return nil, status.Errorf(codes.ResourceExhausted, "insufficient storage on backend for expansion: %v", err)
		}
		return nil, status.Errorf(codes.Internal, "failed to resize volume on backend: %v", err)
	}

	// Backend layer already logged "Resized volume X" at V(2) - no duplicate needed.
	klog.V(4).Infof("ControllerExpandVolume CSI call completed for %s", volumeID)

	// Determine if node expansion is required
	// For mount volumes: yes, to resize the filesystem (ext4, xfs, etc.)
	// For block volumes: no, kernel sees new size automatically via NVMe rescan
	nodeExpansionRequired := true
	if cap := req.GetVolumeCapability(); cap != nil {
		if cap.GetBlock() != nil {
			nodeExpansionRequired = false
			klog.V(4).Infof("Block volume expansion for %s - node expansion not required", volumeID)
		}
	}

	// Return response indicating node expansion is required to resize the filesystem
	return &csi.ControllerExpandVolumeResponse{
		CapacityBytes:         requiredBytes,
		NodeExpansionRequired: nodeExpansionRequired,
	}, nil
}

// ListVolumes lists all volumes on the selected storage backend.
func (cs *ControllerServer) ListVolumes(ctx context.Context, req *csi.ListVolumesRequest) (*csi.ListVolumesResponse, error) {
	klog.V(4).Info("ListVolumes called")

	knownBackends, err := cs.driver.controllerBackendsForKnownSelections(ctx)
	if err != nil {
		return nil, mapBackendError("resolve known backends", err)
	}

	var volumes []backend.Volume
	for _, resolved := range knownBackends {
		backendVolumes, err := resolved.backend.ListVolumes(ctx)
		if err != nil {
			klog.Errorf("Failed to list volumes from backend %s: %v", resolved.backend.Name(), err)
			return nil, status.Errorf(codes.Internal, "failed to list volumes from backend %s: %v", resolved.backend.Name(), err)
		}
		volumes = append(volumes, backendVolumes...)
	}
	sort.Slice(volumes, func(i, j int) bool {
		return volumes[i].ID < volumes[j].ID
	})

	// Convert to CSI format
	var entries []*csi.ListVolumesResponse_Entry
	for _, vol := range volumes {
		entries = append(entries, &csi.ListVolumesResponse_Entry{
			Volume: &csi.Volume{
				VolumeId:      vol.ID,
				CapacityBytes: vol.SizeBytes,
			},
		})
	}

	return &csi.ListVolumesResponse{
		Entries: entries,
	}, nil
}

// ControllerGetVolume is not yet implemented
func (cs *ControllerServer) ControllerGetVolume(ctx context.Context, req *csi.ControllerGetVolumeRequest) (*csi.ControllerGetVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerGetVolume is not yet implemented")
}

// ControllerModifyVolume is not yet implemented
func (cs *ControllerServer) ControllerModifyVolume(ctx context.Context, req *csi.ControllerModifyVolumeRequest) (*csi.ControllerModifyVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ControllerModifyVolume is not yet implemented")
}

// Helper functions

func mapBackendError(operation string, err error) error {
	if err == nil {
		return nil
	}
	if stderrors.Is(err, utils.ErrConnectionFailed) || stderrors.Is(err, utils.ErrOperationTimeout) {
		return status.Errorf(codes.Unavailable, "%s: storage backend unavailable: %v", operation, err)
	}
	return status.Errorf(codes.Internal, "%s: %v", operation, err)
}

// validateVolumeCapabilities checks if the requested capabilities are supported
func (cs *ControllerServer) validateVolumeCapabilities(caps []*csi.VolumeCapability) error {
	for _, cap := range caps {
		// Check access mode
		accessMode := cap.GetAccessMode().GetMode()
		supported := false
		for _, supportedMode := range cs.driver.vcaps {
			if accessMode == supportedMode.GetMode() {
				supported = true
				break
			}
		}

		if !supported {
			return fmt.Errorf("access mode %v is not supported", accessMode)
		}

		// Check access type (must be block or mount)
		if cap.GetBlock() == nil && cap.GetMount() == nil {
			return fmt.Errorf("volume capability must specify either block or mount")
		}

		// RWX block-only validation (ROADMAP-4)
		// RWX with filesystem volumes risks data corruption - reject with actionable error
		if accessMode == csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER {
			if cap.GetMount() != nil {
				return fmt.Errorf("RWX access mode requires volumeMode: Block. " +
					"Filesystem volumes risk data corruption with multi-node access. " +
					"For KubeVirt VM live migration, use volumeMode: Block in your PVC")
			}
			// Log valid RWX block usage for debugging/auditing
			klog.V(2).Info("RWX block volume capability validated (KubeVirt live migration use case)")
		}
	}

	return nil
}

// getRDSAddress extracts RDS address from parameters
func (cs *ControllerServer) getRDSAddress(params map[string]string) string {
	if addr, ok := params[paramRDSAddress]; ok {
		return addr
	}
	if addr, ok := params[paramTargetAddr]; ok {
		return addr
	}
	if addr, ok := params[paramNVMEAddress]; ok {
		return addr
	}
	// Fall back to driver's RDS client address
	if storageBackend := cs.driver.controllerBackend(); storageBackend != nil {
		return storageBackend.Address()
	}
	return ""
}

// getNVMEAddress gets the NVMe/TCP target address from params or falls back to the backend address.
func (cs *ControllerServer) getNVMEAddress(params map[string]string) string {
	// Prefer nvmeAddress if specified (for separate storage network)
	if addr, ok := params[paramNVMEAddress]; ok {
		return addr
	}
	if addr, ok := params[paramTargetAddr]; ok {
		return addr
	}
	// Fall back to backend address if nvmeAddress not specified.
	return cs.getRDSAddress(params)
}
