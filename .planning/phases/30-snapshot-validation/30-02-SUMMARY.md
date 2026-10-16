---
phase: 30-snapshot-validation
plan: 02
subsystem: testing
tags: [csi, sanity, snapshot, copy-from, mock, nvme]

# Dependency graph
requires:
  - phase: 30-snapshot-validation
    provides: "Mock RDS server with copy-from snapshot semantics (Plan 01)"
  - phase: 29-snapshot-implementation-fix
    provides: "Rewritten pkg/rds/commands.go and CSI controller snapshot RPCs"
provides:
  - "CSI sanity test suite passing 70/70 tests including all snapshot test cases"
  - "Updated TC-08 hardware validation reflecting copy-from snapshot semantics"
affects: ["phase-31", "phase-32", "hardware-validation"]

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "Snapshot ID derived from CSI name only (UUID v5 of name), not source volume — CSI spec compliance"
    - "source-volume field in mock SSH output avoids reverse-engineering source from slot name"
    - "RouterOS slot~ wildcard query simulated in mock via strings.Contains"
    - "Mock server address must be 127.0.0.1 (not localhost) — NodeStageVolume validates strict IP"

key-files:
  created: []
  modified:
    - pkg/utils/snapshotid.go
    - test/mock/rds_server.go
    - pkg/rds/commands.go
    - pkg/rds/commands_test.go
    - test/sanity/sanity_test.go
    - docs/HARDWARE_VALIDATION.md

key-decisions:
  - "GenerateSnapshotID now derives ID from CSI snapshot name only (UUID v5), not from source volume. CSI spec requires same name always produces same ID regardless of source — enables AlreadyExists detection across different sources."
  - "source-volume field added to mock formatSnapshotDetail output so parseSnapshotInfo recovers SourceVolume directly, avoiding extraction from slot name which no longer embeds source UUID."
  - "Removed fallback ExtractSourceVolumeIDFromSnapshotID from parseSnapshotInfo — slot UUID is now name-hash not source UUID, so fallback would produce wrong source."
  - "TC-08 SSH verification changed from /disk/btrfs/subvolume/print to /disk print detail where slot~snap- to match actual copy-from disk semantics."

patterns-established:
  - "CSI snapshot idempotency: look up existing snapshot by ID (derived from name), compare source volumes, return AlreadyExists if source differs — never use timestamp-based IDs for new snapshots."
  - "Mock server slot~ wildcard: explicit check for slot~ prefix before exact slot= lookup enables RouterOS-compatible wildcard query simulation."

# Metrics
duration: 35min
completed: 2026-02-18
---

# Phase 30 Plan 02: Snapshot Validation Summary

**CSI sanity snapshot tests fixed to 70/70 passing by correcting snapshot ID generation, mock server address, idempotency source-volume tracking, and slot~ wildcard query handling; TC-08 updated for copy-from semantics.**

## Performance

- **Duration:** ~35 min
- **Started:** 2026-02-18T00:00:00Z
- **Completed:** 2026-02-18T00:35:00Z
- **Tasks:** 2
- **Files modified:** 6

## Accomplishments
- Fixed 5 distinct CSI sanity test failure categories — went from 56 passed / 14 failed to 70 passed / 0 failed
- Corrected snapshot ID generation: `GenerateSnapshotID` now derives ID from CSI name only (UUID v5), satisfying the CSI spec "same name = same ID regardless of source" requirement
- Updated TC-08 hardware validation: SSH verification commands now use `/disk print detail where slot~"snap-"` instead of deprecated Btrfs subvolume commands; expected output reflects file-backed disk format without NVMe export flags

## Task Commits

Each task was committed atomically:

1. **Task 1: Run CSI sanity snapshot tests and fix any failures** - `c7b2cc4` (fix)
2. **Task 2: Update TC-08 hardware validation for copy-from snapshot approach** - `78840a1` (docs)

**Plan metadata:** TBD (docs: complete plan — created in this session)

