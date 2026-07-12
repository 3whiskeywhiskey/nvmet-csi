# nvmet-csi driver - Capabilities & Comparison

This document provides a comprehensive analysis of the nvmet-csi driver's capabilities, comparing it with mature CSI drivers like AWS EBS CSI and Longhorn to help operators make informed deployment decisions.

## Overview

**Project:** backend-neutral `nvmet-csi`, successor to the archived RDS-focused driver
**Primary backend:** Linux ZFS-on-Linux with kernel `nvmet`
**Compatibility backend:** MikroTik RouterOS/ROSE Data Server over SSH
**Architecture:** single-target NVMe/TCP storage, not distributed storage

Capability status is backend-specific. This document uses:

- **Implemented** for an existing production code path;
- **Automated-tested** for unit, mock, sanity, or automated E2E evidence;
- **Live-validated** only for a dated hardware execution recorded in
  `nvmet-csi-live-validation.md`;
- **Unsupported** when a backend explicitly rejects or lacks an operation.

Linux-ZOL ordinary lifecycle and reboot recovery are live-validated. The
RouterOS compatibility path's ordinary lifecycle is also live-validated under
an isolated CSI identity. RouterOS snapshots are implemented and
automated-tested but await TC-08 hardware execution; Linux-ZOL snapshots are
unsupported.

## CSI Specification Coverage

The driver implements CSI Specification v1.5.0+ with the following capabilities:

### Identity Service

| Capability | Status | Notes |
|------------|--------|-------|
| CONTROLLER_SERVICE | ✅ Supported | Driver provides controller component |
| VOLUME_ACCESSIBILITY_CONSTRAINTS | ✅ Supported | Topology-aware scheduling for node affinity |

**Implementation Details:**
- GetPluginInfo returns driver name (`nvmet.csi.whiskey.works`) and version
- GetPluginCapabilities declares controller service support
- Probe validates RDS connectivity (SSH health check)

### Controller Service

| Capability | Linux-ZOL | RouterOS/RDS | Notes |
|------------|-----------|--------------|-------|
| CREATE_DELETE_VOLUME | ✅ Supported | ✅ Supported | Backend-specific provisioning and ownership checks |
| PUBLISH_UNPUBLISH_VOLUME | ✅ Supported | ✅ Supported | `VolumeAttachment` tracking is scoped to the configured CSI identity |
| LIST_VOLUMES | ✅ Supported | ✅ Supported | Uses backend observation rather than one shared command shape |
| LIST_VOLUMES_PUBLISHED_NODES | ✅ Supported | ✅ Supported | Rebuilt from Kubernetes `VolumeAttachment` objects |
| GET_CAPACITY | ✅ Supported | ✅ Supported | Backend-specific pool/appliance capacity |
| EXPAND_VOLUME | ✅ Supported | ✅ Supported | Linux zvol resize and RouterOS file-backed expansion |
| CREATE_DELETE_SNAPSHOT | ❌ Unsupported | ⚠️ Implemented, automated-tested | Real RouterOS TC-08 pending |
| LIST_SNAPSHOTS | ❌ Unsupported | ⚠️ Implemented, automated-tested | Real RouterOS TC-08 pending |
| VOLUME_CONTENT_SOURCE (snapshot restore) | ❌ Unsupported | ⚠️ Same-backend restore implemented, automated-tested | Real RouterOS TC-08 pending |
| CLONE_VOLUME | ❌ Unsupported | ❌ Unsupported | Use snapshot restore where the backend supports it |

The CSI capability list is currently advertised globally by a driver instance,
although Linux-ZOL does not implement snapshots. `CreateSnapshot` also selects
from `VolumeSnapshotClass` parameters rather than authoritative source-volume
ownership. Until this is hardened, use separate CSI identities for RouterOS
snapshot support and Linux-ZOL, with a RouterOS-specific
`VolumeSnapshotClass`. A class or capability advertisement is not proof that
Linux-ZOL snapshots work.

**Why Not GET_VOLUME:**
This is an optional CSI capability primarily used for validation. The driver can enumerate volumes via LIST_VOLUMES, making GET_VOLUME redundant for our use case.

### Node Service

