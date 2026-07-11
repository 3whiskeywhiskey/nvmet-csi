# Export Reconciler (Linux-ZOL nvmet reboot safety)

## Problem

Kernel `nvmet` exports live in configfs (`/sys/kernel/config/nvmet/`), which
is runtime state: a target host reboot wipes every port, subsystem, and
namespace. The backing zvols survive, and so does the desired-state metadata
stored on them as ZFS user properties (`org.nvmet-csi:volume-id`,
`org.nvmet-csi:nqn`, `org.nvmet-csi:port`), but nothing rebuilt the exports.

The controller only (re)builds an export inside CSI `CreateVolume`, and that
RPC never arrives again for already-provisioned PVs. Result: after a target
reboot, every retained Linux-ZOL PVC failed to mount until an operator
recreated configfs entries by hand.

`boot.kernelModules = [ "nvmet" "nvmet_tcp" ]` (see
`nixos/modules/nvmet-target.nix` in the netboot config) loads the modules but
cannot restore configfs contents. The controller owns this invariant now:

> For every managed Linux-ZOL PV that should be exported, the matching
> configfs subsystem/namespace/port export exists and is correct.

## Design

The export reconciler (`pkg/reconciler/export_reconciler.go`) runs in the
controller. Every pass:

1. **Desired set** — list PVs bound to this CSI driver, keep
   `backend: linux-zol-nvmet` selections, group volume IDs (plus the `nqn` and
   `nvmePort` volume attributes) per backend selection
   (`pkg/driver/export_targets.go`).
2. **Actual set** — `Backend.ListVolumes()`: one batched `zfs get` for all
   zvol properties plus one configfs probe script per volume.
3. For each PV-backed volume, build a plan with the same
   `BuildEnsureVolumePlan` used by provisioning and the `linux-zol-plan` job.
   The desired spec mirrors the *observed* volume size, so the plan can only
   be `noop`, `repair`, or `conflict`.
4. Apply `repair` plans (create subsystem/namespace, set `device_path`,
   enable, ensure port attrs, link subsystem into port — all idempotent,
   read-before-write). Everything else is reported, never mutated.

### Safety invariants

- **Repair-only.** The reconciler structurally cannot create, resize, or
  delete backend volumes. A PV whose backing zvol is missing is a
  data-loss-class condition: it is logged as an error and reported, never
  papered over with an empty volume.
- **PV-gated.** Backend volumes without a PV are never exported (or touched);
  they are reported as orphan candidates.
- **Conflicts are surfaced, not rewritten.** If configfs holds a conflicting
  active value (wrong port address, foreign symlink, NQN mismatch between PV
  and ZFS metadata), the pass records a precise repair-required error.
- **RDS excluded.** Only `linux-zol-nvmet` selections participate. The
  RouterOS/RDS backend keeps persistent export state and must not receive
  background mutation traffic (`docs/routeros-rds-hardening.md`).

### Idempotent configfs writes

All export mutations use read-before-write command builders
(`pkg/backend/linuxzol/host.go`):

| Object | Builder | Behavior |
| --- | --- | --- |
| `ports/<port>/addr_*` | `writeIfEmptyOrEqualCommand` | write when empty, no-op when equal, error when conflicting |
| `.../attr_allow_any_host` | `writeIfDifferentCommand` | no-op when equal |
| `.../namespaces/1/device_path` | `setNamespaceDevicePathCommand` | no-op when equal; refuses rewrite while namespace is enabled |
| `.../namespaces/1/enable` | `enableNamespaceCommand` | no-op when already enabled |
| `ports/<port>/subsystems/<nqn>` | `linkIfAbsentOrEqualCommand` | no-op when equal, error on foreign entry |

A fully converged volume produces **zero** configfs writes.

## Configuration

Controller flags (defaults shown; the reconciler is on by default because it
is the reboot-recovery path):

```text
-enable-export-reconciler=true
-export-check-interval=10m
-export-reconcile-run-on-start=true
-export-reconcile-dry-run=false
```

Helm values:

```yaml
controller:
  exportReconciler:
    enabled: true
    checkInterval: 10m
    runOnStart: true
    dryRun: false
```

## Operational flow after a target reboot

1. Target host boots; `nvmet`/`nvmet_tcp` modules load; configfs is empty.
2. Controller pod starts (or its periodic pass fires), lists PVs, observes
   unexported zvols, and applies repair plans per volume.
3. Acceptance check:

```bash
sudo nvme discover -t tcp -a <target-address> -s 4420
```

must list all managed retained zvol-backed subsystems with no manual configfs
work. A second reconciliation pass (or controller restart) is a no-op.

Logs to watch (controller):

```text
Export reconciliation for linux-zol-nvmet/<addr>/<pool>: repaired export for pvc-... 
Export reconciliation cycle complete (duration=..., targets=1, repaired=N, conflicts=0)
```

`conflicts>0` means operator attention: the log lines name the volume and the
exact conflicting attribute.

## Relationship to `linux-zol-plan`

The Helm `linux-zol-plan` job remains the dry-run/preflight/debug surface. It
renders the same `BuildEnsureVolumePlan` output the reconciler applies, so a
plan printed by the job is exactly what a reconciliation pass would execute.
Use `exportReconciler.dryRun: true` to run the controller in the same
observe-only mode.

## Limitations

- Host ACLs are not implemented; subsystems are created with
  `attr_allow_any_host=1`, matching the provisioning path.
- One namespace (`namespaces/1`) per subsystem, matching the provisioning
  path.
- The reconciler does not delete stale exports for removed PVs; deletion
  remains owned by CSI `DeleteVolume` (and orphan tooling for RDS).
