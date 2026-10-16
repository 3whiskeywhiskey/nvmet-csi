# Phase 19: Error Handling Standardization - Research

**Researched:** 2026-02-04
**Domain:** Go error handling patterns and CSI error conventions
**Confidence:** HIGH

## Summary

Error handling standardization in Go 1.13+ requires converting `fmt.Errorf` calls with `%v` to `%w` for proper error wrapping, ensuring all errors include contextual information (operation, volume ID, node, reason), and establishing consistent patterns across packages. The codebase currently has 6 instances using `%v` that need conversion and 147 already using `%w`. The project has sophisticated error infrastructure (`pkg/utils/errors.go`) with sanitization, classification (Internal/User/Validation), and security-aware logging.

The standard approach is to use `fmt.Errorf` with `%w` for error wrapping, add operation context at each layer, convert Go errors to gRPC status codes at CSI boundaries, and audit error paths using linters (`errorlint`, `wrapcheck`) and manual code review. Error messages must include diagnostic context while protecting sensitive information (IP addresses, paths, hostnames).

**Primary recommendation:** Use `fmt.Errorf` with `%w` for all error wrapping, add operation-specific context at each layer, convert to gRPC status codes at CSI boundaries, and document patterns in CONVENTIONS.md with examples from the existing sophisticated error infrastructure.

## Standard Stack

The established libraries/tools for Go error handling:

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| fmt | stdlib 1.13+ | Error wrapping with `%w` verb | Official Go error wrapping mechanism since 1.13 |
| errors | stdlib 1.13+ | `errors.Is()`, `errors.As()`, `errors.Unwrap()` | Standard library error inspection functions |
| google.golang.org/grpc/status | v1.50+ | gRPC status code conversion | CSI spec requires gRPC error codes |
| google.golang.org/grpc/codes | v1.50+ | Standard gRPC error codes | Industry-standard RPC error semantics |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| golangci-lint/errorlint | v1.8.0+ | Lint for proper `%w` usage | CI/CD verification of error wrapping |
| golangci-lint/wrapcheck | v2.11.0+ | Check external errors are wrapped | Ensure error context propagation |
| k8s.io/klog/v2 | v2.x | Structured logging with error context | Production error logging with verbosity levels |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Standard errors | github.com/pkg/errors | pkg/errors predates stdlib %w, now deprecated in favor of fmt.Errorf |
| Custom error types | uber-go/multierr | Only needed for aggregating multiple errors (not CSI use case) |
| Manual wrapping | errors.Join() (Go 1.20+) | For combining multiple errors, not for adding context |

**Installation:**
```bash
# Core libraries are stdlib, already available
# Linters installed via golangci-lint
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

# CSI/gRPC dependencies (already in project)
go get google.golang.org/grpc@latest
```

## Architecture Patterns

### Current Codebase Structure
The project has sophisticated error infrastructure in place:

```
pkg/
├── utils/
│   ├── errors.go           # SanitizedError type, security-aware sanitization
│   └── errors_test.go      # Error sanitization tests
├── driver/
│   ├── controller.go       # CSI Controller with gRPC status conversion
│   ├── node.go             # CSI Node with gRPC status conversion
│   └── doc.go              # Logging conventions (V(0)-V(5))
├── rds/
│   ├── commands.go         # RDS operations with error wrapping
│   └── ssh_client.go       # SSH operations with connection errors
├── nvme/
│   └── nvme.go             # NVMe operations with device errors
└── mount/
    └── mount.go            # Mount operations with filesystem errors
```

### Pattern 1: Error Wrapping with Context
**What:** Add operation context at each layer using `fmt.Errorf` with `%w`
**When to use:** Every function that calls another function and receives an error

**Example:**
```go
// Source: Official Go Blog https://go.dev/blog/go1.13-errors
// Current codebase: pkg/rds/commands.go

// BAD - loses error chain (using %v)
if err != nil {
    return fmt.Errorf("failed to create volume: %v", err)
}

// GOOD - preserves error chain (using %w)
if err != nil {
    return fmt.Errorf("failed to create volume %s: %w", volumeID, err)
}

// BETTER - includes operation context
if err != nil {
    return fmt.Errorf("failed to create volume %s on RDS %s: %w", volumeID, rdsAddress, err)
}
```

