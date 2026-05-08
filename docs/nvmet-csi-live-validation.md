# nvmet-csi Side-by-Side Validation Plan

This plan covers the safe path for validating `nvmet-csi` beside the existing
nvmet-csi deployment. The goal is to prove the Linux ZoL/nvmet path first on the
r740xd NVMe ZFS pool, then add RDS compatibility testing only after the driver
has already demonstrated calm reconciliation behavior without touching RDS.

## What Can Be Proven Without Live RDS

The following work can be completed in unit tests, fake backends, and CI before
any cluster or RDS test:

- backend selection from StorageClass parameters;
- controller observe-before-mutate plans for create, delete, resize, repair, and
  conflict states;
- exact command sequences for Linux ZoL/nvmet local and SSH executors;
- exact command sequences for RouterOS/RDS compatibility operations;
- dry-run plan output that reports planned mutations without applying them;
- mutation rate limiting, serialization, backoff, and error mapping;
- orphan inventory behavior with dry-run and confirmation windows;
- stale `VolumeAttachment` cleanup that deletes Kubernetes attachment objects
  without touching node mounts or backend exports;
- controller and node idempotency under repeated CSI calls;
- Helm/Kustomize rendering for a second CSI driver name and non-default
  StorageClasses.

Live hardware is required for kernel/configfs behavior, kubelet mount behavior,
NVMe/TCP sessions, Cilium/routing interactions, and RouterOS crash-correlation
evidence.

## Side-by-Side Install Shape

Install `nvmet-csi` as a distinct CSI driver from the current RDS deployment.
The current binary supports `-driver-name`; the side-by-side deployment must use
a new value such as `nvmet.csi.whiskey.works`.

Required isolation:

- separate namespace, ServiceAccounts, RBAC objects, sockets, and `CSIDriver`;
- no default StorageClass during validation;
- no reuse of the existing `nvmet.csi.whiskey.works` driver name;
- separate controller logs and metrics labels so mutation counts are attributable
  to the new driver;
- existing nvmet-csi StorageClasses remain untouched until explicit cutover.

The CSI node service should remain backend-agnostic. It only needs the target
address, port, NQN, filesystem, and NVMe reconnect settings from the volume
context. Backend-specific behavior belongs in the controller.

## StorageClass Strategy

Start with explicit StorageClasses rather than PVC annotations.

Recommended initial classes:

- `nvmet-zol-r740xd`: `backend=linux-zol-nvmet`, `executor=host-namespace`;
- `nvmet-zol-r740xd-ssh`: `backend=linux-zol-nvmet`, `executor=ssh`;
- `nvmet-rds-compat`: `backend=routeros-rds`, used only after ZoL validation.

Use separate StorageClasses because the external provisioner passes
StorageClass parameters to `CreateVolume`, while PVC annotations are not a
stable backend selection contract. A single `nvmet-csi` controller can serve
multiple backend types if it can reach and safely control each target, but the
StorageClass should make that target explicit and auditable.

For the host-namespace ZoL path, schedule the controller on the r740xd ZFS/nvmet
host and grant only the host access it needs:

- ZFS tools and access to the selected pool;
- `/sys/kernel/config/nvmet` for configfs export management;
- `/dev/zvol` for zvol device paths;
- host PID namespace access so the controller can enter the host mount namespace
  and use the host's matching ZFS userland;
- a stable target address on the storage host matching the StorageClass
  `targetAddress`/`nvmeAddress`;
- node selector or affinity pinning to the storage host;
- tolerations only if the storage host is tainted for infrastructure workloads.

If RDS compatibility and local ZoL are served by one controller instance, that
controller inherits both blast radii. Prefer separate StorageClasses at a
minimum; consider separate deployments and driver names if operational isolation
matters more than a single migration surface.

## Validation Phases

### Phase 0: Preflight, No Mutations

- Confirm r740xd pool name, free space, and host labels.
- Confirm no validation StorageClass is default.
- Confirm existing nvmet-csi PVCs and `VolumeAttachment` objects still reference
  the old driver name.
- Capture baseline controller logs and metrics for both drivers.
- Confirm no live RDS commands are required for ZoL validation.
- Run the read-only ZoL host preflight from the controller image on the storage
  host before creating PVCs:
  `nvmet-csi-plugin -linux-zol-preflight -linux-zol-preflight-executor=host-namespace -linux-zol-preflight-zfs-pool=tank/nvmet -linux-zol-preflight-target-address=<r740xd-nvme-ip> -linux-zol-preflight-nvme-port=4420`.
  This checks the pool, configfs, zvol device root, and any existing nvmet port
  attributes without creating zvols or exports.
- For Helm installs, enable `linuxZOLPreflight.enabled=true` with the same
  `executor`, `zfsPool`, `targetAddress`, and `nvmePort` values as the test
  StorageClass. The rendered Job is read-only and should complete successfully
  before any PVCs are created.
- Also enable `linuxZOLPlan.enabled=true` with a disposable
  `linuxZOLPlan.volumeID` and the same target values. The rendered Job prints
  the observed action and exact zfs/configfs commands, but does not apply them.

