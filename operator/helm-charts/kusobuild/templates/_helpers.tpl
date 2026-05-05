{{- define "kusobuild.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kusobuild.labels" -}}
app.kubernetes.io/name: kusobuild
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
# component=kusobuild is the pod-level marker server-go counts at
# admission time. Without this label kuso-server can't see the build
# in cluster-truth queries and would over-admit.
app.kubernetes.io/component: kusobuild
kuso.sislelabs.com/project: {{ .Values.project | default "unknown" }}
kuso.sislelabs.com/service: {{ .Values.service | default "unknown" }}
kuso.sislelabs.com/build-ref: {{ .Values.ref | default "unknown" | quote }}
{{- end }}
