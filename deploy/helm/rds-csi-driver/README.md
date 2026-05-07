# RDS CSI Driver Helm Chart

Helm chart for deploying the RDS CSI Driver - a Kubernetes CSI (Container Storage Interface) driver that provides dynamic provisioning of persistent block storage volumes using MikroTik RouterOS Data Server (RDS) with NVMe over TCP.

## Prerequisites

- **Kubernetes**: 1.26 or later
- **Helm**: 3.14 or later
- **RDS**: MikroTik RouterOS 7.1+ with ROSE Data Server configured
- **SSH Access**: SSH key pair for RouterOS CLI access
- **Network**: NVMe/TCP connectivity from Kubernetes nodes to RDS storage interface

## Distribution

This Helm chart is distributed via the git repository only. To use it:

1. Clone the repository:
   ```bash
   git clone https://github.com/3whiskeywhiskey/rds-csi.git
   cd rds-csi
   ```

2. Install directly from the local path:
   ```bash
   helm install rds-csi ./deploy/helm/rds-csi-driver --namespace rds-csi --create-namespace
   ```

There is no OCI registry or Helm repository hosting at this time.

## Quick Start

### 1. Create Namespace

```bash
kubectl create namespace rds-csi
```

### 2. Create Secret

The driver requires a Kubernetes Secret containing SSH credentials and optionally SNMP community string.

**Required Secret Keys:**
- `rds-private-key`: SSH private key for RouterOS CLI authentication
- `rds-host-key`: SSH host public key for host verification
- `snmp-community`: (optional) SNMP community string for hardware monitoring

**Example Secret YAML:**

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: rds-csi-secret
  namespace: rds-csi
type: Opaque
data:
  # Base64-encoded SSH private key (e.g., from ~/.ssh/id_rsa)
  rds-private-key: LS0tLS1CRUdJTiBPUEVOU1NIIFBSSVZBVEUgS0VZLS0tLS0K...

  # Base64-encoded SSH host public key (from RDS /ip/ssh/print)
  rds-host-key: c3NoLXJzYSBBQUFBQjNOemFDMXljMkVBQUFBREFRQUJBQUFCZ1FD...

  # Base64-encoded SNMP community string (optional)
  snmp-community: cHVibGlj
```

**Create from files:**

```bash
kubectl create secret generic rds-csi-secret \
  -n rds-csi \
  --from-file=rds-private-key=/path/to/ssh-key \
  --from-file=rds-host-key=/path/to/host-key \
  --from-file=snmp-community=<(echo -n "public")
```

### 3. Install Chart

```bash
helm install rds-csi ./deploy/helm/rds-csi-driver \
  --namespace rds-csi \
  --set rds.managementIP=10.42.241.3 \
  --set rds.storageIP=10.42.68.1
```

### 4. Verify Installation

```bash
# Check pods
kubectl get pods -n rds-csi

# Verify CSI driver registration
kubectl get csidriver rds.csi.srvlab.io

