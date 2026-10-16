---
phase: 32-resilience-regression-tests
plan: 02
subsystem: testing
tags: [hardware-validation, resilience, nvme, reconnect, node-failure, documentation]

# Dependency graph
requires:
  - phase: 32-resilience-regression-tests
    provides: Automated resilience E2E tests (RESIL-01/02/03) from plan 01 that this documentation references
provides:
  - TC-09 (NVMe reconnect after network interruption) hardware validation procedure
  - TC-10 (RDS restart volume preservation) hardware validation procedure
  - TC-11 (node failure stale VolumeAttachment cleanup) hardware validation procedure
  - Updated TESTING.md with resilience regression test references
affects:
  - hardware validation runs (use TC-09/10/11 procedures for resilience checks)
  - future changes to pkg/rds/connection_manager.go (must pass RESIL-01/02)
  - future changes to pkg/attachment/reconciler.go (must pass RESIL-03)

# Tech tracking
tech-stack:
  added: []
  patterns:
    - "iptables block/restore pattern for NVMe/TCP interruption simulation in hardware tests"
    - "ctrl_loss_tmo=-1 infinite retry verified in TC-09 success criteria"
    - "exponential backoff reconnection verified in TC-10 success criteria"
    - "attachment reconciler 5-min interval with 30s grace period verified in TC-11"

key-files:
  created:
    - .planning/phases/32-resilience-regression-tests/32-02-SUMMARY.md
  modified:
    - docs/HARDWARE_VALIDATION.md
    - docs/TESTING.md

key-decisions:
  - "TC-09 uses iptables OUTPUT chain block (not INPUT) to simulate NVMe/TCP disruption from the node's perspective"
  - "TC-10 DANGER warning documents full cluster impact of RDS restart (all NVMe/TCP connections on all nodes)"
  - "TC-11 uses controller pod restart to trigger immediate reconciliation (faster than 5-minute interval)"
  - "TESTING.md cross-references HARDWARE_VALIDATION.md TC-09/10/11 to clearly separate mock-tested vs hardware-validated resilience behaviors"

patterns-established:
  - "Hardware test cases include both expected happy path AND failure mode troubleshooting for each scenario"
  - "CAUTION/DANGER boxes in hardware test cases signal blast radius and maintenance window requirements"

# Metrics
duration: 8min
completed: 2026-02-18
---

# Phase 32 Plan 02: Resilience Hardware Validation Test Cases Summary

**TC-09/TC-10/TC-11 documented in HARDWARE_VALIDATION.md providing step-by-step resilience validation procedures: NVMe reconnect via iptables, RDS restart data preservation, and node failure stale attachment cleanup**

## Performance

- **Duration:** ~8 min
- **Started:** 2026-02-18T17:07:00Z
- **Completed:** 2026-02-18T17:15:59Z
- **Tasks:** 3 (2 auto tasks + 1 checkpoint human-verify, approved)
- **Files modified:** 2

## Accomplishments

- Documented TC-09: NVMe reconnect after network interruption — iptables block/restore with ctrl_loss_tmo=-1 verification
- Documented TC-10: RDS restart volume preservation — full reboot procedure with Btrfs data integrity check
- Documented TC-11: Node failure stale VolumeAttachment cleanup — cordon/drain/delete node + reconciler verification
- Updated Results Template table with TC-09, TC-10, TC-11 rows
- Updated TESTING.md with resilience_test.go E2E coverage, new Resilience Regression Tests subsection, and hardware cross-references

## Task Commits

Each task was committed atomically:

1. **Task 1: Add resilience test cases TC-09, TC-10, TC-11 to HARDWARE_VALIDATION.md** - `9e14c9f` (docs)
2. **Task 2: Update TESTING.md with resilience test references** - `a160f5c` (docs)
3. **Task 3: Review resilience test documentation** - checkpoint:human-verify (approved by user 2026-02-18)

## Files Created/Modified

- `docs/HARDWARE_VALIDATION.md` - Added 604 lines: TC-09 (NVMe reconnect), TC-10 (RDS restart), TC-11 (node failure), updated Results Template
- `docs/TESTING.md` - Added 38 lines: resilience_test.go references, new Resilience Regression Tests subsection, CSI Capability Matrix resilience note

## Decisions Made

- TC-09 uses `iptables -A OUTPUT -d 198.51.100.20 -p tcp --dport 4420 -j DROP` on the worker node (OUTPUT chain intercepts outbound NVMe/TCP from node to RDS)
- TC-10 includes a DANGER warning documenting that RDS restart affects ALL NVMe/TCP connections on ALL cluster nodes — not just the test pod
- TC-11 documents restarting the controller pod as a way to trigger immediate reconciliation rather than waiting the full 5-minute interval
- TESTING.md explicitly separates what mock tests validate (RESIL-01/02/03 logic) from what hardware tests validate (kernel NVMe reconnect, Btrfs persistence, real node crash)

## Deviations from Plan

None - plan executed exactly as written.

## Issues Encountered

None.

## User Setup Required

None - no external service configuration required.

## Next Phase Readiness

- All resilience documentation complete (Phase 32 plans 01 and 02)
- Hardware validation procedures ready for execution: TC-09, TC-10, TC-11
- v0.11.0 data protection milestone documentation complete
- Automated resilience regression tests (RESIL-01/02/03) protect connection manager and attachment reconciler from regressions

## Self-Check: PASSED

- FOUND: docs/HARDWARE_VALIDATION.md (2501 lines, +604 from 1898 baseline)
- FOUND: docs/TESTING.md (updated with 12 resilience references)
- FOUND: .planning/phases/32-resilience-regression-tests/32-02-SUMMARY.md
- FOUND: commit 9e14c9f (Task 1: HARDWARE_VALIDATION.md TC-09/10/11)
- FOUND: commit a160f5c (Task 2: TESTING.md resilience references)
- TC headers verified: TC-09, TC-10, TC-11 all present in HARDWARE_VALIDATION.md (3/3)
- Resilience references verified: 12 occurrences in TESTING.md (minimum 3 required)

---
*Phase: 32-resilience-regression-tests*
*Completed: 2026-02-18*
