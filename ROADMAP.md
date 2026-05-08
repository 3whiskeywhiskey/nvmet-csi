# nvmet-csi driver - Roadmap

This document provides a high-level overview of the nvmet-csi driver development milestones and current status. For detailed phase plans, see `.planning/ROADMAP.md`.

## Overview

**Goal**: Build a production-ready CSI driver for MikroTik ROSE Data Server (RDS) NVMe/TCP storage

**Current Status**: v0.8.0 shipped, v0.9.0 in progress

## Milestones

### ✅ v0.1.0 Foundation (Shipped: 2024-03-15)

**Delivered**: Project scaffolding, SSH client, CSI Identity service, build system

<details>
<summary>Key accomplishments</summary>

- SSH client wrapper for RouterOS CLI with retry logic and exponential backoff
- CSI Identity service (GetPluginInfo, Probe, GetPluginCapabilities)
- Volume ID utilities with NQN generation and command injection prevention
- Multi-architecture build support (darwin/linux, amd64/arm64)
- Unit test foundation (17 test cases, 100% passing)

**Phases**: 1-3

</details>

---

### ✅ v0.2.0 Controller Service (Shipped: 2024-04-01)

**Delivered**: Complete volume lifecycle management via SSH/RouterOS CLI

<details>
<summary>Key accomplishments</summary>

- CreateVolume with file-backed NVMe/TCP export on RDS
- DeleteVolume with cleanup and idempotency
- ValidateVolumeCapabilities for access mode validation
- GetCapacity for RDS free space queries with unit conversion
- ListVolumes for volume enumeration
- gRPC server implementation with all required CSI methods
- Error mapping: RDS errors to appropriate gRPC status codes

**Phases**: 4-5

</details>

---

### ✅ v0.3.0 Node Service (Shipped: 2024-05-15)

**Delivered**: Volume attachment and mounting on worker nodes with two-phase mounting architecture

<details>
<summary>Key accomplishments</summary>

- NodeStageVolume: NVMe/TCP connect, format filesystem, mount to staging path
- NodeUnstageVolume: Unmount and NVMe/TCP disconnect
- NodePublishVolume: Bind mount from staging to pod path
- NodeUnpublishVolume: Unmount from pod path
- NodeGetVolumeStats: Filesystem usage statistics
- NVMe/TCP connection manager with device discovery via sysfs
- Filesystem operations with ext4/ext3/xfs support
- Idempotent operations with proper error handling

**Phases**: 6-8

</details>

---

### ✅ v0.4.0 Production Hardening (Shipped: 2024-06-30)

**Delivered**: NVMe-oF reconnection reliability and health monitoring

<details>
<summary>Key accomplishments</summary>

- NQN-based device path resolution (no hardcoded /dev/nvmeXnY paths)
- Automatic stale mount detection and recovery with exponential backoff
- Kernel reconnection parameters (ctrl_loss_tmo, reconnect_delay) configurable via StorageClass
- Kubernetes event posting for mount failures and recovery actions
- Prometheus metrics endpoint (:9809) with 10 CSI-specific metrics
- VolumeCondition health reporting in NodeGetVolumeStats
- Volume expansion support (ControllerExpandVolume, NodeExpandVolume)

**Phases**: 9-11

</details>

---

### ✅ v0.5.0 NVMe-oF Reconnection (Shipped: 2025-01-15)

**Delivered**: Error resilience framework ensuring volumes remain accessible after network hiccups and RDS restarts

<details>
<summary>Key accomplishments</summary>

- Enhanced NQN-based device path resolution via sysfs scanning
- Automatic stale mount detection and recovery with exponential backoff
- Circuit breaker pattern for error resilience
- Filesystem health checks before operations
- Procmounts timeout (10s) to prevent hanging
- Duplicate mount detection (100 threshold)
- Orphan volume reconciliation with configurable grace period

**Phases**: 12-14

</details>

---

### ✅ v0.6.0 Block Volumes & KubeVirt (Shipped: 2025-11-15)

**Delivered**: CSI block volume support for KubeVirt VMs with validated live migration

<details>
<summary>Key accomplishments</summary>

- Block volume lifecycle: NodeStageVolume/NodePublishVolume handle block volumes without formatting
- KubeVirt integration: VM boot and live migration validated on metal cluster (~15s migration)
- Mount storm prevention: Fixed devtmpfs propagation bug (mknod vs bind mount)
- System volume protection: NQN prefix filtering prevents orphan cleaner from disconnecting system volumes
- Error resilience framework: Circuit breaker, filesystem health checks, timeout handling
- Stale state fix: Clear PV annotations on detachment to prevent false positive attachments

**Phases**: 15-16

</details>

---

### ✅ v0.7.0 Migration Tracking (Shipped: 2026-01-20)

**Delivered**: VolumeAttachment-based state rebuild and complete migration metrics observability

<details>
<summary>Key accomplishments</summary>

- VolumeAttachment objects are now authoritative for state rebuild
- Controller rebuilds attachment state from VolumeAttachment objects (not PV annotations)
- Migration state survives controller restarts with dual VolumeAttachment detection
- PV annotations now informational-only (write-only for debugging)
- Migration metrics observability complete via Prometheus
- Comprehensive test coverage (14 tests, 84.5% coverage for rebuild subsystem)

**Phases**: 17-19

</details>

---

### ✅ v0.8.0 Code Quality & Logging (Shipped: 2026-02-04)

**Delivered**: Systematic codebase cleanup with improved maintainability and comprehensive test coverage

<details>
<summary>Key accomplishments</summary>

