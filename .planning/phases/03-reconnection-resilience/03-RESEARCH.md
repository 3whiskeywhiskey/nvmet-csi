# Phase 3: Reconnection Resilience - Research

**Researched:** 2026-01-30
**Domain:** NVMe/TCP kernel reconnection parameters, exponential backoff algorithms, Kubernetes StorageClass configuration, orphaned connection cleanup
**Confidence:** HIGH

## Summary

This phase implements connection resilience for NVMe/TCP volumes by setting kernel-level reconnection parameters during connection, implementing exponential backoff with jitter for retry operations, and cleaning up orphaned connections. The research confirms that NVMe/TCP kernel parameters (ctrl_loss_tmo, reconnect_delay) can be set either at connection time via nvme-cli flags or post-connection via sysfs. StorageClass parameters flow through VolumeContext to node operations, enabling user configuration.

Key findings:
1. **NVMe/TCP kernel parameters are well-documented** - ctrl_loss_tmo defaults to 600s, reconnect_delay controls retry interval, both settable via nvme connect flags or sysfs
2. **Production best practice: ctrl_loss_tmo=-1** - Unlimited reconnection prevents filesystem read-only mount after timeout
3. **Exponential backoff with jitter is standard** - k8s.io/apimachinery/pkg/util/wait provides Backoff with jitter support, already available in dependencies
4. **StorageClass parameters flow through VolumeContext** - Controller returns parameters in CreateVolumeResponse.Volume.VolumeContext, which is passed to NodeStageVolume
5. **Orphaned connection detection uses existing resolver** - Phase 1's DeviceResolver.IsOrphanedSubsystem() already detects subsystems without namespaces

**Primary recommendation:** Set ctrl_loss_tmo=-1 and reconnect_delay=5 by default (configurable via StorageClass), use k8s.io/apimachinery/pkg/util/wait.Backoff for retry logic, pass configuration through VolumeContext, and implement startup reconciliation using existing orphan detection.

## Standard Stack

The established libraries/tools for this domain:

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| `nvme-cli` | system | Set connection parameters via flags | Standard NVMe management tool, --ctrl-loss-tmo and --reconnect-delay flags |
| `k8s.io/apimachinery/pkg/util/wait` | v0.28.0 (via k8s.io/client-go) | Exponential backoff with jitter | Already in dependencies, standard K8s retry mechanism, includes Backoff struct with jitter |
| `k8s.io/client-go` | v0.28.0 | Already in go.mod | No new dependency needed |
| Go stdlib `os` | Go 1.24 | Write sysfs parameters | Standard for file I/O |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| `context` | Go 1.24 | Timeout and cancellation | All retry operations should respect context |
| `time` | Go 1.24 | Duration and timeout calculations | Backoff timing |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| k8s.io/apimachinery/pkg/util/wait | github.com/cenkalti/backoff/v4 | cenkalti/backoff is popular but adds dependency; k8s wait package is already available |
| nvme connect flags | Post-connection sysfs writes | Flags are simpler and atomic; sysfs writes require finding correct controller path |
| Default unlimited retry | User-configurable timeout | Unlimited (-1) prevents filesystem read-only but may mask network issues; make configurable |

**No new dependencies required.** All needed functionality is in existing dependencies or standard library.

## Architecture Patterns

### Recommended Project Structure
```
pkg/
├── nvme/
│   ├── nvme.go              # MODIFY: Add connection parameter support
│   ├── nvme_test.go         # MODIFY: Test parameter passing
│   ├── config.go            # NEW: NVMe connection configuration
│   └── config_test.go       # NEW: Configuration tests
├── driver/
│   ├── node.go              # MODIFY: Pass VolumeContext parameters to nvme.Connector
│   ├── controller.go        # MODIFY: Include NVMe parameters in VolumeContext
│   └── params.go            # NEW: StorageClass parameter parsing and defaults
├── utils/
│   └── retry.go             # NEW: Retry utilities with exponential backoff + jitter
```

