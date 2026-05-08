# Testing Guide

This document describes the comprehensive testing strategy for the nvmet-csi driver.

## Testing Pyramid

```
                    /\
                   /  \
              E2E /    \ Manual Testing
                 /      \ (Real RDS)
                /--------\
               / CSI      \
              /  Sanity    \
             /    Tests     \
            /--------------\
           /  Integration   \
          /   Tests (Mock)   \
         /--------------------\
        /    Unit Tests        \
       /________________________\
```

## Test Types

### 1. Unit Tests

**Purpose**: Test individual components in isolation

**Location**: `pkg/*/` (co-located with code)

**Run Command**:
```bash
make test                 # Run all unit tests
make test-coverage        # Generate coverage report
```

**Coverage**:
- ✅ SSH client command parsing
- ✅ Volume ID generation and validation
- ✅ Identity service RPCs
- ✅ Controller service validation logic
- ✅ Capability checks

**Current Status**: 23 tests, 100% passing

### 2. Integration Tests

**Purpose**: Test full component workflows with a mock RDS server

**Location**: `test/integration/`, `test/mock/`

**Run Command**:
```bash
make test-integration     # Run with mock RDS
```

**What's Tested**:
- Full create/delete volume workflow
- Idempotency for create and delete operations
- Capacity reporting
- Volume capability validation
- Error handling with mocked RDS responses
- SSH client interaction patterns

**Mock RDS Server**:
- Simulates RouterOS CLI via SSH
- Parses `/disk add|remove|print` commands
- Returns realistic output
- Maintains in-memory volume state

**Example Test**:
```go
func TestControllerIntegrationWithMockRDS(t *testing.T) {
    // Start mock RDS
    mockRDS, _ := mock.NewMockRDSServer(12222)
    mockRDS.Start()
    defer mockRDS.Stop()

    // Test volume lifecycle
    // CreateVolume → verify on mock → DeleteVolume → verify cleanup
}
```

### 3. CSI Sanity Tests

**Purpose**: Validate compliance with official CSI specification

