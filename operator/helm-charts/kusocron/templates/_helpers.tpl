{{- define "kusocron.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kusocron.labels" -}}
app.kubernetes.io/name: kusocron
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
kuso.sislelabs.com/project: {{ .Values.project | default "unknown" }}
kuso.sislelabs.com/service: {{ .Values.service | default "unknown" }}
kuso.sislelabs.com/cron: {{ .Release.Name }}
{{- end }}
