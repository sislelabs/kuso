{{- define "kusoservice.labels" -}}
app.kubernetes.io/name: kusoservice
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
kuso.sislelabs.com/project: {{ .Values.project | default "unknown" | quote }}
kuso.sislelabs.com/service: {{ .Release.Name | quote }}
{{- end }}