### Phase 1: Linux ZoL Dry Run and Command Audit

- Run controller/backend tests in CI.
- Review planned commands for create, repair, resize, and delete.
- For a live read-only plan against r740xd, run:
  `nvmet-csi-plugin -linux-zol-plan -linux-zol-plan-action=ensure -linux-zol-plan-executor=host-namespace -linux-zol-plan-zfs-pool=tank/nvmet -linux-zol-plan-target-address=<r740xd-nvme-ip> -linux-zol-plan-volume-id=pvc-11111111-1111-1111-1111-111111111111 -linux-zol-plan-size-bytes=1073741824`.
  This observes zfs/configfs state, prints the action and exact commands, and
  exits without applying mutations.
- Confirm plans are no-op after a successful create.
- Confirm conflict states do not generate delete/recreate plans.
- Confirm create and repair plans configure the Linux nvmet TCP port attributes:
  `addr_trtype=tcp`, `addr_adrfam`, `addr_traddr`, and `addr_trsvcid`.
- Confirm non-empty mismatched nvmet port attributes produce a conflict or a
  guarded command failure, not an automatic rewrite of a shared port.
- Confirm delete plans remove expected nvmet links before destroying zvols.

### Phase 2: Local ZoL Smoke Test

- Install `nvmet-csi` side-by-side with only `nvmet-zol-r740xd` enabled.
- Create one disposable PVC and one pod.
- Verify zvol properties identify the CSI volume ID, NQN, and port.
- Verify nvmet subsystem, namespace, port listener attributes, and port link
  exist.
- Verify the node sees one NVMe/TCP session and the filesystem mounts.
- Delete the pod and PVC; verify cleanup converges without repeated mutations.

### Phase 3: Local ZoL Churn Test

Run churn only against disposable ZoL-backed workloads:

- pod delete/recreate loop;
- node plugin restart;
- controller restart;
- drain of a non-storage consumer node;
- KubeVirt test VM respin if needed.

Record per-minute mutation counts, plan actions, attach/detach latency, stale
attachment count, and nvmet session count. The expected result is a small,
bounded number of mutations with no delete/recreate storm.

### Phase 4: RDS Compatibility Preflight

Do not create RDS-backed PVCs yet.

- Confirm RouterOS/RDS compatibility StorageClass is not default.
- Confirm `mount=no` appears in planned `/disk add type=file` commands.
- Confirm RDS mutation guard settings are enabled.
- Confirm orphan reconciliation is disabled or dry-run with startup scan off.
- Capture RDS baseline logs:
  `/log/print where topics~"critical|error|nvme|disk|container"`.
- Capture `/disk print detail` for existing CSI-owned PVC disks and note
  whether `mount` is enabled.

### Phase 5: Single RDS Compatibility PVC

Only after Phase 4 is clean:

- create one low-priority RDS compatibility PVC;
- verify exactly one create mutation sequence;
- verify the new RouterOS disk has `mount=no`;
- attach it to one disposable pod;
- delete the pod and PVC;
- verify delete cleanup does not require RDS reboot.

Do not run drains, VM churn, CNPG failover, or bulk PVC tests in this phase.

### Phase 6: Controlled RDS Churn

Run this only after explicit approval.

- Keep test workload count small.
- Gather RouterOS logs before, during, and after.
- Gather `supout.rif` timestamps if generated.
- Track RouterOS mutation count per minute.
- Track Kubernetes `VolumeAttachment`, PV, PVC, pod events, and controller logs.
- Correlate every mutation burst with node drain, pod eviction, VM respin, or
  PVC reattachment.

RDS reboot is not part of this test plan unless explicitly approved for a
maintenance window.

## Safe Remediation During Validation

Use Kubernetes state repair before host or RouterOS surgery:

1. Identify stale `VolumeAttachment` objects for the test PVC.
2. If a node is gone or returned with stale state, clear the stuck
   `VolumeAttachment` and respin the pod.
3. Let CSI reattach cleanly.
4. Avoid manual mount edits on Kubernetes nodes unless explicitly approved.
5. Avoid direct RouterOS disk/export cleanup unless the planned mutation and
   exact object names have been reviewed.
6. Never reboot RDS as normal recovery.

Node-side observation commands such as `nvme list-subsys`, `findmnt`, and pod
events are acceptable for diagnosis. Manual unmount, forced disconnect, or RDS
object deletion is not part of the default remediation path.

## Decision Points

Use separate `nvmet-csi` instances when:

- local ZoL needs host-level privileges that RDS compatibility should not have;
- RDS credentials should not live on the ZoL controller;
- operational ownership differs by backend;
- one backend needs disruptive testing while the other remains stable.

Use one `nvmet-csi` instance with multiple StorageClasses when:

- a single controller location can safely reach all targets;
- credentials and host privileges are acceptable in one trust boundary;
- metrics labels clearly separate backend, executor, and target;
- backend selection remains explicit in StorageClass parameters.

The first live rollout should optimize for isolation and auditability, not for
minimum manifest count.
