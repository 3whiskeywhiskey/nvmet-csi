# Hardware Validation Guide

**Version:** Written for nvmet-csi driver v0.9.0+, RouterOS 7.16+
**Last Updated:** 2026-02-18
**Purpose:** Step-by-step test procedures for validating nvmet-csi driver functionality on production hardware

## Overview

This guide provides executable test scenarios for validating the nvmet-csi driver against real MikroTik ROSE Data Server (RDS) hardware. Use this guide for:

- **Initial deployment validation** - Verify driver works with your RDS hardware
- **Post-upgrade verification** - Confirm upgrades didn't break functionality
- **Troubleshooting production issues** - Reproduce and diagnose problems
- **Hardware validation** - Test new RDS hardware before production use

Each test case includes exact commands, expected output, cleanup procedures, and troubleshooting guidance.

## Prerequisites

Before running these tests, ensure you have:

### Access Requirements
- **RDS accessible via two networks:**
  - Management IP: `10.42.241.3` (SSH port 22 for volume management)
  - Storage IP: `10.42.68.1` (NVMe/TCP port 4420 for block device access)
- **SSH access to RDS:** `ssh admin@10.42.241.3` with working credentials
- **SSH access to worker nodes:** For verifying NVMe/TCP connections
- **kubectl access:** Cluster-admin privileges to create/delete test resources

### Cluster Requirements
- Kubernetes cluster with nvmet-csi driver v0.9.0+ deployed
- At least 20GB free space on RDS for test volumes
- Worker nodes with:
  - `nvme-cli` installed (for NVMe/TCP operations)
  - Kernel 5.0+ (for NVMe/TCP support)
  - Network connectivity to RDS storage IP (10.42.68.1)

### Validation Tools
- `kubectl` command-line tool
- SSH client with key-based authentication
- Basic Unix utilities: `grep`, `awk`, `df`

## Environment Validation

Run these pre-flight checks before executing test cases to ensure your environment is ready.

### Step 1: Verify Controller Running

```bash
kubectl get pods -n kube-system -l app=nvmet-csi-controller
```

**Expected:** One pod in `Running` state

```
NAME                                  READY   STATUS    RESTARTS   AGE
nvmet-csi-controller-7d8b9f5c6d-xk2mn   3/3     Running   0          5m
```

**If not running:** Check controller logs for errors:
```bash
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin --tail=50
```

### Step 2: Verify Node Plugin Running

```bash
kubectl get pods -n kube-system -l app=nvmet-csi-node
```

**Expected:** DaemonSet pod on every worker node in `Running` state

```
NAME                 READY   STATUS    RESTARTS   AGE
nvmet-csi-node-5k7wq   3/3     Running   0          5m
nvmet-csi-node-8n2xp   3/3     Running   0          5m
```

**If not running:** Check node plugin logs:
```bash
kubectl logs -n kube-system -l app=nvmet-csi-node -c nvmet-csi-plugin --tail=50
```

### Step 3: Verify StorageClass Exists

```bash
kubectl get storageclass rds-nvme-tcp
```

**Expected:** StorageClass with correct provisioner

```
NAME            PROVISIONER           RECLAIMPOLICY   VOLUMEBINDINGMODE      ALLOWVOLUMEEXPANSION
rds-nvme-tcp    nvmet.csi.whiskey.works     Delete          WaitForFirstConsumer   true
```

### Step 4: Verify SSH Connectivity to RDS

```bash
ssh admin@10.42.241.3 '/system/resource/print'
```

**Expected:** RouterOS system information output

```
       uptime: 15d2h45m30s
      version: 7.16
 cpu-frequency: 2400MHz
   free-memory: 12.5GiB
   total-memory: 16.0GiB
```

**If connection fails:** Check network connectivity, SSH credentials, firewall rules

### Step 5: Verify RDS Storage Capacity

```bash
ssh admin@10.42.241.3 '/disk print brief'
```

**Expected:** List of disks with available slots

```
# SLOT        TYPE   FILE-SIZE    NVME-TCP-EXPORT
0 storage1   file   100.0GiB     yes
```

**If no output or error:** Verify RDS has storage pool configured

---

## Test Cases

All test resources use the label `test=hardware-validation` for easy cleanup. Test volume/pod names use the `test-hw-` prefix.

### TC-01: Basic Volume Lifecycle

**Objective:** Verify end-to-end volume provisioning, mounting, data persistence, and cleanup

**Estimated Time:** 5 minutes

**Prerequisites:**
- Environment validation complete (all pre-flight checks passed)
- At least 5GB free space on RDS

**Steps:**

#### 1. Create PVC

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-01
  labels:
    test: hardware-validation
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 5Gi
EOF
```

**Expected:** PVC created, status "Pending" (WaitForFirstConsumer binding mode)

```bash
kubectl get pvc test-hw-pvc-01
```

```
NAME             STATUS    VOLUME   CAPACITY   ACCESS MODES   STORAGECLASS   AGE
test-hw-pvc-01   Pending                                      rds-nvme-tcp   5s
```

#### 2. Create Pod Using Volume

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-01
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-01
EOF
```

**Expected:** Pod triggers volume provisioning, PVC changes to "Bound" within 30 seconds

```bash
kubectl get pvc test-hw-pvc-01 --watch
```

```
NAME             STATUS    VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE
test-hw-pvc-01   Pending                                                                        rds-nvme-tcp   10s
test-hw-pvc-01   Bound     pvc-12345678-1234-1234-1234-123456789abc   5Gi        RWO            rds-nvme-tcp   25s
```

**If stuck in Pending >60s:** Check controller logs for errors:
```bash
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin --tail=30 | grep CreateVolume
```

#### 3. Verify Volume on RDS

```bash
ssh admin@10.42.241.3 '/disk print detail where slot~"pvc-"' | head -20
```

**Expected:** Volume exists with correct size and NVMe/TCP export enabled

```
slot: pvc-12345678-1234-1234-1234-123456789abc
type: file
file-path: /storage-pool/kubernetes-volumes/pvc-12345678-1234-1234-1234-123456789abc.img
file-size: 5.0GiB
nvme-tcp-export: yes
nvme-tcp-server-port: 4420
nvme-tcp-server-nqn: nqn.2000-02.com.mikrotik:pvc-12345678-1234-1234-1234-123456789abc
```

#### 4. Wait for Pod Running

```bash
kubectl wait --for=condition=Ready pod/test-hw-pod-01 --timeout=60s
```

**Expected:** Pod reaches "Running" state within 60 seconds

```bash
kubectl get pod test-hw-pod-01
```

```
NAME             READY   STATUS    RESTARTS   AGE
test-hw-pod-01   1/1     Running   0          45s
```

**If stuck in ContainerCreating:** Check node plugin logs:
```bash
kubectl logs -n kube-system -l app=nvmet-csi-node -c nvmet-csi-plugin --tail=30 | grep NodeStage
```

#### 5. Verify Volume Mounted in Pod

```bash
kubectl exec test-hw-pod-01 -- df -h /data
```

**Expected:** Filesystem shows ~5GB capacity (4.9GB due to filesystem overhead)

```
Filesystem      Size  Used Avail Use% Mounted on
/dev/nvme1n1    4.9G   24M  4.6G   1% /data
```

#### 6. Write Test Data

