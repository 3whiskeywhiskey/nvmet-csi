# Codebase Structure

**Analysis Date:** 2026-01-30

## Directory Layout

```
/path/to/rds-csi/
├── cmd/                           # Entry points
│   └── rds-csi-plugin/
│       └── main.go               # Single binary for both controller and node modes
├── pkg/                           # Core packages
│   ├── driver/                   # CSI service implementations
│   │   ├── driver.go             # Driver initialization and coordination
│   │   ├── server.go             # gRPC server setup
│   │   ├── identity.go           # Identity service (GetPluginInfo, Probe)
│   │   ├── controller.go         # Controller service (Create/Delete/GetCapacity)
│   │   ├── node.go               # Node service (Stage/Publish/Unpublish)
│   │   └── *_test.go             # Unit tests
│   ├── rds/                      # RDS/RouterOS client
│   │   ├── client.go             # RDSClient interface definition
│   │   ├── ssh_client.go         # SSH implementation
│   │   ├── commands.go           # RouterOS CLI command wrappers
│   │   ├── pool.go               # SSH connection pooling
│   │   ├── types.go              # Data structures (VolumeInfo, CapacityInfo)
│   │   └── *_test.go             # Unit tests
│   ├── nvme/                     # NVMe/TCP operations
│   │   ├── nvme.go               # Connector interface and implementation
│   │   └── *_test.go             # Unit tests
│   ├── mount/                    # Filesystem mounting
│   │   ├── mount.go              # Mounter interface and mount/unmount/format
│   │   └── *_test.go             # Unit tests
│   ├── reconciler/               # Orphan volume detection
│   │   ├── orphan_reconciler.go  # Background cleanup task
│   │   └── *_test.go             # Unit tests
│   ├── security/                 # Security and auditing
│   │   ├── logger.go             # Centralized event logging
│   │   ├── metrics.go            # Operation metrics
│   │   ├── events.go             # Event type definitions
│   │   └── *_test.go             # Unit tests
│   └── utils/                    # Utility functions
│       ├── validation.go         # Input validation (volumeID, IP, port, filepath)
│       ├── volumeid.go           # UUID generation and NQN conversion
│       ├── errors.go             # Error classification and mapping
│       ├── regex.go              # RouterOS output parsing
│       └── *_test.go             # Unit tests
├── deploy/                        # Kubernetes deployment
│   ├── kubernetes/
│   │   ├── namespace.yaml        # RDS CSI namespace
│   │   ├── rbac.yaml             # Service accounts and roles
│   │   ├── controller.yaml       # Controller deployment
│   │   ├── node.yaml             # Node daemon set
│   │   ├── csidriver.yaml        # CSIDriver custom resource
│   │   ├── kustomization.yaml    # Kustomize base
│   │   └── deploy.sh             # Deployment script
│   └── helm/
│       └── rds-csi-driver/       # Helm chart
│           ├── Chart.yaml
│           ├── values.yaml
│           └── templates/        # Kubernetes manifest templates
├── test/                          # Testing infrastructure
│   ├── integration/              # Integration tests with real RDS/routing OS
│   │   ├── controller_integration_test.go
│   │   ├── hardware_integration_test.go
│   │   ├── orphan_reconciler_integration_test.go
│   │   └── *.go
│   ├── mock/                     # Mock RDS server for unit testing
│   │   └── rds_server.go         # Mock SSH server
│   ├── e2e/                      # End-to-end tests
│   ├── sanity/                   # CSI sanity tests
│   └── docker/                   # Docker test utilities
├── docs/                          # Documentation
├── hack/                          # Build and development scripts
├── scripts/                       # Utility scripts
├── examples/                      # Example manifests and StorageClasses
├── Makefile                       # Build and test targets
├── go.mod                         # Go module definition
└── go.sum                         # Go dependency lock file
```

## Directory Purposes

**cmd/rds-csi-plugin/:**
- Purpose: Single-binary entry point for both controller and node instances
- Contains: main.go with flag parsing, configuration loading, driver initialization
- Key files: `main.go` (190 lines) - parses 15+ flags for RDS connection, mode selection, orphan reconciler config

**pkg/driver/:**
- Purpose: CSI specification implementation (gRPC services)
- Contains: Driver coordination, gRPC server, Identity/Controller/Node service implementations
- Key files:
  - `driver.go` (296 lines) - Driver struct, initialization, lifecycle management
  - `controller.go` (~500 lines) - CreateVolume, DeleteVolume, ValidateVolumeCapabilities, GetCapacity
  - `node.go` (~600 lines) - NodeStageVolume, NodeUnstageVolume, NodePublishVolume, NodeUnpublishVolume
  - `identity.go` (81 lines) - GetPluginInfo, GetPluginCapabilities, Probe
  - `server.go` (146 lines) - gRPC server setup and endpoint parsing

