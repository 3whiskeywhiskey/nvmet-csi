# nvmet-csi Design

This repository is the successor to the original RDS-focused CSI driver. The
goal is to make `nvmet-csi` a backend-neutral NVMe/TCP CSI driver that can serve
Linux ZFS + `nvmet` targets first while retaining a RouterOS/RDS backend for
transitional and lower-priority workloads.

This is a fork-and-generalize project, not a rewrite. The original RDS path is
load-bearing in the metal cluster during the migration, so calm operational
behavior and explicit cutover controls matter as much as the new backend.

## Origin

`whiskey-works/nvmet-csi` was bootstrapped on 2026-04-27 from the predecessor
RDS-focused repository at v0.11.0. The full commit and tag history was preserved
so old implementation decisions, tests, and milestone notes remain available
during the refactor.

The original implementation is a Kubernetes CSI driver for MikroTik RouterOS
ROSE Data Server. It provisions file-backed disks on RDS and exposes them over
NVMe/TCP, while Kubernetes nodes connect with `nvme-cli` and mount or publish
the resulting block devices.

## Stance

The migration stance is:

- keep the RouterOS/RDS backend isolated and explicit until operators
  intentionally migrate workloads;
- abstract backend-specific storage operations behind a typed interface;
- make the controller, attachment manager, and reconcilers backend-neutral;
- make Linux ZFS + `nvmet` the primary backend path for new validation;
- avoid production disruption from code, image, module, or driver-name renames
  by deploying side-by-side instead of preserving old identities.

The RDS backend must be treated as fragile shared infrastructure. RDS also
provides routing and MinIO, so crash loops or RouterOS kernel failures have a
cluster-wide blast radius. The refactor must reduce mutation storms and make
recovery more explicit, not merely move existing behavior behind new packages.

See `docs/routeros-rds-hardening.md` for the detailed operational handoff and
hardening requirements.

## Current Capability and Evidence Status

Current documents distinguish **Implemented**, **Automated-tested**,
**Live-validated**, and **Unsupported**. A completed plan or documented
hardware procedure is not live-validation evidence.

| Backend / capability | Status |
| --- | --- |
| Linux-ZOL volume lifecycle, expansion, and target reconciliation | Implemented, automated-tested, and live-validated on Metal in July 2026 |
| Linux-ZOL snapshots and snapshot restore | Unsupported |
| RouterOS/RDS ordinary volume lifecycle through nvmet-csi | Implemented, automated-tested, and live-validated on an isolated Metal deployment in July 2026 |
| RouterOS/RDS snapshot create/delete/list and same-backend restore | Implemented and automated-tested; real-RouterOS TC-08 pending |
| RouterOS/RDS disruption and node-roll resilience | Automated/mock-tested or documented; TC-09-TC-11 pending live execution |

Snapshot capabilities are advertised globally by a driver instance even though
only RouterOS implements them. Until source-volume routing and capability
handling are hardened, operate RouterOS snapshot support and Linux-ZOL under
separate CSI identities and backend-specific `VolumeSnapshotClass` objects.

## Preserve, Abstract, Keep, Add

| Category | Scope | Intent |
| --- | --- | --- |
| Preserve | `pkg/nvme`, `pkg/mount`, `pkg/observability`, `pkg/security`, `pkg/utils`, `pkg/circuitbreaker`, `pkg/driver/identity.go`, `test/*` | Keep proven CSI, NVMe, mount, observability, validation, and test behavior unless a local change is required by the backend abstraction. |
| Abstract | `pkg/driver`, `pkg/driver/server.go`, `pkg/driver/params.go`, `pkg/driver/controller.go`, `pkg/attachment`, `pkg/reconciler`, `cmd/nvmet-csi-plugin/main.go`, `deploy/*` | Move direct RDS knowledge out of shared controller and deployment surfaces. |
| Keep | Existing `pkg/rds` behavior through a RouterOS/RDS backend adapter | Existing RDS-backed workloads remain independent until operators choose a migration. |
| Added | `pkg/backend`, `pkg/backend/linuxzol`, `pkg/backend/routeros` | Typed backend contract with production Linux-ZOL and RouterOS adapters. |

