package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/circuitbreaker"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/mount"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/nvme"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/security"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
)

const (
	// Default filesystem type if not specified
	defaultFSType = "ext4"

	// Device connection timeout (reserved for future use)
	// deviceTimeout = 30 * time.Second

	// VolumeContext keys
	volumeContextNQN         = "nqn"
	volumeContextAddress     = "rdsAddress"
	volumeContextNVMEAddress = "nvmeAddress"
	volumeContextPort        = "nvmePort"
	volumeContextFSType      = "fsType"
)

// NodeServer implements the CSI Node service
type NodeServer struct {
	csi.UnimplementedNodeServer
	driver         *Driver
	nvmeConn       nvme.Connector
	mounter        mount.Mounter
	nodeID         string
	eventPoster    *EventPoster                         // for posting K8s events
	staleChecker   *mount.StaleMountChecker             // for detecting stale mounts
	recoverer      *mount.MountRecoverer                // for recovering stale mounts
	circuitBreaker *circuitbreaker.VolumeCircuitBreaker // for preventing mount retry storms
}

// NewNodeServer creates a new Node service
// If k8sClient is provided, events will be posted for mount failures
func NewNodeServer(driver *Driver, nodeID string, k8sClient kubernetes.Interface) *NodeServer {
	var eventPoster *EventPoster
	if k8sClient != nil {
		eventPoster = NewEventPoster(k8sClient)
	}

	// Use injected mounter if available (for testing), otherwise create new one
	var m mount.Mounter
	if driver.mounter != nil {
		m = driver.mounter
	} else {
		m = mount.NewMounter()
	}

	// Use injected connector if available (for testing), otherwise create new one
	var connector nvme.Connector
	if driver.nvmeConnector != nil {
		connector = driver.nvmeConnector
	} else {
		nvmeConfig := nvme.DefaultConfig()
		nvmeConfig.ManagedNQNPrefix = driver.managedNQNPrefix
		connector = nvme.NewConnectorWithConfig(nvmeConfig)
		// Pass Prometheus metrics to connector if available
		if driver.metrics != nil {
			connector.SetPromMetrics(driver.metrics)
		}
	}

	// Create stale mount checker using connector's resolver
	staleChecker := mount.NewStaleMountChecker(connector.GetResolver())

	// Inject custom getMountDev function if provided (for testing)
	if driver.getMountDevFunc != nil {
		staleChecker.SetMountDeviceFunc(driver.getMountDevFunc)
	}

	// Create recovery with default config
	recoverer := mount.NewMountRecoverer(
		mount.DefaultRecoveryConfig(),
		m,
		staleChecker,
		connector.GetResolver(),
	)

	// Pass metrics to recoverer if available
	if driver.metrics != nil {
		recoverer.SetMetrics(driver.metrics)
	}

	// Pass metrics to eventPoster if available
	if driver.metrics != nil && eventPoster != nil {
		eventPoster.SetMetrics(driver.metrics)
	}

	return &NodeServer{
		driver:         driver,
		nvmeConn:       connector,
		mounter:        m,
		nodeID:         nodeID,
		eventPoster:    eventPoster,
		staleChecker:   staleChecker,
		recoverer:      recoverer,
		circuitBreaker: circuitbreaker.NewVolumeCircuitBreaker(),
	}
}

