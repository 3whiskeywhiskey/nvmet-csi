# Pitfalls Research: CSI Driver Testing Infrastructure

**Domain:** Kubernetes CSI Driver Testing and Validation
**Researched:** 2026-02-04
**Confidence:** MEDIUM-HIGH

## Executive Summary

Adding production testing to CSI drivers reveals pitfalls clustered around four themes: **idempotency misunderstandings** (most critical), **mock-reality divergence** (most insidious), **test isolation failures** (most frustrating), and **hardware-specific timing** (most environment-dependent). The RDS CSI driver faces specific risks around NVMe/TCP disconnect cleanup, SSH-based control plane testing, and production hardware validation without breaking existing volumes.

## Critical Pitfalls

### Pitfall 1: Idempotency Violations Under Real Conditions

**What goes wrong:**
CSI sanity tests pass locally but fail when kubelet retries operations on production nodes. NodeStageVolume called twice causes "already mounted" errors, NodeUnstageVolume races with new NodeStageVolume calls, device path comparisons fail due to symlink vs canonical path mismatches.

**Why it happens:**
- Kubelet >= 1.20 assumes CSI driver implementations are idempotent and retries aggressively
- On AWS Nitro instances: `findDevicePath()` returns `/dev/xvdcf` (symlink) while `GetDeviceNameFromMount()` returns `/dev/nvme1n1` (canonical), breaking idempotency checks
- NodeStageVolume can be issued while NodeUnstageVolume is still in progress, violating spec assumptions
- Tests often use fresh state, missing edge cases like "volume already staged from previous call"

**How to avoid:**
1. **Test with kubelet retries**: Simulate kubelet restarting mid-operation, calling same method twice
2. **Canonical path normalization**: Always resolve symlinks before path comparisons (`filepath.EvalSymlinks()`)
3. **State checking first**: Check if operation already complete before starting (e.g., already mounted, already disconnected)
4. **Lock-free idempotency**: Don't rely on locks; make operations naturally idempotent
5. **Test race conditions**: Call NodeStageVolume while NodeUnstageVolume still running

**Warning signs:**
- Sanity tests pass but production pods fail to mount with "device busy" or "already exists"
- Kubelet logs show "operation already in progress" despite driver claiming success
- Device paths differ in logs vs actual mount table (`/dev/nvmeX` vs `/dev/disk/by-id/...`)
- Intermittent failures that don't reproduce in tests

**Phase to address:**
- **v0.9.0 Milestone 1 (CSI Sanity Tests)**: Add idempotency test cases, retry simulation
- **v0.9.0 Milestone 3 (E2E Testing)**: Include kubelet restart tests

**RDS-specific considerations:**
- NVMe-oF devices may appear as `/dev/nvme1n1` vs `/dev/disk/by-id/nvme-...` depending on udev timing
- SSH operations are slow (~200ms), increasing window for race conditions
- Multiple pods on same node could trigger concurrent CreateVolume calls

---

### Pitfall 2: Mock Storage Backend Diverges From Real Hardware

**What goes wrong:**
Tests pass against mock RDS server but fail against real RDS. Mock returns instant responses, real SSH has 200ms latency exposing timeout bugs. Mock always succeeds disk creation, real RDS returns "not enough space" or RouterOS version incompatibilities.

**Why it happens:**
- Mock maintainers don't use real hardware regularly, divergence accumulates
- Edge cases (disk full, concurrent operations, version differences) not in mock
- Mock doesn't simulate RouterOS CLI output variations (e.g., field order changes between RouterOS versions)
- Performance characteristics wildly different (mock: 1ms, real: 200ms SSH + 3s disk allocation)

**How to avoid:**
1. **Record real RDS interactions**: Use "record & replay" for mock development (save actual SSH command/response pairs)
2. **Fuzz mock responses**: Randomly inject delays, errors, field reordering to match real variability
3. **Test against real hardware weekly**: CI job that runs subset of tests against actual RDS instance
4. **Mock compatibility matrix**: Document which RouterOS versions mock represents
5. **Fail-fast on unknown responses**: Mock should error on unexpected commands, not silently succeed

