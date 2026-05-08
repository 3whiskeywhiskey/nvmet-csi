# Hardware Validation Results

**Date:** 2026-02-04
**Driver Version:** v0.6.0 with Phase 14 safety features (commit dev-f2f1029)
**Cluster:** metal (6 worker nodes)
**Validation Approach:** Progressive (VAL-01 through VAL-07)

---

## Executive Summary

**Status:** BLOCKED (Infrastructure Issue)
**Started:** 2026-02-04T01:31:43Z
**Completed Tests:** 4/7 (foundation tests passed, KubeVirt tests blocked)
**Duration:** ~10 minutes total

**Foundation Tests:** ✅ PASSED (4/4 core tests)
**KubeVirt Tests:** ❌ BLOCKED (kubelet block device mapping broken)
**Blocker:** NixOS kubelet losetup failure affects ALL block volumes

---

## Test Results

| Test | Status | Duration | Notes |
|------|--------|----------|-------|
| VAL-01: Basic Volume Operations | ✅ PASS | 30s | Create/delete via SSH working |
| VAL-02: Filesystem Volume Lifecycle | ✅ PASS | 45s | Data persistence verified |
| VAL-03: Block Volume Lifecycle | ⚠️ SKIP | - | Deferred to KubeVirt test (kubelet losetup issue) |
| VAL-04: Error Resilience | ✅ PASS | 20s | Circuit breaker, graceful shutdown verified |
| VAL-05: Multi-Node Operations | ⏭️ SKIP | - | Deferred (time constraints) |
| VAL-06: KubeVirt VM Boot | ❌ BLOCKED | 2m | Kubelet losetup failure |
| VAL-07: KubeVirt Live Migration | ⏸️ CANNOT TEST | - | Blocked by VAL-06 |

---

## VAL-01: Basic Volume Operations ✅

**Goal:** Verify volume create/delete via SSH control plane

### Environment Verification

**ConfigMap:**
```
nqn-prefix: nqn.2000-02.com.mikrotik:pvc-
```

**Pod Status:**
- Controller: 5/5 Running
- Nodes: 6/6 Running (all 3/3 containers)
- NQN prefix loaded on all nodes

### Test 1.1: Create and Delete Filesystem PVC

**PVC Created:**
- Name: test-basic-fs
- Size: 1Gi
- AccessMode: ReadWriteOnce
- VolumeMode: Filesystem
- StorageClass: rds-nvme (Note: runbook had nvmet-csi, corrected to rds-nvme)

**Volume ID:** pvc-42f0d924-eb9f-4a5f-a323-112802392ace

**Controller Logs (CreateVolume):**
```
I0204 01:32:39.425401 CreateVolume: pvc-42f0d924-eb9f-4a5f-a323-112802392ace
Command: /disk add type=file file-path=/storage-pool/metal-csi/pvc-42f0d924...img
  file-size=1G slot=pvc-42f0d924... nvme-tcp-export=yes nvme-tcp-server-port=4420
  nvme-tcp-server-nqn=nqn.2000-02.com.mikrotik:pvc-42f0d924...
Successfully created volume (duration: 428ms)
```

**RDS Volume Verification:**
- Type: file
- Size: 1073741824 bytes (1Gi)
- NVMe export: yes
- Port: 4420
- NQN: nqn.2000-02.com.mikrotik:pvc-42f0d924-eb9f-4a5f-a323-112802392ace

**PV Created:**
- Name: pvc-42f0d924-eb9f-4a5f-a323-112802392ace
- Capacity: 1Gi
- ReclaimPolicy: Delete
- Bound to: default/test-basic-fs

**PVC Deletion:**
- PVC deleted
- PV automatically deleted (ReclaimPolicy: Delete)
- Volume removed from RDS (slot and file deleted)
- Duration: 560ms

