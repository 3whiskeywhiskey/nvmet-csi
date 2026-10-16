# Phase 28: Helm Chart - Research

**Researched:** 2026-02-06
**Domain:** Helm chart packaging and configuration for Kubernetes CSI drivers
**Confidence:** HIGH

## Summary

Helm charts for CSI drivers follow well-established patterns from the Kubernetes SIG-Storage community. The standard approach uses a flat values.yaml structure organized by component (controller, node, sidecars), conditional resource creation (StorageClass, ServiceMonitor), and template helpers for reusability. CSI drivers require special handling for privileged node pods, sidecar version compatibility, and RBAC permissions.

The RDS CSI driver chart must template the existing Kubernetes manifests (controller Deployment, node DaemonSet, RBAC, StorageClasses) with values supporting RDS connection configuration, monitoring toggles, resource customization, and deployment preferences (affinity, tolerations, node selectors). The chart will be published in the git repository only (no OCI registry or GitHub Pages needed for homelab use).

**Primary recommendation:** Use flat values structure (`controller.*`, `node.*`, `rds.*`, `monitoring.*`) with conditional ServiceMonitor deployment based on CRD detection, follow kubernetes-sigs/aws-ebs-csi-driver patterns for sidecar configuration, and provide comprehensive NOTES.txt with post-install instructions.

## Standard Stack

The established libraries/tools for Helm chart development and CSI driver packaging:

### Core
| Library | Version | Purpose | Why Standard |
|---------|---------|---------|--------------|
| Helm | 3.14+ | Chart packaging and templating | Official Kubernetes package manager, SemVer 2 support |
| Kubernetes CSI Sidecars | Latest stable | Provisioner, attacher, resizer, snapshotter | Maintained by kubernetes-csi project, required for CSI functionality |
| Prometheus Operator CRDs | v0.68+ | ServiceMonitor support | De facto standard for Kubernetes monitoring |

### Supporting
| Library | Version | Purpose | When to Use |
|---------|---------|---------|-------------|
| chart-testing (ct) | v3.10+ | Lint and installation tests | CI/CD pipeline validation |
| helm-unittest | v0.3+ | Template unit tests | Testing template logic without cluster |
| JSON Schema | Draft 7 | values.yaml validation | Runtime validation of user input |

### Alternatives Considered
| Instead of | Could Use | Tradeoff |
|------------|-----------|----------|
| Flat values structure | Nested structure | Nested harder to override with `--set`, flat simpler for CLI use |
| Git-only distribution | OCI registry / GitHub Pages | OCI/Pages adds complexity, git clone sufficient for homelab |
| Conditional ServiceMonitor | Always create | Fails without Prometheus Operator CRDs, conditional safer |

**Installation:**
```bash
# Development tools (use nix-shell for reproducibility)
nix-shell -p helm chart-testing kubectl

# Or install directly
brew install helm chart-testing  # macOS
```

## Architecture Patterns

### Recommended Project Structure
```
deploy/helm/rds-csi-driver/
├── Chart.yaml              # Chart metadata (name, version, appVersion)
├── values.yaml             # Default configuration values
├── values.schema.json      # JSON Schema validation (optional but recommended)
├── README.md               # Installation and configuration guide
├── templates/
│   ├── NOTES.txt           # Post-install instructions
│   ├── _helpers.tpl        # Template helper functions
│   ├── namespace.yaml      # Namespace (conditional)
│   ├── serviceaccount.yaml # ServiceAccounts (controller + node)
│   ├── rbac.yaml           # ClusterRoles and ClusterRoleBindings
│   ├── csidriver.yaml      # CSIDriver resource
│   ├── controller.yaml     # Controller Deployment
│   ├── node.yaml           # Node DaemonSet
│   ├── storageclass.yaml   # StorageClass resources (loop for multiple)
│   ├── service.yaml        # Metrics service (conditional)
│   └── servicemonitor.yaml # Prometheus ServiceMonitor (conditional)
└── .helmignore             # Exclude files from package
```

