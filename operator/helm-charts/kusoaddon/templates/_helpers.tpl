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

{{- /* Pod-level hardening. The SA-token drop is gated on spec.podHardening,
       which kuso-server sets only when it CREATES an addon: flipping it
       on an existing addon changes the pod template and restarts the DB,
       so existing addons render byte-identical until someone opts them
       in (kubectl patch spec.podHardening=true). It lives in this helper
       so every template that includes it (s3.yaml too) picks it up. No
       addon image talks to the kube API. runAsUser/fsGroup are unchanged
       from what live StatefulSets already run with. */ -}}
{{- define "kusoaddon.podSecurityContext" -}}
{{- if .Values.podHardening -}}
automountServiceAccountToken: false
{{ end -}}
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

{{- /* Alias of containerSecurityContext under the name other templates
       are told to use. */ -}}
{{- define "kusoaddon.securityContext" -}}
{{- include "kusoaddon.containerSecurityContext" . -}}
{{- end -}}

{{- /* MUST stay byte-identical to kusoenvironment.subscriberKey: env pods
       that mount <conn> carry this label key, and the addon's
       subscribers NetworkPolicy admits them by it. */ -}}
{{- define "kusoaddon.subscriberKey" -}}
{{- printf "conn.kuso.sislelabs.com/%s" (sha256sum . | trunc 20) -}}
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
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
kuso.sislelabs.com/project: {{ .Values.project | default "unknown" | quote }}
kuso.sislelabs.com/addon: {{ .Release.Name | quote }}
kuso.sislelabs.com/addon-kind: {{ .Values.kind | quote }}
{{- end }}

{{- /* Labels for pods CNPG creates (HA postgres instances + Pooler). Only
       the kuso.* identity labels: the app.kubernetes.io selector labels
       would pull them into Services that select this release's own pods. */ -}}
{{- define "kusoaddon.cnpgPodLabels" -}}
kuso.sislelabs.com/project: {{ .Values.project | default "unknown" | quote }}
kuso.sislelabs.com/addon: {{ .Release.Name | quote }}
kuso.sislelabs.com/addon-kind: {{ .Values.kind | quote }}
{{- end }}

{{- define "kusoaddon.selectorLabels" -}}
app.kubernetes.io/name: kusoaddon
app.kubernetes.io/instance: {{ .Release.Name | quote }}
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
{{- /*
  Value labels → nodeSelector (exact match). Empty-value labels are
  capability flags (e.g. `gpu`) → nodeAffinity Exists (a nodeSelector
  value "" would exact-match the empty string, not check presence).
  Mirrors kusoenvironment/templates/deployment.yaml. Empty-label Exists
  terms + the hostname pin (.nodes) share one matchExpressions list.
*/}}
{{- $valueLabels := dict }}
{{- $flagLabels := list }}
{{- range $k, $v := .labels }}
{{- if $v }}{{ $_ := set $valueLabels $k $v }}{{ else }}{{ $flagLabels = append $flagLabels $k }}{{ end }}
{{- end }}
{{- if $valueLabels }}
nodeSelector:
  {{- range $k, $v := $valueLabels }}
  {{ printf "kuso.sislelabs.com/%s" $k }}: {{ $v | quote }}
  {{- end }}
{{- end }}
{{- if or .nodes $flagLabels }}
affinity:
  nodeAffinity:
    requiredDuringSchedulingIgnoredDuringExecution:
      nodeSelectorTerms:
        - matchExpressions:
            {{- range $flagLabels }}
            - key: {{ printf "kuso.sislelabs.com/%s" . | quote }}
              operator: Exists
            {{- end }}
            {{- if .nodes }}
            - key: kubernetes.io/hostname
              operator: In
              values:
                {{- range .nodes }}
                - {{ . | quote }}
                {{- end }}
            {{- end }}
{{- end }}
{{- if and .labels .labels.region }}
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

SECURITY: storageSize is user-supplied (KusoAddon.spec.storageSize, set
by anyone with project-editor rights) and is rendered into 15 manifests.
It used to be emitted RAW. A YAML block scalar could therefore carry a
newline plus `---` and inject a COMPLETE, SEPARATE Kubernetes object into
the release — reproduced with a privileged, hostPID Pod, which Helm's
kind-ordering even installs BEFORE the StatefulSet. On a namespace with
no PodSecurity labels (notably the home namespace, which deliberately
carries none so buildkitd can run privileged) that is node compromise
from a semi-trusted role.

`regexMatch` against a strict Kubernetes quantity is the fix: anything
that is not a bare quantity falls back to the t-shirt default, so a
malicious value renders as `5Gi` instead of as markup. Quoting alone
would not be enough here — the value lands in a context where a
multi-line scalar can still break out.

