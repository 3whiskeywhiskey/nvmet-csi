# RDS CSI Driver Architecture

This document describes the system architecture, component interactions, and design decisions for the RDS CSI Driver.

## System Overview

The RDS CSI Driver is a Kubernetes-native storage driver that enables dynamic provisioning of persistent block volumes backed by MikroTik ROSE Data Server (RDS) via NVMe over TCP.

```
┌───────────────────────────────────────────────────────────────────────────────┐
│ Kubernetes Cluster                                                            │
│                                                                               │
│  ┌─────────────────┐        ┌──────────────────┐        ┌─────────────────┐ │
│  │   Application   │        │   Application    │        │   KubeVirt VM   │ │
│  │      Pod        │        │      Pod         │        │   (virt-        │ │
│  │                 │        │                  │        │    launcher)    │ │
│  └────────┬────────┘        └────────┬─────────┘        └────────┬────────┘ │
│           │ VolumeMount              │ VolumeMount               │ Volume    │
│           │                          │                           │  Mount    │
│  ┌────────▼──────────────────────────▼───────────────────────────▼────────┐  │
│  │                         Kubelet (Volume Manager)                       │  │
│  │                                                                         │  │
│  └────────────────────────────────┬────────────────────────────────────────┘  │
│                                   │ CSI gRPC                                  │
│                                   │                                           │
│  ┌────────────────────────────────▼────────────────────────────────────────┐ │
│  │ RDS CSI Node Plugin (DaemonSet on every worker node)                   │ │
│  │                                                                         │ │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌────────────────────┐   │ │
│  │  │  Node Service    │  │  Identity Service│  │  Health Monitor    │   │ │
│  │  │  - NodeStage     │  │  - GetPluginInfo │  │  - Liveness Probe  │   │ │
│  │  │  - NodeUnstage   │  │  - Probe         │  │  - Metrics         │   │ │
│  │  │  - NodePublish   │  │  - GetCapabilities│  │                    │   │ │
│  │  │  - NodeUnpublish │  │                  │  │                    │   │ │
│  │  └──────────────────┘  └──────────────────┘  └────────────────────┘   │ │
│  │           │ NVMe/TCP                │ /dev/nvmeXnY                      │ │
│  └───────────┼─────────────────────────┼────────────────────────────────────┘ │
│              │                         │                                      │
│  ┌───────────▼─────────────────────────▼────────────────────────────────────┐ │
│  │                    Persistent Volume Claims (PVCs)                       │ │
│  └──────────────────────────────────┬────────────────────────────────────────┘ │
│                                     │ CSI gRPC                                │
│  ┌──────────────────────────────────▼────────────────────────────────────────┐ │
│  │ RDS CSI Controller (Deployment, 1 replica)                               │ │
│  │                                                                           │ │
│  │  ┌────────────────────┐  ┌────────────────────┐  ┌────────────────────┐ │ │
│  │  │ Controller Service │  │  Identity Service  │  │  Volume Manager    │ │ │
│  │  │ - CreateVolume     │  │  - GetPluginInfo   │  │  - ID Generation   │ │ │
│  │  │ - DeleteVolume     │  │  - Probe           │  │  - Metadata Store  │ │ │
│  │  │ - GetCapacity      │  │  - GetCapabilities │  │                    │ │ │
│  │  │ - ValidateVolCap   │  │                    │  │                    │ │ │
│  │  └──────────┬─────────┘  └────────────────────┘  └────────────────────┘ │ │
│  │             │ SSH (RouterOS CLI)                                         │ │
│  └─────────────┼────────────────────────────────────────────────────────────┘ │
│                │                                                              │
└────────────────┼──────────────────────────────────────────────────────────────┘
                 │
                 │ SSH Port 22: Volume Management
                 │ NVMe/TCP Port 4420: Block Device Access
                 │
┌────────────────▼──────────────────────────────────────────────────────────────┐
│ MikroTik ROSE Data Server (RDS) - 10.42.68.1                                 │
│                                                                               │
│  ┌────────────────────────────────────────────────────────────────────────┐  │
│  │ RouterOS CLI (SSH Management Interface)                                │  │
│  │  /disk add type=file file-path=... file-size=... nvme-tcp-export=yes  │  │
│  │  /disk remove ...                                                      │  │
│  │  /disk print detail                                                    │  │
│  └────────────────────────────────────────────────────────────────────────┘  │
│                                     │                                         │
│  ┌──────────────────────────────────▼──────────────────────────────────────┐ │
│  │ NVMe/TCP Subsystem                                                      │ │
│  │  - Exports file-backed disks as NVMe namespaces                        │ │
│  │  - NQN: nqn.2000-02.com.mikrotik:<volume-id>                           │ │
│  │  - Port: 4420 (default)                                                │ │
│  └──────────────────────────────────────────────────────────────────────────┘ │
│                                     │                                         │
│  ┌──────────────────────────────────▼──────────────────────────────────────┐ │
│  │ Btrfs RAID6 Storage Pool                                                │ │
│  │  /storage-pool/kubernetes-volumes/                                      │ │
│  │    ├── pvc-<uuid-1>.img  (50GB)                                        │ │
│  │    ├── pvc-<uuid-2>.img  (100GB)                                       │ │
│  │    └── pvc-<uuid-3>.img  (200GB)                                       │ │
│  └──────────────────────────────────────────────────────────────────────────┘ │
│                                                                               │
│  Hardware: 10x 960GB NVMe SSDs in RAID6 (7.68TB usable)                      │
└───────────────────────────────────────────────────────────────────────────────┘
```

