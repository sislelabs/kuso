{{- define "kusoservice.labels" -}}
app.kubernetes.io/name: kusoservice
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
kuso.sislelabs.com/project: {{ .Values.project | default "unknown" }}
kuso.sislelabs.com/service: {{ .Release.Name }}
{{- end }}
