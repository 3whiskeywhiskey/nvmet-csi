package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	storagebackend "gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/backend"
	linuxzolbackend "gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/backend/linuxzol"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/driver"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/nvme"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/observability"
	"gitea.whiskey.works/whiskey-works/nvmet-csi/pkg/utils"
)

const (
	// ShutdownTimeout is the maximum time to wait for graceful shutdown
	ShutdownTimeout = 30 * time.Second
)

var (
	// Driver configuration
	endpoint   = flag.String("endpoint", "unix:///var/lib/kubelet/plugins/nvmet.csi.whiskey.works/csi.sock", "CSI endpoint")
	nodeID     = flag.String("node-id", "", "Node ID (required for node service)")
	driverName = flag.String("driver-name", "nvmet.csi.whiskey.works", "Name of the CSI driver")

	// RDS configuration
	rdsAddress           = flag.String("rds-address", "", "RDS server IP address (required for controller)")
	rdsPort              = flag.Int("rds-port", 22, "RDS SSH port")
	rdsUser              = flag.String("rds-user", "admin", "RDS SSH user")
	rdsKeyFile           = flag.String("rds-key-file", "/etc/nvmet-csi/ssh-key/id_rsa", "Path to RDS SSH private key")
	rdsHostKey           = flag.String("rds-host-key", "", "Path to RDS SSH host public key (required for secure verification)")
	rdsInsecure          = flag.Bool("rds-insecure-skip-verify", false, "Skip SSH host key verification (INSECURE - for testing only)")
	rdsVolumeBasePath    = flag.String("rds-volume-base-path", "", "Base path for volumes on RDS (e.g., /storage-pool/metal-csi, required for file orphan detection)")
	rdsMonitoringEnabled = flag.Bool("rds-monitoring-enabled", true, "Enable RDS disk performance metrics over SSH")
	rdsMonitoringSlot    = flag.String("rds-monitoring-slot", "storage-pool", "RDS disk slot to monitor over SSH")
	rdsSNMPHost          = flag.String("rds-snmp-host", "", "Optional SNMP host for RDS hardware metrics")
	rdsSNMPCommunityFile = flag.String("rds-snmp-community-file", "", "File containing the SNMP community; required with --rds-snmp-host")

	// RDS mutation guardrails
	rdsMutationMinInterval = flag.Duration("rds-mutation-min-interval", 250*time.Millisecond, "Minimum spacing between mutating RDS operations (0 disables guard)")
	rdsMutationBurst       = flag.Int("rds-mutation-burst", 1, "Maximum RDS mutations allowed to burst through the guard")
	rdsMutationTimeout     = flag.Duration("rds-mutation-timeout", 30*time.Second, "Maximum time to wait for the RDS mutation guard")

	// Mode flags
	controllerMode = flag.Bool("controller", false, "Run in controller mode")
	nodeMode       = flag.Bool("node", false, "Run in node mode")

	// Node cleanup flags
	nodeOrphanCleanup               = flag.Bool("node-orphan-cleanup", false, "Run startup cleanup for managed orphaned NVMe connections")
	nodeOrphanCleanupDryRun         = flag.Bool("node-orphan-cleanup-dry-run", true, "Dry-run mode for startup orphan NVMe cleanup (log only, do not disconnect)")
	nodeOrphanCleanupMaxDisconnects = flag.Int("node-orphan-cleanup-max-disconnects", nvme.DefaultOrphanCleanupMaxDisconnects, "Maximum orphaned NVMe sessions to disconnect in one startup cleanup pass (<=0 allows all)")

	// Orphan reconciler flags
	enableOrphanReconciler = flag.Bool("enable-orphan-reconciler", false, "Enable orphan volume detection and cleanup")
	orphanCheckInterval    = flag.Duration("orphan-check-interval", 1*time.Hour, "Interval between orphan checks")
	orphanGracePeriod      = flag.Duration("orphan-grace-period", 5*time.Minute, "Minimum age before considering a volume orphaned")
	orphanDryRun           = flag.Bool("orphan-dry-run", true, "Dry-run mode for orphan cleanup (only log, don't delete)")
	orphanRunOnStart       = flag.Bool("orphan-run-on-start", false, "Run orphan inventory immediately on controller startup")
	orphanMinConfirmations = flag.Int("orphan-min-confirmations", 2, "Consecutive orphan inventory passes required before cleanup")

	// Attachment management flags
	attachmentGracePeriod       = flag.Duration("attachment-grace-period", 30*time.Second, "Grace period for attachment handoff during live migration")
	attachmentReconcileInterval = flag.Duration("attachment-reconcile-interval", 5*time.Minute, "Interval between attachment reconciliation checks")
	staleVACleanup              = flag.Bool("enable-stale-volumeattachment-cleanup", false, "Enable stale Kubernetes VolumeAttachment cleanup for deleted nodes")
	staleVADryRun               = flag.Bool("stale-volumeattachment-dry-run", true, "Dry-run mode for stale VolumeAttachment cleanup")
	staleVAMinConfirmations     = flag.Int("stale-volumeattachment-min-confirmations", 2, "Consecutive stale VolumeAttachment reconciliation passes required before deletion")

	// Export reconciler flags (linux-zol-nvmet reboot recovery). Enabled by
	// default: kernel nvmet configfs state is lost on target reboot and the
	// reconciler is the only path that rebuilds it. Repair-only by design.
	enableExportReconciler    = flag.Bool("enable-export-reconciler", true, "Enable NVMe/TCP export reconciliation for linux-zol-nvmet backends (rebuilds configfs exports lost to target reboots)")
	exportCheckInterval       = flag.Duration("export-check-interval", 10*time.Minute, "Interval between export reconciliation passes")
	exportReconcileDryRun     = flag.Bool("export-reconcile-dry-run", false, "Dry-run mode for export reconciliation (log planned repairs, don't apply)")
	exportReconcileRunOnStart = flag.Bool("export-reconcile-run-on-start", true, "Run export reconciliation immediately on controller startup")

	// VMI serialization flags (kubevirt concurrent operation mitigation)
	enableVMISerialization = flag.Bool("enable-vmi-serialization", false, "Enable per-VMI operation serialization to mitigate kubevirt concurrency issues")
	vmiCacheTTL            = flag.Duration("vmi-cache-ttl", 60*time.Second, "Cache TTL for PVC-to-VMI mapping lookups")

	// Kubernetes configuration
	kubeconfig = flag.String("kubeconfig", "", "Path to kubeconfig file (optional, uses in-cluster config if not specified)")

	// Metrics configuration
	metricsAddr = flag.String("metrics-address", ":9809", "Address for Prometheus metrics endpoint (empty to disable)")

	// Version flag
	version = flag.Bool("version", false, "Print version and exit")

	// Linux ZoL/nvmet preflight flags. This mode is read-only and exits before
	// starting CSI services.
	linuxZOLPreflight             = flag.Bool("linux-zol-preflight", false, "Run read-only Linux ZoL/nvmet host preflight checks and exit")
	linuxZOLPreflightExecutor     = flag.String("linux-zol-preflight-executor", storagebackend.ExecutorHostNamespace, "Linux ZoL preflight executor: local, host-namespace, or ssh")
	linuxZOLPreflightZFSPool      = flag.String("linux-zol-preflight-zfs-pool", "", "ZFS pool/dataset for Linux ZoL preflight")
	linuxZOLPreflightTarget       = flag.String("linux-zol-preflight-target-address", "", "NVMe/TCP target address for Linux ZoL preflight")
	linuxZOLPreflightPort         = flag.Int("linux-zol-preflight-nvme-port", 4420, "NVMe/TCP port for Linux ZoL preflight")
	linuxZOLPreflightMinMTU       = flag.Int("linux-zol-preflight-min-mtu", 0, "Minimum MTU required on the target-address interface for Linux ZoL preflight (0 disables)")
	linuxZOLPreflightSSHTarget    = flag.String("linux-zol-preflight-ssh-target", "", "SSH target for Linux ZoL preflight when executor=ssh")
	linuxZOLPreflightDeviceRoot   = flag.String("linux-zol-preflight-device-root", "", "Device root for Linux ZoL preflight")
	linuxZOLPreflightConfigFSRoot = flag.String("linux-zol-preflight-configfs-root", "", "ConfigFS root for Linux ZoL preflight")

	// Linux ZoL/nvmet dry-run plan flags. This mode observes host state, prints
	// the planned reconciliation action and commands, and exits without applying.
	linuxZOLPlan             = flag.Bool("linux-zol-plan", false, "Build a read-only Linux ZoL/nvmet volume reconciliation plan and exit")
	linuxZOLPlanAction       = flag.String("linux-zol-plan-action", "ensure", "Linux ZoL plan action: ensure or delete")
	linuxZOLPlanExecutor     = flag.String("linux-zol-plan-executor", storagebackend.ExecutorHostNamespace, "Linux ZoL plan executor: local, host-namespace, or ssh")
	linuxZOLPlanZFSPool      = flag.String("linux-zol-plan-zfs-pool", "", "ZFS pool/dataset for Linux ZoL plan")
	linuxZOLPlanTarget       = flag.String("linux-zol-plan-target-address", "", "NVMe/TCP target address for Linux ZoL plan")
	linuxZOLPlanPort         = flag.Int("linux-zol-plan-nvme-port", 4420, "NVMe/TCP port for Linux ZoL plan")
	linuxZOLPlanSSHTarget    = flag.String("linux-zol-plan-ssh-target", "", "SSH target for Linux ZoL plan when executor=ssh")
	linuxZOLPlanDeviceRoot   = flag.String("linux-zol-plan-device-root", "", "Device root for Linux ZoL plan")
	linuxZOLPlanConfigFSRoot = flag.String("linux-zol-plan-configfs-root", "", "ConfigFS root for Linux ZoL plan")
	linuxZOLPlanVolumeID     = flag.String("linux-zol-plan-volume-id", "", "Volume ID for Linux ZoL plan")
	linuxZOLPlanSizeBytes    = flag.Int64("linux-zol-plan-size-bytes", 0, "Desired volume size in bytes for linux-zol-plan-action=ensure")
	linuxZOLPlanNQN          = flag.String("linux-zol-plan-nqn", "", "Exact NQN for linux-zol-plan-action=ensure")
	linuxZOLPlanNQNPrefix    = flag.String("linux-zol-plan-nqn-prefix", utils.NQNPrefix, "NQN namespace used to derive an NQN when --linux-zol-plan-nqn is omitted")
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	if *version {
		fmt.Println(driver.DriverName)
		// Version info will be displayed by driver
		os.Exit(0)
	}

	if *linuxZOLPreflight {
		if err := runLinuxZOLPreflight(context.Background()); err != nil {
			klog.Fatalf("Linux ZoL/nvmet preflight failed: %v", err)
		}
		os.Exit(0)
	}
	if *linuxZOLPlan {
		if err := runLinuxZOLPlan(context.Background(), os.Stdout); err != nil {
			klog.Fatalf("Linux ZoL/nvmet plan failed: %v", err)
		}
		os.Exit(0)
	}

	// Validate mode flags
	if !*controllerMode && !*nodeMode {
		klog.Fatal("Must specify at least one of --controller or --node")
	}

	if *nodeMode && *nodeID == "" {
		klog.Fatal("--node-id is required in node mode")
	}

	// Read SSH private key and host key if controller mode
	var privateKey []byte
	var hostKey []byte
	var err error
	if *controllerMode && *rdsAddress != "" {
		privateKey, err = os.ReadFile(*rdsKeyFile)
		if err != nil {
			klog.Fatalf("Failed to read SSH key from %s: %v", *rdsKeyFile, err)
		}
		klog.V(4).Infof("Loaded SSH key from %s", *rdsKeyFile)

		// Enforce host key verification in production
		if *rdsHostKey == "" && !*rdsInsecure {
			klog.Fatal("SECURITY: --rds-host-key is required for production use. Use --rds-insecure-skip-verify ONLY for testing.")
		}

		// Read host key if provided
		if *rdsHostKey != "" {
			hostKey, err = os.ReadFile(*rdsHostKey)
			if err != nil {
				klog.Fatalf("Failed to read SSH host key from %s: %v", *rdsHostKey, err)
			}
			klog.V(4).Infof("Loaded SSH host key from %s", *rdsHostKey)
		} else if *rdsInsecure {
			klog.Warning("SECURITY WARNING: SSH host key verification is disabled. This is INSECURE and should only be used for testing!")
		}
	} else if *controllerMode {
		klog.Info("No RDS address configured; RouterOS/RDS backend will be unavailable unless configured later")
	}
	var snmpCommunity string
	if *rdsSNMPHost != "" || *rdsSNMPCommunityFile != "" {
		if *rdsSNMPHost == "" || *rdsSNMPCommunityFile == "" {
			klog.Fatal("--rds-snmp-host and --rds-snmp-community-file must be set together")
		}
		communityBytes, readErr := os.ReadFile(*rdsSNMPCommunityFile)
		if readErr != nil {
			klog.Fatalf("Failed to read SNMP community file: %v", readErr)
		}
		snmpCommunity = strings.TrimSpace(string(communityBytes))
		if snmpCommunity == "" {
			klog.Fatal("SNMP community file is empty")
		}
	}

	// Create Kubernetes client if needed. The controller uses it for durable
	// backend resolution from PersistentVolume attributes during operations that
	// do not carry StorageClass parameters, such as DeleteVolume and expansion.
	var k8sClient kubernetes.Interface
	if *controllerMode {
		k8sClient, err = createKubernetesClient(*kubeconfig)
		if err != nil {
			klog.Fatalf("Failed to create Kubernetes client: %v", err)
		}
		klog.Info("Kubernetes client initialized")
	}

	// Create Prometheus metrics
	var promMetrics *observability.Metrics
	if *metricsAddr != "" {
		promMetrics = observability.NewMetrics()
		klog.Infof("Prometheus metrics enabled on %s", *metricsAddr)
	}

	// Read managed NQN prefix for node plugin
	managedNQNPrefix := os.Getenv(nvme.EnvManagedNQNPrefix)

	// Create driver configuration
	config := driver.DriverConfig{
		DriverName:                    *driverName,
		NodeID:                        *nodeID,
		RDSAddress:                    *rdsAddress,
		RDSPort:                       *rdsPort,
		RDSUser:                       *rdsUser,
		RDSPrivateKey:                 privateKey,
		RDSHostKey:                    hostKey,
		RDSInsecureSkipVerify:         *rdsInsecure,
		RDSVolumeBasePath:             *rdsVolumeBasePath,
		EnableRDSMonitoring:           *rdsMonitoringEnabled,
		RDSMonitoringSlot:             *rdsMonitoringSlot,
		RDSSNMPHost:                   *rdsSNMPHost,
		RDSSNMPCommunity:              snmpCommunity,
		RDSMutationMinInterval:        *rdsMutationMinInterval,
		RDSMutationBurst:              *rdsMutationBurst,
		RDSMutationTimeout:            *rdsMutationTimeout,
		K8sClient:                     k8sClient,
		Metrics:                       promMetrics,
		EnableOrphanReconciler:        *enableOrphanReconciler,
		OrphanCheckInterval:           *orphanCheckInterval,
		OrphanGracePeriod:             *orphanGracePeriod,
		OrphanDryRun:                  *orphanDryRun,
		OrphanRunOnStart:              *orphanRunOnStart,
		OrphanMinConfirmations:        *orphanMinConfirmations,
		EnableAttachmentReconciler:    true, // Always enable attachment reconciler in controller mode
		AttachmentGracePeriod:         *attachmentGracePeriod,
		AttachmentReconcileInterval:   *attachmentReconcileInterval,
		StaleVolumeAttachmentCleanup:  *staleVACleanup,
		StaleVolumeAttachmentDelete:   !*staleVADryRun,
		StaleVolumeAttachmentMinConfs: *staleVAMinConfirmations,
		EnableExportReconciler:        *enableExportReconciler,
		ExportCheckInterval:           *exportCheckInterval,
		ExportReconcileDryRun:         *exportReconcileDryRun,
		ExportReconcileRunOnStart:     *exportReconcileRunOnStart,
		EnableVMISerialization:        *enableVMISerialization,
		VMICacheTTL:                   *vmiCacheTTL,
		ManagedNQNPrefix:              managedNQNPrefix,
		EnableController:              *controllerMode,
		EnableNode:                    *nodeMode,
	}

	// Cleanup orphaned NVMe connections on node startup only when explicitly
	// enabled. The cleanup can disconnect managed NVMe sessions, so keep it as
	// an auditable remediation action instead of ambient startup behavior.
	if *nodeMode && *nodeOrphanCleanup {
		klog.Infof("Running orphan NVMe connection cleanup on startup (dry_run=%t, max_disconnects=%d)",
			*nodeOrphanCleanupDryRun, *nodeOrphanCleanupMaxDisconnects)

		// Create a connector for cleanup (same as node server uses internally)
		cleanupConnector := nvme.NewConnector()
		cleaner := nvme.NewOrphanCleaner(cleanupConnector, managedNQNPrefix)
		cleaner.SetDryRun(*nodeOrphanCleanupDryRun)
		cleaner.SetMaxDisconnects(*nodeOrphanCleanupMaxDisconnects)

		// Pass metrics to cleaner for recording orphan cleanup
		if promMetrics != nil {
			cleaner.SetMetrics(promMetrics)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := cleaner.CleanupOrphanedConnections(ctx); err != nil {
			// Log warning but don't fail startup - cleanup is best effort
			klog.Warningf("Orphan NVMe cleanup failed (non-fatal): %v", err)
		}
		cancel()
	} else if *nodeMode {
		klog.Info("Startup orphan NVMe connection cleanup disabled")
	}

	// Create driver
	klog.Info("Creating nvmet-csi driver")
	drv, err := driver.NewDriver(config)
	if err != nil {
		klog.Fatalf("Failed to create driver: %v", err)
	}

	// Start metrics HTTP server
	if promMetrics != nil {
		go func() {
			mux := http.NewServeMux()
			mux.Handle("/metrics", promMetrics.Handler())

			klog.Infof("Starting metrics server on %s", *metricsAddr)
			if err := http.ListenAndServe(*metricsAddr, mux); err != nil && err != http.ErrServerClosed {
				klog.Errorf("Metrics server failed: %v", err)
			}
		}()
	}

	// Handle shutdown gracefully with timeout
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start driver in goroutine so we can handle signals
	errChan := make(chan error, 1)
	go func() {
		klog.Infof("Starting driver in modes: controller=%v node=%v", *controllerMode, *nodeMode)
		if err := drv.Run(*endpoint); err != nil {
			errChan <- err
		}
	}()

	// Wait for signal or driver error
	select {
	case sig := <-sigChan:
		klog.Infof("Received signal %s, initiating graceful shutdown", sig)

		// Create shutdown context with timeout
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), ShutdownTimeout)
		defer shutdownCancel()

		if err := drv.ShutdownWithContext(shutdownCtx); err != nil {
			klog.Errorf("Graceful shutdown failed: %v", err)
			os.Exit(1)
		}
		klog.Info("Driver stopped gracefully")
		os.Exit(0)

	case err := <-errChan:
		klog.Fatalf("Driver failed: %v", err)
	}
}

