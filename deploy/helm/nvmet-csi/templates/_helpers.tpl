{{/*
Expand the name of the chart.
*/}}
{{- define "nvmet-csi.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "nvmet-csi.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "nvmet-csi.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "nvmet-csi.labels" -}}
helm.sh/chart: {{ include "nvmet-csi.chart" . }}
{{ include "nvmet-csi.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "nvmet-csi.selectorLabels" -}}
app.kubernetes.io/name: {{ include "nvmet-csi.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Controller-specific selector labels
*/}}
{{- define "nvmet-csi.controllerSelectorLabels" -}}
{{ include "nvmet-csi.selectorLabels" . }}
app.kubernetes.io/component: controller
{{- end }}

{{/*
Node-specific selector labels
*/}}
{{- define "nvmet-csi.nodeSelectorLabels" -}}
{{ include "nvmet-csi.selectorLabels" . }}
app.kubernetes.io/component: node
{{- end }}

{{/*
Create the name of the controller service account to use
*/}}
{{- define "nvmet-csi.controllerServiceAccountName" -}}
{{- printf "%s-controller" (include "nvmet-csi.fullname" .) }}
{{- end }}

{{/*
Create the name of the node service account to use
*/}}
{{- define "nvmet-csi.nodeServiceAccountName" -}}
{{- printf "%s-node" (include "nvmet-csi.fullname" .) }}
{{- end }}

{{/*
CSI driver name (used in CSIDriver, StorageClass, etc.)
*/}}
{{- define "nvmet-csi.driverName" -}}
{{- default "nvmet.csi.whiskey.works" .Values.driverName }}
{{- end }}

{{/*
Determine the image tag to use (from values or Chart.appVersion)
*/}}
{{- define "nvmet-csi.imageTag" -}}
{{- if .tag }}
{{- .tag }}
{{- else }}
{{- $.Chart.AppVersion }}
{{- end }}
{{- end }}

{{/*
Controller image with tag
*/}}
{{- define "nvmet-csi.controllerImage" -}}
{{- $tag := include "nvmet-csi.imageTag" (merge (dict "Chart" .Chart) .Values.controller.image) }}
{{- printf "%s:%s" .Values.controller.image.repository $tag }}
{{- end }}

{{/*
Node image with tag
*/}}
{{- define "nvmet-csi.nodeImage" -}}
{{- $tag := include "nvmet-csi.imageTag" (merge (dict "Chart" .Chart) .Values.node.image) }}
{{- printf "%s:%s" .Values.node.image.repository $tag }}
{{- end }}

{{/*
Scheduled snapshot ServiceAccount name
*/}}
{{- define "nvmet-csi.snapshotScheduleServiceAccountName" -}}
{{- printf "%s-snapshot-scheduler" (include "nvmet-csi.fullname" .) }}
{{- end }}