### Pattern 1: NVMe Connection with Kernel Parameters
**What:** Set ctrl_loss_tmo and reconnect_delay when establishing NVMe/TCP connection.
**When to use:** Every nvme connect operation in Connector.ConnectWithContext.
**Example:**
```go
// Source: nvme-cli documentation and SLES NVMe-oF guide
type ConnectionConfig struct {
    // CtrlLossTmo is the controller loss timeout in seconds
    // -1 = unlimited (recommended for production)
    // 600 = default kernel value
    CtrlLossTmo int

    // ReconnectDelay is the delay between reconnect attempts in seconds
    // Default: 10 (kernel default)
    ReconnectDelay int

    // KeepAliveTmo is the keep-alive timeout in seconds
    // Optional, kernel handles default
    KeepAliveTmo int
}

func DefaultConnectionConfig() ConnectionConfig {
    return ConnectionConfig{
        CtrlLossTmo:    -1, // Unlimited reconnection
        ReconnectDelay: 5,  // 5 second retry interval
        KeepAliveTmo:   0,  // Use kernel default
    }
}

func (c *connector) buildConnectArgs(target Target, config ConnectionConfig) []string {
    args := []string{
        "connect",
        "-t", target.Transport,
        "-a", target.TargetAddress,
        "-s", fmt.Sprintf("%d", target.TargetPort),
        "-n", target.NQN,
    }

    // Add connection resilience parameters
    if config.CtrlLossTmo != 0 {
        args = append(args, "-l", fmt.Sprintf("%d", config.CtrlLossTmo))
    }

    if config.ReconnectDelay > 0 {
        args = append(args, "-c", fmt.Sprintf("%d", config.ReconnectDelay))
    }

    if config.KeepAliveTmo > 0 {
        args = append(args, "-k", fmt.Sprintf("%d", config.KeepAliveTmo))
    }

    if target.HostNQN != "" {
        args = append(args, "-q", target.HostNQN)
    }

    return args
}
```

### Pattern 2: StorageClass Parameter Parsing
**What:** Parse StorageClass parameters with defaults and validation.
**When to use:** Controller CreateVolume to build VolumeContext.
**Example:**
```go
// Source: CSI spec and Kubernetes StorageClass documentation
const (
    paramCtrlLossTmo    = "ctrlLossTmo"
    paramReconnectDelay = "reconnectDelay"
    paramKeepAliveTmo   = "keepAliveTmo"
)

type NVMEConnectionParams struct {
    CtrlLossTmo    int
    ReconnectDelay int
    KeepAliveTmo   int
}

func ParseNVMEConnectionParams(params map[string]string) (NVMEConnectionParams, error) {
    // Start with defaults
    config := NVMEConnectionParams{
        CtrlLossTmo:    -1, // Unlimited
        ReconnectDelay: 5,  // 5 seconds
        KeepAliveTmo:   0,  // Kernel default
    }

    // Parse ctrl_loss_tmo
    if val, ok := params[paramCtrlLossTmo]; ok {
        parsed, err := strconv.Atoi(val)
        if err != nil {
            return config, fmt.Errorf("invalid %s: %w", paramCtrlLossTmo, err)
        }
        // -1 = unlimited, >0 = timeout in seconds
        if parsed < -1 {
            return config, fmt.Errorf("%s must be -1 (unlimited) or positive", paramCtrlLossTmo)
        }
        config.CtrlLossTmo = parsed
    }

    // Parse reconnect_delay
    if val, ok := params[paramReconnectDelay]; ok {
        parsed, err := strconv.Atoi(val)
        if err != nil {
            return config, fmt.Errorf("invalid %s: %w", paramReconnectDelay, err)
        }
        if parsed < 1 {
            return config, fmt.Errorf("%s must be positive", paramReconnectDelay)
        }
        config.ReconnectDelay = parsed
    }

    return config, nil
}

// In controller.go CreateVolume:
func (cs *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
    // ... existing volume creation ...

    // Parse NVMe connection parameters
    nvmeParams, err := ParseNVMEConnectionParams(req.GetParameters())
    if err != nil {
        return nil, status.Errorf(codes.InvalidArgument, "invalid NVMe parameters: %v", err)
    }

    // Include in VolumeContext for NodeStageVolume
    volumeContext := map[string]string{
        "rdsAddress":     cs.getRDSAddress(params),
        "nvmeAddress":    cs.getNVMEAddress(params),
        "nvmePort":       fmt.Sprintf("%d", nvmePort),
        "nqn":            nqn,
        "ctrlLossTmo":    fmt.Sprintf("%d", nvmeParams.CtrlLossTmo),
        "reconnectDelay": fmt.Sprintf("%d", nvmeParams.ReconnectDelay),
    }

    return &csi.CreateVolumeResponse{
        Volume: &csi.Volume{
            VolumeId:      volumeID,
            CapacityBytes: requiredBytes,
            VolumeContext: volumeContext,
        },
    }, nil
}
```

