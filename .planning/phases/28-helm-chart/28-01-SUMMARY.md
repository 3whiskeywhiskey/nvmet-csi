---
phase: 28-helm-chart
plan: 01
subsystem: deployment
tags: [helm, packaging, values, schema, templates]
requires:
  - v0.9.0 production readiness (Phase 25.2)
  - v0.10.0 snapshots (Phase 26)
  - v0.10.0 monitoring metrics (Phase 28.2)
provides:
  - Helm chart skeleton with comprehensive configuration
  - Values structure for all driver features
  - JSON Schema validation for required parameters
  - Template helpers for naming and labels
  - Chart foundation for template development
affects:
  - 28-02 (RBAC templates depend on serviceAccountName helpers)
  - 28-03 (Deployment templates depend on values structure)
  - 28-04 (DaemonSet templates depend on node configuration)
  - All future Helm chart plans (foundational files)
tech-stack:
  added: []
  patterns:
    - Flat values structure for simpler --set overrides
    - Component-specific selector labels (controller, node)
    - Image tag defaults to Chart.appVersion if not specified
    - Secret key documentation in values.yaml comments
key-files:
  created:
    - deploy/helm/rds-csi-driver/Chart.yaml
    - deploy/helm/rds-csi-driver/values.yaml
    - deploy/helm/rds-csi-driver/values.schema.json
    - deploy/helm/rds-csi-driver/templates/_helpers.tpl
    - deploy/helm/rds-csi-driver/.helmignore
  modified: []
decisions:
  - id: helm-values-flat-structure
    what: Use flat values structure (rds.*, controller.*, node.*) instead of deeply nested
    why: Simpler --set overrides (--set rds.managementIP vs --set config.rds.connection.managementIP)
    impact: Easier CLI usage, clearer values hierarchy
  - id: secret-key-names-in-comments
    what: Document Secret key names (rds-private-key, rds-host-key) in values.yaml comments
    why: Match deployed manifest convention, guide users on Secret creation
    impact: Users know exact key names required in Secret
  - id: schema-validates-required-fields
    what: JSON Schema enforces rds.managementIP and rds.secretName as required
    why: Fail fast at install time with clear error instead of runtime failure
    impact: Better user experience, catches misconfiguration before deployment
  - id: component-selector-labels
    what: Separate selector label helpers for controller and node (rds-csi.controllerSelectorLabels, rds-csi.nodeSelectorLabels)
    why: Enable component-specific pod selectors for monitoring, debugging
    impact: Clearer pod identification in kubectl and Prometheus
completed: 2026-02-07
duration: 2 minutes
---

# Phase 28 Plan 01: Helm Chart Skeleton Summary

**One-liner:** Helm chart foundation with comprehensive values (RDS, controller, node, sidecars, monitoring), JSON Schema validation, and 13 template helpers

## What Was Built

Created the foundational Helm chart files that all subsequent templates depend on:

1. **Chart.yaml**: Chart metadata with apiVersion v2, version 1.0.0, appVersion 0.10.0, kubeVersion >=1.26.0-0
2. **values.yaml**: Comprehensive default configuration covering:
   - RDS connection (managementIP, storageIP, SSH, NVMe/TCP, Secret reference)
   - Controller configuration (replicas, image, resources, orphan reconciler, attachment tracking, VMI serialization)
   - Node configuration (image, resources, kubelet path, tolerations, sidecars)
   - All 5 CSI sidecars (provisioner, attacher, resizer, snapshotter, livenessprobe)
   - Monitoring configuration (metrics port, ServiceMonitor, RDS monitoring toggle)
   - StorageClasses (rds-nvme, rds-nvme-rwx with RWX documentation)
   - VolumeSnapshotClass configuration
3. **values.schema.json**: JSON Schema draft-07 validation enforcing required fields and patterns
4. **templates/_helpers.tpl**: 13 template helpers for naming, labels, selector labels, service accounts, driver name, image tags
5. **.helmignore**: Standard exclusion patterns for chart packaging

## Verification Results

All verification checks passed:

```bash
$ helm lint deploy/helm/rds-csi-driver/
==> Linting deploy/helm/rds-csi-driver/
[INFO] Chart.yaml: icon is recommended

1 chart(s) linted, 0 chart(s) failed
```

