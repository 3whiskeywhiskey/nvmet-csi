# Phase 5: Attachment Manager Foundation - Research

**Researched:** 2026-01-30
**Domain:** In-memory state management with persistent storage
**Confidence:** HIGH

## Summary

Phase 5 establishes an AttachmentManager component that tracks volume-to-node attachments with in-memory state backed by PersistentVolume annotations. This research investigated Go concurrency patterns, Kubernetes client-go update mechanisms, and CSI driver state management best practices.

The standard approach uses a struct with sync.RWMutex for thread-safe in-memory state, paired with client-go's retry.RetryOnConflict for atomic PV annotation updates. Per-volume locks prevent race conditions during concurrent operations on the same volume.

**Primary recommendation:** Use map[string]*AttachmentState with sync.RWMutex for global state protection, separate map[string]*sync.Mutex for per-volume operation locks, and k8s.io/client-go/util/retry.RetryOnConflict for PV annotation updates with conflict resolution.

## Standard Stack

The established libraries/tools for this domain:

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| sync (stdlib) | Go 1.24 | RWMutex, Mutex primitives | Built-in concurrency safety, zero dependencies |
| k8s.io/client-go | v0.28.0 | Kubernetes API interaction | Official Kubernetes client, already in use |
| k8s.io/client-go/util/retry | v0.28.0 | Conflict resolution | Handles optimistic concurrency for PV updates |
| k8s.io/api | v0.28.0 | Kubernetes resource types | Standard types for PV manipulation |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| k8s.io/klog/v2 | v2.130.1 | Structured logging | Already in use, standard for CSI drivers |
| context (stdlib) | Go 1.24 | Cancellation propagation | All API calls and lifecycle management |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| sync.RWMutex | sync.Map | sync.Map optimized for write-once/read-many, but doesn't support per-key locking needed here |
| PV annotations | Custom CRD | CRDs add complexity, require installation, PV annotations are simpler for single-field state |
| Manual retry | exponential backoff lib | retry.RetryOnConflict already handles exponential backoff correctly |

**Installation:**
```bash
# Already in go.mod, no new dependencies required
go mod verify
```

## Architecture Patterns

### Recommended Project Structure
```
pkg/
├── attachment/
│   ├── manager.go          # AttachmentManager struct and core logic
│   ├── manager_test.go     # Unit tests with mock k8sClient
│   ├── types.go            # AttachmentState struct definition
│   └── lock.go             # VolumeLockManager for per-volume locks
```

### Pattern 1: Two-Level Locking Strategy
**What:** Global RWMutex protects the attachment map, per-volume Mutex prevents concurrent operations on same volume
**When to use:** Any time tracking state with both frequent reads (list attachments) and writes (attach/detach)
**Example:**
```go
// Source: Existing patterns from pkg/nvme/nvme.go and pkg/rds/pool.go
type AttachmentManager struct {
    mu          sync.RWMutex                  // Protects attachments map
    attachments map[string]*AttachmentState   // volumeID → state
    volumeLocks *VolumeLockManager            // Per-volume operation locks
    k8sClient   kubernetes.Interface
}

// Read operation - RLock allows concurrent reads
func (am *AttachmentManager) GetAttachment(volumeID string) (*AttachmentState, error) {
    am.mu.RLock()
    defer am.mu.RUnlock()
    state, exists := am.attachments[volumeID]
    if !exists {
        return nil, fmt.Errorf("volume %s not tracked", volumeID)
    }
    return state, nil
}

// Write operation - Lock for exclusive access
func (am *AttachmentManager) trackAttachment(volumeID, nodeID string) error {
    am.mu.Lock()
    defer am.mu.Unlock()
    am.attachments[volumeID] = &AttachmentState{
        VolumeID: volumeID,
        NodeID:   nodeID,
        AttachedAt: time.Now(),
    }
    return nil
}
```