func runLinuxZOLPreflight(ctx context.Context) error {
	if *linuxZOLPreflightZFSPool == "" {
		return fmt.Errorf("--linux-zol-preflight-zfs-pool is required")
	}
	if *linuxZOLPreflightTarget == "" {
		return fmt.Errorf("--linux-zol-preflight-target-address is required")
	}
	if *linuxZOLPreflightPort <= 0 {
		return fmt.Errorf("--linux-zol-preflight-nvme-port must be positive")
	}
	if *linuxZOLPreflightMinMTU < 0 {
		return fmt.Errorf("--linux-zol-preflight-min-mtu must be non-negative")
	}

	host, err := linuxzolbackend.NewCommandHostFromOptions(linuxzolbackend.CommandBackendOptions{
		Config: linuxzolbackend.Config{
			Pool:         *linuxZOLPreflightZFSPool,
			Address:      *linuxZOLPreflightTarget,
			DeviceRoot:   *linuxZOLPreflightDeviceRoot,
			ConfigFSRoot: *linuxZOLPreflightConfigFSRoot,
			MinMTU:       *linuxZOLPreflightMinMTU,
		},
		Executor:  *linuxZOLPreflightExecutor,
		SSHTarget: *linuxZOLPreflightSSHTarget,
	})
	if err != nil {
		return err
	}

	checks := host.Preflight(ctx, *linuxZOLPreflightPort)
	for _, check := range checks {
		status := "ok"
		if !check.OK {
			status = "fail"
		}
		fmt.Printf("%s\t%s\t%s\n", status, check.Name, check.Detail)
	}
	if !linuxzolbackend.PreflightChecksOK(checks) {
		return fmt.Errorf("one or more checks failed")
	}
	return nil
}