## Files Created/Modified
- `pkg/utils/snapshotid.go` - `GenerateSnapshotID` now bases ID only on CSI name via UUID v5; `ExtractSourceVolumeIDFromSnapshotID` marked deprecated (slot UUID no longer encodes source)
- `test/mock/rds_server.go` - Changed address to `127.0.0.1` (strict IP); added `source-volume` field to `formatSnapshotDetail`; added `slot~"pattern"` wildcard handling in `handleDiskPrintDetail`
- `pkg/rds/commands.go` - Removed fallback `ExtractSourceVolumeIDFromSnapshotID` call from `parseSnapshotInfo`; updated comment explaining slot UUID is now name-hash not source UUID
- `pkg/rds/commands_test.go` - Updated `TestParseSnapshotInfo` expectations: entries without explicit `source-volume` field now expect `expectSourceVolume: ""`
- `test/sanity/sanity_test.go` - Updated `TestSnapshotParameters` comment from Btrfs reference to copy-from reference
- `docs/HARDWARE_VALIDATION.md` - TC-08 fully updated: header, Step 4 SSH command and expected output, Step 7 delete verification, Success Criteria, Troubleshooting, Execution section, Results Template description

## Decisions Made

1. **Snapshot ID based on CSI name only** — CSI spec requires that the same snapshot name produces the same snapshot ID regardless of which source volume was specified. Embedding source UUID in the ID (old approach) means two CreateSnapshot calls with the same name but different sources produce different IDs, making it impossible to detect the conflict. New approach: `snap-<uuid5-of-name>-at-<first10hex>` — same name always produces same ID, enabling the driver to look up the existing snapshot and detect source mismatch.

2. **source-volume field in mock output** — Since slot UUID no longer encodes source volume, `parseSnapshotInfo` can no longer fall back to `ExtractSourceVolumeIDFromSnapshotID` to recover `SourceVolume`. Added explicit `source-volume="..."` field in `formatSnapshotDetail` so the parser recovers it directly from SSH output.

3. **Removed source extraction fallback from parseSnapshotInfo** — The fallback produced wrong results (returning the hash of the CSI name masquerading as a source volume UUID). Better to have `SourceVolume` be empty when not explicitly in output than silently return wrong data.

## Deviations from Plan

### Auto-fixed Issues

**1. [Rule 1 - Bug] Snapshot ID generation embedded source UUID instead of CSI name**
- **Found during:** Task 1 (Run CSI sanity snapshot tests)
- **Issue:** `GenerateSnapshotID` produced different IDs for same name with different sources, violating CSI spec. CSI sanity tests use synthetic volume names (`CreateSnapshot-volume-1-EE532494-3189CF06`), not `pvc-<uuid>` format, exposing the mismatch.
- **Fix:** Changed `GenerateSnapshotID` to derive ID from CSI name only via UUID v5: `snap-<uuid5-of-name>-at-<first10hex>`
- **Files modified:** `pkg/utils/snapshotid.go`
- **Verification:** `go test ./test/sanity/ -v -timeout 120s` — snapshot ID validation tests pass
- **Committed in:** `c7b2cc4` (Task 1 commit)

**2. [Rule 1 - Bug] Mock server used "localhost" instead of IP address**
- **Found during:** Task 1 (Run CSI sanity snapshot tests)
- **Issue:** `MockRDSServer.address` returned `"localhost"` but `ValidateIPAddress` in `NodeStageVolume` requires strict IP format, causing node service tests to fail with "invalid IP address: localhost"
- **Fix:** Changed mock server address to `"127.0.0.1"`
- **Files modified:** `test/mock/rds_server.go`
- **Verification:** Node service sanity tests pass (NodeStageVolume, NodePublishVolume etc.)
- **Committed in:** `c7b2cc4` (Task 1 commit)