# Check available StorageClasses
kubectl get storageclass
```

## Configuration

All configuration is done via the `values.yaml` file or `--set` flags during installation.

### RDS Connection Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `rds.managementIP` | Management interface IP (SSH control plane) | `10.42.241.3` |
| `rds.storageIP` | Storage interface IP (NVMe/TCP data plane) | `10.42.68.1` |
| `rds.sshPort` | SSH port for RouterOS CLI | `22` |
| `rds.nvmePort` | NVMe/TCP port for storage connections | `4420` |
| `rds.sshUser` | SSH username on RouterOS | `metal-csi` |
| `rds.basePath` | Base path for volumes on RDS | `/storage-pool/metal-csi` |
| `rds.secretName` | Kubernetes Secret containing RDS credentials | `rds-csi-secret` |
| `rds.insecureSkipVerify` | Skip SSH host key verification (INSECURE - testing only) | `false` |
| `rds.nqnPrefix` | NQN prefix for CSI-managed volumes | `nqn.2000-02.com.mikrotik:pvc-` |
| `rds.mutationGuard.minInterval` | Minimum spacing between mutating RDS operations | `250ms` |
| `rds.mutationGuard.burst` | Maximum RDS mutations allowed to burst | `1` |
| `rds.mutationGuard.timeout` | Maximum wait for a mutation guard token | `30s` |

### Controller Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `controller.replicas` | Number of controller replicas (must be 1) | `1` |
| `controller.image.repository` | Controller container image repository | `ghcr.io/3whiskeywhiskey/rds-csi` |
| `controller.image.tag` | Controller image tag (defaults to Chart.appVersion) | `""` |
| `controller.image.pullPolicy` | Image pull policy | `Always` |
| `controller.logLevel` | Log verbosity level (0-10) | `5` |
| `controller.resources.requests.cpu` | CPU request | `10m` |
| `controller.resources.requests.memory` | Memory request | `64Mi` |
| `controller.resources.limits.cpu` | CPU limit | `200m` |
| `controller.resources.limits.memory` | Memory limit | `256Mi` |
| `controller.nodeSelector` | Node selector for controller pod | `{}` |
| `controller.tolerations` | Tolerations for controller pod | See values.yaml |
| `controller.affinity` | Affinity for controller pod | See values.yaml |
| `controller.priorityClassName` | Priority class for controller pod | `system-cluster-critical` |
| `controller.orphanReconciler.enabled` | Enable orphaned volume detection and cleanup | `false` |
| `controller.orphanReconciler.checkInterval` | Orphan check interval | `1h` |
| `controller.orphanReconciler.gracePeriod` | Grace period before cleanup | `5m` |
| `controller.orphanReconciler.runOnStart` | Run orphan inventory immediately on controller startup | `false` |
| `controller.orphanReconciler.minConfirmations` | Consecutive inventories required before cleanup | `2` |
| `controller.orphanReconciler.dryRun` | Dry-run mode (no actual cleanup) | `true` |
| `controller.attachmentGracePeriod` | Attachment grace period for live migration | `30s` |
| `controller.attachmentReconcileInterval` | Attachment reconciliation interval | `5m` |
| `controller.staleVolumeAttachmentCleanup.enabled` | Enable guarded cleanup of stale `VolumeAttachment` objects for deleted nodes | `false` |
| `controller.staleVolumeAttachmentCleanup.dryRun` | Log planned stale `VolumeAttachment` deletes without deleting | `true` |
| `controller.staleVolumeAttachmentCleanup.minConfirmations` | Consecutive reconciliation passes required before deletion | `2` |
| `controller.vmiSerialization.enabled` | Enable VMI serialization (KubeVirt) | `false` |
| `controller.vmiSerialization.cacheTTL` | VMI cache TTL | `60s` |

### Node Plugin Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `node.image.repository` | Node plugin container image repository | `ghcr.io/3whiskeywhiskey/rds-csi` |
| `node.image.tag` | Node image tag (defaults to Chart.appVersion) | `""` |
| `node.image.pullPolicy` | Image pull policy | `Always` |
| `node.logLevel` | Log verbosity level (0-10) | `5` |
| `node.orphanCleanup.enabled` | Run startup cleanup for managed orphaned NVMe sessions | `false` |
| `node.orphanCleanup.dryRun` | Log managed orphaned NVMe sessions without disconnecting them | `true` |
| `node.orphanCleanup.maxDisconnects` | Maximum orphaned NVMe sessions to disconnect in one startup cleanup pass | `4` |
| `node.resources.requests.cpu` | CPU request | `10m` |
| `node.resources.requests.memory` | Memory request | `128Mi` |
| `node.resources.limits.cpu` | CPU limit | `200m` |
| `node.resources.limits.memory` | Memory limit | `512Mi` |
| `node.kubeletPath` | Kubelet directory path | `/var/lib/kubelet` |
| `node.nodeSelector` | Node selector for node plugin pods | `{kubernetes.io/os: linux}` |
| `node.tolerations` | Tolerations for node plugin pods | `[{operator: Exists}]` |
| `node.priorityClassName` | Priority class for node plugin pods | `system-node-critical` |
| `node.terminationGracePeriodSeconds` | Termination grace period for clean unmount | `60` |

### Sidecar Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `sidecars.provisioner.image.repository` | Provisioner sidecar image | `registry.k8s.io/sig-storage/csi-provisioner` |
| `sidecars.provisioner.image.tag` | Provisioner image tag | `v4.0.0` |
| `sidecars.attacher.image.repository` | Attacher sidecar image | `registry.k8s.io/sig-storage/csi-attacher` |
| `sidecars.attacher.image.tag` | Attacher image tag | `v4.5.0` |
| `sidecars.resizer.image.repository` | Resizer sidecar image | `registry.k8s.io/sig-storage/csi-resizer` |
| `sidecars.resizer.image.tag` | Resizer image tag | `v1.10.0` |
| `sidecars.snapshotter.image.repository` | Snapshotter sidecar image | `registry.k8s.io/sig-storage/csi-snapshotter` |
| `sidecars.snapshotter.image.tag` | Snapshotter image tag | `v8.2.0` |
| `sidecars.livenessProbe.image.repository` | Liveness probe sidecar image | `registry.k8s.io/sig-storage/livenessprobe` |
| `sidecars.livenessProbe.image.tag` | Liveness probe image tag | `v2.12.0` |

### Monitoring Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `monitoring.enabled` | Enable Prometheus metrics endpoint | `true` |
| `monitoring.port` | Metrics server port | `9809` |
| `monitoring.serviceMonitor.enabled` | Enable ServiceMonitor resource creation | `false` |
| `monitoring.serviceMonitor.forceEnable` | Force ServiceMonitor creation (skip CRD detection) | `false` |
| `monitoring.serviceMonitor.interval` | Prometheus scrape interval | `30s` |
| `monitoring.serviceMonitor.labels` | Additional labels for ServiceMonitor | `{}` |
| `monitoring.rdsMonitoring.enabled` | Enable RDS disk and hardware metrics | `true` |

### StorageClass Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `storageClasses[0].name` | Primary StorageClass name | `rds-nvme` |
| `storageClasses[0].enabled` | Enable StorageClass creation | `true` |
| `storageClasses[0].isDefault` | Set as default StorageClass | `false` |
| `storageClasses[0].fsType` | Filesystem type | `ext4` |
| `storageClasses[0].nvmeAddress` | NVMe/TCP address (defaults to rds.storageIP) | `""` |
| `storageClasses[0].nvmePort` | NVMe/TCP port (defaults to rds.nvmePort) | `""` |
| `storageClasses[0].volumePath` | Volume base path (defaults to rds.basePath) | `""` |
| `storageClasses[0].volumeBindingMode` | Volume binding mode | `WaitForFirstConsumer` |
| `storageClasses[0].reclaimPolicy` | Reclaim policy | `Delete` |
| `storageClasses[0].allowVolumeExpansion` | Allow volume expansion | `true` |
| `storageClasses[0].mountOptions` | Mount options | `[]` |
| `storageClasses[1].name` | RWX StorageClass name (KubeVirt) | `rds-nvme-rwx` |
| `storageClasses[1].enabled` | Enable RWX StorageClass | `false` |

**Important:** The `rds-nvme-rwx` StorageClass is disabled by default. It enables ReadWriteMany (RWX) access mode for KubeVirt live migration support. This is NOT intended for general-purpose shared filesystems like NFS. Enable only in KubeVirt environments.

### Snapshot Settings

| Parameter | Description | Default |
|-----------|-------------|---------|
| `snapshotClass.enabled` | Enable VolumeSnapshotClass creation | `true` |
| `snapshotClass.name` | VolumeSnapshotClass name | `rds-csi-snapclass` |
| `snapshotClass.deletionPolicy` | Deletion policy (Delete or Retain) | `Delete` |

**Important:** Snapshot functionality requires VolumeSnapshot CRDs and snapshot-controller to be installed separately. See installation instructions in NOTES.txt.

## Secret Structure

The driver requires a Kubernetes Secret with specific keys. Here's the complete structure:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: rds-csi-secret
  namespace: rds-csi
type: Opaque
data:
  # SSH private key for RouterOS authentication
  # Generate: ssh-keygen -t rsa -b 4096 -f rds-csi-key
  # Encode: cat rds-csi-key | base64 -w 0
  rds-private-key: LS0tLS1CRUdJTiBPUEVOU1NIIFBSSVZBVEUgS0VZLS0tLS0K...

  # SSH host public key from RouterOS
  # Get from RDS: /ip/ssh/print
  # Encode: echo "ssh-rsa AAAA..." | base64 -w 0
  rds-host-key: c3NoLXJzYSBBQUFBQjNOemFDMXljMkVBQUFBREFRQUJBQUFCZ1FD...

  # SNMP community string for hardware monitoring (optional)
  # Only needed if monitoring.rdsMonitoring.enabled=true
  # Default RouterOS community is "public"
  # Encode: echo -n "public" | base64
  snmp-community: cHVibGlj
```

