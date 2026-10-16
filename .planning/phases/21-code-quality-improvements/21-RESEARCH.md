# Phase 21: Code Quality Improvements - Research

**Researched:** 2026-02-04
**Domain:** Go code quality, refactoring patterns, package organization
**Confidence:** HIGH

## Summary

Phase 21 focuses on eliminating code smells and improving maintainability in the RDS CSI driver codebase. The primary tasks involve extracting common error handling patterns, replacing duplicated switch statements with table-driven lookups, and refactoring large packages for better separation of concerns.

**Current state analysis:**
- Phase 18 (logging cleanup) completed: 11 operation-specific Log* methods consolidated using table-driven LogOperation helper (logger.go reduced from 540 to 445 lines)
- Phase 19 (error handling) completed: 10 sentinel errors defined, helper functions created (WrapVolumeError, WrapNodeError, etc.), 96.1% compliance with %w error wrapping
- CONCERNS.md identifies 3 primary code smells: severity mapping duplication (one remaining switch in LogEvent), large packages (driver: 3552 lines, rds: 1834 lines, nvme: 1655 lines), and common patterns needing extraction

The standard approach for Go code quality improvements combines table-driven design patterns, package decomposition following Single Responsibility Principle, and complexity metrics enforcement via golangci-lint. Go's tooling ecosystem provides comprehensive support for automated refactoring and quality measurement.

**Primary recommendation:** Use table-driven maps for the remaining severity mapping switch, split large packages (driver, rds, nvme) into focused sub-packages by functionality, and configure golangci-lint complexity thresholds (cyclop, gocyclo, gocognit) to prevent regression.

## Standard Stack

The established libraries/tools for Go code quality:

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| golangci-lint | 1.61+ | Meta-linter with complexity metrics | Industry standard, used by Kubernetes/Prometheus, aggregates 50+ linters |
| gocyclo | 0.8.0+ | Cyclomatic complexity measurement | Official Go tool, integrated into golangci-lint, measures function complexity |
| gocognit | 1.1.0+ | Cognitive complexity measurement | Measures human readability complexity, more nuanced than cyclomatic |
| cyclop | 1.2.0+ | Package-level complexity | Checks both function and package complexity boundaries |
| errorlint | 1.4.5+ | Error handling pattern enforcement | Already configured in .golangci.yml, enforces %w wrapping |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| gopls | v0.17.0+ | Go language server with refactoring | For automated extract-function, rename operations |
| gopatch | latest | Pattern-based refactoring | For bulk transformations (e.g., replacing all switch patterns) |
| golang.org/x/tools/refactor | latest | Example-based refactoring | For complex structural changes |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| golangci-lint | Individual linters (go vet, staticcheck) | golangci-lint aggregates and parallelizes, far more efficient |
| Map lookup | Switch statement | Switch is faster for <10 cases, but map more maintainable and reduces complexity metrics |
| Package split | Keep large packages | Large packages increase coupling and cognitive load; split improves testability |

**Installation:**
```bash
# golangci-lint already installed via Makefile
make install-tools

# Or directly via go install
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
go install github.com/fzipp/gocyclo/cmd/gocyclo@latest
go install github.com/uudashr/gocognit/cmd/gocognit@latest
```

## Architecture Patterns

### Pattern 1: Table-Driven Design for Switch Elimination

**What:** Replace duplicated switch statements with map lookups containing configuration data.

**When to use:** When the same switch structure appears multiple times, or when cases only differ in data (not control flow).

