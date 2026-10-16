# Stack Research: v0.9.0 Testing Infrastructure

**Domain:** Kubernetes CSI Driver Testing
**Researched:** 2026-02-04
**Confidence:** HIGH

## Recommended Stack

### Core Testing Technologies

| Technology | Version | Purpose | Why Recommended |
|------------|---------|---------|-----------------|
| csi-test/csi-sanity | v5.4.0 | CSI spec compliance testing | Official Kubernetes-CSI testing framework; validates all CSI gRPC methods against spec; used by all production CSI drivers (hostpath, NFS, AWS EBS, vSphere) |
| Ginkgo | v2.28.1 | BDD test framework | Required by csi-test sanity framework; provides expressive test specs and test organization; standard in Kubernetes ecosystem |
| Gomega | v1.36.2 | Matcher/assertion library | Official companion to Ginkgo; provides expressive assertions like `Expect(x).To(Equal(y))`; more readable than testify for complex assertions |
| testify | v1.11.1 (existing) | Unit test assertions | Already in use; excellent for table-driven tests; `assert` and `require` packages provide clean test code; keep for existing unit tests |
| golang.org/x/crypto/ssh | v0.41.0 (existing) | Mock SSH server | Standard library provides `ssh/test` package for creating test SSH servers; already a dependency; zero additional install |

### Supporting Libraries

| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| k8s.io/client-go/kubernetes/fake | v0.28.0 (existing) | Fake Kubernetes API | Already in use; E2E tests that need PV/PVC/StorageClass operations without real cluster |
| github.com/stretchr/testify/mock | v1.11.1 (existing) | Mock generation | Creating mocks for interfaces (RDSClient, Mounter); already in use in test/mock/rds_server.go |
| github.com/stretchr/testify/suite | v1.11.1 (existing) | Test suite organization | Optional; for E2E tests needing setup/teardown hooks; not required for simple tests |

### Development Tools

| Tool | Purpose | Notes |
|------|---------|-------|
| golangci-lint | Static analysis and linting | Already configured in project; run via `make lint`; includes coverage checks |
| go tool cover | Coverage analysis and reporting | Built-in; use `make test-coverage` to generate HTML reports; current: 65% overall |
| Docker Compose | Integration test environment | For running mock RDS server + CSI driver in isolated environment; see test/docker/ |

## Installation

```bash
# Core testing frameworks (add to go.mod)
go get github.com/kubernetes-csi/csi-test/v5@v5.4.0
go get github.com/onsi/ginkgo/v2@v2.28.1
go get github.com/onsi/gomega@v1.36.2

# Already installed (existing dependencies)
# github.com/stretchr/testify v1.11.1
# golang.org/x/crypto v0.41.0
# k8s.io/client-go v0.28.0

# Development tools (optional, if not using Nix)
go install github.com/onsi/ginkgo/v2/ginkgo@v2.28.1
```

## Alternatives Considered

| Recommended | Alternative | When to Use Alternative |
|-------------|-------------|-------------------------|
| csi-test/csi-sanity | Manual gRPC testing | Never; sanity tests are CSI spec requirement; all production drivers use csi-sanity |
| Ginkgo/Gomega | testify only | Small projects without BDD needs; however, csi-sanity requires Ginkgo, so must install anyway |
| golang.org/x/crypto/ssh | gliderlabs/ssh | If need more SSH server features (shell, SFTP); for mock RouterOS CLI, stdlib is sufficient |
| Mock RDS (test/mock/rds_server.go) | Real RDS in CI | If CI has access to dedicated RDS hardware; not practical for GitHub Actions/external CI |
| Fake Kubernetes client | Real cluster in tests | E2E tests with full Kubernetes; expensive and slow; use for final validation only |

## What NOT to Use