### Pattern 3: Exponential Backoff with Jitter
**What:** Use k8s.io/apimachinery/pkg/util/wait.Backoff for retry operations.
**When to use:** Any operation that might fail transiently (NVMe connect, SSH operations).
**Example:**
```go
// Source: k8s.io/apimachinery/pkg/util/wait documentation
import (
    "k8s.io/apimachinery/pkg/util/wait"
)

// Recommended backoff configuration to prevent thundering herd
func DefaultBackoffConfig() wait.Backoff {
    return wait.Backoff{
        Steps:    5,              // Maximum attempts
        Duration: 1 * time.Second, // Initial duration
        Factor:   2.0,            // Exponential factor (2x)
        Jitter:   0.1,            // 10% jitter to prevent thundering herd
    }
}

// Example: Retry NVMe connection with exponential backoff
func (c *connector) ConnectWithRetry(ctx context.Context, target Target, config ConnectionConfig) (string, error) {
    backoff := DefaultBackoffConfig()

    var lastErr error
    var devicePath string

    err := wait.ExponentialBackoffWithContext(ctx, backoff, func() (bool, error) {
        devicePath, lastErr = c.ConnectWithContext(ctx, target)
        if lastErr == nil {
            return true, nil // Success, stop retrying
        }

        // Check if error is retryable
        if isRetryable(lastErr) {
            klog.V(3).Infof("Connect attempt failed (retryable): %v", lastErr)
            return false, nil // Retry
        }

        // Non-retryable error, stop immediately
        return false, lastErr
    })

    if err != nil {
        return "", fmt.Errorf("connect failed after retries: %w (last error: %v)", err, lastErr)
    }

    return devicePath, nil
}

// Determine if error is retryable
func isRetryable(err error) bool {
    if err == nil {
        return false
    }

    errStr := err.Error()

    // Retryable: temporary network issues
    retryablePatterns := []string{
        "connection refused",
        "connection timeout",
        "no route to host",
        "network unreachable",
        "device did not appear",
    }

    for _, pattern := range retryablePatterns {
        if strings.Contains(strings.ToLower(errStr), pattern) {
            return true
        }
    }

    return false
}
```

