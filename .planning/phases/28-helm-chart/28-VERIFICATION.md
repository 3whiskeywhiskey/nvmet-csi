---
phase: 28-helm-chart
verified: 2026-02-06T19:30:00Z
status: passed
score: 6/6 must-haves verified
---

# Phase 28: Helm Chart Verification Report

**Phase Goal:** Helm chart enables one-command deployment of the RDS CSI driver with configurable values for RDS connection, storage classes, monitoring, and all component settings

**Verified:** 2026-02-06T19:30:00Z
**Status:** passed
**Re-verification:** No — initial verification

## Goal Achievement

### Observable Truths

| # | Truth | Status | Evidence |
|---|-------|--------|----------|
| 1 | Helm chart deploys controller and node plugin with configurable values | ✓ VERIFIED | Chart.yaml exists (apiVersion v2, version 1.0.0, appVersion 0.10.0). Controller Deployment template has 6 containers (driver + 5 sidecars), all args templated from values.yaml. Node DaemonSet has 3 containers. helm template renders 12 resources successfully. |
| 2 | Chart supports customization of RDS connection parameters (address, SSH port, NVMe port) | ✓ VERIFIED | values.yaml contains rds.managementIP, rds.storageIP, rds.sshPort, rds.nvmePort. Controller args reference these via templates (-rds-address={{ .Values.rds.managementIP }}). StorageClass parameters default to rds.* values. |
| 3 | Chart includes RBAC, ServiceAccount, and Secret reference management (user creates Secret, chart references it) | ✓ VERIFIED | templates/rbac.yaml creates 2 ClusterRoles and 2 ClusterRoleBindings with complete rules. templates/serviceaccount.yaml creates controller and node ServiceAccounts. Controller volume references .Values.rds.secretName. NOTES.txt documents Secret prerequisite with kubectl verification command. |
| 4 | Chart supports multiple storage classes with different configurations | ✓ VERIFIED | values.yaml defines storageClasses array with 2 entries (rds-nvme enabled, rds-nvme-rwx disabled). templates/storageclass.yaml loops over array with range. Parameters support nvmeAddress, nvmePort, volumePath overrides with fallback to rds.* defaults. Only enabled=true classes are rendered. |
| 5 | Chart documentation includes installation instructions and configuration examples | ✓ VERIFIED | README.md (625 lines) documents prerequisites, quick start (4 steps), configuration table for ALL values.yaml parameters, Secret structure with exact key names (rds-private-key, rds-host-key), storage classes, monitoring, upgrading, troubleshooting. NOTES.txt provides post-install instructions with Secret validation, deployment verification, available StorageClasses. |
| 6 | Chart distributed via git repository (users install from local clone or deploy/helm/ directory) | ✓ VERIFIED | README.md "Distribution" section documents git-only distribution: "Clone repository, install from local path: helm install rds-csi ./deploy/helm/rds-csi-driver". No OCI registry or Helm repository mentioned. |

**Score:** 6/6 truths verified

### Required Artifacts

