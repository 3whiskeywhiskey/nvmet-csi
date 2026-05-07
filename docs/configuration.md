# RDS CSI Driver Configuration

This document describes the configuration options for the RDS CSI driver.

## Basic Configuration

The driver is configured through command-line flags and environment variables. Kubernetes deployments use ConfigMaps and Secrets to provide configuration values.

### RDS Connection Settings

Configure RDS server connection via ConfigMap `rds-csi-config`:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: rds-csi-config
  namespace: rds-csi
data:
  rds-address: "10.42.241.3"
  rds-port: "22"
  rds-user: "metal-csi"
  rds-volume-base-path: "/storage-pool/metal-csi"
  rds-mutation-min-interval: "250ms"
  rds-mutation-burst: "1"
  rds-mutation-timeout: "30s"
  nqn-prefix: "nqn.2000-02.com.mikrotik:pvc-"
```

### SSH Credentials

Provide SSH credentials via Secret `rds-csi-secret`:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: rds-csi-secret
  namespace: rds-csi
type: Opaque
stringData:
  rds-private-key: |
    -----BEGIN OPENSSH PRIVATE KEY-----
    ...
    -----END OPENSSH PRIVATE KEY-----
  rds-host-key: "ssh-rsa AAAAB3NzaC1yc2..."
```

## Error Resilience Settings (Phase 14)

### NQN Prefix Filtering

The driver only manages NVMe volumes that match the configured NQN prefix. This prevents
accidental disconnection of system volumes (e.g., NixOS diskless nodes that mount /var
from RDS).

**Environment Variable:** `CSI_MANAGED_NQN_PREFIX`
**Default:** `nqn.2000-02.com.mikrotik:pvc-`
**Required:** Yes (driver refuses to start without it)

Configure via Kubernetes ConfigMap:

```yaml
data:
  nqn-prefix: "nqn.2000-02.com.mikrotik:pvc-"
```

The node plugin reads this value from the ConfigMap:

```yaml
env:
  - name: CSI_MANAGED_NQN_PREFIX
    valueFrom:
      configMapKeyRef:
        name: rds-csi-config
        key: nqn-prefix
```

### Node Startup Orphan Cleanup

The node plugin can scan for managed NVMe/TCP sessions that appear connected
but no longer have a working block device. This cleanup may disconnect managed
NVMe sessions, so it is disabled by default and should be enabled only during an
explicit remediation or audit window.

**Flag:** `-node-orphan-cleanup`
**Default:** `false`

When disabled, the node plugin logs that startup orphan cleanup is disabled and
does not enumerate or disconnect NVMe subsystems. When enabled, it still filters
by `CSI_MANAGED_NQN_PREFIX` before considering any disconnect.

### Circuit Breaker

The driver uses per-volume circuit breakers to prevent retry storms on repeatedly
failing volumes. After 3 consecutive failures, the circuit opens and returns
`Unavailable` error until reset.

**Reset via PV annotation:**

```yaml
metadata:
  annotations:
    rds.csi.srvlab.io/reset-circuit-breaker: "true"
```

### Graceful Shutdown

The driver waits up to 30 seconds for in-flight operations to complete during
shutdown. Set `terminationGracePeriodSeconds: 60` in the pod spec to give
Kubernetes enough time before SIGKILL.

```yaml
spec:
  terminationGracePeriodSeconds: 60
```

### Mount Storm Detection

The driver monitors `/proc/mounts` for duplicate mount entries. If more than 100
duplicates are detected for a single volume, the driver:

1. Stops attempting new mounts
2. Returns error to kubelet
3. Logs warning for operator intervention

This prevents runaway mount storms that can corrupt filesystems or exhaust system resources.

## Orphan Reconciler Settings

Enable orphan volume detection and cleanup in the controller:

```yaml
args:
  - "-enable-orphan-reconciler"
  - "-orphan-check-interval=1h"
  - "-orphan-grace-period=5m"
  - "-orphan-run-on-start=false"
  - "-orphan-min-confirmations=2"
  - "-orphan-dry-run=true"
```

- **orphan-check-interval:** How often to check for orphaned volumes (default: 1h)
- **orphan-grace-period:** Minimum age before considering a volume orphaned (default: 5m)
- **orphan-run-on-start:** Run an inventory immediately when the controller starts (default: false)
- **orphan-min-confirmations:** Consecutive inventories required before cleanup is planned (default: 2)
- **orphan-dry-run:** Log orphans without deleting (default: true, set false to enable cleanup)