### Pattern 4: Orphaned Connection Cleanup on Startup
**What:** Detect and disconnect orphaned NVMe connections during node plugin initialization.
**When to use:** Node plugin startup (in main.go or node service initialization).
**Example:**
```go
// Source: Phase 1's DeviceResolver.IsOrphanedSubsystem pattern
type OrphanCleaner struct {
    connector Connector
    resolver  *nvme.DeviceResolver
}

func NewOrphanCleaner(connector Connector, resolver *nvme.DeviceResolver) *OrphanCleaner {
    return &OrphanCleaner{
        connector: connector,
        resolver:  resolver,
    }
}

// CleanupOrphanedConnections finds and disconnects orphaned NVMe subsystems
// An orphaned subsystem is one that is connected but has no accessible namespaces
func (o *OrphanCleaner) CleanupOrphanedConnections(ctx context.Context) error {
    klog.V(2).Info("Scanning for orphaned NVMe connections")

    // Get all connected subsystems
    subsystems, err := o.resolver.ListSubsystems()
    if err != nil {
        return fmt.Errorf("failed to list subsystems: %w", err)
    }

    orphanCount := 0
    for _, subsystem := range subsystems {
        // Check if subsystem is orphaned
        orphaned, err := o.resolver.IsOrphanedSubsystem(subsystem.NQN)
        if err != nil {
            klog.Warningf("Failed to check orphan status for %s: %v", subsystem.NQN, err)
            continue
        }

        if orphaned {
            klog.Warningf("Found orphaned subsystem: %s", subsystem.NQN)
            orphanCount++

            // Attempt to disconnect
            if err := o.connector.DisconnectWithContext(ctx, subsystem.NQN); err != nil {
                klog.Errorf("Failed to disconnect orphaned subsystem %s: %v", subsystem.NQN, err)
            } else {
                klog.V(2).Infof("Successfully disconnected orphaned subsystem %s", subsystem.NQN)
            }
        }
    }

    if orphanCount > 0 {
        klog.Infof("Cleaned up %d orphaned NVMe connections", orphanCount)
    } else {
        klog.V(2).Info("No orphaned NVMe connections found")
    }

    return nil
}

// In node plugin startup (cmd/rds-csi-plugin/main.go):
func runNodeService(driver *driver.Driver) {
    // ... existing initialization ...

    // Cleanup orphaned connections on startup
    cleaner := NewOrphanCleaner(driver.nvmeConnector, driver.nvmeConnector.GetResolver())
    ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
    defer cancel()

    if err := cleaner.CleanupOrphanedConnections(ctx); err != nil {
        klog.Warningf("Orphan cleanup failed (non-fatal): %v", err)
    }

    // ... start gRPC server ...
}
```

### Pattern 5: VolumeContext Parameter Extraction
**What:** Extract and validate NVMe connection parameters from VolumeContext in node operations.
**When to use:** NodeStageVolume to configure NVMe connection.
**Example:**
```go
// Source: CSI spec VolumeContext flow
func (ns *NodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
    // ... existing validation ...

    volumeContext := req.GetVolumeContext()

    // Parse NVMe connection parameters from VolumeContext
    connConfig := nvme.DefaultConnectionConfig()

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

    // Build target with configuration
    target := nvme.Target{
        Transport:     "tcp",
        NQN:           volumeContext["nqn"],
        TargetAddress: volumeContext["nvmeAddress"],
        TargetPort:    nvmePort,
    }

    // Connect with resilience parameters
    devicePath, err := ns.connector.ConnectWithConfig(ctx, target, connConfig)
    if err != nil {
        return nil, status.Errorf(codes.Internal, "failed to connect: %v", err)
    }

    // ... continue with staging ...
}
```

### Anti-Patterns to Avoid
- **Setting parameters via sysfs after connection:** More complex than using nvme connect flags, requires finding correct controller path, race conditions
- **Zero jitter in backoff:** Multiple nodes retrying simultaneously creates thundering herd problem
- **Unlimited retries without context:** Respect context cancellation, avoid hanging operations
- **Silent orphan cleanup:** Log all cleanup actions for operator visibility
- **Hardcoded connection parameters:** Make configurable via StorageClass for different workload requirements

## Don't Hand-Roll