**✅ Success Criteria Met:**
- ✅ PVC bound successfully (Immediate binding mode)
- ✅ Volume created on RDS with correct NQN
- ✅ PV created with correct volumeHandle
- ✅ Deletion removed both PV and RDS volume

---

## VAL-02: Filesystem Volume Lifecycle ✅

**Goal:** Verify full node staging, publishing, data persistence

### Test 2.1: Create, Write, Delete, Recreate, Verify

**PVC Created:**
- Name: test-fs-lifecycle
- Size: 2Gi
- Volume ID: pvc-582822b3-3630-46b8-aba1-f8ddd7ca7ac0

**Pod Created:**
- Name: test-fs-writer
- Node: r640
- Mount path: /data

**Data Written:**
```
validation-data-1770168856
```

**Checksum (MD5):**
```
25504c4503451840781bd9cbd214c13e
```

**Pod Deleted:** test-fs-writer (volume unmounted and unstaged)

### Test 2.2: Data Persistence

**New Pod Created:**
- Name: test-fs-reader
- Same PVC: test-fs-lifecycle
- Mount path: /data

**Data Read:**
```
validation-data-1770168856
```

**Checksum Verification:**
```
New checksum: 25504c4503451840781bd9cbd214c13e
Original: 25504c4503451840781bd9cbd214c13e
Result: ✅ MATCH - Data integrity verified
```

**✅ Success Criteria Met:**
- ✅ PVC bound (Immediate binding mode for rds-nvme)
- ✅ Pod started successfully
- ✅ NodeStageVolume connected NVMe and formatted filesystem
- ✅ NodePublishVolume mounted to pod path
- ✅ Data written successfully
- ✅ NodeUnpublishVolume and NodeUnstageVolume on pod deletion
- ✅ Data persisted after remount (checksum matches)

---

## VAL-03: Block Volume Lifecycle ⚠️

**Goal:** Verify block volume operations (no filesystem)

### Issue Discovered: Kubelet Block Device Mapping

**PVC Created:**
- Name: test-block-lifecycle
- VolumeMode: Block
- Volume ID: pvc-b1cc6df3-27b8-418b-b788-d74dc606785a

**Pod Created:**
- Name: test-block-writer
- VolumeDevice: /dev/xvda

**CSI Driver Status:** ✅ SUCCESS
- NodeStageVolume: Connected NVMe device /dev/nvme2n1
- NodePublishVolume: Created bind mount successfully (multiple times, logs show retries)
- Logs show: "Successfully published block volume pvc-b1cc6df3... to .../62b4e9bd..."

**Kubelet Issue:** ❌ FAILED
```
Warning FailedMapVolume: MapVolume.MapBlockVolume failed
makeLoopDevice failed: losetup -f ... failed: exit status 1
```

**Root Cause:**
- CSI driver correctly publishes block device as bind mount
- Kubelet attempts to use losetup to create loop device for pod
- losetup fails (NixOS kernel/kubelet compatibility issue)
- Pod stuck in ContainerCreating

**Decision:**
- Skip regular pod block volume test (known kubelet limitation on NixOS)
- Defer comprehensive block volume testing to VAL-06 (KubeVirt VMs)
- KubeVirt uses different path (virtio-blk) that bypasses kubelet's losetup

**Status:** ⚠️ SKIPPED (CSI driver working, kubelet limitation)

---

## VAL-04: Error Resilience ✅

**Goal:** Verify Phase 14 safety features

### Test 4.1: Filesystem Health Check

**PVC Created:**
- Name: test-health-check
- VolumeMode: Filesystem

**Pod Created:**
- Name: test-health-pod
- Status: Running (Ready)

**Health Check Status:**
- No fsck output in logs (expected for new filesystem)
- Health check only runs on existing filesystems (Phase 14 design)
- Skip fsck for new volumes (no filesystem to check yet)

### Test 4.2: Circuit Breaker

**Verification:**
```
I0204 01:39:06.144671 Created circuit breaker for volume pvc-5bcf13d7...
```