| Artifact | Expected | Status | Details |
|----------|----------|--------|---------|
| `deploy/helm/rds-csi-driver/Chart.yaml` | Chart metadata with apiVersion v2, version 1.0.0, appVersion 0.10.0 | ✓ VERIFIED | 19 lines, apiVersion v2, version 1.0.0, appVersion 0.10.0, kubeVersion >=1.26.0-0 |
| `deploy/helm/rds-csi-driver/values.yaml` | Comprehensive default configuration | ✓ VERIFIED | 328 lines, flat structure (rds.*, controller.*, node.*, sidecars.*, monitoring.*, storageClasses array). Secret key names documented in comments (rds-private-key, rds-host-key, snmp-community). All driver flags from main.go represented. |
| `deploy/helm/rds-csi-driver/values.schema.json` | JSON Schema validation with required fields | ✓ VERIFIED | 211 lines, draft-07 schema, required fields: rds.managementIP (IP pattern), rds.secretName (k8s name pattern). Port ranges, Kubernetes naming patterns enforced. |
| `deploy/helm/rds-csi-driver/templates/_helpers.tpl` | Template helpers (13 named templates) | ✓ VERIFIED | 116 lines, defines rds-csi.name, fullname, chart, labels, selectorLabels, controllerSelectorLabels, nodeSelectorLabels, controllerServiceAccountName, nodeServiceAccountName, driverName, imageTag, controllerImage, nodeImage |
| `deploy/helm/rds-csi-driver/templates/controller.yaml` | Controller Deployment with 6 containers | ✓ VERIFIED | 194 lines, Deployment with driver + csi-provisioner + csi-attacher + csi-resizer + csi-snapshotter + liveness-probe. All args templated from values. Secret volume references .Values.rds.secretName. Key paths: /etc/rds-csi/rds-private-key, /etc/rds-csi/rds-host-key. |
| `deploy/helm/rds-csi-driver/templates/node.yaml` | Node DaemonSet with 3 containers | ✓ VERIFIED | 183 lines, DaemonSet with driver + node-driver-registrar + liveness-probe. Privileged security context, hostNetwork, bidirectional mount propagation. Kubelet path templated. |
| `deploy/helm/rds-csi-driver/templates/rbac.yaml` | RBAC resources with complete rules | ✓ VERIFIED | 134 lines, 2 ClusterRoles (controller, node) + 2 ClusterRoleBindings. Controller rules include PVs, PVCs, PVC status, StorageClasses, VolumeAttachments, CSINodes, Nodes, Events, Leases, snapshot.storage.k8s.io resources. |
| `deploy/helm/rds-csi-driver/templates/serviceaccount.yaml` | ServiceAccount resources | ✓ VERIFIED | 26 lines, 2 ServiceAccounts with component labels (controller, node) |
| `deploy/helm/rds-csi-driver/templates/csidriver.yaml` | CSIDriver resource with hardcoded driver name | ✓ VERIFIED | 39 lines, driver name: rds.csi.srvlab.io (from helper, not templated). All spec fields preserved (attachRequired, podInfoOnMount, volumeLifecycleModes, fsGroupPolicy, storageCapacity). |
| `deploy/helm/rds-csi-driver/templates/storageclass.yaml` | Conditional StorageClass loop | ✓ VERIFIED | 29 lines, range loop over .Values.storageClasses, creates only if enabled=true. Parameters default to rds.* values. Conditional isDefault annotation. |
| `deploy/helm/rds-csi-driver/templates/snapshotclass.yaml` | Conditional VolumeSnapshotClass | ✓ VERIFIED | 29 lines, conditional on snapshotClass.enabled. Driver name hardcoded to rds.csi.srvlab.io. Includes prerequisite comments. |
| `deploy/helm/rds-csi-driver/templates/service.yaml` | Metrics Service for Prometheus | ✓ VERIFIED | 20 lines, conditional on monitoring.enabled. Includes component=controller label. Selector targets controller pods. Exposes monitoring.port (9809). |
| `deploy/helm/rds-csi-driver/templates/servicemonitor.yaml` | Conditional ServiceMonitor with CRD detection | ✓ VERIFIED | 24 lines, triple-conditional (monitoring.enabled AND serviceMonitor.enabled AND CRD detection/forceEnable). Selector includes selectorLabels + component=controller. |
| `deploy/helm/rds-csi-driver/templates/NOTES.txt` | Post-install instructions with Secret prerequisite validation | ✓ VERIFIED | 184 lines, Secret verification command (kubectl get secret {{ .Values.rds.secretName }}), required key names documented, deployment verification commands, available StorageClasses list, conditional sections (monitoring, snapshots, upgrade notice). |
| `deploy/helm/rds-csi-driver/README.md` | Chart documentation | ✓ VERIFIED | 625 lines, prerequisites, distribution (git-only), quick start (4 steps), configuration table for ALL values, Secret structure example, storage classes section, monitoring, upgrading, troubleshooting. |
| `deploy/helm/rds-csi-driver/.helmignore` | File exclusion patterns | ✓ VERIFIED | 9 lines, standard exclusions (.git, .gitignore, *.swp, .DS_Store, etc) |

### Key Link Verification

| From | To | Via | Status | Details |
|------|----|----|--------|---------|
| templates/controller.yaml | templates/_helpers.tpl | include rds-csi.labels | ✓ WIRED | helm template output shows labels: helm.sh/chart, app.kubernetes.io/name, app.kubernetes.io/instance, app.kubernetes.io/version, app.kubernetes.io/managed-by |
| templates/rbac.yaml | templates/serviceaccount.yaml | ClusterRoleBinding subjects reference ServiceAccount | ✓ WIRED | Rendered ClusterRoleBinding subjects contain kind: ServiceAccount, name: test-release-rds-csi-driver-controller, namespace: rds-csi |
| templates/controller.yaml | values.yaml | All container args templated from .Values.rds and .Values.controller | ✓ WIRED | grep confirms args reference .Values.rds.managementIP, .Values.rds.sshPort, .Values.controller.logLevel, .Values.monitoring.port (conditional), .Values.controller.orphanReconciler.* (conditional) |
| templates/controller.yaml | rds.secretName | Secret volume references .Values.rds.secretName | ✓ WIRED | grep finds "secretName: {{ .Values.rds.secretName }}" in controller.yaml. Volume mounted at /etc/rds-csi with key paths rds-private-key and rds-host-key. |
| templates/servicemonitor.yaml | templates/service.yaml | ServiceMonitor selector includes component=controller matching Service | ✓ WIRED | ServiceMonitor selector: app.kubernetes.io/component: controller (verified via helm template with forceEnable). Service has same label in metadata and selector. |
| templates/storageclass.yaml | values.yaml | Iterates storageClasses array with fallback to rds.* | ✓ WIRED | Template uses "range $idx, $sc := .Values.storageClasses". Parameters use "default $.Values.rds.storageIP" pattern for fallback. |
| templates/NOTES.txt | rds.secretName | Prints Secret verification command using .Values.rds.secretName | ✓ WIRED | grep confirms "kubectl get secret {{ .Values.rds.secretName }}" in NOTES.txt |