The API boundary validates this too (addons.validateStorageSize); this
layer also covers a CR applied directly with kubectl.
*/}}
{{- define "kusoaddon.storageSize" -}}
{{- if and .Values.storageSize (regexMatch "^[0-9]+(\\.[0-9]+)?(E|P|T|G|M|K|Ei|Pi|Ti|Gi|Mi|Ki)?$" (toString .Values.storageSize)) -}}
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
When a user overrides .Values.resources with a plain Gi/Mi memory limit we
take 75% of it; any other limit shape falls back to the small default.
*/}}
{{- define "kusoaddon.redpandaMemMB" -}}
{{- $lim := "" -}}
{{- if and .Values.resources (kindIs "map" .Values.resources.limits) -}}
{{- $lim = toString (default "" .Values.resources.limits.memory) -}}
{{- end -}}
{{- if regexMatch "^[0-9]+Gi$" $lim -}}
{{- div (mul (int (trimSuffix "Gi" $lim)) 1024 3) 4 -}}
{{- else if regexMatch "^[0-9]+Mi$" $lim -}}
{{- div (mul (int (trimSuffix "Mi" $lim)) 3) 4 -}}
{{- else if .Values.resources -}}
768
{{- else if eq .Values.size "medium" -}}
1536
{{- else if eq .Values.size "large" -}}
3072
{{- else -}}
768
{{- end -}}
{{- end -}}

{{- /*
   PgBouncer ini for the opt-in single-node pooler (postgres-pooler.yaml).
   Context: dict Values, authQuery, externalPooled, backendHost, backendPort.
   The ini is a named template so the ConfigMap body and the pod-template
   checksum below render from one source. pgbouncer.ini is a subPath mount,
   which kubelet never refreshes; without the checksum a config change
   (poolSize, host, TLS mode) updates the ConfigMap and nothing else, and the
   running PgBouncer keeps its old settings until an unrelated restart.
*/ -}}
{{- define "kusoaddon.poolerIni" -}}
[databases]
; * matches whatever dbname the client connects with, routed at the
; backend below — the addon's own StatefulSet Service, or the external
; provider's host when pooler.externalBackend is set.
* = host={{ .backendHost }} port={{ .backendPort }}

[pgbouncer]
listen_addr = 0.0.0.0
listen_port = 6432
; scram-sha-256, not md5: the addon's stock postgres:16 image
; defaults password_encryption=scram-sha-256, so the role's
; stored verifier is SCRAM. An md5 auth_type + md5 userlist
; can't authenticate to a SCRAM backend — pgbouncer rejects the
; backend login with "wrong password type". With auth_type
; scram-sha-256 and a plaintext-password userlist, pgbouncer
; runs SCRAM both to the client and to the backend.
auth_type = scram-sha-256
auth_file = /etc/pgbouncer/userlist.txt
{{- if .authQuery }}
; auth_query mode (shared cluster-DB pooler): the backend serves many
; per-project roles created/rotated dynamically as projects opt in, so a
; static userlist can't keep up. PgBouncer logs in as auth_user (the
; instance superuser, the only entry in userlist.txt) and looks up each
; client's SCRAM verifier from pg_shadow via auth_query — no pooler
; restart when a new per-project role appears.
; auth_user is the postgres chart's fixed superuser "kuso" (same
; literal the chart stamps into the conn secret + pg_isready probe);
; it's the sole entry rendered into userlist.txt below.
auth_user = kuso
auth_query = SELECT usename, passwd FROM pg_shadow WHERE usename=$1
{{- end }}
pool_mode = transaction
{{- /* Client side of the pooler. Each app pod holds a pgx/libpq pool of its
       own (25 is a common default), so 200 was exhausted by ~8 pods and the
       9th got "no more connections allowed" — a scaling wall unrelated to the
       backend's cap. Client connections are cheap (a few KB each, fd limit is
       1M); the backend pool (default_pool_size) is what actually protects the
       database. */}}
