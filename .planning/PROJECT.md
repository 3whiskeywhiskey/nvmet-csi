# RDS CSI Driver

## What This Is

A Kubernetes CSI driver for MikroTik ROSE Data Server (RDS) that provides dynamic NVMe/TCP block storage with volume snapshots and automated backup. The driver manages volume lifecycle via SSH/RouterOS CLI (control plane) and connects to storage via NVMe over TCP (data plane).

## Core Value

**Volumes remain accessible after NVMe-oF reconnections.** When network hiccups or RDS restarts cause connection drops, the driver detects and handles controller renumbering so mounted volumes continue working without pod restarts.

## Current State

**Version:** v0.11.0 (shipped 2026-02-18)
**LOC:** 46,633 Go
**Tech Stack:** Go 1.24, CSI Spec v1.5.0+, NVMe/TCP, SSH/RouterOS CLI

### What's Working

- Volume creation/deletion via SSH with NVMe/TCP export
- Node staging with NVMe connect, format, mount
- **Volume snapshots** via `/disk add copy-from` CoW (create, restore, delete, list)
- **Scheduled snapshots** via Helm CronJob with retention cleanup
- **NQN-based device path resolution** (no hardcoded paths)
- **Automatic stale mount detection and recovery**
- **Kernel reconnection parameters** (ctrl_loss_tmo, reconnect_delay)
- **Resilience regression tests** (NVMe reconnect, RDS restart, node failure)
- **Prometheus metrics** on :9809
- **Kubernetes events** for mount failures
- **VolumeCondition** health reporting
- **Helm chart** for one-command deployment

### Known Limitations

- Single RDS controller (no multipath)
- No controller HA (single replica)
- Hardware validation tests (TC-08 through TC-11) documented but not yet executed against real RDS

## Latest Milestones

**v0.11.0 Data Protection** (Shipped: 2026-02-18)
- Fixed snapshot implementation (Btrfs subvolume → `/disk add copy-from` CoW)
- Scheduled snapshots CronJob with retention, resilience regression tests
- See: `.planning/milestones/v0.11.0-ROADMAP.md`

**v0.10.0 Feature Enhancements** (Shipped: 2026-02-06)
- Hardware validation, documentation, Helm chart, RDS health monitoring
- See: `.planning/milestones/v0.10.0-ROADMAP.md`

**v0.9.0 Production Readiness & Test Maturity** (Shipped: 2026-02-06)
- CSI sanity tests, mock RDS server, E2E suite, attachment reconciliation
- See: `.planning/milestones/v0.9.0-ROADMAP.md`

## Requirements

### Validated

- ✓ Volume creation via SSH (`/disk add`) with NVMe/TCP export — v1
- ✓ Volume deletion via SSH (`/disk remove`) — v1
- ✓ Node staging: NVMe connect, format filesystem, mount to staging path — v1
- ✓ Node publishing: bind mount to pod path — v1
- ✓ Node unstaging: unmount, NVMe disconnect — v1
- ✓ SSH connection pooling with retry logic — v1
- ✓ Orphan volume reconciliation (detect/cleanup unused volumes) — v1
- ✓ Input validation preventing command injection — v1
- ✓ Secure mount options enforced (nosuid, nodev, noexec) — v1
- ✓ Security event logging for audit trails — v1
- ✓ Detect NVMe-oF controller renumbering after reconnection — v1
- ✓ Update or remount when device paths change (nvme0 → nvme3) — v1
- ✓ Cleanup stale mounts referencing non-existent device paths — v1
- ✓ Health checks via VolumeCondition in NodeGetVolumeStats — v1
- ✓ Validate device paths are functional before returning them — v1
- ✓ Handle orphaned NVMe subsystems (appear connected but no device path) — v1
- ✓ Prometheus metrics endpoint — v1
- ✓ NodeStageVolume handles block volumes without formatting — v0.6.0
- ✓ NodePublishVolume creates block device files using mknod — v0.6.0
- ✓ NodeUnpublishVolume/NodeUnstageVolume clean up block mounts — v0.6.0
- ✓ KubeVirt VMs boot successfully with RDS block volumes — v0.6.0
- ✓ KubeVirt live migration works end-to-end on metal cluster — v0.6.0
- ✓ Mount storm prevention (NQN filtering, duplicate detection, circuit breaker) — v0.6.0
- ✓ System volume protection via configurable NQN prefix filtering — v0.6.0
- ✓ Controller rebuilds state from VolumeAttachment objects (not PV annotations) — v0.7.0
- ✓ Migration state detection from multiple VolumeAttachment objects — v0.7.0
- ✓ PV annotations are informational-only (never read during rebuild) — v0.7.0
- ✓ Migration metrics (count, duration, active) observable via Prometheus — v0.7.0
- ✓ Production logs contain only actionable information at info level — v0.8.0
- ✓ Error handling follows consistent patterns throughout codebase — v0.8.0
- ✓ Test coverage >60% on critical paths — v0.8.0 (65.0% total)
- ✓ Common patterns extracted into reusable utilities — v0.8.0
- ✓ Code smells from analysis resolved or explicitly deferred — v0.8.0
- ✓ CSI sanity tests pass with zero failures in CI — v0.9.0
- ✓ Mock RDS server supports all driver commands with latency simulation — v0.9.0
- ✓ E2E test suite runs in CI without real hardware — v0.9.0
- ✓ Critical error paths have test coverage (controller, node, RDS client) — v0.9.0
- ✓ Attachment reconciliation detects and cleans stale VolumeAttachments — v0.9.0
- ✓ RDS connection manager with exponential backoff and auto-reconnection — v0.9.0
- ✓ Probe health check reflects RDS connection state — v0.9.0
- ✓ golangci-lint v2 enforced in CI with all issues resolved — v0.9.0
- ✓ Manual test scenarios documented for hardware validation — v0.10.0
- ✓ CSI capability gap analysis vs peer drivers — v0.10.0
- ✓ Helm chart for simplified deployment — v0.10.0
- ✓ RDS health monitoring design (SSH polling with Prometheus metrics) — v0.10.0
- ✓ Accurate nvme_connections_active metric from attachment manager — v0.10.0
- ✓ Snapshot creation uses `/disk add copy-from` CoW instead of Btrfs subvolume — v0.11.0
- ✓ Snapshot restore creates new volume from snapshot via copy-from — v0.11.0
- ✓ Snapshot deletion cleans up copied disk and file — v0.11.0
- ✓ Snapshots validated end-to-end against real RDS hardware — v0.11.0 (TC-08 documented)
- ✓ Scheduled snapshot CronJob with configurable retention — v0.11.0
- ✓ Resilience regression tests for NVMe reconnect, RDS restart, node failure — v0.11.0

