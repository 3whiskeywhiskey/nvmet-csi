---
phase: 27-documentation-a-hardware-validation
plan: 01
subsystem: documentation
status: complete
tags:
  - documentation
  - hardware-validation
  - testing
  - operator-guide
requires:
  - v0.9.0 deployed on production cluster
  - Real RDS hardware access
provides:
  - Hardware validation procedures
  - Test scenarios with exact commands
  - Troubleshooting decision trees
  - Performance baselines
affects:
  - Phase 28 (Additional Documentation) - Can reference hardware validation examples
  - Future deployments - Operators can validate driver against their RDS hardware
tech-stack:
  added: []
  patterns:
    - Test case format with objective, prerequisites, steps, cleanup, success criteria
    - Troubleshooting decision tree pattern
    - Expected vs actual performance tracking
decisions:
  - id: hw-validation-structure
    choice: "7 test cases covering lifecycle, NVMe/TCP, expansion, block volumes, failure recovery, resilience, concurrency"
    reason: "Comprehensive coverage of critical driver functionality validatable on real hardware"
  - id: timing-baselines
    choice: "Document expected operation timings (10-30s volume creation, 2-5s NVMe connect, etc.)"
    reason: "Operators need timing expectations to identify performance issues"
  - id: cleanup-idempotency
    choice: "All cleanup procedures work even if test fails mid-way"
    reason: "Prevent RDS storage exhaustion from incomplete test runs"
key-files:
  created:
    - docs/HARDWARE_VALIDATION.md
  modified:
    - README.md
metrics:
  duration: "4 minutes"
  completed: "2026-02-06"
---

# Phase 27 Plan 01: Hardware Validation Documentation Summary

> Comprehensive step-by-step test procedures for validating RDS CSI driver on production hardware

## One-Liner

Created HARDWARE_VALIDATION.md with 7 executable test cases (1565 lines) covering volume lifecycle, NVMe/TCP validation, expansion, block volumes, failure recovery, connection resilience, and concurrent operations—enabling operators to validate driver functionality against real RDS hardware.

## Objectives Met

✅ Created comprehensive hardware validation guide (HARDWARE_VALIDATION.md)
✅ 7 test cases with complete coverage of driver functionality
✅ Each test case includes exact commands, expected output, cleanup, and troubleshooting
✅ Performance baselines with expected timing for common operations
✅ Troubleshooting decision tree for common failure modes
✅ Added documentation link to README.md
✅ All commands reference real cluster IPs (192.0.2.10, 198.51.100.20)
✅ Cleanup procedures are idempotent (work even if tests fail mid-way)

## What Was Built

### 1. HARDWARE_VALIDATION.md (1565 lines)

**Purpose:** Step-by-step manual testing procedures that validate driver behavior against real MikroTik ROSE Data Server hardware.

**Key Sections:**

1. **Overview & Prerequisites**
   - Clear purpose statement (initial deployment, post-upgrade, troubleshooting)
   - Access requirements (RDS management IP, storage IP, SSH, kubectl)
   - Cluster requirements (driver version, storage space, nvme-cli)

2. **Environment Validation (Pre-flight checks)**
   - 5 validation steps to confirm environment readiness before testing
   - Verify controller running, node plugin running, StorageClass exists
   - Verify SSH connectivity to RDS, check storage capacity
   - Each step with exact command and expected output

3. **Test Cases (7 scenarios)**

   **TC-01: Basic Volume Lifecycle (~5 min)**
   - Create PVC, create pod, verify mount, write data, verify persistence
   - Verify volume on RDS via SSH, check NVMe/TCP export
   - Complete cleanup with verification
   - Success criteria: PVC bound <30s, data writable/readable, cleanup works

   **TC-02: NVMe/TCP Connection Validation (~5 min)**
   - Create volume, SSH to worker node, verify NVMe device exists
   - Check NVMe subsystem, verify NQN format, check transport type
   - Verify block device path, check connection parameters
   - Success criteria: NVMe device visible, correct NQN, TCP transport

   **TC-03: Volume Expansion (~5 min)**
   - Create 5Gi volume, expand to 10Gi, verify filesystem resize
   - Monitor expansion progress via events
   - Verify size in pod and on RDS
   - Success criteria: Expansion completes in 5-20s, size reflects in pod

   **TC-04: Block Volume for KubeVirt (~10 min, OPTIONAL)**
   - Create volumeMode: Block PVC, mount as device (not filesystem)
   - Write/read data via dd commands
   - Optional test (skip if KubeVirt not installed)
   - Success criteria: Block device accessible, I/O works

   **TC-05: Failure Recovery - Pod Deletion and Reattachment (~5 min)**
   - Create volume, write unique data, delete pod (NOT PVC)
   - Recreate pod with same PVC, verify data persisted
   - Tests data persistence across pod lifecycle
   - Success criteria: Data survives pod deletion

   **TC-06: Failure Recovery - RDS Connection Resilience (~10 min)**
   - Verify connection manager monitoring, check probe endpoint
   - Document expected behavior if RDS becomes unreachable
   - Validates monitoring, not destructive testing
   - Success criteria: Monitoring active, probe healthy, behavior documented

   **TC-07: Multi-Volume Concurrent Operations (~5 min)**
   - Create 3 PVCs simultaneously, create 3 pods
   - Write unique data to each, verify isolation
   - Tests concurrent controller operations
   - Success criteria: All volumes bind, no data cross-contamination