type linuxZOLPlanOptions struct {
	Action       string
	Executor     string
	ZFSPool      string
	Target       string
	Port         int
	SSHTarget    string
	DeviceRoot   string
	ConfigFSRoot string
	VolumeID     string
	SizeBytes    int64
	NQN          string
	NQNPrefix    string
}

func runLinuxZOLPlan(ctx context.Context, out io.Writer) error {
	opts := linuxZOLPlanOptions{
		Action:       *linuxZOLPlanAction,
		Executor:     *linuxZOLPlanExecutor,
		ZFSPool:      *linuxZOLPlanZFSPool,
		Target:       *linuxZOLPlanTarget,
		Port:         *linuxZOLPlanPort,
		SSHTarget:    *linuxZOLPlanSSHTarget,
		DeviceRoot:   *linuxZOLPlanDeviceRoot,
		ConfigFSRoot: *linuxZOLPlanConfigFSRoot,
		VolumeID:     *linuxZOLPlanVolumeID,
		SizeBytes:    *linuxZOLPlanSizeBytes,
		NQN:          *linuxZOLPlanNQN,
		NQNPrefix:    *linuxZOLPlanNQNPrefix,
	}
	return runLinuxZOLPlanWithOptions(ctx, out, opts)
}

func runLinuxZOLPlanWithOptions(ctx context.Context, out io.Writer, opts linuxZOLPlanOptions) error {
	if err := opts.validate(); err != nil {
		return err
	}

	storageBackend, err := linuxzolbackend.NewCommandBackend(linuxzolbackend.CommandBackendOptions{
		Config: linuxzolbackend.Config{
			Pool:         opts.ZFSPool,
			Address:      opts.Target,
			DeviceRoot:   opts.DeviceRoot,
			ConfigFSRoot: opts.ConfigFSRoot,
		},
		Executor:  opts.Executor,
		SSHTarget: opts.SSHTarget,
	})
	if err != nil {
		return err
	}

	var plan *storagebackend.VolumePlan
	var planErr error
	switch opts.Action {
	case "ensure":
		nqn, nqnErr := resolveLinuxZOLPlanNQN(opts.VolumeID, opts.NQN, opts.NQNPrefix)
		if nqnErr != nil {
			return nqnErr
		}
		plan, planErr = storageBackend.BuildEnsureVolumePlan(ctx, storagebackend.CreateVolumeRequest{
			ID:          opts.VolumeID,
			SizeBytes:   opts.SizeBytes,
			NVMETCPPort: opts.Port,
			NVMETCPNQN:  nqn,
			Parameters: map[string]string{
				"backend":       storagebackend.TypeLinuxZOLNVMET,
				"executor":      opts.Executor,
				"zfsPool":       opts.ZFSPool,
				"targetAddress": opts.Target,
			},
		})
	case "delete":
		plan, planErr = storageBackend.BuildDeleteVolumePlan(ctx, opts.VolumeID)
	default:
		return fmt.Errorf("--linux-zol-plan-action must be ensure or delete")
	}
	if planErr != nil {
		return planErr
	}

	return renderLinuxZOLPlan(out, plan)
}