### Pattern 2: CSI Boundary Error Conversion
**What:** Convert Go errors to gRPC status codes at CSI service boundaries
**When to use:** All CSI method returns (CreateVolume, DeleteVolume, NodeStageVolume, etc.)

**Example:**
```go
// Source: Current codebase pkg/driver/controller.go, pkg/driver/node.go

// Input validation errors
if volumeID == "" {
    return nil, status.Error(codes.InvalidArgument, "volume ID is required")
}

// Validation with context
if err := utils.ValidateVolumeID(volumeID); err != nil {
    return nil, status.Errorf(codes.InvalidArgument, "invalid volume ID: %v", err)
}

// Internal errors from operations
if err := cs.driver.rdsClient.CreateVolume(opts); err != nil {
    return nil, status.Errorf(codes.Internal, "failed to create volume: %v", err)
}

// Not found errors
if errors.Is(err, ErrVolumeNotFound) {
    return nil, status.Errorf(codes.NotFound, "volume %s not found", volumeID)
}

// Resource exhausted errors
if strings.Contains(err.Error(), "not enough space") {
    return nil, status.Errorf(codes.ResourceExhausted, "insufficient storage capacity")
}
```

### Pattern 3: Layered Error Context
**What:** Each layer adds its own context without duplicating lower layer details
**When to use:** Multi-layer operations (CSI → Driver → RDS → SSH)

**Example:**
```go
// Source: Current codebase pattern across pkg/driver, pkg/rds, pkg/nvme

// Layer 4: SSH client (lowest)
func (c *sshClient) runCommand(cmd string) (string, error) {
    output, err := session.CombinedOutput(cmd)
    if err != nil {
        return "", fmt.Errorf("SSH command failed: %w", err)
    }
    return string(output), nil
}

// Layer 3: RDS commands
func (c *sshClient) CreateVolume(opts CreateVolumeOptions) error {
    cmd := buildDiskAddCommand(opts)
    _, err := c.runCommandWithRetry(cmd, 3)
    if err != nil {
        return fmt.Errorf("failed to create volume %s: %w", opts.Slot, err)
    }
    return nil
}

// Layer 2: Driver
func (cs *ControllerServer) CreateVolume(ctx context.Context, req *csi.CreateVolumeRequest) (*csi.CreateVolumeResponse, error) {
    opts := buildCreateOptions(req)
    if err := cs.driver.rdsClient.CreateVolume(opts); err != nil {
        return nil, status.Errorf(codes.Internal, "volume creation failed for %s: %v", volumeID, err)
    }
    return response, nil
}

// Error chain result:
// "volume creation failed for pvc-123: failed to create volume pvc-123: SSH command failed: exit status 1"
```

### Pattern 4: Error Sanitization for Security
**What:** Remove sensitive information (IPs, paths, credentials) from user-facing errors
**When to use:** All errors returned to Kubernetes/users via CSI responses

**Example:**
```go
// Source: Current codebase pkg/utils/errors.go (already implemented)

// Internal error with full details (logged but not exposed)
err := fmt.Errorf("failed to connect to RDS at 198.51.100.20:22: authentication failed")

// Sanitized error for user
sanitized := utils.NewInternalError(err, "RDS connection failed")
// Returns: "RDS connection failed" (IP hidden)
// Logs: "[INTERNAL ERROR] Error: RDS connection failed (internal: failed to connect to RDS at [IP-ADDRESS]:22: authentication failed)"

// User-facing error with safe context
userErr := utils.NewUserError(err, "Volume creation")
// Returns: "Volume creation failed: RDS connection failed"
```

### Pattern 5: Error Context Fields
**What:** Include standard diagnostic fields in error messages
**When to use:** All error messages in CSI driver operations

