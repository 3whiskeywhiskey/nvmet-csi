# Testing Guide

Comprehensive guide to testing the nvmet-csi driver, including unit tests, integration tests, CSI sanity tests, and E2E tests.

## Overview

The nvmet-csi driver employs a multi-layered testing strategy:

1. **Unit Tests** - Test individual functions and packages in isolation
2. **Integration Tests** - Test driver + mock RDS interaction
3. **Sanity Tests** - Validate CSI spec compliance (Identity + Controller services)
4. **E2E Tests** - Validate full stack with in-process driver and mock RDS using Ginkgo v2

## Running Tests Locally

### Unit Tests

Run all unit tests with coverage:

```bash
make test
```

Run tests for a specific package:

```bash
go test -v ./pkg/driver/...
```

Generate coverage report:

```bash
make test-coverage
open coverage.html
```

### Integration Tests

Run integration tests with mock RDS:

```bash
make test-integration
```

These tests validate driver behavior with a simulated RouterOS CLI, covering:
- SSH connection handling
- Command execution and parsing
- Error handling and retries
- Volume lifecycle operations

### CSI Sanity Tests

Sanity tests validate CSI specification compliance using the official `csi-test` package.

#### With Mock RDS (Recommended for CI)

```bash
make test-sanity-mock
```

This runs the Go-based sanity test suite with an in-process mock RDS server. It validates Identity and Controller services against CSI spec v1.12.0.

#### With Real RDS Hardware

```bash
make test-sanity-real RDS_ADDRESS=198.51.100.20 RDS_SSH_KEY=~/.ssh/id_rsa
```

WARNING: This runs tests against real hardware and will create/delete actual volumes. Use a dedicated test RDS instance.

### E2E Tests

E2E (end-to-end) tests validate full driver behavior using the Ginkgo v2 framework with an in-process driver and mock RDS server.

**Run E2E tests:**

```bash
make test-e2e
```

**What E2E tests cover:**
- Volume lifecycle operations (create, delete, expand)
- Block volume support (raw block mode)
- Driver startup and shutdown
- In-process testing for fast iteration
- `resilience_test.go` - NVMe reconnect, RDS connection recovery, and node failure stale attachment cleanup

**Test location:** `test/e2e/`

**Key test suites:**
- `lifecycle_test.go` - Volume create, delete, and expansion workflows
- `block_volume_test.go` - Block mode volume operations
- `resilience_test.go` - Resilience regression (RESIL-01, RESIL-02, RESIL-03) with mock error injection

**E2E in CI:** E2E tests run in a dedicated CI job without requiring real hardware, using the mock RDS server for fast validation.

### Hardware Integration Tests

For testing against real RDS hardware, use the hardware integration test suite:

```bash
RDS_ADDRESS=192.0.2.10 RDS_USER=admin RDS_PRIVATE_KEY_PATH=~/.ssh/id_rsa go test -v ./test/integration/ -run TestHardwareIntegration
```

**Key points:**
- These tests are skipped by default (require `RDS_ADDRESS` env var)
- Safe to run: creates test volume, verifies operations, deletes volume
- Test file: `test/integration/hardware_integration_test.go`

For comprehensive manual validation procedures against real hardware, see [HARDWARE_VALIDATION.md](HARDWARE_VALIDATION.md).

### Resilience Regression Tests

Resilience regression tests validate driver resilience to connection failures and node failures using mock infrastructure, without requiring real hardware. These tests live in `test/e2e/resilience_test.go` and use a mock RDS server with error injection to simulate failure scenarios.

**Run resilience tests:**

```bash
go test -v ./test/e2e/... -ginkgo.v -ginkgo.focus="Resilience"
```

**What they cover:**

| Test ID | Scenario | What is validated |
|---------|----------|-------------------|
| RESIL-01 | SSH connection recovery | Controller reconnects to RDS after connection drop; queued volume operations succeed after reconnect |
| RESIL-02 | RDS unavailability handling | Volume operations return retriable errors during RDS downtime; driver recovers when RDS comes back |
| RESIL-03 | Stale attachment cleanup | Reconciler clears VolumeAttachment for deleted node; volume is reattachable after cleanup |

**Note on hardware-level validation:** The resilience regression tests use mock infrastructure with simulated failures. They cannot reproduce hardware-level behaviors such as:

