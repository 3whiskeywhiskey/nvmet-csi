# RDS CSI Driver

[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)
[![Go Report Card](https://goreportcard.com/badge/github.com/3whiskeywhiskey/rds-csi)](https://goreportcard.com/report/github.com/3whiskeywhiskey/rds-csi)
[![Dev Build](https://github.com/3whiskeywhiskey/rds-csi/actions/workflows/dev.yml/badge.svg)](https://github.com/3whiskeywhiskey/rds-csi/actions/workflows/dev.yml)
[![Main Build](https://github.com/3whiskeywhiskey/rds-csi/actions/workflows/main.yml/badge.svg)](https://github.com/3whiskeywhiskey/rds-csi/actions/workflows/main.yml)

Kubernetes CSI (Container Storage Interface) driver for **MikroTik ROSE Data Server (RDS)** NVMe/TCP storage.

## Overview

The RDS CSI Driver enables dynamic provisioning of persistent block storage volumes for Kubernetes workloads using MikroTik's ROSE Data Server as the storage backend. It leverages NVMe over TCP (NVMe/TCP) for high-performance remote block device access.

### Features

- **Dynamic Volume Provisioning**: Automatically create storage volumes on demand via PersistentVolumeClaims
- **NVMe/TCP Protocol**: Low-latency, high-throughput block storage access
- **File-backed Volumes**: Efficient storage allocation using file-backed disk images on Btrfs RAID
- **SSH-based Management**: Secure remote administration using RouterOS CLI
- **Volume Expansion**: Dynamically resize volumes (ControllerExpandVolume, NodeExpandVolume)
- **Block Volume Support**: CSI block volumes for KubeVirt VMs without filesystem formatting
- **KubeVirt Live Migration**: VM migration validated with ~15s migration window
- **NVMe-oF Reconnection Resilience**: Volumes remain accessible after network hiccups and RDS restarts
- **Attachment Reconciliation**: Automatic recovery from stale VolumeAttachment state after infrastructure failures
- **Orphan Volume Reconciliation**: Automatic detection and cleanup of orphaned volumes (optional)
- **Enhanced Error Handling**: Comprehensive retry logic, idempotent operations, and audit logging
- **Snapshots**: Btrfs-based volume snapshots (planned)
- **Volume Cloning**: Fast volume duplication for rapid VM provisioning (planned)

### Architecture

```
┌─────────────────────────────────────────────────────────────┐
│ Kubernetes Cluster                                          │
│                                                             │
│  ┌──────────────┐         ┌─────────────────────────────┐ │
│  │  KubeVirt VM │────────▶│  PVC Request                │ │
│  │  StatefulSet │         │  (StorageClass: rds-nvme)   │ │
│  │  or Pod      │         └──────────┬──────────────────┘ │
│  └──────────────┘                    │                     │
│                                      ▼                      │
│                     ┌────────────────────────────────┐     │
│                     │  RDS CSI Driver                │     │
│                     │  - Controller (Deployment)     │     │
│                     │  - Node Plugin (DaemonSet)     │     │
│                     └──────────┬─────────────────────┘     │
│                                │                            │
└────────────────────────────────┼────────────────────────────┘
                                 │
              ┌──────────────────┼──────────────────┐
              │                  │                  │
          SSH │ (Management)     │ (Data Plane)     │ NVMe/TCP
        :22   │                  │                  │ :4420
              ▼                  ▼                  ▼
   ┌────────────────────────────────────────────────────┐
   │  MikroTik RDS                                      │
   │  - Management IP: 10.42.241.3 (1Gbit)             │
   │  - Storage IP: 10.42.68.1 (25Gbit)                │
   │  - Btrfs RAID Storage                             │
   │  - NVMe/TCP Export                                │
   │  - File-backed Volumes                            │
   └────────────────────────────────────────────────────┘
```

**Key Architecture Features:**

- **Dual-IP Design**: Separates management (SSH) and data plane (NVMe/TCP) traffic
  - Management interface (1Gbit): Controller uses for volume provisioning via SSH
  - Storage interface (25Gbit): Node uses for high-speed NVMe/TCP block access
- **Two-Phase Mounting**:
  - **Stage**: Node connects NVMe/TCP, formats filesystem, mounts to staging path
  - **Publish**: Bind mount from staging to pod-specific path
- **File-backed Volumes**: Btrfs sparse files with NVMe/TCP export for flexibility
- **Multi-arch Support**: Runs on both amd64 and arm64 nodes

## Status

**Current Version**: v0.8.0 (v0.9.0 in progress)

**Completed:**
- [x] Project setup and documentation
- [x] CSI Identity service
- [x] Controller service (CreateVolume, DeleteVolume, ValidateVolumeCapabilities, GetCapacity)
- [x] Node service (NodeStageVolume, NodePublishVolume, NodeUnstageVolume, NodeUnpublishVolume)
- [x] Kubernetes deployment manifests and RBAC
- [x] E2E testing in production cluster
- [x] Volume expansion support
- [x] Block volume support for KubeVirt
- [x] KubeVirt live migration validation
- [x] Orphan reconciliation
- [x] NVMe-oF reconnection resilience
- [x] Attachment reconciliation
- [ ] Helm chart (planned)
- [x] Prometheus metrics endpoint
- [x] Comprehensive test coverage (65%+)

**In Progress (v0.9.0):**
- [ ] Volume snapshots (Phase 26)
- [ ] Documentation & hardware validation (Phase 27)

See [ROADMAP.md](ROADMAP.md) for detailed milestone history and current progress.

## Prerequisites

### RDS Requirements
- **MikroTik RouterOS**: Version 7.1+ with ROSE Data Server
- **Storage Backend**: Btrfs filesystem (typically RAID configuration)
- **NVMe/TCP Support**: Enabled and configured
- **SSH Access**: Public key authentication configured

### Kubernetes Cluster
- **Kubernetes Version**: 1.26+
- **CSI Spec**: v1.5.0+ support
- **Kernel**: Linux 5.0+ with NVMe-TCP kernel module (`nvme-tcp`)
- **nvme-cli**: Installed on all worker nodes (for NVMe/TCP operations)

### Network
- Worker nodes must have network connectivity to RDS on storage VLAN
- NVMe/TCP port accessible (default: 4420)
- SSH port accessible for controller (default: 22)

## Quick Start

### Installation

```bash
# Apply manifests
kubectl apply -f deploy/kubernetes/

# Create SSH key secret
kubectl create secret generic rds-ssh-key \
  --from-file=id_rsa=/path/to/rds-ssh-key \
  --namespace kube-system
```

### Usage Example

#### Create StorageClass

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: rds-nvme-tcp
provisioner: rds.csi.srvlab.io
parameters:
  # RDS connection details
  rdsAddress: "10.42.68.1"
  nvmePort: "4420"
  sshPort: "22"

  # Volume parameters
  fsType: "ext4"
  volumePath: "/storage-pool/kubernetes-volumes"

  # NVMe parameters
  nqnPrefix: "nqn.2000-02.com.mikrotik"

reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

#### Create PersistentVolumeClaim

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: my-volume
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 50Gi
```

#### Use in Pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
    - name: app
      image: nginx
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: my-volume
```

## Configuration

### StorageClass Parameters

| Parameter | Description | Default | Required |
|-----------|-------------|---------|----------|
| `rdsAddress` | RDS management IP address (SSH) | - | Yes (via ConfigMap) |
| `nvmeAddress` | RDS storage IP address (NVMe/TCP data plane) | Same as `rdsAddress` | No |
| `nvmePort` | NVMe/TCP target port | `4420` | No |
| `sshPort` | SSH port for management | `22` | No |
| `fsType` | Filesystem type (ext4, xfs, ext3) | `ext4` | No |
| `volumePath` | Base path for volumes on RDS | `/storage-pool/metal-csi` | No |
| `nqnPrefix` | NVMe Qualified Name prefix | `nqn.2000-02.com.mikrotik` | No |

**Note**: `nvmeAddress` allows using a separate high-speed network for storage traffic while management operations use `rdsAddress`.

### Driver Configuration

See [docs/configuration.md](docs/configuration.md) for comprehensive configuration reference.

## Known Limitations

### RouterOS Version Compatibility
**Requires:** RouterOS 7.1+ with ROSE Data Server feature enabled
**Impact:** Cannot deploy on RouterOS 6.x, CHR (Cloud Hosted Router), or non-RDS RouterOS
**Detection:** Controller logs "SSH connection failed" or unexpected command output
**Workaround:** Ensure RDS hardware with RouterOS 7.16+ for full feature support

### NVMe Device Timing Assumptions
**Requires:** NVMe block device appears within 30 seconds of `nvme connect`
**Impact:** On congested networks or heavily loaded RDS, NodeStageVolume may timeout
**Detection:** Node plugin logs "timeout waiting for NVMe device" or pod stuck in ContainerCreating
**Workaround:** Check network latency to RDS storage IP; restart node plugin pod if transient

### Dual-IP Architecture
**Recommended:** Separate management (SSH) and storage (NVMe/TCP) network interfaces
**Impact:** Single-IP deployments work but SSH management traffic shares bandwidth with storage I/O
**Detection:** Higher than expected I/O latency; management operations slow during heavy I/O
**Workaround:** Configure `nvmeAddress` in StorageClass to use dedicated storage interface. Management via `rdsAddress` can use lower-bandwidth interface.

### Single Controller Instance
**Design:** One controller replica (no HA leader election)
**Impact:** Brief volume provisioning unavailability during controller pod restarts (~10s)
**Detection:** PVC stuck Pending during controller restart
**Workaround:** Existing volumes remain accessible; only new provisioning/deletion is affected during restart

### Access Mode Restrictions
**Supported:** ReadWriteOnce (RWO) only
**Impact:** Cannot mount a volume on multiple nodes simultaneously
**Detection:** PVC with ReadWriteMany will fail to provision
**Workaround:** Use one PVC per pod; for shared storage needs, consider alternative solutions

### Volume Size Minimum
**Minimum:** Volume size must be at least 1 GiB
**Impact:** Requests below 1 GiB will be rounded up by the driver
**Detection:** Created volume size may differ from requested size for sub-1GiB requests

For a comprehensive comparison with other CSI drivers, see [Capabilities Analysis](docs/CAPABILITIES.md).

## Kubernetes Deployment

### Prerequisites

- Kubernetes v1.26+ with CSI support
- Linux nodes with kernel 5.0+ (`nvme-tcp` module)
- `nvme-cli` installed on all nodes
- Network connectivity from nodes to RDS

### Quick Deploy

```bash
# 1. Update RDS credentials and configuration
vi deploy/kubernetes/controller.yaml

# 2. Deploy the driver
./deploy/kubernetes/deploy.sh

# 3. Create test PVC and Pod
kubectl apply -f examples/pvc.yaml
kubectl apply -f examples/pod.yaml
```

For detailed installation instructions, see [Kubernetes Setup Guide](docs/kubernetes-setup.md).

## Documentation

- **[Hardware Validation Guide](docs/HARDWARE_VALIDATION.md)** - Step-by-step test procedures for production RDS hardware
- **[Kubernetes Setup Guide](docs/kubernetes-setup.md)** - Complete deployment guide
- **[Capabilities & Comparison](docs/CAPABILITIES.md)** - Feature comparison with AWS EBS CSI and Longhorn
- [Design](DESIGN.md) - Refactor stance, migration map, and backend contract sketch
- [Architecture](docs/architecture.md) - System design and component interactions
- [RouterOS RDS Hardening Notes](docs/routeros-rds-hardening.md) - Fragility, churn, and refactor safety requirements
- [nvmet-csi Live Validation Plan](docs/nvmet-csi-live-validation.md) - Side-by-side rollout plan for ZoL and RDS compatibility testing
- [RDS Commands Reference](docs/rds-commands.md) - RouterOS CLI commands used
- [CI/CD Pipeline](docs/ci-cd.md) - Automated builds, releases, and versioning
- [Development Guide](CLAUDE.md) - Development notes and context
- [Testing Guide](TESTING.md) - Test procedures

## Troubleshooting

### Check Driver Status

```bash
# Controller pod
kubectl get pods -n kube-system -l app=rds-csi-controller

# Node plugin pods
kubectl get pods -n kube-system -l app=rds-csi-node

# Check logs
kubectl logs -n kube-system -l app=rds-csi-controller -c rds-csi-plugin
```

### Common Issues

**Volume stuck in Pending:**
- Check controller pod logs for RDS connection errors
- Verify SSH key authentication is working
- Ensure RDS has sufficient free space

**Mount failures:**
- Verify NVMe/TCP kernel module is loaded: `lsmod | grep nvme_tcp`
- Check node plugin logs for connection errors
- Verify network connectivity to RDS on storage VLAN

See [docs/troubleshooting.md](docs/troubleshooting.md) for more details.

## Development

### Building from Source

```bash
# Clone repository
git clone https://github.com/3whiskeywhiskey/rds-csi.git
cd rds-csi

# Build binary
make build

# Build container image
make docker

# Run tests
make test

# Run CSI sanity tests
make sanity
```

### Contributing

Contributions are welcome! Please see [CONTRIBUTING.md](CONTRIBUTING.md) for guidelines.

### Testing

See [docs/development.md](docs/development.md) for development environment setup and testing procedures.

## Roadmap

See [ROADMAP.md](ROADMAP.md) for detailed milestone history and current progress.

**Current Focus**: v0.9.0 - Production Readiness & Test Maturity

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](LICENSE) file for details.

## Related Projects

- [Kubernetes CSI Specification](https://github.com/container-storage-interface/spec)
- [MikroTik RouterOS Documentation](https://help.mikrotik.com/docs/spaces/ROS/overview)
- [democratic-csi](https://github.com/democratic-csi/democratic-csi) - Inspiration for SSH-based storage management
- [SPDK CSI](https://github.com/spdk/spdk-csi) - Reference for NVMe/TCP handling

## Support

- **Issues**: [github.com/3whiskeywhiskey/rds-csi/issues](https://github.com/3whiskeywhiskey/rds-csi/issues)
- **Discussions**: Use issue tracker for questions and feature requests

## Acknowledgments

This project was created as part of a homelab infrastructure project for running KubeVirt virtualization on diskless NixOS nodes with centralized MikroTik ROSE Data Server storage.
