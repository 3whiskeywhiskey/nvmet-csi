# RouterOS RDS Hardening Notes

This document captures operational findings and design requirements for the
RDS-backed NVMe/TCP path while this repository is refactored toward
`nvmet-csi`.

The key premise is that RouterOS/RDS is fragile shared infrastructure. The CSI
driver must be conservative, idempotent, observable, and explicit about
recovery. A normal Kubernetes drain, reschedule, or cleanup loop must not be
able to create a RouterOS disk/export/session mutation storm.

## Current Context

- The metal k3s cluster uses Cilium, KubeVirt, CNPG, Vault, Gitea, Attic, and
  related infrastructure.
- A MikroTik RouterOS RDS box provides routing, MinIO, and NVMe/TCP storage.
- RDS-backed PVCs have historically been exposed as RouterOS-hosted NVMe/TCP
  exports.
- RDS is load-bearing for internet, MinIO, and storage. Do not reboot it
  without explicit operator approval.
- Critical workloads are being moved away from RDS-backed storage where
  possible. Direct host-local durable PVs on PMEM/NVMe hosts are preferred for
  first-class durable services.

## Observed Failure Pattern

During cluster churn, node drains, VM or pod reschedules, and attachment
cleanup, RDS has repeatedly kernel-crashed or rebooted.

RouterOS logs after reboot have included:

- `router was rebooted without proper shutdown, probably kernel failure`
- `kernel failure in previous boot`
- `Automatic supout.rif file generated due to service malfunction`

The working theory is not that RouterOS causes Kubernetes flapping. The working
theory is that Kubernetes or CSI flapping causes RouterOS storage churn, which
can trigger a RouterOS kernel or ROSE storage failure. When RDS crashes, the
blast radius includes Cilium/BGP/service routing, MinIO, and RDS-backed
storage.

Issue `#18` captured one concrete incident: autosupout files appeared around
bursty disk remove/add activity and an invalid `/disk/monitor-traffic` API path.
That issue is still relevant but narrower than the design requirements here.

## Likely Trigger Class

The suspect trigger class is rapid or unnecessary RouterOS storage mutations:

- repeated `/disk add`, `/disk remove`, export enable/disable, or snapshot-like
  operations during pod churn;
- stale NVMe/TCP attachments after node outage or reboot;
- remediation loops that delete and recreate backing objects instead of
  reconciling actual state;
- orphan cleanup acting on too broad a set of RouterOS objects;
- metrics or monitoring commands adding load or calling invalid RouterOS paths;
- RouterOS auto-mount or filesystem scanning of PVC backing disks.

The `mount=no` hypothesis needs serious testing: PVC backing disks may need to
be configured so RouterOS does not try to mount or scan them. The driver now
emits `mount=no` for new file-backed disk objects it creates (`CreateVolume`,
snapshot disk creation, and snapshot restore). If RouterOS is performing
filesystem work on file-backed PVC disks, disabling mount behavior could reduce
kernel interaction and lower crash risk. This is a creation-time mitigation; it
does not rewrite existing RouterOS disk objects.

## Operational Guardrails

These are standing rules for future refactor and incident work:

- Do not reboot RDS without explicit approval. It takes down routing, MinIO, and
  RDS-backed storage.
- Do not repeatedly stop/start MinIO on RDS. It can take a long time to become
  healthy after RDS reboot.
- Do not perform manual Kubernetes node mount surgery unless explicitly
  approved.
- Prefer clearing Kubernetes `VolumeAttachment` or CSI state and letting pods
  reattach cleanly.
- Treat `~/code/flux-repo` as the source of truth for Kubernetes manifests.
- Treat homelab repo manifests as stale until confirmed.
- Inventory and decide before deleting old `homelab-csi`, `rds-csi`, or
  `metal-csi` RouterOS objects.

## Refactor Requirements

The RDS compatibility backend and the future `nvmet-csi` backend abstraction
must keep these requirements visible:

1. **Observe before mutate.** Reconciliation should list actual state, diff it
   against desired state, and mutate only when the diff proves a change is
   required.
2. **No add/remove storms.** The driver must not delete/recreate RouterOS disks,
   exports, or sessions as a generic recovery path.
3. **Idempotent attach/detach.** Repeated attach/detach calls must converge to
   the same state without extra mutations.
4. **Monotonic recovery where possible.** Prefer marking, waiting, and reusing
   stable objects over tearing them down.
5. **Rate-limited RouterOS mutations.** All write operations against RouterOS
   storage APIs must have serialization, rate limiting, jittered backoff, and
   bounded retries.
6. **Separate reads from writes.** Metrics and health collection must not share
   the same pressure path as mutating operations, and failed metrics collection
   must not trigger storage cleanup.