**pkg/rds/:**
- Purpose: MikroTik ROSE Data Server SSH client and command abstraction
- Contains: SSH connection management, RouterOS CLI command wrappers, output parsing, connection pooling
- Key files:
  - `client.go` (69 lines) - RDSClient interface definition and factory (NewClient)
  - `ssh_client.go` (~200+ lines) - SSH connection, authentication, command execution
  - `commands.go` (~400+ lines) - CreateVolume, DeleteVolume, ResizeVolume, ListVolumes, GetCapacity, GetVolume with retry logic
  - `pool.go` - SSH connection pooling for efficiency
  - `types.go` - VolumeInfo, CapacityInfo, CreateVolumeOptions, FileInfo data structures

**pkg/nvme/:**
- Purpose: NVMe/TCP target connectivity and device management
- Contains: Connector interface, nvme-cli wrapper, device discovery with timeouts
- Key files:
  - `nvme.go` (~400+ lines) - Connector implementation, Connect/Disconnect with context support, device polling, metrics
  - Target struct: Encapsulates NQN, IP, port, transport
  - Metrics: Tracks successful/failed connections, operation timing

**pkg/mount/:**
- Purpose: Filesystem mounting and formatting with security constraints
- Contains: Mount option validation, format/mount/unmount operations
- Key files:
  - `mount.go` (~200+ lines) - Mounter interface, secure mount option enforcement
  - Dangerous options whitelist: Blocks suid, dev, exec always
  - Default secure options: nosuid, nodev, noexec applied to all mounts

**pkg/reconciler/:**
- Purpose: Background cleanup of orphaned volumes not tracked by Kubernetes
- Contains: Volume discovery, orphan detection, deletion with grace period
- Key files:
  - `orphan_reconciler.go` (~300+ lines) - OrphanReconciler loop, volume comparison logic, dry-run support

**pkg/security/:**
- Purpose: Centralized security event logging and metrics
- Contains: Structured event logging, metrics collection, audit trail
- Key files:
  - `logger.go` - Centralized security event logging with klog integration
  - `metrics.go` - Prometheus-style metrics (operation counts, durations, failures)
  - `events.go` - SecurityEvent types and severity levels

**pkg/utils/:**
- Purpose: Shared utilities for validation, parsing, and conversion
- Contains: Volume ID validation, regex parsing, error classification, IP/port validation
- Key files:
  - `validation.go` (60+ lines) - ValidateVolumeID, ValidateFilePath, ValidateIPAddress, ValidatePortString, ValidateNVMETargetContext
  - `volumeid.go` (~100 lines) - UUID generation, VolumeIDToNQN, VolumeIDToFilePath conversions
  - `errors.go` (~200+ lines) - ErrorType classification, RouterOS output pattern matching to CSI error codes
  - `regex.go` (~150+ lines) - Parsing of RouterOS `/disk print detail` output

**deploy/kubernetes/:**
- Purpose: Raw Kubernetes manifests for deployment
- Contains: Namespace, RBAC, Deployment (controller), DaemonSet (node), CSIDriver resource
- Key files:
  - `controller.yaml` - Deployment with 1 replica, mounts SSH key Secret
  - `node.yaml` - DaemonSet on all nodes, mounts kubelet plugin directory
  - `rbac.yaml` - ServiceAccount, ClusterRole, ClusterRoleBinding
  - `csidriver.yaml` - CSIDriver resource declaring attachment required, filesystem requirement

**deploy/helm/:**
- Purpose: Templated Helm chart for flexible installation
- Contains: Helm values, chart metadata, templated Kubernetes resources
- Structure: Standard Helm layout with templates/ subdirectory for parameterized manifests

**test/:**
- Purpose: Testing infrastructure and test cases
- integration/: Real RDS/RouterOS integration tests (requires live hardware or docker-compose)
- mock/: Mock RDS server for unit testing
- e2e/: End-to-end tests in Kubernetes cluster
- sanity/: CSI sanity tests (csi-test/cmd/csi-sanity)

**docs/:**
- Purpose: Architecture and implementation documentation
- Contains: RDS commands reference, architecture deep-dives, troubleshooting guides

**hack/ and scripts/:**
- Purpose: Build and development automation
- Contains: Build scripts for cross-platform compilation, installation tools, testing utilities

## Key File Locations

**Entry Points:**
- `cmd/rds-csi-plugin/main.go`: Single binary entry point, flag parsing, driver initialization

**Configuration:**
- `cmd/rds-csi-plugin/main.go`: All runtime flags and defaults
- `deploy/kubernetes/controller.yaml`: Deployment args and env vars for controller
- `deploy/kubernetes/node.yaml`: Deployment args and env vars for node plugin
- `deploy/helm/rds-csi-driver/values.yaml`: Helm default values

**Core Logic:**
- `pkg/driver/controller.go`: CreateVolume, DeleteVolume business logic
- `pkg/driver/node.go`: NodeStageVolume, NodePublishVolume business logic
- `pkg/rds/commands.go`: RouterOS command execution and output parsing
- `pkg/nvme/nvme.go`: NVMe/TCP connection and device discovery
- `pkg/mount/mount.go`: Filesystem operations with secure defaults