```bash
kubectl exec test-hw-pod-01 -- sh -c 'echo "hardware-validation-test-$(date +%s)" > /data/validation.txt'
kubectl exec test-hw-pod-01 -- cat /data/validation.txt
```

**Expected:** Data written and readable, output shows timestamp

```
hardware-validation-test-1738814800
```

#### 7. Verify Data Persistence

```bash
kubectl exec test-hw-pod-01 -- sh -c 'cat /data/validation.txt'
```

**Expected:** Same content as step 6 (data persisted)

**Cleanup:**

```bash
# Delete pod first
kubectl delete pod test-hw-pod-01

# Wait for pod deletion
kubectl wait --for=delete pod/test-hw-pod-01 --timeout=30s

# Delete PVC
kubectl delete pvc test-hw-pvc-01

# Wait 30 seconds for controller to clean up volume on RDS
sleep 30

# Verify volume deleted on RDS
ssh admin@10.42.241.3 '/disk print detail where slot~"pvc-12345678"'
```

**Expected:** No output (volume deleted)

**Success Criteria:**
- ✅ PVC bound within 30 seconds
- ✅ Volume visible on RDS with correct size and NVMe/TCP export
- ✅ Pod running and volume mounted at `/data`
- ✅ Data writable and readable
- ✅ Volume deleted from RDS after PVC deletion

**Troubleshooting:**
- **PVC stuck Pending:** Check controller logs, verify SSH connectivity to RDS, verify RDS has free space
- **Pod stuck ContainerCreating:** Check node logs, verify NVMe/TCP connectivity (port 4420), check node network connectivity
- **Mount fails:** SSH to worker node, check `dmesg | grep nvme` for NVMe errors, check `mount` for mount errors
- **Data not writable:** Check filesystem mount options (ro/rw), check PVC access mode

---

### TC-02: NVMe/TCP Connection Validation

**Objective:** Verify NVMe/TCP connection parameters, device naming, and transport configuration

**Estimated Time:** 5 minutes

**Prerequisites:**
- TC-01 environment validation complete
- Worker node SSH access

**Steps:**

#### 1. Create PVC and Pod

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-02
  labels:
    test: hardware-validation
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-02
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-02
EOF
```

#### 2. Wait for Pod Running

```bash
kubectl wait --for=condition=Ready pod/test-hw-pod-02 --timeout=60s
```

#### 3. Get Pod Node Assignment

```bash
NODE_NAME=$(kubectl get pod test-hw-pod-02 -o jsonpath='{.spec.nodeName}')
echo "Pod scheduled on node: $NODE_NAME"
```

#### 4. SSH to Worker Node and Verify NVMe Device

```bash
# Replace <node-ip> with actual worker node IP
ssh <node-ip> 'nvme list'
```

**Expected:** NVMe device connected via TCP transport

```
Node             SN                   Model                                    Namespace Usage                      Format           FW Rev
---------------- -------------------- ---------------------------------------- --------- -------------------------- ---------------- --------
/dev/nvme1n1     MikroTik-pvc-12345   MikroTik ROSE Data Server                1           5.37  GB /   5.37  GB    512   B +  0 B   1.0
```

#### 5. Verify NVMe Subsystem

```bash
ssh <node-ip> 'nvme list-subsys'
```

**Expected:** Subsystem with correct NQN format

```
nvme-subsys1 - NQN=nqn.2000-02.com.mikrotik:pvc-12345678-1234-1234-1234-123456789abc
\
 +- nvme1 tcp traddr=10.42.68.1 trsvcid=4420 live
```

#### 6. Verify Transport Type

```bash
ssh <node-ip> 'cat /sys/class/nvme/nvme1/transport'
```

**Expected:** Output shows `tcp`

```
tcp
```

#### 7. Verify Block Device Path

```bash
ssh <node-ip> 'lsblk | grep nvme'
```

**Expected:** Block device with mounted filesystem

```
nvme1n1                    259:1    0   5G  0 disk /var/lib/kubelet/plugins/kubernetes.io/csi/...
```

#### 8. Check NVMe Connection Parameters

```bash
ssh <node-ip> 'cat /sys/class/nvme/nvme1/ctrl_loss_tmo'
```

**Expected:** Controller loss timeout value (typically 600 seconds or -1 for infinite)

```
600
```

**Cleanup:**

```bash
kubectl delete pod test-hw-pod-02
kubectl delete pvc test-hw-pvc-02
```

**Success Criteria:**
- ✅ NVMe device visible with `nvme list`
- ✅ NQN matches expected format: `nqn.2000-02.com.mikrotik:pvc-<uuid>`
- ✅ Transport type is `tcp`
- ✅ Block device path exists under `/dev/nvme*`
- ✅ Connection parameters configured correctly

**Troubleshooting:**
- **No NVMe device found:** Check `dmesg | grep nvme` for connection errors, verify network connectivity to 10.42.68.1:4420
- **Wrong transport type:** Verify nvme-cli version supports TCP transport, check kernel version (5.0+ required)
- **Connection timeouts:** Check ctrl_loss_tmo, verify RDS is responding on port 4420, check network latency

---

### TC-03: Volume Expansion

**Objective:** Verify volume expansion from 5Gi to 10Gi

**Estimated Time:** 5 minutes

**Prerequisites:**
- StorageClass has `allowVolumeExpansion: true`
- At least 10GB free space on RDS

**Important Note:** Volume expansion in nvmet-csi requires a pod restart for the filesystem resize to take effect. The controller expands the file on RDS immediately, but the kubelet only calls `NodeExpandVolume` during pod startup. This is normal behavior for CSI drivers that report `NodeExpansionRequired: true`.

**Steps:**

#### 1. Create PVC and Pod

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-03
  labels:
    test: hardware-validation
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-03
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-03
EOF
```

#### 2. Wait for Pod Running and Verify Initial Size

```bash
kubectl wait --for=condition=Ready pod/test-hw-pod-03 --timeout=60s
kubectl exec test-hw-pod-03 -- df -h /data
```

**Expected:** ~5GB capacity

```
Filesystem      Size  Used Avail Use% Mounted on
/dev/nvme1n1    4.9G   24M  4.6G   1% /data
```

#### 3. Expand Volume to 10Gi

```bash
kubectl patch pvc test-hw-pvc-03 -p '{"spec":{"resources":{"requests":{"storage":"10Gi"}}}}'
```

**Expected:** PVC patched successfully

```
persistentvolumeclaim/test-hw-pvc-03 patched
```

#### 4. Monitor Expansion Progress

```bash
kubectl describe pvc test-hw-pvc-03 | grep -A 5 Events
```

**Expected:** Events showing expansion in progress

```
Events:
  Type    Reason                      Age   From                         Message
  ----    ------                      ----  ----                         -------
  Normal  Resizing                    15s   external-resizer             External resizer is resizing volume
  Normal  FileSystemResizeRequired    10s   external-resizer             Require file system resize of volume on node
```

**Note:** The PV capacity will show 10Gi, but the filesystem inside the pod will still show ~5GB at this point.

#### 5. Restart Pod to Complete Filesystem Resize

**IMPORTANT:** The kubelet only calls `NodeExpandVolume` during pod startup. A pod restart is required for the filesystem resize to take effect.

```bash
# Delete the pod (keep the PVC)
kubectl delete pod test-hw-pod-03

# Recreate the pod with the same PVC
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-03
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-03
EOF

# Wait for pod to be ready
kubectl wait --for=condition=Ready pod/test-hw-pod-03 --timeout=60s
```

