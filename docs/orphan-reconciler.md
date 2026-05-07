# Orphan Volume Reconciliation

## Overview

The RDS CSI Driver includes an optional **Orphan Reconciler** that periodically detects and cleans up orphaned volumes - volumes that exist on the RDS storage server but have no corresponding PersistentVolume (PV) in Kubernetes.

## Why Orphan Volumes Occur

Orphaned volumes can be created in several scenarios:

1. **Test Failures**: Integration or E2E tests that fail during cleanup
2. **Force Deletion**: PVs deleted with `kubectl delete pv --force --grace-period=0` while the CSI driver is unavailable
3. **Controller Crashes**: The controller pod crashes between creating a volume on RDS and creating the PV in Kubernetes
4. **Network Partitions**: Temporary network issues prevent proper cleanup communication

## How It Works

The orphan reconciler runs as part of the controller pod and performs the following steps:

1. **Periodic Scanning**: Every `orphan-check-interval` (default: 1 hour), the reconciler:
   - Lists all volumes on the RDS server
   - Lists all PVs in Kubernetes managed by this CSI driver
   - Identifies volumes on RDS that don't have a corresponding PV
   - Builds an observe-only inventory with object counts and planned cleanup actions

2. **Confirmation Window**: Requires the same orphan candidate to appear in
   `orphan-min-confirmations` consecutive inventories (default: 2) before any
   destructive cleanup is planned. This protects against transient Kubernetes
   API/cache churn and keeps cleanup monotonic during node drains.

3. **Grace Period**: Only considers volumes and files older than `orphan-grace-period` (default: 5 minutes) to avoid deleting objects that are currently being provisioned. File cleanup requires a known RouterOS timestamp; files with unknown age are reported but not deleted.

4. **Cleanup**: For each confirmed orphaned volume:
   - **Dry-run mode** (default): Logs the orphaned volume without deleting it
   - **Active mode**: Deletes the orphaned volume from RDS

File cleanup is limited to CSI-looking image names under `rds-volume-base-path`.
The reconciler ignores non-`pvc-*.img` files, directory entries, files that
still have disk objects, and files referenced by active PVs.

The inventory step is built before any delete operation is applied. Tests and
future simulator tooling can inspect the planned mutation list to prove the
reconciler stays calm before pointing it at live RouterOS/RDS.

The reconciler does not run an inventory immediately on controller startup by
default. This avoids adding RouterOS `/disk print` and `/file print` chatter
during controller restarts, cluster recovery, or node churn. Use
`-orphan-run-on-start=true` only for an explicit audit/remediation session.

## Configuration

The orphan reconciler is **disabled by default** and must be explicitly enabled. Configuration options:

