---
phase: quick
plan: 006
type: execute
wave: 1
depends_on: []
files_modified:
  - pkg/mount/mount.go
  - pkg/mount/mount_test.go
  - pkg/driver/node.go
  - pkg/driver/node_test.go
autonomous: true

must_haves:
  truths:
    - "blkid exit code 1 (device error) returns an error, not false/nil"
    - "blkid exit code 2 (no filesystem) returns false/nil as before"
    - "Format() refuses to run mkfs when IsFormatted returns an error"
    - "NodeStageVolume retries IsFormatted on transient device errors after NVMe connect"
    - "NodeStageVolume only formats when blkid explicitly confirms no filesystem"
    - "Existing volumes with filesystems are never reformatted due to transient blkid failures"
  artifacts:
    - path: "pkg/mount/mount.go"
      provides: "IsFormatted with exit code distinction, enhanced logging"
      contains: "exit status 1"
    - path: "pkg/driver/node.go"
      provides: "Retry logic for IsFormatted after NVMe connect"
      contains: "retrying IsFormatted"
  key_links:
    - from: "pkg/mount/mount.go:IsFormatted"
      to: "pkg/mount/mount.go:Format"
      via: "Format calls IsFormatted and checks error"
      pattern: "IsFormatted.*err != nil.*return"
    - from: "pkg/driver/node.go:NodeStageVolume"
      to: "pkg/mount/mount.go:IsFormatted"
      via: "retry loop around IsFormatted before format decision"
      pattern: "IsFormatted.*retry"
---

<objective>
Fix a critical data loss bug where blkid exit status 1 (device I/O error / device not ready) is treated
identically to exit status 2 (no filesystem found). On NVMe-oF reconnect after PXE boot, the device may
not be immediately ready for I/O. blkid fails with exit 1, IsFormatted returns (false, nil), and Format()
runs mkfs.ext4 -F on an existing volume, destroying all data.

Purpose: Prevent data loss on NVMe-oF reconnect by correctly distinguishing "device error" from "no filesystem"
Output: Safe IsFormatted/Format behavior with retry logic in NodeStageVolume
</objective>

<execution_context>
@~/.claude/get-shit-done/workflows/execute-plan.md
@~/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@pkg/mount/mount.go (IsFormatted at line 381, Format at line 342)
@pkg/mount/mount_test.go (TestIsFormatted at line 272, TestFormat at line 218)
@pkg/driver/node.go (NodeStageVolume at line 121, circuit breaker block at line 269)
@pkg/driver/node_test.go (mockMounter at line 22)
</context>

<tasks>

<task type="auto">
  <name>Task 1: Fix IsFormatted exit code handling and Format safety</name>
  <files>pkg/mount/mount.go, pkg/mount/mount_test.go</files>
  <action>
  **A. Fix IsFormatted() (line 381-398) to distinguish blkid exit codes:**

  Replace the current implementation that lumps exit 1 and exit 2 together. The new logic:
  - Exit 0 with output: device is formatted, return (true, nil)
  - Exit 0 with empty output: ambiguous, return (false, nil) - same as today
  - Exit 2: blkid explicitly says "no filesystem found", return (false, nil)
  - Exit 1: device error / cannot read device / device not found, return (false, error) with descriptive message like "blkid could not read device %s (exit status 1): device may not be ready"
  - Any other exit code: return (false, error) as today

  Parse the exit code properly. Use `exec.ExitError` type assertion to extract the actual exit code rather than string matching on the error message. This is more robust:
  ```go
  var exitErr *exec.ExitError
  if errors.As(err, &exitErr) {
      switch exitErr.ExitCode() {
      case 2:
          // blkid exit 2 = no filesystem found on device
          klog.V(4).Infof("IsFormatted: device %s has no filesystem (blkid exit 2)", device)
          return false, nil
      case 1:
          // blkid exit 1 = device error (I/O error, device not found, device not ready)
          // CRITICAL: Do NOT treat this as "not formatted" - this would cause data loss
          klog.Warningf("IsFormatted: blkid cannot read device %s (exit 1, output: %q) - device may not be ready", device, string(output))
          return false, fmt.Errorf("blkid cannot read device %s (exit status 1): device may not be ready or has I/O errors", device)
      default:
          return false, fmt.Errorf("blkid failed on %s with exit code %d: %w", device, exitErr.ExitCode(), err)
      }
  }
  ```

  Add `"errors"` to the import block.

  Add comprehensive klog logging at each decision point:
  - V(4) when blkid succeeds and returns a filesystem type
  - V(4) when blkid exit 2 (no filesystem)
  - Warning when blkid exit 1 (device error)

  **B. Verify Format() already handles errors correctly (line 342-378):**

  Format() already does:
  ```go
  formatted, err := m.IsFormatted(device)
  if err != nil {
      return fmt.Errorf("failed to check if device is formatted: %w", err)
  }
  ```

  This is already safe -- it returns the error rather than proceeding to mkfs. No change needed to Format() logic itself, but add a klog.Infof at V(2) level before the mkfs call to log the format decision:
  ```go
  klog.V(2).Infof("Format: device %s confirmed unformatted by blkid, proceeding with mkfs.%s", device, fsType)
  ```

  This creates an audit trail for any future format operations.

  **C. Update tests in mount_test.go:**

  1. Update `TestIsFormatted` to add test cases:
     - "device error exit 1" -> blkidExitCode=1, expectedResult=false, expectError=true
     - "unknown exit code" -> blkidExitCode=3, expectedResult=false, expectError=true
     - Keep existing test for "not formatted" (exit 2) -> false, no error

  2. Update `TestFormat` and `TestFormat_ErrorScenarios`: Add a test case where blkid returns exit code 1 (device error). Verify Format() returns an error and does NOT call mkfs. Use the command-aware mock pattern already used in TestFormat_ErrorScenarios. The test should verify that when blkid exits 1, Format returns an error containing "blkid cannot read device" or "failed to check if device is formatted".

  3. Update `TestFormatUnsupportedFilesystem` if needed - this test uses exit code 2 for the blkid mock, which should still work correctly.
  </action>
  <verify>
  Run `cd /path/to/rds-csi && make test` - all tests pass.
  Run specifically: `go test ./pkg/mount/ -v -run "TestIsFormatted|TestFormat"` - verify new test cases pass.
  Verify the "device error exit 1" test case returns (false, error).
  Verify the "not formatted exit 2" test case still returns (false, nil).
  </verify>
  <done>
  IsFormatted correctly distinguishes exit 1 (error) from exit 2 (no filesystem).
  Format refuses to mkfs when IsFormatted returns an error.
  All existing mount tests still pass, new test cases cover the exit code distinction.
  </done>
