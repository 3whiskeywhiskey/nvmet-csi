---
phase: quick
plan: 003
type: execute
wave: 1
depends_on: []
files_modified:
  - pkg/utils/volumeid.go
  - pkg/driver/controller_test.go
  - pkg/driver/node_test.go
autonomous: true

must_haves:
  truths:
    - "All 22 test failures are fixed and tests pass"
    - "`make test` passes with 0 failures in pkg/utils and pkg/driver"
    - "ValidateIPAddress rejects non-IP strings like hostnames, 999.999.999.999, not-an-ip"
    - "ControllerPublishVolume tests include VolumeCapability in requests"
    - "NodeGetVolumeStats tests properly mock IsLikelyMountPoint to return true"
  artifacts:
    - path: "pkg/utils/volumeid.go"
      provides: "Fixed ValidateIPAddress that rejects hostnames and invalid IPs"
    - path: "pkg/driver/controller_test.go"
      provides: "Fixed ControllerPublishVolume tests with VolumeCapability"
    - path: "pkg/driver/node_test.go"
      provides: "Fixed NodeGetVolumeStats tests with proper mount mock setup"
  key_links:
    - from: "pkg/utils/volumeid.go:ValidateIPAddress"
      to: "pkg/driver/node.go"
      via: "IP validation in NodeStageVolume context validation"
      pattern: "ValidateIPAddress"
---

<objective>
Fix all 22 test failures across pkg/utils (6 failures) and pkg/driver (16 failures) so CI coverage passes.

Purpose: These pre-existing test failures block CI verification and mask real regressions.
Output: All tests pass, `make test` exits cleanly for both packages.
</objective>

<execution_context>
@~/.claude/get-shit-done/workflows/execute-plan.md
@~/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@CLAUDE.md
@pkg/utils/volumeid.go
@pkg/utils/volumeid_test.go
@pkg/driver/controller_test.go
@pkg/driver/node_test.go
</context>

<tasks>

<task type="auto">
  <name>Task 1: Fix ValidateIPAddress to reject non-IP strings</name>
  <files>pkg/utils/volumeid.go</files>
  <action>
  Fix `ValidateIPAddress()` function (line ~192) to ONLY accept valid IPv4 and IPv6 addresses,
  NOT hostnames. The function currently falls through to hostname validation when `net.ParseIP()`
  returns nil, which causes "not-an-ip", "999.999.999.999", and "example.com" to pass validation.

  The fix: Remove the hostname validation fallback entirely. The function name is `ValidateIPAddress`,
  not `ValidateIPAddressOrHostname`. After `net.ParseIP()` returns nil, immediately return an error.

  Specifically:
  - Keep the empty check at the top
  - Keep the `net.ParseIP()` check
  - If `net.ParseIP()` returns nil, return error: `fmt.Errorf("invalid IP address: %s", address)`
  - Remove lines 206-218 (the hostname validation block)
  - Note: "999.999.999.999" also fails net.ParseIP() so it will correctly be rejected
  - Note: "256.256.256.256" also fails net.ParseIP() so it will correctly be rejected

  This fixes 6 test failures:
  - TestValidateIPAddress: "not-an-ip", "999.999.999.999" (which is "256.256.256.256" in the test named "invalid_IPv4_octets"), "example.com" (hostname)
  - TestValidateNVMEAddress/invalid_IP
  - TestValidateNVMETargetContext/invalid_address
  - TestNodeStageVolume_ErrorScenarios/invalid_IP_address_format
  </action>
  <verify>
  Run: `go test ./pkg/utils/ -run "TestValidateIPAddress|TestValidateNVMEAddress|TestValidateNVMETargetContext" -v`
  All subtests pass, including "invalid_IP_format", "invalid_IPv4_octets", "hostname_instead_of_IP".
  </verify>
  <done>All 6 IP validation test failures fixed. ValidateIPAddress only accepts valid IPs.</done>
</task>

<task type="auto">
  <name>Task 2: Fix ControllerPublishVolume tests to include VolumeCapability</name>
  <files>pkg/driver/controller_test.go</files>
  <action>
  Fix 5 ControllerPublishVolume test functions that are missing `VolumeCapability` in their requests.
  The implementation now validates `req.GetVolumeCapability() != nil` (line ~512 of controller.go),
  but these older tests don't set it. Add VolumeCapability to each test's request.

  Use this capability (RWO mount, matching the pattern used in other passing tests):
  ```go
  VolumeCapability: &csi.VolumeCapability{
      AccessMode: &csi.VolumeCapability_AccessMode{
          Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
      },
      AccessType: &csi.VolumeCapability_Mount{
          Mount: &csi.VolumeCapability_MountVolume{},
      },
  },
  ```

  Tests to fix (add VolumeCapability to the request struct):
  1. `TestControllerPublishVolume_Success` (~line 432) - already has VolumeContext, add VolumeCapability
  2. `TestControllerPublishVolume_Idempotent` (~line 476) - both req uses (first and second publish)
  3. `TestControllerPublishVolume_RWOConflict` (~line 510) - both req1 and req2
  4. `TestControllerPublishVolume_StaleAttachmentSelfHealing` (~line 562) - the req
  5. `TestControllerPublishVolume_VolumeNotFound` (~line 593) - the req

  IMPORTANT: For tests 2-5, the existing requests do NOT have VolumeContext either. That is OK,
  because VolumeContext is optional and the controller uses the RDS mock's address. Just add
  VolumeCapability to each request.
  </action>
  <verify>
  Run: `go test ./pkg/driver/ -run "TestControllerPublishVolume_Success|TestControllerPublishVolume_Idempotent|TestControllerPublishVolume_RWOConflict|TestControllerPublishVolume_StaleAttachmentSelfHealing|TestControllerPublishVolume_VolumeNotFound" -v`
  All 5 tests pass.
  </verify>
  <done>All 5 ControllerPublishVolume tests pass with VolumeCapability added.</done>