## Storage Classes

The chart creates two StorageClasses:

### 1. rds-nvme (Primary - ReadWriteOnce)

Default StorageClass for standard block storage volumes. Supports:
- ReadWriteOnce (RWO) access mode
- Dynamic provisioning
- Volume expansion
- Snapshots

**Use case:** General-purpose persistent storage for databases, application data, etc.

### 2. rds-nvme-rwx (KubeVirt - ReadWriteMany)

Optional StorageClass for KubeVirt live migration support. Disabled by default.

**IMPORTANT:** This StorageClass enables multi-attach during KubeVirt VM live migration windows. It is NOT intended for general-purpose shared filesystems like NFS. The underlying filesystem (ext4) does not support concurrent writes from multiple nodes.

**Enable for KubeVirt:**

```bash
helm install rds-csi ./deploy/helm/rds-csi-driver \
  --namespace rds-csi \
  --set storageClasses[1].enabled=true
```

**How it works:** During KubeVirt live migration, the volume is temporarily attached to both source and destination nodes. The source node has a read-only mount while the destination node takes over with a read-write mount. This is safe because KubeVirt coordinates the migration to prevent concurrent writes.

## Monitoring

The driver exposes Prometheus metrics on port 9809 (configurable).

### Metrics Exposed

**CSI Operation Metrics:**
- `rds_csi_operations_total` - Counter of CSI operations by method and status
- `rds_csi_operation_duration_seconds` - Histogram of CSI operation latencies
- `rds_csi_nvme_connections_active` - Gauge of active NVMe/TCP connections