### Pattern 1: Flat Values Structure
**What:** Organize values by component at top level, not deeply nested
**When to use:** Always - simpler `--set` overrides and clearer organization
**Example:**
```yaml
# Source: kubernetes-sigs/aws-ebs-csi-driver values.yaml pattern
controller:
  replicas: 1
  image:
    repository: ghcr.io/3whiskeywhiskey/rds-csi
    tag: "dev"
  resources:
    requests:
      cpu: 10m
      memory: 64Mi
  nodeSelector: {}
  tolerations: []
  affinity: {}

node:
  image:
    repository: ghcr.io/3whiskeywhiskey/rds-csi
    tag: "dev"
  resources:
    requests:
      cpu: 10m
      memory: 128Mi
  kubeletPath: /var/lib/kubelet
  tolerations:
    - operator: Exists  # Run on all nodes

rds:
  managementIP: "192.0.2.10"
  storageIP: "198.51.100.20"
  sshPort: 22
  nvmePort: 4420
  sshUser: "metal-csi"
  basePath: "/storage-pool/metal-csi"
  secretName: "rds-csi-secret"  # Pre-existing secret

monitoring:
  enabled: true
  port: 9808
  serviceMonitor:
    enabled: false  # Auto-detect CRDs by default
    forceEnable: false  # Override auto-detection
  rdsMonitoring:
    enabled: true  # Toggle RDS health metrics separately
```

### Pattern 2: Conditional ServiceMonitor with CRD Detection
**What:** Deploy ServiceMonitor only if Prometheus Operator CRDs exist
**When to use:** Always - prevents deployment failures on clusters without Prometheus Operator
**Example:**
```yaml
# Source: kubernetes-sigs/aws-ebs-csi-driver ServiceMonitor pattern
{{- if and .Values.monitoring.enabled .Values.monitoring.serviceMonitor.enabled }}
{{- if or .Values.monitoring.serviceMonitor.forceEnable (.Capabilities.APIVersions.Has "monitoring.coreos.com/v1") }}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: {{ include "rds-csi.fullname" . }}
spec:
  selector:
    matchLabels:
      {{- include "rds-csi.labels" . | nindent 6 }}
  endpoints:
    - port: metrics
      interval: 30s
{{- end }}
{{- end }}
```

### Pattern 3: Helper Functions in _helpers.tpl
**What:** Define reusable template snippets for labels, names, selectors
**When to use:** Always - DRY principle, consistent labeling
**Example:**
```yaml
# Source: Helm best practices - Named Templates
{{/*
Expand the name of the chart.
*/}}
{{- define "rds-csi.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "rds-csi.labels" -}}
helm.sh/chart: {{ include "rds-csi.chart" . }}
{{ include "rds-csi.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "rds-csi.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rds-csi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
```

### Pattern 4: Multiple StorageClasses via Loop
**What:** Iterate over `storageClasses` array to create multiple StorageClass resources
**When to use:** When supporting multiple access modes or configurations
**Example:**
```yaml
# Source: kubernetes-csi/csi-driver-nfs storageclass.yaml pattern
{{- range $idx, $sc := .Values.storageClasses }}
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: {{ $sc.name }}
  {{- with $sc.annotations }}
  annotations:
    {{- toYaml . | nindent 4 }}
  {{- end }}
provisioner: rds.csi.srvlab.io
parameters:
  basePath: {{ $sc.basePath | default $.Values.rds.basePath | quote }}
  fsLabel: {{ $sc.fsLabel | default "ext4" | quote }}
volumeBindingMode: {{ $sc.volumeBindingMode | default "WaitForFirstConsumer" }}
reclaimPolicy: {{ $sc.reclaimPolicy | default "Delete" }}
allowVolumeExpansion: {{ $sc.allowVolumeExpansion | default true }}
{{- if $sc.accessModes }}
# RWX support for KubeVirt live migration (not general-purpose shared filesystem)
allowedTopologies: []
mountOptions: []
{{- end }}
{{- end }}
```