- Logging cleanup: 78% reduction in security logger code, V(3) eliminated codebase-wide
- Error handling standardization: 96.1% %w compliance, 10 sentinel errors integrated
- Test coverage expansion: 65.0% total coverage (up from 48%), enforcement configured
- Code quality improvements: Severity mapping table reduces cyclomatic complexity 80%
- Test infrastructure stabilization: 148 tests pass consistently with race detector
- Maintainability improvements: Documented conventions in CONVENTIONS.md
- golangci-lint enforcement enabled in CI

**Stats**: 88 files modified, +14,120 insertions, -584 deletions

**Phases**: 20-21

</details>

---

### 🚧 v0.9.0 Production Readiness & Test Maturity (In Progress)

**Goal**: Validate CSI spec compliance and build production-ready testing infrastructure enabling confident v1.0 release

#### Completed Phases

<details>
<summary>Phase 22: CSI Sanity Tests Integration ✅</summary>

- csi-sanity test suite runs in CI without failures
- All controller service methods pass idempotency validation
- Negative test cases validate proper CSI error codes
- CSI capability matrix documented

**Plans**: 2/2 complete

</details>

<details>
<summary>Phase 23: Mock RDS Enhancements ✅</summary>

- Mock RDS server handles all volume lifecycle commands
- Realistic SSH latency simulation (200ms average, 150-250ms range)
- Error injection for disk full, SSH timeout, command parsing errors
- Stateful volume tracking for sequential operation correctness
- RouterOS-formatted output matching production RDS
- Concurrent connection handling

**Plans**: 2/2 complete

</details>

<details>
<summary>Phase 24: Automated E2E Test Suite ✅</summary>

- Automated volume lifecycle test in CI (create → stage → publish → unpublish → unstage → delete)
- Block volume test with KubeVirt VirtualMachineInstance
- Volume expansion test validates filesystem resizes
- Multi-volume test validates 5+ concurrent operations
- Orphan detection and node failure simulation tests
- Controller restart test validates state rebuild

**Plans**: 4/4 complete

</details>

<details>
<summary>Phase 25: Coverage & Quality Improvements ✅</summary>

- Error paths in controller and node services tested
- Edge cases from sanity test failures have regression tests
- Negative test scenarios validate CSI error codes
- Coverage enforcement in CI at 65% baseline
- Flaky tests identified and documented in TESTING.md

**Plans**: 4/4 complete

</details>

<details>
<summary>Phase 25.1: Attachment Reconciliation & RDS Resilience ✅</summary>

- Controller reconciliation loop detects stale VolumeAttachment objects
- Node NotReady watcher triggers proactive attachment validation
- RDS connection loss triggers automatic reconnect with exponential backoff
- CSI Probe health check reflects RDS connection state
- Kubernetes events and Prometheus metrics for observability

**Context**: Production incident fix - 5+ hour outage extended due to stale VolumeAttachments after RDS crash

**Plans**: 3/3 complete

</details>

<details>
<summary>Phase 25.2: Fix Linter Issues Blocking CI Verification ✅</summary>

- Resolved 134 pre-existing linter issues (63 errcheck, 56 cyclop, 7 gocyclo, 8 staticcheck)
- Updated golangci-lint config to v2 format
- `make verify` passes without lint failures
- CI/CD verification step succeeds

**Context**: golangci-lint v2 upgrade exposed issues blocked by v1 config format

**Plans**: 2/2 complete

</details>

#### Remaining Phases

**Phase 26: Volume Snapshots** (Not Started)
- Btrfs-based volume snapshots via RouterOS CLI
- CreateSnapshot/DeleteSnapshot/ListSnapshots implementation
- CreateVolume from snapshot (restore workflow)
- external-snapshotter sidecar integration
- CSI sanity snapshot tests

**Phase 27: Documentation & Hardware Validation** (Not Started)
- Manual test scenarios for production RDS testing
- Testing guide for contributors (TESTING.md)
- CSI capability gap analysis vs peer drivers
- Known limitations documentation
- CI/CD integration guide
- Troubleshooting guide expansion

---

## Future Considerations (Post v0.9.0)

Potential areas for v1.0+ development:

- **Controller HA**: Multiple controller replicas with leader election
- **Volume Encryption**: LUKS encryption at rest, TLS for NVMe/TCP in transit
- **NVMe Multipath**: Support for multiple RDS controllers (if hardware supports)
- **Advanced Monitoring**: Detailed metrics, alerting rules, dashboards
- **Multi-tenancy**: Namespace isolation, quotas
- **Performance Optimization**: Connection pooling, parallel operations
- **Volume Replication**: Cross-RDS replication for disaster recovery

---

## Progress Summary

| Milestone | Status | Phases | Shipped Date |
|-----------|--------|--------|--------------|
| v0.1.0 Foundation | ✅ Shipped | 1-3 | 2024-03-15 |
| v0.2.0 Controller Service | ✅ Shipped | 4-5 | 2024-04-01 |
| v0.3.0 Node Service | ✅ Shipped | 6-8 | 2024-05-15 |
| v0.4.0 Production Hardening | ✅ Shipped | 9-11 | 2024-06-30 |
| v0.5.0 NVMe-oF Reconnection | ✅ Shipped | 12-14 | 2025-01-15 |
| v0.6.0 Block Volumes & KubeVirt | ✅ Shipped | 15-16 | 2025-11-15 |
| v0.7.0 Migration Tracking | ✅ Shipped | 17-19 | 2026-01-20 |
| v0.8.0 Code Quality & Logging | ✅ Shipped | 20-21 | 2026-02-04 |
| v0.9.0 Production Readiness | 🚧 In Progress | 22-27 | Target: 2026-Q1 |

**Current Phase**: 26 of 27 (Volume Snapshots - Not Started)

**Overall Progress**: ~37% of v0.9.0 complete (20/TBD plans)

---

*Last updated: 2026-02-06*
*For detailed phase plans and requirements, see `.planning/ROADMAP.md`*
