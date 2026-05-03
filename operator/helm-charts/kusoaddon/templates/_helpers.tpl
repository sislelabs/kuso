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
{{ .Release.Name }}-conn
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
Placement renders nodeSelector + (optional) hostname-restricted
nodeAffinity from .Values.placement. Mirrors the kusoenvironment
chart so a service and its addon land on the same set of nodes
when the operator pins both. region label also gets a matching
toleration so the kuso.sislelabs.com/region NoSchedule taint
doesn't block scheduling.
*/}}
{{- define "kusoaddon.placement" -}}
{{- with .Values.placement }}
{{- if .labels }}
nodeSelector:
  {{- range $k, $v := .labels }}
  {{ printf "kuso.sislelabs.com/%s" $k }}: {{ $v | quote }}
  {{- end }}
{{- end }}
{{- if .nodes }}
affinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
        - matchExpressions:
            - key: kubernetes.io/hostname
              operator: In
              values:
                {{- range .nodes }}
                - {{ . | quote }}
                {{- end }}
{{- end }}
{{- if .labels.region }}
tolerations:
  - key: kuso.sislelabs.com/region
    operator: Equal
    value: {{ .labels.region | quote }}
    effect: NoSchedule
{{- end }}
{{- end }}
{{- end -}}

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