| Capability | Status | Notes |
|------------|--------|-------|
| STAGE_UNSTAGE_VOLUME | ✅ Supported | NVMe/TCP connect, filesystem format, mount to staging path |
| EXPAND_VOLUME | ✅ Supported | Kernel automatically detects block device resize (no explicit resize2fs/xfs_growfs) |
| GET_VOLUME_STATS | ✅ Supported | Real filesystem statistics via statfs(2) |
| VOLUME_CONDITION | ✅ Supported | NVMe device health checks via nvme-cli |
| SINGLE_NODE_MULTI_WRITER | ❌ Not Supported | NVMe/TCP namespaces are single-initiator (protocol limitation) |

**Why Not SINGLE_NODE_MULTI_WRITER:**
NVMe/TCP namespaces in RouterOS are exported as single-initiator targets. Allowing multiple nodes to connect simultaneously would require shared filesystem support (like GFS2 or OCFS2) and RouterOS multi-host NVMe namespace configuration, which isn't supported by the platform.

## Feature Comparison Matrix

This table compares the nvmet-csi driver against two mature CSI drivers: AWS EBS CSI (cloud-native block storage) and Longhorn (distributed storage for Kubernetes).

| Feature | nvmet-csi | AWS EBS CSI | Longhorn |
|---------|---------|-------------|----------|
| **Provisioning** |
| Dynamic provisioning | ✅ Supported | ✅ Supported | ✅ Supported |
| Volume expansion | ✅ Online expansion | ✅ Online expansion | ✅ Online expansion |
| Volume snapshots | ⚠️ RouterOS only: implemented/automated-tested; TC-08 pending. Linux-ZOL unsupported | ✅ Supported | ✅ Supported |
| Volume cloning | ❌ Not planned | ✅ Supported | ✅ Supported |
| Volume import | ❌ Not supported | ✅ Supported | ❌ Not supported |
| **Access Modes** |
| ReadWriteOnce (RWO) | ✅ Supported | ✅ Supported | ✅ Supported |
| ReadWriteMany (RWX) | ❌ Not supported | ✅ Multi-attach | ✅ Via NFS |
| ReadOnlyMany (ROX) | ❌ Not supported | ✅ Supported | ✅ Supported |
| Block volume mode | ✅ Supported | ✅ Supported | ✅ Supported |
| **Topology & Scheduling** |
| Topology awareness | ✅ Basic (single server) | ✅ AZ-based | ✅ Node-based |
| Volume binding mode | ✅ WaitForFirstConsumer | ✅ WaitForFirstConsumer | ✅ WaitForFirstConsumer |
| Zone constraints | ❌ N/A (single server) | ✅ Cross-AZ support | ✅ Cross-node support |
| **Reliability & HA** |
| Controller HA | ❌ Single replica | ✅ Managed by AWS | ✅ Multi-replica |
| Storage HA | ❌ Single server | ✅ EBS replication | ✅ 3-way replication |
| Node failure handling | ✅ Stale attachment reconciliation | ✅ AWS-managed | ✅ Automatic failover |
| Storage failure handling | ❌ Manual (RDS hardware dependency) | ✅ Automatic | ✅ Automatic rebuild |
| **Performance** |
| Protocol | NVMe/TCP (~1ms latency) | iSCSI/AWS API (~3ms) | iSCSI (~3ms) |
| Max IOPS | ~500k (hardware dependent) | 64,000 (io2 Block Express) | 10,000+ (configurable) |
| Max throughput | ~3 GB/s (25Gbit NIC) | 4,000 MB/s (io2 Block Express) | 1 GB/s+ (configurable) |
| Multipath support | ❌ N/A (single endpoint) | ✅ EBS-optimized | ✅ Built-in |
| **Advanced Features** |
| Volume encryption | ❌ Not supported | ✅ AWS KMS | ❌ Not supported |
| Thin provisioning | ✅ Btrfs sparse files | ❌ Fixed allocation | ✅ Thin provisioning |
| Compression | ✅ Btrfs (if enabled on pool) | ❌ Not supported | ❌ Not supported |
| Deduplication | ❌ Not supported | ❌ Not supported | ❌ Not supported |
| **Monitoring & Observability** |
| Prometheus metrics | ✅ Supported | ✅ Supported | ✅ Supported |
| Volume health checks | ✅ VolumeCondition API | ⚠️ Limited | ✅ Comprehensive |
| Event logging | ✅ Kubernetes events | ✅ CloudWatch | ✅ Kubernetes events |
| Audit trail | ✅ SSH logs | ✅ CloudTrail | ✅ Event logs |
| **Resilience Features** |
| Auto-reconnection | ✅ Comprehensive (ctrl_loss_tmo) | ✅ AWS-managed | ✅ Built-in |
| Stale attachment cleanup | ✅ Automatic reconciliation | ✅ AWS-managed | ✅ Automatic |
| Orphan volume detection | ✅ Automatic (optional) | ❌ Manual | ✅ Automatic |
| **Workload Support** |
| KubeVirt VMs | ✅ Production-validated | ⚠️ Not common use case | ✅ Production-validated |
| Live migration | ✅ Validated (~15s window) | ❌ N/A | ✅ Supported |
| StatefulSets | ✅ Supported | ✅ Supported | ✅ Supported |
| Ephemeral volumes | ❌ Not supported | ✅ Supported | ✅ Supported |

