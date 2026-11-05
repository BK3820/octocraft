{{- define "octocraft.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "octocraft.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "octocraft.labels" -}}
{{ include "octocraft.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{- end -}}

{{- define "octocraft.validate" -}}
{{- if not .Values.keyVault.url -}}
  {{- fail "keyVault.url is required; this chart cannot use mock Vault" -}}
{{- end -}}
{{- if or (not .Values.workloadIdentity.clientId) (not .Values.workloadIdentity.tenantId) -}}
  {{- fail "Azure Workload Identity requires clientId and tenantId" -}}
{{- end -}}
{{- end -}}