</task>

<task type="auto">
  <name>Task 3: Fix NodeGetVolumeStats tests to properly mock mount state</name>
  <files>pkg/driver/node_test.go</files>
  <action>
  Fix ~11 NodeGetVolumeStats test failures. The root cause: NodeGetVolumeStats calls
  `ns.mounter.IsLikelyMountPoint(volumePath)` and returns NotFound if it returns false or error.
  The test paths don't exist on disk, and the mock's `isLikelyMounted` defaults to `false`.

  Fix all NodeGetVolumeStats test setup functions to set `m.isLikelyMounted = true` on the mockMounter
  before the test calls NodeGetVolumeStats. This simulates that the volume is actually mounted.

  Specific changes:

  1. `TestNodeGetVolumeStats_AlwaysReturnsVolumeCondition` (~line 201):
     - In each subtest's `setupServer` func, set `m.isLikelyMounted = true` before returning
     - For the "healthy volume" case (line 213): add `m.isLikelyMounted = true`
     - For the "stale mount due to mount not found" case (line 230): add `m.isLikelyMounted = true` on the mockMounter param
     - For the "stale mount due to device disappeared" case (line 241): add `m.isLikelyMounted = true`
     - For the "stale check returns error" case (line 254): add `m.isLikelyMounted = true`
     - For the "invalid volume ID" case (line 268): add `m.isLikelyMounted = true`

  2. `TestNodeGetVolumeStats_UsageReported` (~line 380):
     - Add `isLikelyMounted: true` to the mockMounter initializer at line 381

  3. `TestNodeGetVolumeStats_StaleMountReturnsEmptyUsage` (~line 447):
     - Add `isLikelyMounted: true` to the mockMounter at line 448

  4. `TestNodeGetVolumeStats_MetricsRecorded` (~line 481):
     - Add `isLikelyMounted: true` to the mockMounter at line 482

  5. `TestNodeGetVolumeStats_VolumeConditionNeverNil` (~line 1212):
     - In each scenario's `setup()` func, set `isLikelyMounted: true` on the mockMounter:
       - "with stale checker" (line 1221): pass `&mockMounter{isLikelyMounted: true}`
       - "without stale checker" (line 1227): set `isLikelyMounted: true` on the mounter at line 1237
       - "invalid volume ID" (line 1243): pass `&mockMounter{isLikelyMounted: true}`

  Also fix 2 remaining node test failures:

  6. `TestNodeStageVolume_ErrorScenarios/invalid_IP_address_format` - This one will be fixed by Task 1
     (ValidateIPAddress fix). No changes needed in node_test.go for this.

  7. `TestCSI_NegativeScenarios_Node/NodeStageVolume:_invalid_nvmeAddress` - Also fixed by Task 1.
     No changes needed in node_test.go.
  </action>
  <verify>
  Run: `go test ./pkg/driver/ -run "TestNodeGetVolumeStats|TestNodeStageVolume_ErrorScenarios/invalid_IP|TestCSI_NegativeScenarios_Node/NodeStageVolume:_invalid_nvmeAddress" -v`
  All NodeGetVolumeStats tests pass. Both invalid IP tests pass.
  </verify>
  <done>All 11 NodeGetVolumeStats tests pass. VolumeCondition is always returned. Total: 16 driver test failures fixed.</done>
</task>

</tasks>

<verification>
Run the full test suite for both packages:

```bash
go test ./pkg/utils/ -v -count=1 2>&1 | tail -5
go test ./pkg/driver/ -v -count=1 2>&1 | tail -10
make test
```

All 22 previously failing tests now pass. No new test failures introduced.
</verification>

<success_criteria>
- `go test ./pkg/utils/` exits 0 with 0 failures
- `go test ./pkg/driver/` exits 0 with 0 failures
- `make test` exits 0
- All 22 specific test failures listed in the planning context are resolved
</success_criteria>

<output>
After completion, create `.planning/quick/003-fix-all-22-test-failures-in-pkg-utils-an/003-SUMMARY.md`
</output>