4. **Performance Baselines**
   - Expected timing for common operations (creation, connection, deletion, expansion)
   - I/O performance benchmarking guidance (fio commands)
   - Expected throughput: 2.0 GB/s read, 1.8 GB/s write
   - Expected IOPS: 150K random read, 50K random write
   - Latency: 1-3ms

5. **Troubleshooting Decision Tree**
   - Symptom-driven diagnostic flows:
     - PVC stuck in Pending → check controller → check SSH → check capacity
     - Pod stuck in ContainerCreating → check node logs → check NVMe/TCP
     - SSH authentication issues → verify secret → test connectivity
     - NVMe connection failures → check module → check firewall
     - Volume not deleted on RDS → manual cleanup procedure
     - Expansion not reflecting → check events → check logs
   - Each flow includes exact commands and common root causes

6. **Results Template**
   - Table for recording test outcomes (pass/fail, duration, notes)
   - Performance measurements (expected vs actual timing)
   - Environment details (RDS version, k8s version, OS)
   - Issues encountered section

7. **Cleanup All Test Resources**
   - Single command to remove all test resources if interrupted
   - Verify cleanup on RDS

### 2. README.md Updates

**Added Hardware Validation Guide to Documentation section:**
- Positioned as first entry (most relevant for operators)
- Links to step-by-step test procedures for production RDS hardware
- Clear description: "Step-by-step test procedures for production RDS hardware"

## Technical Decisions

### Decision 1: Test Case Coverage

**Choice:** 7 test cases covering:
- Basic lifecycle (TC-01)
- NVMe/TCP validation (TC-02)
- Volume expansion (TC-03)
- Block volumes for KubeVirt (TC-04, optional)
- Pod reattachment persistence (TC-05)
- Connection resilience monitoring (TC-06)
- Concurrent operations (TC-07)

**Rationale:**
- Covers all critical driver functionality validatable on real hardware
- Balances comprehensiveness with execution time (~45 min total)
- Includes optional test (TC-04) for KubeVirt users without blocking others
- Focuses on operator-facing scenarios, not internal implementation details

**Alternatives considered:**
- More test cases (snapshot tests, topology tests) → Deferred to Phase 26 completion
- Fewer test cases → Would miss critical scenarios like expansion or concurrency

### Decision 2: Timing Baselines

**Choice:** Document expected operation timings:
- Volume creation: 10-30s
- NVMe connect: 2-5s
- Volume deletion: 5-15s
- Expansion: 5-20s

**Rationale:**
- Operators need timing expectations to identify performance degradation
- Actual hardware behavior discovered empirically during v0.6-v0.9 development
- Ranges account for variability (network latency, RDS load)

**Alternatives considered:**
- No timing documentation → Users can't distinguish normal vs slow operations
- Exact timings → Would cause false positives due to environmental factors

### Decision 3: Cleanup Idempotency