- Actual NVMe/TCP kernel reconnection behavior (`ctrl_loss_tmo`, `reconnect_delay`)
- True RDS restart with Btrfs RAID6 data persistence verification
- Real node power-off scenarios (kernel crash, power loss)

For hardware-level resilience validation, use the manual test procedures documented in [HARDWARE_VALIDATION.md](HARDWARE_VALIDATION.md):
- **TC-09** — NVMe reconnect after network interruption (iptables block/restore)
- **TC-10** — RDS restart with volume preservation verification
- **TC-11** — Node failure stale VolumeAttachment cleanup

**Resilience behavior is regression-tested:** Changes to connection manager logic (`pkg/rds/connection_manager.go`) or attachment reconciler (`pkg/attachment/reconciler.go`) must pass the resilience regression tests before merging.

### Verification Suite

Run all code quality checks:

```bash
make verify
```

This runs:
- `go fmt` - Code formatting
- `go vet` - Static analysis
- `golangci-lint` - Comprehensive linting
- Unit tests
- Integration tests

## CSI Capability Matrix

This table documents which CSI capabilities are implemented and tested by the driver.

### Identity Service

| Capability | Implemented | Tested | Notes |
|------------|-------------|--------|-------|
| CONTROLLER_SERVICE | Yes | Yes (sanity) | Driver has controller component |
| VOLUME_ACCESSIBILITY_CONSTRAINTS | Yes | Yes (sanity) | Topology support for node scheduling |

### Controller Service

| Capability | Implemented | Tested | Notes |
|------------|-------------|--------|-------|
| CREATE_DELETE_VOLUME | Yes | Yes (sanity) | Core volume provisioning functionality |
| PUBLISH_UNPUBLISH_VOLUME | Yes | Yes (sanity) | Volume attachment tracking |
| GET_CAPACITY | Yes | Yes (sanity) | Returns RDS storage pool capacity |
| LIST_VOLUMES | Yes | Yes (sanity) | Enumerates CSI-managed volumes |
| EXPAND_VOLUME | Yes | Yes (sanity) | Online volume expansion support |
| CREATE_DELETE_SNAPSHOT | Yes | ✓ | Phase 26 (v0.10.0) |
| CLONE_VOLUME | No | Skipped | Not planned |
| GET_VOLUME | No | Skipped | Optional capability, not required |

### Node Service

Node service capabilities are implemented but require NVMe/TCP hardware for testing.

| Capability | Implemented | Tested | Notes |
|------------|-------------|--------|-------|
| STAGE_UNSTAGE_VOLUME | Yes | No (deferred) | Requires NVMe/TCP target connection |
| EXPAND_VOLUME | Yes | No (deferred) | Requires mounted filesystem |
| GET_VOLUME_STATS | Yes | No (deferred) | Requires mounted filesystem |
| VOLUME_CONDITION | Yes | No (deferred) | Requires active NVMe connection |

**Node service testing status:**
- Sanity tests skip Node service (no NVMe/TCP mock available)
- Node functionality tested manually with real hardware
- E2E tests with hardware environment planned for Phase 24

**Resilience testing status:**
- SSH connection recovery and RDS unavailability: regression-tested via `resilience_test.go` (RESIL-01, RESIL-02)
- Stale VolumeAttachment cleanup: regression-tested via `resilience_test.go` (RESIL-03)
- Hardware-level NVMe reconnect and RDS restart: manual validation via HARDWARE_VALIDATION.md TC-09, TC-10, TC-11

## Test Infrastructure

### Mock RDS Server

The mock RDS server (`test/mock/rds_server.go`) simulates RouterOS CLI behavior for testing without real hardware.

**Features:**
- SSH server on configurable port (default: 12222 for sanity tests, 2222 for integration tests)
- Simulates RouterOS CLI commands (`/disk add`, `/disk remove`, `/disk print`, `/file print`)
- Command history logging for debugging test failures
- Thread-safe state management

**Usage in tests:**

```go
mockRDS, err := mock.NewMockRDSServer(12222)
require.NoError(t, err)
mockRDS.Start()
defer mockRDS.Stop()

// Run tests against localhost:12222
```

### Mock RDS Server Configuration

The mock RDS server supports environment-based configuration for timing simulation and error injection, enabling various testing scenarios without modifying code.

#### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MOCK_RDS_REALISTIC_TIMING` | `false` | Enable realistic operation delays |
| `MOCK_RDS_SSH_LATENCY_MS` | `200` | Base SSH connection latency (ms) |
| `MOCK_RDS_SSH_LATENCY_JITTER_MS` | `50` | Latency jitter range +/- (ms) |
| `MOCK_RDS_DISK_ADD_DELAY_MS` | `500` | Disk add operation delay (ms) |
| `MOCK_RDS_DISK_REMOVE_DELAY_MS` | `300` | Disk remove operation delay (ms) |
| `MOCK_RDS_ERROR_MODE` | `none` | Error injection mode |
| `MOCK_RDS_ERROR_AFTER_N` | `0` | Fail after N operations (0 = immediate) |
| `MOCK_RDS_ENABLE_HISTORY` | `true` | Enable command history logging |
| `MOCK_RDS_HISTORY_DEPTH` | `100` | Max commands in history |
| `MOCK_RDS_ROUTEROS_VERSION` | `7.16` | RouterOS version to simulate |

#### Error Injection Modes

| Mode | Description | Error Message |
|------|-------------|---------------|
| `none` | No errors injected | - |
| `disk_full` | Simulate disk full condition | `failure: not enough space` |
| `ssh_timeout` | Simulate SSH connection timeout | (connection hangs) |
| `command_fail` | Simulate command execution failure | `failure: execution error` |

#### Usage Examples

**Run sanity tests with error injection:**
```bash
MOCK_RDS_ERROR_MODE=disk_full make test-sanity-mock
```

**Run tests with realistic timing:**
```bash
MOCK_RDS_REALISTIC_TIMING=true go test ./test/sanity/... -v
```

**Fail after 3rd operation (for idempotency testing):**
```bash
MOCK_RDS_ERROR_MODE=disk_full MOCK_RDS_ERROR_AFTER_N=3 make test-sanity-mock
```

**Test with realistic SSH latency (150-250ms):**
```bash
MOCK_RDS_REALISTIC_TIMING=true \
  MOCK_RDS_SSH_LATENCY_MS=200 \
  MOCK_RDS_SSH_LATENCY_JITTER_MS=50 \
  go test ./test/integration/... -v
```

#### Stress Testing

Run concurrent connection stress tests to validate MOCK-07 (concurrent SSH connections without state corruption):

```bash
# Run all stress tests
go test ./test/mock/... -run TestConcurrent -v

# Run with race detector
go test ./test/mock/... -run TestConcurrent -v -race

# Skip timing tests (faster CI)
go test ./test/mock/... -short -v
```

**Available stress tests:**
- `TestConcurrentConnections` - 50 parallel volume operations
- `TestConcurrentSameVolume` - Idempotency validation (10 goroutines, 1 volume)
- `TestConcurrentCreateDelete` - Race between create/delete operations
- `TestConcurrentMixedOperations` - Concurrent create/query/delete mix
- `TestConcurrentCommandHistory` - History tracking with concurrency

### Sanity Test Configuration

The sanity tests use the official `csi-test/v5/pkg/sanity` package with these settings:

- **Test volume size:** 10 GiB (realistic size validation)
- **Expand size:** 20 GiB (online expansion tests)
- **Idempotent count:** 2 (validates CreateVolume/DeleteVolume idempotency)
- **Target path:** `/tmp/csi-target` (unused for Controller-only tests)
- **Staging path:** `/tmp/csi-staging` (unused for Controller-only tests)

### In-Process Testing Pattern

The sanity tests use an in-process testing pattern:
1. Start mock RDS server as goroutine
2. Create driver instance with mock RDS config
3. Start driver gRPC server on Unix socket
4. Run csi-sanity tests against the socket
5. Clean up on test completion

This pattern provides:
- Faster startup (no subprocess overhead)
- Easier debugging (single process)
- Better CI integration (no cleanup issues)

## Debugging Test Failures

### Enable Verbose Logging

```bash
# Unit/integration tests
go test -v ./pkg/driver/...

# Sanity tests with detailed output
go test -v -race ./test/sanity/...
```

### Check Mock RDS Command History

The mock RDS server logs all executed commands. Enable logging in tests:

```go
mockRDS, _ := mock.NewMockRDSServer(12222)
mockRDS.Start()
defer func() {
    history := mockRDS.GetCommandHistory()
    for _, cmd := range history {
        t.Logf("RDS command: %s (success: %t)", cmd.Command, cmd.Success)
    }
    mockRDS.Stop()
}()
```

### CI Artifacts