**Warning signs:**
- Tests run 10x faster in CI than production (mock too fast)
- "Works in dev, breaks in staging" pattern
- Mock never returns certain error codes that real hardware does
- Test coverage metrics high but production has basic failures

**Phase to address:**
- **v0.9.0 Milestone 2 (Mock RDS)**: Design mock from recorded real interactions
- **v0.9.0 Milestone 5 (Hardware Testing)**: Weekly real RDS test run

**RDS-specific considerations:**
- RouterOS CLI output format varies by version (7.1 vs 7.16+)
- NVMe-TCP export behavior differs between RouterOS builds
- Btrfs filesystem timing (real disk allocation takes 1-3 seconds, mock should simulate this)
- SSH key authentication edge cases (password prompt, key format issues)

---

### Pitfall 3: Test Cleanup Failures Create Cascading Issues

**What goes wrong:**
Test suite crashes or times out, leaving orphaned volumes on RDS. Next test run fails because volume IDs collide or RDS runs out of space. Manual cleanup required between test runs. Tests become flaky because previous test's volume still mounted.

**Why it happens:**
- Tests killed mid-operation (timeout, crash, Ctrl-C) skip cleanup hooks
- Cleanup assumes success path; on failure, resources leak
- No global cleanup reconciliation between test runs
- Tests don't check for stale resources before starting
- VMs/pods killed during tests leave NVMe connections active

**How to avoid:**
1. **Dedicated test namespace**: Use unique volume ID prefix per test run (e.g., `test-<timestamp>-pvc-<uuid>`)
2. **Cleanup job before tests**: Pre-test reconciliation that removes all test-prefixed volumes
3. **Deferred cleanup in Go**: Use `defer` for cleanup even on panic
4. **Post-test orphan scan**: CI step that lists orphaned volumes and fails if found (but cleans them anyway)
5. **Test isolation**: Each test uses unique volume IDs, never reuses names

**Warning signs:**
- Test failures mention "volume already exists" or "name in use"
- RDS `/disk print` shows dozens of old test volumes
- CI flakiness increases over time (state accumulation)
- Tests fail after manual intervention but pass on fresh cluster

**Phase to address:**
- **v0.9.0 Milestone 1 (CSI Sanity Tests)**: Implement cleanup hooks, unique IDs per test
- **v0.9.0 Milestone 3 (E2E Testing)**: Pre/post-test reconciliation jobs

**RDS-specific considerations:**
- Orphan reconciler exists but needs dry-run testing in CI
- NVMe connections may persist after pod deletion if cleanup fails
- Btrfs file deletion is async; test may pass but file still exists briefly
- SSH connection pool must be drained before test cleanup

---

### Pitfall 4: Hardware-Specific Device Timing Not Captured in Tests

**What goes wrong:**
NVMe device appears instantly in tests but takes 2-30 seconds on real hardware. Test uses 1-second timeout, production needs 30 seconds. Device path format differs (`nvmeXnY` vs `nvmeXcYnZ` for NVMe-oF). Tests assume `/dev/nvme1n1`, production has `/dev/nvme27n1` after many disconnects.

**Why it happens:**
- Mock instantly returns device path, real kernel takes time to enumerate
- Test environment uses loopback devices or RAM disks (instant)
- Production uses NVMe-oF with network latency and target discovery time
- Device numbering is sequential; tests assume low numbers, production recycles

**How to avoid:**
1. **Adaptive timeouts**: Start with 5s, double on retry up to 60s max
2. **Device discovery by NQN, not path**: Match `/sys/class/nvme/*/subsysnqn` against expected NQN
3. **Test with real NVMe-oF target**: Even if mock backend, use actual `nvme connect` to real NVMe-TCP target
4. **Device number range testing**: Test with high device numbers (e.g., create/destroy 50 volumes first to get nvme50n1)
5. **Glob patterns, not hardcoded paths**: Use `/dev/nvme*n*` glob, iterate to find matching NQN

