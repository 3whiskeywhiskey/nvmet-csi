---
phase: quick
plan: 002
type: execute
wave: 1
depends_on: []
files_modified:
  - pkg/attachment/reconciler.go
  - pkg/attachment/reconciler_test.go
autonomous: true

must_haves:
  truths:
    - "Stop() returns promptly even when triggerCh or ticker are simultaneously ready"
    - "TestTriggerReconcile_AfterStop passes reliably under race detector and stress"
    - "All existing reconciler tests continue to pass"
  artifacts:
    - path: "pkg/attachment/reconciler.go"
      provides: "Race-free shutdown in run() loop"
      contains: "stopCh priority check"
    - path: "pkg/attachment/reconciler_test.go"
      provides: "Robust test with no timing-dependent flakiness"
  key_links:
    - from: "run() select loop"
      to: "Stop() doneCh wait"
      via: "stopCh closure detected before processing other cases"
      pattern: "case <-stopCh"
---

<objective>
Fix a potential deadlock/hang in AttachmentReconciler.Stop() caused by Go's random select case ordering.

Purpose: When Stop() closes stopCh, the run() goroutine's select may randomly pick triggerCh or ticker.C first (if they are also ready), causing an extra reconcile() pass before noticing the stop signal. On slow/loaded machines (CI), this delay can cause test timeouts that appear as deadlocks.

Output: A race-free shutdown pattern in run() that prioritizes stopCh, plus a hardened test.
</objective>

<execution_context>
@~/.claude/get-shit-done/workflows/execute-plan.md
@~/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@pkg/attachment/reconciler.go
@pkg/attachment/reconciler_test.go
</context>

<tasks>

<task type="auto">
  <name>Task 1: Fix stopCh priority in run() loop and harden test</name>
  <files>pkg/attachment/reconciler.go, pkg/attachment/reconciler_test.go</files>
  <action>
In `pkg/attachment/reconciler.go`, fix the `run()` method's select loop (lines 174-189) to prioritize `stopCh` and `ctx.Done()` over `triggerCh` and `ticker.C`. Use the standard Go priority-select pattern:

Replace the current single `select` with a two-stage check:

```go
for {
    // Priority check: stop signals take precedence over work
    select {
    case <-stopCh:
        klog.V(2).Info("Attachment reconciler shutting down")
        return
    case <-ctx.Done():
        klog.V(2).Info("Attachment reconciler context cancelled")
        return
    default:
    }

    // Wait for work or stop signal
    select {
    case <-ticker.C:
        klog.V(2).Info("Attachment reconciliation triggered by periodic timer")
        r.reconcile(ctx)
    case <-triggerCh:
        klog.V(2).Info("Attachment reconciliation triggered by node event")
        r.reconcile(ctx)
    case <-stopCh:
        klog.V(2).Info("Attachment reconciler shutting down")
        return
    case <-ctx.Done():
        klog.V(2).Info("Attachment reconciler context cancelled")
        return
    }
}
```

The first non-blocking select (with `default`) ensures that if stopCh is already closed when we loop back after a reconcile() call, we exit immediately instead of randomly picking another case.

In `pkg/attachment/reconciler_test.go`, update `TestTriggerReconcile_AfterStop`:
1. Change `Interval` from `100 * time.Millisecond` to `1 * time.Hour` (we do not need the ticker to fire in this test; a long interval eliminates any timing sensitivity)
2. Keep the `time.Sleep(10 * time.Millisecond)` as-is (sufficient for goroutine startup)
3. The test logic remains the same: Start, sleep, Stop, TriggerReconcile, verify no panic

Also update `TestReconciler_StartStop` similarly: change its `Interval` from `100 * time.Millisecond` to `1 * time.Hour` since the test only verifies Start/Stop lifecycle, not periodic reconciliation.
  </action>
  <verify>
Run the following commands:
1. `go build ./pkg/attachment/` -- compiles without errors
2. `go test -v -run "TestTriggerReconcile_AfterStop" -count=100 -race -timeout 60s ./pkg/attachment/` -- all 100 iterations pass
3. `go test -v -race -timeout 30s ./pkg/attachment/` -- all reconciler tests pass
4. `go vet ./pkg/attachment/` -- no issues
  </verify>
  <done>
    - run() prioritizes stopCh/ctx.Done() before processing triggerCh or ticker cases
    - TestTriggerReconcile_AfterStop passes 100/100 with -race flag
    - All other reconciler tests continue passing
    - No race conditions detected
  </done>
</task>

</tasks>

<verification>
- `go test -v -race -count=100 -timeout 120s ./pkg/attachment/` passes all iterations
- `go test -race ./...` shows no regressions in other packages (ignore pre-existing pkg/driver failures)
</verification>

<success_criteria>
- The priority-select pattern in run() guarantees Stop() returns promptly regardless of Go's random select ordering
- All attachment package tests pass reliably under stress with race detector
- No behavioral change to reconciliation logic (only shutdown ordering improved)
</success_criteria>

<output>
After completion, create `.planning/quick/002-fix-testtriggerreconcile-afterstop-deadl/002-SUMMARY.md`
</output>