**Tool**: [csi-sanity](https://github.com/kubernetes-csi/csi-test/tree/master/cmd/csi-sanity)

**Run Commands**:
```bash
# With mock RDS (Identity + Controller tests)
make test-sanity-mock

# With real RDS (requires hardware)
make test-sanity-real RDS_ADDRESS=192.168.88.1 RDS_SSH_KEY=~/.ssh/id_rsa

# In Docker Compose
make test-docker-sanity
```

**What's Tested**:
- Identity service compliance
- Controller service compliance
- gRPC error code correctness
- Idempotency requirements
- Parameter validation

**Supported Tests** (Milestone 2):
- ✅ Identity service tests (GetPluginInfo, Probe, GetPluginCapabilities)
- ✅ Controller service tests (CreateVolume, DeleteVolume, etc.)
- ⏸️ Node service tests (deferred to Milestone 3)

### 4. Manual E2E Testing

**Purpose**: Validate real-world behavior with actual RDS hardware

**Location**: `test/e2e/MANUAL_TESTING.md`

**Prerequisites**:
- MikroTik RDS server (RouterOS 7.x)
- SSH key-based authentication
- Network connectivity to RDS

**What's Tested**:
- All CSI RPCs against real RDS
- Volume creation on actual hardware
- NVMe/TCP export configuration
- Filesystem capacity queries
- Error scenarios (out of space, SSH failures)
- Security (command injection prevention)

**Run Commands**:
```bash
# Follow the manual testing guide
cat test/e2e/MANUAL_TESTING.md

# Or use grpcurl directly:
./bin/nvmet-csi-plugin-* --controller-mode --rds-address=... &
grpcurl -unix /tmp/csi.sock csi.v1.Controller/CreateVolume -d '{...}'
```

### 5. Docker Compose Testing

**Purpose**: Run tests in isolated containerized environment

**Location**: `docker-compose.test.yml`, `test/docker/`

**Run Commands**:
```bash
# Run all tests
make test-docker

# Run specific test suites
make test-docker-sanity
make test-docker-integration
```

**Components**:
- `mock-rds`: Containerized SSH server simulating RDS
- `csi-controller`: Driver controller connected to mock
- `csi-sanity`: Official sanity test suite
- `integration-tests`: Go integration tests

**Benefits**:
- Reproducible test environment
- No local dependencies required
- CI/CD ready
- Parallel test execution

## Test Matrix

| Test Type | Requires RDS Hardware | Execution Time | Coverage Focus |
|-----------|----------------------|----------------|----------------|
| Unit Tests | ❌ No | ~1s | Individual functions |
| Integration Tests | ❌ No (uses mock) | ~10s | Component workflows |
| CSI Sanity (Mock) | ❌ No | ~30s | CSI spec compliance |
| CSI Sanity (Real) | ✅ Yes | ~1m | Real hardware validation |
| Manual E2E | ✅ Yes | ~5-10m | End-to-end scenarios |
| Docker Compose | ❌ No | ~2m | Containerized validation |

## Running Specific Test Suites

```bash
# Unit tests only
make test

# Unit tests with coverage
make test-coverage

# E2E tests (mock environment)
make e2e-test

# CSI sanity tests
go test -v -timeout 15m ./test/sanity/...

# Stress tests
go test -v -race -timeout 5m ./test/mock/... -run TestConcurrent

# Flaky test detection
go test -count=10 -race ./pkg/...
```

## Running All Tests

### Quick Validation (No Hardware)

```bash
# Run all tests that don't require RDS hardware
make test                  # Unit tests
make test-integration      # Integration tests with mock
make test-sanity-mock      # CSI sanity with mock
```

### Full Validation (With RDS Hardware)

```bash
# Run complete test suite
make test                                                    # Unit tests
make test-integration                                        # Integration tests
make test-sanity-real \
  RDS_ADDRESS=192.168.88.1 \
  RDS_SSH_KEY=~/.ssh/id_rsa                                 # CSI sanity with real RDS

# Manual E2E testing
# Follow test/e2e/MANUAL_TESTING.md
```

### CI/CD Pipeline

```bash
# Recommended CI workflow
make verify                # Lint + format + vet + unit tests
make test-integration      # Integration tests
make test-docker-sanity    # Containerized CSI sanity tests
```

## Test Coverage Goals

### Current Status (v0.9.0)

- **Overall Coverage**: ~68.6% (target 70%)
- **CI Threshold**: 65% minimum
- **Package Thresholds**:
  - pkg/rds: 67.7% (critical path)
  - pkg/mount: 70.3% (system interaction)
  - pkg/nvme: 54.5% (hardware-dependent)
  - pkg/utils: 88.0% (pure utility)
  - pkg/attachment: 83.5% (state management)
  - pkg/driver: 59.8% (CSI implementation)
  - pkg/security: 91.9% (validation)
  - pkg/circuitbreaker: 90.2% (reliability)
  - pkg/observability: 75.0% (monitoring)
  - pkg/reconciler: 66.4% (state management)

### Target (v1.0.0)

- **Overall Coverage**: >70% code coverage
- **CSI Sanity**: 100% of applicable tests passing
- **E2E Tests**: Full volume lifecycle validated on real hardware
- **Stress Tests**: Concurrent operations validated

## Writing Tests

### Unit Test Example

```go
// pkg/utils/volumeid_test.go
func TestVolumeIDToNQN(t *testing.T) {
    volumeID := "pvc-12345678-1234-1234-1234-123456789abc"
    nqn, err := VolumeIDToNQN(volumeID)

    if err != nil {
        t.Fatalf("Unexpected error: %v", err)
    }

    expected := "nqn.2025-01.io.srvlab.rds:pvc-12345678-1234-1234-1234-123456789abc"
    if nqn != expected {
        t.Errorf("Expected NQN %s, got %s", expected, nqn)
    }
}
```

### Integration Test Example

```go
// test/integration/controller_integration_test.go
func TestCreateVolumeIntegration(t *testing.T) {
    // Start mock RDS
    mockRDS, _ := mock.NewMockRDSServer(12222)
    mockRDS.Start()
    defer mockRDS.Stop()

    // Create controller with mock RDS client
    cs := setupControllerWithMock(mockRDS)

    // Test CreateVolume
    req := &csi.CreateVolumeRequest{...}
    resp, err := cs.CreateVolume(context.Background(), req)

    // Verify volume created on mock RDS
    vol, exists := mockRDS.GetVolume(resp.Volume.VolumeId)
    if !exists {
        t.Error("Volume not found on mock RDS")
    }
}
```

## Debugging Tests

### Enable Verbose Logging

```bash
# Unit tests
go test -v ./pkg/...

# Integration tests
go test -v ./test/integration/...

# CSI sanity tests
# Edit test/sanity/run-sanity-tests.sh and add --ginkgo.v
```

### Run Specific Test

```bash
# Run single unit test
go test -v -run TestVolumeIDToNQN ./pkg/utils/

# Run single integration test
go test -v -run TestCreateVolumeIntegration ./test/integration/
```

### Inspect Mock RDS State

```go
// In integration test
mockRDS.ListVolumes()  // See all volumes
vol, _ := mockRDS.GetVolume(volumeID)  // Get specific volume
```

## Test Data

### Volume IDs

Always use valid UUID-based volume IDs:
```
pvc-12345678-1234-1234-1234-123456789abc  ✅ Valid
invalid-format                             ❌ Invalid
pvc-test; rm -rf /                         ❌ Invalid (injection attempt)
```

### NQNs (NVMe Qualified Names)

```
nqn.2025-01.io.srvlab.rds:pvc-<uuid>      ✅ Valid format
```

### Capacity Values

```
1073741824      = 1 GiB (minimum)
17592186044416  = 16 TiB (maximum)
```

## Continuous Integration

### GitHub Actions Example

```yaml
name: Tests

on: [push, pull_request]

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.24'

      - name: Unit Tests
        run: make test

      - name: Integration Tests
        run: make test-integration

      - name: CSI Sanity Tests
        run: make test-sanity-mock

      - name: Upload Coverage
        uses: codecov/codecov-action@v3
        with:
          files: ./coverage.out
```

## Troubleshooting

### Mock RDS Not Responding

```bash
# Check mock server started
netstat -an | grep 12222

# Check SSH connectivity
ssh -p 12222 -o StrictHostKeyChecking=no admin@localhost echo "test"
```

### CSI Sanity Tests Failing

```bash
# Check socket exists
ls -l /tmp/csi-sanity.sock

# Check driver logs
# (driver outputs to stdout in test mode)
```

### Integration Tests Timing Out

```bash
# Increase timeout
go test -v -timeout 20m ./test/integration/...
```

## Flaky Tests

### Detected Flaky Tests

| Test | Package | Failure Rate | Cause | Status |
|------|---------|--------------|-------|--------|
| (none currently) | - | - | - | - |

### Detection Methodology

Flaky tests are detected using:
```bash
go test -count=10 -race ./pkg/...
```

A test is considered flaky if it fails intermittently across multiple runs.

### Skipped Tests

| Test | Package | Reason | Tracking Issue |
|------|---------|--------|----------------|
| (none currently) | - | - | - |

If a flaky test cannot be fixed immediately, it is skipped with `t.Skip("reason")`
and tracked for future resolution.

## Next Steps

After Milestone 2 testing is complete:

1. **Milestone 3**: Add node service tests
   - NodeStageVolume/NodeUnstageVolume tests
   - NodePublishVolume/NodeUnpublishVolume tests
   - NVMe/TCP connection tests

2. **Milestone 4**: Add E2E tests in Kubernetes
   - PVC/Pod lifecycle tests
   - Multi-pod scenarios
   - Failure recovery tests

3. **Milestone 5**: Performance and stress tests
   - Concurrent volume operations
   - Large volume counts
   - Failover scenarios

## Resources

- [CSI Spec](https://github.com/container-storage-interface/spec)
- [csi-test](https://github.com/kubernetes-csi/csi-test)
- [CSI Driver Development Best Practices](https://kubernetes-csi.github.io/docs/developing.html)
- [MikroTik RDS Documentation](https://help.mikrotik.com/docs/)

---

**Last Updated**: 2026-02-05
**Milestone**: v0.9.0 Production Readiness
**Test Status**: ✅ All tests passing (68.6% coverage)