### Legend
- ✅ Supported - Fully implemented and tested
- 🔄 Planned - On roadmap with timeline
- ⚠️ Limited - Partially supported or constrained
- ❌ Not supported - Not available or not applicable

## Unique Advantages

The nvmet-csi driver offers several unique advantages for its target use case:

### 1. NVMe/TCP Protocol Performance

**Advantage:** Native NVMe/TCP protocol provides ~1ms latency vs ~3ms for iSCSI-based drivers.

**Why It Matters:** For latency-sensitive workloads like databases and KubeVirt VMs, every millisecond counts. NVMe/TCP's kernel-space implementation eliminates userspace overhead.

**Trade-off:** Requires NVMe/TCP kernel module (standard in Linux 5.0+) and dedicated storage network for optimal performance.

### 2. File-backed Volume Flexibility

**Advantage:** Btrfs file-backed volumes provide thin provisioning, flexible sizing, and transparent resizing without LVM complexity.

**Why It Matters:** Adding a 50GB volume doesn't immediately consume 50GB of physical storage. Btrfs sparse files grow as data is written. Online expansion is trivial (just resize the file).

**Trade-off:** RouterOS manages Btrfs directly; no direct operator access to filesystem tools.

### 3. SSH-based Management

**Advantage:** All control plane operations are SSH-based, providing auditable, debuggable, human-readable commands.

**Why It Matters:** When troubleshooting, you can SSH to RDS and manually run the same commands the driver uses. Full audit trail via RouterOS logs. No "black box" API.

**Trade-off:** SSH latency adds ~200ms to volume provisioning. Not suitable for high-frequency create/delete operations.

### 4. Attachment Reconciliation

**Advantage:** Automatic recovery from stale VolumeAttachment state after infrastructure failures.

**Why It Matters:** Born from production incident (Phase 25.1). When RDS crashes or nodes fail, driver automatically cleans up stale attachments, preventing "volume stuck in use" scenarios that require manual intervention in other drivers.

**Trade-off:** Adds complexity to controller logic. Requires VolumeAttachment API access (RBAC).

### 5. NVMe-oF Reconnection Resilience

**Advantage:** Comprehensive handling of NVMe-oF controller renumbering via `ctrl_loss_tmo=300` and automatic reconnection.

**Why It Matters:** Survived production RDS crashes without losing volume connectivity. Kubernetes workloads remain running through brief storage interruptions.

**Trade-off:** 300-second timeout means volumes may appear "stuck" during RDS restarts. Fine for infrastructure maintenance, not suitable for sub-second failover requirements.

### 6. Production-tested KubeVirt Integration

**Advantage:** Live migration validated in production with ~15-second migration window. Block volume mode fully supported.

**Why It Matters:** Many CSI drivers claim KubeVirt support but haven't been tested with live migration. nvmet-csi has been validated in production with VMs that have been migrated across nodes without downtime.

**Trade-off:** KubeVirt-specific features like hotplug not yet tested/documented.

## Architectural Differences

Understanding the architectural differences between nvmet-csi and other drivers is essential for setting correct expectations:

### Single-Server vs Distributed Storage

**nvmet-csi:** Single MikroTik RDS server provides storage for entire cluster.

**Longhorn:** Distributed storage with replica placement across multiple nodes.

