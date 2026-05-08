# KubeVirt Live Migration with nvmet-csi driver

## Overview

The nvmet-csi driver supports KubeVirt VM live migration through temporary ReadWriteMany (RWX) block volume access during migration. This enables zero-downtime VM migration across Kubernetes nodes while the VM continues running.

**Critical limitation:** RWX block volumes are safe ONLY for KubeVirt live migration due to QEMU's I/O coordination. Using RWX for general workloads will cause data corruption.

This document covers:
- Safe usage patterns (KubeVirt only)
- Why RWX is safe for KubeVirt but unsafe for other workloads
- StorageClass configuration for migration timeouts
- Monitoring migration progress and failures
- Troubleshooting common issues

## Safe Usage: KubeVirt Live Migration ONLY

### ✅ SAFE: KubeVirt VM Live Migration

KubeVirt VM migration with RWX block volumes is safe because QEMU coordinates I/O between source and target VMs:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: vm-disk
spec:
  accessModes:
    - ReadWriteMany    # RWX allowed for KubeVirt migration
  volumeMode: Block    # REQUIRED - filesystem mode rejected
  storageClassName: rds-nvme
  resources:
    requests:
      storage: 20Gi
```

**Why this is safe:**
- Only one QEMU process writes at any given moment
- Source VM pauses I/O before final handoff to target VM
- Target VM receives complete memory state including in-flight I/O
- Migration completes in seconds (< 5 minutes by default)

### ❌ UNSAFE - DATA CORRUPTION RISK: General RWX Workloads

**DO NOT** use RWX block volumes for multiple pods writing simultaneously. The CSI driver does NOT provide distributed locking or I/O coordination:

```yaml
# EXAMPLE OF WHAT NOT TO DO - THIS WILL CORRUPT YOUR DATA
apiVersion: v1
kind: Pod
metadata:
  name: app-1
spec:
  containers:
  - name: app
    volumeDevices:
    - name: shared-disk
      devicePath: /dev/xvda
  volumes:
  - name: shared-disk
    persistentVolumeClaim:
      claimName: vm-disk

---
# Second pod on different node - DATA CORRUPTION!
apiVersion: v1
kind: Pod
metadata:
  name: app-2
spec:
  containers:
  - name: app
    volumeDevices:
    - name: shared-disk
      devicePath: /dev/xvda
  volumes:
  - name: shared-disk
    persistentVolumeClaim:
      claimName: vm-disk  # Same PVC as app-1 - WILL CORRUPT DATA
```

**What will happen:**
- Both pods write to the same block device from different nodes
- No distributed locking prevents conflicting writes
- Filesystem metadata corruption (ext4, xfs, btrfs superblocks)
- Data loss, VM boot failures, unrecoverable corruption

**Recovery:** None - data corruption from simultaneous writes is not recoverable. Restore from backup.

## Why RWX is Safe for KubeVirt (But Not General Use)

### QEMU Coordination During Migration

KubeVirt's live migration protocol ensures only one VM writes at any time:

1. **Pre-migration:** Source VM runs normally, writes to NVMe/TCP volume
2. **Memory transfer:** Source VM continues running, memory pages copied to target
3. **Dual-attach window:** Target VM attaches to volume (RWX allows 2 nodes)
4. **I/O pause:** Source VM pauses I/O operations
5. **Final handoff:** Target VM receives memory state including in-flight I/O
6. **Completion:** Source VM detaches, target VM continues with exclusive access

**Key guarantee:** QEMU ensures only one VM issues writes during the handoff. The brief dual-attach window (seconds) does not result in concurrent writes.

### NO Coordination for General Workloads

The nvmet-csi driver does NOT provide:

- **Distributed locking** - No mechanism prevents two nodes from writing simultaneously
- **I/O fencing** - No split-brain protection if nodes lose coordination
- **Cluster filesystem support** - GFS2, OCFS2, and other cluster-aware filesystems NOT supported
- **Write ordering guarantees** - No coordination of write operations across nodes
- **Conflict resolution** - No detection or resolution of conflicting writes

**Result:** Two pods on different nodes writing to the same RWX block volume will corrupt data. The driver permits the attachment (for KubeVirt migration) but provides NO protection against concurrent writes.

## StorageClass Configuration

Configure migration timeout in your StorageClass to control how long dual-attach is permitted:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: rds-kubevirt
provisioner: nvmet.csi.whiskey.works
parameters:
  # Migration timeout: Maximum duration for dual-node attachment
  # Default: 300 seconds (5 minutes)
  # Range: 30-3600 seconds (30 seconds to 1 hour)
  #
  # Increase for:
  # - VMs with large memory (> 16GB)
  # - Slow network between nodes
  # - High memory churn workloads
  #
  # Decrease for:
  # - Small VMs (< 4GB memory)
  # - Fast datacenter network (10Gbps+)
  # - Strict dual-attach time limits
  migrationTimeoutSeconds: "300"

  # Standard RDS parameters
  rdsAddress: "10.42.68.1"
  rdsPort: "22"
  rdsBasePath: "/storage-pool/kubernetes-volumes"
  nvmePort: "4420"

volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

### Timeout Tuning Guidelines

| VM Memory | Network Speed | Recommended Timeout |
|-----------|---------------|---------------------|
| < 4GB     | 10Gbps+       | 60-120s            |
| 4-16GB    | 10Gbps+       | 120-300s (default) |
| 16-64GB   | 10Gbps+       | 300-600s           |
| > 64GB    | 10Gbps+       | 600-1800s          |
| Any       | < 1Gbps       | Add 2-5x base time |

**Timeout exceeded:** If migration takes longer than `migrationTimeoutSeconds`, subsequent attach attempts will be rejected with `FailedPrecondition` error. Detach the source node to reset the migration state.

## Monitoring Migration

### Prometheus Metrics

Query migration metrics to track success rate, duration, and active migrations:

```promql
# Number of currently active migrations (dual-attach state)
nvmet_csi_migration_active_migrations