### Pattern 2: Per-Volume Locking with Lock Manager
**What:** Separate lock manager that provides per-volume mutexes for serializing operations
**When to use:** Preventing race conditions when multiple NodePublishVolume requests arrive for same volume
**Example:**
```go
// Source: Derived from existing pkg/nvme/nvme.go activeOpsMu pattern
type VolumeLockManager struct {
    mu    sync.Mutex
    locks map[string]*sync.Mutex
}

func NewVolumeLockManager() *VolumeLockManager {
    return &VolumeLockManager{
        locks: make(map[string]*sync.Mutex),
    }
}

func (vlm *VolumeLockManager) Lock(volumeID string) {
    vlm.mu.Lock()
    lock, exists := vlm.locks[volumeID]
    if !exists {
        lock = &sync.Mutex{}
        vlm.locks[volumeID] = lock
    }
    vlm.mu.Unlock()
    lock.Lock()  // Acquire per-volume lock AFTER releasing manager lock
}

func (vlm *VolumeLockManager) Unlock(volumeID string) {
    vlm.mu.Lock()
    lock, exists := vlm.locks[volumeID]
    vlm.mu.Unlock()
    if exists {
        lock.Unlock()
    }
}
```

### Pattern 3: PV Annotation Update with Retry
**What:** Use retry.RetryOnConflict to handle concurrent updates to PersistentVolume annotations
**When to use:** Persisting attachment state to PV after in-memory state changes
**Example:**
```go
// Source: https://pkg.go.dev/k8s.io/client-go/util/retry
import "k8s.io/client-go/util/retry"

const (
    annotationAttachedNode = "rds.csi.srvlab.io/attached-node"
    annotationAttachedAt   = "rds.csi.srvlab.io/attached-at"
)

func (am *AttachmentManager) persistAttachment(ctx context.Context, volumeID, nodeID string) error {
    return retry.RetryOnConflict(retry.DefaultRetry, func() error {
        // Get latest version of PV
        pv, err := am.k8sClient.CoreV1().PersistentVolumes().Get(ctx, volumeID, metav1.GetOptions{})
        if err != nil {
            return err
        }

        // Update annotations
        if pv.Annotations == nil {
            pv.Annotations = make(map[string]string)
        }
        pv.Annotations[annotationAttachedNode] = nodeID
        pv.Annotations[annotationAttachedAt] = time.Now().Format(time.RFC3339)

        // Try to update - will retry on conflict
        _, err = am.k8sClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
        return err
    })
}
```

### Pattern 4: State Rebuild from API Server
**What:** On controller startup, list all PVs and rebuild in-memory state from annotations
**When to use:** Controller initialization to recover state after restart
**Example:**
```go
// Source: Pattern from pkg/reconciler/orphan_reconciler.go List operation
func (am *AttachmentManager) RebuildState(ctx context.Context) error {
    klog.Info("Rebuilding attachment state from PersistentVolumes")

    // List all PVs for this driver
    pvList, err := am.k8sClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
    if err != nil {
        return fmt.Errorf("failed to list PVs: %w", err)
    }

    am.mu.Lock()
    defer am.mu.Unlock()

    // Clear existing state
    am.attachments = make(map[string]*AttachmentState)

    // Rebuild from annotations
    for _, pv := range pvList.Items {
        if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == "rds.csi.srvlab.io" {
            volumeID := pv.Spec.CSI.VolumeHandle

            // Check for attachment annotation
            if nodeID, exists := pv.Annotations[annotationAttachedNode]; exists {
                attachedAt := time.Now()  // Default if missing
                if timeStr, ok := pv.Annotations[annotationAttachedAt]; ok {
                    if parsed, err := time.Parse(time.RFC3339, timeStr); err == nil {
                        attachedAt = parsed
                    }
                }

                am.attachments[volumeID] = &AttachmentState{
                    VolumeID:   volumeID,
                    NodeID:     nodeID,
                    AttachedAt: attachedAt,
                }
                klog.V(2).Infof("Rebuilt attachment: volume=%s, node=%s", volumeID, nodeID)
            }
        }
    }

    klog.Infof("State rebuild complete: %d attachments recovered", len(am.attachments))
    return nil
}
```

### Anti-Patterns to Avoid
- **Holding locks during I/O:** Never hold global lock while making API calls - deadlock risk
- **Skipping defer for Unlock:** Always use `defer mu.Unlock()` to prevent abandoned locks
- **Locking entire operation:** Lock only critical section, not entire attach/detach flow
- **Forgetting conflict resolution:** Direct PV.Update() without retry will fail on conflicts

