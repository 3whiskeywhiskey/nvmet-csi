# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a **Kubernetes CSI (Container Storage Interface) driver** for MikroTik ROSE Data Server (RDS) that provides dynamic provisioning of persistent block storage volumes using NVMe over TCP. The driver is written in Go and is currently in the Foundation phase (Milestone 1).

**Key Technologies:**
- Go 1.24
- Kubernetes CSI Spec v1.5.0+
- NVMe/TCP for data plane (block device access)
- SSH/RouterOS CLI for control plane (volume management)
- Btrfs-backed file volumes on RDS

**Target Environment:**
- Kubernetes 1.26+
- NixOS worker nodes (diskless, network boot)
- MikroTik RouterOS 7.1+ with ROSE Data Server
- Production use case: KubeVirt VMs on NVMe/TCP storage

## Critical RDS Safety Notes

RDS is load-bearing shared infrastructure for routing, MinIO, and NVMe/TCP
storage. Do not reboot RDS, repeatedly restart MinIO, or perform manual
Kubernetes node mount surgery unless the operator explicitly approves it.

During the `nvmet-csi` to `nvmet-csi` refactor, treat RouterOS/RDS as fragile:
avoid add/remove storms, prefer observe-before-mutate reconciliation, rate-limit
RouterOS storage mutations, and make stale attachment/session remediation
explicit and auditable. See `docs/routeros-rds-hardening.md` before changing
controller, attachment, reconciler, RDS, or backend abstraction code.

## Essential Commands

### Build and Development

```bash
# Build for your local OS/architecture (macOS, Linux, etc.)
make build-local

# Build for specific platforms
make build-darwin-amd64   # macOS Intel
make build-darwin-arm64   # macOS Apple Silicon
make build-linux-amd64    # Linux x86_64
make build-linux-arm64    # Linux ARM64

# Build for all platforms
make build-all

# Build the binary for Linux/amd64 (default container target)
make build

# Build Docker image
make docker

# Run unit tests
make test

# Run tests with coverage
make test-coverage

# Run CSI sanity tests (requires csi-sanity installed)
make sanity

# Format code
make fmt

# Run linters
make lint

# Run all verification checks (fmt + vet + lint + test)
make verify

# Clean build artifacts
make clean

# Install development tools
make install-tools
```

**Local Testing:** Use `make build-local` to build a binary you can run on your development machine. The binary will be named like `bin/nvmet-csi-plugin-darwin-arm64` based on your platform.

### Nix Shell for Dependencies

Use `nix-shell` to bring in dependencies like Helm, golangci-lint, or other tools you may need:

```bash
# Enter nix shell with common tools
nix-shell -p go golangci-lint kubectl helm csi-test
```

### Deployment

```bash
# Deploy to Kubernetes via kubectl
make deploy

# Deploy via Helm
make deploy-helm

# Remove from Kubernetes
make undeploy

# View controller logs
make logs-controller

# View node plugin logs
make logs-node
```

### Gitea Integration

Use the Gitea MCP tools to interact with this repository's issues, PRs, etc. The repository is hosted at `git.srvlab.io/whiskey/nvmet-csi`.

## Architecture

### High-Level Structure

```
┌─────────────────────────────────────────────────────────────┐
│ Kubernetes Cluster                                          │
│                                                             │
│  ┌──────────────────────────────────────────────────────┐  │
│  │ nvmet-csi Controller (Deployment)                      │  │
│  │ - CreateVolume via SSH → RDS                         │  │
│  │ - DeleteVolume via SSH → RDS                         │  │
│  │ - Volume ID generation and metadata                  │  │
│  └──────────────┬───────────────────────────────────────┘  │
│                 │ SSH (RouterOS CLI)                       │
│                 │                                          │
│  ┌──────────────▼───────────────────────────────────────┐  │
│  │ nvmet-csi Node Plugin (DaemonSet)                      │  │
│  │ - NodeStageVolume: nvme connect                      │  │
│  │ - NodePublishVolume: mount filesystem                │  │
│  │ - NodeUnstageVolume: nvme disconnect                 │  │
│  └──────────────┬───────────────────────────────────────┘  │
│                 │ NVMe/TCP                                 │
└─────────────────┼──────────────────────────────────────────┘
                  │
┌─────────────────▼──────────────────────────────────────────┐
│ MikroTik RDS (10.42.68.1)                                  │
│ - SSH CLI: /disk add, /disk remove                         │
│ - NVMe/TCP: file-backed volumes on Btrfs RAID6            │
└────────────────────────────────────────────────────────────┘
```