| Avoid | Why | Use Instead |
|-------|-----|-------------|
| go.uber.org/mock (gomock) | Renamed from golang/mock; confusing with testify/mock; extra dependency | testify/mock (already in use) or manual interface mocks |
| CSI mock driver (csi-test/mock) | Generic in-memory driver; not RDS-specific; doesn't test SSH/NVMe/TCP | Custom mock RDS server (test/mock/rds_server.go) |
| Kubernetes E2E framework (import k8s.io/kubernetes/test/e2e) | Massive dependency (100MB+); requires real cluster; slow | csi-sanity + lightweight E2E with fake client |
| Old csi-test versions (v4, v3) | Outdated; v5 supports CSI spec v1.10.0 | csi-test/v5 v5.4.0 |
| Manual grpc.Dial for testing | Reinvents csi-sanity; doesn't validate CSI spec; error-prone | Use csi-sanity which connects via Unix socket |

## Stack Patterns by Testing Type

### Pattern 1: Unit Tests (pkg/*)
**Existing pattern - KEEP:**
- Framework: Go `testing` package + testify/assert
- Mocking: Manual mocks implementing interfaces (mockMounter, mockRDSClient)
- Assertions: `assert.Equal`, `assert.NoError`, `require.NotNil`
- Structure: Table-driven tests with `t.Run()`

**Why:** Simple, fast, no external dependencies, works well for 148 existing tests

### Pattern 2: CSI Sanity Tests (test/sanity/)
**NEW - IMPLEMENT:**
- Framework: Ginkgo + Gomega (required by csi-sanity)
- Driver: RDS CSI driver (real implementation)
- Backend: Mock RDS server (test/mock/rds_server.go) OR real RDS (if available)
- Invocation: `csi-sanity --csi.endpoint=/var/run/csi/csi.sock`

**Why:** Validates CSI spec compliance; required for production readiness; catches protocol errors

### Pattern 3: Integration Tests (test/integration/)
**Existing pattern - ENHANCE:**
- Framework: Go `testing` + testify
- Environment: Mock RDS server + real SSH client + real NVMe logic
- Scope: Full volume lifecycle (create → stage → publish → unstage → delete)
- Assertions: testify/assert for clear error messages

**Why:** Tests real RDS integration without hardware; validates SSH command parsing and NVMe operations

### Pattern 4: E2E Tests (test/e2e/)
**NEW - IMPLEMENT:**
- Framework: Ginkgo + Gomega OR Go testing (both viable)
- Environment: Kubernetes with fake client OR kind cluster
- Scope: PVC creation → Pod mounting → Data persistence → Cleanup
- Storage: Mock RDS for CI, real RDS for manual testing

**Why:** Validates full Kubernetes workflow; tests CSI sidecars (provisioner, attacher, node-driver-registrar)

## Mock Infrastructure Patterns

### Pattern A: Mock RDS Server (CURRENT - ENHANCE)
**Implementation:** `test/mock/rds_server.go`
- Uses `golang.org/x/crypto/ssh` to create SSH server
- Simulates RouterOS CLI (`/disk add`, `/disk remove`, `/disk print`)
- Stores volumes in memory map
- Responds with RouterOS-style output

**When to use:** Unit tests, integration tests, CSI sanity tests, CI/CD

**Enhancements needed for v0.9.0:**
- Add support for `/file print` (capacity queries)
- Simulate command errors (disk full, invalid slot)
- Add delay/timeout simulation for reliability testing
- Support concurrent SSH connections

### Pattern B: Real RDS (OPTIONAL)
**Setup:** Export `RDS_ADDRESS`, `RDS_SSH_KEY` environment variables
**When to use:** Manual validation before releases; hardware validation

**Trade-off:** Requires physical hardware; slower than mock; not suitable for CI

### Pattern C: Mock NVMe Subsystem (FUTURE)
**Not implemented yet**
**Would need:** Simulate `/sys/class/nvme/` filesystem for device resolution
**Complexity:** High; requires extensive sysfs mocking
**Priority:** LOW; current tests use real NVMe logic with mocked exec commands

## Version Compatibility

| Package | Compatible With | Notes |
|---------|-----------------|-------|
| csi-test/v5 v5.4.0 | CSI spec v1.10.0 | RDS driver uses v1.10.0; fully compatible |
| Ginkgo v2.28.1 | Go 1.20+ | RDS driver uses Go 1.24; fully compatible |
| Gomega v1.36.2 | Ginkgo v2.x | Use matching major version |
| testify v1.11.1 | All Go versions | Already in use; no conflicts with Ginkgo |
| csi-test/v5 | Ginkgo v2.x | csi-sanity uses Ginkgo internally; must use v2 |