## Don't Hand-Roll

Problems that look simple but have existing solutions:

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Retry with backoff | Custom sleep loop | retry.RetryOnConflict | Handles exponential backoff, max retries, conflict detection automatically |
| Thread-safe map | Custom lock wrapper | sync.RWMutex + map[string]T | Standard pattern, well-tested, readers don't block each other |
| Per-key locking | sync.Map with custom logic | map[string]*sync.Mutex pattern | sync.Map doesn't support per-key locks, manual approach gives control |
| Time formatting | Custom string format | time.RFC3339 | Standard format, timezone-aware, sortable |
| API resource updates | Direct Update() call | retry.RetryOnConflict wrapper | Handles optimistic concurrency conflicts from multiple writers |

**Key insight:** Kubernetes API server uses optimistic concurrency (resourceVersion). Direct updates fail when another client modified the resource. retry.RetryOnConflict handles this correctly by re-fetching and retrying.

## Common Pitfalls

### Pitfall 1: Lock Inversion Deadlock
**What goes wrong:** Thread A acquires global lock then per-volume lock; Thread B acquires per-volume lock then global lock → deadlock
**Why it happens:** Inconsistent lock acquisition order across different code paths
**How to avoid:** Always acquire locks in same order: volumeLock THEN global lock, or vice versa consistently
**Warning signs:** Hung goroutines, operations timing out, `go test -race` warnings

### Pitfall 2: Forgetting to Release Per-Volume Locks
**What goes wrong:** Per-volume lock acquired but never released due to early return or panic
**Why it happens:** Missing defer statement for Unlock()
**How to avoid:** Always use defer pattern immediately after Lock():
```go
am.volumeLocks.Lock(volumeID)
defer am.volumeLocks.Unlock(volumeID)
```
**Warning signs:** Subsequent operations on same volume hang forever

### Pitfall 3: Holding Global Lock During API Calls
**What goes wrong:** Global RWMutex held while calling k8sClient.PersistentVolumes().Update() blocks all other operations
**Why it happens:** Not separating in-memory state update from persistence
**How to avoid:** Update in-memory state under lock, persist to API outside lock:
```go
// Update in-memory state (fast, under lock)
am.mu.Lock()
am.attachments[volumeID] = state
am.mu.Unlock()

// Persist to API (slow, no lock held)
err := am.persistAttachment(ctx, volumeID, nodeID)
```
**Warning signs:** High latency for read operations, blocked list/get calls

### Pitfall 4: Race Condition in State Rebuild
**What goes wrong:** Controller rebuilds state while NodePublishVolume updates attachment → inconsistent state
**Why it happens:** RebuildState() called without coordinating with active operations
**How to avoid:** Only call RebuildState() during initialization before serving requests, or use global lock
**Warning signs:** Attachments disappear from tracking after rebuild

### Pitfall 5: Annotation Key Collisions
**What goes wrong:** Using generic annotation keys like "attached-node" conflicts with other controllers
**Why it happens:** Not following Kubernetes naming conventions with domain prefix
**How to avoid:** Always use driver domain prefix: `rds.csi.srvlab.io/attached-node`
**Warning signs:** Unexpected annotation values, conflicts with other CSI drivers

### Pitfall 6: Stale Reads After Write
**What goes wrong:** Persist attachment to PV, immediately read back and get old value
**Why it happens:** Kubernetes API has eventual consistency; List/Get may return cached data
**How to avoid:** Use in-memory state as source of truth, treat PV annotations as persistent backup
**Warning signs:** Tests fail intermittently, state appears to revert

## Code Examples

Verified patterns from official sources:

### Attachment State Struct
```go
// Source: Derived from existing patterns in pkg/nvme/nvme.go Metrics struct
type AttachmentState struct {
    VolumeID   string
    NodeID     string
    AttachedAt time.Time
}
```

