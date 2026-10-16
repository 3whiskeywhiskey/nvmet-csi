# nvmet-zol-r740xd First Install Runbook

This runbook validates the Linux ZoL + kernel nvmet path beside the existing
`rds-csi` deployment. It does not require RouterOS/RDS mutations.

## Preconditions

- r740xd is a Kubernetes worker with ZFS tools, the target ZFS pool, and kernel
  nvmet configfs available.
- r740xd has a stable node label, recommended:
  `kubernetes.io/hostname=r740xd`.
- The selected pool or dataset exists, for example `tank/nvmet`.
- The selected NVMe/TCP target address is reachable from consumer nodes on the
  v68 jumbo-frame storage VLAN. The live r740xd address is `198.51.100.23`.
  Preflight requires the interface that owns this address to have MTU >= 9000.
- Existing RDS-backed StorageClasses remain non-default or unchanged.
- No RDS reboot, RouterOS disk cleanup, or node mount surgery is part of this
  test.

## Install Profile

Start from the chart example:

```sh
deploy/helm/nvmet-csi/examples/nvmet-zol-r740xd-values.yaml
```

Before applying it, confirm these values match the live host:

- `controller.nodeSelector.kubernetes.io/hostname`
- `linuxZOLPreflight.zfsPool`
- `linuxZOLPreflight.targetAddress`
- `linuxZOLPreflight.minMTU`
- `linuxZOLPlan.zfsPool`
- `linuxZOLPlan.targetAddress`
- `storageClasses[0].zfsPool`
- `storageClasses[0].targetAddress`
- `storageClasses[0].nvmeAddress`

For the metal cluster, these target address fields should stay on `198.51.100.23`,
not the routed v67/control-plane addresses.

Render locally first:

```sh
helm template nvmet-zol deploy/helm/nvmet-csi \
  --namespace nvmet-csi \
  -f deploy/helm/nvmet-csi/examples/nvmet-zol-r740xd-values.yaml
```

Confirm the rendered output has only the `nvmet-zol-r740xd` StorageClass and
does not render RDS credentials or `-rds-address`.

## Preflight

Install or upgrade the side-by-side release:

```sh
helm upgrade --install nvmet-zol deploy/helm/nvmet-csi \
  --namespace nvmet-csi \
  --create-namespace \
  -f deploy/helm/nvmet-csi/examples/nvmet-zol-r740xd-values.yaml
```

Wait for the controller and node pods:

```sh
kubectl -n nvmet-csi rollout status deploy/nvmet-zol-nvmet-csi-controller
kubectl -n nvmet-csi rollout status ds/nvmet-zol-nvmet-csi-node
```

Run and inspect the read-only jobs:

```sh
kubectl -n nvmet-csi logs job/nvmet-zol-nvmet-csi-linux-zol-preflight
kubectl -n nvmet-csi logs job/nvmet-zol-nvmet-csi-linux-zol-plan
```

Expected result:

- preflight passes;
- the plan job reports a create plan for the disposable test ID;
- no command has been applied by the plan job.

## Disposable PVC Smoke Test

Create one small PVC and pod:

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: nvmet-zol-smoke
  namespace: default
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: nvmet-zol-r740xd
  resources:
    requests:
      storage: 1Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: nvmet-zol-smoke
  namespace: default
spec:
  restartPolicy: Never
  containers:
    - name: shell
      image: busybox:1.36
      command: ["sh", "-c", "echo ok > /data/probe && cat /data/probe && sleep 300"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: nvmet-zol-smoke
```

Verify Kubernetes state:

```sh
kubectl -n default get pvc,pod nvmet-zol-smoke
kubectl get pv -o wide | grep nvmet-zol-smoke
kubectl get volumeattachments.storage.k8s.io
```

Verify r740xd state from a privileged host shell or the controller logs:

```sh
zfs get -H -o property,value org.nvmet-csi:volume-id,org.nvmet-csi:nqn,org.nvmet-csi:port tank/nvmet/<pvc-id>
find /sys/kernel/config/nvmet/subsystems -maxdepth 3 -name device_path -print -exec cat {} \;
find /sys/kernel/config/nvmet/ports -maxdepth 3 -type l -ls
```

Verify the consumer node sees exactly the expected NVMe/TCP session:

```sh
nvme list-subsys
findmnt | grep nvmet-zol-smoke
```

## Cleanup

Delete the pod and PVC:

```sh
kubectl -n default delete pod nvmet-zol-smoke
kubectl -n default delete pvc nvmet-zol-smoke
```

Confirm cleanup converges:

```sh
kubectl get pv,volumeattachments.storage.k8s.io
zfs list -t volume -r tank/nvmet
find /sys/kernel/config/nvmet/subsystems -maxdepth 1 -type d -name '*nvmet-csi*'
find /sys/kernel/config/nvmet/ports -maxdepth 3 -type l -ls
```

Expected result:

- the disposable zvol is gone;
- the matching nvmet subsystem and port symlink are gone;
- unrelated zvols, subsystems, and sessions remain untouched;
- no RDS logs or RouterOS objects change.

## Restart And Churn Smokes

After the single PVC smoke passes, run these only with disposable namespaces and
the `nvmet-zol-r740xd` StorageClass.

Controller restart:

1. Create a one-PVC smoke pod and verify it can read/write.
2. Delete the `nvmet-csi-controller` pod.
3. Wait for the Deployment rollout.
4. Verify the workload can still read the existing file and write a new file.
5. Delete the namespace and confirm the PV and `nvmet.csi.whiskey.works`
   `VolumeAttachment` disappear.

Expected controller logs include state rebuild from `VolumeAttachment` objects
and `Attachment reconciliation complete: no stale attachments`.

Node plugin restart:

1. Create a one-PVC smoke pod and note the node where it is running.
2. Delete only the nvmet-csi node DaemonSet pod on that node.
3. Wait for the DaemonSet rollout.
4. Verify the workload can still read the existing file and write a new file.
5. Delete the namespace and confirm cleanup converges.

Small concurrent attach/delete:

1. Create three 1Gi PVCs and three pods in a disposable namespace.
2. Verify all pods read back their own probe files.
3. Confirm three `nvmet.csi.whiskey.works` `VolumeAttachment` objects exist.
4. Delete the namespace as a unit.
5. Confirm all three PVs and all three `VolumeAttachment` objects disappear.

As of 2026-05-10, all three smokes passed in the metal cluster on image
`dev-20260510-012501-3ea331d`.

## Stop Conditions

Stop after the first failure and preserve logs if any of these occur:

- preflight cannot confirm the ZFS pool or nvmet configfs;
- the dry-run plan wants to repair or delete an unexpected object;
- the controller repeatedly alternates create/delete/repair for the same PVC;
- delete plans conflict on owner metadata, dataset, device path, NQN, or port;
- any RDS-backed workload changes state during the ZoL-only test.

Default remediation is Kubernetes-level only: clear stuck test
`VolumeAttachment` objects and respin the disposable pod. Do not manually edit
node mounts or RouterOS state without explicit approval.