**Testing:**
- `test/mock/rds_server.go`: Mock RDS SSH server for unit tests
- `test/integration/controller_integration_test.go`: Real RDS integration tests
- `*_test.go` files: Unit tests co-located with source files (pkg/driver/*_test.go, pkg/rds/*_test.go, etc.)

## Naming Conventions

**Files:**
- Source: `<functionality>.go` (driver.go, controller.go, ssh_client.go)
- Tests: `<functionality>_test.go` (controller_test.go, ssh_client_test.go)
- Integration tests: `<component>_integration_test.go` (controller_integration_test.go)
- Example: camelCase for variables, PascalCase for types and functions

**Directories:**
- Lowercase only: `cmd`, `pkg`, `deploy`, `test`, `docs`
- Semantic grouping: `pkg/rds`, `pkg/nvme`, `pkg/mount` by responsibility
- Test type separation: `test/mock`, `test/integration`, `test/e2e`, `test/sanity`

**Packages:**
- Package name matches directory name (pkg/rds → package rds)
- Exported functions PascalCase (CreateVolume, NewClient, Connect)
- Unexported helpers lowercase (newSSHClient, formatBytes, validateCreateVolumeOptions)

**Constants and Variables:**
- Public constants UPPER_CASE or PascalCase (DriverName, defaultVolumeBasePath)
- Private constants lowercase (defaultFSType, maxMsgSize)
- Global instances lowercase (globalLogger, version, gitCommit)

## Where to Add New Code

**New Feature (e.g., Volume Snapshot Support):**
- Primary code: `pkg/driver/controller.go` (add CSI snapshot methods) and `pkg/rds/commands.go` (add RouterOS snapshot commands)
- Tests: `pkg/driver/controller_test.go` and `pkg/rds/commands_test.go`
- Example: Implement `CreateSnapshot()` and `DeleteSnapshot()` in ControllerServer

**New Component/Module (e.g., Ceph Backend Support):**
- Implementation: Create `pkg/ceph/client.go` implementing RDSClient interface
- Tests: `pkg/ceph/client_test.go` with same interface contracts as SSH client
- Integration: Modify `pkg/rds/client.go` NewClient factory to route to new implementation
- Example: NewClient(config) checks config.Protocol == "ceph" and returns newCephClient

**Utilities and Helpers:**
- Shared validation: `pkg/utils/validation.go` (add ValidateSomething functions)
- Shared parsing: `pkg/utils/regex.go` (add parsing regex patterns)
- Shared error handling: `pkg/utils/errors.go` (add error classification)
- Test helpers: `pkg/utils/*_test.go` (test validation functions inline)

**New Test Fixtures:**
- Unit test mocks: `test/mock/` (extend rds_server.go with additional mock behavior)
- Integration tests: `test/integration/` (add *_integration_test.go files)
- CSI sanity: `test/sanity/` (extend with custom sanity test cases)

## Special Directories

**bin/:**
- Purpose: Build output directory for compiled binaries
- Generated: Yes (created by Makefile during build)
- Committed: No (in .gitignore)
- Contents: `rds-csi-plugin-<os>-<arch>` binaries (e.g., rds-csi-plugin-linux-amd64)

**vendor/ (if present):**
- Purpose: Vendored dependencies
- Generated: Yes (created by `go mod vendor`)
- Committed: No (dependencies managed via go.mod/go.sum)

**test/docker/:**
- Purpose: Docker test environment setup
- Generated: No (committed source files)
- Contents: docker-compose configurations, test fixtures
- Usage: `docker-compose -f test/docker/docker-compose.yml up` for test environment

**.planning/codebase/:**
- Purpose: GSD analysis documents
- Generated: Yes (by claude mapping agents)
- Committed: Yes (reference for future development)
- Contents: ARCHITECTURE.md, STRUCTURE.md, CONVENTIONS.md, TESTING.md, CONCERNS.md, STACK.md, INTEGRATIONS.md

## Import Path Patterns

All imports use absolute package paths with module prefix:
```go
import (
    // Standard library
    "context"
    "fmt"
    "time"

    // External dependencies
    "github.com/container-storage-interface/spec/lib/go/csi"
    "k8s.io/klog/v2"
    "golang.org/x/crypto/ssh"

    // Internal packages
    "git.srvlab.io/whiskey/rds-csi-driver/pkg/driver"
    "git.srvlab.io/whiskey/rds-csi-driver/pkg/rds"
    "git.srvlab.io/whiskey/rds-csi-driver/pkg/utils"
)
```

## Build and Artifact Structure

**Makefile targets produce:**
- `bin/rds-csi-plugin-darwin-amd64`: macOS Intel binary
- `bin/rds-csi-plugin-darwin-arm64`: macOS Apple Silicon binary
- `bin/rds-csi-plugin-linux-amd64`: Linux x86_64 binary
- `bin/rds-csi-plugin-linux-arm64`: Linux ARM64 binary
- Docker image: `rds-csi-plugin:latest` (built from Dockerfile for Linux/amd64)

**Version information embedded via ldflags:**
- `-X git.srvlab.io/whiskey/rds-csi-driver/pkg/driver.version=<version>`
- `-X git.srvlab.io/whiskey/rds-csi-driver/pkg/driver.gitCommit=<commit>`
- `-X git.srvlab.io/whiskey/rds-csi-driver/pkg/driver.buildDate=<date>`

---

*Structure analysis: 2026-01-30*