**Required fields:**
- Operation name (e.g., "CreateVolume", "NodeStageVolume", "NVMe connect")
- Volume ID (when applicable)
- Node ID (for node operations)
- Device/path (for filesystem operations)
- Reason (what went wrong)

**Example:**
```go
// Source: Best practices from research + current codebase patterns

// Minimal context
return fmt.Errorf("failed to mount: %w", err)

// Good context
return fmt.Errorf("failed to mount volume %s: %w", volumeID, err)

// Best context (includes all relevant fields)
return fmt.Errorf("failed to mount volume %s to %s on node %s: %w",
    volumeID, targetPath, nodeID, err)

// For NVMe operations
return fmt.Errorf("failed to connect NVMe device (nqn=%s, address=%s:%d): %w",
    nqn, address, port, err)

// For filesystem operations
return fmt.Errorf("failed to format device %s as %s for volume %s: %w",
    devicePath, fsType, volumeID, err)
```

### Anti-Patterns to Avoid
- **Using %v instead of %w:** Breaks error chains, prevents `errors.Is()` and `errors.As()` from working
- **Logging then wrapping:** Creates duplicate log entries (see Phase 18 decisions about logging at V(4) vs V(2))
- **Wrapping already-wrapped gRPC status:** gRPC status codes should be terminal, not wrapped again
- **Missing context:** Generic "operation failed" without volumeID, operation, or reason
- **Exposing internal details:** Returning raw SSH errors, IP addresses, or file paths to users
- **Silent failures:** Returning nil or logging without propagating errors

## Don't Hand-Roll

Problems that look simple but have existing solutions:

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Error sanitization | String replacement functions | `pkg/utils/errors.go` SanitizedError | Already handles IP, path, hostname removal with regex patterns |
| gRPC error codes | Custom error→code mapping | `google.golang.org/grpc/status` | Standard gRPC status package with proper code semantics |
| Error inspection | String contains checks | `errors.Is()`, `errors.As()` | Works with wrapped errors, type-safe |
| Multiple error aggregation | Manual concatenation | `errors.Join()` (Go 1.20+) | Properly aggregates errors with Unwrap support |
| Contextual logging | Manual fmt.Sprintf | klog with structured fields | Already used in codebase, supports verbosity levels |

**Key insight:** The codebase already has sophisticated error infrastructure (`pkg/utils/errors.go`) with 385 lines of sanitization logic, error classification (Internal/User/Validation), and security-aware logging. Don't create competing patterns—use and document the existing infrastructure.

## Common Pitfalls

### Pitfall 1: Converting %v to %w Without Understanding Implications
**What goes wrong:** Blindly changing all `%v` to `%w` can expose internal implementation details as part of your API
**Why it happens:** %w makes the error part of your public API contract, allowing callers to depend on specific error types
**How to avoid:** For each error, decide: "Should callers be able to inspect this error with errors.Is/As?" If yes, use %w. If it's an implementation detail that might change, use %v.
**Warning signs:** Exposing `*os.PathError`, SSH connection errors, or database-specific errors to CSI layer

**Example:**
```go
// BAD - exposes SSH implementation detail
return fmt.Errorf("volume creation failed: %w", sshErr)
// Now callers can detect ssh.ConnectError, coupling them to our SSH choice

// GOOD - hides implementation, preserves context
return fmt.Errorf("volume creation failed: %v", sshErr)
// OR create domain-specific error
return fmt.Errorf("RDS connection failed: %w", ErrRDSUnavailable)
```

### Pitfall 2: Missing Operation Context in Multi-Layer Calls
**What goes wrong:** Error messages like "connection refused" without indicating which connection to what
**Why it happens:** Lower layers don't know the operation context (volume ID, node ID, etc.)
**How to avoid:** Each layer must add its own context. The layer that has volumeID must include it before passing down.
**Warning signs:** Debugging requires reading code to understand which operation failed