### AttachmentManager Initialization
```go
// Source: Pattern from pkg/driver/driver.go NewDriver()
func NewAttachmentManager(k8sClient kubernetes.Interface) *AttachmentManager {
    return &AttachmentManager{
        attachments: make(map[string]*AttachmentState),
        volumeLocks: NewVolumeLockManager(),
        k8sClient:   k8sClient,
    }
}

// Initialize method called during controller startup
func (am *AttachmentManager) Initialize(ctx context.Context) error {
    klog.Info("Initializing AttachmentManager")

    // Rebuild state from PV annotations
    if err := am.RebuildState(ctx); err != nil {
        return fmt.Errorf("failed to rebuild state: %w", err)
    }

    klog.Info("AttachmentManager initialized successfully")
    return nil
}
```

### Complete Attach Operation (Thread-Safe)
```go
// Source: Combining patterns from all previous examples
func (am *AttachmentManager) TrackAttachment(ctx context.Context, volumeID, nodeID string) error {
    // Acquire per-volume lock to prevent concurrent operations on same volume
    am.volumeLocks.Lock(volumeID)
    defer am.volumeLocks.Unlock(volumeID)

    // Check current state under read lock
    am.mu.RLock()
    existing, exists := am.attachments[volumeID]
    am.mu.RUnlock()

    if exists {
        if existing.NodeID == nodeID {
            klog.V(2).Infof("Volume %s already attached to node %s", volumeID, nodeID)
            return nil  // Idempotent
        }
        return fmt.Errorf("volume %s already attached to node %s", volumeID, existing.NodeID)
    }

    // Create new attachment state
    state := &AttachmentState{
        VolumeID:   volumeID,
        NodeID:     nodeID,
        AttachedAt: time.Now(),
    }

    // Update in-memory state under write lock
    am.mu.Lock()
    am.attachments[volumeID] = state
    am.mu.Unlock()

    // Persist to PV annotation (outside lock, with retry)
    if err := am.persistAttachment(ctx, volumeID, nodeID); err != nil {
        // Rollback in-memory state on persistence failure
        am.mu.Lock()
        delete(am.attachments, volumeID)
        am.mu.Unlock()
        return fmt.Errorf("failed to persist attachment: %w", err)
    }

    klog.V(2).Infof("Tracked attachment: volume=%s, node=%s", volumeID, nodeID)
    return nil
}
```