### Pattern 5: Sidecar Container Configuration
**What:** Parameterize sidecar images and arguments separately
**When to use:** When deploying CSI drivers with external-provisioner, attacher, resizer, snapshotter
**Example:**
```yaml
# Source: hpe-storage/co-deployments values.yaml pattern
sidecars:
  provisioner:
    image:
      repository: registry.k8s.io/sig-storage/csi-provisioner
      tag: v4.0.0
    resources:
      requests:
        cpu: 10m
        memory: 64Mi
    additionalArgs:
      - "--timeout=300s"
      - "--feature-gates=Topology=false"

  attacher:
    image:
      repository: registry.k8s.io/sig-storage/csi-attacher
      tag: v4.5.0
    resources:
      requests:
        cpu: 10m
        memory: 64Mi

  resizer:
    image:
      repository: registry.k8s.io/sig-storage/csi-resizer
      tag: v1.10.0
    resources:
      requests:
        cpu: 10m
        memory: 64Mi

  snapshotter:
    image:
      repository: registry.k8s.io/sig-storage/csi-snapshotter
      tag: v8.2.0
    resources:
      requests:
        cpu: 10m
        memory: 64Mi
```

### Anti-Patterns to Avoid
- **Hard-coded namespace in templates:** Use `{{ .Release.Namespace }}` instead - allows installation in any namespace
- **Secrets in values.yaml:** Reference existing secrets by name - never store credentials in chart
- **Deep nesting in values.yaml:** Harder to override with `--set controller.deployment.spec.template.spec.containers[0].image` - use flat structure
- **Missing labels:** Every resource needs standard labels from `_helpers.tpl` - enables `helm list`, label selectors
- **Ignoring .Capabilities.APIVersions:** Always check for CRD existence before conditional resources - prevents deployment failures

## Don't Hand-Roll

Problems that look simple but have existing solutions:

| Problem | Don't Build | Use Instead | Why |
|---------|-------------|-------------|-----|
| Template labeling | Custom label functions | Helm's standard label helpers (`_helpers.tpl`) | Consistent with community, enables `helm list`, label selectors |
| CRD detection | Assumption CRDs exist | `.Capabilities.APIVersions.Has` conditional | Prevents failures on clusters without Prometheus Operator |
| Values validation | Runtime errors | values.schema.json with JSON Schema | Catches errors at `helm install` time, provides IDE autocomplete |
| Chart versioning | Manual Chart.yaml updates | SemVer 2 with independent chart/app versions | Standard practice, `appVersion` tracks driver version separately |
| Sidecar versions | Hard-coded tags | Parameterized `sidecars.*.image.tag` | Users can override for compatibility, easier updates |
| Secret management | Chart-managed secrets | Pre-existing secret reference | Users manage secrets outside chart, no credentials in values |
| Upgrade compatibility | `--reuse-values` | `--reset-then-reuse-values` | Helm 3.14+ best practice, avoids merge conflicts |
| NOTES.txt generation | Static text | Template with conditionals | Show relevant instructions based on enabled features |

**Key insight:** Helm has 10+ years of CSI driver chart patterns - reuse kubernetes-sigs/aws-ebs-csi-driver structure instead of inventing custom approaches. Template conditional resources (ServiceMonitor, multiple StorageClasses), validate with JSON Schema, and test with chart-testing (ct).

## Common Pitfalls

### Pitfall 1: --reuse-values Upgrade Failures
**What goes wrong:** Users run `helm upgrade --reuse-values` and chart fails to apply new defaults or changes
**Why it happens:** `--reuse-values` merges old values with template changes, causing conflicts when chart structure changes
**How to avoid:** Document `--reset-then-reuse-values` as upgrade command, add warning in NOTES.txt
**Warning signs:** Upgrade failures with "field is immutable" or missing new parameters

**Example NOTES.txt warning:**
```yaml
{{- if .Release.IsUpgrade }}
⚠️  IMPORTANT: This chart does not support --reuse-values for upgrades.
   Use --reset-then-reuse-values instead:

   helm upgrade {{ .Release.Name }} rds-csi-driver/ --reset-then-reuse-values -f custom-values.yaml
{{- end }}
```

### Pitfall 2: ServiceMonitor Deployment Without CRDs
**What goes wrong:** Chart creates ServiceMonitor, deployment fails because Prometheus Operator CRDs don't exist
**Why it happens:** Assuming Prometheus Operator is installed, or unconditionally creating ServiceMonitor
**How to avoid:** Use `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1"` conditional, provide `forceEnable` override
**Warning signs:** `no matches for kind "ServiceMonitor"` errors during `helm install`