**Warning signs:**
- Timeouts only in production, never in CI
- "Device not found" errors despite successful `nvme connect`
- Tests break when run on nodes with existing NVMe devices
- Different behavior between first volume (nvme1n1) and tenth volume (nvme10n1)

**Phase to address:**
- **v0.9.0 Milestone 1 (CSI Sanity Tests)**: Device discovery by NQN, not path
- **v0.9.0 Milestone 5 (Hardware Testing)**: Real NVMe-TCP target tests with timing validation

**RDS-specific considerations:**
- Current implementation already uses NQN-based discovery (good!)
- 30-second timeout may still be too short for degraded networks
- NVMe-oF device naming varies by kernel version (5.0 vs 5.10+)
- RDS exports on port 4420; ensure no firewall delays

---

### Pitfall 5: CSI Sanity Tests Skip Critical Negative Cases

**What goes wrong:**
CSI sanity tests pass but production fails on error paths. Tests don't validate "volume already exists with different size" idempotency, "delete non-existent volume" behavior, "stage volume that's already staged elsewhere" concurrency.

**Why it happens:**
- `csi-sanity` focuses on happy path compliance, not edge cases
- Negative tests require manual implementation beyond sanity suite
- Snapshot tests often skipped (design limitations in sanity framework)
- Tests don't validate error message quality (just error presence)

**How to avoid:**
1. **Supplement csi-sanity**: Add custom negative test suite (e.g., `test/negative/`)
2. **Error code validation**: Verify gRPC error codes match spec (ResourceExhausted vs Unavailable)
3. **Concurrency tests**: Multiple goroutines calling same CSI method simultaneously
4. **Snapshot testing despite limitations**: Even if sanity skips it, manually test
5. **Capability validation**: Test that driver rejects unsupported operations (e.g., multi-attach for ReadWriteOnce)

**Warning signs:**
- "All tests pass" but production has errors
- Driver returns wrong gRPC error codes (generic Unavailable instead of specific codes)
- No tests for CreateVolume with existing volume ID
- No tests for concurrent operations

**Phase to address:**
- **v0.9.0 Milestone 1 (CSI Sanity Tests)**: Run full sanity suite including capability validation
- **v0.9.0 Milestone 4 (Gap Analysis)**: Identify missing negative tests, add to custom suite

**RDS-specific considerations:**
- RouterOS error messages vary; ensure parsing handles variations
- SSH timeout should return Unavailable, not Internal
- Volume name length limits (DNS-1123 label validation)

---

### Pitfall 6: Volume Naming Constraints Break Sanity Tests

**What goes wrong:**
CSI sanity generates volume names like `test-volume-with-very-long-name-12345678901234567890` that violate DNS-1123 label limits (63 chars) or Longhorn's volume naming requirements. Test fails with cryptic validation errors.

**Why it happens:**
- Sanity test doesn't know backend storage naming constraints
- CSI spec allows arbitrary volume IDs, but implementations have limits
- Driver doesn't validate volume ID format before SSH command injection

**How to avoid:**
1. **Volume ID transformation**: Hash long names to fixed-length IDs (e.g., `pvc-<uuid>`)
2. **Validation in CreateVolume**: Return InvalidArgument if volume ID too long/invalid
3. **Configure sanity test name prefix**: Use `-ginkgo.label-filter` to skip problematic tests or shorter prefix
4. **Document naming constraints**: In CSIDriver object and docs

**Warning signs:**
- Sanity test fails with "invalid name" from backend storage
- Volume IDs truncated, causing collisions
- DNS-1123 validation errors in Kubernetes API

**Phase to address:**
- **v0.9.0 Milestone 1 (CSI Sanity Tests)**: Add volume ID validation, test with long names

**RDS-specific considerations:**
- RouterOS slot names limited to certain characters
- Filesystem path length limits (Btrfs allows 255 bytes per component)

---

## Technical Debt Patterns