## Component Details

### Backend Model

The CSI node plugin is intentionally backend-agnostic. It consumes generic
NVMe/TCP publish context (`nvme_address`, `nvme_port`, `nvme_nqn`, filesystem and
reconnect settings), connects to the target, discovers the block device, and
stages/publishes it for kubelet. The node path should not need to know whether a
target was created by RouterOS, Linux nvmet, a local controller pod, or SSH.

The CSI controller owns backend-specific provisioning. StorageClass parameters
select a backend type and executor:

- `backend=routeros-rds`, `executor=ssh`: compatibility path for MikroTik
  RouterOS/RDS. This is the current implemented backend.
- `backend=linux-zol-nvmet`, `executor=local`: planned kube-native path where the
  controller pod is scheduled onto the ZFS/nvmet host and manipulates zvols and
  configfs locally.
- `backend=linux-zol-nvmet`, `executor=ssh`: planned remote-host path, similar to
  democratic-csi's trusted SSH model.

Current controller code routes through a generic backend interface, with
RouterOS/RDS adapted behind that interface. Unsupported backend selections are
rejected during provisioning instead of silently falling back to RDS.

The Linux ZoL/nvmet backend package exists as an offline-testable skeleton. It
normalizes a volume request into a zvol device path, observes existing zvol and
nvmet state before mutating, and plans only create, resize, delete, no-op, or
conflict actions. Its command-backed host emits conservative `zfs` and configfs
operations through either a local executor or an SSH executor. Controller
factory wiring and live-cluster deployment are intentionally separate follow-up
work so the command/state model can be tested without pointing at a real storage
host.

### CSI Controller Plugin

**Deployment**: Kubernetes Deployment (1 replica, no horizontal scaling needed)

**Responsibilities**:
1. **Volume Lifecycle Management**
   - Create volumes by SSH'ing to RDS and executing `/disk add` commands
   - Delete volumes and clean up backing files
   - Validate volume capabilities (access modes, filesystem types)
   - Query available capacity from RDS

2. **Volume Metadata**
   - Generate unique volume IDs (UUID v4)
   - Map volume ID to NVMe subsystem NQN
   - Store volume parameters (size, path, NQN)

3. **RDS Communication**
   - Maintain SSH connection pool to RDS
   - Execute RouterOS CLI commands
   - Parse command output
   - Handle errors and retries

**Container Sidecars**:
- **csi-provisioner**: Watches PVCs, calls CreateVolume/DeleteVolume
- **csi-resizer**: Handles volume expansion (post-v1.0)
- **csi-snapshotter**: Handles snapshots (post-v1.0)
- **livenessprobe**: Monitors controller health

**Key Operations**:

#### CreateVolume Flow
```
1. Receive CreateVolume gRPC call from csi-provisioner
   - Parameters: size, fsType, StorageClass parameters

2. Generate unique volume ID: pvc-<uuid>

3. SSH to RDS and execute:
   /disk add type=file \
     file-path=/storage-pool/kubernetes-volumes/pvc-<uuid>.img \
     file-size=<size> \
     nvme-tcp-export=yes \
     nvme-tcp-server-port=4420 \
     nvme-tcp-server-nqn=nqn.2000-02.com.mikrotik:pvc-<uuid>

4. Verify disk was created:
   /disk print detail where slot=pvc-<uuid>

5. Return VolumeContext:
   - volumeID: pvc-<uuid>
   - rdsAddress: 10.42.68.1
   - nvmePort: 4420
   - nqn: nqn.2000-02.com.mikrotik:pvc-<uuid>
```