### Active

(No active requirements — next milestone not yet defined)

### Out of Scope

- Controller HA — requires leader election, separate milestone after v1.0
- Volume encryption — different concern, separate security milestone
- NVMe multipath — single RDS controller, not applicable
- Automatic pod restart — CSI spec says drivers report, orchestrators act
- Security hardening (SSH host key verification, injection fuzzing) — separate security milestone
- Stress/load testing — hardware constraints, would require dedicated test infrastructure
- Package refactoring unless code substantially modified — defer to v1.0+ if not triggered
- Btrfs subvolume snapshots — file-backed disks aren't subvolumes; copy-from is correct
- Application-consistent snapshots — requires app-level quiesce hooks (Velero territory)
- Snapshot scheduling operator — CronJob is simpler and sufficient

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| 10s TTL for DeviceResolver cache | Balances freshness vs scanning overhead | ✓ Good |
| Prefer nvmeXnY over nvmeXcYnZ | Multipath compatibility | ✓ Good |
| ctrl_loss_tmo=-1 default | Prevents filesystem read-only mount | ✓ Good |
| 10% jitter in backoff | Prevents thundering herd | ✓ Good |
| Refuse force unmount if in use | Prevents data loss | ✓ Good |
| Custom prometheus.Registry | Avoids restart panics | ✓ Good |
| Orphan cleanup on startup | Best-effort, non-blocking | ✓ Good |
| Table-driven severity mapping | Reduces complexity, single source of truth | ✓ Good |
| Coverage threshold 65% | Realistic for hardware-dependent code, enforced in CI | ✓ Good |
| Sentinel errors over string matching | Type-safe, errors.Is() compatible | ✓ Good |
| V(2) for outcomes, V(4) for diagnostics | Clear operator vs debug separation | ✓ Good |
| Defer QUAL-03 package refactoring | Risk > benefit at current scale | ✓ Good |
| CSI sanity tests in CI with artifact capture | Debugging test failures, traceability | ✓ Good |
| Mock RDS 200ms SSH latency | Exposes timeout bugs, realistic behavior | ✓ Good |
| E2E tests use in-process driver | Faster iteration, easier debugging | ✓ Good |
| Node watcher after informer cache sync | Avoids race conditions on startup | ✓ Good |
| Connection manager polls every 5s | Production-friendly, not chatty | ✓ Good |
| MaxElapsedTime=0 for reconnection | Never give up on RDS reconnection | ✓ Good |
| Complexity threshold 50 | Justified by CSI spec compliance needs | ✓ Good |
| golangci-lint v2 nested config | Required for v2 compatibility | ✓ Good |
| `/disk add copy-from` for snapshots | Btrfs subvolumes don't work with file-backed disks; CoW reflink copies | ✓ Good |
| UUID v5 snapshot IDs from CSI name | Deterministic, idempotent per CSI spec | ✓ Good |
| Snapshot disks not NVMe-exported | Immutable backups, write-protected by omission | ✓ Good |
| SetErrorMode for resilience tests | MockRDSServer.shutdown closes permanently on Stop() | ✓ Good |
| CronJob over operator for scheduling | Simpler, sufficient, no custom CRDs needed | ✓ Good |
| blkid exit 1 = error (not unformatted) | Prevents data loss on NVMe-oF reconnect race | ✓ Good |

## Constraints

- **Testing**: RDS restart affects site networking; need confidence before testing
- **Compatibility**: Must work with existing volumes and mounts (no breaking changes)
- **Dependencies**: Uses nvme-cli binary; solutions must work within that constraint
- **Data Safety**: Postgres consolidation makes snapshot reliability critical — data loss is unacceptable
- **Hardware Validation**: TC-08 through TC-11 require maintenance window for execution against real RDS at 198.51.100.20

---
*Last updated: 2026-02-18 after v0.11.0 milestone completion*