**3. [Rule 1 - Bug] CreateSnapshot idempotency failure — wrong SourceVolume recovery**
- **Found during:** Task 1 (Run CSI sanity snapshot tests)
- **Issue:** When CreateSnapshot was called a second time (idempotency check), it looked up `existingSnapshot.SourceVolume` which was recovered via `ExtractSourceVolumeIDFromSnapshotID`. But that function extracted `pvc-<hash-of-name>` (slot UUID = name hash now), not the original source volume ID — causing the idempotency comparison to fail.
- **Fix:** Added `source-volume="..."` field to `formatSnapshotDetail` output in mock server; `parseSnapshotInfo` reads this field directly instead of reverse-engineering from slot name
- **Files modified:** `test/mock/rds_server.go`, `pkg/rds/commands.go`
- **Verification:** CSI sanity idempotency tests for CreateSnapshot pass
- **Committed in:** `c7b2cc4` (Task 1 commit)

**4. [Rule 1 - Bug] ListSnapshots slot~ wildcard not handled by mock**
- **Found during:** Task 1 (Run CSI sanity snapshot tests)
- **Issue:** Mock's `handleDiskPrintDetail` used `slot=([^\s]+)` regex which captured `~"snap-"` as the slot value when driver called `/disk print detail where slot~"snap-"`. It then did exact lookup for literal `~"snap-"`, finding nothing.
- **Fix:** Added explicit `slot~"pattern"` detection before exact `slot=` lookup, filtering volumes/snapshots via `strings.Contains(slot, pattern)`
- **Files modified:** `test/mock/rds_server.go`
- **Verification:** ListSnapshots returns correct snapshot entries; sanity tests pass
- **Committed in:** `c7b2cc4` (Task 1 commit)

**5. [Rule 1 - Bug] TestParseSnapshotInfo unit tests expected wrong SourceVolume after fallback removal**
- **Found during:** Task 1 (after fixing commands.go to remove fallback)
- **Issue:** Unit tests expected `expectSourceVolume: "pvc-<uuid>"` for entries without explicit `source-volume` field in output, relying on the now-removed `ExtractSourceVolumeIDFromSnapshotID` fallback
- **Fix:** Updated test cases to `expectSourceVolume: ""` for entries without explicit field; kept one test case with explicit `source-volume` field showing full expected value
- **Files modified:** `pkg/rds/commands_test.go`
- **Verification:** `go test ./pkg/... -count=1` passes
- **Committed in:** `c7b2cc4` (Task 1 commit)

---

**Total deviations:** 5 auto-fixed (all Rule 1 - Bug)
**Impact on plan:** All fixes necessary for CSI spec compliance and correct behavior. The snapshot ID generation change is the most significant — it aligns the implementation with the CSI spec's idempotency model. No scope creep.

## Issues Encountered

The baseline sanity test run revealed the test suite had been partially broken before this phase: 56 passed but 14 failures existed in the snapshot test area. This plan fixed all 14 failures plus any new failures that appeared after the Plan 01 mock server changes, achieving 70/70 passing.

## User Setup Required

None — no external service configuration required.

## Next Phase Readiness

- CSI sanity test suite is fully green (70/70), confirming CSI spec compliance including all snapshot operations
- TC-08 hardware validation is documented with correct SSH commands and expected output, ready for manual execution against real RDS hardware at 198.51.100.20
- Phase 31 (hardware validation execution) can proceed with confidence that the driver is correct per CSI spec
- Remaining concern: pre-existing race condition in `pkg/rds` tests under `-race` flag (TestReconnection_WithBackoff) is unrelated to snapshot work and pre-dates this phase

---
*Phase: 30-snapshot-validation*
*Completed: 2026-02-18*

## Self-Check: PASSED

All claimed files exist:
- FOUND: pkg/utils/snapshotid.go
- FOUND: test/mock/rds_server.go
- FOUND: pkg/rds/commands.go
- FOUND: pkg/rds/commands_test.go
- FOUND: test/sanity/sanity_test.go
- FOUND: docs/HARDWARE_VALIDATION.md
- FOUND: .planning/phases/30-snapshot-validation/30-02-SUMMARY.md

All task commits exist:
- FOUND: c7b2cc4 (fix(30-02): fix CSI sanity snapshot tests)
- FOUND: 78840a1 (docs(30-02): update TC-08 hardware validation)
