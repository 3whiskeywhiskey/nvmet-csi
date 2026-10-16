---
phase: quick-007
plan: 01
type: execute
wave: 1
depends_on: []
files_modified:
  - test/mock/rds_server.go
  - pkg/driver/controller.go
  - pkg/utils/snapshotid.go
  - pkg/rds/commands.go
autonomous: true
must_haves:
  truths:
    - "Mock formatSnapshotDetail emits creation-time= field matching parseRouterOSTime format"
    - "ListSnapshots no longer calls ExtractSourceVolumeIDFromSnapshotID for source volume fallback"
    - "Dead code functions (GenerateSnapshotIDFromSource, ExtractSourceVolumeIDFromSnapshotID, ExtractTimestampFromSnapshotID) are removed from snapshotid.go"
    - "parseSnapshotInfo timestamp fallback using ExtractTimestampFromSnapshotID is removed"
    - "All tests pass (make test) and linter is clean (make lint)"
  artifacts:
    - path: "test/mock/rds_server.go"
      provides: "formatSnapshotDetail with creation-time= field"
      contains: "creation-time="
    - path: "pkg/utils/snapshotid.go"
      provides: "Snapshot ID utilities without dead code"
    - path: "pkg/driver/controller.go"
      provides: "ListSnapshots without deprecated fallback"
    - path: "pkg/rds/commands.go"
      provides: "parseSnapshotInfo without timestamp fallback"
  key_links:
    - from: "test/mock/rds_server.go formatSnapshotDetail"
      to: "pkg/rds/commands.go parseRouterOSTime"
      via: "creation-time= field in SSH output"
      pattern: "creation-time="
---

<objective>
Clean up snapshot tech debt: add creation-time= to mock output, remove deprecated ListSnapshots fallback, and delete dead code from snapshotid.go and commands.go.

Purpose: The v0.11.0 snapshot implementation evolved through phases 29-30, leaving behind deprecated functions and a fallback that returns wrong source IDs for new-format snapshot IDs. This cleanup removes the tech debt identified in the v0.11.0 milestone audit.

Output: Four files cleaned up, all tests passing, no dead code.
</objective>

<execution_context>
@~/.claude/get-shit-done/workflows/execute-plan.md
@~/.claude/get-shit-done/templates/summary.md
</execution_context>

<context>
@.planning/STATE.md
@.planning/v0.11.0-MILESTONE-AUDIT.md
@test/mock/rds_server.go (formatSnapshotDetail at ~line 836)
@pkg/driver/controller.go (ListSnapshots at ~line 1167)
@pkg/utils/snapshotid.go (dead functions at lines 61-136)
@pkg/rds/commands.go (parseSnapshotInfo fallback at ~line 1131)
</context>

<tasks>

<task type="auto">
  <name>Task 1: Add creation-time= to mock and remove timestamp fallback from parseSnapshotInfo</name>
  <files>test/mock/rds_server.go, pkg/rds/commands.go</files>
  <action>
  **test/mock/rds_server.go — formatSnapshotDetail (~line 836):**

  Add `creation-time=` field to the format string. The MockSnapshot struct already has a `CreatedAt time.Time` field populated with `time.Now()` at snapshot creation (line 626).

  Format the timestamp as `jan/02/2006 15:04:05` (RouterOS month/day/year format, lowercase month abbreviation) to match what `parseRouterOSTime` in commands.go expects.

  Change the format string from:
  ```go
  return fmt.Sprintf(`slot="%s" type="file" file-path="%s" file-size=%d source-volume="%s" status="ready"`,
      snap.Slot, snap.FilePath, snap.FileSizeBytes, snap.SourceVolume)
  ```
  To:
  ```go
  creationTime := snap.CreatedAt.Format("jan/02/2006 15:04:05")
  return fmt.Sprintf(`slot="%s" type="file" file-path="%s" file-size=%d source-volume="%s" creation-time=%s status="ready"`,
      snap.Slot, snap.FilePath, snap.FileSizeBytes, snap.SourceVolume, creationTime)
  ```

  Note: `time.Format` uses lowercase "jan" already when you use the reference time layout. The format layout string should be `"Jan/02/2006 15:04:05"` (Go reference time uses title-case "Jan"), and then lowercase it with `strings.ToLower()` since RouterOS uses lowercase month abbreviations. OR, since `parseRouterOSTime` already title-cases the month during parsing (line 724-725 of commands.go), emitting lowercase is correct. Use `strings.ToLower(snap.CreatedAt.Format("Jan/02/2006 15:04:05"))` to produce e.g. `feb/18/2026 14:30:00`.

  **pkg/rds/commands.go — parseSnapshotInfo (~lines 1131-1138):**

  Remove the timestamp fallback block. Change from:
  ```go
  // Extract creation time: try creation-time field first, then fall back to timestamp in slot name
  snapshot.CreatedAt = parseRouterOSTime(normalized)
  if snapshot.CreatedAt.IsZero() && snapshot.Name != "" {
      // Fall back: parse Unix timestamp from snap-<uuid>-at-<ts> slot name
      if ts, err := utils.ExtractTimestampFromSnapshotID(snapshot.Name); err == nil {
          snapshot.CreatedAt = time.Unix(ts, 0)
      }
  }
  ```
  To:
  ```go
  // Extract creation time from creation-time= field in disk output.
  // No fallback — the slot name suffix is a name-derived hash (not a Unix timestamp)
  // for deterministic snapshot IDs, so ExtractTimestampFromSnapshotID would fail.
  snapshot.CreatedAt = parseRouterOSTime(normalized)
  ```

  This removes the only call to `utils.ExtractTimestampFromSnapshotID` in commands.go. The `utils` import must remain (used extensively elsewhere in the file).
  </action>
  <verify>
  Run `make test` — all tests pass. Specifically verify snapshot-related tests still pass:
  `go test ./pkg/rds/... -run Snapshot -v`
  `go test ./test/... -v -count=1 2>&1 | head -100`
  </verify>
  <done>
  - formatSnapshotDetail emits creation-time= with RouterOS-format timestamp
  - parseSnapshotInfo uses only parseRouterOSTime (no fallback to ExtractTimestampFromSnapshotID)
  - All existing tests pass
  </done>
