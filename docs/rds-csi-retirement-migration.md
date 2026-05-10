# RDS CSI Retirement Migration Plan

This plan moves the cluster off the legacy `rds.csi.srvlab.io` driver before
any node-roll stress test. The goal is to remove the known crash trigger first,
then optionally reintroduce selected RDS-backed storage through `nvmet-csi`
after the ZoL path has carried real workloads.

Do not use this plan as approval to reboot RDS, manually edit node mounts, or
bulk-delete RouterOS storage objects. RDS cleanup is a later audited step.

## Goal

1. Move every active workload off PVCs provisioned by `rds-csi`.
2. Make old RDS PVs safe to abandon by setting reclaim policy to `Retain`
   before any PVC deletion.
3. Remove `rds-csi` from the cluster once no live workload or
   `VolumeAttachment` depends on it.
4. Roll Kubernetes nodes repeatedly to validate that the old rds-csi
   attach/detach storm is gone.
5. Add an `nvmet-csi` RDS StorageClass later and migrate selected workloads back
   to RDS only through the hardened reconciler.

## Safety Rules

- Patch legacy RDS PVs to `Retain` before deleting old PVCs. Several current
  `rds-nvme` PVs still have `Delete`, which would hand deletion to the known
  fragile driver.
- Keep `nvmet-zol-r740xd` non-default.
- Prefer a retain-style ZoL StorageClass for migrated durable workloads, or
  patch each new migrated PV to `Retain` immediately after provisioning.
- Do not remove `rds-csi` while any `VolumeAttachment` still names
  `rds.csi.srvlab.io`.
- Do not manually disconnect host NVMe sessions or edit mounts during migration.
  Use workload scale-down, Kubernetes detach, and controlled copy jobs.
- Treat old RDS disks and released PVs as retained rollback artifacts until a
  separate RDS cleanup plan is reviewed.

## Live Snapshot

Captured on 2026-05-10 from the metal cluster.

Active `rds-csi` attachments:

| Namespace | PVC | PV | Node |
| --- | --- | --- | --- |
| gitea | gitea-shared-storage | `pvc-02f8ba39-a719-4dce-b512-193894a5776b` | r640-1 |
| infra-vms | gpu-worker-1-rancher-data | `pvc-115b33c8-0b64-4279-a82c-f1d9a69e0b96` | c4140 |
| monitoring | alertmanager-kube-prometheus-stack-alertmanager-db-alertmanager-kube-prometheus-stack-alertmanager-0 | `pvc-9de164be-65d6-471c-bc34-6bba87a0d238` | r640-1 |
| authelia | authelia-data | `pvc-10c4084a-3f9f-44d4-b1af-fef8d2238ded` | r640-1 |
| comfyui | comfyui-output | `pvc-76367254-bd09-43e1-8dd8-4e6b043bf1a1` | gpu-worker-1 |
| monitoring | prometheus-kube-prometheus-stack-prometheus-db-prometheus-kube-prometheus-stack-prometheus-0 | `pvc-0fae82d4-d00a-460d-85e4-79504a866c6c` | r640-3 |
| infra-vms | gpu-worker-1-rootdisk | `pvc-c89861bc-9a36-4102-9148-722ca3331dac` | c4140 |

Legacy RDS PVs that still had reclaim policy `Delete` at snapshot time:

| Namespace | PVC | PV |
| --- | --- | --- |
| srvlab-cluster | srvlab-control-1-rancher | `pvc-024fb630-db5e-4e3a-9488-e2463acd7721` |
| monitoring | prometheus-kube-prometheus-stack-prometheus-db-prometheus-kube-prometheus-stack-prometheus-0 | `pvc-0fae82d4-d00a-460d-85e4-79504a866c6c` |
| gitea | nix-store-act-runner-0 | `pvc-15a6a87e-dbe9-4155-80b6-567241f74d52` |
| vllm | eval-results | `pvc-220b27d4-5b03-4e81-97d5-ece065f264e4` |
| nix-cache | attic-storage | `pvc-318fccd1-aabf-462d-9033-4564c850485a` |
| comfyui | comfyui-output | `pvc-76367254-bd09-43e1-8dd8-4e6b043bf1a1` |
| gitea | docker-certs-act-runner-0 | `pvc-81a673b4-fd90-4c3a-a016-a8d4aafac4d8` |
| monitoring | alertmanager-kube-prometheus-stack-alertmanager-db-alertmanager-kube-prometheus-stack-alertmanager-0 | `pvc-9de164be-65d6-471c-bc34-6bba87a0d238` |
| srvlab-cluster | srvlab-control-2-rancher | `pvc-b71842e9-12c4-4089-9477-96d616141a62` |
| network-observability | data-clickhouse-shard0-0 | `pvc-bbfb3854-e881-4ab0-907e-0791961cf028` |
| infra-vms | gpu-worker-1-rootdisk | `pvc-c89861bc-9a36-4102-9148-722ca3331dac` |
| srvlab-cluster | srvlab-control-3-rancher | `pvc-d6e84b87-74cf-4fdc-aed2-4b6c1d10e862` |
| gitea | runner-data-act-runner-0 | `pvc-dfe79f63-0719-45df-9071-4754d58b94fa` |
| vllm | glm5-profiling-data | `pvc-f2c1ad92-785c-470b-b7ce-6a82a5f4e2ed` |

Legacy RDS PVs already set to `Retain`:

| Namespace | PVC | PV |
| --- | --- | --- |
| gitea | gitea-shared-storage | `pvc-02f8ba39-a719-4dce-b512-193894a5776b` |
| authelia | authelia-data | `pvc-10c4084a-3f9f-44d4-b1af-fef8d2238ded` |
| infra-vms | gpu-worker-1-rancher-data | `pvc-115b33c8-0b64-4279-a82c-f1d9a69e0b96` |
| postgres | postgres-data | `pvc-735ac893-549f-4725-8f2f-0427a83fe214` |
| cairn | cairn-postgres-data | `pvc-e154ec04-a1ee-45aa-be08-7c1696a3bb66` |

## Phase 0: Guard Rails

Before migrating data:

1. Capture the current PVC, PV, `VolumeAttachment`, pod, and VM inventory.
2. Patch all `rds.csi.srvlab.io` PVs to `Retain`.
3. Add a durable ZoL retain class, for example `nvmet-zol-r740xd-retain`, or
   explicitly patch each new migrated PV to `Retain`.
4. Keep a copy-job manifest template for cold filesystem copies.
5. Confirm Flux has no unexpected default StorageClass changes.

Recommended guard command for legacy RDS PVs:

```sh
kubectl get pv -o json \
  | jq -r '.items[]
      | select(.spec.csi.driver == "rds.csi.srvlab.io")
      | .metadata.name' \
  | xargs -r -n1 kubectl patch pv --type=merge \
      -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}'
```

## Phase 1: Retire Inactive Or Disposable RDS PVCs

These should move first because they reduce the old driver's surface area with
minimal application risk.

| Workload | Current state | Recommended action |
| --- | --- | --- |
| `srvlab-cluster/srvlab-control-*` | VMs stopped | Patch PVs `Retain`, then remove or recreate on non-RDS storage only if still needed. |
| `network-observability/clickhouse-shard0` | StatefulSet scaled to zero | Patch PV `Retain`; delete/recreate only if historical data is disposable. |
| `vllm/eval-results` | Not attached | Patch PV `Retain`; recreate on ZoL or NFS if needed. |
| `vllm/glm5-profiling-data` | Not attached | Patch PV `Retain`; recreate on ZoL or NFS if needed. |
| `gitea/*act-runner-0` | Old StatefulSet storage, not attached | Patch PVs `Retain`; verify current runner deployment does not use them, then retire. |
| `postgres/postgres-data` | Old PVC, not attached | Already `Retain`; remove only after confirming CNPG replacement is source of truth. |
| `cairn/cairn-postgres-data` | Deployment scaled to zero | Already `Retain`; remove only after confirming it is obsolete. |

## Phase 2: Migrate Active Filesystem PVCs

Use cold filesystem copies:

1. Create a new ZoL PVC.
2. Scale the workload to zero.
3. Run a one-shot copy pod mounting old RDS PVC read-only and new ZoL PVC
   read-write.