### Code Structure

- **`cmd/nvmet-csi-plugin/`**: Main entry point for both controller and node plugin
- **`pkg/driver/`**: CSI driver implementation (Identity, Controller, Node services)
- **`pkg/rds/`**: SSH client and RouterOS CLI command wrappers
- **`pkg/utils/`**: Utility functions (volume ID generation, NVMe helpers, etc.)
- **`deploy/kubernetes/`**: Raw Kubernetes manifests (RBAC, Deployment, DaemonSet)
- **`deploy/helm/`**: Helm chart for installation
- **`test/`**: E2E tests and test utilities
- **`hack/`**: Build scripts and development tools

### CSI Services

The driver implements three CSI services:

1. **Identity Service** (both controller and node):
   - `GetPluginInfo()`: Returns driver name and version
   - `GetPluginCapabilities()`: Declares supported capabilities
   - `Probe()`: Health check

2. **Controller Service** (runs in controller pod):
   - `CreateVolume()`: SSH to RDS, execute `/disk add` to create file-backed NVMe/TCP volume
   - `DeleteVolume()`: SSH to RDS, execute `/disk remove` to clean up
   - `ValidateVolumeCapabilities()`: Validate access modes (ReadWriteOnce only)
   - `GetCapacity()`: Query available space on RDS
   - `ControllerGetCapabilities()`: Declare controller capabilities

3. **Node Service** (runs in node DaemonSet):
   - `NodeStageVolume()`: Connect to NVMe/TCP target using `nvme connect`, format if needed
   - `NodeUnstageVolume()`: Disconnect from NVMe/TCP target using `nvme disconnect`
   - `NodePublishVolume()`: Bind mount staged volume to pod path
   - `NodeUnpublishVolume()`: Unmount from pod path
   - `NodeGetCapabilities()`: Declare staging support
   - `NodeGetInfo()`: Return node ID and topology

## Volume Lifecycle

Understanding the full volume lifecycle is critical for debugging:

1. **PVC Created** → csi-provisioner calls `CreateVolume()`
2. **Controller** → SSH to RDS, run `/disk add type=file ...` to create file-backed disk with NVMe/TCP export
3. **Volume Bound** → PV created and bound to PVC
4. **Pod Scheduled** → kubelet calls `NodeStageVolume()`
5. **Node Stage** → Run `nvme connect -t tcp -a <rds-ip> -n <nqn>`, wait for `/dev/nvmeXnY`, format filesystem, mount to staging path
6. **Node Publish** → kubelet calls `NodePublishVolume()`, bind mount staging path to pod path
7. **Pod Running** → Application accesses volume
8. **Pod Deleted** → kubelet calls `NodeUnpublishVolume()` then `NodeUnstageVolume()`
9. **Node Cleanup** → Unmount filesystem, run `nvme disconnect`
10. **PVC Deleted** → csi-provisioner calls `DeleteVolume()`
11. **Controller** → SSH to RDS, run `/disk remove [find slot=<volume-id>]`

## RDS RouterOS Commands

The driver uses these key RouterOS CLI commands via SSH:

### Create Volume
```bash
/disk add \
  type=file \
  file-path=/storage-pool/kubernetes-volumes/pvc-<uuid>.img \
  file-size=<size> \
  slot=pvc-<uuid> \
  nvme-tcp-export=yes \
  nvme-tcp-server-port=4420 \
  nvme-tcp-server-nqn=nqn.2000-02.com.mikrotik:pvc-<uuid>
```

### Delete Volume
```bash
/disk remove [find slot=pvc-<uuid>]
```

### List Volumes
```bash
/disk print detail
/disk print detail where slot=pvc-<uuid>
```

### Query Capacity
```bash
/file print detail where name="/storage-pool"
```

See `docs/rds-commands.md` for comprehensive command reference.

## Volume ID and NQN Format

- **Volume ID**: `pvc-<uuid>` (e.g., `pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890`)
- **NQN**: `nqn.2000-02.com.mikrotik:<volume-id>` (e.g., `nqn.2000-02.com.mikrotik:pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890`)
- **File Path**: `<base-path>/<volume-id>.img` (e.g., `/storage-pool/kubernetes-volumes/pvc-<uuid>.img`)

UUID generation ensures uniqueness. The `pvc-` prefix identifies CSI-managed volumes.