# Migration success rate (migrations/second)
rate(nvmet_csi_migration_migrations_total{result="success"}[5m])

# Migration timeout rate (timed out migrations/second)
rate(nvmet_csi_migration_migrations_total{result="timeout"}[5m])

# 95th percentile migration duration
histogram_quantile(0.95, rate(nvmet_csi_migration_migration_duration_seconds_bucket[5m]))

# 50th percentile migration duration (median)
histogram_quantile(0.50, rate(nvmet_csi_migration_migration_duration_seconds_bucket[5m]))

# Total migrations by result (success, timeout, failed)
nvmet_csi_migration_migrations_total
```

**Alerting examples:**

```promql
# Alert if migration timeout rate exceeds 10%
rate(nvmet_csi_migration_migrations_total{result="timeout"}[5m])
  /
rate(nvmet_csi_migration_migrations_total[5m]) > 0.1

# Alert if any migration is stuck for > 10 minutes
nvmet_csi_migration_active_migrations > 0
  AND
time() - nvmet_csi_migration_active_migrations > 600
```

### Kubernetes Events

Migration lifecycle events are posted to the PVC for operator visibility:

```bash
# View migration events on a specific PVC
kubectl describe pvc vm-disk | grep -A 5 Migration

# Watch migration events in real-time
kubectl get events --watch | grep Migration

# Filter migration events across all namespaces
kubectl get events --all-namespaces --field-selector reason=MigrationStarted
kubectl get events --all-namespaces --field-selector reason=MigrationCompleted
kubectl get events --all-namespaces --field-selector reason=MigrationFailed
```

**Example event output:**

```
Events:
  Type    Reason               Age    From                Message
  ----    ------               ----   ----                -------
  Normal  MigrationStarted     2m15s  nvmet-csi-controller  [pvc-a1b2c3d4]: KubeVirt live migration started - source: node1, target: node2, timeout: 5m0s
  Normal  MigrationCompleted   1m30s  nvmet-csi-controller  [pvc-a1b2c3d4]: KubeVirt live migration completed - source: node1 → target: node2 (duration: 45s)
```

**Failed migration example:**

```
Events:
  Type     Reason            Age   From                Message
  ----     ------            ----  ----                -------
  Normal   MigrationStarted  6m    nvmet-csi-controller  [pvc-xyz123]: KubeVirt live migration started - source: node1, target: node2, timeout: 5m0s
  Warning  MigrationFailed   1m    nvmet-csi-controller  [pvc-xyz123]: KubeVirt live migration failed - source: node1, attempted target: node2, reason: timeout, elapsed: 5m0s
```

## Troubleshooting

### Migration Timeout

**Error message:**
```
Volume pvc-xyz123 migration timeout exceeded (5m2s elapsed, 5m0s max).
Previous migration may be stuck. Detach source node to reset, or adjust
migrationTimeoutSeconds in StorageClass.
```

**Causes:**
- VM has large memory (16GB+) that takes longer than timeout to transfer
- Slow network between nodes (< 1Gbps)
- High memory churn in VM workload (databases, caches)
- Source node under heavy load (CPU/memory pressure)

**Solutions:**

1. **Increase timeout in StorageClass:**
   ```yaml
   parameters:
     migrationTimeoutSeconds: "600"  # Increase to 10 minutes
   ```

2. **Check network bandwidth between nodes:**
   ```bash
   # From source node to target node
   iperf3 -c target-node-ip -p 5201
   ```
   - Target: 10Gbps+ for large VMs
   - Minimum: 1Gbps for small VMs

3. **Verify NVMe/TCP performance:**
   ```bash
   # Check NVMe connection on target node
   nvme list | grep pvc-xyz123

   # Test disk I/O throughput
   fio --filename=/dev/nvme0n1 --direct=1 --rw=read --bs=1M --ioengine=libaio --runtime=10 --name=test
   ```

4. **Reset stuck migration:**
   ```bash
   # Delete source pod to trigger detach
   kubectl delete pod vm-source-pod --force --grace-period=0

   # Verify detachment completed
   kubectl get volumeattachment | grep pvc-xyz123
   ```

### Data Corruption After Using RWX

**Symptom:**
- VM fails to boot after using RWX volume with multiple pods
- Filesystem errors: "Superblock checksum does not match superblock"
- Data loss or inconsistent data between reads

**Cause:**
You used a RWX block volume with multiple pods writing simultaneously (not a KubeVirt migration scenario). The CSI driver does not provide distributed locking, so concurrent writes corrupted the filesystem.

**Prevention:**
- **ONLY** use RWX access mode for KubeVirt VM live migration
- **NEVER** use RWX for general workloads (databases, caches, shared storage)
- Use ReadWriteOnce (RWO) for all non-KubeVirt workloads
- For shared storage, use NFS or a cluster-aware filesystem

**Recovery:**
- Data corruption from concurrent writes is **NOT RECOVERABLE**
- Restore from backup
- Recreate PVC with RWO access mode if not using KubeVirt
- Review application architecture to avoid shared block storage

**Validation after restoration:**
```bash
# Check filesystem integrity
fsck -n /dev/nvme0n1

