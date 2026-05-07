package driver

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/attachment"
	storagebackend "git.srvlab.io/whiskey/rds-csi-driver/pkg/backend"
	linuxzolbackend "git.srvlab.io/whiskey/rds-csi-driver/pkg/backend/linuxzol"
	routerosbackend "git.srvlab.io/whiskey/rds-csi-driver/pkg/backend/routeros"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/mount"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/nvme"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/observability"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/rds"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/reconciler"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/utils"
)

const (
	// DriverName is the official name of this CSI driver
	DriverName = "rds.csi.srvlab.io"

	// DriverVersion is the version of the driver
	// These will be set via ldflags during build
	defaultVersion = "dev"
)

var (
	version   = defaultVersion
	gitCommit = "unknown"
	buildDate = "unknown"
)

// Driver implements the CSI Controller, Node, and Identity services
type Driver struct {
	name    string
	version string
	nodeID  string

	// CSI services
	ids csi.IdentityServer
	cs  csi.ControllerServer
	ns  csi.NodeServer

	// RDS client (interface allows different implementations: SSH, API, mock)
	rdsClient rds.RDSClient

	// Controller storage backend. RDS is currently adapted through this generic
	// interface; ZoL/nvmet backends will plug in here without changing node code.
	backend    storagebackend.Backend
	backendsMu sync.Mutex
	backends   map[string]storagebackend.Backend

	// backendFactory is injectable for controller tests. Production uses
	// Driver.newBackendForSelection.
	backendFactory func(BackendSelection) (storagebackend.Backend, error)

	volumeBackendMu         sync.Mutex
	volumeBackendSelections map[string]BackendSelection

	snapshotBackendMu         sync.Mutex
	snapshotBackendSelections map[string]BackendSelection

	// NVMe connector (interface allows different implementations: real, mock)
	nvmeConnector nvme.Connector

	// Mounter (interface allows different implementations: real, mock)
	mounter mount.Mounter

	// Custom getMountDev function for testing (optional)
	getMountDevFunc func(path string) (string, error)

	// Kubernetes client (for events and reconciler)
	k8sClient kubernetes.Interface

	// Informer factory (for cached API access, avoids throttling)
	informerFactory informers.SharedInformerFactory

	// Prometheus metrics (may be nil if disabled)
	metrics *observability.Metrics

	// Orphan reconciler (optional)
	reconciler *reconciler.OrphanReconciler

	// Attachment manager (for controller only)
	attachmentManager *attachment.AttachmentManager

	// Attachment reconciler (for controller only)
	attachmentReconciler *attachment.AttachmentReconciler

	// Node watcher for event-driven attachment reconciliation
	nodeWatcher *attachment.NodeWatcher

	// Connection manager for RDS connection resilience
	connectionManager *rds.ConnectionManager

	// Grace period for attachment handoff during live migration
	attachmentGracePeriod time.Duration

	// VMI grouper for per-VMI operation serialization
	vmiGrouper *VMIGrouper

	// Managed NQN prefix for orphan cleaner filtering
	managedNQNPrefix string

	// Capabilities
	vcaps  []*csi.VolumeCapability_AccessMode
	cscaps []*csi.ControllerServiceCapability
	nscaps []*csi.NodeServiceCapability
}