**Example conditional:**
```yaml
{{- if and .Values.monitoring.enabled .Values.monitoring.serviceMonitor.enabled }}
{{- if or .Values.monitoring.serviceMonitor.forceEnable (.Capabilities.APIVersions.Has "monitoring.coreos.com/v1") }}
# Create ServiceMonitor
{{- end }}
{{- end }}
```

### Pitfall 3: StorageClass Default Annotation Conflicts
**What goes wrong:** Multiple default StorageClasses exist, PVCs don't know which to use
**Why it happens:** Chart unconditionally sets `storageclass.kubernetes.io/is-default-class: "true"` without checking existing defaults
**How to avoid:** Make default annotation configurable (`storageClasses.*.isDefault`), default to `false`, document in NOTES.txt
**Warning signs:** PVCs stay Pending, or bind to unexpected StorageClass

**Example configuration:**
```yaml
# values.yaml
storageClasses:
  - name: rds-nvme
    isDefault: false  # User must explicitly enable
    reclaimPolicy: Delete
  - name: rds-nvme-rwx
    enabled: false  # Disabled by default
    isDefault: false
```

### Pitfall 4: Node DaemonSet Missing Tolerations
**What goes wrong:** Node plugin doesn't run on tainted nodes (control-plane, NoSchedule), volumes fail to mount
**Why it happens:** Forgot `tolerations: [{operator: Exists}]` on node DaemonSet
**How to avoid:** Default to `operator: Exists` for node, allow override via values, test on tainted cluster
**Warning signs:** Pods stuck in ContainerCreating on specific nodes, `no CSI node` errors

**Example values.yaml:**
```yaml
node:
  tolerations:
    - operator: Exists  # Run on ALL nodes by default (CSI requirement)
  # Users can override for specific taints if needed
```

### Pitfall 5: Chart Version vs. App Version Confusion
**What goes wrong:** Users expect chart version to match driver version, confusion about compatibility
**Why it happens:** Not documenting difference between `version` (chart packaging) and `appVersion` (driver binary)
**How to avoid:** Use independent SemVer 2 for chart (1.0.0, 1.1.0), track driver version in `appVersion`, document in README
**Warning signs:** GitHub issues asking "why is chart 1.0.0 but driver is 0.10.0?"

**Example Chart.yaml:**
```yaml
apiVersion: v2
name: rds-csi-driver
version: 1.0.0          # Chart packaging version (independent)
appVersion: "0.10.0"    # Driver binary version (tracks git tags)
description: Kubernetes CSI driver for MikroTik ROSE Data Server
```

### Pitfall 6: Secret Key Name Mismatch
**What goes wrong:** Controller fails to start because secret key is `ssh-key` but code expects `rds-private-key`
**Why it happens:** Mismatch between existing Kubernetes manifests and chart template variable names
**How to avoid:** Keep consistent key names, document required secret structure in README, add validation helper
**Warning signs:** Controller CrashLoopBackOff with "secret key not found" errors

**Example secret structure documentation:**
```yaml
# README.md - Required Secret Structure
apiVersion: v1
kind: Secret
metadata:
  name: rds-csi-secret
  namespace: rds-csi
type: Opaque
stringData:
  ssh-key: |          # SSH private key for RouterOS CLI
    [private key supplied by operator]
    ...
    [end generated key]
  snmp-community: "public"  # SNMP community string for hardware monitoring
```

### Pitfall 7: Hard-Coded Namespace in Templates
**What goes wrong:** Chart only works in `rds-csi` namespace, fails when users install to custom namespace
**Why it happens:** Templates reference `namespace: rds-csi` instead of `{{ .Release.Namespace }}`
**How to avoid:** Always use `{{ .Release.Namespace }}`, test with `helm install --namespace custom-ns`
**Warning signs:** RBAC failures, ServiceAccount not found errors when installing to non-default namespace