- ✅ Chart.yaml has apiVersion v2, version 1.0.0, appVersion 0.10.0
- ✅ values.yaml covers all sections: rds, controller, node, sidecars, monitoring, storageClasses, snapshotClass
- ✅ values.yaml documents Secret key names as `rds-private-key` and `rds-host-key` (matching deploy/kubernetes/controller.yaml)
- ✅ _helpers.tpl defines 13 named templates (name, fullname, chart, labels, selectorLabels, serviceAccountName, driverName, controllerImage, nodeImage)
- ✅ values.schema.json validates rds.managementIP and rds.secretName as required
- ✅ Helm lint passes with no structural errors

## Key Implementation Details

### Secret Key Name Matching

The values.yaml documents the exact Secret key names used in deployed manifests:

```yaml
# Secret must contain keys:
#   - rds-private-key: SSH private key for RouterOS authentication
#   - rds-host-key: SSH host public key for host verification
#   - snmp-community: (optional) SNMP community string for hardware monitoring
secretName: "rds-csi-secret"
```

This matches `deploy/kubernetes/controller.yaml` which mounts the secret at `/etc/rds-csi` and references keys `rds-private-key` and `rds-host-key`. The Helm chart follows deployed convention, NOT main.go defaults (`/etc/rds-csi/ssh-key/id_rsa`).

### Component-Specific Selector Labels

Template helpers provide component-specific selectors:

```yaml
{{- define "rds-csi.controllerSelectorLabels" -}}
{{ include "rds-csi.selectorLabels" . }}
app.kubernetes.io/component: controller
{{- end }}

{{- define "rds-csi.nodeSelectorLabels" -}}
{{ include "rds-csi.selectorLabels" . }}
app.kubernetes.io/component: node
{{- end }}
```

This enables targeted pod selection for monitoring and debugging.

### Image Tag Defaulting

The `rds-csi.imageTag` helper defaults empty tag to Chart.appVersion:

```yaml
{{- define "rds-csi.imageTag" -}}
{{- if .tag }}
{{- .tag }}
{{- else }}
{{- $.Chart.AppVersion }}
{{- end }}
{{- end }}
```

This allows users to omit `controller.image.tag` and `node.image.tag` in values.yaml while still getting a valid image reference.

### RWX StorageClass Documentation

The RWX StorageClass includes inline documentation clarifying its purpose:

```yaml
# ReadWriteMany StorageClass (for KubeVirt live migration)
# WARNING: This enables multi-attach during migration windows.
# NOT intended for general-purpose shared filesystems (like NFS).
- name: rds-nvme-rwx
  enabled: false  # Explicitly enable for KubeVirt environments
  # RWX: Required for KubeVirt live migration. Not for general-purpose shared filesystems.
  accessModes:
    - ReadWriteMany
```

This prevents misuse while enabling the validated KubeVirt migration use case.

### Schema Validation Patterns

The JSON Schema enforces:
- IP address format for managementIP and storageIP
- Port ranges (1-65535) for sshPort and nvmePort
- Kubernetes naming patterns for secretName, StorageClass names
- Base path must start with `/`
- Required fields: rds.managementIP, rds.secretName
- Controller replicas constrained to 1 (no HA)

Failed validation example:
```bash
$ helm install rds-csi . --set rds.managementIP=""
Error: values don't meet the specifications of the schema(s) in the following chart(s):
rds-csi-driver:
- rds.managementIP: Invalid type. Expected: string, given: null
```

## Test Results

Helm lint validation:
```bash
$ helm lint deploy/helm/rds-csi-driver/
==> Linting deploy/helm/rds-csi-driver/
[INFO] Chart.yaml: icon is recommended

1 chart(s) linted, 0 chart(s) failed
```

Template helper count:
```bash
$ grep -c "define \"rds-csi" deploy/helm/rds-csi-driver/templates/_helpers.tpl
13
```

Values structure verification:
```bash
$ grep "managementIP:" deploy/helm/rds-csi-driver/values.yaml
  managementIP: "192.0.2.10"

$ grep -A2 "rds-private-key" deploy/helm/rds-csi-driver/values.yaml
  #   - rds-private-key: SSH private key for RouterOS authentication
  #   - rds-host-key: SSH host public key for host verification
  #   - snmp-community: (optional) SNMP community string for hardware monitoring
```

