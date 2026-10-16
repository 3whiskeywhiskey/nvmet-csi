# Research Summary: v0.9.0 Production Testing Infrastructure

**Project:** RDS CSI Driver v0.9.0 — Production Readiness & Test Maturity
**Domain:** Kubernetes CSI Driver Testing Infrastructure
**Researched:** 2026-02-04
**Confidence:** HIGH

## Executive Summary

To achieve production readiness, the RDS CSI driver must implement comprehensive testing infrastructure that validates CSI spec compliance and enables continuous validation. Expert CSI driver teams use a layered testing pyramid: unit tests (fast, isolated), integration tests (driver + mock storage), CSI sanity tests (spec compliance), and E2E tests (real cluster validation). The RDS driver has strong foundations (148 unit tests, 65% coverage, working mock RDS server) but lacks automated sanity and E2E testing — the critical validation layers required before v1.0 release.

The recommended approach uses official Kubernetes CSI tooling: csi-test/csi-sanity (v5.4.0) for spec compliance validation with Ginkgo/Gomega test framework, enhanced mock RDS server with realistic timing and error injection, and Docker Compose orchestration for reproducible CI environments. The existing testify-based unit tests remain unchanged; new testing layers supplement rather than replace current infrastructure.

The primary risk is mock-reality divergence: tests passing against mock RDS but failing against real hardware due to timing differences (mock: instant, real: 200ms SSH + 3s disk allocation), RouterOS version variations (CLI output format changes between 7.1/7.10/7.16), and NVMe-oF device timing (2-30s for device enumeration). Mitigation requires weekly hardware validation runs, recording real RDS interactions for mock development, and adaptive timeouts with NQN-based device discovery.

## Key Findings

### Recommended Stack

CSI driver testing requires official Kubernetes CSI frameworks layered on existing Go testing infrastructure. The stack builds on proven patterns from production drivers (AWS EBS CSI, Longhorn, democratic-csi).

**Core technologies:**
- **csi-test/csi-sanity v5.4.0**: Official CSI spec compliance testing — mandatory for validating all gRPC methods against CSI v1.10.0 spec; used by all production drivers; provides automated idempotency and error code validation
- **Ginkgo v2.28.1 + Gomega v1.36.2**: BDD test framework — required by csi-sanity; provides expressive test organization and matchers; standard in Kubernetes ecosystem
- **testify v1.11.1 (existing)**: Unit test assertions — already in use with 148 tests; keep for existing unit tests; excellent for table-driven tests and clear error messages
- **golang.org/x/crypto/ssh (existing)**: Mock SSH server — standard library provides test SSH server; already used in test/mock/rds_server.go; zero additional dependencies
- **Docker Compose**: CI test orchestration — orchestrates mock RDS + driver + sanity tests for reproducible CI environment

**Stack additions needed:**
```bash
go get github.com/kubernetes-csi/csi-test/v5@v5.4.0
go get github.com/onsi/ginkgo/v2@v2.28.1
go get github.com/onsi/gomega@v1.36.2
```

### Expected Features

Based on peer comparison with production CSI drivers, the RDS driver meets table stakes expectations but has testing maturity gaps.

**Must have (table stakes):**
- CSI sanity tests — validates spec compliance (currently MISSING, blocks v1.0)
- Unit tests with 70%+ coverage — validates logic paths (currently 65%, needs 5% increase)
- Integration tests with mock backend — validates driver + RDS interaction (EXISTS but needs enhancement)
- Basic E2E validation — validates real cluster workflows (manual only, needs automation)
- Idempotency validation — repeated calls return correct results (manual only, sanity tests will automate)

**Should have (competitive):**
- Automated E2E test suite — continuous validation of Kubernetes integration (MISSING, high priority for v0.9.0)
- Chaos testing — network partition, node failure resilience (MISSING, defer to v1.0)
- Performance benchmarks — latency, throughput, IOPS baselines (ad-hoc only, defer to v1.0)
- Load testing — concurrent volume operations (MISSING, defer to v1.0)
- Hardware validation tests — real RDS testing in CI (MISSING, weekly manual runs acceptable)

**Defer (v2+):**
- Soak testing — 24-hour stability runs
- Upgrade testing — driver version migration validation
- Multi-cluster scenarios — cross-cluster volume access
- Snapshot testing — once CREATE_DELETE_SNAPSHOT capability implemented

