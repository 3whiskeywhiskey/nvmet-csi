# Stale VolumeAttachment Cleanup

The controller includes optional cleanup for Kubernetes `VolumeAttachment`
objects that still point at deleted nodes. This is separate from RouterOS/RDS
disk or file orphan cleanup: it only mutates Kubernetes `VolumeAttachment`
objects and does not touch node mounts or RouterOS storage objects directly.

This should be treated as an explicit remediation tool for the operational
pattern: clear the stuck Kubernetes attachment and let the pod attach cleanly
again.

## Defaults

Stale `VolumeAttachment` cleanup is disabled by default.

When enabled, it should be run in dry-run first:

```yaml
args:
  - "-enable-stale-volumeattachment-cleanup"
  - "-stale-volumeattachment-dry-run=true"
  - "-stale-volumeattachment-min-confirmations=2"
```

Active deletion requires setting:

```yaml
args:
  - "-stale-volumeattachment-dry-run=false"
```

## Safety Rules

A `VolumeAttachment` is eligible only when all of these are true:

1. It belongs to this CSI driver.
2. It references a non-empty PersistentVolume name.
3. The referenced PersistentVolume still exists.
4. The referenced node no longer exists.
5. The object is older than the attachment grace period.
6. The same candidate appears in the required number of consecutive
   reconciliation passes.

The controller skips attachments that are already deleting, attachments for
other CSI drivers, attachments whose node still exists, and attachments whose
PV no longer exists.

## Why This Exists

The previous attachment reconciler could clear only the driver's in-memory
attachment tracking and informational PV annotations. That was useful for local
state recovery, but it did not remove a stuck Kubernetes `VolumeAttachment`.

This cleanup path targets the actual Kubernetes object that can block reattach
after node loss, while keeping the same conservative posture used for orphaned
RDS objects: observe first, require confirmation, then mutate only the minimal
object.

## Operator Guidance

Use this before considering node mount surgery or RouterOS/RDS intervention:

1. Enable cleanup in dry-run.
2. Watch controller logs for planned stale `VolumeAttachment` deletes.
3. Confirm the affected node is gone and the PV/PVC should reattach elsewhere.
4. Enable active deletion for the remediation window.
5. Return to dry-run or disabled after cleanup.

This does not require an RDS reboot and should not require direct host mount
changes.

## Metrics

Prometheus exposes `rds_csi_attachment_stale_volumeattachment_cleanup_total`
with fixed `result` and `dry_run` labels. Expected results are:

- `candidate`: a stale `VolumeAttachment` matched the safety rules.
- `planned`: the candidate reached the configured confirmation threshold.
- `delete_attempt`: active cleanup attempted a Kubernetes delete.
- `delete_success`: active cleanup deleted the object.
- `delete_failure`: active cleanup failed to delete the object.
- `delete_not_found`: the object disappeared before active cleanup deleted it.