**Why It Matters:** nvmet-csi's reliability model depends on RDS hardware (RAID, power, network). Longhorn's reliability comes from software replication. Different failure domains.

**When to choose nvmet-csi:**
- Homelab/small clusters where a single high-quality storage server is acceptable
- Latency-sensitive workloads where NVMe/TCP beats distributed storage overhead
- Environments where infrastructure is managed outside Kubernetes (RouterOS)

**When to choose Longhorn:**
- Multi-node deployments requiring storage HA
- Environments where every node has local storage
- Need for automated recovery from storage node failures

### NVMe/TCP vs iSCSI vs Cloud API

**nvmet-csi:** NVMe/TCP protocol for data plane, SSH CLI for control plane.

**AWS EBS CSI:** AWS API for both control and data plane (EBS volumes attach via AWS infrastructure).

**Longhorn:** iSCSI protocol for data plane, REST API for control plane.

**Why It Matters:**
- NVMe/TCP has lower CPU overhead than iSCSI (kernel-space vs userspace)
- SSH CLI is synchronous and slower than REST APIs
- Cloud APIs abstract hardware, SSH CLI exposes it

### Single Controller vs High Availability

**nvmet-csi:** Single controller replica (no leader election).

**AWS EBS CSI:** AWS manages controller HA transparently.

**Longhorn:** Multiple controller replicas with leader election.

**Why It Matters:** nvmet-csi's single controller is an intentional simplification. Since the storage backend is a single server, adding controller HA doesn't improve storage availability. During controller pod restarts (~10s), existing volumes remain accessible; only new provisioning/deletion is affected.

**Trade-off:** Brief provisioning unavailability during controller restarts. Acceptable for homelab/small cluster; not suitable for large-scale multi-tenant environments.

## What's Not Supported and Why

This section provides honest assessment of capabilities the driver doesn't support, with explanations of why.

### Volume Cloning

**Status:** ❌ Not Planned

**Why Not:** RouterOS doesn't expose Btrfs reflink functionality through its CLI interface. While the underlying Btrfs filesystem supports efficient copy-on-write cloning, there's no `/disk clone` command. Implementing this would require RouterOS-level API changes outside the CSI driver's control.

**Workaround:** Create snapshots (Phase 26) and restore to new volume. Not instant like reflink cloning, but achieves the same end result.

### ReadWriteMany (RWX) Access Mode

**Status:** ❌ Not Supported (protocol limitation)

**Why Not:** NVMe/TCP namespaces in RouterOS are single-initiator targets. The NVMe protocol allows multiple connections to a namespace, but the filesystem on top would need to be cluster-aware (GFS2, OCFS2, NFS). RDS doesn't support multi-host namespace export in the way that would enable RWX.

**Workaround:** Use separate volumes per pod (RWO) or deploy a shared filesystem layer on top (NFS server running on a single node with RDS-backed storage).

### Controller High Availability

**Status:** ❌ Not Planned (intentional simplification)

**Why Not:** The storage backend is a single MikroTik RDS server. Adding controller high availability (multiple replicas with leader election) doesn't improve storage availability. If RDS fails, storage is unavailable regardless of controller replica count.

**Trade-off:** During controller pod restarts (~10s), new volume provisioning/deletion is unavailable. Existing volumes remain accessible. This is acceptable for homelab and small cluster deployments.

**When This Matters:** Large multi-tenant clusters with high provisioning rates. For those use cases, distributed storage (Longhorn, Ceph) is a better fit.

### Volume Encryption

**Status:** ❌ Not Supported (RouterOS limitation)

**Why Not:** Volume encryption would need to be implemented at the RouterOS level (LUKS, dm-crypt) or within the Btrfs pool configuration. RouterOS doesn't currently expose encryption configuration through the CLI for NVMe/TCP exported volumes.

**Workaround:** Use network encryption (VPN/IPSec for NVMe/TCP traffic), encrypt at application layer, or encrypt the entire Btrfs pool on RDS (if RouterOS supports it - requires manual configuration outside CSI driver).

**When This Matters:** Compliance requirements (HIPAA, PCI-DSS) that mandate encryption at rest. For those use cases, use a driver with native encryption support (AWS EBS CSI, encrypted Longhorn volumes).

### Multipath Support

**Status:** ❌ Not Applicable (single storage endpoint)

