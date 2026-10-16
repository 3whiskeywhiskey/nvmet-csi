# Progressive Hardware Validation Runbook

**Purpose:** Systematically validate nvmet-csi driver functionality on metal cluster, building from basic operations through complex scenarios to KubeVirt live migration.

**Cluster:** metal (production)
**Driver Version:** v0.6.0 (with Phase 14 safety features)
**Date:** 2026-02-03

---

## Pre-Validation Checklist

Before starting validation, ensure the environment is ready:

- [ ] Latest driver image built and pushed (with Phase 14 features)
- [ ] ConfigMap `nvmet-csi-config` contains `nqn-prefix` key
- [ ] All nodes healthy and ready
- [ ] RDS accessible at 192.0.2.10:22
- [ ] No existing test PVCs from previous runs

```bash
# Verify cluster state
kubectl get nodes
kubectl -n nvmet-csi get pods -o wide
kubectl -n nvmet-csi get configmap nvmet-csi-config -o yaml | grep nqn-prefix

# Clean up any previous test resources
kubectl delete pvc -l test=nvmet-csi-validation --all-namespaces
kubectl delete pod -l test=nvmet-csi-validation --all-namespaces
kubectl delete vm -l test=nvmet-csi-validation --all-namespaces
```

---

## VAL-01: Basic Volume Operations

**Goal:** Verify volume create/delete via SSH control plane

### Test 1.1: Create and Delete Filesystem PVC

```bash
# Create test PVC
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-basic-fs
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: nvmet-csi
EOF

# Wait for PVC to bind
kubectl get pvc test-basic-fs -w
# Expected: STATUS should transition to "Bound"
```

**Verify on RDS:**

```bash
# SSH to RDS and check volume exists
ssh metal-csi@192.0.2.10 '/disk print detail where slot~"pvc-"'

# Expected output should show:
# - slot: pvc-<uuid>
# - file-path: /storage-pool/metal-csi/pvc-<uuid>.img
# - file-size: ~1Gi
# - nvme-tcp-export: yes
# - nvme-tcp-server-nqn: nqn.2000-02.com.mikrotik:pvc-<uuid>
```

**Verify PV created:**

```bash
kubectl get pv
# Expected: PV with matching volumeHandle to PVC's volume name
```

**Delete PVC:**

```bash
kubectl delete pvc test-basic-fs

# Verify PV also deleted
kubectl get pv
# Expected: PV should be gone (reclaim policy: Delete)
```

**Verify volume deleted from RDS:**

```bash
ssh metal-csi@192.0.2.10 '/disk print detail where slot~"pvc-"'
# Expected: Volume should be removed
```

**✓ Success Criteria:**
- PVC binds successfully
- Volume appears on RDS with correct NQN
- PV created with correct volumeHandle
- Deletion removes both PV and RDS volume

---

## VAL-02: Filesystem Volume Lifecycle

**Goal:** Verify full node staging, publishing, data persistence

### Test 2.1: Stage, Publish, Write, Unpublish, Unstage

```bash
# Create PVC with WaitForFirstConsumer
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-fs-lifecycle
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 2Gi
  storageClassName: nvmet-csi
EOF

# Verify PVC is Pending (WaitForFirstConsumer)
kubectl get pvc test-fs-lifecycle
# Expected: STATUS="Pending"
```

**Create test pod:**

```bash
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: test-fs-writer
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  containers:
  - name: writer
    image: busybox
    command:
      - sleep
      - "3600"
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-fs-lifecycle
EOF

# Wait for pod to be Running
kubectl wait --for=condition=Ready pod/test-fs-writer --timeout=120s

# Verify PVC now Bound
kubectl get pvc test-fs-lifecycle
# Expected: STATUS="Bound"
```

**Verify staging on node:**

```bash
# Identify node where pod is running
NODE=$(kubectl get pod test-fs-writer -o jsonpath='{.spec.nodeName}')
echo "Pod running on: $NODE"

# Check node plugin logs for staging
kubectl -n nvmet-csi logs -l app=nvmet-csi-node --tail=50 | grep -i "stage\|mount"
# Expected: NodeStageVolume succeeded, NVMe connected, filesystem formatted/mounted
```

**Write test data:**

```bash
# Write data inside pod
kubectl exec test-fs-writer -- sh -c 'echo "validation-data-$(date +%s)" > /data/testfile'
kubectl exec test-fs-writer -- cat /data/testfile

# Create checksum for later verification
CHECKSUM=$(kubectl exec test-fs-writer -- md5sum /data/testfile | awk '{print $1}')
echo "Checksum: $CHECKSUM"
```