**Example from Phase 18 (completed):**
```go
// BEFORE: Duplicated switch in every Log* method (300+ lines)
func (l *Logger) LogVolumeCreate(...) {
    switch outcome {
    case OutcomeSuccess:
        eventType = EventVolumeCreateSuccess
        severity = SeverityInfo
        message = "Volume created successfully"
    case OutcomeFailure:
        eventType = EventVolumeCreateFailure
        severity = SeverityError
        message = "Volume creation failed"
    // ...
    }
}

// AFTER: Single table-driven helper
var operationConfigs = map[string]OperationLogConfig{
    "VolumeCreate": {
        SuccessType: EventVolumeCreateSuccess,
        FailureType: EventVolumeCreateFailure,
        SuccessSev: SeverityInfo,
        FailureSev: SeverityError,
        // ...
    },
}

func (l *Logger) LogOperation(config OperationLogConfig, outcome EventOutcome, ...) {
    // Single switch on outcome, config provides data
}
```

**For Phase 21 (remaining work):**
The severity-to-verbosity mapping in LogEvent() (lines 49-69) should become:
```go
var severityToVerbosity = map[EventSeverity]struct {
    level   klog.Level
    logFunc func(args ...interface{})
}{
    SeverityInfo:     {level: 2, logFunc: func(args ...interface{}) { klog.V(2).Info(args...) }},
    SeverityWarning:  {level: 1, logFunc: klog.Warning},
    SeverityError:    {level: 0, logFunc: klog.Error},
    SeverityCritical: {level: 0, logFunc: klog.Error},
}
```

### Pattern 2: Package Decomposition by Responsibility

**What:** Split large packages (>1500 lines) into focused sub-packages following Single Responsibility Principle.

**When to use:** When a package has multiple distinct concerns, or when file count exceeds 10-12 files.

**Current package sizes (non-test code):**
- `pkg/driver/`: 3552 lines (controller.go: 1007, node.go: 970, driver.go: 503, events.go: 395, vmi_grouper.go: 279)
- `pkg/rds/`: 1834 lines (commands.go: 735, pool.go: 440, ssh_client.go: 348)
- `pkg/nvme/`: 1655 lines (nvme.go: 881, resolver.go: 257)

**Recommended decomposition:**

```
pkg/driver/
├── csi/              # CSI interface implementations
│   ├── controller.go # ControllerServer implementation
│   ├── node.go       # NodeServer implementation
│   └── identity.go   # IdentityServer (from driver.go)
├── events/           # Event management
│   ├── events.go     # Event types and recording
│   └── vmi.go        # VMI grouping logic
└── driver.go         # Main driver struct (reduced to ~100 lines)

pkg/rds/
├── client.go         # RDSClient interface and factory
├── commands/         # Command implementations
│   ├── volume.go     # Volume operations (create, delete)
│   ├── disk.go       # Disk query operations
│   └── file.go       # File operations
├── pool/             # Connection pool
│   └── pool.go
└── ssh/              # SSH transport
    └── client.go

pkg/nvme/
├── connector.go      # Connector interface
├── nvme.go           # NVMe operations (connect, disconnect)
├── resolver/         # Device path resolution
│   ├── resolver.go
│   └── sysfs.go
└── device.go         # Device utilities
```

**Guidelines:**
- Each sub-package should have <800 lines total
- Each file should focus on one aspect (operations, types, or utilities)
- Interface definitions stay in parent package for import simplicity
- Internal implementation details move to sub-packages

### Pattern 3: Extract Common Error Handling Utilities

**What:** Consolidate repeated error handling patterns into reusable helper functions.

**Current state:** Phase 19 completed most of this work:
- Sentinel errors defined in `pkg/utils/errors.go`
- Helper functions exist: WrapVolumeError, WrapNodeError, WrapDeviceError, WrapMountError
- Error wrapping compliance: 96.1% (150 %w uses, 6 correct %v uses)

**Remaining work for Phase 21:**
The 6 correct %v uses need documentation explaining why %v is appropriate (formatting non-error values). Add inline comments:

```go
// Use %v (not %w) because pids is []int, not an error
return fmt.Errorf("found active PIDs: %v", pids)
```