| Flag | Default | Description |
|------|---------|-------------|
| `-enable-orphan-reconciler` | `false` | Enable orphan volume detection and cleanup |
| `-orphan-check-interval` | `1h` | Interval between orphan checks |
| `-orphan-grace-period` | `5m` | Minimum age before considering a volume orphaned |
| `-orphan-run-on-start` | `false` | Run an inventory immediately on controller startup |
| `-orphan-min-confirmations` | `2` | Consecutive inventory passes required before cleanup |
| `-orphan-dry-run` | `true` | Dry-run mode (log only, don't delete) |

## Enabling the Orphan Reconciler

### Option 1: Edit Controller Deployment

Edit the `deploy/kubernetes/controller.yaml` file and uncomment the orphan reconciler flags:

```yaml
args:
  - "-endpoint=$(CSI_ENDPOINT)"
  - "-controller"
  - "-rds-address=$(RDS_ADDRESS)"
  # Enable orphan reconciler
  - "-enable-orphan-reconciler"
  - "-orphan-check-interval=1h"
  - "-orphan-grace-period=5m"
  - "-orphan-run-on-start=false"
  - "-orphan-min-confirmations=2"
  - "-orphan-dry-run=false"  # Set to false to actually delete orphans
```

### Option 2: Using kubectl patch

```bash
kubectl -n kube-system patch deployment rds-csi-controller --type='json' -p='[
  {
    "op": "add",
    "path": "/spec/template/spec/containers/0/args/-",
    "value": "-enable-orphan-reconciler"
  },
  {
    "op": "add",
    "path": "/spec/template/spec/containers/0/args/-",
    "value": "-orphan-dry-run=false"
  }
]'
```

## Operational Workflow

### Initial Deployment (Dry-Run Mode)

1. **Enable with dry-run**: Deploy with `-enable-orphan-reconciler` and `-orphan-dry-run=true` (default). Keep `-orphan-run-on-start=false` unless you are intentionally running an audit.
2. **Monitor logs**: Watch controller logs for orphaned volume detections:
   ```bash
   kubectl -n kube-system logs -f deployment/rds-csi-controller -c rds-csi-driver | grep -i orphan
   ```
3. **Review findings**: Check if detected orphans are legitimate or false positives
4. **Manual cleanup** (if needed):
   ```bash
   # SSH to RDS and manually remove orphans
   ssh admin@10.42.241.3
   /disk print detail where slot~"pvc-"
   /disk remove [find slot=pvc-xxxx-orphan-id]
   ```

### Production Deployment (Active Mode)

Once you've validated the orphan detection:

1. **Enable active cleanup**: Set `-orphan-dry-run=false`
2. **Monitor closely**: Watch logs during the first few reconciliation cycles
3. **Adjust intervals**: Tune `orphan-check-interval`, `orphan-grace-period`, and `orphan-min-confirmations` based on your workload

## Safety Features

### Volume ID Filtering

- Only considers volumes with the CSI-managed prefix (`pvc-`)
- Ignores manually created volumes without the prefix

### Grace Period

- Prevents premature deletion of volumes being provisioned
- Default 5-minute grace period should be sufficient for most provisioning operations
- Increase if your cluster has slow storage provisioning

### Dry-Run Mode

- Enabled by default for safety
- Allows you to validate orphan detection before enabling cleanup
- Production recommendation: Run in dry-run for at least 24 hours before enabling active cleanup

### Repeated Confirmation

- Requires repeated observations before deletes are planned
- Prevents one bad or incomplete PV list response from causing RouterOS deletes
- Resets candidate state when the RDS object becomes active again or disappears

## Monitoring

### Log Messages

**Orphan detected (dry-run):**
```
W1110 12:34:56.789 orphan_reconciler.go:123] Orphaned volume detected: pvc-abc123 (path=/storage-pool/metal-csi/pvc-abc123.img, size=10737418240 bytes, age=2h30m)
I1110 12:34:56.789 orphan_reconciler.go:124] [DRY-RUN] Would delete orphaned volume: pvc-abc123
```

**Orphan deleted (active mode):**
```
W1110 12:34:56.789 orphan_reconciler.go:123] Orphaned volume detected: pvc-abc123 (path=/storage-pool/metal-csi/pvc-abc123.img, size=10737418240 bytes, age=2h30m)
I1110 12:34:56.790 orphan_reconciler.go:156] Successfully deleted orphaned volume: pvc-abc123
```

**No orphans found:**
```
V2 1110 12:34:56.789 orphan_reconciler.go:145] No orphaned volumes found (checked 15 volumes in 234ms)
```

### Metrics (Future Enhancement)

Planned Prometheus metrics:
- `rds_csi_orphan_volumes_detected_total`: Total orphaned volumes detected
- `rds_csi_orphan_volumes_deleted_total`: Total orphaned volumes deleted
- `rds_csi_orphan_reconciliation_duration_seconds`: Reconciliation cycle duration
- `rds_csi_orphan_reconciliation_errors_total`: Reconciliation errors

## Troubleshooting

### False Positives

**Symptom**: Volumes detected as orphans but are actually in use

**Possible causes**:
- PVs exist but not yet bound
- External provisioner hasn't created PV yet
- Using a different CSI driver name

**Solution**:
- Increase `-orphan-grace-period` (e.g., `10m` or `15m`)
- Check PV list: `kubectl get pv -o wide`
- Verify CSI driver name matches: `kubectl get csidriver`

### Reconciliation Failures

**Symptom**: Errors in controller logs during reconciliation

**Possible causes**:
- RDS SSH connection issues
- Kubernetes API access issues
- Insufficient RBAC permissions

**Solution**:
- Check RDS connectivity: `kubectl -n kube-system exec -it deployment/rds-csi-controller -c rds-csi-driver -- sh -c "ssh admin@$RDS_ADDRESS /disk print"`
- Verify RBAC: Controller ServiceAccount needs `get`, `list`, `watch` on PVs
- Check controller logs for specific error messages

### High Orphan Count

**Symptom**: Many orphaned volumes detected

**Investigation steps**:
1. Check test history: Recent failed E2E or integration tests?
2. Review recent incidents: Controller crashes or network issues?
3. Check volume creation timestamps: Recent or historical orphans?

**Remediation**:
- One-time bulk cleanup: Temporarily reduce `orphan-grace-period` to `1m`
- Manual inspection: SSH to RDS and review `/disk print detail`
- Consider: Recent Kubernetes upgrades or CSI driver updates?

## RBAC Requirements

The orphan reconciler requires the controller ServiceAccount to have permissions to list PVs:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: rds-csi-controller-role
rules:
  - apiGroups: [""]
    resources: ["persistentvolumes"]
    verbs: ["get", "list", "watch"]
```

These permissions are already included in the default `deploy/kubernetes/rbac.yaml`.

## Best Practices

1. **Start with dry-run**: Always enable dry-run mode first in new environments
2. **Monitor before activating**: Run dry-run for at least 24 hours in production
3. **Tune grace period**: Adjust based on your provisioning latency (check metrics)
4. **Regular audits**: Periodically review orphan logs even in active mode
5. **Alert on high counts**: Set up alerts if orphan count exceeds a threshold

## Limitations

1. **No creation time tracking**: RDS doesn't expose volume creation timestamps, so grace period is advisory
2. **Single controller**: Only runs in the active controller pod (no leader election needed)
3. **No cross-cluster support**: Only cleans up volumes from the local Kubernetes cluster
4. **Manual volumes ignored**: Only manages CSI-created volumes (with `pvc-` prefix)

## Future Enhancements

Planned improvements for future releases:

- [ ] Prometheus metrics for monitoring
- [ ] Configurable volume ID patterns (beyond `pvc-` prefix)
- [ ] Manual trigger API (on-demand reconciliation)
- [ ] Volume age estimation heuristics
- [ ] Integration with audit logging systems
- [ ] Support for volume snapshots cleanup

## Related Documentation

- [Architecture Overview](architecture.md) - Overall system design
- [RDS Commands Reference](rds-commands.md) - RouterOS CLI commands
- [Troubleshooting Guide](../README.md#troubleshooting) - Common issues and solutions

---

**Last Updated**: 2025-11-10
**Status**: Implemented (v0.2.0+)