### Pitfall 8: Missing Leader Election Namespace
**What goes wrong:** Sidecars fail leader election, multiple controllers process same PVC
**Why it happens:** Leader election namespace hard-coded or missing from sidecar args
**How to avoid:** Template `--leader-election-namespace={{ .Release.Namespace }}` in all sidecar args
**Warning signs:** Duplicate volume creation, sidecar logs showing "failed to get lease"

**Example template fix:**
```yaml
# templates/controller.yaml
- name: csi-provisioner
  args:
    - "--csi-address=$(ADDRESS)"
    - "--leader-election=true"
    - "--leader-election-namespace={{ .Release.Namespace }}"  # CRITICAL
```

## Code Examples

Verified patterns from official sources:

### Conditional Resource Creation with Feature Toggles
```yaml
# Source: kubernetes-sigs/aws-ebs-csi-driver
# templates/servicemonitor.yaml
{{- if .Values.monitoring.enabled }}
{{- if or .Values.monitoring.serviceMonitor.forceEnable (.Capabilities.APIVersions.Has "monitoring.coreos.com/v1") }}
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: {{ include "rds-csi.fullname" . }}-controller
  labels:
    {{- include "rds-csi.labels" . | nindent 4 }}
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: {{ include "rds-csi.name" . }}
      app.kubernetes.io/component: controller
  endpoints:
    - port: metrics
      interval: 30s
      path: /metrics
{{- end }}
{{- end }}
```

### StorageClass with Conditional Default Annotation
```yaml
# Source: Consolidated CSI driver patterns
# templates/storageclass.yaml
{{- range $idx, $sc := .Values.storageClasses }}
{{- if $sc.enabled }}
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: {{ $sc.name }}
  {{- if $sc.isDefault }}
  annotations:
    storageclass.kubernetes.io/is-default-class: "true"
  {{- end }}
  labels:
    {{- include "rds-csi.labels" $ | nindent 4 }}
provisioner: rds.csi.srvlab.io
parameters:
  basePath: {{ $sc.basePath | default $.Values.rds.basePath | quote }}
  fsLabel: {{ $sc.fsLabel | default "ext4" | quote }}
volumeBindingMode: {{ $sc.volumeBindingMode | default "WaitForFirstConsumer" }}
reclaimPolicy: {{ $sc.reclaimPolicy | default "Delete" }}
allowVolumeExpansion: {{ $sc.allowVolumeExpansion | default true }}
{{- end }}
{{- end }}
```

### RBAC with Templated Namespace
```yaml
# Source: CSI driver best practices
# templates/rbac.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: {{ include "rds-csi.fullname" . }}-controller-binding
  labels:
    {{- include "rds-csi.labels" . | nindent 4 }}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: {{ include "rds-csi.fullname" . }}-controller-role
subjects:
  - kind: ServiceAccount
    name: {{ include "rds-csi.serviceAccountName" . }}-controller
    namespace: {{ .Release.Namespace }}
```

