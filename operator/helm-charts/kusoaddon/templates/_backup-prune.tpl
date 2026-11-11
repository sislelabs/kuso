{{- /*
kusoaddon.backupPrune — retention prune shell shared by every backup CronJob.

Call with (dict "mode" "files") for kinds that write <ts>.<ext> objects, or
(dict "mode" "prefixes") for the s3 kind, which writes <ts>/ snapshot
prefixes. Every backup key starts with the stamp `date -u +%Y%m%dT%H%M%SZ`
produced, so age is compared on that stamp as a 14-digit integer. Nothing
parses a date string: the image's `date` is busybox, which rejected the
ISO form the old per-object parse used, so every object got obj_ts=0 and
was skipped while the Job still exited 0.

Fails safe: a failed or unparseable listing deletes nothing, names without
a leading stamp are never touched, and the newest KEEP_MIN objects of each
series survive even when they are all past retention.
*/ -}}
{{- define "kusoaddon.backupPrune" -}}
# retention-prune:begin
if [ -n "${RETENTION_DAYS:-}" ] && [ "${RETENTION_DAYS}" -gt 0 ]; then
  # `{ ... } || true`: the backup already uploaded by here, so a prune
  # problem must never fail the Job and fire a false backup-failed alert.
  # set -e does not apply inside it, hence the explicit checks.
  { KEEP_MIN=3
    CUTOFF=$(date -u -d "@$(( $(date +%s) - RETENTION_DAYS * 86400 ))" +%Y%m%d%H%M%S) || CUTOFF=""
    echo "==> pruning {{ if eq .mode "prefixes" }}snapshots{{ else }}dumps{{ end }} older than ${RETENTION_DAYS}d (cutoff=${CUTOFF}, keep newest ${KEEP_MIN})"
    case "${CUTOFF}" in
      [0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;;
      *) echo "    bad cutoff; not pruning"; CUTOFF="" ;;
    esac
    if [ -z "${CUTOFF}" ]; then
      :
    elif ! LISTING=$(aws s3 ls --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${PREFIX}"); then
      echo "    listing failed; not pruning"
    else
      {{- if eq .mode "prefixes" }}
      NAMES=$(printf '%s\n' "${LISTING}" | awk '$1 == "PRE" {print $2}')
      {{- else }}
      NAMES=$(printf '%s\n' "${LISTING}" | awk '$1 != "PRE" {print $4}')
      {{- end }}
      # A same-name predecessor's dumps share this prefix. Unless the owner
      # marker claims them for this addon, only stamps from this addon's
      # lifetime are prune candidates (and count toward KEEP_MIN).
      if [ -n "${OWNER_SINCE:-}" ] && ! aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${PREFIX}.kuso-owner.json" - 2>/dev/null \
          | grep -qF "\"addonUID\":\"${OWNER_UID}\""; then
        NAMES=$(printf '%s\n' "${NAMES}" | awk -v since="${OWNER_SINCE}" \
          'match($0, /^[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]Z/) { s = substr($0, 1, 8) substr($0, 10, 6); if (s >= since) print }')
      fi
      # KEEP_MIN is per series (the name after the stamp: ".sql.gz",
      # ".db-<tenant>.sql.gz", their manifests), not per run: a tenant
      # whose dumps failed for the last few runs must still keep its own
      # newest copies. "<stamp>|<series>|<name>", newest first per series;
      # anything past the KEEP_MIN newest of its series and older than
      # CUTOFF goes.
      {{- if .dbSeries }}
      # A ".db-<name>" series whose database is gone (a dropped preview or
      # env clone, seeded from production data) gets no new dumps, so
      # KEEP_MIN would hold its last copies forever. Once the run listed
      # the live databases (DBLIST_OK=1, ALL_DBS), such a series keeps
      # nothing past retention. An unknown listing changes nothing.
      {{- end }}
      printf '%s\n' "${NAMES}" \
        | sed -n 's/^\([0-9]\{8\}\)T\([0-9]\{6\}\)Z\(.*\)$/\1\2|\3|&/p' \
        | sort -t '|' -k2,2 -k1,1r \
        | awk -F '|' -v keep="${KEEP_MIN}" -v cutoff="${CUTOFF}" {{- if .dbSeries }} -v listed="${DBLIST_OK:-0}" -v live="$(printf '%s' "${ALL_DBS:-}" | tr '\n' ' ')"{{ end }} \
            {{- if .dbSeries }}
            'BEGIN { nl = split(live, l, " "); for (i = 1; i <= nl; i++) if (l[i] != "") alive[l[i]] = 1 }
             { if ($2 != prev) { n = 0; prev = $2; k = keep
                 if (listed == "1" && $2 ~ /^\.db-.+\.sql\.gz(\.manifest\.json)?$/) {
                   db = $2; sub(/^\.db-/, "", db); sub(/\.sql\.gz(\.manifest\.json)?$/, "", db)
                   if (!(db in alive)) k = 0
                 } }
               n++; if (n > k && ($1 + 0) < (cutoff + 0)) print $3 }' \
            {{- else }}
            '{ if ($2 != prev) { n = 0; prev = $2 } n++; if (n > keep && ($1 + 0) < (cutoff + 0)) print $3 }' \
            {{- end }}
        | while read -r name; do
          {{- if eq .mode "prefixes" }}
          echo "    rm -r s3://${BUCKET}/${PREFIX}${name}"
          aws s3 rm --endpoint-url "${S3_ENDPOINT}" --recursive "s3://${BUCKET}/${PREFIX}${name}" || true
          {{- else }}
          echo "    rm s3://${BUCKET}/${PREFIX}${name}"
          aws s3 rm --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${PREFIX}${name}" || true
          {{- end }}
      done
    fi ; } || true
fi
# retention-prune:end
{{- end -}}

{{- /*
kusoaddon.backupRetentionDays — RETENTION_DAYS for the prune step.
0 means "keep forever" (the prune skips), so it must survive: `default 14`
treats 0 as empty and silently re-enabled the 14-day prune. Only a missing
or null key falls back to 14.
*/ -}}
{{- define "kusoaddon.backupRetentionDays" -}}
{{- $b := .Values.backup | default dict -}}
{{- if and (hasKey $b "retentionDays") (not (kindIs "invalid" $b.retentionDays)) -}}
{{- $b.retentionDays | int -}}
{{- else -}}
14
{{- end -}}
{{- end -}}