**Pattern:**
```go
// Bottom layer adds device/resource context
if err := operation(); err != nil {
    return utils.WrapVolumeError(utils.ErrOperationFailed, volumeID, "specific operation failed")
}

// Middle layer adds operation context
if err := bottomLayer(); err != nil {
    return fmt.Errorf("stage volume: %w", err)  // Preserves error chain
}

// Top layer (CSI boundary) converts to gRPC status
if err := middleLayer(); err != nil {
    if errors.Is(err, utils.ErrVolumeNotFound) {
        return nil, status.Error(codes.NotFound, "volume not found")
    }
    return nil, status.Errorf(codes.Internal, "internal error: %v", err)
}
```

### Anti-Patterns to Avoid

- **Large God Objects:** Don't create utility packages with unrelated functions. Each package should have a clear purpose.
- **Circular Dependencies:** If package A needs package B and B needs A, extract shared types to a third package.
- **Premature Abstraction:** Don't extract a pattern until it appears 3+ times. Two occurrences may be coincidence.
- **Over-Splitting:** Don't create packages with <100 lines. Small packages increase import complexity without benefit.

## Don't Hand-Roll

Problems that look simple but have existing solutions:

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Cyclomatic complexity measurement | Custom AST walker | gocyclo / cyclop linter | Handles edge cases (defer, closures, type switches), standard metrics |
| Package refactoring | Manual file moves | gopls extract refactoring | Handles imports, references, preserves semantics |
| Switch-to-map conversion | Manual rewrite | gopatch pattern file | Bulk transformation with verification |
| Complexity thresholds | Custom scripts | golangci-lint with cyclop/gocognit | Integrated into CI, configurable, widely understood |

**Key insight:** Go's tooling ecosystem is mature. Use standard tools rather than custom scripts to benefit from community knowledge and avoid maintenance burden.

## Common Pitfalls

### Pitfall 1: Splitting Packages Too Early

**What goes wrong:** Creating sub-packages before responsibilities stabilize leads to constant reorganization and import churn.

**Why it happens:** Eager to reduce line counts without analyzing actual coupling.

**How to avoid:**
- Wait until a package exceeds 1500 lines consistently
- Analyze import graphs: `go mod graph | grep 'rds-csi-driver/pkg'`
- Use cohesion metrics: files that always change together belong in same package
- Refactor when adding new feature would make package responsibilities unclear

**Warning signs:**
- Sub-packages importing parent package (circular dependency)
- Files with <50 lines (over-splitting)
- Package purpose not explainable in one sentence

### Pitfall 2: Map Lookup Performance Myths

**What goes wrong:** Avoiding maps because "switches are faster" when difference is negligible.

**Why it happens:** Micro-optimization based on outdated advice or misread benchmarks.

**How to avoid:**
- Measure actual hot paths with pprof: `go test -cpuprofile=cpu.out`
- Absolute difference: map lookup ~10-15ns, switch ~5-8ns for 7 cases
- Only matters if executed millions of times per second in tight loop
- CSI operations (volume create/delete) are network-bound (100ms+), not CPU-bound

**Real performance impact:** Security logger calls happen 10-100x per second. Map overhead: 0.001ms. SSH round-trip: 5-20ms. Map is 0.005% of operation time.

**Decision rule:** Choose map if it reduces complexity or improves maintainability. Only use switch for performance if profiling shows it in top 10 hot paths.

### Pitfall 3: Breaking Backward Compatibility During Refactoring

**What goes wrong:** Moving exported functions to sub-packages breaks existing imports.

**Why it happens:** Focus on internal structure without considering external consumers.

**How to avoid:**
- Keep exported interfaces in parent package
- Use type aliases to maintain compatibility during transition:
  ```go
  // pkg/rds/client.go
  package rds

  import "git.srvlab.io/whiskey/rds-csi-driver/pkg/rds/ssh"

  // Deprecated: Use ssh.Client directly. Will be removed in v0.9.0.
  type SSHClient = ssh.Client
  ```
- Add deprecation notices with target version
- Maintain for at least 2 minor versions before removal

**Warning signs:**
- Test builds pass but external integrations fail
- Import paths change in driver/controller.go or driver/node.go
- Exported types moved without aliases