| Shortcut | Immediate Benefit | Long-term Cost | When Acceptable |
|----------|-------------------|----------------|-----------------|
| Skip snapshot tests in sanity suite | Faster test runs (20% speedup) | No validation of snapshot idempotency, may break in production | Never for drivers claiming snapshot support; Acceptable for MVP without snapshots |
| Use sleep instead of polling for device ready | Simple implementation (5 lines vs 20) | Race conditions, flaky tests, arbitrary timeouts | Never; always poll with timeout |
| Mock SSH responses without recording real output | Faster mock development (1 day vs 3) | Mock diverges from reality, test blind spots | Acceptable for initial prototyping; Must record real sessions before v1.0 |
| Single node E2E tests only | Cheaper CI (1 node vs 3) | Miss multi-node race conditions, volume movement edge cases | Acceptable for alpha; Must add multi-node by beta |
| Disable orphan reconciler in tests | Simpler test setup (no RBAC) | Orphans accumulate, tests become flaky over time | Never; reconciler is critical for test hygiene |

## Integration Gotchas

| Integration | Common Mistake | Correct Approach |
|-------------|----------------|------------------|
| csi-sanity | Assume passing = spec compliant | Supplement with custom negative tests and concurrency tests |
| NVMe-CLI | Use device path for disconnect (`nvme disconnect -d /dev/nvme1n1`) | Use NQN for disconnect (`nvme disconnect -n <nqn>`) or controller device (`/dev/nvme1`) |
| Kubernetes E2E | Import k8s.io/kubernetes/test/e2e (vendor hell) | Use standalone test framework, call kubectl directly |
| Mock SSH Server | Return success for all commands | Parse commands, validate parameters, return realistic errors |
| CSI Sidecar Versions | Use latest sidecars with old CSI spec | Pin sidecar versions compatible with driver's CSI spec version (v1.5.0 → specific sidecar versions) |

## Performance Traps

| Trap | Symptoms | Prevention | When It Breaks |
|------|----------|------------|----------------|
| Sequential test execution | Test suite takes 30+ minutes | Parallel test execution with unique volume IDs | >50 tests |
| No SSH connection pooling | CreateVolume takes 500ms (200ms SSH handshake) | Reuse SSH connections across operations | >10 volumes/min |
| Synchronous sanity tests | CI timeout (2hr default) | Run sanity with `-ginkgo.p` (parallel) and `-ginkgo.timeout 4h` | >100 test cases |
| Small-scale testing only | Performance looks good at 10 volumes | Load test with 100+ concurrent volume operations | Production with 50+ pods |
| Mock responds instantly | Tests assume <1s operations | Mock should simulate real latency (SSH: 200ms, disk create: 3s) | Any production use |

## Security Mistakes

| Mistake | Risk | Prevention |
|---------|------|------------|
| SSH private key in test fixtures | Keys leaked in Git history | Generate ephemeral keys per test run, never commit |
| Mock server accepts any command | Command injection testing blind spot | Mock should reject malicious inputs (shell metacharacters, path traversal) |
| Test namespace has cluster-admin | Tests can't catch RBAC issues | Use realistic RBAC in test environment (separate controller/node permissions) |
| No TLS for test clusters | Tests don't catch TLS issues | Use real certs even in dev (Let's Encrypt staging) |
| Volume IDs not validated | Command injection via crafted volume names | Strict validation: `^pvc-[a-f0-9-]+$` regex |

## Testing Anti-Patterns

### Anti-Pattern 1: "Works on My Machine" Mock Testing

**What it looks like:**
All tests use mock RDS server, never tested against real hardware until production.

**Why it's bad:**
Mock diverges from reality. Real hardware has timing issues, error modes, and quirks that mocks don't capture. Tests give false confidence.

