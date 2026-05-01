{{/*
Common labels for all resources owned by a KusoProject.
*/}}
{{- define "kusoproject.labels" -}}
app.kubernetes.io/name: kusoproject
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
kuso.sislelabs.com/project: {{ .Release.Name }}
{{- end }}
