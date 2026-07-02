{{- define "kusoaddon.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- /*
podSecurityContext / containerSecurityContext

Set runAsNonRoot=true plus the official non-root UID/GID for each
upstream addon image. Without the explicit UID, the kubelet sees
the image's default user (which for stock postgres / redis / etc.
is `root` in the image metadata even when the entrypoint later
drops to a non-root account), and the runAsNonRoot=true policy
fails the container before the entrypoint can run.

Defaults pulled from each upstream image's docs:
  postgres:16       → UID 999  (postgres)
  redis:7           → UID 999  (redis)
  meilisearch       → UID 1000 (meili)
  clickhouse        → UID 101  (clickhouse)
  bitnami/minio (s3)→ UID 1001
  axllent/mailpit   → UID 1000
  nats:2            → UID 1000

We fall back to UID 1000 for any kind we haven't mapped explicitly
(safe default — most upstream images either honor it or have a
matching `nobody` user).
*/ -}}
{{- define "kusoaddon.uidForKind" -}}
{{- if eq .Values.kind "postgres" -}}999
{{- else if eq .Values.kind "redis" -}}999
{{- else if eq .Values.kind "valkey" -}}999
{{- else if eq .Values.kind "mongodb" -}}999
{{- else if eq .Values.kind "rabbitmq" -}}999
{{- else if eq .Values.kind "clickhouse" -}}101
{{- else if eq .Values.kind "s3" -}}1001
{{- else -}}1000
{{- end -}}
{{- end -}}

{{- define "kusoaddon.podSecurityContext" -}}
securityContext:
  runAsNonRoot: true
  runAsUser: {{ include "kusoaddon.uidForKind" . }}
  runAsGroup: {{ include "kusoaddon.uidForKind" . }}
  fsGroup: {{ include "kusoaddon.uidForKind" . }}
  seccompProfile:
    type: RuntimeDefault
{{- end -}}

{{- define "kusoaddon.containerSecurityContext" -}}
securityContext:
  allowPrivilegeEscalation: false
  capabilities:
    drop: ["ALL"]
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

{{/*
Container resources by t-shirt size. Every engine template renders this
via `include "kusoaddon.resources"` instead of `toYaml .Values.resources`
so an addon always lands with requests+limits — without them a datastore
sizes itself off the whole node (Redpanda/Seastar was observed reserving
14GB idle on a 15GB node because nothing capped it).

An explicit .Values.resources wins verbatim (BYO escape hatch). Otherwise
we derive from .Values.size. Requests are deliberately modest (schedulable
on a small node); limits are the real memory ceiling the engine must
respect. memRequestMB / memLimitMB below MUST stay in step with these so
Redpanda's --memory flag matches its cgroup limit.
*/}}
{{- define "kusoaddon.resources" -}}
{{- if .Values.resources -}}
{{- toYaml .Values.resources -}}
{{- else if eq .Values.size "medium" -}}
requests:
  cpu: 250m
  memory: 512Mi
limits:
  cpu: "2"
  memory: 2Gi
{{- else if eq .Values.size "large" -}}
requests:
  cpu: 500m
  memory: 1Gi
limits:
  cpu: "4"
  memory: 4Gi
{{- else -}}
requests:
  cpu: 100m
  memory: 256Mi
limits:
  cpu: "1"
  memory: 1Gi
{{- end -}}
{{- end -}}

{{/*
Redpanda --memory value in whole MB. Seastar pre-reserves this at startup
regardless of load, so it must sit UNDER the container memory limit from
kusoaddon.resources (leave headroom for the entrypoint/rpk sidecar work
and page cache). We target ~75% of the size's memory limit:
  small  → 1Gi limit  → 768M
  medium → 2Gi limit  → 1536M
  large  → 4Gi limit  → 3072M
When a user overrides .Values.resources we can't reliably parse their
limit string in templating, so fall back to the small default; a user
sophisticated enough to override resources can also override version/args
via raw helm if they need a bigger broker.
*/}}
{{- define "kusoaddon.redpandaMemMB" -}}
{{- if .Values.resources -}}
768
{{- else if eq .Values.size "medium" -}}
1536
{{- else if eq .Values.size "large" -}}
3072
{{- else -}}
768
{{- end -}}
{{- end -}}