// NodeStageVolume stages a volume to a staging path on the node
// This involves:
// 1. Connecting to the NVMe/TCP target
// 2. Waiting for the block device to appear
// 3. Formatting the filesystem if needed
// 4. Mounting the filesystem to the staging path
func (ns *NodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (resp *csi.NodeStageVolumeResponse, err error) {
	// Record metrics for this operation
	metricsStart := time.Now()
	defer func() {
		if ns.driver.metrics != nil {
			ns.driver.metrics.RecordVolumeOp("stage", err, time.Since(metricsStart))
		}
	}()

	volumeID := req.GetVolumeId()
	stagingPath := req.GetStagingTargetPath()

	klog.V(2).Infof("NodeStageVolume called for volume: %s, staging path: %s", volumeID, stagingPath)

	// Validate request
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if stagingPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability is required")
	}

	// Detect volume mode early - block volumes don't have filesystems
	isBlockVolume := req.GetVolumeCapability().GetBlock() != nil

	// ControllerPublishVolume observes the backend and returns the authoritative
	// target coordinates. Fall back to PV volume attributes for compatibility
	// with static volumes and older external-attacher behavior.
	volumeContext := req.GetVolumeContext()
	publishContext := req.GetPublishContext()
	nqn := publishContext[volumeContextNQN]
	if nqn == "" {
		nqn = volumeContext[volumeContextNQN]
	}
	nvmeAddress := publishContext[volumeContextNVMEAddress]
	if nvmeAddress == "" {
		nvmeAddress = volumeContext[volumeContextNVMEAddress]
	}
	if nvmeAddress == "" {
		nvmeAddress = publishContext[volumeContextAddress]
	}
	if nvmeAddress == "" {
		nvmeAddress = volumeContext[volumeContextAddress]
	}
	nvmePort := publishContext[volumeContextPort]
	if nvmePort == "" {
		nvmePort = volumeContext[volumeContextPort]
	}

	if nqn == "" || nvmeAddress == "" || nvmePort == "" {
		return nil, status.Errorf(codes.InvalidArgument,
			"missing required volume context: nqn=%s, nvmeAddress=%s, nvmePort=%s",
			nqn, nvmeAddress, nvmePort)
	}

	// SECURITY: Validate port format and range
	port, err := utils.ValidatePortString(nvmePort, true)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid nvmePort: %v", err)
	}

	// SECURITY: Validate IP address format
	if err := utils.ValidateIPAddress(nvmeAddress); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid nvmeAddress: %v", err)
	}

	// SECURITY: Validate NVMe target context (address + port combination)
	// Note: expectedAddress is empty here as we don't have RDS address in node plugin
	// The controller validates this during volume creation
	if err := utils.ValidateNVMETargetContext(nqn, nvmeAddress, port, ""); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid NVMe target context: %v", err)
	}

	if ns.driver != nil && ns.driver.managedNQNPrefix != "" {
		expectedNQN, err := ns.volumeIDToNQN(volumeID)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid volume ID: %v", err)
		}
		if nqn != expectedNQN {
			return nil, status.Errorf(codes.InvalidArgument,
				"volume NQN %q does not match this driver instance's owned NQN %q", nqn, expectedNQN)
		}
	}

	// Get filesystem type from capability or use default (only for filesystem volumes)
	fsType := defaultFSType
	if !isBlockVolume {
		if mnt := req.GetVolumeCapability().GetMount(); mnt != nil {
			if mnt.FsType != "" {
				fsType = mnt.FsType
			}
		}
	}

	// Extract connection parameters from VolumeContext
	connConfig := nvme.DefaultConnectionConfig()
	connConfig.NrIOQueues = ns.driver.nvmeNrIOQueues
	connConfig.QueueSize = ns.driver.nvmeQueueSize

	if val, ok := volumeContext["ctrlLossTmo"]; ok {
		if parsed, err := strconv.Atoi(val); err == nil {
			connConfig.CtrlLossTmo = parsed
		}
	}

	if val, ok := volumeContext["reconnectDelay"]; ok {
		if parsed, err := strconv.Atoi(val); err == nil {
			connConfig.ReconnectDelay = parsed
		}
	}

	if val, ok := volumeContext["keepAliveTmo"]; ok {
		if parsed, err := strconv.Atoi(val); err == nil {
			connConfig.KeepAliveTmo = parsed
		}
	}

	klog.V(2).Infof("Staging volume %s: NQN=%s, Address=%s:%d, FSType=%s",
		volumeID, nqn, nvmeAddress, port, fsType)

	// Extract PVC info for event posting
	pvcNamespace := volumeContext["csi.storage.k8s.io/pvc/namespace"]
	pvcName := volumeContext["csi.storage.k8s.io/pvc/name"]

	// Log volume stage request
	secLogger := security.GetLogger()
	secLogger.LogVolumeStage(volumeID, ns.nodeID, nqn, nvmeAddress, security.OutcomeUnknown, nil, 0)

	startTime := time.Now()

	// Step 1: Connect to NVMe/TCP target with retry support
	target := nvme.Target{
		Transport:     "tcp",
		NQN:           nqn,
		TargetAddress: nvmeAddress,
		TargetPort:    port,
	}

	klog.V(2).Infof("Connecting with config: ctrl_loss_tmo=%d, reconnect_delay=%d, nr_io_queues=%d, queue_size=%d (with retry)",
		connConfig.CtrlLossTmo, connConfig.ReconnectDelay, connConfig.NrIOQueues, connConfig.QueueSize)

	devicePath, err := ns.nvmeConn.ConnectWithRetry(ctx, target, connConfig)
	if err != nil {
		// Post connection failure event (ignore error - event posting is best effort)
		if ns.eventPoster != nil && pvcNamespace != "" && pvcName != "" {
			targetAddr := fmt.Sprintf("%s:%d", nvmeAddress, port)
			_ = ns.eventPoster.PostConnectionFailure(ctx, pvcNamespace, pvcName, volumeID, ns.nodeID, targetAddr, err)
		}
		// Log volume stage failure
		secLogger.LogVolumeStage(volumeID, ns.nodeID, nqn, nvmeAddress, security.OutcomeFailure, err, time.Since(startTime))
		return nil, status.Errorf(codes.Internal, "failed to connect to NVMe target: %v", err)
	}

	klog.V(2).Infof("Connected to NVMe target, device: %s", devicePath)

	if isBlockVolume {
		// Block volume: device is connected above via nvme-tcp
		// Per CSI spec and AWS EBS CSI driver pattern, NodeStageVolume for block volumes
		// does NOT create anything at staging_target_path - it just ensures device is ready
		// NodePublishVolume will find the device by NQN and bind mount to target path
		klog.V(2).Infof("Successfully staged block volume %s (device: %s, NQN: %s)",
			volumeID, devicePath, nqn)
		secLogger.LogVolumeStage(volumeID, ns.nodeID, nqn, nvmeAddress, security.OutcomeSuccess, nil, time.Since(startTime))
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Filesystem volume: format and mount with circuit breaker protection
	// Wrap format and mount operations in circuit breaker to prevent retry storms
	err = ns.circuitBreaker.Execute(ctx, volumeID, func() error {
		// Step 2a: Determine filesystem state with retry for transient device errors
		// After NVMe-oF connect, the device may not be immediately ready for I/O.
		// blkid exit 1 means "cannot read device" which is transient after connect.
		// blkid exit 2 means "no filesystem" which is definitive.
		// We retry on exit 1 errors to avoid mistakenly formatting an existing volume.
		const (
			isFormattedMaxRetries = 5
			isFormattedRetryDelay = 2 * time.Second
		)

		var formatted bool
		var formatCheckErr error

		for attempt := 1; attempt <= isFormattedMaxRetries; attempt++ {
			formatted, formatCheckErr = ns.mounter.IsFormatted(devicePath)
			if formatCheckErr == nil {
				// blkid succeeded or returned exit 2 (no fs) - we have a definitive answer
				break
			}

			// blkid returned an error (likely exit 1 - device not ready)
			if attempt < isFormattedMaxRetries {
				klog.Warningf("IsFormatted check failed for %s (attempt %d/%d): %v - retrying in %v",
					devicePath, attempt, isFormattedMaxRetries, formatCheckErr, isFormattedRetryDelay)
				select {
				case <-ctx.Done():
					return fmt.Errorf("context cancelled while waiting for device %s to be ready: %w", devicePath, ctx.Err())
				case <-time.After(isFormattedRetryDelay):
					// continue retry
				}
			} else {
				// All retries exhausted - device is not readable
				klog.Errorf("IsFormatted check failed for %s after %d attempts: %v - refusing to format to prevent data loss",
					devicePath, isFormattedMaxRetries, formatCheckErr)
				return fmt.Errorf("cannot determine filesystem state of device %s after %d attempts (last error: %w) - refusing to format to prevent potential data loss",
					devicePath, isFormattedMaxRetries, formatCheckErr)
			}
		}

		// Step 2b: Check filesystem health (only for existing filesystems)
		if formatted {
			klog.V(2).Infof("Running filesystem health check for %s", devicePath)
			if healthErr := mount.CheckFilesystemHealth(ctx, devicePath, fsType); healthErr != nil {
				return fmt.Errorf("filesystem health check failed: %w", healthErr)
			}
		}

		// Step 2c: Format filesystem if needed (only when blkid definitively confirmed no filesystem)
		if formatErr := ns.mounter.Format(devicePath, fsType); formatErr != nil {
			return fmt.Errorf("failed to format device: %w", formatErr)
		}

		// Step 3: Mount to staging path
		mountOptions := []string{}
		if mnt := req.GetVolumeCapability().GetMount(); mnt != nil {
			mountOptions = mnt.MountFlags
		}

		if mountErr := ns.mounter.Mount(devicePath, stagingPath, fsType, mountOptions); mountErr != nil {
			return fmt.Errorf("failed to mount device: %w", mountErr)
		}

		return nil
	})

	if err != nil {
		// Post failure event if this is a circuit breaker or mount error
		if ns.eventPoster != nil && pvcNamespace != "" && pvcName != "" {
			_ = ns.eventPoster.PostMountFailure(ctx, pvcNamespace, pvcName, volumeID, ns.nodeID,
				fmt.Sprintf("stage volume failed: %v", err))
		}
		// Cleanup NVMe connection on failure
		_ = ns.nvmeConn.Disconnect(nqn)
		secLogger.LogVolumeStage(volumeID, ns.nodeID, nqn, nvmeAddress, security.OutcomeFailure, err, time.Since(startTime))
		return nil, status.Errorf(codes.Internal, "failed to stage filesystem volume: %v", err)
	}

	klog.V(2).Infof("Successfully staged volume %s to %s", volumeID, stagingPath)

	// Log volume stage success
	secLogger.LogVolumeStage(volumeID, ns.nodeID, nqn, nvmeAddress, security.OutcomeSuccess, nil, time.Since(startTime))

	return &csi.NodeStageVolumeResponse{}, nil
}