**Delete pod (keep PVC):**

```bash
kubectl delete pod test-fs-writer
# Expected: Pod deleted, volume unpublished and unstaged
```

**Verify unstaging:**

```bash
# Check node logs for unstage
kubectl -n nvmet-csi logs -l app=nvmet-csi-node --tail=50 | grep -i "unstage\|disconnect"
# Expected: NodeUnstageVolume succeeded, NVMe disconnected
```

### Test 2.2: Data Persistence After Remount

**Recreate pod with same PVC:**

```bash
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: test-fs-reader
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  containers:
  - name: reader
    image: busybox
    command:
      - sleep
      - "3600"
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-fs-lifecycle
EOF

kubectl wait --for=condition=Ready pod/test-fs-reader --timeout=120s
```

**Verify data persisted:**

```bash
# Read data back
kubectl exec test-fs-reader -- cat /data/testfile

# Verify checksum matches
NEW_CHECKSUM=$(kubectl exec test-fs-reader -- md5sum /data/testfile | awk '{print $1}')
if [ "$CHECKSUM" = "$NEW_CHECKSUM" ]; then
  echo "✓ Data integrity verified (checksum matches)"
else
  echo "✗ FAIL: Checksum mismatch! Expected: $CHECKSUM, Got: $NEW_CHECKSUM"
fi
```

**Cleanup:**

```bash
kubectl delete pod test-fs-reader
kubectl delete pvc test-fs-lifecycle
```

**✓ Success Criteria:**
- PVC stays Pending until pod scheduled (WaitForFirstConsumer)
- Pod starts successfully
- NodeStageVolume connects NVMe and formats filesystem
- NodePublishVolume mounts to pod path
- Data written successfully
- NodeUnpublishVolume and NodeUnstageVolume on pod deletion
- Data persists after remount (checksum matches)

---

## VAL-03: Block Volume Lifecycle

**Goal:** Verify block volume operations (no filesystem)

### Test 3.1: Block Device Access

```bash
# Create block-mode PVC
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-block-lifecycle
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Block
  resources:
    requests:
      storage: 1Gi
  storageClassName: nvmet-csi
EOF
```

**Create pod with block device:**

```bash
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: test-block-writer
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  containers:
  - name: writer
    image: busybox
    command:
      - sleep
      - "3600"
    volumeDevices:
    - name: data
      devicePath: /dev/xvda
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-block-lifecycle
EOF

kubectl wait --for=condition=Ready pod/test-block-writer --timeout=120s
```

**Verify block device visible:**

```bash
# Check device exists
kubectl exec test-block-writer -- ls -l /dev/xvda
# Expected: block device file exists

# Check device is actually a block device
kubectl exec test-block-writer -- sh -c '[ -b /dev/xvda ] && echo "Block device confirmed" || echo "ERROR: Not a block device"'
```

**Write to block device:**

```bash
# Write pattern to block device
kubectl exec test-block-writer -- dd if=/dev/zero of=/dev/xvda bs=1M count=10
# Expected: 10+0 records in/out

# Write a specific pattern for verification
kubectl exec test-block-writer -- sh -c 'echo "BLOCK_TEST_DATA" | dd of=/dev/xvda bs=1 count=15'
```

**Read from block device:**

```bash
# Read back data
kubectl exec test-block-writer -- dd if=/dev/xvda bs=1 count=15 2>/dev/null
# Expected: BLOCK_TEST_DATA
```

**Verify staging metadata:**

```bash
# Check node logs - should NOT format filesystem for block volumes
kubectl -n nvmet-csi logs -l app=nvmet-csi-node --tail=50 | grep -i "block\|stage"
# Expected: "Block volume detected, skipping filesystem format"
```

**Cleanup:**

```bash
kubectl delete pod test-block-writer
kubectl delete pvc test-block-lifecycle
```

**✓ Success Criteria:**
- Block PVC binds successfully
- Pod mounts block device at specified devicePath
- Block device is writable and readable
- No filesystem formatting occurs (block mode)
- Unstaging cleans up block device properly

---

## VAL-04: Error Resilience

**Goal:** Verify Phase 14 safety features (health checks, circuit breaker, graceful shutdown)

### Test 4.1: Filesystem Health Check

```bash
# Create PVC and pod
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-health-check
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: nvmet-csi
---
apiVersion: v1
kind: Pod
metadata:
  name: test-health-pod
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  containers:
  - name: app
    image: busybox
    command: ["sleep", "3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-health-check
EOF

kubectl wait --for=condition=Ready pod/test-health-pod --timeout=120s
```