**Example:**
```go
// BAD - loses volume context when calling SSH
err := sshClient.RunCommand("/disk add ...")
if err != nil {
    return fmt.Errorf("command failed: %w", err)
}
// Error: "command failed: connection refused"
// Missing: Which volume? Which RDS server?

// GOOD - preserves context
err := sshClient.RunCommand("/disk add ...")
if err != nil {
    return fmt.Errorf("failed to create volume %s on RDS %s: %w", volumeID, rdsAddr, err)
}
// Error: "failed to create volume pvc-123 on RDS 198.51.100.20: connection refused"
```

### Pitfall 3: Wrapping gRPC Status Errors
**What goes wrong:** gRPC status codes become nested and lose their semantic meaning
**Why it happens:** Treating gRPC status.Error like regular errors and wrapping them
**How to avoid:** gRPC status errors are terminal—convert to them, don't wrap them. Extract info and create new status if needed.
**Warning signs:** Error handling code can't extract status.Code() from wrapped errors

**Example:**
```go
// BAD - wraps gRPC status
statusErr := status.Error(codes.InvalidArgument, "volume ID required")
return fmt.Errorf("validation failed: %w", statusErr)
// gRPC can no longer extract the InvalidArgument code

// GOOD - status errors are terminal
return status.Error(codes.InvalidArgument, "volume ID is required")

// If you need to add context, create new status
if err != nil {
    return status.Errorf(codes.Internal, "failed to create volume %s: %v", volumeID, err)
}
```

### Pitfall 4: Inconsistent Error Classification
**What goes wrong:** Some errors use `status.Error` with codes, others use `fmt.Errorf`, no clear boundary
**Why it happens:** No documented convention for where to convert to gRPC status
**How to avoid:** Convert to gRPC status ONLY at CSI service method boundaries. Internal code uses fmt.Errorf.
**Warning signs:** status.Error calls scattered throughout internal packages

**Example:**
```go
// BAD - internal package returns gRPC status
// pkg/nvme/nvme.go
func Connect(target Target) (string, error) {
    return "", status.Error(codes.Internal, "connection failed")
}

// GOOD - internal returns Go error, CSI boundary converts
// pkg/nvme/nvme.go
func Connect(target Target) (string, error) {
    return "", fmt.Errorf("failed to connect to NQN %s: %w", target.NQN, err)
}

// pkg/driver/node.go (CSI boundary)
func (ns *NodeServer) NodeStageVolume(...) (*csi.NodeStageVolumeResponse, error) {
    devicePath, err := ns.nvmeConn.Connect(target)
    if err != nil {
        return nil, status.Errorf(codes.Internal, "NVMe connection failed: %v", err)
    }
    return response, nil
}
```

### Pitfall 5: Silent Error Path Changes
**What goes wrong:** Errors get logged but not returned, or returned nil despite errors occurring
**Why it happens:** Code evolution where error handling is added without full propagation audit
**How to avoid:** Audit all error paths—every error should be logged once and returned up to caller or handled definitively
**Warning signs:** `if err != nil { klog.Errorf(...) }` without return, functions returning success despite logged errors

## Code Examples

Verified patterns from official sources and current codebase:

### Converting %v to %w in Existing Code
```go
// Source: Current codebase - 6 instances needing conversion

// BEFORE (pkg/mount/mount.go:644)
if inUse {
    return fmt.Errorf("refusing to force unmount %s: mount is in use by processes: %v", target, pids)
}

// AFTER - %v is correct here (pids is []int, not an error)
// No change needed - this is a value, not error wrapping

// BEFORE (pkg/driver/controller.go:948)
if !supported {
    return fmt.Errorf("access mode %v is not supported", accessMode)
}

// AFTER - %v is correct here (accessMode is not an error)
// No change needed - this is formatting an enum value

// PATTERN: Only convert %v to %w when wrapping errors, not when formatting non-error values
```