**Critical compatibility note:** csi-test v5 requires Ginkgo v2. Do NOT use Ginkgo v1 (deprecated).

## Gap Analysis Methodology

### Tool 1: go tool cover (CURRENT)
**Purpose:** Line coverage percentage per package
**Usage:**
```bash
make test-coverage          # Generates coverage.html
go tool cover -func=coverage.out | grep total
```

**Strengths:**
- Built-in; no installation
- Fast; runs with tests
- HTML visualization shows uncovered lines

**Weaknesses:**
- Doesn't identify WHAT scenarios are missing
- Line coverage ≠ behavior coverage
- No CSI spec coverage mapping

### Tool 2: csi-sanity (NEW - RECOMMENDED)
**Purpose:** CSI specification compliance testing
**Usage:**
```bash
make test-sanity-mock       # Uses mock RDS
make test-sanity-real       # Uses real RDS (requires RDS_ADDRESS)
```

**What it tests:**
- All CSI gRPC methods (CreateVolume, DeleteVolume, NodeStageVolume, etc.)
- Error handling (missing parameters, invalid volume IDs, etc.)
- Idempotency (calling same method twice)
- Capability validation

**Gap detection:**
- If test fails → implementation doesn't conform to CSI spec
- If test passes → method works according to spec
- Reports: Pass/fail with specific gRPC error codes

### Tool 3: Manual CSI Capability Matrix (NEW - CREATE)
**Purpose:** Document what CSI features are implemented vs. optional
**Format:** Markdown table in `docs/CSI-CAPABILITIES.md`

Example:
| CSI Method | Status | Tested | Notes |
|------------|--------|--------|-------|
| CreateVolume | IMPLEMENTED | ✅ Sanity + Unit | Supports block and mount volumes |
| VolumeContentSource | NOT IMPLEMENTED | N/A | Cloning not supported (RDS limitation) |
| ControllerPublishVolume | NOT IMPLEMENTED | N/A | Not needed (NVMe/TCP is node-local) |

**Gap detection:** Visual audit of unimplemented methods

### Tool 4: Coverage Diff (RECOMMENDED ADDITION)
**Purpose:** Prevent coverage regressions
**Implementation:**
```bash
# In CI
go test -coverprofile=new.out ./pkg/...
go tool cover -func=new.out | grep total > new-coverage.txt
# Compare with baseline (e.g., 65%)
# Fail if coverage drops below threshold
```

**Gap detection:** Alerts when new code isn't tested

### Tool 5: Test Coverage by Scenario (MANUAL)
**Purpose:** Identify missing error scenarios
**Method:** Code review checklist per CSI method

Example for `NodeStageVolume`:
- [ ] Happy path (mount volume)
- [ ] Happy path (block volume)
- [ ] Volume already staged (idempotency)
- [ ] Invalid volume ID
- [ ] NVMe connect fails
- [ ] Filesystem format fails
- [ ] Mount fails
- [x] Staging path doesn't exist (COVERED)

**Gap detection:** Unchecked boxes = missing tests

## Integration with Existing Infrastructure

### Makefile Targets (EXTEND)

Current targets to keep:
```makefile
test                 # Go unit tests with testify
test-coverage        # Coverage report (keep)
test-integration     # Integration tests with mock RDS (keep)
lint                 # golangci-lint (keep)
verify               # fmt + vet + lint + test (keep)
```

New targets to add:
```makefile
test-sanity          # Run csi-sanity (auto-detect mock vs real RDS)
test-sanity-mock     # Force mock RDS
test-sanity-real     # Force real RDS (requires RDS_ADDRESS env var)
test-e2e             # End-to-end tests with fake Kubernetes
test-all             # Run all tests (unit + integration + sanity + e2e)
coverage-check       # Fail if coverage < 70%
```

### Go Module Changes