**What to do instead:**
- Weekly CI job against real RDS instance (even if slower)
- "Record & replay" real RDS interactions to keep mock accurate
- Fail-fast when mock sees unexpected behavior (don't silently succeed)

---

### Anti-Pattern 2: Test Cleanup in AfterEach Only

**What it looks like:**
```go
AfterEach(func() {
    deleteVolume(volumeID)  // Cleanup
})
```

**Why it's bad:**
If test panics, crashes, or times out, `AfterEach` doesn't run. Orphaned volumes accumulate.

**What to do instead:**
```go
BeforeEach(func() {
    cleanupOrphanedTestVolumes()  // Pre-cleanup
})

It("test", func() {
    defer cleanupVolume(volumeID)  // Always runs
    // Test code
})
```

---

### Anti-Pattern 3: Hardcoded Device Paths

**What it looks like:**
```go
devicePath := "/dev/nvme1n1"  // Assume first NVMe device
```

**Why it's bad:**
Device numbers are sequential and reused. After 10 disconnects, you get `/dev/nvme11n1`. Tests break in production with multiple volumes.

**What to do instead:**
```go
devicePath := findDeviceByNQN(nqn)  // Search /sys/class/nvme/*/subsysnqn
```

---

## "Looks Done But Isn't" Checklist

Things that appear complete but are missing critical pieces:

- [ ] **CSI Sanity Tests**: Often missing negative tests (delete non-existent volume, create duplicate, concurrent operations)
- [ ] **Mock RDS Server**: Often missing error injection (disk full, SSH timeout, concurrent command conflicts)
- [ ] **E2E Tests**: Often missing cleanup validation (check no orphaned volumes after test suite)
- [ ] **NodeUnstageVolume**: Often missing force-disconnect logic (what if NVMe disconnect hangs?)
- [ ] **Device Discovery**: Often missing timeout and retry (assumes device appears instantly)
- [ ] **Idempotency**: Often missing "operation already complete" checks (assumes fresh state)
- [ ] **Volume ID Validation**: Often missing injection protection (allows shell metacharacters)
- [ ] **Error Handling**: Often returns generic errors (should return specific gRPC codes per CSI spec)

## Recovery Strategies

| Pitfall | Recovery Cost | Recovery Steps |
|---------|---------------|----------------|
| Idempotency violations in production | MEDIUM | 1. Add state checks before operations 2. Resolve symlinks in path comparisons 3. Add kubelet retry tests 4. Backport fixes to production |
| Mock diverged from real hardware | HIGH | 1. Record real RDS interactions 2. Update mock to match 3. Re-run all tests 4. Identify tests that were false-passing |
| Orphaned volumes from test failures | LOW | 1. Enable orphan reconciler with short grace period (1h) 2. Manual cleanup script 3. Add pre-test cleanup job |
| Device timing issues | LOW | 1. Increase timeouts (5s→30s) 2. Add retry logic 3. Use NQN-based discovery 4. Test with real hardware |
| Missing negative tests | MEDIUM | 1. Identify gaps via gap analysis 2. Add custom test suite 3. Run against production to verify fixes |
| Volume naming violations | LOW | 1. Add validation in CreateVolume 2. Transform long names to UUIDs 3. Update sanity test config |

## Pitfall-to-Phase Mapping

| Pitfall | Prevention Phase | Verification |
|---------|------------------|--------------|
| Idempotency violations | v0.9.0 Milestone 1 (Sanity Tests) | Kubelet restart test, retry simulation test pass |
| Mock divergence | v0.9.0 Milestone 2 (Mock RDS) | Real RDS test suite has >90% pass rate with mock |
| Test cleanup failures | v0.9.0 Milestone 1 (Sanity Tests) | Zero orphaned volumes after test suite completion |
| Device timing issues | v0.9.0 Milestone 5 (Hardware Testing) | Tests pass on real hardware with high device numbers |
| CSI sanity gaps | v0.9.0 Milestone 4 (Gap Analysis) | Gap analysis document lists all missing capabilities |
| Volume naming constraints | v0.9.0 Milestone 1 (Sanity Tests) | Sanity tests pass with long volume names |

## RDS CSI Driver Specific Pitfalls

### SSH-Based Control Plane Testing

**Pitfall:** Mock SSH server accepts all commands without validation, hiding command injection vulnerabilities and RouterOS syntax errors.

**Prevention:**
1. Mock must parse `/disk add` parameters and validate syntax
2. Reject invalid volume IDs (anything not matching `^pvc-[a-f0-9-]+$`)
3. Simulate RouterOS error messages for invalid commands
4. Test with malicious inputs (shell metacharacters, path traversal)

**Phase to address:** v0.9.0 Milestone 2 (Mock RDS)

---

### NVMe-oF Disconnect Cleanup

**Pitfall:** `nvme disconnect` fails silently when device is in use (mounted, open file handles). Volume appears to unmount but NVMe connection persists, causing "target busy" errors on next mount.

**Prevention:**
1. Check device not mounted before disconnect (`cat /proc/mounts | grep <device>`)
2. Use `fuser -m <mountpoint>` to check for processes using volume
3. Force unmount with `umount -f` if regular unmount fails
4. Retry disconnect with exponential backoff (device may be flushing buffers)
5. Log persistent connections for later reconciliation

**Warning signs:**
- `nvme list` shows stale connections after pod deletion
- "Address already in use" on `nvme connect`
- `/sys/class/nvme/` has entries after NodeUnstageVolume

**Phase to address:** v0.9.0 Milestone 3 (E2E Testing)

---

### Dual-IP Architecture Testing

**Pitfall:** Controller uses SSH address (192.0.2.10), nodes use NVMe address (198.51.100.20). Tests that use single IP miss cross-network validation. VolumeContext may have wrong IP for nodes.

**Prevention:**
1. E2E tests must use dual-IP configuration (matching production)
2. Validate VolumeContext contains `nvmeAddress` field
3. Test fallback behavior when `nvmeAddress` not set
4. Network partition tests (SSH reachable, NVMe unreachable)

**Phase to address:** v0.9.0 Milestone 3 (E2E Testing)

---

### RouterOS Version Compatibility

**Pitfall:** CLI output format varies between RouterOS 7.1, 7.10, 7.16. Parser breaks on unexpected field order or missing fields.

**Prevention:**
1. Test against multiple RouterOS versions (7.1, 7.10, 7.16+)
2. Regex parsers should be order-independent
3. Handle missing fields gracefully (use defaults)
4. Log unparsed CLI output for debugging

**Phase to address:** v0.9.0 Milestone 2 (Mock RDS) - record multiple versions

---

### Orphan Reconciler Testing Without Breaking Production

**Pitfall:** Orphan reconciler tests could delete real volumes if test uses production RDS. Reconciler needs comprehensive dry-run validation before enabling in production.

**Prevention:**
1. **Always test with dry-run first**: Verify reconciler identifies correct volumes
2. **Use test volume prefix**: Only reconcile volumes matching `test-*` prefix in tests
3. **Separate test RDS**: Never run reconciler tests against production RDS
4. **Grace period validation**: Test that grace period is honored (create volume, wait <grace-period, verify not deleted)
5. **Kubernetes integration test**: Verify reconciler correctly lists PVs (not just volumes on RDS)

**Phase to address:** v0.9.0 Milestone 3 (E2E Testing)

---

## Known CSI Ecosystem Issues to Watch

### Kubelet Volume Reconstruction After Reboot

**Issue:** After node reboot, kubelet tries to reconstruct volume state. If CSI driver not registered yet, unmount fails and volumes leak.

**Mitigation:**
- Ensure node plugin starts before kubelet finishes reconstruction
- Make NodeUnstageVolume idempotent (returns success if already unstaged)
- Add startup probe with adequate initialDelaySeconds

**Source:** [Kubernetes Issue #72500](https://github.com/kubernetes/kubernetes/issues/72500)

---

### Graceful Node Shutdown Doesn't Wait for Volume Teardown

**Issue:** Node shutdown doesn't wait for CSI driver to complete NodeUnstageVolume, causing Zombie LUNs or corruption.

**Mitigation:**
- Increase sidecar timeout values (default 15s → 60s)
- Monitor for missed NodeUnstageVolume calls (reconciler should detect)
- Document that ungraceful shutdown may leave orphans

**Source:** [Kubernetes Issue #115148](https://github.com/kubernetes/kubernetes/issues/115148)

---

### CSI Driver Restart Loses Volume Manager State

**Issue:** When CSI Node plugin restarts, driver registration removed from CSINode. Volume manager can't dispatch NodeUnstageVolume.

**Mitigation:**
- Use StatefulSet or DaemonSet with proper lifecycle hooks
- Persist state to filesystem (e.g., NVMe connection tracking in /var/lib/rds-csi/)
- Reconcile on startup (scan /proc/mounts for orphaned mounts)

**Source:** [Kubernetes Issue #120268](https://github.com/kubernetes/kubernetes/issues/120268)

---

## Sources

### CSI Testing Framework
- [Kubernetes CSI Testing Guide](https://kubernetes.io/blog/2020/01/08/testing-of-csi-drivers/)
- [csi-test GitHub Repository](https://github.com/kubernetes-csi/csi-test)
- [CSI Sanity Test README](https://github.com/kubernetes-csi/csi-test/blob/master/pkg/sanity/README.md)
- [Kubernetes CSI Functional Testing Documentation](https://kubernetes-csi.github.io/docs/functional-testing.html)

### Real-World Driver Issues
- [AWS EBS CSI Driver Issues](https://github.com/kubernetes-sigs/aws-ebs-csi-driver)
- [NodeStage Idempotency Issue on Nitro Instances](https://github.com/kubernetes-sigs/aws-ebs-csi-driver/issues/1076)
- [Longhorn CSI Sanity Test Failures](https://github.com/longhorn/longhorn/issues/2076)
- [Volume Name Limitation Breaking Sanity Tests](https://github.com/longhorn/longhorn/issues/2270)
- [Democratic CSI GitHub Repository](https://github.com/democratic-csi/democratic-csi)

### CSI Spec and Compliance
- [Container Storage Interface Specification](https://github.com/container-storage-interface/spec/blob/master/spec.md)
- [Kubernetes CSI Developer Documentation](https://kubernetes-csi.github.io/docs/)

### Kubernetes Volume Management Issues
- [NodeUnstageVolume Missing During Driver Restart](https://github.com/kubernetes/kubernetes/issues/120268)
- [NodeStageVolume Called Before NodeUnstageVolume Completes](https://github.com/kubernetes/kubernetes/issues/121357)
- [Staging Directory Cleanup with Bind-Mounted Kubelet](https://github.com/kubernetes/kubernetes/issues/119752)
- [CSI Plugin Registration After Kubelet Restart](https://github.com/kubernetes/kubernetes/issues/72500)
- [Graceful Node Shutdown Volume Teardown](https://github.com/kubernetes/kubernetes/issues/115148)

### NVMe-Specific Issues
- [NVMe Disconnect Device Path Issue](https://github.com/linux-nvme/nvme-cli/issues/563)
- [NVMe-CLI Disconnect Controller vs Device](https://github.com/linux-nvme/nvme-cli/issues/499)
- [Rook Ceph CSI Common Issues](https://rook.io/docs/rook/v1.14/Troubleshooting/ceph-csi-common-issues/)

### Test Isolation and Cleanup
- [CSI Orphaned Volumes in VMware](https://github.com/kubernetes-sigs/vsphere-csi-driver/issues/251)
- [Kubernetes Orphaned Pod Volumes Investigation](https://github.com/kubernetes/kubernetes/issues/72346)
- [csi-test Mount Cleanup Failure](https://github.com/kubernetes-csi/csi-test/issues/196)

---

*Pitfalls research for: RDS CSI Driver v0.9.0 Testing Infrastructure*
*Researched: 2026-02-04*
*Confidence: MEDIUM-HIGH (based on official CSI documentation, real-world driver issues, and ecosystem discussions; some RDS-specific items are LOW confidence pending real hardware validation)*
