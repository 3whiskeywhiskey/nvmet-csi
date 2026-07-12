# Project State

## Project Reference

See `DESIGN.md` for current architecture and `docs/nvmet-csi-live-validation.md`
for dated hardware evidence. `.planning/milestones/` preserves the predecessor
RDS-focused development history.

**Current project:** backend-neutral `nvmet-csi`; Linux-ZOL primary,
RouterOS/RDS compatibility backend
**Core value:** volumes remain accessible and recoverable through NVMe/TCP
reconnections, controller restarts, and target-host reboot
**Current focus:** non-destructive RouterOS resilience validation and remaining
Linux-ZOL consumer-node/KubeVirt churn

## Current Position

The predecessor plans through v0.11.0 are archived. Post-fork backend
abstraction, Linux-ZOL lifecycle/reboot recovery, and isolated RouterOS
ordinary lifecycle are implemented and live-validated. Snapshot create,
restore, and list are unsupported on both backends after RouterOS TC-08 failed
live data-integrity validation.

## Performance Metrics

**By Milestone:**

| Milestone | Phases | Plans | Status |
|-----------|--------|-------|--------|
| v0.1.0-v0.8.0 | 1-21 | 79/79 | Shipped 2026-02-04 |
| v0.9.0 Production Readiness | 22-25.2 | 17/17 | Shipped 2026-02-06 |
| v0.10.0 Feature Enhancements | 26-28 | 19/19 | Shipped 2026-02-06 |
| v0.11.0 Data Protection | 29-32 | 7/7 | Shipped 2026-02-18 |

**Total:** 122 plans, 32 phases, 11 milestones, 7 quick tasks

## Accumulated Context

### Decisions

All decisions logged in PROJECT.md Key Decisions table.
v0.11.0 decisions archived — see `.planning/milestones/v0.11.0-ROADMAP.md` for details.

### Pending Todos

None.

### Blockers/Concerns

- RouterOS TC-08 proved `/disk add copy-from` does not preserve backing data and can disconnect an attached source; snapshots are disabled.
- RouterOS resilience TC-09-TC-11 remains pending live execution.
- A future snapshot design needs a proven data-copy primitive and new live validation.
- Current observed evidence is recorded in `docs/nvmet-csi-live-validation.md`.

### Quick Tasks Completed

| # | Description | Date | Commit | Directory |
|---|-------------|------|--------|-----------|
| 002 | Fix deployment manifests to use DaemonSet | 2026-02-07 | — | [002-fix-deployment-manifests](./quick/002-fix-deployment-manifests/) |
| 003 | Generate README badges and status | 2026-02-08 | — | [003-generate-readme-badges](./quick/003-generate-readme-badges/) |
| 004 | Update README and documentation | 2026-02-08 | — | [004-update-readme-and-documentation-to-refle](./quick/004-update-readme-and-documentation-to-refle/) |
| 005 | Fix README remove non-existent Helm instructions | 2026-02-08 | — | [005-fix-readme-md-remove-non-existent-helm-i](./quick/005-fix-readme-md-remove-non-existent-helm-i/) |
| 006 | Fix blkid race condition on NVMe reconnect | 2026-02-12 | — | [006-fix-blkid-race-condition](./quick/006-fix-blkid-race-condition/) |
| 007 | Snapshot tech debt: creation-time, dead code, fallback removal | 2026-02-18 | 3dd73e6 | [7-clean-up-snapshot-tech-debt-add-creation](./quick/7-clean-up-snapshot-tech-debt-add-creation/) |

## Session Continuity

Last current activity: 2026-07-12
Current work: reconcile failed RouterOS TC-08 and disable snapshot advertisement
Deferred tonight: returning-node DaemonSet rollout on `r640-1` and `c4140`
Next: non-destructive TC-09/TC-11 and Linux-ZOL churn validation