**RDS Disk Metrics** (when `monitoring.rdsMonitoring.enabled=true`):
- `rds_disk_read_ops_per_second` - Read IOPS
- `rds_disk_write_ops_per_second` - Write IOPS
- `rds_disk_read_bytes_per_second` - Read throughput
- `rds_disk_write_bytes_per_second` - Write throughput
- `rds_disk_read_latency_ms` - Read latency
- `rds_disk_write_latency_ms` - Write latency
- `rds_disk_queue_depth` - Queue depth
- `rds_disk_active_time_percent` - Active time percentage
- `rds_disk_capacity_bytes` - Total disk capacity

**RDS Hardware Metrics** (when `monitoring.rdsMonitoring.enabled=true`):
- `rds_hardware_cpu_temperature_celsius` - CPU temperature
- `rds_hardware_board_temperature_celsius` - Board temperature
- `rds_hardware_fan1_speed_rpm` - Fan 1 speed
- `rds_hardware_fan2_speed_rpm` - Fan 2 speed
- `rds_hardware_psu1_power_watts` - PSU 1 power draw
- `rds_hardware_psu2_power_watts` - PSU 2 power draw
- `rds_hardware_psu1_temperature_celsius` - PSU 1 temperature
- `rds_hardware_psu2_temperature_celsius` - PSU 2 temperature

