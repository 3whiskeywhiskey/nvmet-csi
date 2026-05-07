# nvmet-csi Design

This repository is the `nvmet-csi` fork of the RDS CSI driver. The goal is to
generalize the inherited CSI implementation so it can preserve the existing
RDS-backed path while adding a Linux ZFS + `nvmet` backend.

This is a fork-and-generalize project, not a rewrite. The existing RDS path is
load-bearing in the metal cluster during the migration, so compatibility and
calm operational behavior matter as much as the new backend.

## Origin

`whiskey-works/nvmet-csi` was bootstrapped on 2026-04-27 by mirror-pushing the
existing `rds-csi` repository at v0.11.0. The full commit and tag history was
preserved so old implementation decisions, tests, and milestone notes remain
available during the refactor.

The original implementation is a Kubernetes CSI driver for MikroTik RouterOS
ROSE Data Server. It provisions file-backed disks on RDS and exposes them over
NVMe/TCP, while Kubernetes nodes connect with `nvme-cli` and mount or publish
the resulting block devices.

## Stance

The migration stance is:

- keep the current RDS backend working until operators intentionally migrate
  workloads;
- abstract backend-specific storage operations behind a typed interface;
- make the controller, attachment manager, and reconcilers backend-neutral;
- add a Linux ZFS + `nvmet` backend after the abstraction is proven with RDS;
- avoid production disruption from code, image, module, or driver-name renames.

The RDS backend must be treated as fragile shared infrastructure. RDS also
provides routing and MinIO, so crash loops or RouterOS kernel failures have a
cluster-wide blast radius. The refactor must reduce mutation storms and make
recovery more explicit, not merely move existing behavior behind new packages.

See `docs/routeros-rds-hardening.md` for the detailed operational handoff and
hardening requirements.

## Preserve, Abstract, Keep, Add

| Category | Scope | Intent |
| --- | --- | --- |
| Preserve | `pkg/nvme`, `pkg/mount`, `pkg/observability`, `pkg/security`, `pkg/utils`, `pkg/circuitbreaker`, `pkg/driver/identity.go`, `test/*` | Keep proven CSI, NVMe, mount, observability, validation, and test behavior unless a local change is required by the backend abstraction. |
| Abstract | `pkg/driver`, `pkg/driver/server.go`, `pkg/driver/params.go`, `pkg/driver/controller.go`, `pkg/attachment`, `pkg/reconciler`, `cmd/rds-csi-plugin/main.go`, `deploy/*` | Move direct RDS knowledge out of shared controller and deployment surfaces. |
| Keep | Existing `pkg/rds` behavior through an RDS compatibility backend adapter | Existing RDS deployments must continue to work during the migration. |
| Add | `pkg/backend`, `pkg/backend/options`, `pkg/config`, `pkg/backend/rds`, `pkg/backend/nvmet` | Introduce a backend contract, RDS adapter, initial nvmet stub, then real ZFS + nvmet implementation. |

## Migration Map

The working migration map came from the earlier symphony execution plan. We are
preserving the plan as project guidance, but this repository no longer relies
on symphony as the agent runtime.

0. **Target bootstrap**: done by mirror-pushing `rds-csi` into
   `whiskey-works/nvmet-csi` with full history.
1. **Seam capture**: document every place where controller, attachment, and
   reconciler code knows about RDS. See issue `#5`.
2. **Backend scaffolding**: add `pkg/backend`, `pkg/config`, and
   `pkg/backend/options`. See issues `#6` and `#7`.
3. **Controller inversion**: extract `pkg/backend/rds`, add a stub
   `pkg/backend/nvmet`, and refactor `ControllerServer` to call the backend
   interface. See issues `#8`, `#9`, and `#10`.
4. **Attachment and reconciler decoupling**: remove RDS metadata assumptions
   from attachment recovery and orphan reconciliation. See issue `#11`.
5. **Deployment, config, and rename migration**: rename module path, driver
   binary, image refs, manifests, and Helm chart with an explicit migration
   runbook. See issues `#13`, `#14`, and `#15`.
6. **Compile and parity gate**: prove the RDS path is still behaviorally stable
   after abstraction, then tag the backend-abstraction milestone. See issue
   `#12`.

The real Linux ZFS + `nvmet` backend follows this abstraction work in issue
`#16`. Facility-backed runtime validation and production migration runbooks are
tracked in issue `#17`.

## Validation Policy

The first-party validation gate for abstraction work is compile, unit tests, and
non-runtime integration tests. Runtime validation for real ZFS + `nvmet`
requires privileged hosts with ZFS, configfs, kernel `nvmet` support, and
NVMe/TCP initiators. That validation is deferred to the facility-backed v1.0
track in issue `#17`.

Two inherited e2e failures, State Recovery and Orphan Detection, are accepted as
baseline until the relevant refactors or facility tests prove otherwise. If a
refactor makes either test pass, remove it from the baseline exception and
document the change in the PR.

RDS hardware tests that can disrupt routing or storage must only run during an
approved maintenance window.

## Rename Policy

Operator-approved breaking renames are expected:

- Go module path: legacy `git.srvlab.io/whiskey/rds-csi-driver` to the final
  `nvmet-csi` module path chosen in issue `#3`;
- binary: `rds-csi-plugin` to `nvmet-csi-plugin`;
- image registry path: moved under the `whiskey-works/nvmet-csi` registry
  namespace;
- driver name, StorageClass names, Helm chart names, labels, manifests, and
  documentation.

These renames are intentionally staged after backend abstraction so import and
deployment churn does not obscure semantic refactor review. Existing RDS
installations remain independent until operators choose an in-place migration
or side-by-side deployment model. The Helm alias and migration policy lives in
issue `#15`.

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
