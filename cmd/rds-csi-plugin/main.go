package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"git.srvlab.io/whiskey/rds-csi-driver/pkg/driver"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/nvme"
	"git.srvlab.io/whiskey/rds-csi-driver/pkg/observability"
)

const (
	// ShutdownTimeout is the maximum time to wait for graceful shutdown
	ShutdownTimeout = 30 * time.Second
)

var (
	// Driver configuration
	endpoint   = flag.String("endpoint", "unix:///var/lib/kubelet/plugins/rds.csi.srvlab.io/csi.sock", "CSI endpoint")
	nodeID     = flag.String("node-id", "", "Node ID (required for node service)")
	driverName = flag.String("driver-name", "rds.csi.srvlab.io", "Name of the CSI driver")

	// RDS configuration
	rdsAddress        = flag.String("rds-address", "", "RDS server IP address (required for controller)")
	rdsPort           = flag.Int("rds-port", 22, "RDS SSH port")
	rdsUser           = flag.String("rds-user", "admin", "RDS SSH user")
	rdsKeyFile        = flag.String("rds-key-file", "/etc/rds-csi/ssh-key/id_rsa", "Path to RDS SSH private key")
	rdsHostKey        = flag.String("rds-host-key", "", "Path to RDS SSH host public key (required for secure verification)")
	rdsInsecure       = flag.Bool("rds-insecure-skip-verify", false, "Skip SSH host key verification (INSECURE - for testing only)")
	rdsVolumeBasePath = flag.String("rds-volume-base-path", "", "Base path for volumes on RDS (e.g., /storage-pool/metal-csi, required for file orphan detection)")

	// RDS mutation guardrails
	rdsMutationMinInterval = flag.Duration("rds-mutation-min-interval", 250*time.Millisecond, "Minimum spacing between mutating RDS operations (0 disables guard)")
	rdsMutationBurst       = flag.Int("rds-mutation-burst", 1, "Maximum RDS mutations allowed to burst through the guard")
	rdsMutationTimeout     = flag.Duration("rds-mutation-timeout", 30*time.Second, "Maximum time to wait for the RDS mutation guard")

	// Mode flags
	controllerMode = flag.Bool("controller", false, "Run in controller mode")
	nodeMode       = flag.Bool("node", false, "Run in node mode")

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

	// VMI serialization flags (kubevirt concurrent operation mitigation)
	enableVMISerialization = flag.Bool("enable-vmi-serialization", false, "Enable per-VMI operation serialization to mitigate kubevirt concurrency issues")
	vmiCacheTTL            = flag.Duration("vmi-cache-ttl", 60*time.Second, "Cache TTL for PVC-to-VMI mapping lookups")

	// Kubernetes configuration
	kubeconfig = flag.String("kubeconfig", "", "Path to kubeconfig file (optional, uses in-cluster config if not specified)")

	// Metrics configuration
	metricsAddr = flag.String("metrics-address", ":9809", "Address for Prometheus metrics endpoint (empty to disable)")

	// Version flag
	version = flag.Bool("version", false, "Print version and exit")
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	if *version {
		fmt.Println(driver.DriverName)
		// Version info will be displayed by driver
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
		DriverName:                  *driverName,
		NodeID:                      *nodeID,
		RDSAddress:                  *rdsAddress,
		RDSPort:                     *rdsPort,
		RDSUser:                     *rdsUser,
		RDSPrivateKey:               privateKey,
		RDSHostKey:                  hostKey,
		RDSInsecureSkipVerify:       *rdsInsecure,
		RDSVolumeBasePath:           *rdsVolumeBasePath,
		RDSMutationMinInterval:      *rdsMutationMinInterval,
		RDSMutationBurst:            *rdsMutationBurst,
		RDSMutationTimeout:          *rdsMutationTimeout,
		K8sClient:                   k8sClient,
		Metrics:                     promMetrics,
		EnableOrphanReconciler:      *enableOrphanReconciler,
		OrphanCheckInterval:         *orphanCheckInterval,
		OrphanGracePeriod:           *orphanGracePeriod,
		OrphanDryRun:                *orphanDryRun,
		OrphanRunOnStart:            *orphanRunOnStart,
		OrphanMinConfirmations:      *orphanMinConfirmations,
		EnableAttachmentReconciler:  true, // Always enable attachment reconciler in controller mode
		AttachmentGracePeriod:       *attachmentGracePeriod,
		AttachmentReconcileInterval: *attachmentReconcileInterval,
		EnableVMISerialization:      *enableVMISerialization,
		VMICacheTTL:                 *vmiCacheTTL,
		ManagedNQNPrefix:            managedNQNPrefix,
		EnableController:            *controllerMode,
		EnableNode:                  *nodeMode,
	}

	// Cleanup orphaned NVMe connections on node startup
	// This prevents accumulation of orphaned connections after node restarts
	if *nodeMode {
		klog.Info("Running orphan NVMe connection cleanup on startup")

		// Create a connector for cleanup (same as node server uses internally)
		cleanupConnector := nvme.NewConnector()
		cleaner := nvme.NewOrphanCleaner(cleanupConnector, managedNQNPrefix)

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
	}

	// Create driver
	klog.Info("Creating RDS CSI driver")
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