// DriverConfig contains configuration for creating a driver instance
type DriverConfig struct {
	DriverName string
	NodeID     string
	Version    string

	// RDS connection settings
	RDSAddress            string
	RDSPort               int
	RDSUser               string
	RDSPrivateKey         []byte
	RDSHostKey            []byte // SSH host public key for verification
	RDSInsecureSkipVerify bool   // Skip host key verification (INSECURE)
	RDSVolumeBasePath     string // Base path for volumes on RDS (e.g., /storage-pool/metal-csi)

	// RDS mutation guardrails
	RDSMutationMinInterval time.Duration
	RDSMutationBurst       int
	RDSMutationTimeout     time.Duration

	// Kubernetes client (required for orphan reconciler)
	K8sClient kubernetes.Interface

	// Prometheus metrics (optional, nil to disable)
	Metrics *observability.Metrics

	// Orphan reconciler settings
	EnableOrphanReconciler bool
	OrphanCheckInterval    time.Duration
	OrphanGracePeriod      time.Duration
	OrphanDryRun           bool
	OrphanRunOnStart       bool
	OrphanMinConfirmations int

	// Attachment reconciler settings
	EnableAttachmentReconciler    bool
	AttachmentReconcileInterval   time.Duration // Default: 5 minutes
	AttachmentGracePeriod         time.Duration // Default: 30 seconds
	StaleVolumeAttachmentCleanup  bool
	StaleVolumeAttachmentDelete   bool
	StaleVolumeAttachmentMinConfs int

	// VMI serialization settings (for kubevirt concurrent operation mitigation)
	EnableVMISerialization bool          // Enable per-VMI operation locks
	VMICacheTTL            time.Duration // Cache TTL for PVC->VMI mapping (default: 60s)

	// NQN prefix for orphan cleaner filtering (required for node mode)
	ManagedNQNPrefix string

	// Mode flags
	EnableController bool
	EnableNode       bool
}