func (o linuxZOLPlanOptions) validate() error {
	if o.ZFSPool == "" {
		return fmt.Errorf("--linux-zol-plan-zfs-pool is required")
	}
	if o.Target == "" {
		return fmt.Errorf("--linux-zol-plan-target-address is required")
	}
	if o.Port <= 0 {
		return fmt.Errorf("--linux-zol-plan-nvme-port must be positive")
	}
	if o.VolumeID == "" {
		return fmt.Errorf("--linux-zol-plan-volume-id is required")
	}
	if err := utils.ValidateVolumeID(o.VolumeID); err != nil {
		return fmt.Errorf("invalid --linux-zol-plan-volume-id: %w", err)
	}
	switch o.Action {
	case "ensure":
		if o.SizeBytes <= 0 {
			return fmt.Errorf("--linux-zol-plan-size-bytes must be positive for ensure plans")
		}
	case "delete":
	default:
		return fmt.Errorf("--linux-zol-plan-action must be ensure or delete")
	}
	return nil
}

func resolveLinuxZOLPlanNQN(volumeID, exactNQN, nqnPrefix string) (string, error) {
	if exactNQN != "" {
		if err := utils.ValidateNQN(exactNQN); err != nil {
			return "", err
		}
		return exactNQN, nil
	}
	return utils.VolumeIDToNQNWithPrefix(volumeID, nqnPrefix)
}