**Verify health check ran:**

```bash
# Check logs for health check
kubectl -n nvmet-csi logs -l app=nvmet-csi-node --tail=100 | grep -i "health\|fsck"
# Expected: "Running filesystem health check" or "Filesystem health check passed"
```

**Cleanup:**

```bash
kubectl delete pod test-health-pod
kubectl delete pvc test-health-check
```

### Test 4.2: Circuit Breaker (Repeated Failures)

**Note:** This test requires simulating failures. For now, verify circuit breaker is initialized:

```bash
# Check node logs for circuit breaker initialization
kubectl -n nvmet-csi logs -l app=nvmet-csi-node --tail=200 | grep -i "circuit"
# Expected: Circuit breaker should be initialized per volume
```

### Test 4.3: Graceful Shutdown

```bash
# Verify terminationGracePeriodSeconds in DaemonSet
kubectl -n nvmet-csi get daemonset nvmet-csi-node -o jsonpath='{.spec.template.spec.terminationGracePeriodSeconds}'
# Expected: 60
```

**Trigger graceful shutdown:**

```bash
# Select a node to test (pick one without active volumes)
TEST_NODE=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')

# Delete node pod to trigger graceful shutdown
NODE_POD=$(kubectl -n nvmet-csi get pod -l app=nvmet-csi-node --field-selector spec.nodeName=$TEST_NODE -o jsonpath='{.items[0].metadata.name}')
kubectl -n nvmet-csi delete pod $NODE_POD

# Watch pod termination timing
kubectl -n nvmet-csi get pod $NODE_POD -w
# Expected: Pod should terminate within 60 seconds
```

**✓ Success Criteria:**
- Health check runs before mounting existing filesystems
- Circuit breaker initialized per volume
- Graceful shutdown completes within terminationGracePeriodSeconds

---

## VAL-05: Multi-Node Operations

**Goal:** Verify RWO attachment conflict detection and migration handoff

### Test 5.1: RWO Conflict Detection

```bash
# Create RWO PVC
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-rwo-conflict
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: nvmet-csi
EOF
```

**Create pod on specific node:**

```bash
# Pick a node for initial pod
NODE_A=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: test-rwo-pod-a
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  nodeName: $NODE_A
  containers:
  - name: app
    image: busybox
    command: ["sleep", "3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-rwo-conflict
EOF

kubectl wait --for=condition=Ready pod/test-rwo-pod-a --timeout=120s
```

**Try to attach to different node (should fail):**

```bash
# Pick a different node
NODE_B=$(kubectl get nodes -o jsonpath='{.items[1].metadata.name}')

cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: test-rwo-pod-b
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  nodeName: $NODE_B
  containers:
  - name: app
    image: busybox
    command: ["sleep", "3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-rwo-conflict
EOF

# Watch pod events
kubectl describe pod test-rwo-pod-b | tail -20
# Expected: FailedAttachVolume event with "already attached to node"
```

**Verify attachment conflict in controller logs:**

```bash
kubectl -n nvmet-csi logs -l app=nvmet-csi-controller --tail=50 | grep -i "conflict\|already attached"
# Expected: "RWO volume already attached to node X, rejecting attachment to node Y"
```

**Delete first pod and verify second pod succeeds:**

```bash
kubectl delete pod test-rwo-pod-a

# Wait for attachment to clear (grace period)
sleep 10

# Check if pod-b can now attach
kubectl get pod test-rwo-pod-b -w
# Expected: Should eventually become Running as attachment clears
```

**Cleanup:**

```bash
kubectl delete pod test-rwo-pod-b
kubectl delete pvc test-rwo-conflict
```

**✓ Success Criteria:**
- RWO volume attaches successfully to first node
- Second node attachment rejected with FAILED_PRECONDITION
- After first pod deleted, second pod can attach (grace period respected)

---

## VAL-06: KubeVirt VM Boot (Block Volume)

**Goal:** Verify KubeVirt VM can boot with RDS block volume

### Test 6.1: Simple VM with Block Disk

```bash
# Create RWX block PVC for VM (needed for live migration later)
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-vm-disk
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  accessModes:
    - ReadWriteMany
  volumeMode: Block
  resources:
    requests:
      storage: 5Gi
  storageClassName: nvmet-csi
EOF

kubectl get pvc test-vm-disk -w
# Expected: STATUS="Bound"
```