File orphan cleanup only plans deletes for `pvc-*.img` files with known
timestamps older than the grace period. Manual `.img` files and unknown-age
files are left alone.

See [docs/orphan-reconciler.md](orphan-reconciler.md) for details.

## RDS Mutation Guardrails

The controller rate-limits mutating RouterOS/RDS operations before they reach the RDS client. This is intended to reduce add/remove storms during node drains, reschedules, and retry loops.

```yaml
args:
  - "-rds-mutation-min-interval=250ms"
  - "-rds-mutation-burst=1"
  - "-rds-mutation-timeout=30s"
```

- **rds-mutation-min-interval:** Minimum spacing between mutating RDS operations. Set to `0s` to disable.
- **rds-mutation-burst:** Number of mutations allowed through immediately.
- **rds-mutation-timeout:** Maximum time a mutation can wait for the guard before failing.

## Attachment Reconciler Settings

The attachment reconciler runs in the controller to track volume attachments during KubeVirt live migration:

```yaml
args:
  - "-attachment-grace-period=30s"
  - "-attachment-reconcile-interval=5m"
  # Optional, disabled by default. Start with dry-run for audit/remediation.
  # - "-enable-stale-volumeattachment-cleanup"
  # - "-stale-volumeattachment-dry-run=true"
  # - "-stale-volumeattachment-min-confirmations=2"
```

- **attachment-grace-period:** Grace period for attachment handoff during live migration (default: 30s)
- **attachment-reconcile-interval:** Interval between reconciliation checks (default: 5m)
- **enable-stale-volumeattachment-cleanup:** Enable guarded cleanup of stale Kubernetes `VolumeAttachment` objects for deleted nodes (default: false)
- **stale-volumeattachment-dry-run:** Log planned `VolumeAttachment` deletes without deleting (default: true)
- **stale-volumeattachment-min-confirmations:** Consecutive reconciliation passes required before deletion (default: 2)

See [docs/kubevirt-migration.md](kubevirt-migration.md) for details.

## VMI Serialization Settings

Enable per-VMI operation serialization to mitigate KubeVirt concurrency issues:

```yaml
args:
  - "-enable-vmi-serialization"
  - "-vmi-cache-ttl=60s"
```

- **enable-vmi-serialization:** Enable per-VMI operation locks (default: false)
- **vmi-cache-ttl:** Cache TTL for PVC-to-VMI mapping lookups (default: 60s)

## Metrics Configuration

Enable Prometheus metrics endpoint:

```yaml
args:
  - "-metrics-address=:9809"
```

Metrics are exposed at `http://<pod-ip>:9809/metrics`.

## Security Configuration

### SSH Host Key Verification

**Production:** Always use SSH host key verification:

```yaml
args:
  - "-rds-host-key=/etc/rds-csi/rds-host-key"
```

**Testing only:** Skip host key verification (INSECURE):

```yaml
args:
  - "-rds-insecure-skip-verify=true"
```

**WARNING:** Never use `-rds-insecure-skip-verify=true` in production.

### Pod Security Context

Node plugin requires privileged mode for bidirectional mount propagation (Kubernetes requirement for CSI drivers):

```yaml
securityContext:
  privileged: true
  readOnlyRootFilesystem: true
  runAsUser: 0
```

Controller runs unprivileged:

```yaml
# (no securityContext needed - runs as non-root by default)
```

## Logging Configuration

Control log verbosity with `-v` flag:

```yaml
args:
  - "-v=5"
```

- **v=2:** Info level (default)
- **v=4:** Debug level
- **v=5:** Trace level (includes CSI method calls)
- **v=6:** Verbose trace (includes SSH commands)

## Advanced Configuration

### Volume Base Path

Set the base path for volumes on RDS:

```yaml
args:
  - "-rds-volume-base-path=/storage-pool/metal-csi"
```

This path is used for:
- Volume creation (files stored at `{base-path}/{volume-id}.img`)
- Orphan detection (only checks volumes under this path)
- Path validation (rejects volumes outside this path)

### NVMe Connection Settings

NVMe connection parameters are currently hardcoded:

- **Port:** 4420
- **Transport:** TCP
- **ctrl_loss_tmo:** -1 (infinite retry)

Future versions may expose these as configuration options.
