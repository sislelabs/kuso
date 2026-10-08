{{- define "kusorun.fullname" -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "kusorun.labels" -}}
app.kubernetes.io/name: kusorun
app.kubernetes.io/instance: {{ .Release.Name | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/component: kusorun
kuso.sislelabs.com/project: {{ .Values.project | default "unknown" | quote }}
kuso.sislelabs.com/service: {{ .Values.service | default "unknown" | quote }}
kuso.sislelabs.com/run: {{ .Release.Name | quote }}
{{- end }}

{{/*
kusorun.waitForAddonsScript — body of the wait-for-addons initContainer.
Copied VERBATIM from server-go/internal/releaserun/releaserun.go
(waitForAddonsScript); TestWaitForAddonsScript_ChartMatchesGo fails the
build if the two drift. Edit the Go const, then paste here.
*/}}
{{- define "kusorun.waitForAddonsScript" -}}
set -u
# host_port_from_url <url> -> prints "host port" parsed from a
# scheme://[user[:pass]]@host[:port]/... connection string, or nothing.
host_port_from_url() {
  u=$1
  # NATS HA seeds are comma-separated (nats://a@h1,nats://a@h2); keep
  # only the first seed so the rest of the parse sees one authority.
  first=$(printf '%s' "$u" | sed -e 's#,.*$##')
  # drop the scheme.
  rest=$(printf '%s' "$first" | sed -e 's#^[a-zA-Z][a-zA-Z0-9+.-]*://##')
  # authority is everything before the first / or ? (the path/query).
  authority=$(printf '%s' "$rest" | sed -e 's#[/?].*$##')
  # strip userinfo up to the LAST @ (passwords can contain @, e.g.
  # postgres://user:p@ss@host:5432) so we keep only host[:port].
  case "$authority" in
    *@*) hostport=$(printf '%s' "$authority" | sed -e 's#^.*@##') ;;
    *)   hostport=$authority ;;
  esac
  host=$(printf '%s' "$hostport" | sed -e 's#:.*$##')
  port=$(printf '%s' "$hostport" | sed -n 's#^[^:]*:\([0-9][0-9]*\).*$#\1#p')
  [ -z "$host" ] && return 0
  printf '%s %s' "$host" "$port"
}

wait_one() {
  name=$1; host=$2; port=$3
  [ -z "$host" ] && return 0
  [ -z "$port" ] && port=$4
  echo "wait-for-addons: waiting for $name at $host:$port"
  i=0
  attempts="${WAIT_FOR_ADDONS_ATTEMPTS:-60}"
  while [ "$i" -lt "$attempts" ]; do
    if nc -z -w 2 "$host" "$port" 2>/dev/null; then
      echo "wait-for-addons: $name reachable at $host:$port"
      return 0
    fi
    i=$((i+1))
    sleep 2
  done
  echo "wait-for-addons: $name at $host:$port not reachable after $((attempts*2))s" >&2
  return 1
}

rc=0
if [ -n "${DATABASE_URL:-}" ]; then
  set -- $(host_port_from_url "$DATABASE_URL"); wait_one postgres "${1:-}" "${2:-}" 5432 || rc=1
fi
if [ -n "${REDIS_URL:-}" ]; then
  set -- $(host_port_from_url "$REDIS_URL"); wait_one redis "${1:-}" "${2:-}" 6379 || rc=1
fi
if [ -n "${NATS_URL:-}" ]; then
  set -- $(host_port_from_url "$NATS_URL"); wait_one nats "${1:-}" "${2:-}" 4222 || rc=1
fi
# Strict (default): a present addon that never answers is a failure — the
# release Job runs with backoffLimit 0 and wants a clear verdict, not a
# migration attempted against nothing. Soft (WAIT_FOR_ADDONS_SOFT=1): app
# pods. Some apps hold a DATABASE_URL/REDIS_URL they only touch lazily, or
# one that is plain wrong yet has never stopped them running; turning that
# into a hard block would fail a rollout that used to succeed. Warn loudly
# and hand over to the app, which knows whether it can live without it.
if [ "$rc" -ne 0 ] && [ "${WAIT_FOR_ADDONS_SOFT:-}" = "1" ]; then
  echo "wait-for-addons: not every addon answered — starting the app anyway (WAIT_FOR_ADDONS_SOFT=1)" >&2
  exit 0
fi
exit $rc
{{- end -}}

{{/*
kusorun.envBlock — the env + envFrom of the run container, shared with the
wait-for-addons init so both resolve the same DATABASE_URL etc. Pass
(dict "Values" .Values "waitEnv" true) to prepend the init's WAIT_FOR_ADDONS_* knobs.
*/}}
{{- define "kusorun.envBlock" -}}
{{- if or .waitEnv .Values.env }}
env:
{{- if .waitEnv }}
  - name: WAIT_FOR_ADDONS_SOFT
    value: "1"
{{- end }}
{{- range .Values.env }}
  - name: {{ .name | quote }}
    {{- if .valueFrom }}
    {{- /* A ${{ }} alias (secretKeyRef/configMapKeyRef). Resolves
           against the same Secrets mounted via envFromSecrets, so a
           run sees the service's aliased env (e.g. DATABASE_URI). */}}
    valueFrom:
      {{- toYaml .valueFrom | nindent 6 }}
    {{- else }}
    value: {{ .value | quote }}
    {{- end }}
{{- end }}
{{- end }}
{{- with .Values.envFromSecrets }}
envFrom:
  {{- range . }}
  # optional=true mirrors the kusoenvironment chart's
  # rendering — a missing secret (e.g. an addon that's been
  # uninstalled while a run reference still carries its
  # conn-secret name) shouldn't pin the pod in
  # CreateContainerConfigError. The run still starts and
  # the missing-env-var hint surfaces in the user's code.
  - secretRef:
      name: {{ . | quote }}
      optional: true
  {{- end }}
{{- end }}
{{- end -}}