# For ext4
e2fsck -n /dev/nvme0n1

# For xfs
xfs_repair -n /dev/nvme0n1
```

### Migration Fails with "Already attached to 2 nodes"

**Error message:**
```
Volume pvc-xyz123 already attached to 2 nodes (migration limit).
Attached nodes: [node1, node2]
```

**Cause:**
A previous migration did not complete cleanup, leaving the volume attached to 2 nodes. New migration attempts are blocked to prevent 3-way attachment.

**Solution:**

1. **Check current attachments:**
   ```bash
   kubectl get volumeattachment | grep pvc-xyz123
   ```

2. **Delete stale VolumeAttachment objects:**
   ```bash
   # Identify stale attachments (no corresponding pod)
   kubectl delete volumeattachment <stale-attachment-name>
   ```

3. **Force detach from source node:**
   ```bash
   # Delete the source pod
   kubectl delete pod <source-vm-pod> --force --grace-period=0
   ```

4. **Wait for automatic cleanup:**
   - CSI controller will detect pod deletion and trigger ControllerUnpublishVolume
   - Check events: `kubectl describe pvc vm-disk`

### NVMe Connection Fails on Target Node

**Error message in node plugin logs:**
```
Failed to connect to NVMe target: nvme connect failed with exit code 1
```

**Causes:**
- NVMe/TCP subsystem not exported on RDS
- Network connectivity issue between target node and RDS
- Firewall blocking NVMe/TCP port 4420

**Solutions:**

1. **Verify NVMe export on RDS:**
   ```bash
   ssh admin@10.42.68.1
   /disk print detail where slot=pvc-xyz123
   # Check: nvme-tcp-export=yes, nvme-tcp-server-port=4420
   ```

2. **Test network connectivity:**
   ```bash
   # From target node
   nc -zv 10.42.68.1 4420
   ```

3. **Check NVMe discovery:**
   ```bash
   nvme discover -t tcp -a 10.42.68.1 -s 4420
   # Should list the volume's NQN
   ```

4. **Check node plugin logs:**
   ```bash
   kubectl logs -n kube-system -l app=nvmet-csi-node --tail=100
   ```

## Future Enhancements

The following features are **not currently implemented** but may be added in future versions:

### Deferred Features

- **Cluster filesystem support (GFS2, OCFS2):** Enable true concurrent RWX access with distributed locking
- **RDS-level namespace reservations:** Prevent split-brain scenarios at the storage layer
- **KubeVirt API integration:** Automatic migration detection and timeout coordination
- **Multi-attach for StatefulSets:** Controlled RWX for replicated stateful workloads
- **Automatic migration timeout tuning:** Dynamic timeout based on VM memory size

### Workarounds for Shared Storage

If you need shared storage for non-KubeVirt workloads, consider these alternatives:

- **NFS:** Use NFS CSI driver for filesystem-based shared storage
- **CephFS:** Use Rook/Ceph for distributed filesystem with strong consistency
- **S3-compatible object storage:** MinIO or Ceph RGW for object-based sharing
- **Application-level replication:** Use database replication instead of shared block storage

**Do not use RWX block volumes for:**
- Shared database storage (PostgreSQL, MySQL data directories)
- Shared cache storage (Redis, Memcached data files)
- Shared log aggregation (Elasticsearch, Loki data volumes)
- Shared application state (session stores, job queues)

## Summary

- ✅ **Safe:** KubeVirt VM live migration with RWX block volumes (QEMU I/O coordination)
- ❌ **Unsafe:** General RWX workloads with multiple pods (data corruption risk)
- **Timeout:** Configure `migrationTimeoutSeconds` in StorageClass (default 300s)
- **Monitoring:** Use Prometheus metrics and Kubernetes events to track migrations
- **Troubleshooting:** Increase timeout for large VMs, reset stuck migrations by deleting source pod
- **Recovery:** Data corruption is not recoverable - restore from backup

**Key takeaway:** RWX support in the nvmet-csi driver is designed exclusively for KubeVirt live migration. Do not use RWX for any other purpose.
