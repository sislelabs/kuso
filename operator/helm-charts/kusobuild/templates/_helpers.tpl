{{- define "kusobuild.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kusobuild.labels" -}}
app.kubernetes.io/name: kusobuild
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
kuso.sislelabs.com/project: {{ .Values.project | default "unknown" }}
kuso.sislelabs.com/service: {{ .Values.service | default "unknown" }}
kuso.sislelabs.com/build-ref: {{ .Values.ref | default "unknown" | quote }}
{{- end }}