4. Verify file counts and application-specific ownership.
5. Patch Flux to point the workload at the new PVC or StorageClass.
6. Bring the workload back and validate.
7. Leave the old RDS PV retained.

Suggested order:

| Workload | Why this order |
| --- | --- |
| `comfyui/comfyui-output` | Active but not core cluster auth/source control; good first real copy. |
| `authelia/authelia-data` | Small and important; migrate after the copy pattern is proven. |
| `nix-cache/attic-storage` | Operationally useful but rebuildable; migrate before Gitea if downtime is acceptable. |
| `gitea/gitea-shared-storage` | Larger and more critical; use planned downtime and a final sync. |

For Gitea, prefer a two-pass copy: first while online if the chart allows a
temporary helper pod, then a final copy with Gitea scaled down.

## Phase 3: Monitoring Data

Monitoring already has a `prometheus-zfs-local` path in Flux. Decide whether the
old RDS Prometheus and Alertmanager histories matter.

If history is disposable:

1. Patch old RDS PVs to `Retain`.
2. Remove or disable the old RDS-backed Prometheus/Alertmanager storage values.
3. Confirm the active monitoring stack uses non-RDS storage.

If history matters, handle it as a workload-specific export/import instead of a
blind live filesystem copy.

## Phase 4: KubeVirt VM PVCs

Do not live-copy KubeVirt disks.

For `infra-vms/gpu-worker-1-rootdisk` and
`infra-vms/gpu-worker-1-rancher-data`:

1. Stop the VM cleanly.
2. Wait for both RDS `VolumeAttachment` objects to detach.
3. Copy or clone the filesystem content to new ZoL PVCs.
4. Patch the VM/DataVolume references in Flux.
5. Boot and validate the VM.
6. Leave old RDS PVs retained.

Use CDI clone/import only after confirming it will not cause rds-csi delete or
detach churn. A simple cold copy is easier to reason about.

## Phase 5: Remove rds-csi

Remove the old driver only after all checks are true:

```sh
kubectl get volumeattachments.storage.k8s.io \
  -o custom-columns=NAME:.metadata.name,ATTACHER:.spec.attacher,PV:.spec.source.persistentVolumeName,NODE:.spec.nodeName

kubectl get pods -A -o wide
kubectl get pvc -A
kubectl get csidrivers
kubectl get storageclasses
```

Required state:

- no `VolumeAttachment` has `ATTACHER=rds.csi.srvlab.io`;
- no running pod or VM uses a PVC backed by `rds.csi.srvlab.io`;
- Flux no longer creates new PVCs with `storageClassName: rds-nvme`,
  `rds-nvme-retain`, or `rds-csi-rwx`;
- old RDS PVs are retained and treated as rollback artifacts;
- `rds-csi` controller, node DaemonSet, `CSIDriver`, StorageClasses, and
  ServiceMonitors are removed from Flux.

## Phase 6: Node-Roll Stress Test

Run the node-roll test only after `rds-csi` is gone.

Expected result:

- no RDS reboot;
- no RouterOS critical/kernel-failure logs;
- no `rds.csi.srvlab.io` `VolumeAttachment` churn because the driver is absent;
- `nvmet-csi` ZoL attachments reconcile without delete/recreate storms.

Record:

- `VolumeAttachment` creation/deletion counts;
- `nvmet-csi` attach and detach logs;
- zvol and nvmet subsystem count on r740xd;
- pod and VM reschedule events;
- RDS logs around the roll, even though RDS should not be touched.

## Phase 7: Optional RDS Reintroduction Through nvmet-csi

Only after the ZoL-backed workload and node-roll tests are clean:

1. Add an `nvmet-csi` RDS compatibility StorageClass with a distinct base path.
2. Keep it non-default.
3. Confirm planned RouterOS disk creation uses `mount=no`.
4. Start with one disposable RDS compatibility PVC.
5. Verify single-create, single-attach, single-detach behavior.
6. Migrate only selected second-class or transitional workloads back to RDS.

This phase is where RDS crash resistance is tested. It should not be mixed with
the first retirement of `rds-csi`.