### Proper gRPC Status Code Usage
```go
// Source: gRPC official docs + current codebase pkg/driver/

// Input validation
if volumeID == "" {
    return nil, status.Error(codes.InvalidArgument, "volume ID is required")
}

// Validation with wrapped context
if err := utils.ValidateVolumeID(volumeID); err != nil {
    return nil, status.Errorf(codes.InvalidArgument, "invalid volume ID: %v", err)
}

// Internal system errors
if err := operation(); err != nil {
    return nil, status.Errorf(codes.Internal, "operation failed: %v", err)
}

// Not found
if errors.Is(err, ErrNotFound) {
    return nil, status.Errorf(codes.NotFound, "volume %s not found", volumeID)
}

// Already exists
if errors.Is(err, ErrAlreadyExists) {
    return nil, status.Errorf(codes.AlreadyExists, "volume %s already exists", volumeID)
}

// Resource exhausted
if isOutOfSpace(err) {
    return nil, status.Errorf(codes.ResourceExhausted, "insufficient storage capacity")
}

// Unavailable (transient, retry recommended)
if isConnectionError(err) {
    return nil, status.Errorf(codes.Unavailable, "RDS temporarily unavailable")
}
```

### Error Context Pattern Template
```go
// Source: Best practices synthesis from research + current codebase

// Template for adding error context at each layer
func OperationName(volumeID string, params Params) error {
    // Validate inputs first
    if volumeID == "" {
        return fmt.Errorf("volume ID is required")
    }

    // Call lower layer
    err := lowerLayer.Operation(params)
    if err != nil {
        // Add context: operation + key identifiers + wrapped error
        return fmt.Errorf("failed to perform operation for volume %s: %w", volumeID, err)
    }

    return nil
}

// CSI boundary: convert to gRPC status
func (cs *ControllerServer) CSIMethod(ctx context.Context, req *Request) (*Response, error) {
    volumeID := req.GetVolumeId()

    // Validate at boundary
    if volumeID == "" {
        return nil, status.Error(codes.InvalidArgument, "volume ID is required")
    }

    // Call internal operation
    err := cs.driver.OperationName(volumeID, params)
    if err != nil {
        // Convert to appropriate gRPC code
        if errors.Is(err, ErrNotFound) {
            return nil, status.Errorf(codes.NotFound, "volume %s not found", volumeID)
        }
        return nil, status.Errorf(codes.Internal, "operation failed: %v", err)
    }

    return response, nil
}
```