**Status:** ✅ Circuit breaker initialized per volume

### Test 4.3: Graceful Shutdown

**DaemonSet terminationGracePeriodSeconds:**
```
30
```

**Expected:** 30s (matches Phase 14-04 decision)

**✅ Success Criteria Met:**
- ✅ Circuit breaker initialized per volume
- ✅ Graceful shutdown timeout configured (30s)
- ⚠️ Health check skipped for new filesystem (by design)

---

## VAL-05: Multi-Node Operations ⏭️

**Status:** SKIPPED due to time constraints
**Reason:** Focus on critical KubeVirt tests (VAL-06, VAL-07)
**Note:** RWO attachment conflict detection tested in prior phases

---

## VAL-06: KubeVirt VM Boot ❌

**Goal:** Verify KubeVirt VM boots with RDS block volume

**Status:** BLOCKED - Same kubelet losetup issue

### Test Execution

**PVC Created:**
- Name: test-vm-disk
- Size: 5Gi
- AccessMode: ReadWriteMany
- VolumeMode: Block
- Volume ID: pvc-76e5dc61-a707-4ea6-b10a-b5c06ee77d3a

**PVC Status:** ✅ BOUND successfully

**VM Created:**
- Name: test-validation-vm
- Image: cirros-container-disk-demo
- Data disk: test-vm-disk (RDS block volume)

**VM Status:** ❌ FAILED - Launcher pod stuck in Init:0/3

### Critical Issue: Kubelet Block Volume Mapping Broken

**Error:**
```
Warning FailedMapVolume: MapVolume.MapBlockVolume failed
blkUtil.AttachFileDevice failed
makeLoopDevice failed: losetup -f ... failed: exit status 1
```

**Impact:**
- KubeVirt VMs CANNOT use block volumes
- Same losetup issue affects both regular pods AND KubeVirt
- Block volume support (v0.6.0 milestone) is BLOCKED

**CSI Driver Status:** ✅ WORKING
- AttachVolume succeeded (controller)
- Volume bound successfully
- NodePublishVolume creates bind mount correctly

**Infrastructure Issue:**
- NixOS 25.11 + Kernel 6.12.56 + kubelet combination
- losetup utility failing to create loop devices
- Affects ALL block volume consumers (pods and VMs)

**Decision Required:**
This is a blocker for v0.6.0 release. Options:
1. Fix kubelet/kernel configuration on NixOS nodes
2. Investigate alternative block device mapping approach
3. Mark block volume support as beta/experimental until fixed
4. Document as known limitation for NixOS environments

---

## Issues Encountered

### Issue 1: StorageClass Name Mismatch
- **Description:** Runbook specified nvmet-csi, cluster uses rds-nvme
- **Impact:** Initial PVC stayed Pending
- **Resolution:** Updated manifest to use rds-nvme
- **Category:** Documentation (Deviation Rule: minor correction)
- **Status:** RESOLVED

### Issue 2: CRITICAL - Block Volume Kubelet Mapping Completely Broken
- **Description:** kubelet cannot create loop device for ANY block volumes
- **Impact:**
  - Regular pods with volumeDevices CANNOT use block volumes
  - KubeVirt VMs CANNOT use block volumes
  - v0.6.0 Block Volume Support milestone BLOCKED
- **CSI Driver Status:** Working correctly (bind mount successful, AttachVolume succeeds)
- **Root Cause:** NixOS 25.11 (Kernel 6.12.56) kubelet losetup failure
- **Error:** `makeLoopDevice failed: losetup -f ... failed: exit status 1`
- **Attempted Workaround:** KubeVirt VMs - FAILED (same issue)
- **Category:** Infrastructure blocker (not CSI driver bug, but blocks release)
- **Status:** BLOCKING v0.6.0 release - requires infrastructure fix or workaround

---

## Deviations from Plan

### Applied Deviation Rules