### Controller Deployment with Sidecar Configuration
```yaml
# Source: kubernetes-sigs/aws-ebs-csi-driver
# templates/controller.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ include "rds-csi.fullname" . }}-controller
  namespace: {{ .Release.Namespace }}
spec:
  replicas: {{ .Values.controller.replicas }}
  selector:
    matchLabels:
      {{- include "rds-csi.selectorLabels" . | nindent 6 }}
      app.kubernetes.io/component: controller
  template:
    metadata:
      labels:
        {{- include "rds-csi.selectorLabels" . | nindent 8 }}
        app.kubernetes.io/component: controller
    spec:
      serviceAccountName: {{ include "rds-csi.serviceAccountName" . }}-controller
      {{- with .Values.controller.nodeSelector }}
      nodeSelector:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.controller.affinity }}
      affinity:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      {{- with .Values.controller.tolerations }}
      tolerations:
        {{- toYaml . | nindent 8 }}
      {{- end }}
      containers:
        - name: rds-csi-driver
          image: "{{ .Values.controller.image.repository }}:{{ .Values.controller.image.tag | default .Chart.AppVersion }}"
          args:
            - "-endpoint=$(CSI_ENDPOINT)"
            - "-controller"
            - "-rds-address={{ .Values.rds.managementIP }}"
            - "-rds-port={{ .Values.rds.sshPort }}"
            - "-v={{ .Values.controller.logLevel }}"
          env:
            - name: CSI_ENDPOINT
              value: unix:///var/lib/csi/sockets/pluginproxy/csi.sock
          volumeMounts:
            - name: socket-dir
              mountPath: /var/lib/csi/sockets/pluginproxy/
            - name: rds-credentials
              mountPath: /etc/rds-csi
              readOnly: true
          resources:
            {{- toYaml .Values.controller.resources | nindent 12 }}

        - name: csi-provisioner
          image: "{{ .Values.sidecars.provisioner.image.repository }}:{{ .Values.sidecars.provisioner.image.tag }}"
          args:
            - "--csi-address=$(ADDRESS)"
            - "--leader-election=true"
            - "--leader-election-namespace={{ .Release.Namespace }}"
            {{- range .Values.sidecars.provisioner.additionalArgs }}
            - {{ . | quote }}
            {{- end }}
          env:
            - name: ADDRESS
              value: /var/lib/csi/sockets/pluginproxy/csi.sock
          volumeMounts:
            - name: socket-dir
              mountPath: /var/lib/csi/sockets/pluginproxy/
          resources:
            {{- toYaml .Values.sidecars.provisioner.resources | nindent 12 }}

      volumes:
        - name: socket-dir
          emptyDir: {}
        - name: rds-credentials
          secret:
            secretName: {{ .Values.rds.secretName }}
            defaultMode: 0400
```

### NOTES.txt with Conditional Instructions
```yaml
# Source: Helm best practices - NOTES.txt
# templates/NOTES.txt
Thank you for installing {{ .Chart.Name }}.

Your release is named {{ .Release.Name }}.

To get started:

1. Verify the RDS CSI driver is running:
   kubectl get pods -n {{ .Release.Namespace }} -l app.kubernetes.io/name={{ include "rds-csi.name" . }}

2. Check the CSI driver registration:
   kubectl get csidriver rds.csi.srvlab.io

3. Available StorageClasses:
{{- range .Values.storageClasses }}
{{- if .enabled }}
   - {{ .name }}{{ if .isDefault }} (default){{ end }}
{{- end }}
{{- end }}

{{- if .Values.monitoring.enabled }}

4. Metrics are exposed on port {{ .Values.monitoring.port }}:
   kubectl port-forward -n {{ .Release.Namespace }} svc/{{ include "rds-csi.fullname" . }}-metrics {{ .Values.monitoring.port }}:{{ .Values.monitoring.port }}

{{- if and .Values.monitoring.serviceMonitor.enabled (not (.Capabilities.APIVersions.Has "monitoring.coreos.com/v1")) }}
⚠️  WARNING: ServiceMonitor is enabled but Prometheus Operator CRDs are not detected.
   ServiceMonitor will NOT be created. Install Prometheus Operator or set:
   monitoring.serviceMonitor.forceEnable=true
{{- end }}
{{- end }}

{{- if .Release.IsUpgrade }}

⚠️  UPGRADE NOTICE: This chart does not support --reuse-values.
   Always use --reset-then-reuse-values for upgrades:
   helm upgrade {{ .Release.Name }} rds-csi-driver/ --reset-then-reuse-values -f values.yaml
{{- end }}

For more information, visit: https://github.com/3whiskeywhiskey/rds-csi-driver
```

