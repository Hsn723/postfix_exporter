{{/*
Expand the name of the chart.
*/}}
{{- define "project.name" -}}
{{- default .Chart.Name | trunc 63 | trimSuffix "-" | replace "_" "-"}}
{{- end }}

{{/*
Create a default fully qualified app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "project.fullname" -}}
{{- $name := default .Chart.Name }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Create a namespaced app name.
We truncate at 63 chars because some Kubernetes name fields are limited to this (by the DNS naming spec).
If release name contains chart name it will be used as a full name.
*/}}
{{- define "project.namespacedname" -}}
{{- $name := default .Chart.Name }}
{{- if contains $name .Release.Name }}
{{- printf "%s/%s" .Release.Namespace .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s/%s-%s" .Release.Namespace .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "project.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "project.labels" -}}
helm.sh/chart: {{ include "project.chart" . }}
app.kubernetes.io/name: {{ include "project.name" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}
