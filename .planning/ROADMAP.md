# Roadmap Index: nvmet-csi

## Current nvmet-csi Status

`nvmet-csi` is the backend-neutral successor to the RDS-focused driver whose
v0.1.0-v0.11.0 plans are archived below. Linux-ZOL is the primary backend;
RouterOS/RDS is an isolated compatibility backend. Current live evidence is
maintained in `docs/nvmet-csi-live-validation.md`.

| Backend / capability | Current evidence |
| --- | --- |
| Linux-ZOL ordinary lifecycle and reboot recovery | Implemented, automated-tested, live-validated |
| Linux-ZOL snapshots | Unsupported |
| RouterOS ordinary lifecycle through nvmet-csi | Implemented, automated-tested, live-validated |
| Volume snapshots on both backends | Unsupported; RouterOS TC-08 failed live data-integrity validation |
| RouterOS resilience | Automated/mock-tested or documented; TC-09-TC-11 pending live execution |


RouterOS `/disk add copy-from` copied settings rather than backing data on the
tested 7.23rc2 appliance. Snapshot capability advertisement and Helm class
creation are therefore disabled pending a different, data-preserving design.

## Archived Predecessor Milestones

The statuses below are historical planning records. Phase completion denotes
completion of the planned implementation, automated test, or documentation
deliverable; it is not independent evidence that a hardware procedure ran.

## Milestones

- ✅ **v0.1.0 Foundation** - Phases 1-3 (shipped 2024-03-15)
- ✅ **v0.2.0 Controller Service** - Phases 4-5 (shipped 2024-04-01)
- ✅ **v0.3.0 Volume Fencing** - Phases 6-8 (shipped 2026-02-03)
- ✅ **v0.4.0 Production Hardening** - Phases 9-11 (shipped 2024-06-30)
- ✅ **v0.5.0 NVMe-oF Reconnection** - Phases 12-14 (shipped 2025-01-15)
- ✅ **v0.6.0 Block Volumes & KubeVirt** - Phases 15-16 (shipped 2025-11-15)
- ✅ **v0.7.0 Migration Tracking** - Phases 17-19 (shipped 2026-01-20)
- ✅ **v0.8.0 Code Quality & Logging** - Phases 20-21 (shipped 2026-02-04)
- ✅ **v0.9.0 Production Readiness & Test Maturity** - Phases 22-25.2 (shipped 2026-02-06)
- ✅ **v0.10.0 Feature Enhancements** - Phases 26-28 (shipped 2026-02-06)
- ✅ **v0.11.0 Data Protection** - Phases 29-32 (shipped 2026-02-18)

## Phases

<details>
<summary>✅ v0.1.0-v0.10.0 (Phases 1-28) - SHIPPED 2026-02-06</summary>

See milestone archives in `.planning/milestones/` for complete phase details:
- `.planning/milestones/v0.1.0-ROADMAP.md`
- `.planning/milestones/v0.2.0-ROADMAP.md`
- `.planning/milestones/v0.3.0-ROADMAP.md`
- `.planning/milestones/v0.4.0-ROADMAP.md`
- `.planning/milestones/v0.5.0-ROADMAP.md`
- `.planning/milestones/v0.6.0-ROADMAP.md`
- `.planning/milestones/v0.7.0-ROADMAP.md`
- `.planning/milestones/v0.8.0-ROADMAP.md`
- `.planning/milestones/v0.9.0-ROADMAP.md`
- `.planning/milestones/v0.10.0-ROADMAP.md`

</details>

<details>
<summary>✅ v0.11.0 Data Protection (Phases 29-32) - ARCHIVED 2026-02-18</summary>

See: `.planning/milestones/v0.11.0-ROADMAP.md`

- [x] Phase 29: Snapshot implementation fix (2/2 plans) — 2026-02-18
- [x] Phase 30: Snapshot implementation/automated validation and TC-08 procedure (2/2 plans) — TC-08 was not live-executed at archive time
- [x] Phase 31: Scheduled snapshot manifests and automated rendering (1/1 plan) — live CronJob execution pending
- [x] Phase 32: Automated resilience regressions and TC-09-TC-11 procedures (2/2 plans) — hardware execution pending

</details>

---
*Last updated: 2026-07-12; historical milestone archives remain unchanged.*