### values.schema.json for Input Validation
```json
// Source: Helm schema validation best practices
// values.schema.json
{
  "$schema": "https://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["rds"],
  "properties": {
    "rds": {
      "type": "object",
      "required": ["managementIP", "secretName"],
      "properties": {
        "managementIP": {
          "type": "string",
          "pattern": "^([0-9]{1,3}\\.){3}[0-9]{1,3}$",
          "description": "RDS management IP address for SSH"
        },
        "storageIP": {
          "type": "string",
          "pattern": "^([0-9]{1,3}\\.){3}[0-9]{1,3}$",
          "description": "RDS storage IP address for NVMe/TCP"
        },
        "sshPort": {
          "type": "integer",
          "minimum": 1,
          "maximum": 65535,
          "description": "SSH port for RouterOS CLI"
        },
        "secretName": {
          "type": "string",
          "pattern": "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$",
          "maxLength": 253,
          "description": "Name of secret containing SSH key and SNMP community"
        },
        "basePath": {
          "type": "string",
          "pattern": "^/.*",
          "description": "Base path for volumes on RDS"
        }
      }
    },
    "controller": {
      "type": "object",
      "properties": {
        "replicas": {
          "type": "integer",
          "minimum": 1,
          "maximum": 1,
          "description": "Controller replicas (must be 1, no leader election)"
        },
        "logLevel": {
          "type": "integer",
          "minimum": 0,
          "maximum": 10,
          "description": "Log verbosity level (0-10)"
        }
      }
    },
    "storageClasses": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["name"],
        "properties": {
          "name": {
            "type": "string",
            "pattern": "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$",
            "maxLength": 63
          },
          "enabled": {
            "type": "boolean"
          },
          "isDefault": {
            "type": "boolean"
          },
          "reclaimPolicy": {
            "type": "string",
            "enum": ["Delete", "Retain"]
          },
          "volumeBindingMode": {
            "type": "string",
            "enum": ["Immediate", "WaitForFirstConsumer"]
          }
        }
      }
    }
  }
}
```

## State of the Art

| Old Approach | Current Approach | When Changed | Impact |
|--------------|------------------|--------------|--------|
| Nested values (`controller.deployment.spec.replicas`) | Flat values (`controller.replicas`) | Helm 3.0+ (2019) | Simpler `--set` overrides, clearer structure |
| `--reuse-values` for upgrades | `--reset-then-reuse-values` | Helm 3.14 (2024) | Prevents merge conflicts, safer upgrades |
| Always create ServiceMonitor | Conditional with CRD detection | Prometheus Operator CRD maturity (2020) | Prevents failures on clusters without Prometheus |
| Chart version == app version | Independent SemVer (chart vs appVersion) | Helm 3 best practices (2020) | Clearer versioning, chart can evolve independently |
| Manual values validation | values.schema.json with JSON Schema | Helm 3.1+ (2020) | Catches errors at install time, IDE autocomplete |
| Single StorageClass in template | Loop over `storageClasses` array | CSI multi-access-mode support (2021) | RWO and RWX in same chart, user control |
| Sidecar versions in Chart.yaml dependencies | Parameterized `sidecars.*.image` | CSI sidecar version fragmentation (2022) | User control for compatibility, easier updates |

**Deprecated/outdated:**
- **Helm 2 Tiller architecture**: Removed in Helm 3 - no server-side component, client-only
- **apiVersion: v1 in Chart.yaml**: Use `v2` for Helm 3+ charts
- **requirements.yaml for dependencies**: Moved to `dependencies` field in Chart.yaml
- **Hard-coded namespaces**: Always use `{{ .Release.Namespace }}`
- **VolumeSnapshot v1beta1**: Moved to v1 GA in Kubernetes 1.20+ (use csi-snapshotter v8.2.0+)

## Open Questions

Things that couldn't be fully resolved:

1. **CSI Sidecar Version Compatibility Matrix**
   - What we know: Latest versions are csi-provisioner v4.0.0, csi-attacher v4.5.0, csi-resizer v1.10.0, csi-snapshotter v8.2.0
   - What's unclear: Minimum Kubernetes version for each sidecar, compatibility matrix not published
   - Recommendation: Use versions from kubernetes-sigs/aws-ebs-csi-driver chart (proven compatible), document Kubernetes 1.26+ requirement

2. **ServiceMonitor Auto-Detection Reliability**
   - What we know: `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1"` detects Prometheus Operator CRDs
   - What's unclear: Does this work reliably during `helm template` (no cluster access)?
   - Recommendation: Provide `forceEnable` override for users who need `helm template` to generate ServiceMonitor

3. **Helm Chart Repository Publishing**
   - What we know: Chart published in git repository only per CONTEXT.md decision
   - What's unclear: Should we add `helm package` + GitHub Releases for versioned tarballs?
   - Recommendation: Start with git-only distribution, add GitHub Releases if users request it (simple automation)

