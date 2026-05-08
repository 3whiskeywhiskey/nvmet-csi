# Metal Cluster: csi-attacher Deployment Fix

## What Was Fixed

Added the `csi-attacher` sidecar to the controller deployment in `deploy/kubernetes/controller.yaml`.

**Why it was needed:**
- Required for `ControllerPublishVolume`/`ControllerUnpublishVolume` operations
- Essential for RWX block volume support (v0.5.0 feature)
- Manages Kubernetes VolumeAttachment objects
- Enables KubeVirt VM live migration with block devices

**What was missing:**
Without the csi-attacher, block volumes fail to attach to nodes with:
```
MapVolume.MapBlockVolume failed for volume "pvc-xxx":
mount failed: exit status 32 - wrong fs type, bad superblock
```

## Deployment Steps

### 1. Apply Updated Controller Manifest

```bash
# From the nvmet-csi repository
kubectl apply -f deploy/kubernetes/controller.yaml
```

This will:
- Restart the controller deployment with new sidecar
- Preserve existing PVCs and VolumeAttachments
- No downtime for existing mounted volumes

### 2. Verify csi-attacher is Running

```bash
# Check controller pod has 5 containers (was 4)
kubectl get pod -n nvmet-csi -l app=nvmet-csi-controller

# Verify csi-attacher container is present
kubectl describe pod -n nvmet-csi -l app=nvmet-csi-controller | grep -A5 "csi-attacher:"

# Check attacher logs
kubectl logs -n nvmet-csi deployment/nvmet-csi-controller -c csi-attacher --tail=20
```

Expected output:
```
I0203 ... "Attacher started"
I0203 ... "Starting leader election"
I0203 ... "Became leader, starting"
```

### 3. Clean Up Test Resources

```bash
# Delete the stuck test VM
kubectl delete vm test-migration-vm -n default

# Wait for VM deletion to complete
kubectl wait --for=delete vm/test-migration-vm -n default --timeout=60s

# Delete the test PVC (should unbind and delete volume from RDS)
kubectl delete pvc test-migration-pvc -n default
```

### 4. Verify Cleanup on RDS

SSH to RDS and confirm the test volume is removed:

```bash
ssh metal-csi@10.42.241.3
/disk print detail where slot~"test-migration"
```

Should show no results if cleanup succeeded.

## Testing RWX Block Volumes

### Create Test PVC

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: kubevirt-migration-test
  namespace: default
spec:
  accessModes:
    - ReadWriteMany  # RWX for multi-node access during migration
  volumeMode: Block   # MUST be Block for RWX
  resources:
    requests:
      storage: 10Gi
  storageClassName: rds-nvme
```

Apply and verify:
```bash
kubectl apply -f test-rwx-pvc.yaml
kubectl get pvc kubevirt-migration-test -w
# Wait for Bound status
```

### Create Test VM

```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: migration-test-vm
  namespace: default
spec:
  running: true
  template:
    metadata:
      labels:
        kubevirt.io/vm: migration-test-vm
    spec:
      domain:
        devices:
          disks:
            - disk:
                bus: virtio
              name: datadisk
        resources:
          requests:
            memory: 1Gi
      volumes:
        - name: datadisk
          persistentVolumeClaim:
            claimName: kubevirt-migration-test
```

Apply and verify:
```bash
kubectl apply -f test-vm.yaml
kubectl get vm migration-test-vm -w
# Should reach Running state
```

### Verify Volume Attachment

```bash
# Check VolumeAttachment was created
kubectl get volumeattachment | grep kubevirt-migration-test

# Should show attachment with nvmet.csi.whiskey.works as the attacher
# Status should be "true" (attached)

# Check controller logs for ControllerPublishVolume
kubectl logs -n nvmet-csi deployment/nvmet-csi-controller -c nvmet-csi-plugin | grep ControllerPublish
```

Expected log output:
```
I0203 ... "ControllerPublishVolume called" volumeID="pvc-xxx" nodeID="worker-node"
I0203 ... "Volume published successfully" volumeID="pvc-xxx"
```

## Troubleshooting

### Attacher Not Starting

If the attacher container fails to start:

```bash
# Check events
kubectl get events -n nvmet-csi --sort-by='.lastTimestamp' | grep csi-attacher

# Check pod describe
kubectl describe pod -n nvmet-csi -l app=nvmet-csi-controller
```

Common issues:
- Image pull failure: Check network/registry access
- RBAC missing: Verify `kubectl get clusterrole nvmet-csi-controller-role` includes VolumeAttachment permissions

### VolumeAttachment Stuck

If VolumeAttachment stays in "false" status:

```bash
# Check VolumeAttachment details
kubectl describe volumeattachment <name>

# Check controller logs for errors
kubectl logs -n nvmet-csi deployment/nvmet-csi-controller -c nvmet-csi-plugin --tail=100 | grep -i error
```

### Block Device Not Appearing in VM

If VM starts but disk not accessible:

```bash
# Exec into virt-launcher pod
kubectl exec -it virt-launcher-migration-test-vm-xxx -n default -- bash

# Check block device
ls -la /dev/ | grep nvme

# Check VolumeAttachment status
kubectl get volumeattachment -o yaml | grep -A20 "pvc-xxx"
```

## Migration Testing

Once VM is running with RWX block volume:

```bash
# Trigger live migration
virtctl migrate migration-test-vm -n default

# Watch migration progress
kubectl get virtualmachineinstancemigration -w

# Should complete successfully with the RWX volume accessible on both nodes during migration
```

## Rollback (If Needed)

If issues occur, rollback by removing the csi-attacher:

```bash
# Checkout previous version
git checkout HEAD~1 deploy/kubernetes/controller.yaml

# Apply old manifest
kubectl apply -f deploy/kubernetes/controller.yaml

# Note: This will prevent RWX block volumes from working
```

## Next Steps

1. ✅ Apply updated controller manifest
2. ✅ Verify csi-attacher is running
3. ✅ Clean up stuck test resources
4. ✅ Test new VM with RWX block volume
5. ✅ Verify live migration works
6. Consider updating to v0.5.1+ for better error messages

## Reference

- **Commit:** 0bccf4d "fix: add csi-attacher sidecar to controller deployment"
- **Image:** registry.k8s.io/sig-storage/csi-attacher:v4.5.0
- **Required for:** v0.5.0+ (KubeVirt Live Migration milestone)