// NodeUnstageVolume unstages a volume from the staging path
// This involves:
// 1. Unmounting the filesystem from the staging path
// 2. Disconnecting from the NVMe/TCP target
func (ns *NodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (resp *csi.NodeUnstageVolumeResponse, err error) {
	// Record metrics for this operation
	metricsStart := time.Now()
	defer func() {
		if ns.driver.metrics != nil {
			ns.driver.metrics.RecordVolumeOp("unstage", err, time.Since(metricsStart))
		}
	}()

	volumeID := req.GetVolumeId()
	stagingPath := req.GetStagingTargetPath()

	klog.V(2).Infof("NodeUnstageVolume called for volume: %s, staging path: %s", volumeID, stagingPath)

	// Validate request
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if stagingPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}

	// Derive NQN from volume ID for logging
	nqn, err := ns.volumeIDToNQN(volumeID)
	if err != nil {
		nqn = "" // Will use empty NQN in logs
	}

	// Log volume unstage request
	secLogger := security.GetLogger()
	secLogger.LogVolumeUnstage(volumeID, ns.nodeID, nqn, security.OutcomeUnknown, nil, 0)

	startTime := time.Now()

	// Detect if this was a block volume by checking if staging path is mounted
	// Filesystem volumes have a mounted filesystem at staging path
	// Block volumes have nothing at staging path (device connected but not mounted)
	isBlockVolume := false
	if mounted, err := ns.mounter.IsLikelyMountPoint(stagingPath); err != nil || !mounted {
		// If we can't check or it's not mounted, assume block volume
		// (staging path may not even exist for block volumes)
		isBlockVolume = true
	}

	klog.V(2).Infof("NodeUnstageVolume: volume %s, isBlock=%v", volumeID, isBlockVolume)

	if isBlockVolume {
		// Block volume: no filesystem to unmount, just disconnect NVMe device
		klog.V(2).Infof("Unstaging block volume %s (NQN: %s)", volumeID, nqn)

		// Step 1: Clean up orphaned bind mounts BEFORE checking device-in-use
		// This prevents the device-in-use check from detecting our own bind mounts
		if nqn != "" {
			// Find device path by NQN
			devicePath, err := ns.nvmeConn.GetDevicePath(nqn)
			if err == nil {
				// Find and cleanup orphaned bind mounts to this device
				cleanedCount, cleanupErr := ns.findAndCleanupOrphanedMounts(ctx, devicePath)
				if cleanupErr != nil {
					klog.Warningf("Error cleaning up orphaned mounts for %s: %v (proceeding)", devicePath, cleanupErr)
				} else if cleanedCount > 0 {
					klog.Infof("Cleaned up %d orphaned bind mount(s) for device %s before unstaging", cleanedCount, devicePath)
				}

				// Step 2: NOW check device-in-use (after cleaning up our own mounts)
				// Use retry logic to avoid transient false positives from momentary FD operations
				result := nvme.CheckDeviceInUseWithRetry(ctx, devicePath, 3, 1*time.Second)

				// Log if we filtered out driver's own PID (helps diagnose false positives)
				if result.FilteredSelfPIDs > 0 {
					klog.V(2).Infof("Device-in-use check filtered %d driver self-reference(s) for %s",
						result.FilteredSelfPIDs, devicePath)
				}

				if result.TimedOut {
					klog.Warningf("Device %s busy check timed out, proceeding with disconnect", devicePath)
				} else if result.InUse {
					// Device still in use after cleaning up bind mounts
					// This means actual processes have it open

					// During graceful shutdown, we need to clean up anyway to avoid wedging the node
					// Check if we're in a terminating context (driver shutting down)
					select {
					case <-ctx.Done():
						// Context cancelled - driver is shutting down
						klog.Warningf("Device %s in use but driver shutting down, forcing cleanup to prevent node wedge. Processes: %v",
							devicePath, result.Processes)
						// Proceed with cleanup
					default:
						// Not shutting down - this is a normal unstage, don't force it
						klog.Errorf("Device %s in use by external processes: %v", devicePath, result.Processes)
						secLogger.LogVolumeUnstage(volumeID, ns.nodeID, nqn, security.OutcomeFailure,
							fmt.Errorf("device in use"), time.Since(startTime))
						return nil, status.Errorf(codes.FailedPrecondition,
							"Device %s has open file descriptors from external processes, cannot safely unstage. "+
								"Ensure pod using volume has terminated. Processes: %v",
							devicePath, result.Processes)
					}
				} else if result.Error != nil {
					klog.Warningf("Device busy check failed for %s: %v (proceeding)", devicePath, result.Error)
				}
			} else {
				klog.Warningf("Failed to find device for NQN %s: %v (proceeding with disconnect)", nqn, err)
			}
		}

		// No staging directory or files to clean up for block volumes
		// Proceed to NVMe disconnect (below)
	} else {
		// Filesystem volume: existing unmount logic

		// Step 1: Unmount from staging path
		if err := ns.mounter.Unmount(stagingPath); err != nil {
			// Log volume unstage failure
			secLogger.LogVolumeUnstage(volumeID, ns.nodeID, nqn, security.OutcomeFailure, err, time.Since(startTime))
			return nil, status.Errorf(codes.Internal, "failed to unmount staging path: %v", err)
		}

		klog.V(2).Infof("Unmounted volume %s from %s", volumeID, stagingPath)

		// SAFETY-04: Check device-in-use before NVMe disconnect (filesystem volume path)
		// This prevents data corruption if processes still have the device open
		// (e.g., during forced pod termination or node failure scenarios)
		if nqn != "" {
			// GetDevicePath returns error (not empty string) if device not connected
			// This is expected during recovery scenarios where device was already disconnected
			devicePath, devErr := ns.nvmeConn.GetDevicePath(nqn)
			if devErr != nil {
				// Device not found/not connected - skip device-in-use check
				// This can happen if:
				// 1. Device was already disconnected (idempotent unstage)
				// 2. Connection was lost (device unreachable)
				// In both cases, proceed with disconnect attempt (which will be a no-op or cleanup)
				klog.V(4).Infof("Could not get device path for NQN %s: %v (device may already be disconnected, proceeding)", nqn, devErr)
			} else {
				// Device path found - check if it's in use before disconnecting
				// Use retry logic to avoid transient false positives from momentary FD operations
				result := nvme.CheckDeviceInUseWithRetry(ctx, devicePath, 3, 1*time.Second)

				// Log if we filtered out driver's own PID (helps diagnose false positives)
				if result.FilteredSelfPIDs > 0 {
					klog.V(2).Infof("Device-in-use check filtered %d driver self-reference(s) for %s",
						result.FilteredSelfPIDs, devicePath)
				}

				if result.TimedOut {
					// Device check timed out - device may be unresponsive
					// Log warning and proceed with disconnect (device likely dead anyway)
					klog.Warningf("Device %s busy check timed out, proceeding with disconnect (device may be unresponsive)",
						devicePath)
				} else if result.InUse {
					// Device has open file descriptors - unsafe to disconnect
					klog.Errorf("Device %s in use by external processes: %v", devicePath, result.Processes)

					// Log failure
					secLogger.LogVolumeUnstage(volumeID, ns.nodeID, nqn, security.OutcomeFailure,
						fmt.Errorf("device in use"), time.Since(startTime))

					return nil, status.Errorf(codes.FailedPrecondition,
						"Device %s has open file descriptors from external processes, cannot safely unstage. "+
							"Ensure pod using volume has terminated. Processes: %v",
						devicePath, result.Processes)
				} else if result.Error != nil {
					// Check failed but not critical - log and proceed
					klog.Warningf("Device busy check failed for %s: %v (proceeding with disconnect)",
						devicePath, result.Error)
				}
				// If InUse=false and no error, proceed normally
			}
		}
	}

	// Step 2: Disconnect from NVMe/TCP target
	// Derive NQN from volume ID (same as what was used during CreateVolume)
	if nqn == "" {
		nqn, err = ns.volumeIDToNQN(volumeID)
		if err != nil {
			// Log but don't fail - volume might have been disconnected already
			klog.Warningf("Failed to derive NQN from volume ID %s: %v", volumeID, err)
		}
	}

	if nqn != "" {
		if err := ns.nvmeConn.Disconnect(nqn); err != nil {
			// Log but don't fail - disconnection issues shouldn't block unstaging
			klog.Warningf("Failed to disconnect NVMe device for volume %s: %v", volumeID, err)
		} else {
			klog.V(2).Infof("Disconnected NVMe device for volume %s (NQN: %s)", volumeID, nqn)
		}
	}

	klog.V(2).Infof("Successfully unstaged volume %s", volumeID)

	// Log volume unstage success
	secLogger.LogVolumeUnstage(volumeID, ns.nodeID, nqn, security.OutcomeSuccess, nil, time.Since(startTime))

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// NodePublishVolume publishes a volume to the target path
// This involves bind-mounting from the staging path to the target path
func (ns *NodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	stagingPath := req.GetStagingTargetPath()
	targetPath := req.GetTargetPath()

	klog.V(2).Infof("NodePublishVolume called for volume: %s, target path: %s", volumeID, targetPath)

	// Validate request
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if stagingPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staging target path is required")
	}
	if targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "volume capability is required")
	}

	// Detect volume mode early
	isBlockVolume := req.GetVolumeCapability().GetBlock() != nil

	if isBlockVolume {
		// Block volume: find NVMe device by NQN and bind mount to target file

		// Get NQN from volume context or derive from volume ID
		volumeContext := req.GetVolumeContext()
		nqn := volumeContext[volumeContextNQN]
		if nqn == "" {
			var err error
			nqn, err = ns.volumeIDToNQN(volumeID)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "failed to derive NQN from volume ID: %v", err)
			}
		}

		// Find device path by NQN (device was connected in NodeStageVolume)
		devicePath, err := ns.nvmeConn.GetDevicePath(nqn)
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition,
				"failed to find NVMe device for NQN %s: %v (was NodeStageVolume called?)",
				nqn, err)
		}

		// Verify device exists before attempting mount
		if _, err := os.Stat(devicePath); err != nil {
			return nil, status.Errorf(codes.Internal, "block device not found: %s", devicePath)
		}

		klog.V(2).Infof("Publishing block volume %s: NQN %s, device %s -> target %s",
			volumeID, nqn, devicePath, targetPath)

		// Log volume publish request
		secLogger := security.GetLogger()
		secLogger.LogVolumePublish(volumeID, ns.nodeID, targetPath, security.OutcomeUnknown, nil, 0)
		startTime := time.Now()

		// Get device major:minor numbers for mknod
		var stat syscall.Stat_t
		if err := syscall.Stat(devicePath, &stat); err != nil {
			secLogger.LogVolumePublish(volumeID, ns.nodeID, targetPath, security.OutcomeFailure, err, time.Since(startTime))
			return nil, status.Errorf(codes.Internal, "failed to stat device %s: %v", devicePath, err)
		}

		// Ensure parent directory exists
		parentDir := filepath.Dir(targetPath)
		if err := os.MkdirAll(parentDir, 0750); err != nil {
			secLogger.LogVolumePublish(volumeID, ns.nodeID, targetPath, security.OutcomeFailure, err, time.Since(startTime))
			return nil, status.Errorf(codes.Internal, "failed to create parent directory: %v", err)
		}

		// Check if device node already exists (idempotency)
		if _, err := os.Stat(targetPath); err == nil {
			klog.V(4).Infof("Device node %s already exists, assuming idempotent retry", targetPath)
		} else {
			// Create device node using mknod (avoids devtmpfs bind mount storm)
			// This creates a block device node with the same major:minor as the source device
			mode := uint32(syscall.S_IFBLK | 0660)
			if req.GetReadonly() {
				mode = uint32(syscall.S_IFBLK | 0440)
			}

			if err := syscall.Mknod(targetPath, mode, int(stat.Rdev)); err != nil {
				secLogger.LogVolumePublish(volumeID, ns.nodeID, targetPath, security.OutcomeFailure, err, time.Since(startTime))
				return nil, status.Errorf(codes.Internal, "failed to create device node via mknod: %v", err)
			}

			klog.V(2).Infof("Created block device node at %s (major:minor %d:%d)",
				targetPath, unix.Major(uint64(stat.Rdev)), unix.Minor(uint64(stat.Rdev)))
		}

		klog.V(2).Infof("Successfully published block volume %s to %s", volumeID, targetPath)
		secLogger.LogVolumePublish(volumeID, ns.nodeID, targetPath, security.OutcomeSuccess, nil, time.Since(startTime))
		return &csi.NodePublishVolumeResponse{}, nil
	}

	// Filesystem volume: existing bind mount from staging to target

	// Check if staging path is mounted
	mounted, err := ns.mounter.IsLikelyMountPoint(stagingPath)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to check staging path: %v", err)
	}
	if !mounted {
		return nil, status.Errorf(codes.FailedPrecondition,
			"staging path %s is not mounted", stagingPath)
	}

	// Check for stale mount and attempt recovery
	// Extract NQN from volume context or derive from volumeID
	volumeContext := req.GetVolumeContext()
	nqn := volumeContext[volumeContextNQN]
	if nqn == "" {
		nqn, _ = ns.volumeIDToNQN(volumeID)
	}
	if nqn != "" {
		fsType := defaultFSType
		if mnt := req.GetVolumeCapability().GetMount(); mnt != nil && mnt.FsType != "" {
			fsType = mnt.FsType
		}
		// Get mount options for recovery (base options, not bind options)
		stagingMountOptions := []string{}
		if mnt := req.GetVolumeCapability().GetMount(); mnt != nil {
			stagingMountOptions = mnt.MountFlags
		}

		// Extract PVC info from volume context if available
		pvcNamespace := volumeContext["csi.storage.k8s.io/pvc/namespace"]
		pvcName := volumeContext["csi.storage.k8s.io/pvc/name"]

		if err := ns.checkAndRecoverMount(ctx, stagingPath, nqn, fsType, stagingMountOptions, pvcNamespace, pvcName, volumeID); err != nil {
			return nil, status.Errorf(codes.Internal, "stale mount recovery failed: %v", err)
		}
	}

	// Build mount options
	mountOptions := []string{"bind"}
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	// Add any additional mount flags from capability
	if mnt := req.GetVolumeCapability().GetMount(); mnt != nil {
		mountOptions = append(mountOptions, mnt.MountFlags...)
	}

	// Log volume publish request
	secLogger := security.GetLogger()
	secLogger.LogVolumePublish(volumeID, ns.nodeID, targetPath, security.OutcomeUnknown, nil, 0)

	startTime := time.Now()

	// Bind mount from staging to target
	if err := ns.mounter.Mount(stagingPath, targetPath, "", mountOptions); err != nil {
		// Log volume publish failure
		secLogger.LogVolumePublish(volumeID, ns.nodeID, targetPath, security.OutcomeFailure, err, time.Since(startTime))
		return nil, status.Errorf(codes.Internal, "failed to bind mount: %v", err)
	}

	klog.V(2).Infof("Successfully published volume %s to %s", volumeID, targetPath)

	// Log volume publish success
	secLogger.LogVolumePublish(volumeID, ns.nodeID, targetPath, security.OutcomeSuccess, nil, time.Since(startTime))

	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume unpublishes a volume from the target path
