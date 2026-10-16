# Technology Stack

**Analysis Date:** 2026-01-30

## Languages

**Primary:**
- Go 1.24 - Core CSI driver implementation across all packages (`cmd/rds-csi-plugin`, `pkg/driver`, `pkg/rds`, `pkg/nvme`, `pkg/mount`, `pkg/utils`, `pkg/security`, `pkg/reconciler`)

**Secondary:**
- Shell/Bash - Build scripts and deployment automation (`hack/`, `scripts/`, Makefile, deploy scripts)
- YAML - Kubernetes manifests and Helm charts (`deploy/kubernetes/`, `deploy/helm/`)

## Runtime

**Environment:**
- Linux 1.26+ with Kubernetes CSI v1.5.0+ support (target runtime)
- Alpine Linux 3.21 for container image (runtime dependencies layer)
- Go runtime: statically compiled binaries (CGO_ENABLED=0)

**Package Manager:**
- Go Modules (go.mod/go.sum)
- Lockfile: `go.sum` present at `/path/to/rds-csi/go.sum`

## Frameworks

**Core:**
- Kubernetes CSI Spec v1.10.0 (`github.com/container-storage-interface/spec v1.10.0`) - CSI driver interface implementation in `pkg/driver/`
- Kubernetes Client-Go v0.28.0 (`k8s.io/client-go v0.28.0`) - Kubernetes API access for orphan reconciler in `pkg/reconciler/`
- Kubernetes API v0.28.0 (`k8s.io/api v0.28.0`) - API object definitions
- gRPC v1.69.2 (`google.golang.org/grpc`) - RPC framework for CSI communication in `pkg/driver/server.go`

**Logging:**
- Kubernetes klog v2.130.1 (`k8s.io/klog/v2`) - Structured logging throughout all packages

**Cryptography:**
- golang.org/x/crypto v0.31.0 - SSH key handling and verification in `pkg/rds/ssh_client.go`, host key verification in `pkg/rds/ssh_client.go`

**Build/Dev:**
- golangci-lint - Multi-linter runner (installed via make target)
- goimports - Import sorting and unused removal (installed via make target)
- Docker/Docker Buildx - Multi-arch container builds (lines 15, 172 of Makefile)

## Key Dependencies

**Critical:**
- `github.com/container-storage-interface/spec v1.10.0` - Defines CSI service interfaces (Identity, Controller, Node) that driver implements
- `k8s.io/client-go v0.28.0` - Communicates with Kubernetes API for volume and node management
- `golang.org/x/crypto v0.31.0` - SSH cryptography for RouterOS CLI authentication
- `google.golang.org/grpc v1.69.2` - gRPC transport for CSI protocol

**Utilities:**
- `github.com/google/uuid v1.6.0` - Volume ID generation (UUIDs with `pvc-` prefix) in `pkg/utils/volumeid.go`
- `golang.org/x/time v0.3.0` - Timing utilities for retry logic with exponential backoff
- `google.golang.org/protobuf v1.36.1` - Protocol buffer support for gRPC messages

**Kubernetes Infrastructure:**
- `k8s.io/apimachinery v0.28.0` - Common Kubernetes API machinery
- `k8s.io/kube-openapi v0.0.0-20230717233707-2695361300d9` - OpenAPI schema support

## Configuration

**Environment Variables:**
- `CSI_ENDPOINT` - Unix socket path for CSI communication (default: `unix:///var/lib/kubelet/plugins/rds.csi.srvlab.io/csi.sock`)
- `RDS_ADDRESS` - RDS server IP for SSH management (e.g., `192.0.2.10`)
- `RDS_PORT` - SSH port (default: 22)
- `RDS_USER` - SSH username (default: `admin`)
- `RDS_HOST_KEY` - Path to SSH host public key for verification
- `RDS_VOLUME_BASE_PATH` - Base path for volume files on RDS (e.g., `/storage-pool/metal-csi`)
- Kubernetes auth via in-cluster config or kubeconfig file

**Build Configuration:**
- `Dockerfile` - Multi-stage build (golang:1.24-alpine builder, alpine:3.21 runtime)
- `Makefile` - Build targets for cross-platform compilation (darwin-amd64, darwin-arm64, linux-amd64, linux-arm64)
- `go.mod` - Module declaration at `git.srvlab.io/whiskey/rds-csi-driver`
- Build flags: `-ldflags "-s -w"` for size optimization plus version injection

**Build Arguments:**
- `GIT_COMMIT` - Short commit hash
- `GIT_TAG` - Semantic version tag
- `BUILD_DATE` - ISO 8601 timestamp

## Runtime Dependencies (Container)

**Alpine Linux Packages** (from Dockerfile line 43-50):
- ca-certificates - TLS certificate verification
- e2fsprogs, e2fsprogs-extra - ext4 filesystem operations
- xfsprogs - XFS filesystem operations
- blkid - Block device identification
- nvme-cli - NVMe/TCP connection management (critical for data plane)
- openssh-client - SSH client for control plane (RDS management)
- util-linux - General system utilities

## Platform Requirements

**Development:**
- Go 1.24
- Make
- Git
- Docker (for container builds)
- SSH client
- golangci-lint (auto-installed via make)
- goimports (auto-installed via make)
- Optional: Nix shell for dependency isolation (shell.nix)

**Production:**
- Kubernetes 1.26+
- NixOS or Linux worker nodes with network boot support
- MikroTik RouterOS 7.1+ with ROSE Data Server
- NVMe/TCP network connectivity to RDS (typically port 4420)
- SSH access to RDS management interface (port 22)
- Linux kernel with NVMe/TCP and ext4/XFS support
- csi-provisioner sidecar for dynamic provisioning
- csi-node-driver-registrar sidecar for node plugin registration

---

*Stack analysis: 2026-01-30*
