# CRITICAL: Orphan Cleaner NQN Filtering Bug

**Status:** MITIGATED; keep guarded
**Severity:** CRITICAL if prefix filtering is bypassed
**Discovered:** 2026-02-03 during Phase 13 hardware validation

## Problem

The NVMe orphan cleaner in `pkg/nvme/orphan.go` lists connected NVMe
subsystems on node startup and disconnects CSI-managed subsystems that appear
connected but have no working block device. This is useful after node churn, but
it is dangerous if the managed NQN prefix is missing or too broad: it could
disconnect the host's own NVMe-oF mounts (e.g., diskless NixOS nodes using
NVMe/TCP for /var).

## Root Cause

### Historical Unsafe Implementation

```go
// pkg/nvme/sysfs.go
func (s *SysfsScanner) ListSubsystemNQNs() ([]string, error) {
    // Returns ALL NQNs from /sys/class/nvme-subsystem/*/subsysnqn
    // NO FILTERING - includes host system mounts
}
```

```go
// pkg/nvme/orphan.go
func (oc *OrphanCleaner) CleanupOrphanedConnections(ctx context.Context) error {
    nqns, err := oc.resolver.ListConnectedSubsystems() // Gets ALL subsystems
    for _, nqn := range nqns {
        orphaned, _ := oc.resolver.IsOrphanedSubsystem(nqn)
        if orphaned {
            oc.connector.Disconnect(nqn) // Could disconnect host mounts!
        }
    }
}
```

### Attack Vector If Filtering Is Missing

1. Orphan cleaner lists ALL NVMe subsystems (including host's /var mount with different NQN)
2. For each subsystem, checks if "orphaned" by looking for device in expected locations
3. If host's device isn't where scanner expects, marks as orphaned
4. **Disconnects it** → kills host /var mount → kubelet stops → node goes NotReady

### Example Scenario

**Host NVMe mount:**
- NQN: `nqn.2014-08.org.nvmexpress:uuid:12345678-1234-1234-1234-123456789abc` (host's /var)
- Device: `/dev/nvme0n1`
- Mount: `/var`

**CSI Volume:**
- NQN: `nqn.2000-02.com.mikrotik:pvc-a1b2c3d4-e5f6-7890-abcd-ef1234567890`
- Device: `/dev/nvme1n1`
- Mount: `/var/lib/kubelet/...`

**What happens if orphan cleaner runs:**
1. Lists both NQNs
2. Checks if host NQN is "orphaned"
3. Can't find device (looking in wrong place or wrong structure)
4. Marks as orphaned
5. **Disconnects host's /var** → node dies

## Current Status

The node plugin no longer runs this cleanup by default. Startup cleanup requires
the explicit `-node-orphan-cleanup` flag. The Helm and raw manifests set
`CSI_MANAGED_NQN_PREFIX`, and `CleanupOrphanedConnections` validates that the
prefix is non-empty and syntactically valid before listing or disconnecting any
subsystem. The cleaner also skips NQNs that do not match that prefix.

## Required Guardrails

Keep NQN filtering in the cleanup loop:

```go
// pkg/nvme/sysfs.go
func (s *SysfsScanner) ListSubsystemNQNs() ([]string, error) {
    // ... existing code to read all NQNs ...

    var filteredNQNs []string
    for _, nqn := range allNQNs {
        // Only include CSI-managed volumes
        if strings.HasPrefix(nqn, "nqn.2000-02.com.mikrotik:pvc-") {
            filteredNQNs = append(filteredNQNs, nqn)
        }
    }
    return filteredNQNs, nil
}
```

```go
// pkg/nvme/orphan.go
func (oc *OrphanCleaner) CleanupOrphanedConnections(ctx context.Context) error {
    allNQNs, err := oc.resolver.ListConnectedSubsystems()
    // ... error handling ...

    for _, nqn := range allNQNs {
        // SAFETY: Only check CSI-managed volumes
        if !strings.HasPrefix(nqn, "nqn.2000-02.com.mikrotik:pvc-") {
            klog.V(4).Infof("Skipping non-CSI NQN: %s", nqn)
            continue
        }

        // ... existing orphan detection logic ...
    }
}
```

Also keep prefix validation before any scan:

```go
if err := ValidateNQNPrefix(oc.managedNQNPrefix); err != nil {
    return fmt.Errorf("unsafe managed NQN prefix for orphan cleanup: %w", err)
}
```

## Mitigation

**Do not weaken these controls:**
- Do NOT enable `-node-orphan-cleanup` as ambient default behavior
- Do NOT run node orphan cleanup with an empty `CSI_MANAGED_NQN_PREFIX`
- Do NOT use a broad prefix such as `nqn.`
- Do NOT add periodic node orphan cleanup without rate limiting and explicit
  tests on hosts that have non-CSI NVMe/TCP mounts

## Testing Requirements

Before enabling orphan cleanup:
1. Deploy to test cluster with host NVMe-oF mounts (like diskless NixOS nodes)
2. Verify orphan cleaner only lists CSI volume NQNs
3. Verify it never touches host system NQNs
4. Test with actual orphaned CSI volumes to ensure cleanup still works

## Related

- **File:** `pkg/nvme/orphan.go`
- **File:** `pkg/nvme/sysfs.go` (`ListSubsystemNQNs`)
- **File:** `pkg/nvme/resolver.go` (`ListConnectedSubsystems`)
- **Phase:** 13 - Hardware Validation
- **Date:** 2026-02-03