max_client_conn = 1000
{{- /* default_pool_size is backend connections per user/db. 25 suits the
       in-cluster chart (max_connections=100); a managed backend with a
       small cap needs pooler.poolSize, or the pooler itself exhausts the
       provider under load. The reserve pool is ON TOP of the default, so
       it scales down with it (poolSize/4, min 1) rather than pushing the
       total back over the cap. */}}
{{- $poolSize := int (default 25 .Values.pooler.poolSize) }}
default_pool_size = {{ $poolSize }}
reserve_pool_size = {{ if .Values.pooler.poolSize }}{{ max 1 (div $poolSize 4) }}{{ else }}5{{ end }}
{{- /* Global backend caps across every pool of one user / one database.
       The shared instance pooler holds one pool per (tenant role, db), so
       these bound how much of the server one tenant's pools can take. Only
       rendered when set: changing pgbouncer.ini rolls the pooler, which drops
       every tenant's connections, so an upgrade must not change it for
       existing installs. Tenant roles also carry a CONNECTION LIMIT on the
       server, which bounds pooled + DIRECT_URL connections together. */}}
{{- with .Values.pooler.maxUserConnections }}
max_user_connections = {{ int . }}
{{- end }}
{{- with .Values.pooler.maxDbConnections }}
max_db_connections = {{ int . }}
{{- end }}
reserve_pool_timeout = 3
query_wait_timeout = 30
server_idle_timeout = 600
log_connections = 0
log_disconnections = 0
log_pooler_errors = 1
; transaction pooling rejects any startup parameter it doesn't know, and
; pgx sends both timeouts on every connect — without these, every Go app
; dies with "unsupported startup parameter" the moment it's pooled. The
; values still apply; clients that care set them per session instead.
ignore_startup_parameters = extra_float_digits,search_path,statement_timeout,idle_in_transaction_session_timeout
admin_users = kuso
{{- if .externalPooled }}
; Managed providers (PlanetScale, Neon, RDS) refuse unencrypted backend
; logins. PgBouncer connects in plaintext unless told otherwise, so this
; is required, not a hardening extra. The in-cluster path leaves it unset:
; that backend serves plaintext by default (see spec.tls on the addon).
server_tls_sslmode = {{ default "require" .Values.pooler.serverTLSSslmode }}
{{- end }}
{{- end -}}

{{- /*
Backup CronJob guards, shared by every kind in backup-cronjob.yaml.

startingDeadlineSeconds: a CronJob that misses >100 schedules (long
suspend, control-plane outage) otherwise stops starting Jobs for good.
activeDeadlineSeconds: a dump wedged on a dead connection otherwise holds
the Forbid concurrency slot and silently skips every later night. 6h is
far beyond any dump seen here; it caps the whole Job, retries included.
resources: requests only. A memory limit could OOM-kill a dump that works
today (mongodump / large aws multipart uploads), so none is set.
scratch: the dump is staged under /tmp; an emptyDir keeps it out of the
container's writable layer and is freed with the pod. It sits on the
node's root disk, so it is capped (BLOB-7): a runaway dump gets its pod
evicted (a visible backup failure) instead of putting the node, and every
pod on it, under DiskPressure. The cap is backup.scratchSizeLimit, else
3x the addon's storage size (dump + its encrypted copy, or an RDB plus
its gzip, never reach that). External addons have no local size to go
by, so they stay uncapped unless backup.scratchSizeLimit is set.
*/ -}}
{{- define "kusoaddon.backupStartingDeadline" -}}
startingDeadlineSeconds: 3600
{{- end -}}

{{- define "kusoaddon.backupActiveDeadline" -}}
activeDeadlineSeconds: 21600
{{- end -}}

{{- define "kusoaddon.backupScratchVolume" -}}
{{- $limit := "" -}}
{{- $override := toString ((.Values.backup).scratchSizeLimit | default "") -}}
{{- if regexMatch "^[0-9]+(\\.[0-9]+)?(E|P|T|G|M|K|Ei|Pi|Ti|Gi|Mi|Ki)?$" $override -}}
{{- $limit = $override -}}
{{- else if not .Values.external -}}
{{- $q := include "kusoaddon.storageSize" . -}}
{{- $n := regexFind "^[0-9]+(\\.[0-9]+)?" $q | float64 -}}
{{- $limit = printf "%d%s" (mulf $n 3 | ceil | int) (regexReplaceAll "^[0-9]+(\\.[0-9]+)?" $q "") -}}
{{- end -}}
volumes:
  - name: scratch
    {{- if $limit }}
    emptyDir:
      sizeLimit: {{ $limit }}
    {{- else }}
    emptyDir: {}
    {{- end }}
{{- end -}}

{{- /*
kusoaddon.backupSealFn — shell function every backup script calls on a
staged artifact before upload. seal FILE sets SHA (digest of the
plaintext: the restore verifies what it applies), BYTES (stored size)
and ENC_JSON (the manifest's encryption entry, "" when unencrypted).

Encrypts in place (BLOB-8) when the addon's key Secret is mounted AND
the image has openssl; an older backup image without openssl uploads
plaintext with a warning rather than failing the backup. The format is
read by server-go/internal/backup/crypt.go and the restore Job's s3_fetch.
Explicit `|| return 1`: callers use `if ! seal`, which disables set -e
inside the function.
*/ -}}
{{- define "kusoaddon.backupSealFn" -}}
seal() {
  SHA=$(sha256sum "$1" | awk '{print $1}') || return 1
  ENC_JSON=""
  if [ -n "${BACKUP_ENCRYPTION_KEY:-}" ]; then
    if command -v openssl >/dev/null 2>&1; then
      openssl enc -aes-256-cbc -pbkdf2 -iter 100000 -md sha256 -salt -pass env:BACKUP_ENCRYPTION_KEY -in "$1" -out "$1.enc" || return 1
      mv "$1.enc" "$1" || return 1
      KEYID=$(printf 'kuso-backup-key:%s' "${BACKUP_ENCRYPTION_KEY}" | sha256sum | cut -c1-16)
      ENC_JSON=$(printf ',"encryption":{"alg":"openssl-aes-256-cbc-pbkdf2-sha256","keyId":"%s"}' "${KEYID}")
    else
      echo "WARNING: backup image has no openssl; uploading $1 unencrypted" >&2
    fi
  fi
  BYTES=$(wc -c < "$1" | tr -d ' ') || return 1
}
{{- end -}}