Problems that look simple but have existing solutions:

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Exponential backoff with jitter | Custom backoff calculator | `k8s.io/apimachinery/pkg/util/wait.Backoff` | Handles edge cases, jitter calculation, context cancellation, already in dependencies |
| StorageClass parameter flow | Custom parameter passing | CSI VolumeContext field | Standard CSI pattern, controller sets in CreateVolumeResponse, node receives in NodeStageVolume |
| Retry predicate logic | Custom condition function | `wait.ExponentialBackoffWithContext` | Built-in support for condition functions, context handling, proper backoff calculation |
| Connection parameter defaults | Environment variables | StorageClass parameters with defaults in code | User-configurable per StorageClass, better UX, follows K8s patterns |

**Key insight:** Kubernetes has well-established patterns for retry logic and parameter passing. Use them instead of reinventing.

## Common Pitfalls

### Pitfall 1: Not Respecting ctrl_loss_tmo=-1 Semantics
**What goes wrong:** Setting ctrl_loss_tmo=-1 but expecting finite timeout behavior in application logic.
**Why it happens:** -1 means "unlimited", kernel will retry indefinitely, application must handle this.
**How to avoid:**
1. Understand -1 means kernel never gives up on connection
2. If application needs timeout, implement at higher level (context deadline)
3. Document that -1 prevents filesystem read-only mount after timeout
**Warning signs:** Application hangs waiting for connection that will never fail at kernel level.

### Pitfall 2: Thundering Herd on Node Restarts
**What goes wrong:** All pods on a node try to reconnect simultaneously after node restart, overwhelming storage target.
**Why it happens:** No jitter in retry logic, all operations start at same time.
**How to avoid:**
1. Always use jitter in backoff (wait.Backoff.Jitter = 0.1 or higher)
2. Consider adding startup delay variance across node plugin instances
3. Monitor storage target connection rate
**Warning signs:** Storage target connection storms after node restarts, sporadic connection failures.