#### 6. Verify New Size in Pod

```bash
kubectl exec test-hw-pod-03 -- df -h /data
```

**Expected:** ~10GB capacity (9.8GB due to ext4 filesystem overhead)

```
Filesystem      Size  Used Avail Use% Mounted on
/dev/nvme1n1    9.8G   24M  9.5G   1% /data
```

#### 7. Verify Size on RDS

```bash
# Get volume ID from PV
VOLUME_ID=$(kubectl get pvc test-hw-pvc-03 -o jsonpath='{.spec.volumeName}' | sed 's/pv-/pvc-/')
ssh admin@10.42.241.3 "/disk print detail where slot=\"$VOLUME_ID\""
```

**Expected:** File size shows 10GiB

```
file-size: 10.0GiB
```

**Cleanup:**

```bash
kubectl delete pod test-hw-pod-03
kubectl delete pvc test-hw-pvc-03
```

**Success Criteria:**
- ✅ PVC patched successfully
- ✅ Controller expansion events show "Resizing" and "FileSystemResizeRequired"
- ✅ PV capacity shows 10Gi
- ✅ After pod restart, filesystem size reflects new capacity (~9.8GB for 10Gi)
- ✅ RDS volume file size updated to 10GiB

**Troubleshooting:**
- **Expansion stuck:** Check PVC events, check controller logs for ControllerExpandVolume errors, verify RDS has free space
- **Filesystem shows old size after expansion:** This is expected - pod restart is required for filesystem resize to take effect
- **Filesystem still not resized after pod restart:** Check node logs for NodeExpandVolume errors, verify device path is correct
- **Expansion fails on RDS:** Verify RDS has enough free space, check SSH connectivity, verify `/disk set file-size=...` command support on RouterOS 7.x

---

### TC-04: Block Volume for KubeVirt (Optional)

**Objective:** Verify block volume mode for KubeVirt VM use cases

**Estimated Time:** 10 minutes

**Prerequisites:**
- KubeVirt installed on cluster (optional - skip test if not installed)
- At least 10GB free space on RDS

**Note:** This test is OPTIONAL. If KubeVirt is not installed, skip to TC-05.

**Steps:**

#### 1. Check KubeVirt Installation

```bash
kubectl get pods -n kubevirt -l kubevirt.io=virt-operator
```

**Expected:** KubeVirt operator pods running

**If no output:** KubeVirt not installed, skip this test case

#### 2. Create Block Mode PVC

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-04-block
  labels:
    test: hardware-validation
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme-tcp
  volumeMode: Block
  resources:
    requests:
      storage: 10Gi
EOF
```

#### 3. Create Pod with Block Device

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-04-block
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: ubuntu:22.04
    command: ["sleep", "3600"]
    volumeDevices:
    - name: data
      devicePath: /dev/xvda
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-04-block
EOF
```

#### 4. Wait for Pod Running

```bash
kubectl wait --for=condition=Ready pod/test-hw-pod-04-block --timeout=60s
```

#### 5. Verify Block Device in Pod

```bash
kubectl exec test-hw-pod-04-block -- ls -la /dev/xvda
```

**Expected:** Block device exists

```
brw-rw---- 1 root disk 259, 1 Feb  6 04:30 /dev/xvda
```

#### 6. Write Data to Block Device

```bash
kubectl exec test-hw-pod-04-block -- dd if=/dev/zero of=/dev/xvda bs=1M count=10
```

**Expected:** 10MB written successfully

```
10+0 records in
10+0 records out
10485760 bytes (10 MB, 10 MiB) copied, 0.0523 s, 200 MB/s
```

#### 7. Read Data from Block Device

```bash
kubectl exec test-hw-pod-04-block -- dd if=/dev/xvda of=/dev/null bs=1M count=10
```

**Expected:** 10MB read successfully

```
10+0 records in
10+0 records out
10485760 bytes (10 MB, 10 MiB) copied, 0.0134 s, 782 MB/s
```

**Cleanup:**

```bash
kubectl delete pod test-hw-pod-04-block
kubectl delete pvc test-hw-pvc-04-block
```

**Success Criteria:**
- ✅ Block mode PVC created and bound
- ✅ Block device accessible at devicePath in pod
- ✅ Data writable and readable via dd

**Troubleshooting:**
- **PVC stuck pending:** Verify volumeMode: Block is supported, check controller logs
- **Device not found in pod:** Check node logs, verify block device attachment
- **I/O errors:** Check dmesg on node for NVMe errors, verify RDS health

---

### TC-05: Failure Recovery - Pod Deletion and Reattachment

**Objective:** Verify data persistence when pod is deleted and recreated using same PVC

**Estimated Time:** 5 minutes

**Prerequisites:**
- TC-01 environment validation complete

**Steps:**

#### 1. Create PVC and Pod

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-05
  labels:
    test: hardware-validation
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-05-v1
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-05
EOF
```

#### 2. Wait for Pod Running

```bash
kubectl wait --for=condition=Ready pod/test-hw-pod-05-v1 --timeout=60s
```

#### 3. Write Unique Test Data

```bash
TIMESTAMP=$(date +%s)
kubectl exec test-hw-pod-05-v1 -- sh -c "echo 'persistence-test-$TIMESTAMP' > /data/persistence.txt"
kubectl exec test-hw-pod-05-v1 -- cat /data/persistence.txt
```

**Expected:** Output shows unique timestamp

```
persistence-test-1738814800
```

#### 4. Delete Pod (NOT PVC)

```bash
kubectl delete pod test-hw-pod-05-v1
kubectl wait --for=delete pod/test-hw-pod-05-v1 --timeout=30s
```

**Expected:** Pod deleted, PVC remains "Bound"

```bash
kubectl get pvc test-hw-pvc-05
```

```
NAME             STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE
test-hw-pvc-05   Bound    pvc-12345678-1234-1234-1234-123456789abc   5Gi        RWO            rds-nvme-tcp   2m
```

#### 5. Recreate Pod with Same PVC

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-05-v2
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-05
EOF
```

#### 6. Wait for New Pod Running

```bash
kubectl wait --for=condition=Ready pod/test-hw-pod-05-v2 --timeout=60s
```

#### 7. Verify Data Persisted

```bash
kubectl exec test-hw-pod-05-v2 -- cat /data/persistence.txt
```

**Expected:** Same content as step 3 (data survived pod deletion)

```
persistence-test-1738814800
```

**Cleanup:**

```bash
kubectl delete pod test-hw-pod-05-v2
kubectl delete pvc test-hw-pvc-05
```

**Success Criteria:**
- ✅ Data written in first pod instance
- ✅ PVC remains Bound after pod deletion
- ✅ Second pod attaches to same volume
- ✅ Data readable in second pod (persistence verified)

**Troubleshooting:**
- **Data lost:** Check if PVC was accidentally deleted, verify NVMe disconnect/reconnect sequence
- **Second pod stuck:** Check node logs for attachment errors, verify volume not stuck in use

---

### TC-06: Failure Recovery - RDS Connection Resilience

**Objective:** Verify connection manager behavior and controller resilience

**Estimated Time:** 10 minutes

**Prerequisites:**
- Access to view controller logs
- Understanding that this test does NOT restart RDS (validates monitoring only)