**Why Not:** Multipath is used to provide redundant paths to the same storage target. nvmet-csi connects to a single RDS server with a single storage IP. There's no secondary path to configure.

**Architectural Note:** Dual-IP configuration (management vs storage network) is for traffic separation, not multipath failover. If the storage network fails, there's no alternate path to the NVMe/TCP target.

### Ephemeral Volumes

**Status:** ❌ Not Supported

**Why Not:** CSI ephemeral volumes (CSIInlineVolume) require support for per-pod volume provisioning without PVC. nvmet-csi assumes PVC-based provisioning with volume lifecycle tied to PVC, not pod.

**Workaround:** Create PVC with `reclaimPolicy: Delete` for similar behavior. PVC deletion triggers volume deletion.

**Impact:** Pods can't use inline CSI volume specification. Must create PVC first.

## Roadmap & Future Features

### RouterOS Snapshot Validation

**Status:** Implemented and automated-tested; real-RouterOS TC-08 pending.

The RouterOS adapter uses `/disk add type=file copy-from=...` to create an
independent file-backed snapshot without NVMe export fields. Delete removes the
snapshot disk entry and backing file. Restore creates a writable, exported
volume on the same backend. TC-08 must prove create, list, restore, data
integrity, and cleanup on real hardware before this is called live-validated.

### Linux-ZOL Snapshots

**Status:** Unsupported.

The Linux-ZOL adapter explicitly rejects create, delete, and restore and returns
no snapshots. ZFS snapshot support requires a separate design covering naming,
ownership, restore semantics, capacity, reconciliation, and CSI behavior; it
must not be inferred from RouterOS support.

### Snapshot Routing and Advertisement Hardening

Before snapshots are enabled in a mixed-backend driver instance:

1. Resolve snapshot creation from authoritative source-volume ownership.
2. Prove a Linux-ZOL source can never invoke a RouterOS mutation.
3. Return a precise unsupported error for Linux-ZOL.
4. Decide whether snapshot capabilities should be advertised only by
   RouterOS-only driver instances.
5. Gate the snapshot sidecar/class consistently in Helm.

### Remaining Live Validation

- RouterOS snapshot TC-08 and scheduled snapshot retention.
- RouterOS NVMe reconnect and node-failure cases TC-09 and TC-11.
- RouterOS restart TC-10 only in an approved maintenance window.
- Linux-ZOL consumer-node drain and KubeVirt churn.
### Possible Future Features

**RouterOS API Support** (not scheduled)
- Replace SSH CLI with RouterOS REST API for faster control plane operations
- Requires RouterOS API reverse engineering or official SDK

**Prometheus Metrics Expansion** (not scheduled)
- Per-volume latency tracking
- NVMe device statistics export
- RDS capacity forecasting

**Multi-RDS Support** (not planned)
- StorageClass per RDS instance
- Cross-RDS snapshot transfer
- Topology-aware scheduling with multiple RDS servers

See [ROADMAP.md](../ROADMAP.md) for current milestone status and release history.

## Related Documentation

- **[Architecture](architecture.md)** - System design and component interactions
- **[Testing Guide](TESTING.md)** - Test procedures and infrastructure
- **[README.md](../README.md)** - Known Limitations section
- **[ROADMAP.md](../ROADMAP.md)** - Development timeline and milestone history

## Conclusion

The nvmet-csi driver is a production-ready storage driver for Kubernetes environments using MikroTik ROSE Data Server. It excels in its target niche: single-server NVMe/TCP storage for homelabs and small clusters where low latency and hardware integration matter more than distributed storage features.

**Choose nvmet-csi when:**
- You have MikroTik RDS hardware and want native Kubernetes integration
- Low-latency NVMe/TCP storage is more important than storage HA
- You value debuggability and auditability (SSH-based management)
- KubeVirt workloads are a primary use case

**Choose an alternative when:**
- You need distributed storage with automated replication (→ Longhorn, Ceph)
- Controller HA is critical for high-volume provisioning (→ cloud-native CSI drivers)
- Multi-attach (RWX) volumes are required (→ NFS-based or distributed storage)
- Compliance requires encryption at rest (→ drivers with native encryption)

For deployment assistance, see the [Kubernetes Setup Guide](kubernetes-setup.md) and [Hardware Validation Guide](HARDWARE_VALIDATION.md).