### Complete Detach Operation
```go
// Source: Mirror of TrackAttachment pattern
func (am *AttachmentManager) UntrackAttachment(ctx context.Context, volumeID string) error {
    am.volumeLocks.Lock(volumeID)
    defer am.volumeLocks.Unlock(volumeID)

    // Remove from in-memory state
    am.mu.Lock()
    _, exists := am.attachments[volumeID]
    if !exists {
        am.mu.Unlock()
        klog.V(2).Infof("Volume %s not tracked (already detached)", volumeID)
        return nil  // Idempotent
    }
    delete(am.attachments, volumeID)
    am.mu.Unlock()

    // Remove from PV annotation
    if err := am.clearAttachment(ctx, volumeID); err != nil {
        klog.Warningf("Failed to clear attachment annotation for %s: %v", volumeID, err)
        // Don't fail the operation - in-memory state is primary
    }

    klog.V(2).Infof("Untracked attachment: volume=%s", volumeID)
    return nil
}

func (am *AttachmentManager) clearAttachment(ctx context.Context, volumeID string) error {
    return retry.RetryOnConflict(retry.DefaultRetry, func() error {
        pv, err := am.k8sClient.CoreV1().PersistentVolumes().Get(ctx, volumeID, metav1.GetOptions{})
        if err != nil {
            return err
        }

        if pv.Annotations != nil {
            delete(pv.Annotations, annotationAttachedNode)
            delete(pv.Annotations, annotationAttachedAt)
        }

        _, err = am.k8sClient.CoreV1().PersistentVolumes().Update(ctx, pv, metav1.UpdateOptions{})
        return err
    })
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| sync.Map for all use cases | sync.RWMutex + map for dynamic keys | Go 1.9+ | sync.Map optimized for write-once, RWMutex better for dynamic workloads |
| Manual sleep/retry | retry.RetryOnConflict | client-go v0.17+ | Exponential backoff, conflict detection built-in |
| Custom CRDs for state | PV/PVC annotations | CSI spec v1.0+ | Simpler, no CRD installation required |
| Global mutex for everything | Per-resource locks | Modern patterns | Better concurrency, reduced contention |

**Deprecated/outdated:**
- Using sync.Map for per-key locking: sync.Map doesn't support this use case
- Direct PV.Update() without retry: Fails on concurrent updates, not production-ready
- Storing state in ConfigMaps: PV annotations are more semantically correct for volume state

## Open Questions

1. **Annotation Cleanup on PV Deletion**
   - What we know: PV deletion removes all annotations automatically
   - What's unclear: Timing of callback vs annotation cleanup
   - Recommendation: Don't rely on annotations in DeleteVolume, use in-memory state

2. **Multiple Controllers (HA)**
   - What we know: Current design assumes single controller (no leader election)
   - What's unclear: If HA needed, how to coordinate attachment state across replicas
   - Recommendation: Phase 5 focuses on single controller; revisit in future HA phase

3. **Attachment Staleness Detection**
   - What we know: Need to detect node failures where attachment annotation persists
   - What's unclear: Best mechanism for staleness detection (timestamp age? node health?)
   - Recommendation: Start with annotation timestamp, enhance with node readiness checks in later phase

## Sources

### Primary (HIGH confidence)
- [k8s.io/client-go/util/retry documentation](https://pkg.go.dev/k8s.io/client-go/util/retry) - Official retry patterns
- [Go sync package documentation](https://pkg.go.dev/sync) - RWMutex and Mutex primitives
- Existing codebase patterns:
  - `/path/to/rds-csi/pkg/nvme/nvme.go` - RWMutex for Metrics struct
  - `/path/to/rds-csi/pkg/rds/pool.go` - Multiple mutex patterns for connection pooling
  - `/path/to/rds-csi/pkg/reconciler/orphan_reconciler.go` - PV List operation pattern
  - `/path/to/rds-csi/pkg/driver/events.go` - PV Update/Patch operations

### Secondary (MEDIUM confidence)
- [Building Resilient Kubernetes Controllers: A Practical Guide to Retry Mechanisms](https://medium.com/@vamshitejanizam/building-resilient-kubernetes-controllers-a-practical-guide-to-retry-mechanisms-0d689160fa51) - Verified retry patterns
- [Kubernetes operators best practices: understanding conflict errors](https://alenkacz.medium.com/kubernetes-operators-best-practices-understanding-conflict-errors-d05353dff421) - RetryOnConflict usage
- [Understanding sync.Mutex vs sync.RWMutex in Go](https://medium.com/@madhavj211/understanding-sync-mutex-vs-sync-rwmutex-in-go-with-benchmarks-bd9eddc46fb9) - Performance characteristics
- [Which one is better: Builtin map with mutex lock or sync.Map](https://forum.golangbridge.org/t/which-one-is-better-to-use-builtin-map-with-mutex-lock-or-sync-map-in-a-case-where-i-want-to-lock-value-of-specific-key-instead-of-locking-whole-map/9935) - Per-key locking guidance

### Tertiary (LOW confidence)
- [CSI driver controller restart recovery state persistence best practices](https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/1648) - Recovery annotation pattern (Azure-specific)
- [Kubernetes Controllers documentation](https://kubernetes.io/docs/concepts/architecture/controller/) - General controller patterns

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH - All libraries already in use, official Go stdlib and k8s.io packages
- Architecture: HIGH - Patterns verified in existing codebase, official client-go examples
- Pitfalls: HIGH - Based on known Go concurrency issues and CSI driver development experience
- Code examples: HIGH - Derived from existing working code in this repository

**Research date:** 2026-01-30
**Valid until:** 60 days (stable domain, Go stdlib and client-go v0.28 are mature)

**Notes:**
- Zero new dependencies required - all patterns use existing libraries
- AttachmentManager can be unit tested with fake.Clientset from client-go
- Per-volume locks critical for correctness under concurrent NodePublishVolume calls
- PV annotations provide crash recovery but in-memory state is source of truth during runtime