#### DeleteVolume Flow
```
1. Receive DeleteVolume gRPC call from csi-provisioner
   - Parameters: volumeID

2. SSH to RDS and execute:
   /disk remove [find slot=pvc-<uuid>]

3. Verify file was deleted:
   Check that /storage-pool/kubernetes-volumes/pvc-<uuid>.img is gone

4. Return success
```

### CSI Node Plugin

**Deployment**: Kubernetes DaemonSet (runs on every worker node with volumes)

**Responsibilities**:
1. **Device Discovery**
   - Connect to NVMe/TCP targets using `nvme connect`
   - Wait for block device to appear (`/dev/nvmeXnY`)
   - Disconnect on volume unstage

2. **Filesystem Operations**
   - Format new volumes (ext4, xfs, etc.)
   - Mount volumes to staging path
   - Bind mount from staging to pod path
   - Unmount on pod deletion

3. **Health Monitoring**
   - Check NVMe connection status
   - Detect stale mounts
   - Report node capabilities

**Container Sidecars**:
- **node-driver-registrar**: Registers CSI driver with kubelet
- **livenessprobe**: Monitors node plugin health

**Key Operations**:

#### NodeStageVolume Flow
```
1. Receive NodeStageVolume gRPC call from kubelet
   - Parameters: volumeID, volumeContext (NQN, address, port), stagingPath

2. Connect to NVMe/TCP target:
   nvme connect -t tcp \
     -a <rdsAddress> \
     -s <nvmePort> \
     -n <nqn>

3. Wait for device to appear:
   Poll for /dev/nvme* matching the NQN
   Timeout after 30 seconds

4. Identify block device path (e.g., /dev/nvme1n1)

5. Format filesystem if not already formatted:
   mkfs.<fsType> /dev/nvme1n1

6. Mount to staging path:
   mount /dev/nvme1n1 <stagingPath>

7. Return success
```

#### NodePublishVolume Flow
```
1. Receive NodePublishVolume gRPC call from kubelet
   - Parameters: volumeID, stagingPath, targetPath

2. Create target directory if needed:
   mkdir -p <targetPath>

3. Bind mount from staging to target:
   mount --bind <stagingPath> <targetPath>

4. Apply filesystem options (ro/rw, etc.)

5. Return success
```

#### NodeUnstageVolume Flow
```
1. Receive NodeUnstageVolume gRPC call from kubelet
   - Parameters: volumeID, stagingPath

2. Unmount from staging path:
   umount <stagingPath>

3. Disconnect from NVMe/TCP target:
   nvme disconnect -n <nqn>

4. Verify device is gone:
   Check /sys/class/nvme/

5. Return success
```

## Volume Lifecycle

```
┌──────────────────────────────────────────────────────────────────────────┐
│ Volume Lifecycle States                                                  │
└──────────────────────────────────────────────────────────────────────────┘

1. PVC Creation
   User creates PVC with storageClassName: rds-nvme-tcp
   ↓
2. Provisioning (Controller)
   csi-provisioner calls CreateVolume
   Controller SSHs to RDS, creates file-backed NVMe export
   ↓
3. Volume Bound
   PV created and bound to PVC
   ↓
4. Pod Scheduling
   Pod using PVC is scheduled to node
   ↓
5. Volume Attachment (Node)
   kubelet calls NodeStageVolume
   Node plugin connects to NVMe/TCP target, formats, mounts to staging path
   ↓
6. Volume Mounted (Node)
   kubelet calls NodePublishVolume
   Node plugin bind-mounts to pod path
   ↓
7. Pod Running
   Application accesses volume at /data (or mountPath)
   ↓
8. Pod Deletion
   Pod is deleted or evicted
   ↓
9. Volume Unmount (Node)
   kubelet calls NodeUnpublishVolume (unbind mount)
   kubelet calls NodeUnstageVolume (disconnect NVMe, unmount staging)
   ↓
10. PVC Deletion
   User deletes PVC
   ↓
11. Deprovisioning (Controller)
   csi-provisioner calls DeleteVolume
   Controller SSHs to RDS, removes NVMe export and backing file
   ↓
12. Volume Deleted
   PV and PVC removed from cluster
```

## Design Decisions

### 1. File-backed Volumes vs. LVM

**Decision**: Use file-backed disk images stored on Btrfs

**Rationale**:
- RDS does not support LVM or thin provisioning
- File-backed volumes are directly supported by RouterOS
- Btrfs provides efficient space allocation
- Simpler to implement and debug

**Trade-offs**:
- Slight performance overhead vs. raw block devices
- Acceptable for most workloads (<5% overhead)
- Future: Could add raw device support if needed