// NewDriver creates a new RDS CSI driver
func NewDriver(config DriverConfig) (*Driver, error) {
	if config.DriverName == "" {
		config.DriverName = DriverName
	}
	if config.Version == "" {
		config.Version = version
	}

	klog.Infof("Driver: %s Version: %s GitCommit: %s BuildDate: %s", config.DriverName, config.Version, gitCommit, buildDate)

	// Set configured base path as the allowed path for volume validation
	if config.RDSVolumeBasePath != "" {
		if err := utils.SetAllowedBasePath(config.RDSVolumeBasePath); err != nil {
			return nil, fmt.Errorf("failed to set allowed base path: %w", err)
		}
		klog.Infof("Volume base path configured: %s", config.RDSVolumeBasePath)
	}

	// Validate NQN prefix for node plugin (required for orphan cleaner safety)
	if config.EnableNode {
		if config.ManagedNQNPrefix == "" {
			return nil, fmt.Errorf("managed NQN prefix is required for node plugin (set %s)", nvme.EnvManagedNQNPrefix)
		}
		if err := nvme.ValidateNQNPrefix(config.ManagedNQNPrefix); err != nil {
			return nil, fmt.Errorf("invalid NQN prefix: %w", err)
		}
		klog.Infof("Driver managing volumes with NQN prefix: %s", config.ManagedNQNPrefix)
	}

	driver := &Driver{
		name:                      config.DriverName,
		version:                   config.Version,
		nodeID:                    config.NodeID,
		k8sClient:                 config.K8sClient,
		metrics:                   config.Metrics,
		managedNQNPrefix:          config.ManagedNQNPrefix,
		backends:                  map[string]storagebackend.Backend{},
		volumeBackendSelections:   map[string]BackendSelection{},
		snapshotBackendSelections: map[string]BackendSelection{},
	}

	// Initialize RDS client if controller is enabled and an RDS address is
	// configured. Linux-only controller deployments intentionally omit this.
	if config.EnableController && config.RDSAddress != "" {
		rdsClient, err := rds.NewClient(rds.ClientConfig{
			Address:            config.RDSAddress,
			Port:               config.RDSPort,
			User:               config.RDSUser,
			PrivateKey:         config.RDSPrivateKey,
			HostKey:            config.RDSHostKey,
			InsecureSkipVerify: config.RDSInsecureSkipVerify,
			CommandObserver:    config.Metrics,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create RDS client: %w", err)
		}

		// Connect to RDS
		if err := rdsClient.Connect(); err != nil {
			return nil, fmt.Errorf("failed to connect to RDS: %w", err)
		}

		if config.RDSMutationMinInterval > 0 {
			guardedClient, err := rds.NewGuardedClient(rdsClient, rds.GuardedClientConfig{
				MinInterval: config.RDSMutationMinInterval,
				Burst:       config.RDSMutationBurst,
				Timeout:     config.RDSMutationTimeout,
				Observer:    config.Metrics,
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create guarded RDS client: %w", err)
			}
			rdsClient = guardedClient
			klog.Infof("RDS mutation guard enabled (min_interval=%v, burst=%d, timeout=%v)",
				config.RDSMutationMinInterval, config.RDSMutationBurst, config.RDSMutationTimeout)
		}

		driver.rdsClient = rdsClient
		driver.backend = routerosbackend.New(rdsClient)
		klog.Infof("Connected to RDS at %s:%d", config.RDSAddress, config.RDSPort)
	} else if config.EnableController {
		klog.Info("RouterOS/RDS backend not initialized (no RDS address configured)")
	}

	// Initialize attachment manager if controller is enabled
	if config.EnableController && config.K8sClient != nil {
		driver.attachmentManager = attachment.NewAttachmentManager(config.K8sClient)
		if config.Metrics != nil {
			driver.attachmentManager.SetMetrics(config.Metrics)

			// Wire AttachmentManager into Metrics for nvme_connections_active gauge.
			// GaugeFunc callback queries current attachment count on each Prometheus scrape,
			// surviving controller restarts (attachment state rebuilt from VolumeAttachments).
			//
			// NOTE: This counts volumes (len(attachments)), not per-node attachments.
			// During live migration with dual-attach, a volume attached to 2 nodes temporarily
			// is counted as 1, not 2. This matches the VolumeAttachment count in the cluster.
			config.Metrics.SetAttachmentManager(func() int {
				return len(driver.attachmentManager.ListAttachments())
			})
		}
		klog.Info("Attachment manager created")
	}

	// Wire RDS monitoring (disk performance + hardware health) into Prometheus metrics.
	// GaugeFunc callbacks poll via SSH (/disk monitor-traffic) and SNMP (MIKROTIK-MIB)
	// during Prometheus scrape. Only registers in controller mode (node plugin has no RDS client).
	if config.EnableController && config.Metrics != nil && driver.rdsClient != nil {
		// Monitor the storage pool disk (configurable via basePath extraction)
		// Default: "storage-pool" - the primary Btrfs RAID6 pool
		storageSlot := "storage-pool"

		// SNMP target configuration (use storage VLAN IP, same as SSH operations)
		// CSI controller runs in environment with access to storage network
		snmpHost := "10.42.68.1"  // TODO: make configurable via helm values
		snmpCommunity := "public" // TODO: make configurable via helm values

		config.Metrics.SetRDSMonitoring(
			storageSlot,
			snmpHost,
			snmpCommunity,
			// Disk performance metrics callback (SSH)
			func() (*observability.DiskHealthSnapshot, error) {
				metrics, err := driver.rdsClient.GetDiskMetrics(storageSlot)
				if err != nil {
					return nil, err
				}
				return &observability.DiskHealthSnapshot{
					ReadOpsPerSecond:  metrics.ReadOpsPerSecond,
					WriteOpsPerSecond: metrics.WriteOpsPerSecond,
					ReadBytesPerSec:   metrics.ReadBytesPerSec,
					WriteBytesPerSec:  metrics.WriteBytesPerSec,
					ReadTimeMs:        metrics.ReadTimeMs,
					WriteTimeMs:       metrics.WriteTimeMs,
					WaitTimeMs:        metrics.WaitTimeMs,
					InFlightOps:       metrics.InFlightOps,
					ActiveTimeMs:      metrics.ActiveTimeMs,
				}, nil
			},
			// Hardware health metrics callback (SNMP)
			func() (*observability.HardwareHealthSnapshot, error) {
				metrics, err := driver.rdsClient.GetHardwareHealth(snmpHost, snmpCommunity)
				if err != nil {
					return nil, err
				}
				return &observability.HardwareHealthSnapshot{
					CPUTemperature:    metrics.CPUTemperature,
					BoardTemperature:  metrics.BoardTemperature,
					Fan1Speed:         metrics.Fan1Speed,
					Fan2Speed:         metrics.Fan2Speed,
					PSU1Power:         metrics.PSU1Power,
					PSU2Power:         metrics.PSU2Power,
					PSU1Temperature:   metrics.PSU1Temperature,
					PSU2Temperature:   metrics.PSU2Temperature,
					DiskPoolSizeBytes: metrics.DiskPoolSizeBytes,
					DiskPoolUsedBytes: metrics.DiskPoolUsedBytes,
				}, nil
			},
		)
		klog.Infof("RDS monitoring enabled (disk slot=%s, snmp=%s)", storageSlot, snmpHost)
	}

	// Initialize informer factory if we have k8s client (needed for attachment reconciler caching)
	if config.EnableController && config.K8sClient != nil {
		// Create informer factory with 5-minute resync period
		// This provides cached access to nodes and PVs, avoiding API throttling
		driver.informerFactory = informers.NewSharedInformerFactory(config.K8sClient, 5*time.Minute)
		klog.Info("Informer factory created (resync=5m)")
	}

	// Initialize attachment reconciler if enabled
	if config.EnableController && config.EnableAttachmentReconciler && config.K8sClient != nil && driver.attachmentManager != nil {
		// Ensure informer factory is available
		if driver.informerFactory == nil {
			return nil, fmt.Errorf("informer factory required for attachment reconciler")
		}

		// Create EventPoster for posting lifecycle events
		var eventPoster attachment.EventPoster
		if config.K8sClient != nil {
			eventPoster = NewEventPoster(config.K8sClient)
		}

		// Get listers from informer factory (cached, no API calls)
		nodeLister := driver.informerFactory.Core().V1().Nodes().Lister()
		pvLister := driver.informerFactory.Core().V1().PersistentVolumes().Lister()
		vaLister := driver.informerFactory.Storage().V1().VolumeAttachments().Lister()

		reconcilerConfig := attachment.ReconcilerConfig{
			Manager:                       driver.attachmentManager,
			K8sClient:                     config.K8sClient,
			NodeLister:                    nodeLister,
			PVLister:                      pvLister,
			VolumeAttachmentLister:        vaLister,
			Interval:                      config.AttachmentReconcileInterval,
			GracePeriod:                   config.AttachmentGracePeriod,
			StaleVolumeAttachmentCleanup:  config.StaleVolumeAttachmentCleanup,
			StaleVolumeAttachmentDelete:   config.StaleVolumeAttachmentDelete,
			StaleVolumeAttachmentMinConfs: config.StaleVolumeAttachmentMinConfs,
			Metrics:                       config.Metrics,
			EventPoster:                   eventPoster,
		}

		attachmentReconciler, err := attachment.NewAttachmentReconciler(reconcilerConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create attachment reconciler: %w", err)
		}

		driver.attachmentReconciler = attachmentReconciler
		driver.attachmentGracePeriod = config.AttachmentGracePeriod
		if driver.attachmentGracePeriod <= 0 {
			driver.attachmentGracePeriod = 30 * time.Second
		}
		klog.Infof("Attachment reconciler enabled with cached informers (interval=%v, grace_period=%v, stale_volumeattachment_cleanup=%t, stale_volumeattachment_delete=%t)",
			config.AttachmentReconcileInterval, config.AttachmentGracePeriod, config.StaleVolumeAttachmentCleanup, config.StaleVolumeAttachmentDelete)
	}

	// Initialize VMI grouper for per-VMI operation serialization
	if config.EnableController && config.EnableVMISerialization && config.K8sClient != nil {
		driver.vmiGrouper = NewVMIGrouper(VMIGrouperConfig{
			K8sClient: config.K8sClient,
			CacheTTL:  config.VMICacheTTL,
			Enabled:   true,
		})
		klog.Infof("VMI serialization enabled (cache_ttl=%v)", config.VMICacheTTL)
	}

	// Add volume capabilities
	driver.addVolumeCapabilities()

	// Add controller service capabilities
	if config.EnableController {
		driver.addControllerServiceCapabilities()
	}

	// Add node service capabilities
	if config.EnableNode {
		driver.addNodeServiceCapabilities()
	}

	// Initialize orphan reconciler if enabled and we have controller + k8s client
	if config.EnableController && config.EnableOrphanReconciler && config.K8sClient != nil {
		reconcilerConfig := reconciler.OrphanReconcilerConfig{
			RDSClient:            driver.rdsClient,
			K8sClient:            config.K8sClient,
			CheckInterval:        config.OrphanCheckInterval,
			GracePeriod:          config.OrphanGracePeriod,
			DryRun:               config.OrphanDryRun,
			RunOnStart:           config.OrphanRunOnStart,
			MinimumConfirmations: config.OrphanMinConfirmations,
			Enabled:              true,
			BasePath:             config.RDSVolumeBasePath,
		}

		orphanReconciler, err := reconciler.NewOrphanReconciler(reconcilerConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create orphan reconciler: %w", err)
		}

		driver.reconciler = orphanReconciler
		klog.Infof("Orphan reconciler enabled (interval=%v, grace_period=%v, dry_run=%v)",
			config.OrphanCheckInterval, config.OrphanGracePeriod, config.OrphanDryRun)
	}

	return driver, nil
}

// addVolumeCapabilities adds supported volume access modes
func (d *Driver) addVolumeCapabilities() {
	d.vcaps = []*csi.VolumeCapability_AccessMode{
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
		},
		{
			Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER, // NEW: for KubeVirt live migration
		},
	}
}

// addControllerServiceCapabilities adds controller service capabilities
func (d *Driver) addControllerServiceCapabilities() {
	d.cscaps = []*csi.ControllerServiceCapability{
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_GET_CAPACITY,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
				},
			},
		},
		{
			Type: &csi.ControllerServiceCapability_Rpc{
				Rpc: &csi.ControllerServiceCapability_RPC{
					Type: csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
				},
			},
		},
	}
}