**Choice:** All cleanup procedures work even if test fails mid-way
- Use idempotent delete commands (kubectl delete, no errors if already deleted)
- Cleanup sections executable standalone (don't depend on test success)
- Verify cleanup with explicit RDS check

**Rationale:**
- Prevent RDS storage exhaustion from incomplete test runs
- Enable partial test execution (run TC-01, TC-03, skip rest)
- Production RDS has limited storage, failed tests shouldn't consume it permanently

**Alternatives considered:**
- Cleanup only on success → Would leave orphaned volumes on RDS
- Automated cleanup via finalizers → Too complex for manual validation guide

### Decision 4: Production IP Addresses

**Choice:** Use actual production IPs in examples:
- Management: 192.0.2.10 (SSH port 22)
- Storage: 198.51.100.20 (NVMe/TCP port 4420)

**Rationale:**
- User's production cluster has these specific IPs
- Copy-paste commands work immediately without editing
- Reduces chance of typos in IP addresses

**Alternatives considered:**
- Generic IPs (192.168.1.1) → Would require editing every command
- Placeholders <rds-ip> → More error-prone, slower execution

### Decision 5: TC-06 Non-Destructive

**Choice:** TC-06 validates connection monitoring, NOT destructive RDS restart testing

**Rationale:**
- Production RDS restart would impact live workloads
- Connection manager can be validated without RDS downtime
- Real restart testing requires maintenance window (not routine validation)
- Document expected behavior without requiring execution

**Alternatives considered:**
- Full RDS restart test → Too risky for production, defer to staging environment testing
- Skip resilience validation → Misses critical v0.9.0 feature (connection manager)

## Implementation Notes

### Test Case Format Pattern

Each test case follows consistent structure:

```markdown
### TC-##: Test Name

**Objective:** What this test validates
**Estimated Time:** How long it takes
**Prerequisites:** What's needed before running

**Steps:**
1. Step with command
   Expected: Output sample
   If stuck: Troubleshooting command

**Cleanup:**
- Deletion commands
- Verification

**Success Criteria:**
- ✅ Criterion 1
- ✅ Criterion 2

**Troubleshooting:**
- Issue → Fix
```

**Benefits:**
- Consistent reading experience across all test cases
- Operators know what to expect before running test
- Troubleshooting guidance inline with steps (no context switching)
- Success criteria checklist makes pass/fail obvious

### Expected Output Samples

Every command includes expected output sample:

```bash
kubectl get pvc test-hw-pvc-01
```
```
NAME             STATUS    VOLUME   CAPACITY   ACCESS MODES   STORAGECLASS   AGE
test-hw-pvc-01   Pending                                      rds-nvme-tcp   5s
```

**Benefits:**
- Operators can compare actual vs expected output
- Identify deviations immediately (different status, missing fields)
- Builds confidence (seeing expected output confirms correctness)
- Debugging starts at first deviation point

### Troubleshooting Decision Trees

Symptom-driven diagnostic flows:

```
PVC Stuck in Pending
└─ Step 1: Check controller status
   ├─ If CrashLoopBackOff → See "Controller Won't Start"
   └─ If Running → Continue to Step 2
      └─ Step 2: Check controller logs
         ├─ If "SSH failed" → See "SSH Auth Issues"
         ├─ If "not enough space" → See "Insufficient Storage"
         └─ If "timeout" → See "SSH Timeout Issues"
```

**Benefits:**
- Guides operator from symptom to root cause
- Prevents random troubleshooting attempts
- Each branch has specific next action (not generic "check logs")
- Cross-references related issues for common patterns

## Deviations from Plan

None - plan executed exactly as written.

## Challenges & Solutions

### Challenge 1: Balancing Comprehensiveness vs. Execution Time

**Problem:** Could create 20+ test cases covering every driver feature, but would take hours to execute

**Solution:**
- Focus on critical operator-facing scenarios (7 test cases, ~45 min total)
- Mark TC-04 as OPTIONAL (KubeVirt-specific, skip if not relevant)
- Defer advanced scenarios (snapshots, cloning) to future phases

**Result:** Comprehensive coverage without overwhelming operators

### Challenge 2: Mock RDS vs. Real RDS Behavior

**Problem:** Test procedures must match real RDS behavior, not mock behavior

**Solution:**
- Use actual timings from production experience (10-30s creation, not instant mock response)
- Reference real RouterOS command output format (not simplified mock output)
- Include timing variability ranges (not exact values)
- Document "If stuck" guidance for every waiting step

**Result:** Guide reflects reality, operators aren't surprised by actual hardware timings

### Challenge 3: Cleanup on Test Failure

**Problem:** If test fails mid-way, cleanup commands at end of test case won't run

**Solution:**
- Make cleanup commands idempotent (no error if resource already deleted)
- Cleanup sections executable standalone (don't depend on test completion)
- Added "Cleanup All Test Resources" section at document end
- All test resources labeled `test=hardware-validation` for bulk cleanup

**Result:** Failed tests don't leave orphaned volumes on RDS

## Testing Results

**Verification checks:**
- ✅ File length: 1565 lines (exceeds 400-line minimum)
- ✅ Test case count: 7 (TC-01 through TC-07)
- ✅ Cleanup sections: 8 (one per test case + global cleanup)
- ✅ kubectl commands: 97 (real commands, not pseudocode)
- ✅ Expected output samples: Present for every command
- ✅ README.md link: Added to Documentation section as first entry

**Manual verification:**
- Commands reference production IPs (192.0.2.10, 198.51.100.20)
- Each test case has objective, prerequisites, steps, cleanup, success criteria
- Troubleshooting decision tree covers common failure modes
- Performance baselines section with timing expectations

## Files Changed

### Created Files

1. **docs/HARDWARE_VALIDATION.md** (1565 lines)
   - 7 test cases with complete operator instructions
   - Performance baselines and timing expectations
   - Troubleshooting decision trees
   - Results template for recording outcomes

### Modified Files

1. **README.md** (+1 line)
   - Added Hardware Validation Guide to Documentation section
   - Positioned as first entry for operator visibility

## Impact on Future Work

### Phase 28 (Additional Documentation)

**Positive impact:** Can reference hardware validation examples when documenting:
- Troubleshooting procedures (link to decision trees)
- Known limitations (reference test failure modes)
- Deployment best practices (timing expectations)

**Pattern established:** Test case format (objective, steps, cleanup, success criteria) can be reused for:
- Snapshot testing documentation (once Phase 26 snapshots are production-tested)
- KubeVirt VM migration validation
- Performance benchmarking procedures

### Future Deployments

**Operators can now:**
- Validate driver against their RDS hardware before production use
- Troubleshoot issues using decision trees (symptom → root cause)
- Benchmark their environment against expected timings
- Report bugs with reproducible test cases

**Confidence boost:**
- Test cases cover critical functionality comprehensively
- Expected output samples make pass/fail obvious
- Cleanup procedures prevent storage exhaustion

## Next Phase Readiness

**Phase 28 Prerequisites:** ✅ All met
- Hardware validation procedures documented
- Test case format pattern established
- Troubleshooting decision tree pattern established
- Performance baseline data documented

**Blockers:** None

**Dependencies resolved:**
- Production cluster IPs documented (192.0.2.10, 198.51.100.20)
- Expected timings captured from v0.6-v0.9 production experience
- NVMe/TCP validation procedures documented from real hardware

## Lessons Learned

### What Worked Well

1. **Test case format pattern** - Consistent structure made writing 7 test cases efficient
2. **Expected output samples** - Every command has example output (no guessing)
3. **Troubleshooting inline** - "If stuck" guidance at each step reduces context switching
4. **Idempotent cleanup** - Works even if test fails mid-way (prevents storage exhaustion)
5. **Production IP addresses** - Copy-paste commands work immediately

### What Could Be Improved

1. **Automated validation** - Could create script to execute test cases and validate output (future enhancement)
2. **Visual diagrams** - Could add architecture diagrams showing test scenarios (Mermaid format)
3. **Video walkthroughs** - Could record screen captures of test execution (reference material)

### Recommendations for Similar Work

1. **Document-as-you-validate** - Execute every procedure against real hardware before documenting
2. **Timing from reality** - Use actual production timings, not theoretical/mock timings
3. **Failure mode focus** - Document what to do when things go wrong, not just happy path
4. **Idempotency always** - Make every operation safe to retry (cleanup, validation checks)
5. **Results template** - Give operators structured way to record outcomes (pass/fail, timing, issues)

## Commits

**Task 1: Create HARDWARE_VALIDATION.md**
- Commit: `17e28c1` - docs(27-01): create comprehensive hardware validation guide
- Files: docs/HARDWARE_VALIDATION.md (+1565 lines)

**Task 2: Add README.md link**
- Commit: `75baa1e` - docs(27-01): add hardware validation guide to README
- Files: README.md (+1 line)

**Total:** 2 commits, 1566 lines added, 0 lines deleted

## Metadata

**Phase:** 27-documentation-a-hardware-validation
**Plan:** 01
**Duration:** 4 minutes
**Completed:** 2026-02-06
**Executor:** Claude (GSD execute-plan workflow)
**Status:** ✅ Complete - All objectives met, no deviations
