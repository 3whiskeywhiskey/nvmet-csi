# nvmet-csi

## What This Is

`nvmet-csi` is a backend-neutral Kubernetes CSI driver for NVMe/TCP storage.
It is the successor to the RDS-focused driver archived at v0.11.0. Linux
ZFS-on-Linux with kernel `nvmet` is the primary backend; MikroTik RouterOS/ROSE
Data Server remains an explicit compatibility backend. The legacy `rds-csi`
deployment is separate.

## Core Value

Volumes remain accessible and recoverable through NVMe/TCP reconnections,
controller restarts, and target-host reboot without unsafe mutation storms.

## Current State

**Current source status:** post-fork hardening on `dev` as of 2026-07-12
**Archived predecessor baseline:** v0.11.0, shipped 2026-02-18
**Tech stack:** Go 1.24, CSI, NVMe/TCP, ZFS/configfs, SSH/RouterOS CLI

### Backend Evidence Matrix

| Backend / capability | Status |
| --- | --- |
| Linux-ZOL ordinary lifecycle and reboot export recovery | Implemented, automated-tested, live-validated |
| Linux-ZOL snapshots | Unsupported |
| RouterOS ordinary lifecycle through nvmet-csi | Implemented, automated-tested, live-validated under an isolated identity |
| RouterOS snapshots and same-backend restore | Implemented, automated-tested; TC-08 pending live execution |
| RouterOS resilience | Automated/mock-tested or documented; TC-09-TC-11 pending live execution |

### Current Limitations

- Controller replicas are intentionally single-instance; no leader-elected HA.
- Snapshot capability advertisement is driver-global while Linux-ZOL does not implement snapshots.
- `CreateSnapshot` routing is not yet derived from authoritative source-volume ownership.
- Linux-ZOL host access control uses allow-any-host pending an ACL design.
- RouterOS snapshot and disruption procedures still require live execution.

## Historical Milestones

The v0.1.0-v0.11.0 milestones record the RDS-focused predecessor's completed
implementation, automated-test, and documentation plans. They are historical
records, not current live-validation evidence. See `.planning/milestones/`.

The v0.11 snapshot work replaced the invalid Btrfs-subvolume approach with
RouterOS `/disk add copy-from`, added mock/sanity coverage and TC-08
documentation. TC-08 was not actually executed at milestone archive time.

## Current Requirements

Status labels are evidence-scoped:

- **Implemented:** production code exists.
- **Automated-tested:** unit, mock, sanity, or automated E2E coverage exists.
- **Live-validated:** a dated hardware scenario was executed and recorded.
- **Unsupported:** a backend explicitly rejects or lacks the operation.

### Live-validated

- Linux-ZOL provision, attach, filesystem mount, remount persistence, expand, and delete cleanup.
- Linux-ZOL controller restart and target-host reboot export reconstruction.
- RouterOS controller lifecycle and isolated data-path provision, mount, remount, online expansion, and delete cleanup.
- Configured driver-name isolation in the deployed controller attachment rebuild.

See `docs/nvmet-csi-live-validation.md` for exact dates, hardware, object IDs,
inventories, and limitations.

### Implemented and automated-tested; live validation pending

- RouterOS snapshot create/delete/list and same-backend restore: TC-08.
- Scheduled snapshots and retention: execute only after TC-08 passes.
- RouterOS NVMe reconnect and node-failure recovery: TC-09 and TC-11.
- RouterOS restart preservation: TC-10, approved maintenance window only.
- Linux-ZOL consumer drain and KubeVirt churn.

### Unsupported

- Linux-ZOL snapshots and snapshot restore.
- Cross-backend snapshot restore.
- Controller HA.
- NVMe multipath across independent targets.
- Application-consistent snapshots without application quiesce integration.

### Active

Execute TC-08 with a disposable RDS-backed volume under the isolated
`nvmet-rds-test.csi.whiskey.works` identity, then record exact Kubernetes and
RouterOS cleanup evidence.

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

- **RDS safety:** the appliance is fragile shared infrastructure; use isolated identities, dedicated credentials, rate-limited mutations, and exact inventory checks.
- **Maintenance windows:** RDS restart or equivalent disruption requires explicit approval.
- **Evidence:** documentation and automated tests never substitute for dated live hardware execution.
- **Backend scope:** RouterOS snapshot support must not be presented as Linux-ZOL support.
- **Data safety:** ownership metadata is required before destructive backend operations.

---
*Last updated: 2026-07-12 for current nvmet-csi status; archived milestone files remain historical records.*