### Pitfall 4: Complexity Threshold Too Strict Too Soon

**What goes wrong:** Setting gocyclo threshold to 10 immediately causes 50+ violations, team ignores linter.

**Why it happens:** Following "best practice" advice without understanding current state.

**How to avoid:**
- Measure baseline: `gocyclo -over 15 pkg/` shows current complexity
- Set threshold above worst offender initially (e.g., 25 if max is 23)
- Ratchet down by 2-3 points per sprint: 25 → 22 → 19 → 16 → 13 → 10
- Focus on reducing new complexity first, refactor existing code gradually

**Current baseline:**
```bash
gocyclo -over 15 pkg/
# Expected: 5-10 functions above 15 (controller.go, node.go, commands.go)
# Start threshold: 25
# Target for v0.8: 20
# Target for v1.0: 15
```

### Pitfall 5: Ignoring Package Import Cycles

**What goes wrong:** Refactoring creates circular imports that prevent compilation.

**Why it happens:** Moving code without analyzing dependency direction.

**How to avoid:**
- Before moving code, check imports: `go list -f '{{.ImportPath}} -> {{join .Imports ", "}}' ./pkg/...`
- Draw dependency graph: packages should form a DAG (directed acyclic graph)
- Rule: lower-level packages never import higher-level packages
  - Good: `pkg/driver` imports `pkg/rds`, `pkg/rds` imports `pkg/utils`
  - Bad: `pkg/utils` imports `pkg/driver` (cycle)
- Extract shared types to new package if cycle unavoidable

**Detection:**
```bash
go build ./... 2>&1 | grep "import cycle"
```

## Code Examples

Verified patterns from current codebase:

### Table-Driven Severity Mapping (Target for Phase 21)

**Current state (logger.go lines 49-69):**
```go
switch event.Severity {
case SeverityInfo:
    verbosity = 2
    logFunc = func(args ...interface{}) { klog.V(2).Info(args...) }
case SeverityWarning:
    verbosity = 1
    logFunc = klog.Warning
case SeverityError:
    verbosity = 0
    logFunc = klog.Error
case SeverityCritical:
    verbosity = 0
    logFunc = klog.Error
default:
    verbosity = 2
    logFunc = func(args ...interface{}) { klog.V(2).Info(args...) }
}
```

**Recommended refactoring:**
```go
// Package-level map (initialized once)
type severityMapping struct {
    verbosity klog.Level
    logFunc   func(args ...interface{})
}

var severityMap = map[EventSeverity]severityMapping{
    SeverityInfo:     {verbosity: 2, logFunc: func(args ...interface{}) { klog.V(2).Info(args...) }},
    SeverityWarning:  {verbosity: 1, logFunc: klog.Warning},
    SeverityError:    {verbosity: 0, logFunc: klog.Error},
    SeverityCritical: {verbosity: 0, logFunc: klog.Error},
}

// In LogEvent:
mapping, ok := severityMap[event.Severity]
if !ok {
    mapping = severityMap[SeverityInfo] // default
}
logFunc := mapping.logFunc
```

**Impact:**
- Lines reduced: 21 → 8 (in LogEvent method)
- Cyclomatic complexity: 5 → 1
- Future additions: Add map entry (1 line) vs. add case + 3 lines

### Error Helper Function Usage

**From pkg/utils/errors.go (already implemented in Phase 19):**
```go
// Helper functions for layered error context
func WrapVolumeError(sentinel error, volumeID, details string) error {
    if details != "" {
        return fmt.Errorf("volume %s: %s: %w", volumeID, details, sentinel)
    }
    return fmt.Errorf("volume %s: %w", volumeID, sentinel)
}

// Usage in pkg/rds/commands.go:
func (c *Client) CreateVolume(opts CreateVolumeOptions) error {
    output, err := c.sshClient.RunCommand(cmd)
    if err != nil {
        return utils.WrapVolumeError(err, opts.Slot, "ssh command failed")
    }
    // ...
}

// Usage in pkg/driver/controller.go:
func (cs *ControllerServer) CreateVolume(...) (*csi.CreateVolumeResponse, error) {
    err := cs.rdsClient.CreateVolume(opts)
    if err != nil {
        if errors.Is(err, utils.ErrVolumeExists) {
            // Idempotent - already exists
            return existingVolume, nil
        }
        return nil, status.Errorf(codes.Internal, "create failed: %v", err)
    }
    // ...
}
```