func (ns *NodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	targetPath := req.GetTargetPath()

	klog.V(2).Infof("NodeUnpublishVolume called for volume: %s, target path: %s", volumeID, targetPath)

	// Validate request
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if targetPath == "" {
		return nil, status.Error(codes.InvalidArgument, "target path is required")
	}

	// Log volume unpublish request
	secLogger := security.GetLogger()
	secLogger.LogVolumeUnpublish(volumeID, ns.nodeID, targetPath, security.OutcomeUnknown, nil, 0)

	startTime := time.Now()

	// Check if target is a block device (mknod approach) or mount (filesystem)
	var stat syscall.Stat_t
	if err := syscall.Stat(targetPath, &stat); err != nil {
		if os.IsNotExist(err) {
			// Already cleaned up - idempotent
			klog.V(4).Infof("Target path %s does not exist, assuming already unpublished", targetPath)
			secLogger.LogVolumeUnpublish(volumeID, ns.nodeID, targetPath, security.OutcomeSuccess, nil, time.Since(startTime))
			return &csi.NodeUnpublishVolumeResponse{}, nil
		}
		secLogger.LogVolumeUnpublish(volumeID, ns.nodeID, targetPath, security.OutcomeFailure, err, time.Since(startTime))
		return nil, status.Errorf(codes.Internal, "failed to stat target path: %v", err)
	}

	// Block device created with mknod - just remove the device node file
	if stat.Mode&syscall.S_IFMT == syscall.S_IFBLK {
		klog.V(4).Infof("Target %s is a block device node, removing via unlink", targetPath)
		if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
			secLogger.LogVolumeUnpublish(volumeID, ns.nodeID, targetPath, security.OutcomeFailure, err, time.Since(startTime))
			return nil, status.Errorf(codes.Internal, "failed to remove block device node: %v", err)
		}
	} else {
		// Filesystem volume - unmount as usual
		klog.V(4).Infof("Target %s is a mount point, unmounting", targetPath)
		if err := ns.mounter.Unmount(targetPath); err != nil {
			secLogger.LogVolumeUnpublish(volumeID, ns.nodeID, targetPath, security.OutcomeFailure, err, time.Since(startTime))
			return nil, status.Errorf(codes.Internal, "failed to unmount target path: %v", err)
		}
	}

	klog.V(2).Infof("Successfully unpublished volume %s from %s", volumeID, targetPath)

	// Clean up target path after unmount
	// For block volumes, target is a file; for filesystem volumes, target is a directory
	// Use os.RemoveAll which handles both cases
	if err := os.RemoveAll(targetPath); err != nil {
		// Log but don't fail - unmount succeeded, cleanup is best-effort
		klog.Warningf("Failed to remove target path %s: %v", targetPath, err)
	}

	// Log volume unpublish success
	secLogger.LogVolumeUnpublish(volumeID, ns.nodeID, targetPath, security.OutcomeSuccess, nil, time.Since(startTime))

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeGetVolumeStats returns volume usage statistics
func (ns *NodeServer) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	volumeID := req.GetVolumeId()
	volumePath := req.GetVolumePath()

	klog.V(4).Infof("NodeGetVolumeStats called for volume: %s, path: %s", volumeID, volumePath)

	// Validate request
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if volumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "volume path is required")
	}

	// Check if volume path exists and is a mount point
	// Per CSI spec, should return NotFound if volume doesn't exist
	isMounted, err := ns.mounter.IsLikelyMountPoint(volumePath)
	if err != nil || !isMounted {
		return nil, status.Errorf(codes.NotFound, "volume path %s not found or not mounted", volumePath)
	}

	// Track volume condition - always set before returning
	var volumeCondition *csi.VolumeCondition

	// Check for stale mount if we can derive NQN
	// For stats, we just need to verify mount is healthy
	nqn, err := ns.volumeIDToNQN(volumeID)
	if err == nil && ns.staleChecker != nil {
		stale, reason, checkErr := ns.staleChecker.IsMountStale(volumePath, nqn)
		if checkErr != nil {
			klog.V(4).Infof("Could not check mount staleness: %v", checkErr)
			// Health check inconclusive - report as healthy with note
			volumeCondition = &csi.VolumeCondition{
				Abnormal: false,
				Message:  fmt.Sprintf("Health check inconclusive: %v", checkErr),
			}
		} else if stale {
			// For GetVolumeStats, we report unhealthy rather than attempting recovery
			// Recovery should happen in NodePublishVolume when pod accesses volume
			klog.Warningf("Stale mount detected for volume %s at %s (reason: %s)", volumeID, volumePath, reason)
			// Record stale mount metric
			if ns.driver.metrics != nil {
				ns.driver.metrics.RecordStaleMountDetected()
			}
			volumeCondition = &csi.VolumeCondition{
				Abnormal: true,
				Message:  fmt.Sprintf("Stale mount detected: %s", reason),
			}
			// Return early with empty usage for stale mounts
			return &csi.NodeGetVolumeStatsResponse{
				Usage:           []*csi.VolumeUsage{},
				VolumeCondition: volumeCondition,
			}, nil
		} else {
			// Mount is healthy
			volumeCondition = &csi.VolumeCondition{
				Abnormal: false,
				Message:  "Volume is healthy",
			}
		}
	} else {
		// Could not derive NQN or no stale checker - assume healthy
		volumeCondition = &csi.VolumeCondition{
			Abnormal: false,
			Message:  "Volume is healthy",
		}
	}

	// Get device statistics
	stats, err := ns.mounter.GetDeviceStats(volumePath)
	if err != nil {
		// Check if path doesn't exist or isn't mounted
		// Return NotFound per CSI spec for non-existent volumes
		if strings.Contains(err.Error(), "No such file or directory") ||
			strings.Contains(err.Error(), "not mounted") ||
			strings.Contains(err.Error(), "not a mountpoint") {
			return nil, status.Errorf(codes.NotFound, "volume path %s not found or not mounted", volumePath)
		}
		return nil, status.Errorf(codes.Internal, "failed to get volume stats: %v", err)
	}

	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Unit:      csi.VolumeUsage_BYTES,
				Total:     stats.TotalBytes,
				Used:      stats.UsedBytes,
				Available: stats.AvailableBytes,
			},
			{
				Unit:      csi.VolumeUsage_INODES,
				Total:     stats.TotalInodes,
				Used:      stats.UsedInodes,
				Available: stats.AvailableInodes,
			},
		},
		VolumeCondition: volumeCondition,
	}, nil
}