### Architecture Approach

CSI driver testing follows a pyramid architecture with four distinct layers, each testing different integration levels.

**Major components:**

1. **Unit Tests (pkg/\*\_test.go)** — Test individual functions with mocks; use testify assertions and command-aware mocking; 148 existing tests provide strong foundation; co-located with source files for fast edit-test cycles

2. **Mock RDS Server (test/mock/rds_server.go)** — In-process SSH server simulating RouterOS CLI; parses /disk commands, maintains state, returns RouterOS-formatted output; reused across integration and sanity tests; needs enhancements for /file print, error injection, timing simulation

3. **Integration Tests (test/integration/)** — Test driver + mock RDS interaction via real gRPC calls; validates full volume lifecycle without Kubernetes; existing controller_integration_test.go validates CreateVolume/DeleteVolume; needs NodeStageVolume/NodePublishVolume tests

4. **CSI Sanity Tests (test/sanity/)** — Official kubernetes-csi/csi-test framework validates spec compliance via unix socket; tests Identity + Controller services (Node tests require hardware); currently has shell script wrapper, needs CI automation

5. **E2E Tests (test/e2e/)** — Validates real Kubernetes cluster integration; currently manual validation plans (HARDWARE_VALIDATION.md); needs automated Ginkgo test suite using fake Kubernetes client or kind cluster

6. **Docker Compose (docker-compose.test.yml)** — Orchestrates mock RDS + driver + sanity tests for reproducible CI; currently exists but needs reliability validation

### Critical Pitfalls

Research identified six critical pitfalls from production CSI driver experiences:

1. **Idempotency violations under real conditions** — CSI sanity passes but kubelet retries fail in production; NodeStageVolume called twice causes "already mounted" errors; device path comparisons fail due to symlink vs canonical path mismatches (e.g., /dev/xvdcf vs /dev/nvme1n1). **Prevention:** Test with kubelet retries, normalize paths with filepath.EvalSymlinks(), check if operation already complete before starting, add retry simulation tests.

2. **Mock storage backend diverges from real hardware** — Tests pass against mock RDS but fail against real hardware; mock returns instant responses, real SSH has 200ms latency exposing timeout bugs; mock always succeeds, real RDS returns "not enough space" or version incompatibilities. **Prevention:** Record real RDS interactions for mock development, fuzz mock responses with delays and errors, test against real hardware weekly, fail-fast on unexpected responses.

3. **Test cleanup failures create cascading issues** — Test crashes leave orphaned volumes on RDS; next run fails because volume IDs collide or disk full; tests become flaky due to state accumulation. **Prevention:** Use unique volume ID prefix per test run (test-\<timestamp\>-pvc-\<uuid\>), cleanup job before tests, deferred cleanup in Go, post-test orphan scan.

4. **Hardware-specific device timing not captured in tests** — NVMe device appears instantly in tests but takes 2-30 seconds on real hardware; test uses 1-second timeout, production needs 30 seconds; device path format differs (nvmeXnY vs nvmeXcYnZ for NVMe-oF). **Prevention:** Adaptive timeouts (5s → 60s max), device discovery by NQN not path (match /sys/class/nvme/\*/subsysnqn), test with real NVMe-oF target, use glob patterns not hardcoded paths.

5. **CSI sanity tests skip critical negative cases** — csi-sanity focuses on happy paths, doesn't validate all error scenarios; missing tests for "volume already exists with different size", "delete non-existent volume", concurrent operations. **Prevention:** Supplement csi-sanity with custom negative test suite, validate gRPC error codes match spec, add concurrency tests.

6. **Volume naming constraints break sanity tests** — csi-sanity generates long volume names that violate DNS-1123 label limits (63 chars); tests fail with cryptic validation errors. **Prevention:** Hash long names to fixed-length IDs (pvc-\<uuid\>), validate volume ID in CreateVolume, configure sanity test name prefix.

**RDS-specific pitfalls:**
- **SSH-based control plane:** Mock must parse /disk commands and reject shell metacharacters to catch injection vulnerabilities
- **NVMe-oF disconnect cleanup:** nvme disconnect fails silently when device in use; check mounts before disconnect, retry with backoff
- **Dual-IP architecture:** Controller uses SSH address (192.0.2.10), nodes use NVMe address (198.51.100.20); tests must use dual-IP config
- **RouterOS version compatibility:** CLI output varies between 7.1/7.10/7.16; parser must be order-independent, handle missing fields
- **Orphan reconciler testing:** Must test with dry-run first, use test volume prefix, never run against production RDS