### Package Split Example (Proposed for driver package)

**Current structure (3552 lines in 6 files):**
```
pkg/driver/
├── controller.go      (1007 lines) - ControllerServer implementation
├── node.go            (970 lines)  - NodeServer implementation
├── driver.go          (503 lines)  - Driver, IdentityServer, server management
├── events.go          (395 lines)  - Event recording helpers
├── vmi_grouper.go     (279 lines)  - VirtualMachineInstance grouping
└── server.go          (398 lines)  - gRPC server wrapper
```

**Proposed structure (same lines, better organized):**
```
pkg/driver/
├── driver.go          (~150 lines) - Driver struct, NewDriver, initialization
├── csi/                            - CSI service implementations
│   ├── controller.go  (1007 lines) - ControllerServer (unchanged)
│   ├── node.go        (970 lines)  - NodeServer (unchanged)
│   ├── identity.go    (~100 lines) - IdentityServer (extracted from driver.go)
│   └── server.go      (398 lines)  - gRPC server (moved from parent)
├── events/                         - Event management utilities
│   ├── recorder.go    (~300 lines) - Event recording (from events.go)
│   └── vmi.go         (279 lines)  - VMI grouping (from vmi_grouper.go)
└── types.go           (~50 lines)  - Shared types and interfaces
```

**Migration strategy:**
1. Create sub-packages with `internal` visibility initially
2. Move implementations, update imports
3. Verify tests pass: `go test ./pkg/driver/...`
4. Update main.go imports if needed
5. Remove old files after verification