</task>

<task type="auto">
  <name>Task 2: Add retry logic in NodeStageVolume for transient device errors</name>
  <files>pkg/driver/node.go, pkg/driver/node_test.go</files>
  <action>
  **A. Add retry logic around IsFormatted in NodeStageVolume (line 269-299):**

  Inside the circuit breaker callback, replace the current simple IsFormatted + Format flow with a retry-aware flow. The current code at lines 272-285:

  ```go
  // Step 2a: Check filesystem health before mount (only for existing filesystems)
  formatted, formatErr := ns.mounter.IsFormatted(devicePath)
  if formatErr != nil {
      klog.Warningf("Could not check if device is formatted, skipping health check: %v", formatErr)
  } else if formatted {
      ...health check...
  }

  // Step 2b: Format filesystem if needed
  if formatErr := ns.mounter.Format(devicePath, fsType); formatErr != nil {
      return fmt.Errorf("failed to format device: %w", formatErr)
  }
  ```

  Replace with retry-aware logic. The key insight: after NVMe connect, the device may need a moment to become fully ready for I/O. IsFormatted returning an error (exit 1) is a transient condition that should be retried, NOT treated as "unformatted".

  New logic (inside the circuit breaker callback):

  ```go
  // Step 2a: Determine filesystem state with retry for transient device errors
  // After NVMe-oF connect, the device may not be immediately ready for I/O.
  // blkid exit 1 means "cannot read device" which is transient after connect.
  // blkid exit 2 means "no filesystem" which is definitive.
  // We retry on exit 1 errors to avoid mistakenly formatting an existing volume.
  const (
      isFormattedMaxRetries = 5
      isFormattedRetryDelay = 2 * time.Second
  )

  var formatted bool
  var formatCheckErr error

  for attempt := 1; attempt <= isFormattedMaxRetries; attempt++ {
      formatted, formatCheckErr = ns.mounter.IsFormatted(devicePath)
      if formatCheckErr == nil {
          // blkid succeeded or returned exit 2 (no fs) - we have a definitive answer
          break
      }

      // blkid returned an error (likely exit 1 - device not ready)
      if attempt < isFormattedMaxRetries {
          klog.Warningf("IsFormatted check failed for %s (attempt %d/%d): %v - retrying in %v",
              devicePath, attempt, isFormattedMaxRetries, formatCheckErr, isFormattedRetryDelay)
          select {
          case <-ctx.Done():
              return fmt.Errorf("context cancelled while waiting for device %s to be ready: %w", devicePath, ctx.Err())
          case <-time.After(isFormattedRetryDelay):
              // continue retry
          }
      } else {
          // All retries exhausted - device is not readable
          klog.Errorf("IsFormatted check failed for %s after %d attempts: %v - refusing to format to prevent data loss",
              devicePath, isFormattedMaxRetries, formatCheckErr)
          return fmt.Errorf("cannot determine filesystem state of device %s after %d attempts (last error: %w) - refusing to format to prevent potential data loss",
              devicePath, isFormattedMaxRetries, formatCheckErr)
      }
  }

  // Step 2b: Check filesystem health (only for existing filesystems)
  if formatted {
      klog.V(2).Infof("Running filesystem health check for %s", devicePath)
      if healthErr := mount.CheckFilesystemHealth(ctx, devicePath, fsType); healthErr != nil {
          return fmt.Errorf("filesystem health check failed: %w", healthErr)
      }
  }

  // Step 2c: Format filesystem if needed (only when blkid definitively confirmed no filesystem)
  if formatErr := ns.mounter.Format(devicePath, fsType); formatErr != nil {
      return fmt.Errorf("failed to format device: %w", formatErr)
  }
  ```

  Note: Format() itself calls IsFormatted() again internally, which provides a second check. After retries confirmed the device is readable (formatCheckErr == nil, formatted == false), the Format() call will also confirm via IsFormatted that there is no filesystem before running mkfs. This double-check is intentional safety.

  **B. Update mockMounter in node_test.go to support configurable IsFormatted behavior:**

  The current mockMounter.IsFormatted() always returns (true, nil). Update it to be configurable:

  Add fields to mockMounter struct:
  ```go
  isFormatted    bool
  isFormattedErr error
  ```

  Update IsFormatted method:
  ```go
  func (m *mockMounter) IsFormatted(device string) (bool, error) {
      return m.isFormatted, m.isFormattedErr
  }
  ```

  Set `isFormatted: true` in all existing test setups that create `&mockMounter{}` without explicitly setting isFormatted, to preserve the current default behavior. Search for all `&mockMounter{` instantiations and add `isFormatted: true` where needed to keep existing tests passing.

  IMPORTANT: Be careful not to change the behavior of existing tests. The default zero value of bool is false, so any mockMounter that previously relied on IsFormatted returning true MUST now explicitly set `isFormatted: true`. Scan ALL test functions to verify.

  **C. Add test for retry behavior:**

  Add a test case in node_test.go that verifies:
  1. When IsFormatted returns an error (simulating blkid exit 1), NodeStageVolume returns an error rather than formatting the device.
  2. Format is NOT called when IsFormatted keeps erroring (formatCalled should be false).

  The test should create a mockMounter with `isFormattedErr: fmt.Errorf("blkid cannot read device")` and verify that NodeStageVolume returns an error containing "refusing to format to prevent" or "cannot determine filesystem state".

  Note: Since the mock doesn't change behavior between retries, all 5 retries will fail and it will return the "refusing to format" error. This is the correct behavior to test -- the mock simulates a persistently unreadable device.

  For the context deadline, use a context with a generous timeout (30s) so the retries can complete without the context cancelling first, but the delay will make this test slow. To avoid that, consider adding a way to override the retry delay in tests. The simplest approach: extract the retry constants to package-level vars or accept that the test will take ~10s (5 retries * 2s). Given this is a critical safety test, 10s is acceptable. Alternatively, use a short context timeout (e.g., 3s) to verify the context cancellation path.
  </action>
  <verify>
  Run `cd /path/to/rds-csi && make test` - all tests pass.
  Run specifically: `go test ./pkg/driver/ -v -run "TestNodeStageVolume" -timeout 60s` - verify existing and new test cases pass.
  Run `make lint` to verify no linting issues.
  Run `make verify` for full verification.
  </verify>
  <done>
  NodeStageVolume retries IsFormatted up to 5 times with 2s delay on transient errors.
  After all retries fail, NodeStageVolume returns error and refuses to format.
  Existing NodeStageVolume tests pass unchanged.
  The mockMounter supports configurable IsFormatted behavior.
  No volume data can be lost due to transient blkid failures after NVMe-oF reconnect.
  </done>
</task>

</tasks>

<verification>
1. `make verify` passes (fmt + vet + lint + test)
2. `go test ./pkg/mount/ -v -run "TestIsFormatted"` shows exit 1 returns error, exit 2 returns false/nil
3. `go test ./pkg/mount/ -v -run "TestFormat"` shows Format refuses to mkfs on IsFormatted error
4. `go test ./pkg/driver/ -v -run "TestNodeStageVolume" -timeout 60s` shows retry logic works
5. Manual code review: grep for "exit status 1.*exit status 2" in mount.go should return NO results (old pattern gone)
6. Manual code review: grep for "ExitCode" in mount.go should return results (new pattern)
</verification>

<success_criteria>
- blkid exit 1 is NEVER treated as "not formatted" -- it always returns an error
- blkid exit 2 is correctly treated as "not formatted" (no regression)
- Format() propagates IsFormatted errors without running mkfs
- NodeStageVolume retries on transient device errors and refuses to format if device stays unreadable
- All existing tests pass without modification to their assertions
- make verify passes clean
</success_criteria>

<output>
After completion, create `.planning/quick/006-fix-blkid-race-condition-causing-data-lo/006-SUMMARY.md`
</output>