GitHub Actions CI captures test logs as artifacts on failure:
- **sanity-test-logs** - Full sanity test output with timing and error details

To download artifacts:
1. Navigate to failed workflow run
2. Scroll to "Artifacts" section
3. Download `sanity-test-logs.zip`

## Troubleshooting

### Troubleshooting: Unit Test Failures

**Symptom:** "mock expectation not met"
- **Cause:** Mock setup doesn't match new function signature
- **Fix:** Check mock expectations match actual function calls in test
- **Debug:** `go test -v -run TestXxx ./pkg/driver/... 2>&1 | head -50`

**Symptom:** "context deadline exceeded"
- **Cause:** Test timeout too short for realistic mock timing
- **Fix:** Increase context timeout or check for deadlocks/infinite loops
- **Debug:** Enable verbose logging to see where test hangs

**Symptom:** "invalid IP address" or "invalid volume ID"
- **Cause:** Test uses invalid input (empty string, malformed format)
- **Fix:** Ensure test provides valid IPv4 addresses and properly formatted volume IDs
- **Debug:** Check test setup, verify input validation logic

### Troubleshooting: Integration Test Failures

**Symptom:** "connection refused on port 2222"
- **Cause:** Mock RDS server failed to start; port already in use
- **Fix:** Check for port conflicts, ensure no other processes using port 2222
- **Debug:** `lsof -i :2222` to check port usage

**Symptom:** "unexpected SSH command output"
- **Cause:** Mock RDS version mismatch or output format changed
- **Fix:** Check `MOCK_RDS_ROUTEROS_VERSION` matches expected version, update parsers
- **Debug:** `MOCK_RDS_ENABLE_HISTORY=true go test -v ./test/integration/...`

**Symptom:** "state inconsistency between tests"
- **Cause:** Tests sharing mock state without cleanup
- **Fix:** Ensure fresh mock per test or call `ClearCommandHistory()` between tests
- **Debug:** Check test setup/teardown, verify mock isolation

### Troubleshooting: Sanity Test Failures

**Symptom:** "CreateVolume idempotency check failed"
- **Cause:** Driver returning different volume ID for duplicate requests
- **Fix:** Check volume ID generation logic, ensure same name returns same ID
- **Debug:** `go test -v ./test/sanity/... -count=1 2>&1 | grep FAIL`

**Symptom:** "DeleteVolume returned error for non-existent volume"
- **Cause:** DeleteVolume not idempotent per CSI spec
- **Fix:** Return success for already-deleted volumes (CSI spec requires idempotency)
- **Debug:** Check DeleteVolume implementation handles VolumeNotFoundError

**Symptom:** "capability mismatch"
- **Cause:** Driver reports capability but doesn't implement it (or vice versa)
- **Fix:** Update GetCapabilities or implement missing RPC methods
- **Debug:** Compare driver capabilities with test expectations

### Troubleshooting: E2E Test Failures

**Symptom:** "Ginkgo timeout waiting for PVC"
- **Cause:** In-process driver startup slow or volume provisioning delayed
- **Fix:** Increase Eventually timeout in test, check driver initialization logs
- **Debug:** Add verbose Ginkgo output: `go test -v ./test/e2e/... -ginkgo.v`

**Symptom:** "volume not found after create"
- **Cause:** Race condition in mock or volume creation failed silently
- **Fix:** Check mock RDS state, verify CreateVolume logic and error handling
- **Debug:** Enable mock command history to trace volume operations

**Symptom:** "block volume test skipped"
- **Cause:** Block volume support not detected in driver capabilities
- **Fix:** Verify driver reports BLOCK_VOLUME capability in GetCapabilities
- **Debug:** Check driver initialization and capability registration

### Troubleshooting: Mock-Reality Divergence

The mock RDS server simulates RouterOS behavior but has inherent limitations that can lead to divergence from real hardware:

**Timing Differences:**
- **Mock:** Responds instantly to all commands
- **Real RDS:** Takes 10-30s for volume operations (disk creation, deletion)
- **Impact:** Tests may pass with mock but timeout with real hardware

**NVMe/TCP Simulation:**
- **Mock:** Does not simulate NVMe/TCP protocol or kernel device discovery
- **Real RDS:** NVMe/TCP connection takes 2-5s, kernel device enumeration adds latency
- **Impact:** Node service operations untestable with mock alone

**Capacity Enforcement:**
- **Mock:** Does not enforce disk capacity limits
- **Real RDS:** Will fail volume creation if storage pool is full
- **Impact:** Out-of-space scenarios require real hardware testing