// addNodeServiceCapabilities adds node service capabilities
func (d *Driver) addNodeServiceCapabilities() {
	d.nscaps = []*csi.NodeServiceCapability{
		{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
				},
			},
		},
		{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
				},
			},
		},
		{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
				},
			},
		},
		{
			Type: &csi.NodeServiceCapability_Rpc{
				Rpc: &csi.NodeServiceCapability_RPC{
					Type: csi.NodeServiceCapability_RPC_VOLUME_CONDITION,
				},
			},
		},
	}
}

// Run starts the CSI driver gRPC server
func (d *Driver) Run(endpoint string) error {
	klog.Infof("Starting RDS CSI driver at endpoint %s", endpoint)

	// Initialize identity service (always available)
	d.ids = NewIdentityServer(d)

	// Initialize controller service if enabled
	if d.controllerBackend() != nil {
		klog.Info("Controller service enabled")
		d.cs = NewControllerServer(d)
	}

	// Initialize node service if enabled
	if d.nodeID != "" {
		klog.Info("Node service enabled")
		d.ns = NewNodeServer(d, d.nodeID, d.k8sClient)
	}

	// Start informers if we have an informer factory
	// This must happen BEFORE the attachment reconciler starts, so caches are populated
	if d.informerFactory != nil {
		klog.Info("Starting informer factory...")
		ctx := context.Background()

		// Start all informers (non-blocking)
		d.informerFactory.Start(ctx.Done())

		// Wait for caches to sync before proceeding
		// This ensures the reconciler has cached data available immediately
		klog.Info("Waiting for informer caches to sync...")
		synced := d.informerFactory.WaitForCacheSync(ctx.Done())
		for informerType, ok := range synced {
			if !ok {
				klog.Warningf("Failed to sync cache for %v", informerType)
			}
		}
		klog.Info("Informer caches synced successfully")

		// Register node watcher on informer (after caches are synced)
		if d.attachmentReconciler != nil {
			d.nodeWatcher = attachment.NewNodeWatcher(d.attachmentReconciler, d.metrics)
			nodeInformer := d.informerFactory.Core().V1().Nodes().Informer()
			if _, err := nodeInformer.AddEventHandler(d.nodeWatcher.GetEventHandlers()); err != nil {
				klog.Errorf("Failed to register node watcher: %v", err)
			} else {
				klog.Info("Node watcher registered for attachment reconciliation triggers")
			}
		}
	}

	// Initialize attachment manager state
	if d.attachmentManager != nil {
		// Use a timeout context to avoid blocking indefinitely if Kubernetes API is slow
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := d.attachmentManager.Initialize(ctx); err != nil {
			// Log warning but don't fail - reconciler will rebuild state later
			klog.Warningf("Failed to initialize attachment manager (will retry via reconciler): %v", err)
		} else {
			klog.Info("Attachment manager initialized")
		}
	}

	// Start attachment reconciler if configured
	if d.attachmentReconciler != nil {
		ctx := context.Background()
		if err := d.attachmentReconciler.Start(ctx); err != nil {
			return fmt.Errorf("failed to start attachment reconciler: %w", err)
		}
		klog.Info("Attachment reconciler started (using cached informers, no API throttling)")

		// Start connection manager (after RDS client is connected)
		if d.rdsClient != nil {
			cmConfig := rds.ConnectionManagerConfig{
				Client:  d.rdsClient,
				Metrics: d.metrics,
			}
			// Set OnReconnect callback to trigger attachment reconciliation
			cmConfig.OnReconnect = func() {
				klog.Info("RDS reconnected, triggering attachment reconciliation")
				d.attachmentReconciler.TriggerReconcile()
			}
			connectionManager, err := rds.NewConnectionManager(cmConfig)
			if err != nil {
				return fmt.Errorf("failed to create connection manager: %w", err)
			}
			d.connectionManager = connectionManager
			ctx := context.Background()
			d.connectionManager.StartMonitor(ctx)
			klog.Info("RDS connection manager started with automatic reconnection")
		}

		// Perform startup reconciliation (after informers synced AND attachment manager initialized)
		klog.Info("Performing startup attachment reconciliation...")
		d.attachmentReconciler.TriggerReconcile()
		klog.Info("Startup reconciliation triggered")
	}

	// Start orphan reconciler if configured
	if d.reconciler != nil {
		ctx := context.Background()
		if err := d.reconciler.Start(ctx); err != nil {
			return fmt.Errorf("failed to start orphan reconciler: %w", err)
		}
		klog.Info("Orphan reconciler started")
	}

	// Start gRPC server
	server := NewNonBlockingGRPCServer(endpoint)
	if err := server.Start(d.ids, d.cs, d.ns); err != nil {
		return fmt.Errorf("failed to start gRPC server: %w", err)
	}

	klog.Info("Driver initialization complete, server running")

	// Block forever (shutdown handled by Stop method via signal handler)
	select {}
}

