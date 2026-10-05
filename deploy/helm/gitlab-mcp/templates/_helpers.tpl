{{- define "gitlab-mcp.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "gitlab-mcp.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s" (include "gitlab-mcp.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "gitlab-mcp.labels" -}}
app.kubernetes.io/name: {{ include "gitlab-mcp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version }}
{{- end -}}

{{- define "gitlab-mcp.selectorLabels" -}}
app.kubernetes.io/name: {{ include "gitlab-mcp.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "gitlab-mcp.serviceAccountName" -}}
{{ include "gitlab-mcp.fullname" . }}
{{- end -}}

{{/* Secret name holding one instance's credentials (GitLab token, optional ca.crt). */}}
{{- define "gitlab-mcp.instanceSecretName" -}}
{{- printf "%s-inst-%s" (include "gitlab-mcp.fullname" .root) .instance.name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