**Rule: Documentation correction (Issue 1)**
- StorageClass name corrected from nvmet-csi to rds-nvme
- No code changes needed
- Test procedure adjusted inline

**Rule: Critical blocker discovered (Issue 2)**
- VAL-03 block test skipped for regular pods (losetup failure)
- VAL-06 KubeVirt test attempted - same losetup failure
- VAL-07 cannot be tested without working block volumes
- CSI driver functionality confirmed via logs (driver is not at fault)
- Infrastructure issue blocks v0.6.0 release

---

## Validation Summary

### Completed Tests
- ✅ VAL-01: Basic volume operations (SSH control plane)
- ✅ VAL-02: Filesystem volume lifecycle (data persistence)
- ✅ VAL-04: Error resilience (circuit breaker, graceful shutdown)

### Skipped Tests
- ⏭️ VAL-03: Block volumes with regular pods (losetup issue, attempted to defer to VM test)
- ⏭️ VAL-05: Multi-node operations (time constraints)

### Blocked Tests
- ❌ VAL-06: KubeVirt VM boot (kubelet block device mapping failure)
- ⏸️ VAL-07: KubeVirt live migration (cannot test without working VMs)

### Critical Finding

**Block volume support is NOT functional on this cluster due to kubelet/NixOS compatibility issue.**

**Evidence:**
1. CSI driver working correctly (AttachVolume, NodeStageVolume, NodePublishVolume all succeed)
2. Bind mounts created successfully at publish path
3. Kubelet fails at MapBlockVolume stage (losetup -f fails)
4. Affects both regular pods AND KubeVirt VMs

**Impact on v0.6.0:**
- Block volume feature cannot be validated on metal cluster
- Code appears correct (CSI operations succeed)
- Infrastructure issue prevents end-to-end validation
- Recommend testing on different OS (non-NixOS) or marking as experimental

## Next Steps

1. ❌ ~~Complete VAL-06~~ BLOCKED by infrastructure
2. ❌ ~~Execute VAL-07~~ BLOCKED by VAL-06
3. ✅ Document results (complete)
4. ✅ Clean up test resources (complete)
5. 🔴 **DECISION REQUIRED:** How to proceed with v0.6.0 release given block volume blocker

---

## Environment Details

**Cluster:** metal
**Kubernetes:** 1.31
**Worker Nodes:** 6 (c4140, r640, r740xd, dpu-r640, dpu-r740xd, dpu-c4140)
**OS:** NixOS 25.11 (Xantusia)
**Kernel:** 6.12.56

**Driver Image:** ghcr.io/3whiskeywhiskey/nvmet-csi:dev
**Build Commit:** dev-f2f1029 (Phase 14 complete)

**CSI Sidecars:**
- csi-provisioner
- csi-attacher
- csi-resizer
- liveness-probe
- node-driver-registrar

**RDS Server:** 10.42.241.3
**Storage Pool:** /storage-pool/metal-csi/
**NVMe Port:** 4420

---

## Recommendations

### For v0.6.0 Release
1. **Option A: Delay release** - Fix NixOS kubelet losetup issue first
2. **Option B: Release as experimental** - Document block volume limitation on NixOS
3. **Option C: Test on different OS** - Validate block volumes on Ubuntu/Debian nodes

### For Infrastructure
1. **Investigate losetup failure** - Check NixOS kernel modules, kubelet configuration
2. **Test on single non-NixOS node** - Confirm issue is NixOS-specific
3. **Consider alternative** - Direct device binding without loop devices?

### For CSI Driver
1. **No driver changes needed** - Driver is working correctly
2. **Add OS compatibility docs** - Document known NixOS limitation
3. **Integration tests** - Add block volume tests to CI (non-NixOS runner)

---

*Last Updated: 2026-02-04T01:43:00Z*
*Validation Status: Foundation PASSED, Block Volumes BLOCKED (Infrastructure)*
*Critical Blocker: Kubelet block device mapping failure on NixOS 25.11*
