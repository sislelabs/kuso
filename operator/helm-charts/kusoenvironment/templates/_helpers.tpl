{{- define "kusoenvironment.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "kusoenvironment.labels" -}}
app.kubernetes.io/name: kusoenvironment
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
kuso.sislelabs.com/project: {{ .Values.project | default "unknown" }}
kuso.sislelabs.com/service: {{ .Values.service | default "unknown" }}
kuso.sislelabs.com/env-kind: {{ .Values.kind | default "production" }}
{{- end }}

{{- define "kusoenvironment.selectorLabels" -}}
app.kubernetes.io/name: kusoenvironment
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}