{{- /*
kusoaddon.backupKeySecretName — per-addon Secret with the backup
encryption key. Mirrored by backup.KeySecretName in server-go.
*/ -}}
{{- define "kusoaddon.s3RootSecretName" -}}
{{- printf "%s-root" (include "kusoaddon.fullname" .) -}}
{{- end -}}

{{- define "kusoaddon.s3ReadOnlySecretName" -}}
{{- printf "%s-readonly" (include "kusoaddon.fullname" .) -}}
{{- end -}}

{{- define "kusoaddon.backupKeySecretName" -}}
{{- printf "%s-backup-key" .Release.Name -}}
{{- end -}}

{{- /*
kusoaddon.backupS3SecretName — the backup storage Secret a CronJob reads.
kuso-backup-s3-<project> (project-scoped key, BLOB-4) when kuso-server has
rendered one into this namespace, else the shared kuso-backup-s3. Mirrored
by kube.ProjectBackupSecretName. lookup is empty under `helm template`,
which keeps the shared name.
*/ -}}
{{- define "kusoaddon.backupS3SecretName" -}}
{{- $scoped := printf "kuso-backup-s3-%s" (.Values.project | default "") -}}
{{- if and .Values.project (lookup "v1" "Secret" .Release.Namespace $scoped) -}}
{{- $scoped -}}
{{- else -}}
kuso-backup-s3
{{- end -}}
{{- end -}}

{{- /*
kusoaddon.backupExtraEnv — env shared by every backup CronJob:
  BACKUP_ENCRYPTION_KEY  optional: no Secret → plaintext, as before.
  OWNER_SINCE/OWNER_UID  this addon CR's creation stamp (minus 2 minutes of
                         clock skew) and UID, read with lookup. The prune
                         uses them to leave a deleted same-name
                         predecessor's dumps alone (BLOB-4). Empty when
                         lookup returns nothing (helm template), which
                         keeps the old prune behaviour.
*/ -}}
{{- define "kusoaddon.backupExtraEnv" -}}
{{- $cr := lookup "application.kuso.sislelabs.com/v1alpha1" "KusoAddon" .Release.Namespace .Release.Name -}}
{{- $since := "" -}}
{{- $uid := "" -}}
{{- if and $cr $cr.metadata -}}
{{- with $cr.metadata.creationTimestamp -}}
{{- $since = dateInZone "20060102150405" (toDate "2006-01-02T15:04:05Z07:00" . | dateModify "-2m") "UTC" -}}
{{- end -}}
{{- $uid = $cr.metadata.uid | default "" -}}
{{- end -}}
- { name: OWNER_SINCE, value: {{ $since | quote }} }
- { name: OWNER_UID,   value: {{ $uid | quote }} }
{{- if ne .Values.kind "s3" }}
- { name: BACKUP_ENCRYPTION_KEY, valueFrom: { secretKeyRef: { name: {{ include "kusoaddon.backupKeySecretName" . | quote }}, key: key, optional: true } } }
{{- end }}
{{- end -}}

{{- define "kusoaddon.backupContainerExtras" -}}
resources:
  requests:
    cpu: 50m
    memory: 128Mi
volumeMounts:
  - name: scratch
    mountPath: /tmp
{{- end -}}

{{/*
kusoaddon.image renders the main image ref, quoted. Call with
(list $ "<repo>:<tag>"). spec.imagePin (stamped by the server's addon
image updater) appends its digest only while pin.image equals this
exact ref, so changing spec.version drops a stale pin by itself.
*/}}
{{- define "kusoaddon.image" -}}
{{- $root := index . 0 -}}
{{- $ref := index . 1 -}}
{{- $pin := $root.Values.imagePin | default dict -}}
{{- if and (eq ($pin.image | default "") $ref) ($pin.digest | default "") -}}
{{- printf "%s@%s" $ref $pin.digest | quote -}}
{{- else -}}
{{- $ref | quote -}}
{{- end -}}
{{- end -}}