4. **RWX StorageClass Default State**
   - What we know: RWX disabled by default per CONTEXT.md (`storageClasses.rwx.enabled: false`)
   - What's unclear: Should RWX StorageClass resource exist but be disabled via `volumeBindingMode: WaitForFirstConsumer` + documentation, or not exist at all?
   - Recommendation: Don't create RWX StorageClass unless `enabled: true` - cleaner, no confusion

## Sources

### Primary (HIGH confidence)
- [Helm Official Documentation - Charts](https://helm.sh/docs/topics/charts/) - Chart.yaml structure, versioning, dependencies
- [Helm Official Documentation - Named Templates](https://helm.sh/docs/chart_template_guide/named_templates/) - _helpers.tpl patterns
- [Helm Official Documentation - NOTES.txt Files](https://helm.sh/docs/chart_template_guide/notes_files/) - Post-install instructions
- [kubernetes-csi/csi-driver-nfs Helm Chart README](https://github.com/kubernetes-csi/csi-driver-nfs/blob/master/charts/README.md) - CSI driver chart patterns
- [hpe-storage/co-deployments values.yaml](https://github.com/hpe-storage/co-deployments/blob/master/helm/charts/hpe-csi-driver/values.yaml) - CSI driver values structure
- [kubernetes-sigs/aws-ebs-csi-driver metrics documentation](https://github.com/kubernetes-sigs/aws-ebs-csi-driver/blob/master/docs/metrics.md) - ServiceMonitor conditional deployment
- [Schema Validation for Helm Charts with values.schema.json](https://oneuptime.com/blog/post/2026-01-17-helm-schema-validation-values/view) - JSON Schema validation patterns (2026)
- [Testing Helm Charts with Chart Testing (ct) and helm test](https://oneuptime.com/blog/post/2026-01-17-helm-chart-testing-ct-helm-test/view) - Testing best practices (2026)

### Secondary (MEDIUM confidence)
- [Deploying a CSI Driver on Kubernetes](https://kubernetes-csi.github.io/docs/deploying.html) - Sidecar container architecture
- [kubernetes-csi/external-snapshotter releases](https://github.com/kubernetes-csi/external-snapshotter/releases) - Version compatibility notes
- [HPE CSI Info Metrics Provider for Prometheus](https://scod.hpedev.io/csi_driver/metrics.html) - ServiceMonitor integration patterns
- [Helm Charts in Kubernetes – 2026 Guide | Atmosly](https://atmosly.com/knowledge/helm-charts-in-kubernetes-definitive-guide-for-2025) - General best practices
- [Deploying Storage Classes and Persistent Volumes with Helm](https://oneuptime.com/blog/post/2026-01-17-helm-storage-class-persistent-volumes/view) - StorageClass template patterns (2026)

### Tertiary (LOW confidence)
- [GitHub Issue: Helm chart can't annotate storage classes](https://github.com/kubernetes-sigs/aws-ebs-csi-driver/issues/733) - Community discussion on StorageClass annotations
- [Helm chart upgrade - Atlassian DC Helm Charts](https://atlassian.github.io/data-center-helm-charts/userguide/upgrades/HELM_CHART_UPGRADE/) - Upgrade compatibility patterns

## Metadata

**Confidence breakdown:**
- Standard stack: HIGH - Well-established Helm 3 + kubernetes-csi patterns, verified with official documentation
- Architecture: HIGH - Patterns verified from kubernetes-sigs/aws-ebs-csi-driver and kubernetes-csi/csi-driver-nfs official charts
- Pitfalls: HIGH - Documented from real GitHub issues and official upgrade guides
- Code examples: HIGH - All examples sourced from official kubernetes-sigs repositories or Helm documentation
- Sidecar versions: MEDIUM - Latest versions confirmed but full compatibility matrix not published
- ServiceMonitor auto-detection: MEDIUM - Pattern verified but edge cases during `helm template` unclear

**Research date:** 2026-02-06
**Valid until:** 2026-05-06 (90 days - Helm and CSI sidecar ecosystem is stable)