### 2. SSH-based Management vs RouterOS API

**Decision**: Use SSH with RouterOS CLI for volume operations (with API support planned for future)

**Context**: MikroTik RouterOS provides two management interfaces:
1. **SSH + CLI**: Text-based command-line interface over SSH (port 22)
2. **RouterOS API**: Binary protocol over TCP (port 8728/8729)

**Analysis**:

*Advantages of RouterOS API*:
- **Performance**: 2-3x faster (~50-80ms vs 150-200ms per operation)
- **Structured data**: Binary protocol with key-value pairs (no text parsing)
- **Error handling**: Structured error codes (categories 0-7) vs parsing stderr
- **Concurrency**: Tagged requests allow parallel operations
- **Efficiency**: Less bandwidth overhead than text-based CLI

*Advantages of SSH + CLI*:
- **Security**: Public key authentication (asymmetric) vs plain-text passwords
- **Maturity**: SSH is battle-tested, industry standard for device management
- **Debugging**: Easy to test commands manually (`ssh admin@rds "/disk print"`)
- **Familiarity**: RouterOS CLI is well-documented and widely used
- **Testing**: Easier to mock (existing 454-line mock SSH server implementation)

**Decision Rationale**:
1. **Security is paramount**: SSH with public key auth is more secure than API with plain passwords (even with TLS)
2. **Performance not critical**: Volume operations are infrequent (minutes to hours apart, not milliseconds)
3. **Time to market**: SSH implementation is working and tested (600 lines of code)
4. **Risk management**: SSH is proven technology; API adds complexity during alpha stage
5. **Functionality parity**: Both approaches execute the same RouterOS commands

**Implementation**:
- SSH client uses `golang.org/x/crypto/ssh` with retry logic
- CLI commands: `/disk add`, `/disk remove`, `/disk print detail`
- Response parsing with regex patterns for key-value extraction
- Exponential backoff retry for transient errors

**Future Migration Path**:
- Code architected with `RDSClient` interface abstraction
- Can add API implementation alongside SSH in future releases
- Users can choose protocol via config (`Protocol: "ssh"` or `"api"`)
- Hybrid approach possible: SSH for management (secure), API for monitoring (fast)
- Migration would take ~20-30 hours of development effort

**When to Reconsider API**:
- Performance becomes critical (hundreds of volume ops per minute)
- RouterOS adds key-based authentication to API
- Need for real-time volume monitoring alongside management
- API-only features are added by MikroTik

**References**:
- go-routeros library: https://github.com/go-routeros/routeros (v3.0.1, MIT license)
- RouterOS API docs: https://help.mikrotik.com/docs/spaces/ROS/pages/47579160/API

**Trade-offs**:
- Parsing CLI output is less reliable than structured API
- Requires maintaining SSH connection pool
- Mitigated by: Robust parsing with regex, error handling, retries, test coverage

### 3. Single Controller Replica

**Decision**: Run only one controller replica (no HA)

**Rationale**:
- Volume operations are infrequent (not latency-critical)
- RDS is single-server (no distributed storage)
- Leader election adds complexity without significant benefit
- Deployment restarts are fast (<10s)

**Trade-offs**:
- Brief unavailability during controller restarts
- Acceptable for homelab use case
- Future: Add leader election for production environments

### 4. NVMe/TCP vs. iSCSI

**Decision**: Use NVMe/TCP for block device protocol

**Rationale**:
- Lower latency than iSCSI (~1ms vs ~3ms)
- Higher throughput (2+ GB/s vs 1 GB/s)
- Native kernel support in Linux 5.0+
- RDS has first-class NVMe/TCP support

**Trade-offs**:
- Requires modern kernel (5.0+)
- Less mature ecosystem than iSCSI
- Acceptable for target environment (NixOS 24.05+)

### 5. WaitForFirstConsumer Binding

**Decision**: Default to WaitForFirstConsumer volume binding mode

**Rationale**:
- Ensures volume is created when pod is scheduled
- Avoids provisioning volumes on inaccessible nodes
- Standard practice for CSI drivers

**Implementation**:
- StorageClass: `volumeBindingMode: WaitForFirstConsumer`
- No topology awareness initially (all nodes can access RDS)
- Future: Add topology keys for storage network zones

## Security Considerations

### SSH Key Management
- **Storage**: SSH private key stored in Kubernetes Secret
- **Mount**: Secret mounted to controller pod at `/etc/rds-csi/ssh-key`
- **Permissions**: File mode 0600, owned by driver process user
- **Rotation**: Manual key rotation via secret update + pod restart