</task>

<task type="auto">
  <name>Task 2: Remove ListSnapshots fallback and delete dead code from snapshotid.go</name>
  <files>pkg/driver/controller.go, pkg/utils/snapshotid.go</files>
  <action>
  **pkg/driver/controller.go — ListSnapshots (~lines 1167-1186):**

  Remove the deprecated `ExtractSourceVolumeIDFromSnapshotID` fallback from the source volume filter. Change from:
  ```go
  // Filter by source volume if specified.
  // The SourceVolume field is populated by the RDS layer from the snapshot name using
  // ExtractSourceVolumeIDFromSnapshotID. As a defense-in-depth fallback, we also try
  // extracting from the snapshot name directly if SourceVolume is empty.
  if req.GetSourceVolumeId() != "" {
      filtered := make([]rds.SnapshotInfo, 0)
      for _, s := range allSnapshots {
          sourceVol := s.SourceVolume
          if sourceVol == "" {
              // Fallback: derive source volume from snapshot name (snap-<uuid>-at-<suffix>)
              if derived, err := utils.ExtractSourceVolumeIDFromSnapshotID(s.Name); err == nil {
                  sourceVol = derived
              }
          }
          if sourceVol == req.GetSourceVolumeId() {
              filtered = append(filtered, s)
          }
      }
      allSnapshots = filtered
  }
  ```
  To:
  ```go
  // Filter by source volume if specified.
  // SourceVolume is populated by parseSnapshotInfo from the source-volume= field in
  // RDS /disk print output. Snapshots without a source-volume field are excluded from
  // source-based filtering (they cannot be matched to a source volume).
  if req.GetSourceVolumeId() != "" {
      filtered := make([]rds.SnapshotInfo, 0)
      for _, s := range allSnapshots {
          if s.SourceVolume == req.GetSourceVolumeId() {
              filtered = append(filtered, s)
          }
      }
      allSnapshots = filtered
  }
  ```

  Verify the `utils` import is still needed in controller.go (it will be — used in CreateSnapshot, DeleteSnapshot, etc.).

  **pkg/utils/snapshotid.go — Delete dead functions:**

  Delete these three functions entirely:
  1. `GenerateSnapshotIDFromSource` (lines ~61-75) — zero callers after phase 30 switched to `GenerateSnapshotID`
  2. `ExtractSourceVolumeIDFromSnapshotID` (lines ~77-114) — only caller was the ListSnapshots fallback removed above
  3. `ExtractTimestampFromSnapshotID` (lines ~116-136) — only caller was the parseSnapshotInfo fallback removed in Task 1

  After deletion, remove unused imports: `strconv` and `time` are only used by the deleted functions. Keep: `fmt`, `regexp`, `strings`, `github.com/google/uuid`.

  The file should retain: `GenerateSnapshotID`, `ValidateSnapshotID`, `GenerateSnapshotIDLegacy`, `SnapshotNameToID`, constants, and patterns.
  </action>
  <verify>
  Run `make verify` (fmt + vet + lint + test) — everything passes clean.
  Confirm no remaining references: `grep -r "GenerateSnapshotIDFromSource\|ExtractSourceVolumeIDFromSnapshotID\|ExtractTimestampFromSnapshotID" pkg/`
  </verify>
  <done>
  - ListSnapshots uses s.SourceVolume directly without deprecated fallback
  - GenerateSnapshotIDFromSource, ExtractSourceVolumeIDFromSnapshotID, ExtractTimestampFromSnapshotID are deleted from snapshotid.go
  - Unused imports (strconv, time) removed from snapshotid.go
  - `make verify` passes clean (no lint errors, no test failures)
  - `grep` confirms zero remaining references to deleted functions in pkg/
  </done>
</task>

</tasks>

<verification>
1. `make verify` passes (fmt + vet + lint + test)
2. `grep -r "GenerateSnapshotIDFromSource\|ExtractSourceVolumeIDFromSnapshotID\|ExtractTimestampFromSnapshotID" pkg/` returns no matches
3. `grep "creation-time=" test/mock/rds_server.go` confirms mock emits the field
4. Snapshot-related tests pass: `go test ./pkg/rds/... -run Snapshot -v` and `go test ./pkg/driver/... -run Snapshot -v`
</verification>

<success_criteria>
- Mock formatSnapshotDetail emits creation-time= in RouterOS format
- parseSnapshotInfo has no fallback to ExtractTimestampFromSnapshotID
- ListSnapshots has no fallback to ExtractSourceVolumeIDFromSnapshotID
- Three dead functions deleted from snapshotid.go with unused imports cleaned
- `make verify` passes clean
</success_criteria>

<output>
After completion, create `.planning/quick/7-clean-up-snapshot-tech-debt-add-creation/7-SUMMARY.md`
</output>
