# nvmet-csi driver - Roadmap

This document provides a high-level overview of the nvmet-csi driver development milestones and current status. For detailed phase plans, see `.planning/ROADMAP.md`.

## Current Project

**Identity:** `nvmet-csi` is a backend-neutral NVMe/TCP CSI driver and the
successor to the RDS-focused driver whose v0.1.0-v0.11.0 history is preserved
below. Linux ZFS-on-Linux with kernel `nvmet` is the primary backend;
RouterOS/RDS is an explicit compatibility backend. The legacy `rds-csi`
deployment remains separate.

**Current status (2026-07-12):** post-fork backend abstraction, Linux-ZOL
volume lifecycle and reboot recovery, and isolated RouterOS/RDS volume
lifecycle are implemented. Dated hardware evidence lives in
`docs/nvmet-csi-live-validation.md`.

Status terms in current documents are evidence-scoped:

- **Implemented:** a production code path exists.
- **Automated-tested:** unit, mock, sanity, or automated E2E evidence exists.
- **Live-validated:** the named scenario was executed on identified hardware.
- **Unsupported:** the selected backend explicitly rejects or lacks the operation.

The milestones below through v0.11.0 are historical predecessor records, not
the current nvmet-csi release plan.
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

### ✅ v0.9.0-v0.11.0 Historical Predecessor Milestones

The RDS-focused predecessor completed and archived its implementation plans
through v0.11.0 in February 2026. Detailed records are under
`.planning/milestones/`. A checked historical plan means its implementation,
test, or documentation deliverable was completed; it does not by itself prove
that a hardware procedure was executed.

The important erratum is snapshot validation: RouterOS `copy-from` snapshot
RPCs, restore, mock coverage, CSI sanity coverage, and TC-08 documentation were
completed, but TC-08 was not executed against real RouterOS hardware at the
time the milestone was archived. TC-09 through TC-11 were likewise documented
but not live-executed.

---

## Current Backend Capability and Evidence Matrix

| Backend / capability | Implemented | Automated-tested | Live-validated | Current status |
| --- | --- | --- | --- | --- |
| Linux-ZOL volume create/delete, attach, mount, remount, expand | Yes | Yes | Yes, Metal 2026-07-12 | Supported |
| Linux-ZOL target reconstruction after reboot | Yes | Yes | Yes, r740xd 2026-07-11 | Supported |
| Linux-ZOL snapshots and snapshot restore | No | Negative-path only | No | Unsupported |
| RouterOS/RDS volume create/delete, mount/remount, online expand | Yes | Yes | Yes, isolated Metal path 2026-07-11/12 | Compatibility backend |
| RouterOS/RDS snapshot create/delete/list and same-backend restore | Yes | Yes | No; TC-08 pending | Validation in progress |
| RouterOS/RDS node-roll and disruption resilience | Yes / recovery paths | Mock and documented procedures | No; TC-09-TC-11 pending | Validation pending |

CSI controller snapshot capabilities are currently advertised per driver
instance, not per StorageClass. Linux-ZOL does not implement snapshots, and
`CreateSnapshot` routing still relies on `VolumeSnapshotClass` parameters rather
than authoritative source-volume ownership. Until that is hardened, deploy
snapshot-capable RouterOS and snapshot-unsupported Linux-ZOL paths under
separate CSI identities and use a RouterOS-specific `VolumeSnapshotClass`.

## Current Roadmap

### Completed in the July 2026 nvmet-csi hardening cycle

- Backend-neutral controller and typed Linux-ZOL/RouterOS adapters.
- Linux-ZOL plan/observe/apply lifecycle with ownership-checked deletion.
- Durable zvol export intent and repair-only startup/periodic export reconciliation.
- Idempotent configfs writes and batched backend observation.
- Linux-ZOL reboot recovery and disposable worker lifecycle live validation.
- Isolated RouterOS controller and full data-path lifecycle live validation.
- Configured CSI driver-name isolation across attachment and orphan reconciliation.
- BusyBox/traditional `lsof` device-in-use safety fix.

### Active validation

1. Execute RouterOS snapshot TC-08 with an isolated nvmet-csi identity.
2. Validate scheduled snapshot execution only after TC-08 succeeds.
3. Execute non-destructive RouterOS resilience cases TC-09 and TC-11.
4. Execute TC-10 appliance restart only in an approved maintenance window.
5. Validate Linux-ZOL consumer-node drain and KubeVirt churn.

### Release closure

- Reconcile current versioning with the archived v0.11 predecessor history.
- Publish a formal post-fork release only from the exact verified source commit.
- Complete node-plugin rollout on `r640-1` and `c4140` when those nodes return;
  they are not required for the current snapshot validation.

---

## Linux-ZOL nvmet Reboot Safety (2026-07)

**Status**: complete and live-validated on r740xd (2026-07-11).

Kernel `nvmet` configfs state is lost on target host reboot; retained
Linux-ZOL PVCs failed to mount until exports were rebuilt by hand. The
controller now owns the invariant that every managed Linux-ZOL PV has a
correct configfs export:

- **Export reconciler** (`pkg/reconciler/export_reconciler.go`): runs at
  controller startup and on an interval; repair-only (never creates, resizes,
  or deletes volumes); PV-gated; conflicts surfaced as precise errors instead
  of rewrites. See `docs/export-reconciler.md`.
- **Idempotent configfs writes**: all namespace/subsystem/port mutations are
  read-before-write (`device_path` rewrite on an enabled namespace is refused
  explicitly instead of failing with kernel `EBUSY`).
- **Batched observation**: volume state is gathered with one `zfs get` plus
  one configfs probe script per volume (was ~11 executor round-trips each).
- `linux-zol-plan` Helm job remains the dry-run/debug surface for the same
  planner the reconciler applies.

Live closure passed after an actual r740xd outage: startup reconciliation
repaired all 12 managed exports with no conflicts; client discovery from
`r640-3` returned all managed NQNs; a second controller startup repaired zero
exports; and an existing ClickHouse workload remained operational. The image
is digest-pinned through Flux. See `docs/nvmet-csi-live-validation.md`.

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

| Workstream | Status | Evidence |
| --- | --- | --- |
| Historical predecessor plans v0.1.0-v0.11.0 | Archived | `.planning/milestones/` |
| Linux-ZOL backend and reboot safety | Live-validated | `docs/nvmet-csi-live-validation.md` |
| RouterOS/RDS ordinary lifecycle through nvmet-csi | Live-validated | `docs/nvmet-csi-live-validation.md` |
| RouterOS/RDS snapshots | Implemented + automated-tested; TC-08 pending | `docs/HARDWARE_VALIDATION.md` |
| Linux-ZOL snapshots | Unsupported | `pkg/backend/linuxzol/backend.go` |
| RouterOS resilience TC-09-TC-11 | Automated/documented; live execution pending | `docs/HARDWARE_VALIDATION.md` |

**Current focus:** isolated RouterOS snapshot TC-08, then scheduled snapshots
and non-destructive resilience validation.

---

*Last updated: 2026-07-12*
*Historical phase plans and requirements remain under `.planning/milestones/`.*
