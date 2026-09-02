{{- define "kusoenvironment.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "kusoenvironment.labels" -}}
app.kubernetes.io/name: kusoenvironment
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
kuso.sislelabs.com/project: {{ .Values.project | default "unknown" }}
kuso.sislelabs.com/service: {{ .Values.service | default "unknown" }}
kuso.sislelabs.com/env-kind: {{ .Values.kind | default "production" }}
{{- end }}

{{- define "kusoenvironment.selectorLabels" -}}
app.kubernetes.io/name: kusoenvironment
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
kusoenvironment.waitForAddonsScript — body of the wait-for-addons initContainer.
Copied VERBATIM from server-go/internal/releaserun/releaserun.go
(waitForAddonsScript); TestWaitForAddonsScript_ChartMatchesGo fails the
build if the two drift. Edit the Go const, then paste here.
*/}}
{{- define "kusoenvironment.waitForAddonsScript" -}}
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
  while [ "$i" -lt 60 ]; do
    if nc -z -w 2 "$host" "$port" 2>/dev/null; then
      echo "wait-for-addons: $name reachable at $host:$port"
      return 0
    fi
    i=$((i+1))
    sleep 2
  done
  echo "wait-for-addons: $name at $host:$port not reachable after 120s" >&2
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
exit $rc
{{- end -}}