7. **Explicit stale-state model.** Stale NVMe sessions, stale Kubernetes
   `VolumeAttachment` objects, stale RouterOS exports, and orphan backing files
   should be distinct states with distinct remediation.
8. **Auditable remediation.** Recovery operations should emit structured events
   and metrics that identify the volume, node, state observed, decision made,
   and mutation attempted.
9. **Backend contract must carry safety semantics.** The `pkg/backend` interface
   should expose enough information for the controller and reconcilers to avoid
   backend-specific delete/recreate shortcuts.
10. **RDS reboot is not a normal recovery primitive.** Recovery playbooks must
    avoid requiring RDS reboot.

## Questions To Answer

These questions should feed the seam capture, backend interface, RDS wrapper,
controller refactor, and migration runbook work:

1. Does RouterOS `mount=no` on PVC backing disks reduce or eliminate the crash
   trigger?
2. What exact RouterOS API or CLI sequence happens during a Kubernetes node
   drain, node reboot, pod eviction, KubeVirt migration, and CNPG failover?
3. Can the CSI driver reconcile stale exports and sessions without
   delete/recreate storms?
4. Can volume attach/detach be made monotonic and rate-limited?
5. What is the safest remediation command or playbook when a node returns with
   stale NVMe/TCP state?
6. Can we build a simulator or dry-run reconciler before pointing this at live
   RDS?
7. Which metrics and events are required to prove the driver stayed calm during
   churn?

## Data To Gather

Before and during controlled churn tests, gather:

- RouterOS logs:
  `/log/print where topics~"critical|error|nvme|disk|container"`
- autosupout timestamps and file names;
- `/disk print detail` for PVC/NVMe disks, including whether `mount` is enabled;
- RouterOS NVMe/TCP export/session state before and after node drain;
- Kubernetes `VolumeAttachment`, PV, PVC, pod events, and CSI controller logs;
- count of RouterOS mutations per minute during churn;
- correlation with node reboot/drain, pod eviction, CNPG failover/reconcile,
  KubeVirt VM respin, and RDS-backed PVC reattachment.

## Observability Requirements

`nvmet-csi` and the RDS compatibility backend should expose at least:

- attach latency and detach latency;
- RouterOS mutation count by operation and result;
- mutation rate-limiter delays and drops;
- export count, session count, and stale session count;
- stale `VolumeAttachment` count and reconciliation outcomes;
- RouterOS API or SSH errors by category;
- failed mutation count and last failure reason;
- reconciliation diff size: desired objects, actual objects, mutations planned,
  mutations applied;
- dry-run reconciliation output for simulator tests.

## Test And Simulator Requirements

The refactor should add test coverage before live RDS validation:

- fake RouterOS/RDS server that records exact command sequences and mutation
  rates;
- churn tests for node drain, node loss, pod reschedule, KubeVirt migration, and
  repeated attach/detach calls;
- dry-run reconciler mode that returns planned mutations without applying them;
- orphan inventory tests that expose planned disk/file deletes before applying
  any RouterOS mutation;
- regression test proving stale attachment cleanup does not delete/recreate the
  backing RouterOS disk;
- regression test for `mount=no` command shape on every driver-owned
  `/disk add type=file` path;
- tests proving monitoring failures cannot trigger storage mutations.

## Issue Mapping

Keep this document in view while executing these issues:

- `#5` seam capture: include every RDS mutation path and every stale-state
  assumption.
- `#6` backend interface: encode idempotency, observed-state, and typed error
  semantics.
- `#8` RDS backend wrapper: add RouterOS mutation throttling and explicit error
  mapping.
- `#10` controller inversion: controller must call backend methods that preserve
  observe-before-mutate behavior.
- `#11` attachment/reconciler decoupling: stale state handling must be explicit
  and must not rely on delete/recreate storms.
- `#15` migration runbook: document side-by-side operation and safe remediation.
- `#16` nvmet+ZFS backend: apply the same conservative reconciliation model.
- `#17` facility validation: include churn and crash-correlation data capture.
- `#18` RDS hardening: keep the monitor-traffic fix and stale-object inventory
  as concrete near-term mitigations.

## Prioritized Next Steps

1. Add this handoff to `DESIGN.md` once issue `#1` lands.
2. In issue `#5`, produce `docs/backend-seams.md` with mutation paths,
   stale-state paths, and RDS-specific failure modes.
3. In issue `#6`, make the backend interface state-oriented enough to avoid
   delete/recreate reconciliation.
4. In issue `#8`, implement an RDS mutation limiter and command recorder before
   moving more logic behind the backend.
5. In issue `#18`, test and document `mount=no` behavior in a controlled
   maintenance window, including `/disk print detail where slot=<pvc>` before
   and after new PVC creation.