// Stop stops the driver and cleans up resources
func (d *Driver) Stop() {
	klog.Info("Stopping RDS CSI driver")

	// Stop attachment reconciler if running
	if d.attachmentReconciler != nil {
		d.attachmentReconciler.Stop()
		klog.Info("Attachment reconciler stopped")
	}

	// Stop connection manager if running
	if d.connectionManager != nil {
		d.connectionManager.Stop()
		klog.Info("RDS connection manager stopped")
	}

	// Stop orphan reconciler if running
	if d.reconciler != nil {
		d.reconciler.Stop()
		klog.Info("Orphan reconciler stopped")
	}

	if d.rdsClient != nil {
		if err := d.rdsClient.Close(); err != nil {
			klog.Errorf("Error closing RDS client: %v", err)
		}
	}
}

// ShutdownWithContext gracefully stops the driver within the given context timeout.
// Returns error if shutdown does not complete within the timeout.
func (d *Driver) ShutdownWithContext(ctx context.Context) error {
	klog.Info("Initiating graceful shutdown")

	// Create channel to signal shutdown complete
	done := make(chan struct{})

	go func() {
		d.Stop()
		close(done)
	}()

	select {
	case <-done:
		klog.Info("Graceful shutdown complete")
		return nil
	case <-ctx.Done():
		klog.Warningf("Shutdown did not complete within timeout: %v", ctx.Err())
		return ctx.Err()
	}
}