## Implications for Roadmap

Based on research, v0.9.0 should focus on automated testing infrastructure to validate production readiness. Testing infrastructure is blocking for v1.0 confidence; without automated CSI sanity tests, spec compliance is unproven despite high confidence in implementation.

### Suggested Phases

#### Phase 1: CSI Sanity Tests Integration
**Rationale:** CSI sanity tests are the gold standard for spec compliance validation; highest ROI; must pass before v1.0 release; existing test/sanity/run-sanity-tests.sh needs CI automation
**Delivers:** Automated sanity tests in CI, passing all spec compliance checks, idempotency validation, error code validation
**Addresses:** CSI sanity tests (must-have feature from FEATURES.md), spec compliance validation gap
**Avoids:** Idempotency violations pitfall (test retries, canonical paths), volume naming pitfall (validate IDs early)
**Stack:** csi-test/csi-sanity v5.4.0, Ginkgo v2.28.1, Gomega v1.36.2
**Effort:** LOW (1-2 days) — framework exists, needs CI integration
**Priority:** P0 (blocking v1.0)

#### Phase 2: Mock RDS Enhancements
**Rationale:** Mock RDS must match real hardware behavior to prevent false-passing tests; current mock is basic (parses /disk commands) but needs error injection, timing simulation, /file print support
**Delivers:** Enhanced mock with realistic latency (200ms SSH, 3s disk create), error injection (disk full, SSH timeout), RouterOS version variations
**Addresses:** Mock-reality divergence pitfall, integration test reliability
**Avoids:** Mock divergence pitfall (record real interactions, fuzz responses), SSH pitfall (command validation, injection protection)
**Stack:** golang.org/x/crypto/ssh (existing), enhanced command parser
**Effort:** MEDIUM (2-3 days) — significant mock logic additions
**Priority:** P1 (enables better integration tests)

#### Phase 3: Automated E2E Test Suite
**Rationale:** E2E tests validate full Kubernetes stack; currently manual only; automated E2E enables catching regressions in CI
**Delivers:** Ginkgo-based E2E test suite, basic volume lifecycle tests (create → mount → write → delete), cleanup validation
**Addresses:** Automated E2E suite (must-have feature from FEATURES.md)
**Avoids:** Test cleanup pitfall (pre/post-test reconciliation), dual-IP pitfall (test with production config)
**Stack:** Ginkgo v2.28.1, fake Kubernetes client OR kind cluster
**Effort:** MEDIUM (1 week) — test framework setup, fixture creation
**Priority:** P0 (blocking v1.0)

#### Phase 4: Coverage Improvements
**Rationale:** Current coverage is 65%, target is 70%; gap analysis reveals missing negative test cases and error path coverage
**Delivers:** 70%+ code coverage, negative test cases added, error path validation
**Addresses:** Unit test coverage gap (65% → 70%), CSI sanity negative cases pitfall
**Avoids:** Happy path only testing pitfall (explicit error tests)
**Stack:** testify v1.11.1 (existing), go tool cover
**Effort:** LOW (1-2 days) — add missing test cases
**Priority:** P1 (quality improvement)