### RBAC Permissions
Controller requires:
- `persistentvolumes`: get, list, watch, create, delete, patch
- `persistentvolumeclaims`: get, list, watch, update
- `storageclasses`: get, list, watch
- `events`: create, update, patch
- `secrets`: get (for SSH key access)

Node plugin requires:
- `nodes`: get (for NodeGetInfo)
- `secrets`: get (if per-node config needed)

### Network Isolation
- **Control Plane**: Controller communicates with RDS via SSH (port 22)
- **Data Plane**: Nodes communicate with RDS via NVMe/TCP (port 4420)
- **Recommendation**: Use separate VLANs for control and data traffic

## Performance Characteristics

### Throughput
- **Sequential Read**: ~2.0 GB/s per volume (limited by NVMe/TCP)
- **Sequential Write**: ~1.8 GB/s per volume
- **Random Read (4K)**: ~150K IOPS
- **Random Write (4K)**: ~50K IOPS
- **Aggregate**: Limited by RDS uplink (25 Gbps)

### Latency
- **Volume Creation**: 10-30 seconds (SSH overhead + disk allocation)
- **Volume Deletion**: 5-15 seconds (SSH overhead + cleanup)
- **NVMe Connect**: 2-5 seconds (target discovery + connection)
- **NVMe Disconnect**: 1-2 seconds (cleanup)
- **I/O Latency**: 1-3ms (network + RDS processing)

### Scalability
- **Max Volumes per RDS**: Limited by filesystem capacity (~7TB usable / avg volume size)
- **Max Concurrent Mounts per Node**: Limited by kernel (typically 100+)
- **Max Volumes per Cluster**: Tested up to 100 (no hard limit)

## Error Handling

### SSH Connection Failures
- **Detection**: Connection timeout (10s), authentication failure
- **Retry**: Exponential backoff (1s, 2s, 4s, 8s, 16s max)
- **Max Retries**: 5 attempts
- **User Feedback**: Event logged to PVC, visible in `kubectl describe pvc`

### NVMe Connection Failures
- **Detection**: `nvme connect` returns non-zero, device doesn't appear
- **Retry**: 3 attempts with 5s delay
- **Fallback**: Return error to kubelet, volume stays in Pending
- **Recovery**: User can delete PVC, driver will clean up on RDS

### Disk Full on RDS
- **Detection**: `/disk add` returns error "not enough space"
- **Handling**: Return CSI error code `ResourceExhausted`
- **User Feedback**: PVC stays Pending with event "insufficient storage"
- **Resolution**: Admin must free space or provision more storage

### Orphaned Volumes
- **Scenario**: Driver crashes mid-operation, volume created but not tracked
- **Detection**: Manual audit via `kubectl get pv` vs `/disk print` on RDS
- **Cleanup**: Manual deletion via SSH to RDS (future: automated reconciliation)

## Monitoring and Observability

### Metrics (Prometheus)
- `rds_csi_volume_operations_total{operation, status}`: Volume op counter
- `rds_csi_volume_operation_duration_seconds{operation}`: Operation latency histogram
- `rds_csi_ssh_connection_errors_total`: SSH connection failure counter
- `rds_csi_nvme_connection_errors_total{node}`: NVMe connection failure counter
- `rds_csi_volume_capacity_bytes`: Total/used/available capacity

### Logging
- **Level**: Info (default), Debug (via command line flag)
- **Format**: JSON structured logging (compatible with Loki)
- **Key Fields**: volumeID, operation, node, error, duration

### Health Checks
- **Liveness Probe**: gRPC health check endpoint
- **Readiness Probe**: SSH connectivity test to RDS
- **Startup Probe**: Initial health check (30s timeout)

## Future Enhancements

### Snapshots (v0.2.0)
- Use Btrfs snapshots on RDS: `btrfs subvolume snapshot`
- Implement `CreateSnapshot` / `DeleteSnapshot` CSI methods
- Support VolumeSnapshot/VolumeSnapshotContent CRDs

### Volume Cloning (v0.2.0)
- Copy-on-write clones using Btrfs: `cp --reflink=always`
- Fast VM template provisioning
- Implement `CreateVolume` with `contentSource`

### Topology Awareness (v0.3.0)
- Add topology keys for storage network zones
- Prefer scheduling pods on nodes with direct RDS access
- Handle multi-RDS scenarios (future)

### High Availability (v1.0.0)
- Multiple controller replicas with leader election
- Active-passive failover (< 10s downtime)
- Distributed state management (etcd or custom)

---

**Last Updated**: 2025-11-05
**Version**: 1.0