// SetRDSClient sets the RDS client (for testing)
func (d *Driver) SetRDSClient(client rds.RDSClient) {
	d.rdsClient = client
	if client != nil {
		d.backend = routerosbackend.New(client)
	} else {
		d.backend = nil
	}
}

func (d *Driver) controllerBackend() storagebackend.Backend {
	if d == nil {
		return nil
	}
	if d.backend != nil {
		return d.backend
	}
	if d.rdsClient != nil {
		d.backend = routerosbackend.New(d.rdsClient)
		return d.backend
	}
	return nil
}

func (d *Driver) newBackendForSelection(selection BackendSelection) (storagebackend.Backend, error) {
	switch selection.Type {
	case storagebackend.TypeRouterOSRDS:
		backend := d.controllerBackend()
		if backend == nil {
			return nil, fmt.Errorf("RouterOS/RDS backend is not initialized")
		}
		return backend, nil
	case storagebackend.TypeLinuxZOLNVMET:
		return linuxzolbackend.NewCommandBackend(linuxzolbackend.CommandBackendOptions{
			Config: linuxzolbackend.Config{
				Pool:         selection.ZFSPool,
				Address:      selection.TargetAddr,
				DeviceRoot:   selection.DeviceRoot,
				ConfigFSRoot: selection.ConfigFSRoot,
			},
			Executor:  selection.Executor,
			SSHTarget: selection.SSHTarget,
		})
	default:
		return nil, fmt.Errorf("unsupported backend %q", selection.Type)
	}
}