**CAUTION:** This test does NOT restart RDS hardware. It validates connection manager monitoring and health checks only.

**Steps:**

#### 1. Create Test Volume

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-06
  labels:
    test: hardware-validation
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-06
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-06
EOF
```

#### 2. Wait for Pod Running

```bash
kubectl wait --for=condition=Ready pod/test-hw-pod-06 --timeout=60s
```

#### 3. Check Connection Manager Status in Logs

```bash
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin --tail=100 | grep -i "connection\|connected"
```

**Expected:** Logs show active connection to RDS

```
INFO    Connection manager started, polling every 5s
INFO    Connected to RDS at 10.42.241.3:22
INFO    Connection healthy
```

#### 4. Verify Controller Probe Endpoint

```bash
CONTROLLER_POD=$(kubectl get pod -n kube-system -l app=nvmet-csi-controller -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n kube-system $CONTROLLER_POD -c nvmet-csi-plugin -- /bin/sh -c 'wget -q -O - http://localhost:9808/healthz'
```

**Expected:** Health check returns healthy status

```
ok
```

#### 5. Verify Exponential Backoff Configuration

```bash
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin --tail=200 | grep -i "backoff\|retry"
```

**Expected:** Logs show retry configuration (if any connection issues occurred)

```
INFO    Retry configuration: exponential backoff with jitter, max elapsed time: unlimited
```

#### 6. Document Expected Behavior

**Connection Manager Behavior:**
- Polls RDS every 5 seconds for connectivity
- Uses exponential backoff with jitter on connection failure (RandomizationFactor=0.1)
- MaxElapsedTime=0 for background reconnection (never gives up)
- Closes old SSH session before reconnecting to prevent session leaks

**If RDS becomes temporarily unreachable:**
- Controller detects disconnection within 5-10 seconds
- Enters exponential backoff retry: 1s, 2s, 4s, 8s, 16s (with 10% jitter)
- Continues retrying indefinitely until RDS returns
- Automatically reconnects when RDS is available
- Volume operations queue during disconnection, resume after reconnect

#### 7. Verify Probe Reflects Connection State

```bash
# Check probe endpoint (should be healthy since RDS is available)
kubectl exec -n kube-system $CONTROLLER_POD -c nvmet-csi-plugin -- /bin/sh -c 'wget -q -O - http://localhost:9808/healthz; echo'
```

**Expected:** Returns `ok` when connected

**Cleanup:**

```bash
kubectl delete pod test-hw-pod-06
kubectl delete pvc test-hw-pvc-06
```

**Success Criteria:**
- ✅ Connection manager logs show active monitoring
- ✅ Probe endpoint reports healthy status
- ✅ Exponential backoff configuration documented
- ✅ Expected reconnection behavior understood

**Troubleshooting:**
- **Probe unhealthy:** Check RDS SSH connectivity, verify controller can reach 10.42.241.3:22
- **No connection logs:** Increase log verbosity with `--v=5` flag in controller deployment

**Note:** To fully test RDS restart recovery, you would need to:
1. Restart RDS in a maintenance window
2. Observe controller logs during disconnection and reconnection
3. Verify volume operations resume after RDS returns
4. This is NOT performed as part of routine validation

---

### TC-07: Multi-Volume Concurrent Operations

**Objective:** Verify controller handles multiple simultaneous volume operations

**Estimated Time:** 5 minutes

**Prerequisites:**
- At least 15GB free space on RDS (3 x 5Gi volumes)

**Steps:**

#### 1. Create Three PVCs Simultaneously

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-07a
  labels:
    test: hardware-validation
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-07b
  labels:
    test: hardware-validation
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-07c
  labels:
    test: hardware-validation
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 5Gi
EOF
```

#### 2. Create Three Pods Simultaneously

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-07a
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-07a
---
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-07b
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-07b
---
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-07c
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-07c
EOF
```

#### 3. Monitor PVC Binding

```bash
kubectl get pvc test-hw-pvc-07a test-hw-pvc-07b test-hw-pvc-07c --watch
```

**Expected:** All three PVCs transition to "Bound" within 60 seconds

```
NAME             STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS   AGE
test-hw-pvc-07a  Bound    pvc-aaaa1111-1111-1111-1111-111111111111   5Gi        RWO            rds-nvme-tcp   25s
test-hw-pvc-07b  Bound    pvc-bbbb2222-2222-2222-2222-222222222222   5Gi        RWO            rds-nvme-tcp   28s
test-hw-pvc-07c  Bound    pvc-cccc3333-3333-3333-3333-333333333333   5Gi        RWO            rds-nvme-tcp   31s
```

#### 4. Wait for All Pods Running

```bash
kubectl wait --for=condition=Ready pod/test-hw-pod-07a pod/test-hw-pod-07b pod/test-hw-pod-07c --timeout=90s
```

#### 5. Write Unique Data to Each Volume

```bash
kubectl exec test-hw-pod-07a -- sh -c 'echo "volume-a-data" > /data/test.txt'
kubectl exec test-hw-pod-07b -- sh -c 'echo "volume-b-data" > /data/test.txt'
kubectl exec test-hw-pod-07c -- sh -c 'echo "volume-c-data" > /data/test.txt'
```

#### 6. Verify Data Isolation

```bash
kubectl exec test-hw-pod-07a -- cat /data/test.txt
kubectl exec test-hw-pod-07b -- cat /data/test.txt
kubectl exec test-hw-pod-07c -- cat /data/test.txt
```

**Expected:** Each pod reads its own unique data (no cross-contamination)

```
volume-a-data
volume-b-data
volume-c-data
```

#### 7. Verify All Volumes on RDS

```bash
ssh admin@10.42.241.3 '/disk print brief where slot~"pvc-"' | grep -E "test-hw|pvc-aaaa|pvc-bbbb|pvc-cccc"
```

**Expected:** Three volumes visible on RDS

**Cleanup:**

```bash
# Delete all pods
kubectl delete pod test-hw-pod-07a test-hw-pod-07b test-hw-pod-07c

# Wait for pod deletion
kubectl wait --for=delete pod/test-hw-pod-07a pod/test-hw-pod-07b pod/test-hw-pod-07c --timeout=60s

# Delete all PVCs
kubectl delete pvc test-hw-pvc-07a test-hw-pvc-07b test-hw-pvc-07c

# Wait for cleanup
sleep 30

# Verify all volumes deleted on RDS
ssh admin@10.42.241.3 '/disk print brief where slot~"pvc-aaaa|pvc-bbbb|pvc-cccc"'
```

**Expected:** No volumes found (all cleaned up)

**Success Criteria:**
- ✅ All three PVCs bound within 60 seconds
- ✅ All three pods running and volumes mounted
- ✅ Data isolation verified (no cross-contamination)
- ✅ All volumes visible on RDS
- ✅ All volumes cleaned up after deletion

**Troubleshooting:**
- **One PVC stuck:** Check controller logs for errors, verify RDS can handle concurrent operations
- **Slow binding:** Concurrent operations may take longer than sequential, up to 2-3x time is normal
- **Data contamination:** Indicates serious volume ID collision bug, report immediately

---

### TC-08: Volume Snapshot Operations — Failed / Unsupported

**Executed:** 2026-07-12 against an isolated `nvmet-rds-test.csi.whiskey.works`
deployment and RouterOS 7.23rc2.

**Do not execute the historical `copy-from` procedure.** Live testing proved
that `/disk add copy-from=[find slot=<source>]` copies disk configuration, not
the backing image data. Against an attached source it also disconnected the
source NVMe controller and caused workload I/O errors until the disposable
export was toggled off and on.

After cleanly detaching the source, snapshot creation reached `ReadyToUse`, but
restoring it produced a newly formatted blank ext4 filesystem containing only
`lost+found`; the source marker `tc08-before-snapshot-20260712` was absent.
Removing `file-size` from the restore command did not change the result.

RouterOS `/file copy` is a possible basis for a future offline full-copy design,
but the dedicated `metal-csi` account returned:

```text
not enough permissions (9) (/file/copy; line 1)
```

**Current result:** snapshot create, list, restore, and scheduling are
unsupported. The driver does not advertise snapshot capabilities and Helm does
not create a `VolumeSnapshotClass` by default.

**Recovery and cleanup evidence:** the disposable source data remained intact
after export reset; every test PVC, PV, `VolumeAttachment`, `VolumeSnapshot`,
snapshot content, RouterOS disk, and backing file was removed. The isolated
Helm release and namespace were deleted. Final appliance baseline returned to
78 disks and 73 `pvc-`/`snap-` files.

Any future TC-08 replacement requires a proven data-preserving primitive,
least-privilege access to it, explicit offline/application consistency
semantics, negative attached-volume tests, restore data-integrity proof, and
exact deletion cleanup evidence.


---

### TC-09: NVMe Reconnect After Network Interruption

**Objective:** Verify that after a network interruption causes NVMe/TCP connection drop, pods with mounted volumes recover and continue I/O without manual intervention.

**Estimated Time:** 15 minutes

**Prerequisites:**
- TC-01 passed and environment validation complete
- Worker node SSH access
- Ability to temporarily block NVMe/TCP traffic on the worker node (iptables or similar)

> **CAUTION:** This test temporarily interrupts NVMe/TCP traffic on a worker node. If other pods use RDS volumes on the same node, they will also be affected. Run during a maintenance window.

**Steps:**

#### 1. Create PVC and Pod with Continuous I/O Writer

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-09
  labels:
    test: hardware-validation
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-09
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: alpine
    command: ["sh", "-c", "while true; do date >> /data/io-test.log; sleep 1; done"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-09
EOF
```

#### 2. Wait for Pod Running and Verify I/O

```bash
kubectl wait --for=condition=Ready pod/test-hw-pod-09 --timeout=60s

# Verify I/O is happening
kubectl exec test-hw-pod-09 -- tail -5 /data/io-test.log
```

**Expected:** Recent timestamps appearing in the log file

```
Wed Feb 18 10:00:01 UTC 2026
Wed Feb 18 10:00:02 UTC 2026
Wed Feb 18 10:00:03 UTC 2026
```

#### 3. Note the Last Written Timestamp

```bash
kubectl exec test-hw-pod-09 -- tail -1 /data/io-test.log
```

Save this timestamp. After the test, data written before the interruption must still be present.

#### 4. Get Node Placement and Block NVMe/TCP Traffic

```bash
NODE_NAME=$(kubectl get pod test-hw-pod-09 -o jsonpath='{.spec.nodeName}')
echo "Pod is on node: $NODE_NAME"
```

SSH to the worker node and block NVMe/TCP traffic to RDS:

```bash
# SSH to the worker node
ssh <node-ip>

# Block NVMe/TCP outbound traffic to RDS storage IP
iptables -A OUTPUT -d 10.42.68.1 -p tcp --dport 4420 -j DROP
```

#### 5. Wait 30 Seconds and Observe NVMe Errors

```bash
# On the worker node, check for NVMe connection timeout/error messages
sleep 30
dmesg | tail -20 | grep nvme
```

**Expected:** NVMe error messages indicating connection timeout or I/O errors

```
[12345.678] nvme nvme1: Connect command failed, error wo/-19
[12345.679] nvme nvme1: queue 0: reconnecting in 1 seconds
```

#### 6. Verify Pod I/O Pauses

```bash
# I/O will stall because NVMe/TCP is blocked
kubectl exec test-hw-pod-09 -- tail -5 /data/io-test.log
```

**Expected:** No new timestamps appearing (write loop is stalled or filesystem hung/read-only).

#### 7. Restore NVMe/TCP Traffic

```bash
# On the worker node
iptables -D OUTPUT -d 10.42.68.1 -p tcp --dport 4420 -j DROP
```

#### 8. Wait for NVMe/TCP Reconnection

```bash
# Wait up to 60 seconds for kernel NVMe/TCP reconnection
# Controlled by ctrl_loss_tmo and reconnect_delay parameters
sleep 60

# Check NVMe reconnection messages on worker node
dmesg | tail -20 | grep nvme
```

**Expected:** NVMe reconnection messages

```
[12405.123] nvme nvme1: Successfully reconnected (1 attempts, <elapsed>s total)
```

The driver sets `ctrl_loss_tmo=-1` for infinite retry, so the kernel will keep attempting reconnection until it succeeds.

#### 9. Verify I/O Resumes

```bash
kubectl exec test-hw-pod-09 -- tail -10 /data/io-test.log
```

**Expected:** New timestamps after the gap, confirming I/O has resumed without pod restart.

```
Wed Feb 18 10:00:03 UTC 2026
<gap during interruption>
Wed Feb 18 10:01:05 UTC 2026
Wed Feb 18 10:01:06 UTC 2026
```

#### 10. Verify Pre-Interruption Data is Preserved

```bash
# Timestamps from before step 4 should still be in the log
kubectl exec test-hw-pod-09 -- head -10 /data/io-test.log
```

**Expected:** Data written before the network interruption is still present and readable.

**Cleanup:**

```bash
kubectl delete pod test-hw-pod-09
kubectl delete pvc test-hw-pvc-09

# Remove any lingering iptables rules (from the worker node)
ssh <node-ip> 'iptables -D OUTPUT -d 10.42.68.1 -p tcp --dport 4420 -j DROP 2>/dev/null; true'
```

**Success Criteria:**
- ✅ NVMe/TCP connection recovers automatically after network restoration
- ✅ I/O resumes without pod restart
- ✅ Data written before interruption is preserved
- ✅ No data corruption (log file readable, timestamps in order)

**Troubleshooting:**
- **I/O doesn't resume after 60s:** Check `ctrl_loss_tmo` setting on the worker node: `cat /sys/class/nvme/nvmeX/ctrl_loss_tmo`. If set to a short value (e.g., 600 seconds), the controller may have timed out and given up. The driver sets `ctrl_loss_tmo=-1` for infinite retry — verify this is applied during `NodeStageVolume`.
- **Pod is evicted:** Check if the node marked the volume as unhealthy, check kubelet logs for volume condition changes.
- **Filesystem goes read-only:** May require a pod restart to remount. This indicates `ctrl_loss_tmo` fired before reconnection, which means the timeout was not set to infinite.

---

### TC-10: RDS Restart Volume Preservation

**Objective:** Verify that after an RDS restart, volumes remain mounted and data written before the restart is readable after reconnection.

**Estimated Time:** 15-20 minutes

**Prerequisites:**
- TC-01 passed and environment validation complete
- SSH access to RDS management IP (10.42.241.3)
- Understanding that RDS restart will affect ALL NVMe/TCP connections on ALL nodes

> **DANGER:** Restarting RDS affects ALL NVMe/TCP connections on ALL cluster nodes. Only run this test during a maintenance window when no production workloads are running. Ensure you have physical access to the RDS hardware in case it doesn't come back online.

**Steps:**

#### 1. Create PVC and Pod

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-10
  labels:
    test: hardware-validation
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-10
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-10
EOF

kubectl wait --for=condition=Ready pod/test-hw-pod-10 --timeout=60s
```

#### 2. Write Test Data with Timestamp

```bash
kubectl exec test-hw-pod-10 -- sh -c 'echo "pre-restart-$(date +%s)" > /data/restart-test.txt'
```

#### 3. Verify Data and Save Output

```bash
SOURCE_DATA=$(kubectl exec test-hw-pod-10 -- cat /data/restart-test.txt)
echo "Pre-restart data: $SOURCE_DATA"
```

**Expected:** File contains the pre-restart timestamp

```
pre-restart-1771434800
```

#### 4. Check NVMe Connection Status Before Restart

```bash
NODE_NAME=$(kubectl get pod test-hw-pod-10 -o jsonpath='{.spec.nodeName}')
ssh <node-ip> 'nvme list-subsys'
```

**Expected:** Connection state is "live"

```
nvme-subsys1 - NQN=nqn.2000-02.com.mikrotik:pvc-<uuid>
 +- nvme1 tcp traddr=10.42.68.1 trsvcid=4420 live
```

#### 5. Restart RDS

```bash
ssh admin@10.42.241.3 '/system/reboot'
```

Confirm with `y` when prompted. The RDS will begin rebooting immediately.

#### 6. Wait for RDS to Come Back Online

Poll until the management interface responds (typically 60-120 seconds):

```bash
until ssh admin@10.42.241.3 '/system/resource/print' 2>/dev/null | grep -q version; do
  echo "Waiting for RDS to come back online..."
  sleep 10
done
echo "RDS is back online"
```

#### 7. Monitor NVMe Reconnection on Worker Node

```bash
ssh <node-ip> 'dmesg | tail -30 | grep nvme'
```

**Expected:** NVMe reconnection log messages

```
[54321.001] nvme nvme1: queue 0: reconnecting in 1 seconds
[54321.002] nvme nvme1: queue 0: reconnecting in 2 seconds
[54335.456] nvme nvme1: Successfully reconnected (8 attempts, 32.4s total)
```

#### 8. Wait for NVMe/TCP to Reconnect

```bash
# Check connection state (should return to "live")
ssh <node-ip> 'nvme list-subsys'
```

**Expected:** Connection state back to "live". With `ctrl_loss_tmo=-1` (infinite retry), the kernel will keep attempting reconnection for up to 2-3 minutes while RDS comes back online.

#### 9. Check Controller Logs for Reconnection

```bash
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin --tail=50 | grep -i "reconnect"
```

**Expected:** Connection manager logs showing SSH reconnection via exponential backoff

```
INFO    ConnectionManager: RDS connection lost to 10.42.241.3:22, starting reconnection
INFO    ConnectionManager: Reconnection attempt 1 to 10.42.241.3:22
INFO    ConnectionManager: Reconnection attempt 5 to 10.42.241.3:22
INFO    ConnectionManager: Successfully reconnected to 10.42.241.3:22 after 8 attempts (45.20s)
```

#### 10. Verify Data Integrity After Restart

```bash
RESTORED_DATA=$(kubectl exec test-hw-pod-10 -- cat /data/restart-test.txt)
echo "Post-restart data: $RESTORED_DATA"
echo "Pre-restart data:  $SOURCE_DATA"

if [ "$SOURCE_DATA" = "$RESTORED_DATA" ]; then
  echo "✓ Data integrity verified - RDS restart preserved data"
else
  echo "✗ Data mismatch - data loss occurred during RDS restart"
fi
```

**Expected:** Data matches exactly. File-backed volumes are stored on persistent Btrfs RAID6 storage and survive RDS reboots.

#### 11. Verify Continued I/O After Restart

```bash
kubectl exec test-hw-pod-10 -- sh -c 'echo "post-restart-$(date +%s)" >> /data/restart-test.txt && cat /data/restart-test.txt'
```

**Expected:** Both pre-restart and post-restart entries in the file

```
pre-restart-1771434800
post-restart-1771434999
```

**Cleanup:**

```bash
kubectl delete pod test-hw-pod-10
kubectl delete pvc test-hw-pvc-10
```

**Success Criteria:**
- ✅ RDS restarts and comes back online within 2 minutes
- ✅ NVMe/TCP connections reconnect automatically (ctrl_loss_tmo=-1 infinite retry)
- ✅ Data written before restart is fully preserved and readable
- ✅ New I/O succeeds after reconnection
- ✅ Controller SSH connection manager reconnects via exponential backoff

**Troubleshooting:**
- **RDS doesn't come back:** Check physical hardware, verify RDS management IP is still reachable, check power and network connections.
- **NVMe doesn't reconnect:** Check `ctrl_loss_tmo` on the worker node. If the kernel gave up (connection expired), a pod restart may be needed to trigger `NodeStageVolume` again.
- **Data is lost:** This would indicate a serious bug — file-backed volumes are on persistent Btrfs storage and should survive any RDS restart. Investigate storage pool integrity.
- **Controller can't reconnect:** Check SSH key is still valid, verify RDS IP hasn't changed after restart, check RDS user account and authorized_keys.

---

### TC-11: Node Failure Stale VolumeAttachment Cleanup

**Objective:** Verify that after a node failure, stale VolumeAttachment objects are detected and removed, allowing volumes to be reattached on another node.

**Estimated Time:** 10-15 minutes

**Prerequisites:**
- TC-01 passed and environment validation complete
- Cluster with at least 2 worker nodes
- kubectl admin access

**Steps:**

#### 1. Create PVC and Pod

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-hw-pvc-11
  labels:
    test: hardware-validation
spec:
  accessModes: [ReadWriteOnce]
  storageClassName: rds-nvme-tcp
  resources:
    requests:
      storage: 5Gi
---
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-11
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-11
EOF

kubectl wait --for=condition=Ready pod/test-hw-pod-11 --timeout=60s
```

#### 2. Note Which Node the Pod is Scheduled On

```bash
NODE_NAME=$(kubectl get pod test-hw-pod-11 -o jsonpath='{.spec.nodeName}')
echo "Pod is on node: $NODE_NAME"
```

#### 3. Write Test Data

```bash
kubectl exec test-hw-pod-11 -- sh -c 'echo "node-failure-test-$(date +%s)" > /data/node-test.txt'
kubectl exec test-hw-pod-11 -- cat /data/node-test.txt
```

**Expected:** Data written and readable

```
node-failure-test-1771434800
```

#### 4. Verify VolumeAttachment Exists

```bash
PV_NAME=$(kubectl get pvc test-hw-pvc-11 -o jsonpath='{.spec.volumeName}')
echo "PV name: $PV_NAME"
kubectl get volumeattachment | grep "$PV_NAME"
```

**Expected:** VolumeAttachment object exists for the volume

```
csi-<hash>   nvmet.csi.whiskey.works   <pv-name>   <node-name>   true   Running   <age>
```

#### 5. Simulate Node Failure

```bash
# Cordon and drain the node to evict the pod
kubectl cordon $NODE_NAME
kubectl drain $NODE_NAME --force --ignore-daemonsets --delete-emptydir-data --grace-period=0

# Wait for pod to be evicted
kubectl wait --for=delete pod/test-hw-pod-11 --timeout=60s

# Delete the node object to simulate the node going away entirely
kubectl delete node $NODE_NAME
```

**Expected:** Node deleted, pod is in Terminating or Evicted state.

#### 6. Observe Attachment Reconciler Behavior

```bash
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin --tail=50 | grep -i "stale\|reconcil\|attachment"
```

**Expected:** Attachment reconciler logs detecting the stale attachment

```
INFO    Starting attachment reconciliation
INFO    Clearing stale attachment: volume=pvc-<uuid> node=<node-name> (node deleted)
INFO    Attachment reconciliation complete: cleared 1 stale attachments (duration=12ms)
```

#### 7. Wait for Stale Attachment Cleanup

The attachment reconciler runs on a 5-minute interval with a 30-second grace period. To see cleanup faster, restart the controller pod to trigger immediate reconciliation:

```bash
# Trigger immediate reconciliation by restarting the controller
kubectl rollout restart deployment/nvmet-csi-controller -n kube-system
kubectl rollout status deployment/nvmet-csi-controller -n kube-system --timeout=60s
```

Then check the reconciler logs again:

```bash
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin --tail=50 | grep -i "stale\|reconcil"
```

#### 8. Verify VolumeAttachment is Cleared

```bash
kubectl get volumeattachment | grep "$PV_NAME"
```

**Expected:** No output or the VolumeAttachment is gone.

```
(no output)
```

#### 9. Re-Join Node or Reschedule on Another Node

If using a Deployment: The pod will be rescheduled to another available node automatically once the VolumeAttachment is cleared.

If testing with a bare Pod, recreate on another node:

```bash
kubectl apply -f - <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: test-hw-pod-11-v2
  labels:
    test: hardware-validation
spec:
  containers:
  - name: app
    image: nginx:alpine
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-hw-pvc-11
EOF

kubectl wait --for=condition=Ready pod/test-hw-pod-11-v2 --timeout=60s
```

#### 10. Verify Data is Accessible on New Node

```bash
kubectl exec test-hw-pod-11-v2 -- cat /data/node-test.txt
```

**Expected:** Data from step 3 is readable on the new node

```
node-failure-test-1771434800
```

**Cleanup:**

```bash
# Delete pods
kubectl delete pod test-hw-pod-11 test-hw-pod-11-v2 --ignore-not-found=true

# Delete PVC
kubectl delete pvc test-hw-pvc-11

# Re-join the drained node to the cluster (node-specific procedure)
# e.g., for a NixOS node: run nixos-rebuild switch and rejoin via kubeadm
```

**Success Criteria:**
- ✅ VolumeAttachment for the deleted node is cleared by the reconciler
- ✅ Pod reschedules to another node successfully
- ✅ Volume reattaches to the new node without errors
- ✅ Data written on the original node is accessible on the new node

**Troubleshooting:**
- **VolumeAttachment persists:** Check reconciler logs, verify reconciler interval and grace period configuration. Force reconciliation by restarting the controller pod.
- **Pod can't reschedule:** Check if PV still has stale nodeAffinity. Check VolumeAttachment status for any blocking conditions.
- **Volume fails to attach on new node:** The NVMe/TCP connection from the old node may not have been cleanly disconnected. If the node crashed before `NodeUnstageVolume` ran, the NVMe subsystem may still hold the connection on the RDS side. Check for "already connected" errors in node plugin logs. Verify `nvme disconnect` ran on the old node (it may not have if the node hard-crashed) and check RDS connection count.

---

## Performance Baselines

These are expected timing values for common operations on production RDS hardware. Actual timings may vary based on network latency, RDS load, and storage pool performance.

| Operation | Expected Duration | Notes |
|-----------|------------------|-------|
| Volume creation (CreateVolume) | 10-30 seconds | Includes SSH overhead, disk allocation on RDS |
| NVMe/TCP connection (NodeStageVolume) | 2-5 seconds | Target discovery + connection + device appearance |
| Filesystem format (first mount) | 5-15 seconds | Depends on volume size, filesystem type (ext4 faster than xfs) |
| Volume deletion (DeleteVolume) | 5-15 seconds | Includes SSH overhead, file cleanup on RDS |
| Volume expansion (ControllerExpandVolume) | 5-20 seconds | File resize on RDS + filesystem resize |
| NVMe/TCP disconnection (NodeUnstageVolume) | 1-2 seconds | Unmount + NVMe disconnect |
| PVC binding (WaitForFirstConsumer) | 0-30 seconds | Depends on when pod is scheduled |

**I/O Performance (measured with fio):**

To benchmark I/O performance on a mounted volume:

```bash
# Create test pod with fio
kubectl run fio-benchmark --rm -i --tty --image=nixery.dev/shell/fio -- bash

# Inside pod, run sequential read test:
fio --name=seqread --rw=read --bs=1M --size=1G --numjobs=1 --direct=1 --filename=/data/testfile

# Run random read IOPS test:
fio --name=randread --rw=randread --bs=4k --size=1G --numjobs=1 --direct=1 --iodepth=32 --filename=/data/testfile
```

**Expected I/O performance:**
- Sequential read: ~2.0 GB/s per volume
- Sequential write: ~1.8 GB/s per volume
- Random read (4K): ~150K IOPS
- Random write (4K): ~50K IOPS
- Latency: 1-3ms (network + RDS processing)

---

## Troubleshooting Decision Tree

### Symptom: PVC Stuck in Pending

**Step 1:** Check controller pod status
```bash
kubectl get pods -n kube-system -l app=nvmet-csi-controller
```

- **If CrashLoopBackOff:** See "Controller Won't Start" below
- **If Running:** Continue to Step 2

**Step 2:** Check controller logs for CreateVolume errors
```bash
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin --tail=50 | grep CreateVolume
```

- **If "SSH connection failed":** See "SSH Authentication Issues" below
- **If "not enough space":** See "Insufficient Storage" below
- **If "timeout":** See "SSH Timeout Issues" below
- **If no logs:** Continue to Step 3

**Step 3:** Check PVC events
```bash
kubectl describe pvc <pvc-name> | grep -A 10 Events
```

- **Look for "failed to provision volume"** with specific error message
- **If "WaitForFirstConsumer":** PVC waiting for pod to be scheduled (normal)

**Common Root Causes:**
- SSH key authentication failure → Verify Secret exists and is mounted correctly
- RDS disk full → Free space on RDS or expand storage pool
- Network connectivity to RDS → Verify routes, firewall rules
- StorageClass not found → Verify StorageClass exists with correct name

---

### Symptom: Pod Stuck in ContainerCreating

**Step 1:** Check node plugin pod status
```bash
kubectl get pods -n kube-system -l app=nvmet-csi-node
```

- **If no pods:** Node plugin DaemonSet not deployed
- **If not Running:** Check node plugin logs

**Step 2:** Check node plugin logs for NodeStageVolume errors
```bash
kubectl logs -n kube-system -l app=nvmet-csi-node -c nvmet-csi-plugin --tail=50 | grep NodeStage
```

- **If "nvme connect failed":** See "NVMe Connection Failures" below
- **If "timeout waiting for device":** See "Device Appearance Timeout" below
- **If "mount failed":** See "Filesystem Mount Failures" below

**Step 3:** Check pod events
```bash
kubectl describe pod <pod-name> | grep -A 10 Events
```

- **Look for "FailedMount"** or "FailedAttachVolume" messages

**Common Root Causes:**
- NVMe/TCP connectivity issue → Verify port 4420 accessible from node
- Device doesn't appear after connect → Check dmesg on node for NVMe errors
- Filesystem format fails → Check disk full on RDS, verify volume size

---

### Symptom: SSH Authentication Issues

**Check SSH secret exists:**
```bash
kubectl get secret rds-ssh-key -n kube-system
```

**Verify secret mounted to controller:**
```bash
CONTROLLER_POD=$(kubectl get pod -n kube-system -l app=nvmet-csi-controller -o jsonpath='{.items[0].metadata.name}')
kubectl exec -n kube-system $CONTROLLER_POD -c nvmet-csi-plugin -- ls -la /etc/nvmet-csi/
```

**Test SSH connectivity manually:**
```bash
ssh -i <path-to-key> admin@10.42.241.3 '/system/resource/print'
```

**Common Fixes:**
- Recreate secret with correct private key
- Verify key file permissions (should be 0600)
- Check RDS user has permissions for /disk commands
- Verify SSH public key added to RDS authorized_keys

---

### Symptom: Insufficient Storage

**Check RDS capacity:**
```bash
ssh admin@10.42.241.3 '/file print detail where name="/storage-pool"'
```

**List all volumes:**
```bash
ssh admin@10.42.241.3 '/disk print brief'
```

**Common Fixes:**
- Delete unused volumes on RDS
- Expand RDS storage pool
- Reduce PVC requested size
- Clean up orphaned volumes (volumes on RDS without corresponding PVs)

---

### Symptom: NVMe Connection Failures

**Check NVMe/TCP connectivity from node:**
```bash
# From worker node
nvme discover -t tcp -a 10.42.68.1 -s 4420
```

**Check kernel NVMe/TCP support:**
```bash
modprobe nvme-tcp
lsmod | grep nvme
```

**Check firewall rules:**
```bash
# Verify port 4420 is accessible
telnet 10.42.68.1 4420
```

**Common Fixes:**
- Load nvme-tcp kernel module: `modprobe nvme-tcp`
- Add firewall rule to allow port 4420
- Verify network connectivity to storage IP (10.42.68.1)
- Check RDS NVMe/TCP service is running

---

### Symptom: Volume Not Deleted on RDS

**Verify PVC is deleted:**
```bash
kubectl get pvc <pvc-name>
```

**Check controller logs for DeleteVolume errors:**
```bash
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin --tail=50 | grep DeleteVolume
```

**Manual cleanup on RDS:**
```bash
# List volumes
ssh admin@10.42.241.3 '/disk print brief where slot~"pvc-"'

# Delete specific volume
ssh admin@10.42.241.3 '/disk remove [find slot="pvc-12345678-1234-1234-1234-123456789abc"]'
```

**Common Fixes:**
- Check SSH connectivity to RDS
- Verify controller has DeleteVolume permissions
- Manually delete orphaned volume on RDS

---

### Symptom: Expansion Not Reflecting

**Check PVC events:**
```bash
kubectl describe pvc <pvc-name> | grep -A 10 Events
```

**Check controller logs:**
```bash
kubectl logs -n kube-system -l app=nvmet-csi-controller -c nvmet-csi-plugin --tail=50 | grep ControllerExpandVolume
```

**Check node logs:**
```bash
kubectl logs -n kube-system -l app=nvmet-csi-node -c nvmet-csi-plugin --tail=50 | grep NodeExpandVolume
```

**Verify StorageClass allows expansion:**
```bash
kubectl get storageclass rds-nvme-tcp -o jsonpath='{.allowVolumeExpansion}'
```

**Common Fixes:**
- Verify StorageClass has allowVolumeExpansion: true
- Check RDS has free space for expansion
- Verify filesystem supports online resize (ext4/xfs do, others may not)
- Restart pod to trigger filesystem resize if stuck

---

## Results Template

Use this table to record test results for your environment:

| Test Case | Status | Duration | Notes |
|-----------|--------|----------|-------|
| TC-01: Basic Volume Lifecycle | ☐ Pass / ☐ Fail | ___ min | |
| TC-02: NVMe/TCP Connection | ☐ Pass / ☐ Fail | ___ min | |
| TC-03: Volume Expansion | ☐ Pass / ☐ Fail | ___ min | |
| TC-04: Block Volume (Optional) | ☐ Pass / ☐ Fail / ☐ Skip | ___ min | |
| TC-05: Pod Reattachment | ☐ Pass / ☐ Fail | ___ min | |
| TC-06: Connection Resilience | ☐ Pass / ☐ Fail | ___ min | |
| TC-07: Concurrent Operations | ☐ Pass / ☐ Fail | ___ min | |
| TC-08: Snapshot Operations (copy-from) | ☐ Pass / ☐ Fail | ___ min | |
| TC-09: NVMe Reconnect After Network Interruption | ☐ Pass / ☐ Fail | ___ min | |
| TC-10: RDS Restart Volume Preservation | ☐ Pass / ☐ Fail | ___ min | |
| TC-11: Node Failure Stale VolumeAttachment Cleanup | ☐ Pass / ☐ Fail | ___ min | |

**Environment Details:**
- RDS Hardware: _______________
- RouterOS Version: _______________
- Kubernetes Version: _______________
- Worker Node OS: _______________
- nvme-cli Version: _______________
- Test Date: _______________
- Tested By: _______________

**Performance Measurements:**

| Operation | Expected | Actual | Delta |
|-----------|----------|--------|-------|
| Volume Creation | 10-30s | ___s | ___ |
| NVMe Connect | 2-5s | ___s | ___ |
| Volume Deletion | 5-15s | ___s | ___ |
| Volume Expansion | 5-20s | ___s | ___ |

**Issues Encountered:**

1. Issue: _______________
   - Test Case: TC-___
   - Resolution: _______________

**Additional Notes:**

_______________________________________________________________________________

---

## Cleanup All Test Resources

If tests were interrupted or cleanup failed, use this command to remove all test resources:

```bash
# Delete all pods with test label
kubectl delete pods -l test=hardware-validation

# Delete all PVCs with test label
kubectl delete pvc -l test=hardware-validation

# Wait for cleanup
sleep 30

# Verify all test volumes removed from RDS
ssh admin@10.42.241.3 '/disk print brief where slot~"test-hw"'
```

**Expected:** No output (all test volumes cleaned up)

---

## Related Documentation

- [Testing Guide](TESTING.md) - Unit, integration, and E2E test procedures
- [Architecture](architecture.md) - System design and component interactions
- [Kubernetes Setup Guide](kubernetes-setup.md) - Driver installation and configuration
- [RDS Commands Reference](rds-commands.md) - RouterOS CLI commands reference
- [Troubleshooting](../README.md#troubleshooting) - Common issues and solutions

---

**Last Updated:** 2026-02-18
**Maintained By:** nvmet-csi driver Team
**Feedback:** Report issues or suggest improvements via GitHub Issues