### Prometheus Integration

**Manual scraping:**

```yaml
scrape_configs:
  - job_name: 'rds-csi'
    kubernetes_sd_configs:
      - role: service
        namespaces:
          names: [rds-csi]
    relabel_configs:
      - source_labels: [__meta_kubernetes_service_name]
        regex: .*-metrics
        action: keep
```

**Prometheus Operator:**

Enable ServiceMonitor creation:

```bash
helm install rds-csi ./deploy/helm/rds-csi-driver \
  --namespace rds-csi \
  --set monitoring.serviceMonitor.enabled=true
```

The ServiceMonitor will be created only if the Prometheus Operator CRD is detected. To force creation without detection, set `monitoring.serviceMonitor.forceEnable=true`.

## Upgrading

To upgrade an existing installation:

```bash
helm upgrade rds-csi ./deploy/helm/rds-csi-driver \
  --namespace rds-csi \
  --reset-then-reuse-values
```

**IMPORTANT:** Use `--reset-then-reuse-values` instead of `--reuse-values` to ensure new configuration options are applied with their defaults. Using `--reuse-values` can skip new required configuration.

### Chart Versioning vs App Versioning

- **Chart version** (`version` in Chart.yaml): Incremented for Helm chart changes
- **App version** (`appVersion` in Chart.yaml): Tracks the RDS CSI driver version

The chart version and app version are independent. A chart version bump may contain only configuration changes without a new driver release.

## Uninstalling

To uninstall the driver:

```bash
helm uninstall rds-csi -n rds-csi
```

**IMPORTANT:** This will NOT delete existing PersistentVolumes or PersistentVolumeClaims. Volumes will remain provisioned on the RDS server. To clean up volumes, delete all PVCs before uninstalling:

```bash
kubectl delete pvc -n <namespace> --all
helm uninstall rds-csi -n rds-csi
```

## Troubleshooting

### Controller Pod Not Starting

1. Verify Secret exists and has correct keys:
   ```bash
   kubectl get secret rds-csi-secret -n rds-csi -o yaml
   ```

2. Check controller logs:
   ```bash
   kubectl logs -n rds-csi -l app.kubernetes.io/component=controller -c rds-csi-plugin
   ```

3. Common issues:
   - Missing or incorrect Secret keys (`rds-private-key`, `rds-host-key`)
   - SSH authentication failure (verify key is authorized on RDS)
   - Network connectivity to RDS management IP

### Volume Provisioning Fails

1. Check provisioner sidecar logs:
   ```bash
   kubectl logs -n rds-csi -l app.kubernetes.io/component=controller -c csi-provisioner
   ```

2. Common issues:
   - Insufficient space on RDS
   - Invalid `volumePath` configuration
   - RouterOS command failure (check controller logs)

### Node Plugin Fails to Mount Volume

1. Check node plugin logs:
   ```bash
   kubectl logs -n rds-csi -l app.kubernetes.io/component=node -c rds-csi-plugin
   ```

2. Common issues:
   - NVMe/TCP connectivity to storage IP
   - Firewall blocking port 4420
   - Node missing nvme-cli package (required for NVMe/TCP)

### Snapshot Operations Fail

1. Verify VolumeSnapshot CRDs are installed:
   ```bash
   kubectl get crd volumesnapshots.snapshot.storage.k8s.io
   ```

2. Verify snapshot-controller is running:
   ```bash
   kubectl get pods -n kube-system | grep snapshot-controller
   ```

3. Check snapshotter sidecar logs:
   ```bash
   kubectl logs -n rds-csi -l app.kubernetes.io/component=controller -c csi-snapshotter
   ```

## Documentation

For more information:
- **Project repository**: https://github.com/3whiskeywhiskey/rds-csi
- **CSI specification**: https://github.com/container-storage-interface/spec
- **RouterOS documentation**: https://help.mikrotik.com/docs/

## License

See LICENSE file in the project repository.