**Command Output Format:**
- **Mock:** Simulates RouterOS 7.16 output format
- **Real RDS:** Output format may vary across RouterOS versions (7.1-7.16+)
- **Impact:** Parser may fail on different RouterOS versions

**Recommendation:** After making changes to volume operations or command parsing, validate against real hardware using [HARDWARE_VALIDATION.md](HARDWARE_VALIDATION.md) procedures. Mock tests provide fast feedback, but real hardware testing catches integration issues.

## Contributing Tests

### Unit Test Guidelines

- Place tests in `*_test.go` files next to the code under test
- Use `testify/require` for assertions: `require.NoError(t, err)`
- Use table-driven tests for multiple scenarios
- Mock external dependencies (SSH client, Kubernetes client)

**Example:**

```go
func TestVolumeIDValidation(t *testing.T) {
    tests := []struct {
        name    string
        volumeID string
        wantErr bool
    }{
        {"valid pvc format", "pvc-12345678-1234-1234-1234-123456789abc", false},
        {"valid custom name", "my-volume-123", false},
        {"invalid shell chars", "vol;rm -rf /", true},
        {"too long", strings.Repeat("a", 300), true},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := ValidateVolumeID(tt.volumeID)
            if tt.wantErr {
                require.Error(t, err)
            } else {
                require.NoError(t, err)
            }
        })
    }
}
```

### Integration Test Guidelines

- Place tests in `test/integration/*_test.go`
- Use mock RDS server for RouterOS CLI simulation
- Test full request/response cycles
- Validate error handling and retries

### Sanity Test Guidelines

The sanity tests use the official `csi-test` package and should not need modification unless:
- Adding new CSI capabilities (update capability reporting in driver)
- Adjusting test configuration (volume size, idempotent count)
- Fixing capability mismatches between driver and tests

## Test Coverage Goals

**Current coverage:** 68.6% (as of v0.9.0)

**CI enforcement:** Minimum 65% coverage required. Coverage drops below this threshold will fail the build.

**Coverage by package:**
- `pkg/driver` - Target: 70% (core CSI logic, well testable)
- `pkg/driver/attachment` - Target: 65% (reconciliation logic)
- `pkg/driver/connection` - Target: 60% (connection manager)
- `pkg/rds` - Target: 50% (SSH client, hardware dependent)
- `pkg/utils` - Target: 80% (pure functions, fully testable)

**Untested code acceptable when:**
- Requires real hardware (NVMe/TCP operations)
- Requires real Kubernetes cluster (VolumeAttachment tracking)
- Trivial getters/setters

## Continuous Integration

All PRs run automated tests via GitHub Actions (`.github/workflows/pr.yml`):

1. **Verify job** - Code quality checks (fmt, vet, lint) + unit tests + integration tests
2. **Sanity-tests job** - CSI spec compliance validation with mock RDS
3. **Build-test job** - Docker image build for linux/amd64 and linux/arm64
4. **E2E job** - Full stack E2E tests with Ginkgo

**Build fails if:**
- Any unit test fails
- Any integration test fails
- Any sanity test fails (strict CSI compliance required)
- Any E2E test fails
- Linting errors detected
- Code coverage drops below 65%

For complete CI/CD documentation, see [ci-cd.md](ci-cd.md).

## Performance Testing

Performance testing is manual and hardware-dependent:

- **IOPS:** Test with `fio` on NVMe/TCP volumes
- **Latency:** Measure with `fio --rw=randread --iodepth=1`
- **Throughput:** Measure with `fio --rw=read --bs=1M`
- **Concurrent operations:** Create/delete multiple volumes simultaneously

See `docs/performance.md` (planned) for detailed benchmarking procedures.

## Related Documentation

- `CLAUDE.md` - Essential commands and architecture overview
- `docs/architecture.md` - System design and component interactions
- `.planning/phases/22-csi-sanity-tests-integration/22-RESEARCH.md` - CSI testing domain research
- [CSI Spec](https://github.com/container-storage-interface/spec) - Official Container Storage Interface specification
- [csi-test Documentation](https://github.com/kubernetes-csi/csi-test) - Official CSI testing framework

---

For questions or issues with testing, check existing test patterns in the codebase or refer to the CSI testing best practices at https://kubernetes-csi.github.io/docs/functional-testing.html.