## Migration Status

The original backend-neutralization plan is complete:

1. The predecessor repository and history were preserved.
2. Storage operations were moved behind the typed `pkg/backend` contract.
3. RouterOS/RDS was retained through an explicit adapter.
4. The real Linux ZFS + kernel `nvmet` backend was implemented.
5. Controller, attachment, and reconciliation paths were made backend-aware.
6. Deployment and runtime identity became configurable for side-by-side use.
7. Linux-ZOL and isolated RouterOS ordinary lifecycle paths were live-validated.

Remaining work is validation and capability hardening, not backend scaffolding:

- real-RouterOS snapshot TC-08;
- scheduled snapshot execution after TC-08;
- RouterOS resilience TC-09-TC-11;
- Linux-ZOL drain and KubeVirt churn;
- snapshot routing from authoritative source-volume ownership;
- an explicit policy for globally advertised snapshot capabilities.

## Validation Policy

Code support, automated coverage, and hardware execution are reported
separately. `docs/nvmet-csi-live-validation.md` is the source of truth for
dated live observations. Historical `.planning/milestones/` checkmarks record
completed deliverables, not necessarily hardware execution.

Linux-ZOL validation uses disposable volumes and verifies every layer:
Kubernetes objects, zvol metadata, configfs exports, client discovery, mount
behavior, persisted data, and deletion cleanup. RouterOS tests use a dedicated
CSI identity, non-default StorageClass, dedicated credentials, mutation
rate-limits, and exact appliance-side inventory checks. RDS reboot or other
high-blast-radius tests require an approved maintenance window.

## Rename Policy

Operator-approved breaking renames are expected:

- Go module path: `gitea.whiskey.works/whiskey-works/nvmet-csi`;
- binary: `nvmet-csi-plugin`;
- image registry path: moved under the `whiskey-works/nvmet-csi` registry
  namespace;
- driver name, StorageClass names, Helm chart names, labels, manifests, and
  documentation.

These renames are intentionally breaking. Existing predecessor installations
remain independent until operators choose a side-by-side deployment or an
explicit migration.

## Backend Contract Sketch

The backend interface is the load-bearing design boundary. The detailed
contract belongs in issue `#6`, but it should cover at least:

- create, delete, expand, get, and list volumes;
- attach and detach, including idempotent repeated calls;
- list observed backend state for reconciliation;
- distinguish active, stale, orphaned, missing, and unimplemented states;
- expose stable volume context for CSI publish and node staging;
- return typed errors that the driver can translate into gRPC status codes;
- support dry-run or planned-mutation reporting for reconciliation tests.

The interface should be state-oriented enough that reconcilers can observe
actual backend state, diff against desired state, and mutate only when needed.
It should not force generic controller code into backend-specific
delete/recreate recovery shortcuts.

## RDS Hardening Principles

The RDS compatibility backend must improve safety while preserving behavior:

- observe before mutate;
- avoid add/remove storms;
- serialize and rate-limit RouterOS storage mutations;
- separate metrics and health reads from storage writes;
- investigate RouterOS `mount=no` for PVC backing disks;
- model stale Kubernetes `VolumeAttachment`, stale NVMe sessions, stale exports,
  and orphaned backing files as distinct states;
- emit auditable events and metrics for every remediation decision;
- avoid requiring RDS reboot for normal recovery.

These principles also apply to the future Linux ZFS + `nvmet` backend. The new
backend should not inherit avoidable churn patterns from the RDS path.

## Forward Pointers

- Issue `#5`: RDS seam capture and mutation inventory.
- Issue `#6`: backend interface contract and error taxonomy.
- Issue `#8`: RDS backend wrapper.
- Issue `#10`: controller inversion.
- Issue `#11`: attachment and reconciler decoupling.
- Issue `#15`: Helm rename and migration-from-RDS runbook.
- Issue `#16`: real Linux ZFS + `nvmet` backend.
- Issue `#17`: facility-backed runtime validation.
- Issue `#18`: near-term RDS hardening and monitor-traffic fix.