// NodeGetCapabilities returns the supported capabilities of the node service
func (ns *NodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	klog.V(5).Info("NodeGetCapabilities called")

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: ns.driver.nscaps,
	}, nil
}

// NodeGetInfo returns information about the node
func (ns *NodeServer) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	klog.V(4).Infof("NodeGetInfo called for node: %s", ns.nodeID)

	return &csi.NodeGetInfoResponse{
		NodeId: ns.nodeID,
		// MaxVolumesPerNode: 0 means unlimited
		MaxVolumesPerNode: 0,
		// AccessibleTopology provides topology information for scheduling
		// Using simple default topology - can be extended for zone/region awareness
		AccessibleTopology: &csi.Topology{
			Segments: map[string]string{
				"topology.nvmet.csi.whiskey.works/zone": "default",
			},
		},
	}, nil
}

// findAndCleanupOrphanedMounts finds all bind mounts pointing to a device and unmounts them
// Returns the number of mounts cleaned up
func (ns *NodeServer) findAndCleanupOrphanedMounts(ctx context.Context, devicePath string) (int, error) {
	klog.V(4).Infof("Searching for orphaned bind mounts to device %s", devicePath)

	// Get all mounts with timeout to prevent hanging
	// Uses package-level function from mount package
	mounts, err := mount.GetMountsWithTimeout(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to get mounts: %w", err)
	}

	cleanedCount := 0
	for _, mnt := range mounts {
		// Check if this mount's source is our device
		// mnt.Source is the source device for the mount
		if mnt.Source == devicePath {
			klog.V(2).Infof("Found orphaned bind mount: %s -> %s", devicePath, mnt.Mountpoint)

			// Unmount it (force if needed)
			if err := ns.mounter.Unmount(mnt.Mountpoint); err != nil {
				klog.Warningf("Failed to unmount orphaned mount %s: %v", mnt.Mountpoint, err)
				// Continue trying other mounts
			} else {
				klog.V(2).Infof("Successfully cleaned up orphaned mount %s", mnt.Mountpoint)
				cleanedCount++
			}
		}
	}

	if cleanedCount > 0 {
		klog.Infof("Cleaned up %d orphaned bind mount(s) for device %s", cleanedCount, devicePath)
	}

	return cleanedCount, nil
}