**Create KubeVirt VM:**

```bash
cat <<EOF | kubectl apply -f -
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: test-validation-vm
  namespace: default
  labels:
    test: nvmet-csi-validation
spec:
  running: true
  template:
    metadata:
      labels:
        kubevirt.io/vm: test-validation-vm
    spec:
      domain:
        devices:
          disks:
            - name: rootdisk
              disk:
                bus: virtio
            - name: datadisk
              disk:
                bus: virtio
          interfaces:
            - name: default
              masquerade: {}
        resources:
          requests:
            memory: 1Gi
            cpu: 1
      networks:
        - name: default
          pod: {}
      volumes:
        - name: rootdisk
          containerDisk:
            image: quay.io/kubevirt/cirros-container-disk-demo:latest
        - name: datadisk
          persistentVolumeClaim:
            claimName: test-vm-disk
EOF
```

**Wait for VM to start:**

```bash
kubectl get vmi test-validation-vm -w
# Expected: PHASE transitions to "Running"

# Verify VMI is running
kubectl get vmi test-validation-vm -o wide
# Note which node it's running on
```

**Access VM console:**

```bash
# Start console (Ctrl+] to exit)
virtctl console test-validation-vm

# Login: cirros / gocubsgo
```

**Verify block device visible in VM:**

```bash
# Inside VM console
lsblk
# Expected: Should see vdb (5G) - the RDS block volume

# Check device is writable
sudo dd if=/dev/zero of=/dev/vdb bs=1M count=10
# Expected: 10+0 records in/out
```

**Create filesystem and write test data:**

```bash
# Inside VM console
sudo mkfs.ext4 /dev/vdb
sudo mkdir -p /mnt/data
sudo mount /dev/vdb /mnt/data

# Write test data with checksum
sudo dd if=/dev/urandom of=/mnt/data/testfile bs=1M count=100
sudo md5sum /mnt/data/testfile | sudo tee /mnt/data/checksum.txt

# Display checksum for later verification
cat /mnt/data/checksum.txt

# Sync to ensure data written
sync

# Exit console (Ctrl+])
```

**✓ Success Criteria:**
- RWX block PVC binds successfully
- KubeVirt VM boots and reaches Running phase
- Block volume visible as /dev/vdb in VM
- Filesystem can be created and mounted
- Data can be written to volume

---

## VAL-07: KubeVirt Live Migration

**Goal:** Verify live migration works end-to-end with data integrity

**Prerequisites:** VM from VAL-06 must be running

### Test 7.1: Execute Live Migration

```bash
# Record current node
CURRENT_NODE=$(kubectl get vmi test-validation-vm -o jsonpath='{.status.nodeName}')
echo "VM currently on: $CURRENT_NODE"
```

**Initiate migration:**

```bash
cat <<EOF | kubectl apply -f -
apiVersion: kubevirt.io/v1
kind: VirtualMachineInstanceMigration
metadata:
  name: test-validation-migration
  namespace: default
spec:
  vmiName: test-validation-vm
EOF
```

**Watch migration progress:**

```bash
kubectl get vmim test-validation-migration -w
# Expected: PHASE transitions "Scheduling" -> "TargetReady" -> "Running" -> "Succeeded"

# Monitor VMI status
kubectl get vmi test-validation-vm -w
# Expected: STATUS shows "Migrating" then "Running" on new node
```

**Verify VM migrated to different node:**

```bash
NEW_NODE=$(kubectl get vmi test-validation-vm -o jsonpath='{.status.nodeName}')
echo "VM now on: $NEW_NODE"

if [ "$CURRENT_NODE" != "$NEW_NODE" ]; then
  echo "✓ Migration successful - VM moved from $CURRENT_NODE to $NEW_NODE"
else
  echo "✗ FAIL: VM still on same node $CURRENT_NODE"
fi
```

### Test 7.2: Verify Data Integrity After Migration

**Access VM console on new node:**

```bash
virtctl console test-validation-vm
# Login: cirros / gocubsgo
```

**Verify data persisted:**

```bash
# Inside VM console
sudo mount /dev/vdb /mnt/data

# Verify checksum matches
md5sum /mnt/data/testfile
cat /mnt/data/checksum.txt

# Compare checksums manually
# Expected: Checksums should match exactly
```

**If checksums match:**

```bash
echo "✓ Data integrity verified after migration"
```

**Exit console** (Ctrl+])

### Test 7.3: Verify Migration Metrics