Add to `go.mod`:
```go
require (
    github.com/kubernetes-csi/csi-test/v5 v5.4.0
    github.com/onsi/ginkgo/v2 v2.28.1
    github.com/onsi/gomega v1.36.2
)
```

No removals needed; testify and existing dependencies remain.

### Test Directory Structure (AFTER v0.9.0)

```
test/
├── integration/          # Existing; mock RDS + real driver
│   ├── controller_integration_test.go
│   └── hardware_integration_test.go
├── mock/                 # Existing; mock RDS server
│   └── rds_server.go     # Enhance for v0.9.0
├── sanity/               # NEW: CSI sanity wrapper
│   ├── sanity_test.go    # Ginkgo test suite
│   └── sanity.sh         # Helper script (optional)
├── e2e/                  # NEW: End-to-end tests
│   ├── e2e_suite_test.go # Ginkgo suite setup
│   └── volume_lifecycle_test.go
└── docker/               # Existing; Docker Compose environment
    └── docker-compose.yml
```

### CI Integration (GITHUB ACTIONS EXAMPLE)

```yaml
# .github/workflows/test.yml
jobs:
  unit-tests:
    steps:
      - run: make test
      - run: make coverage-check

  integration-tests:
    steps:
      - run: make test-integration

  sanity-tests:
    steps:
      - run: make test-sanity-mock  # Uses mock RDS

  lint:
    steps:
      - run: make lint
```

## Sources

**HIGH Confidence:**
- [kubernetes-csi/csi-test](https://github.com/kubernetes-csi/csi-test) — Official CSI testing framework; v5.4.0 release notes verified
- [csi-test/pkg/sanity README](https://github.com/kubernetes-csi/csi-test/blob/master/pkg/sanity/README.md) — Sanity test usage and requirements
- [Ginkgo v2.28.1 pkg.go.dev](https://pkg.go.dev/github.com/onsi/ginkgo/v2) — Version and Go compatibility verified (Jan 30, 2026 release)
- [Gomega pkg.go.dev](https://pkg.go.dev/github.com/onsi/gomega) — Matcher library documentation
- [testify v1.11.1 pkg.go.dev](https://pkg.go.dev/github.com/stretchr/testify) — Existing dependency; version confirmed
- [golang.org/x/crypto/ssh/test](https://pkg.go.dev/golang.org/x/crypto/ssh/test) — Mock SSH server package (Jan 12, 2026 update)
- [Kubernetes CSI Developer Docs - Functional Testing](https://kubernetes-csi.github.io/docs/functional-testing.html) — E2E testing approaches and TestDriver interface
- [csi-driver-host-path](https://github.com/kubernetes-csi/csi-driver-host-path) — Reference CSI driver; testing patterns studied
- [csi-driver-nfs](https://github.com/kubernetes-csi/csi-driver-nfs) — Production CSI driver; E2E test examples

**MEDIUM Confidence:**
- [Kubernetes Blog: Testing of CSI drivers](https://kubernetes.io/blog/2020/01/08/testing-of-csi-drivers/) — General patterns (2020 article; frameworks updated since)
- [SSH Testing in Go (Medium)](https://medium.com/@metarsit/ssh-is-fun-till-you-need-to-unit-test-it-in-go-f3b3303974ab) — Community patterns for SSH mocking
- [Go Testing Libraries Overview](https://softwarepatternslexicon.com/go/tools-and-libraries-for-go-design-patterns/testing-libraries/) — Testify, GoMock, Ginkgo comparison

**Project Context (HIGH Confidence):**
- `/path/to/rds-csi/.planning/codebase/TESTING.md` — Existing test patterns; 65% coverage baseline; testify usage confirmed
- `/path/to/rds-csi/go.mod` — Current dependencies verified (Go 1.24, testify v1.11.1, crypto v0.41.0)
- `/path/to/rds-csi/test/mock/rds_server.go` — Existing mock RDS implementation using golang.org/x/crypto/ssh

---
*Stack research for: Kubernetes CSI Driver Testing (v0.9.0 milestone)*
*Researched: 2026-02-04*