// NodeExpandVolume expands the filesystem on the node after volume expansion
func (ns *NodeServer) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	volumePath := req.GetVolumePath()

	klog.V(2).Infof("NodeExpandVolume called for volume: %s, path: %s", volumeID, volumePath)

	// Validate request
	if volumeID == "" {
		return nil, status.Error(codes.InvalidArgument, "volume ID is required")
	}
	if volumePath == "" {
		return nil, status.Error(codes.InvalidArgument, "volume path is required")
	}

	// Check if volume path is mounted
	// Per CSI spec, should return NotFound if volume doesn't exist
	mounted, err := ns.mounter.IsLikelyMountPoint(volumePath)
	if err != nil || !mounted {
		return nil, status.Errorf(codes.NotFound, "volume path %s not found or not mounted", volumePath)
	}

	// Derive NQN from volume ID to get device path
	nqn, err := ns.volumeIDToNQN(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to derive NQN from volume ID: %v", err)
	}

	// Get device path using NVMe connector
	devicePath, err := ns.nvmeConn.GetDevicePath(nqn)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get device path: %v", err)
	}

	klog.V(2).Infof("Expanding filesystem on device %s for volume %s", devicePath, volumeID)

	// Resize the filesystem to use the expanded device
	if err := ns.mounter.ResizeFilesystem(devicePath, volumePath); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to resize filesystem: %v", err)
	}

	// Get updated capacity
	capacityBytes := req.GetCapacityRange().GetRequiredBytes()

	klog.V(2).Infof("Successfully expanded volume %s filesystem to %d bytes", volumeID, capacityBytes)

	return &csi.NodeExpandVolumeResponse{
		CapacityBytes: capacityBytes,
	}, nil
}

