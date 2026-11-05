{{- define "github-app-factory.fullname" -}}
{{- printf "%s-%s" .Release.Name .Chart.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "github-app-factory.selectorLabels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "github-app-factory.labels" -}}
{{ include "github-app-factory.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | quote }}
{{- end -}}

{{- define "github-app-factory.validate" -}}
{{- if not .Values.keyVault.url -}}
  {{- fail "keyVault.url is required; this chart cannot use mock Vault" -}}
{{- end -}}
{{- if or (not .Values.workloadIdentity.clientId) (not .Values.workloadIdentity.tenantId) -}}
  {{- fail "Azure Workload Identity requires clientId and tenantId" -}}
{{- end -}}
{{- end -}}