```bash
# Port-forward to controller metrics endpoint
kubectl -n nvmet-csi port-forward deployment/nvmet-csi-controller 9809:9809 &
PF_PID=$!
sleep 2

# Fetch migration metrics
curl -s http://localhost:9809/metrics | grep -E 'csi_migrations_total|csi_migration_duration'

# Expected output:
# csi_migrations_total{...} 1
# csi_migration_duration_seconds_bucket{...}
# csi_migration_duration_seconds_sum{...}
# csi_migration_duration_seconds_count{...}

# Cleanup port-forward
kill $PF_PID
```

**Cleanup test resources:**

```bash
kubectl delete vm test-validation-vm
kubectl delete pvc test-vm-disk
kubectl delete vmim test-validation-migration
```

**✓ Success Criteria:**
- Migration completes successfully (VMIM reaches Succeeded)
- VM moves to different node
- VM remains accessible after migration
- Data integrity verified (checksums match)
- Migration metrics emitted (migrations_total incremented, duration recorded)

---

## Post-Validation Checklist

After completing all validation tests:

- [ ] All test PVCs deleted
- [ ] All test pods deleted
- [ ] All test VMs deleted
- [ ] No orphaned volumes on RDS
- [ ] No errors in driver logs
- [ ] Document any issues encountered
- [ ] Update HARDWARE_VALIDATION.md with results

```bash
# Verify cleanup
kubectl get pvc -l test=nvmet-csi-validation --all-namespaces
kubectl get pod -l test=nvmet-csi-validation --all-namespaces
kubectl get vm -l test=nvmet-csi-validation --all-namespaces

# Check RDS for orphaned volumes
ssh metal-csi@192.0.2.10 '/disk print detail where slot~"pvc-"'
# Expected: Only production volumes, no test volumes

# Review driver logs for any errors
kubectl -n nvmet-csi logs -l app=nvmet-csi-controller --tail=100
kubectl -n nvmet-csi logs -l app=nvmet-csi-node --tail=100
```

---

## Troubleshooting Guide

### PVC Stuck in Pending

**Check:**
- StorageClass exists: `kubectl get storageclass nvmet-csi`
- Controller logs: `kubectl -n nvmet-csi logs -l app=nvmet-csi-controller`
- Events: `kubectl describe pvc <name>`

**Common causes:**
- WaitForFirstConsumer (expected - create pod to trigger binding)
- RDS SSH connection failure
- Volume creation error on RDS

### Pod Stuck in ContainerCreating

**Check:**
- Node plugin logs: `kubectl -n nvmet-csi logs -l app=nvmet-csi-node`
- Pod events: `kubectl describe pod <name>`
- NVMe connection status on node

**Common causes:**
- NVMe connection failure (RDS unreachable on port 4420)
- Filesystem format failure
- Mount operation failure

### VM Fails to Start

**Check:**
- VMI status: `kubectl describe vmi <name>`
- PVC bound: `kubectl get pvc <name>`
- KubeVirt logs

**Common causes:**
- Block volume not properly staged
- Incorrect volumeMode (should be Block for VMs)
- Resource constraints

### Migration Fails

**Check:**
- Migration object: `kubectl describe vmim <name>`
- Node plugin logs on both source and target nodes
- Controller logs for attachment operations

**Common causes:**
- RWO instead of RWX (VMs need RWX for migration)
- Target node cannot attach volume
- Network connectivity issues between nodes

---

## Summary Template

After completing validation, fill out this summary:

```markdown
# Hardware Validation Summary

**Date:** YYYY-MM-DD
**Driver Version:** vX.Y.Z
**Cluster:** metal

## Test Results

| Test | Status | Duration | Notes |
|------|--------|----------|-------|
| VAL-01: Basic Volume Operations | PASS/FAIL | Xm | |
| VAL-02: Filesystem Volume Lifecycle | PASS/FAIL | Xm | |
| VAL-03: Block Volume Lifecycle | PASS/FAIL | Xm | |
| VAL-04: Error Resilience | PASS/FAIL | Xm | |
| VAL-05: Multi-Node Operations | PASS/FAIL | Xm | |
| VAL-06: KubeVirt VM Boot | PASS/FAIL | Xm | |
| VAL-07: KubeVirt Live Migration | PASS/FAIL | Xm | |

## Issues Encountered

1. Issue description
   - Resolution: ...
   - Impact: ...

## Recommendations

1. ...

## Sign-off

- [ ] All critical tests passed
- [ ] Issues documented and resolved
- [ ] v0.6.0 ready for release
```