// SetNVMEConnector sets the NVMe connector (for testing)
func (d *Driver) SetNVMEConnector(connector nvme.Connector) {
	d.nvmeConnector = connector
}

// SetMounter sets the mounter (for testing)
func (d *Driver) SetMounter(mounter mount.Mounter) {
	d.mounter = mounter
}

// SetGetMountDevFunc sets a custom getMountDev function for stale mount checking (for testing)
func (d *Driver) SetGetMountDevFunc(fn func(path string) (string, error)) {
	d.getMountDevFunc = fn
}

// AddVolumeCapabilities adds volume capabilities (exported for testing)
func (d *Driver) AddVolumeCapabilities() {
	d.addVolumeCapabilities()
}

// AddControllerServiceCapabilities adds controller service capabilities (exported for testing)
func (d *Driver) AddControllerServiceCapabilities() {
	d.addControllerServiceCapabilities()
}

// AddNodeServiceCapabilities adds node service capabilities (exported for testing)
func (d *Driver) AddNodeServiceCapabilities() {
	d.addNodeServiceCapabilities()
}

// GetMetrics returns the Prometheus metrics instance (may be nil if disabled)
func (d *Driver) GetMetrics() *observability.Metrics {
	return d.metrics
}

// GetAttachmentManager returns the attachment manager (may be nil if controller disabled)
func (d *Driver) GetAttachmentManager() *attachment.AttachmentManager {
	return d.attachmentManager
}

// GetAttachmentGracePeriod returns the configured grace period for attachment handoff.
func (d *Driver) GetAttachmentGracePeriod() time.Duration {
	return d.attachmentGracePeriod
}

// GetVMIGrouper returns the VMI grouper for per-VMI operation serialization (may be nil if disabled).
func (d *Driver) GetVMIGrouper() *VMIGrouper {
	return d.vmiGrouper
}