func renderLinuxZOLPlan(out io.Writer, plan *storagebackend.VolumePlan) error {
	if plan == nil {
		return nil
	}
	if _, err := fmt.Fprintf(out, "backend\t%s\n", plan.Backend); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "action\t%s\n", plan.Action); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "volume_id\t%s\n", plan.VolumeID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "reason\t%s\n", plan.Reason); err != nil {
		return err
	}
	if plan.Desired != nil {
		if _, err := fmt.Fprintf(out, "desired_path\t%s\n", plan.Desired.Path); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "desired_size_bytes\t%d\n", plan.Desired.SizeBytes); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "desired_nqn\t%s\n", plan.Desired.NVMETCPNQN); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "desired_port\t%d\n", plan.Desired.NVMETCPPort); err != nil {
			return err
		}
	}
	if plan.Actual != nil {
		if _, err := fmt.Fprintf(out, "actual_path\t%s\n", plan.Actual.Path); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "actual_size_bytes\t%d\n", plan.Actual.SizeBytes); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "actual_nqn\t%s\n", plan.Actual.NVMETCPNQN); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "actual_port\t%d\n", plan.Actual.NVMETCPPort); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(out, "actual_status\t%s\n", plan.Actual.Status); err != nil {
			return err
		}
	}
	if len(plan.Commands) == 0 {
		_, err := fmt.Fprintln(out, "commands\t")
		return err
	}
	for _, command := range plan.Commands {
		if _, err := fmt.Fprintf(out, "command\t%s\n", command); err != nil {
			return err
		}
	}
	return nil
}

// createKubernetesClient creates a Kubernetes client using in-cluster config or kubeconfig file
func createKubernetesClient(kubeconfigPath string) (kubernetes.Interface, error) {
	var config *rest.Config
	var err error

	if kubeconfigPath != "" {
		// Use kubeconfig file
		klog.V(2).Infof("Using kubeconfig file: %s", kubeconfigPath)
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
		if err != nil {
			return nil, fmt.Errorf("failed to build config from kubeconfig: %w", err)
		}
	} else {
		// Use in-cluster config
		klog.V(2).Info("Using in-cluster Kubernetes config")
		config, err = rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to get in-cluster config: %w", err)
		}
	}

	// Create the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	return clientset, nil
}