### Requirements Coverage

Requirements mapped to Phase 28 from ROADMAP.md: HELM-01, HELM-02, HELM-03, HELM-04, HELM-05

| Requirement | Status | Supporting Evidence |
|-------------|--------|---------------------|
| HELM-01: Chart structure and metadata | ✓ SATISFIED | Chart.yaml with apiVersion v2, version 1.0.0, appVersion 0.10.0. Complete chart directory structure with templates/, values.yaml, values.schema.json, README.md, NOTES.txt, .helmignore. helm lint passes (1 chart linted, 0 failed). |
| HELM-02: Configurable RDS connection | ✓ SATISFIED | values.yaml defines rds.managementIP, rds.storageIP, rds.sshPort, rds.nvmePort, rds.sshUser, rds.basePath, rds.secretName. Controller args templated (-rds-address={{ .Values.rds.managementIP }}). values.schema.json enforces required fields (managementIP, secretName). |
| HELM-03: Multiple StorageClasses | ✓ SATISFIED | values.yaml storageClasses array with 2 entries. templates/storageclass.yaml loops with range, creates only enabled classes. Parameters support overrides with fallback to rds.* defaults. RWX class disabled by default (enabled: false). |
| HELM-04: Monitoring integration | ✓ SATISFIED | templates/service.yaml creates metrics Service (conditional). templates/servicemonitor.yaml with triple-conditional (monitoring + serviceMonitor + CRD detection). values.yaml monitoring.* section with port, serviceMonitor toggle, rdsMonitoring toggle. |
| HELM-05: Documentation and distribution | ✓ SATISFIED | README.md documents all configuration options, prerequisites, quick start, Secret structure, troubleshooting. NOTES.txt provides post-install instructions with Secret validation. Distribution documented as git-only (no Helm repo publishing). |

### Anti-Patterns Found

| File | Line | Pattern | Severity | Impact |
|------|------|---------|----------|--------|
| None | - | - | - | All templates use best practices: flat values structure, conditional rendering, CRD detection, Secret reference (not creation), component selector labels |

### Human Verification Required

None. All truths are programmatically verifiable via helm lint, helm template, and grep/file inspection.

### Test Results

**helm lint:**
```
==> Linting /path/to/rds-csi/deploy/helm/rds-csi-driver/
[INFO] Chart.yaml: icon is recommended

1 chart(s) linted, 0 chart(s) failed
```

**helm template (default values):**
- Rendered resources: 12 total
  - 2 ServiceAccounts
  - 2 ClusterRoles
  - 2 ClusterRoleBindings
  - 1 CSIDriver
  - 1 Deployment (controller with 6 containers)
  - 1 DaemonSet (node with 3 containers)
  - 1 Service (metrics)
  - 1 StorageClass (rds-nvme only, RWX disabled)
  - 1 VolumeSnapshotClass

**helm template (with ServiceMonitor enabled):**
- ServiceMonitor renders with selector: `app.kubernetes.io/component: controller`
- CRD detection works via `.Capabilities.APIVersions.Has` check

**helm template (with isDefault StorageClass):**
- StorageClass includes annotation: `storageclass.kubernetes.io/is-default-class: "true"`

**values.schema.json validation:**
- Required fields enforced: `rds.managementIP`, `rds.secretName`
- IP address pattern validation on managementIP and storageIP
- Port ranges (1-65535) enforced on sshPort and nvmePort
- Kubernetes naming patterns on secretName and StorageClass names

**Template substantive checks:**
- controller.yaml: 194 lines, 6 containers, all args templated, Secret mount at /etc/rds-csi
- node.yaml: 183 lines, 3 containers, privileged security context, hostNetwork, kubelet path templated
- storageclass.yaml: 29 lines, range loop over array, conditional rendering
- servicemonitor.yaml: 24 lines, triple-conditional, component selector
- rbac.yaml: 134 lines, complete RBAC rules for controller and node
- NOTES.txt: 184 lines, Secret validation, deployment verification, conditional sections
- README.md: 625 lines, all configuration documented, Secret structure, troubleshooting

---

_Verified: 2026-02-06T19:30:00Z_
_Verifier: Claude (gsd-verifier)_
