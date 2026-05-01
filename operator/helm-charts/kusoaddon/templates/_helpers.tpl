{{- define "kusoaddon.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Connection secret name. Convention from docs/REDESIGN.md:
  <project>-<addon>-conn
The server reads addons in a project, collects these secret names, and
populates KusoEnvironment.spec.envFromSecrets so every container gets the
addon's connection envs via envFrom.
*/}}
{{- define "kusoaddon.connSecretName" -}}
{{ .Values.project }}-{{ .Release.Name }}-conn
{{- end -}}

{{- define "kusoaddon.labels" -}}
app.kubernetes.io/name: kusoaddon
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
kuso.sislelabs.com/project: {{ .Values.project | default "unknown" }}
kuso.sislelabs.com/addon: {{ .Release.Name }}
kuso.sislelabs.com/addon-kind: {{ .Values.kind }}
{{- end }}

{{- define "kusoaddon.selectorLabels" -}}
app.kubernetes.io/name: kusoaddon
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
storage size by t-shirt size. Override via .Values.storageSize.
*/}}
{{- define "kusoaddon.storageSize" -}}
{{- if .Values.storageSize -}}
{{ .Values.storageSize }}
{{- else if eq .Values.size "small" -}}
5Gi
{{- else if eq .Values.size "medium" -}}
20Gi
{{- else if eq .Values.size "large" -}}
100Gi
{{- else -}}
5Gi
{{- end -}}
{{- end -}}