// Helper functions

// checkAndRecoverMount checks if staging mount is stale and attempts recovery
// Returns nil if mount is healthy or recovery succeeded
// Returns error if mount is stale and recovery failed
func (ns *NodeServer) checkAndRecoverMount(ctx context.Context, stagingPath, nqn, fsType string, mountOptions []string, pvcNamespace, pvcName, volumeID string) error {
	// Skip stale mount check if staleChecker is not initialized (e.g., in tests)
	if ns.staleChecker == nil {
		return nil
	}

	// Check for stale mount with detailed info for event posting
	staleInfo, err := ns.staleChecker.GetStaleInfo(stagingPath, nqn)
	if err != nil {
		klog.Warningf("Failed to check mount staleness for %s: %v", stagingPath, err)
		// Don't fail the operation if we can't check - proceed optimistically
		return nil
	}

	if !staleInfo.IsStale {
		return nil
	}

	klog.Warningf("Stale mount detected at %s (reason: %s), attempting recovery", stagingPath, staleInfo.Reason)

	// Post stale mount detection event (ignore error - event posting is best effort)
	if ns.eventPoster != nil && pvcNamespace != "" && pvcName != "" {
		_ = ns.eventPoster.PostStaleMountDetected(ctx, pvcNamespace, pvcName, volumeID, ns.nodeID, staleInfo.MountDevice, staleInfo.CurrentDevice)
	}

	// Attempt recovery
	result, err := ns.recoverer.Recover(ctx, stagingPath, nqn, fsType, mountOptions)
	if err != nil {
		// Recovery failed - post event and return error (ignore event error - best effort)
		if ns.eventPoster != nil {
			_ = ns.eventPoster.PostRecoveryFailed(ctx, pvcNamespace, pvcName, volumeID, ns.nodeID, result.Attempts, err)
		}
		return fmt.Errorf("mount recovery failed: %w", err)
	}

	klog.V(2).Infof("Mount recovery succeeded for %s (attempts: %d, device: %s -> %s)",
		stagingPath, result.Attempts, result.OldDevice, result.NewDevice)

	return nil
}

// volumeIDToNQN derives the NQN owned by this node-plugin instance. Using the
// configured prefix keeps later CSI calls consistent with NodeStageVolume for
// isolated backend identities and remains deterministic across pod restarts.
func (ns *NodeServer) volumeIDToNQN(volumeID string) (string, error) {
	prefix := ""
	if ns != nil && ns.driver != nil {
		prefix = ns.driver.managedNQNPrefix
	}
	return utils.VolumeIDToNQNWithPrefix(volumeID, prefix)
}