**Import impact:**
```go
// Before:
import "git.srvlab.io/whiskey/rds-csi-driver/pkg/driver"

// After (external users unchanged):
import "git.srvlab.io/whiskey/rds-csi-driver/pkg/driver"
// Still access driver.NewDriver() - interface unchanged

// Internal files:
import "git.srvlab.io/whiskey/rds-csi-driver/pkg/driver/csi"
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Manual switch statements | Table-driven maps with config structs | Go 1.13+ (map performance improved) | Reduces duplication, improves maintainability |
| Large monolithic packages | Focused sub-packages by responsibility | Go 1.4+ (package initialization order deterministic) | Better testability, clearer dependencies |
| String error matching | Sentinel errors with errors.Is() | Go 1.13 (2019) | Type-safe error handling, wrapping support |
| No complexity metrics in CI | golangci-lint with cyclop/gocognit | 2020+ | Prevent complexity regression automatically |
| Manual refactoring | gopls extract/rename operations | Go 1.18+ (gopls maturity) | Safer refactoring with automated import fixes |

**Deprecated/outdated:**
- **github.com/pkg/errors**: Deprecated since Go 1.13 added native error wrapping. Use fmt.Errorf with %w instead.
- **gocyclo without golangci-lint**: Still works but runs standalone. Prefer integrated linters for CI efficiency.
- **Flat package structure**: Pre-2015 Go projects used flat pkg/. Modern practice uses hierarchical sub-packages for clarity.

## Open Questions

Things that couldn't be fully resolved:

1. **What is the optimal package size threshold?**
   - What we know: Go standard library packages range 500-15,000 lines (net/http: 15,734 lines). Community recommends 500-2000 lines per package.
   - What's unclear: Whether 1500-line threshold is too strict for CSI drivers (lots of boilerplate from spec).
   - Recommendation: Start with 2000-line threshold, refactor only if package has multiple clear responsibilities. Analyze coupling before splitting.

2. **Should we extract common CSI boilerplate?**
   - What we know: ControllerServer and NodeServer have repeated validation patterns (req.GetVolumeId() checks, capability checks).
   - What's unclear: Whether extraction improves or obscures code (CSI spec changes are rare, familiarity matters).
   - Recommendation: Defer until v0.9.0. Current duplication is spec-mandated and explicit. Extract only if validation logic becomes complex.

3. **How strict should complexity thresholds be?**
   - What we know: Current max cyclomatic complexity unknown (needs baseline). Go community recommends 15 for new code, 20-25 for existing.
   - What's unclear: Whether CSI methods inherently have high complexity (lots of if/else for error codes).
   - Recommendation: Measure baseline with `gocyclo -over 15 ./pkg/`. Set initial threshold 5 points above max. Review quarterly and ratchet down.

## Sources

### Primary (HIGH confidence)

- [golangci-lint Linters Documentation](https://golangci-lint.run/docs/linters/) - Official linter list and configuration (2026)
- [Switch vs. Map in Go - Hashrocket](https://hashrocket.com/blog/posts/switch-vs-map-which-is-the-better-way-to-branch-in-go) - Performance analysis with benchmarks
- [Go Package Organization - Medium](https://medium.com/@leodahal4/package-organization-in-go-34efb1cd99a6) - Package structure best practices
- [Error Handling in Go Part 2 - anynines](https://anynines.com/blog/error-handling-in-go-golong-part-2/) - Sentinel errors and wrapping patterns
- [Go Project Structure - Rost Glukhov](https://www.glukhov.org/post/2025/12/go-project-structure/) - 2025 best practices (verified 2026)

### Secondary (MEDIUM confidence)

- [Refactoring Guru - Switch Statements Code Smell](https://refactoring.guru/smells/switch-statements) - General refactoring patterns
- [Code Smell: Switch Statements - DEV Community](https://dev.to/producthackers/code-smell-switch-statements-51h7) - When to replace switches
- [Cyclomatic Complexity in Go - Google Groups](https://groups.google.com/g/golang-nuts/c/HNNUjE5VWos) - Community discussion on thresholds
- [GitHub: gocyclo](https://github.com/fzipp/gocyclo) - Official gocyclo repository and usage
- [GitHub: gocognit](https://github.com/uudashr/gocognit) - Cognitive complexity for Go

### Tertiary (LOW confidence)

- [8 Code Refactoring Tools 2026 - zencoder.ai](https://zencoder.ai/blog/code-refactoring-tools) - AI-powered tools (marketing content)

### Codebase Analysis (HIGH confidence)

- `/path/to/rds-csi/.planning/codebase/CONCERNS.md` - Code smell documentation (2026-02-04)
- `/path/to/rds-csi/.planning/codebase/CONVENTIONS.md` - Error handling conventions (2026-02-04)
- `/path/to/rds-csi/pkg/security/logger.go` - Table-driven pattern example (Phase 18 implementation)
- `/path/to/rds-csi/pkg/utils/errors.go` - Error utilities (Phase 19 implementation)
- `/path/to/rds-csi/.golangci.yml` - Current linter configuration

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH - golangci-lint and gocyclo verified from official sources and .golangci.yml
- Architecture: HIGH - Table-driven pattern verified in completed Phase 18 code, package decomposition from Go standard practices
- Pitfalls: HIGH - Based on analysis of current codebase issues in CONCERNS.md and established Go community knowledge

**Research date:** 2026-02-04
**Valid until:** 2026-05-04 (90 days - Go tooling stable, patterns well-established)

**Codebase baseline:**
- Current complexity: Unknown (needs gocyclo baseline measurement)
- Package sizes: driver (3552), rds (1834), nvme (1655), mount (1350) - all exceed 1500-line threshold
- Error handling: 96.1% compliant with %w wrapping (Phase 19 complete)
- Logging: Table-driven pattern implemented (Phase 18 complete)
- Remaining work: One severity switch statement (21 lines), package decomposition, complexity threshold configuration