#### Phase 5: Hardware Validation Plan
**Rationale:** Real hardware testing catches timing issues and RouterOS version compatibility problems that mocks miss; weekly validation runs are acceptable (don't need full CI automation)
**Delivers:** Hardware validation runbook, weekly test schedule, real RDS test suite
**Addresses:** Mock-reality divergence pitfall, hardware timing pitfall
**Avoids:** Works-on-my-machine anti-pattern, device timing pitfall
**Stack:** Real RDS at 192.0.2.10, real cluster, manual test execution
**Effort:** LOW (1 day) — documentation + test plan
**Priority:** P2 (validation, not blocking)

### Phase Ordering Rationale

- **Sanity first:** Validates CSI spec compliance immediately; highest confidence gain per effort; blocks v1.0 release
- **Mock enhancement second:** Enables reliable integration tests; reduces mock-reality divergence before E2E tests depend on it
- **E2E third:** Requires stable mock for CI mode; validates full Kubernetes integration; second blocker for v1.0
- **Coverage fourth:** Once test infrastructure stable, fill gaps identified in sanity/E2E testing
- **Hardware validation last:** Manual validation acceptable; doesn't block CI; catches edge cases missed by automated tests

**Dependency chain:**
```
Sanity Tests → E2E Tests (both validate different layers)
     ↓              ↓
Mock Enhancements ─┘ (enables reliable CI for both)
     ↓
Coverage Improvements (uses findings from sanity/E2E)
     ↓
Hardware Validation (final verification)
```

### Research Flags

**Phases needing deeper research during planning:**
- **None** — Testing infrastructure is well-documented with established patterns; official kubernetes-csi documentation is comprehensive; production driver examples are plentiful

**Phases with standard patterns (skip research-phase):**
- **All phases** — CSI testing is mature domain with official frameworks; implementation details clear from AWS EBS CSI, Longhorn, democratic-csi references; no novel patterns required

**Implementation notes:**
- Phase 1: Follow csi-test/csi-sanity README verbatim; well-trodden path
- Phase 2: Reference test/mock/rds_server.go existing implementation; incremental improvements
- Phase 3: Use AWS EBS CSI test/e2e/ structure as template
- Phase 4: Standard Go testing practice; go tool cover guides
- Phase 5: Leverage existing HARDWARE_VALIDATION.md plan

## Confidence Assessment

| Area | Confidence | Notes |
|------|------------|-------|
| Stack | **HIGH** | Official kubernetes-csi/csi-test framework; verified versions (v5.4.0, Ginkgo v2.28.1); all dependencies checked against existing go.mod |
| Features | **HIGH** | Gap analysis vs AWS EBS CSI, Longhorn, Ceph RBD; CSI spec requirements clear; table stakes well-defined by community |
| Architecture | **HIGH** | Standard CSI testing pyramid (unit → integration → sanity → E2E); existing RDS driver structure follows best practices; test/mock/rds_server.go validates approach |
| Pitfalls | **MEDIUM-HIGH** | Well-documented in production driver issues (AWS EBS #1076, Longhorn #2076, K8s #120268); RDS-specific pitfalls are inferred from architecture, need real hardware validation |

**Overall confidence:** HIGH

### Gaps to Address

**RDS-specific gaps requiring validation during implementation:**

1. **RouterOS version compatibility:** Research found CLI variations between 7.1/7.10/7.16 but didn't test parsers against all versions. **Mitigation:** Test against production RDS (RouterOS 7.16) in Phase 5, record output for mock in Phase 2.

2. **NVMe-oF device timing on NixOS:** Research cites 2-30s device enumeration timing but RDS driver uses NixOS workers (diskless network boot). Kernel NVMe drivers may have different timing. **Mitigation:** Hardware validation in Phase 5 measures actual timing, adjust timeouts if needed.

3. **Dual-IP architecture testing:** Research identified dual-IP pattern (SSH vs NVMe addresses) but existing tests don't validate VolumeContext passing. **Mitigation:** Phase 3 E2E tests must validate nvmeAddress field in VolumeContext.

4. **Mock RDS state complexity:** Current mock handles basic /disk commands but comprehensive error injection (concurrent operations, disk full, SSH failures) needs design validation. **Mitigation:** Phase 2 starts simple (delay injection), iterates based on sanity test failures.

5. **CSI sanity Node tests:** Research shows Node tests require real hardware (NVMe operations) but unclear if mock can simulate enough for basic validation. **Mitigation:** Phase 1 skips Node tests initially; Phase 2 explores NVMe simulation feasibility.

**Process gaps:**

- **CI reliability validation:** docker-compose.test.yml exists but needs full CI run to validate reliability. **Action:** Phase 1 includes CI integration testing.
- **Test execution time:** Unknown if full sanity suite fits within CI timeout (typically 2hr). **Action:** Phase 1 measures, uses parallel execution if needed (-ginkgo.p flag).
- **Coverage baseline accuracy:** Claimed 65% coverage but test count discrepancy (148 vs 1015 in go test output) suggests subtests. **Action:** Phase 4 re-establishes accurate baseline.

## Sources

### Primary (HIGH confidence)

**CSI Specification and Testing:**
- [kubernetes-csi/csi-test](https://github.com/kubernetes-csi/csi-test) — Official CSI testing framework; v5.4.0 release notes verified
- [csi-test/pkg/sanity README](https://github.com/kubernetes-csi/csi-test/blob/master/pkg/sanity/README.md) — Sanity test usage and requirements
- [Kubernetes CSI Developer Docs - Functional Testing](https://kubernetes-csi.github.io/docs/functional-testing.html) — E2E testing approaches and TestDriver interface
- [Testing of CSI drivers (Kubernetes Blog)](https://kubernetes.io/blog/2020/01/08/testing-of-csi-drivers/) — Testing layers and best practices
- [Container Storage Interface Specification](https://github.com/container-storage-interface/spec) — CSI spec v1.10.0

**Technology Documentation:**
- [Ginkgo v2.28.1 pkg.go.dev](https://pkg.go.dev/github.com/onsi/ginkgo/v2) — Version and Go compatibility verified (Jan 30, 2026 release)
- [Gomega pkg.go.dev](https://pkg.go.dev/github.com/onsi/gomega) — Matcher library documentation
- [testify v1.11.1 pkg.go.dev](https://pkg.go.dev/github.com/stretchr/testify) — Existing dependency; version confirmed
- [golang.org/x/crypto/ssh/test](https://pkg.go.dev/golang.org/x/crypto/ssh/test) — Mock SSH server package (Jan 12, 2026 update)

**Production CSI Drivers:**
- [csi-driver-host-path](https://github.com/kubernetes-csi/csi-driver-host-path) — Reference CSI driver; testing patterns studied
- [AWS EBS CSI Driver](https://github.com/kubernetes-sigs/aws-ebs-csi-driver) — Production driver; test structure, E2E examples
- [Longhorn CSI Driver](https://github.com/longhorn/longhorn) — Production driver; sanity test integration patterns
- [democratic-csi (TrueNAS/ZFS)](https://github.com/democratic-csi/democratic-csi) — Similar storage backend (SSH-based control plane)
- [Ceph RBD CSI Driver](https://github.com/ceph/ceph-csi) — Enterprise driver; comprehensive test suite

### Secondary (MEDIUM confidence)

**Known Issues and Pitfalls:**
- [AWS EBS CSI NodeStage Idempotency Issue](https://github.com/kubernetes-sigs/aws-ebs-csi-driver/issues/1076) — Nitro instance device path mismatches
- [Longhorn Volume Name Limitation](https://github.com/longhorn/longhorn/issues/2270) — Sanity test failures due to naming constraints
- [Kubernetes Issue #120268](https://github.com/kubernetes/kubernetes/issues/120268) — CSI driver restart loses volume manager state
- [Kubernetes Issue #121357](https://github.com/kubernetes/kubernetes/issues/121357) — NodeStageVolume called before NodeUnstageVolume completes
- [NVMe Disconnect Device Path Issue](https://github.com/linux-nvme/nvme-cli/issues/563) — NVMe device vs controller disconnect

**Community Resources:**
- [SSH Testing in Go (Medium)](https://medium.com/@metarsit/ssh-is-fun-till-you-need-to-unit-test-it-in-go-f3b3303974ab) — Community patterns for SSH mocking
- [Chaos Mesh Platform](https://chaos-mesh.org/) — Chaos testing framework reference (for future v1.0 chaos testing)

### Project Context (HIGH confidence)

- `.planning/codebase/TESTING.md` — Existing test patterns; 65% coverage baseline; testify usage confirmed
- `go.mod` — Current dependencies verified (Go 1.24, testify v1.11.1, crypto v0.41.0)
- `test/mock/rds_server.go` — Existing mock RDS implementation using golang.org/x/crypto/ssh
- `test/sanity/run-sanity-tests.sh` — Existing sanity test wrapper script
- `docker-compose.test.yml` — Existing Docker Compose test orchestration
- `CLAUDE.md` — Project architecture, volume lifecycle, testing strategy

---

## Ready for Requirements

SUMMARY.md complete. Research synthesis provides:
- Clear stack recommendations (csi-test v5.4.0 + Ginkgo v2.28.1)
- Prioritized feature gaps (sanity tests P0, E2E suite P0, coverage improvements P1)
- Architectural patterns (test pyramid with 5 layers)
- Phase structure suggestion (5 phases, sanity → mock → E2E → coverage → hardware)
- Risk mitigation strategies (mock-reality divergence, idempotency, timing)

Orchestrator can proceed to requirements definition with high confidence. All research documents committed together.

---
*Research completed: 2026-02-04*
*Ready for roadmap: yes*