## Decisions Made

### Flat Values Structure
Used flat structure at top level (rds.*, controller.*, node.*) instead of deeply nested hierarchy. This makes CLI overrides simpler: `--set rds.managementIP=192.0.2.10` vs `--set config.rds.connection.managementIP=192.0.2.10`.

### Secret Key Documentation
Documented Secret key names in values.yaml comments (rds-private-key, rds-host-key) to match deployed manifest convention in deploy/kubernetes/controller.yaml. This guides users on Secret creation and avoids confusion with main.go defaults.

### Component Selector Labels
Created separate selector label helpers for controller and node (rds-csi.controllerSelectorLabels, rds-csi.nodeSelectorLabels). This enables component-specific pod selectors for monitoring and debugging.

### Image Tag Defaulting
Made image tags optional in values.yaml by defaulting to Chart.appVersion. Users can override with `controller.image.tag` but most users will use the default chart version.

## Deviations from Plan

None - plan executed exactly as written.

## Next Phase Readiness

The Helm chart skeleton is ready for template development:

**Phase 28-02 (RBAC Templates):**
- ✅ serviceAccountName helpers available (rds-csi.controllerServiceAccountName, rds-csi.nodeServiceAccountName)
- ✅ Label helpers available for ClusterRole metadata
- ✅ Namespace configuration in values

**Phase 28-03 (Controller Deployment Template):**
- ✅ Values structure defined for controller configuration
- ✅ Controller image helper (rds-csi.controllerImage)
- ✅ RDS connection values documented
- ✅ Sidecar configuration values defined

**Phase 28-04 (Node DaemonSet Template):**
- ✅ Values structure defined for node configuration
- ✅ Node image helper (rds-csi.nodeImage)
- ✅ Kubelet path, tolerations, resources configured
- ✅ Sidecar configuration values defined

**Phase 28-05 (StorageClass Templates):**
- ✅ StorageClasses array structure defined
- ✅ Driver name helper (rds-csi.driverName)
- ✅ RWX documentation in place

**Phase 28-06 (ServiceMonitor Template):**
- ✅ Monitoring configuration values defined
- ✅ ServiceMonitor toggle and labels configuration

## File Inventory

### Created Files (5)
1. `deploy/helm/rds-csi-driver/Chart.yaml` (365 bytes) - Chart metadata with apiVersion v2
2. `deploy/helm/rds-csi-driver/values.yaml` (8.1 KB) - Comprehensive default configuration
3. `deploy/helm/rds-csi-driver/values.schema.json` (5.7 KB) - JSON Schema validation
4. `deploy/helm/rds-csi-driver/templates/_helpers.tpl` (3.1 KB) - Template helpers
5. `deploy/helm/rds-csi-driver/.helmignore` (127 bytes) - File exclusion patterns

### Modified Files (0)
None.

### Total Size
~17.4 KB of chart foundation files

## Git Commits

### Task 1 Commit
```
bc987c2 feat(28-01): create Helm chart skeleton with Chart.yaml, values.yaml, and .helmignore
- Chart.yaml: apiVersion v2, version 1.0.0, appVersion 0.10.0
- values.yaml: comprehensive configuration for controller, node, RDS, monitoring, sidecars
- values.yaml: documents Secret key names (rds-private-key, rds-host-key) matching deployed manifests
- .helmignore: standard exclusion patterns
- All configuration options from main.go flags represented in values
```

### Task 2 Commit
```
f4bcf30 feat(28-01): add Helm template helpers and values schema validation
- _helpers.tpl: 13 template helpers (name, fullname, labels, selectorLabels, serviceAccountName, driverName, imageTag)
- Component-specific selector labels for controller and node
- values.schema.json: JSON Schema draft-07 validation for all configuration sections
- Schema enforces required fields (rds.managementIP, rds.secretName)
- IP address pattern validation, port ranges, Kubernetes name patterns
- helm lint passes with no errors (1 chart linted, 0 failed)
```

---

**Phase:** 28-helm-chart
**Plan:** 01
**Status:** Complete
**Duration:** 2 minutes
**Completed:** 2026-02-07