## NVMe/TCP Operations

Node plugin uses `nvme-cli` for NVMe/TCP operations:

### Connect to Target
```bash
nvme connect -t tcp -a <rds-ip> -s <port> -n <nqn>
```

### Disconnect from Target
```bash
nvme disconnect -n <nqn>
```

### Discover Targets
```bash
nvme discover -t tcp -a <rds-ip> -s <port>
```

### List NVMe Devices
```bash
nvme list
lsblk | grep nvme
```

Device appears as `/dev/nvmeXnY` after successful connection.

## Key Design Decisions

1. **File-backed Volumes**: Uses file-backed disk images on Btrfs instead of LVM (simpler, RDS-native support)
2. **SSH Management**: Uses SSH + RouterOS CLI instead of REST API (more reliable, better documented)
3. **Single Controller**: One controller replica (no HA/leader election for homelab simplicity)
4. **NVMe/TCP Protocol**: Chosen over iSCSI for lower latency (~1ms vs ~3ms) and higher throughput
5. **WaitForFirstConsumer**: Default binding mode ensures volumes are created when pods are scheduled

## Security Considerations

- **SSH Keys**: Private key stored in Kubernetes Secret, mounted to controller at `/etc/nvmet-csi/ssh-key` with mode 0600
- **Command Injection**: Strict validation of volume IDs (only alphanumeric + hyphen), reject shell metacharacters
- **RBAC**: Controller requires access to PVs, PVCs, StorageClasses, Events, Secrets; Node requires access to Nodes
- **Network Isolation**: Control plane (SSH port 22) and data plane (NVMe/TCP port 4420) should use separate VLANs

## Current Status

**Milestone 1 - Foundation** (Weeks 1-3) - IN PROGRESS

Completed:
- ✅ Project scaffolding and documentation
- ✅ Architecture design

TODO:
- SSH client wrapper for RouterOS CLI (`pkg/rds/`)
- CSI Identity service implementation (`pkg/driver/identity.go`)
- Build system and Docker image
- Unit tests

Next milestone: Controller Service (CreateVolume, DeleteVolume)

See `ROADMAP.md` for detailed implementation plan.

## Error Handling Patterns

### SSH Connection Failures
- Retry with exponential backoff (1s, 2s, 4s, 8s, max 16s)
- Max 5 attempts
- Log events to PVC for user visibility

### NVMe Connection Failures
- Retry 3 times with 5s delay
- Return error to kubelet if all attempts fail
- Volume stays Pending, user can delete and recreate

### Disk Full on RDS
- Detect "not enough space" in SSH command output
- Return CSI error code `ResourceExhausted`
- PVC stays Pending with event

### Command Output Parsing
- Use regex to parse RouterOS CLI output
- Handle malformed output gracefully
- Validate expected fields are present before proceeding

## Testing Strategy

1. **Unit Tests**: Test SSH client, volume ID generation, command parsing
2. **CSI Sanity Tests**: Use `csi-test/cmd/csi-sanity` to validate CSI spec compliance
3. **E2E Tests**: Deploy to real cluster, create/mount/unmount/delete volumes
4. **Manual Testing**: SSH to RDS, verify disk creation/deletion, NVMe connections

## Documentation

- **`README.md`**: Project overview, quick start, installation
- **`docs/architecture.md`**: Detailed system design, component interactions, performance characteristics
- **`docs/rds-commands.md`**: Complete reference of RouterOS CLI commands
- **`ROADMAP.md`**: Development phases, milestones, timeline

## Common Patterns

### Implementing a New CSI Method

1. Add method signature in `pkg/driver/controller.go` or `pkg/driver/node.go`
2. Implement business logic (SSH commands, NVMe operations, filesystem ops)
3. Add comprehensive error handling with specific error messages
4. Log operation with structured fields (volumeID, operation, duration)
5. Add unit tests in `*_test.go`
6. Update CSI sanity tests if needed

### Adding a New RouterOS Command

1. Add command wrapper function in `pkg/rds/commands.go`
2. Implement output parsing with regex
3. Add error detection and retry logic
4. Document command in `docs/rds-commands.md`
5. Add unit test with mock SSH output

### Working with Volume IDs

Always validate volume IDs match pattern `^pvc-[a-f0-9-]+$` to prevent command injection. Use `pkg/utils/volumeid.go` helpers for generation and validation.
- make sure to update roadmap, commit and push changes, and comment on / close issues as you work through milestones