### Pitfall 3: Orphan Cleanup During Active Operations
**What goes wrong:** Orphan cleanup disconnects a subsystem that another goroutine is trying to use.
**Why it happens:** Race between cleanup and new connection operations.
**How to avoid:**
1. Run orphan cleanup only at startup, before serving CSI requests
2. Use connection tracking to prevent cleanup of recently used connections
3. Add grace period (e.g., don't clean up connections established < 5 minutes ago)
**Warning signs:** Intermittent "connection not found" errors during normal operations.

### Pitfall 4: Invalid Parameter Validation
**What goes wrong:** User sets ctrlLossTmo=0 or reconnectDelay=-5, causing unexpected kernel behavior.
**Why it happens:** Missing validation in StorageClass parameter parsing.
**How to avoid:**
1. Validate all numeric parameters: ctrl_loss_tmo >= -1, reconnect_delay > 0
2. Return clear CSI error codes (InvalidArgument) for bad parameters
3. Document valid ranges in storage class examples
**Warning signs:** nvme connect fails with cryptic kernel errors due to invalid parameter values.

### Pitfall 5: Not Updating Config on Already-Connected Subsystems
**What goes wrong:** User changes StorageClass parameters, but existing connections use old parameters.
**Why it happens:** Parameters are only set at connection time via nvme connect flags.
**How to avoid:**
1. Document that parameter changes only affect new volumes
2. Consider supporting parameter updates via sysfs write for advanced users
3. Or: implement reconnection logic that disconnects and reconnects with new parameters
**Warning signs:** User confusion about why parameter changes don't affect existing volumes.

### Pitfall 6: Testing Without Time Mocking
**What goes wrong:** Unit tests that exercise retry logic take minutes to run, become impractical.
**Why it happens:** Tests use real time.Sleep() for backoff delays.
**How to avoid:**
1. Use dependency injection for time-related functions
2. Pass custom wait.Backoff with Duration=1ms for testing
3. Mock context deadlines in tests
**Warning signs:** Test suite runtime grows linearly with backoff durations.

## Code Examples

Verified patterns from official sources:

### NVMe Connect with Connection Parameters
```go
// Source: nvme-cli documentation, SLES NVMe-oF guide
func connectWithParameters(target, ctrlLossTmo, reconnectDelay int) error {
    args := []string{
        "connect",
        "-t", "tcp",
        "-a", "198.51.100.20",
        "-s", "4420",
        "-n", "nqn.2000-02.com.mikrotik:pvc-abc123",
        "-l", fmt.Sprintf("%d", ctrlLossTmo),    // Controller loss timeout
        "-c", fmt.Sprintf("%d", reconnectDelay), // Reconnect delay
    }

    cmd := exec.Command("nvme", args...)
    return cmd.Run()
}

// Production best practice: unlimited reconnection
// nvme connect -t tcp -a 198.51.100.20 -s 4420 -n nqn.2000-02.com.mikrotik:pvc-abc123 -l -1 -c 5
```

### Exponential Backoff with wait.Backoff
```go
// Source: k8s.io/apimachinery/pkg/util/wait documentation
import "k8s.io/apimachinery/pkg/util/wait"

func retryOperation(ctx context.Context) error {
    backoff := wait.Backoff{
        Steps:    5,                // Try 5 times
        Duration: 1 * time.Second,  // Start with 1s
        Factor:   2.0,              // Double each time (1s, 2s, 4s, 8s, 16s)
        Jitter:   0.1,              // Add 10% random jitter
    }

    return wait.ExponentialBackoffWithContext(ctx, backoff, func() (bool, error) {
        err := doOperation()
        if err == nil {
            return true, nil // Success
        }

        if isRetryable(err) {
            return false, nil // Retry
        }

        return false, err // Fatal error, stop
    })
}
```

### StorageClass with NVMe Parameters
```yaml
# Source: Kubernetes StorageClass documentation and CSI best practices
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: rds-nvme-resilient
provisioner: rds.csi.srvlab.io
parameters:
  rdsAddress: "198.51.100.20"
  nvmeAddress: "198.51.100.20"
  nvmePort: "4420"
  volumePath: "/storage-pool/kubernetes-volumes"

  # Connection resilience parameters
  ctrlLossTmo: "-1"      # Unlimited reconnection (recommended)
  reconnectDelay: "5"    # 5 seconds between retry attempts
  keepAliveTmo: "30"     # Optional: keep-alive timeout

volumeBindingMode: WaitForFirstConsumer
reclaimPolicy: Delete
```

### Parsing VolumeContext in NodeStageVolume
```go
// Source: CSI spec NodeStageVolume
func (ns *NodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
    volumeContext := req.GetVolumeContext()

    // Extract connection parameters with defaults
    ctrlLossTmo := -1 // Default: unlimited
    if val, ok := volumeContext["ctrlLossTmo"]; ok {
        if parsed, err := strconv.Atoi(val); err == nil {
            ctrlLossTmo = parsed
        }
    }

    reconnectDelay := 5 // Default: 5 seconds
    if val, ok := volumeContext["reconnectDelay"]; ok {
        if parsed, err := strconv.Atoi(val); err == nil {
            reconnectDelay = parsed
        }
    }

    // Use parameters in NVMe connection
    config := nvme.ConnectionConfig{
        CtrlLossTmo:    ctrlLossTmo,
        ReconnectDelay: reconnectDelay,
    }

    // ... connect with config ...
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Default ctrl_loss_tmo=600 | Set ctrl_loss_tmo=-1 for production | 2020+ best practices | Prevents filesystem read-only mount after timeout |
| No jitter in retry | Exponential backoff with jitter | 2018+ (AWS article) | Prevents thundering herd in distributed systems |
| Hardcoded connection parameters | StorageClass-configurable | CSI 1.0+ pattern | User control over resilience vs. failover speed tradeoff |
| Manual orphan cleanup | Automated startup reconciliation | CSI driver maturity pattern | Reduces operator toil, prevents resource leaks |

**Deprecated/outdated:**
- Manual sysfs parameter writing: Use nvme connect flags instead, simpler and atomic
- Zero jitter backoff: Causes thundering herd, always use jitter in distributed systems
- Fixed 600s timeout: Too short for some failure scenarios, use -1 or user-configurable

## Open Questions

Things that couldn't be fully resolved:

1. **Kernel Default Values**
   - What we know: ctrl_loss_tmo default is 600s (from SLES docs), reconnect_delay default exists but exact value not documented
   - What's unclear: Exact reconnect_delay default value (likely 10s based on references), keep_alive_tmo default
   - Recommendation: Test on target kernel version (NixOS), observe defaults in /sys/class/nvme/nvme*/ctrl_loss_tmo

2. **Orphan Cleanup Timing**
   - What we know: Phase 1's DeviceResolver.IsOrphanedSubsystem() detects orphans, should run at startup
   - What's unclear: Should cleanup be periodic (background loop) or one-time at startup?
   - Recommendation: Start with one-time startup cleanup, add periodic cleanup if operators report orphan accumulation in production

3. **Parameter Update Strategy**
   - What we know: nvme connect flags only apply at connection time, sysfs can update live connections
   - What's unclear: Should driver support updating parameters on existing connections? Disconnect and reconnect?
   - Recommendation: Document that parameter changes only affect new volumes (simplest), defer live update to future phase if users request it

4. **Backoff Jitter Amount**
   - What we know: Jitter prevents thundering herd, k8s.io/client-go uses 0.1 (10%)
   - What's unclear: Optimal jitter percentage for NVMe connection storms
   - Recommendation: Use 0.1 (10%) to match k8s.io/client-go patterns, adjust if production shows issues

## Sources

### Primary (HIGH confidence)
- [SUSE Linux NVMe-oF Storage Administration Guide](https://documentation.suse.com/sles/15-SP7/html/SLES-all/cha-nvmeof.html) - ctrl_loss_tmo default 600s, -1 recommendation
- [nvme-cli Documentation](https://github.com/linux-nvme/nvme-cli/blob/master/Documentation/nvme-connect.txt) - Connection parameter flags
- [k8s.io/apimachinery/pkg/util/wait](https://pkg.go.dev/k8s.io/apimachinery/pkg/util/wait) - Backoff struct with jitter
- [CSI Spec - NodeStageVolume](https://github.com/container-storage-interface/spec/blob/master/spec.md) - VolumeContext flow
- [Dell CSM NVMe/TCP Issue #1465](https://github.com/dell/csm/issues/1465) - UDEV rules for ctrl_loss_tmo=-1

### Secondary (MEDIUM confidence)
- [Kubernetes StorageClass Documentation](https://kubernetes.io/docs/concepts/storage/storage-classes/) - Parameter passing
- [AWS Exponential Backoff and Jitter](https://aws.amazon.com/builders-library/timeouts-retries-and-backoff-with-jitter/) - Jitter rationale
- [How to Implement Retry Logic in Go with Exponential Backoff (2026)](https://oneuptime.com/blog/post/2026-01-07-go-retry-exponential-backoff/view) - Current best practices
- [Kubernetes CSI Developer Documentation](https://kubernetes-csi.github.io/docs/) - StorageClass secrets and parameters
- [k8s.io/client-go Backoff Implementation](https://github.com/kubernetes/client-go/blob/master/util/flowcontrol/backoff.go) - Reference implementation

### Tertiary (LOW confidence)
- WebSearch results on orphaned connection cleanup - General CSI driver patterns, not NVMe-specific
- Community discussions on ctrl_loss_tmo values - Anecdotal, not official documentation

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH - nvme-cli parameters are well-documented, k8s.io/apimachinery/pkg/util/wait is stable API
- Architecture: HIGH - Patterns based on official CSI spec, existing codebase (Phase 1 resolver, Phase 2 backoff), and K8s standards
- Pitfalls: MEDIUM - Based on research and distributed systems best practices, some need production validation

**Research date:** 2026-01-30
**Valid until:** 2026-03-01 (30 days - stable domain, kernel parameters unchanged for years)