### Using Existing Error Sanitization
```go
// Source: Current codebase pkg/utils/errors.go

// For internal errors (hide implementation details)
if err := sshClient.Connect(); err != nil {
    sanitized := utils.NewInternalError(err, "RDS connection failed")
    // sanitized.Error() returns: "RDS connection failed"
    // sanitized.Log() logs: "[INTERNAL ERROR] ... (internal: failed to connect to 198.51.100.20:22: ...)"
    return sanitized
}

// For user-facing errors (automatic sanitization)
if err := validateVolume(volumeID); err != nil {
    sanitized := utils.NewUserError(err, "Volume validation")
    // Automatically removes IPs, paths, hostnames
    return sanitized
}

// For validation errors (safe to show users)
if !isValidSize(size) {
    return utils.NewValidationError("size", "must be positive")
}

// Add context to sanitized errors
sanitized := utils.NewInternalError(err, "Operation failed").
    WithContext("volumeID", volumeID).
    WithContext("nodeID", nodeID)
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| `errors.New()` + string concat | `fmt.Errorf` with `%w` | Go 1.13 (2019) | Error wrapping standard, enables `errors.Is/As` |
| `github.com/pkg/errors` | Standard library `errors` | Go 1.13 (2019) | pkg/errors deprecated in favor of stdlib |
| String matching for errors | `errors.Is()`, `errors.As()` | Go 1.13 (2019) | Type-safe error inspection through wrap chains |
| Custom error wrapping | `errors.Join()` for multiple | Go 1.20 (2023) | Standard way to aggregate multiple errors |
| `%v` everywhere | `%w` for wrapping | Go 1.13 (2019) | Explicit intent: %w = expose, %v = hide |
| Generic CSI errors | Specific gRPC codes | CSI spec v1.0+ | Better error semantics for Kubernetes |

**Deprecated/outdated:**
- `github.com/pkg/errors`: Replaced by stdlib `fmt.Errorf` with `%w` and `errors` package functions
- `%v` for error wrapping: Use `%w` when you want to preserve error chain
- String contains for error types: Use `errors.Is()` for sentinel errors, `errors.As()` for type checks
- Direct `== err` comparison: Use `errors.Is(err, target)` to check through wrap chains

## Open Questions

Things that couldn't be fully resolved:

1. **Should pkg/utils/errors.go SanitizedError always use %w or %v for inner errors?**
   - What we know: SanitizedError wraps errors and provides Unwrap() method
   - What's unclear: Whether sanitized errors should always allow errors.Is/As inspection
   - Recommendation: Use %w internally in SanitizedError since it already provides controlled exposure via sanitization. Document that callers should use errors.Is/As with the sanitized wrapper.

2. **How to handle error classification at gRPC boundaries?**
   - What we know: gRPC has 17 status codes, need mapping from Go errors
   - What's unclear: Should we define sentinel errors (ErrNotFound, ErrAlreadyExists) for automatic mapping?
   - Recommendation: Define package-level sentinel errors in pkg/utils for common cases (NotFound, AlreadyExists, Unavailable), use errors.Is() at CSI boundary for code selection

3. **Linter configuration for error wrapping enforcement?**
   - What we know: errorlint and wrapcheck can enforce patterns, but have known conflicts
   - What's unclear: Optimal configuration for CSI driver use case
   - Recommendation: Enable errorlint to catch %v misuse, configure wrapcheck with ignoreSigs for status.Error (don't require wrapping gRPC status)

## Sources

### Primary (HIGH confidence)
- [Working with Errors in Go 1.13 - Official Go Blog](https://go.dev/blog/go1.13-errors) - Error wrapping with %w, best practices
- [gRPC Status Codes - Official gRPC Docs](https://grpc.io/docs/guides/status-codes/) - All 17 codes and usage guidance
- [errors package - Official Go Docs](https://pkg.go.dev/errors) - errors.Is, errors.As, errors.Unwrap
- Current codebase: pkg/utils/errors.go (385 lines), pkg/driver/node.go, pkg/driver/controller.go - Existing sophisticated error infrastructure

### Secondary (MEDIUM confidence)
- [A practical guide to error handling in Go - Datadog (2024)](https://www.datadoghq.com/blog/go-error-handling/) - Modern patterns including structured logging
- [Best Practices for Error Handling in Go - JetBrains Guide](https://www.jetbrains.com/guide/go/tutorials/handle_errors_in_go/best_practices/) - Context, wrapping, handling layers
- [Context Matters: Advanced Error Handling Techniques in Go - Medium](https://medium.com/@oberonus/context-matters-advanced-error-handling-techniques-in-go-b470f763c7ec) - Operation context patterns
- [Staticcheck - Go Linter](https://staticcheck.dev/) - Static analysis for error patterns

### Tertiary (LOW confidence)
- [Go Error Handling in 2026: Complete Guide - Copy Programming](https://copyprogramming.com/howto/go-creare-a-new-error-golang-code-example) - Community synthesis (lacks official attribution)
- [wrapcheck - GitHub](https://github.com/tomarrell/wrapcheck) - Linter for error wrapping (known conflicts with errorlint)

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH - Official Go documentation, well-established since 1.13
- Architecture: HIGH - Current codebase already implements most patterns correctly
- Pitfalls: HIGH - Combination of official docs and observed codebase patterns
- gRPC conversion: HIGH - Official gRPC documentation and CSI spec requirements

**Research date:** 2026-02-04
**Valid until:** 2026-08-04 (6 months - stable, Go error patterns unlikely to change)

**Codebase analysis:**
- Total Go files with fmt.Errorf: 29 files
- Uses %w: 147 instances across 27 files
- Uses %v: 6 instances across 6 files (needs audit, likely formatting non-errors)
- gRPC status usage: 94 instances across 4 files (correct boundary usage)
- Existing error infrastructure: pkg/utils/errors.go with SanitizedError (385 lines)
