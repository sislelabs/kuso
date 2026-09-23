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
a leading stamp are never touched, and the newest KEEP_MIN distinct stamps
survive even when they are all past retention.
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
      # Oldest stamp among the newest KEEP_MIN; nothing at or after it goes.
      KEEP_FROM=$(printf '%s\n' "${NAMES}" \
        | sed -n 's/^\([0-9]\{8\}\)T\([0-9]\{6\}\)Z.*/\1\2/p' \
        | sort -u | tail -n "${KEEP_MIN}" | head -n 1)
      printf '%s\n' "${NAMES}" | while read -r name; do
        stamp=$(printf '%s\n' "${name}" | sed -n 's/^\([0-9]\{8\}\)T\([0-9]\{6\}\)Z.*/\1\2/p')
        [ -z "${stamp}" ] && continue
        if [ "${stamp}" -lt "${CUTOFF}" ] && [ "${stamp}" -lt "${KEEP_FROM}" ]; then
          {{- if eq .mode "prefixes" }}
          echo "    rm -r s3://${BUCKET}/${PREFIX}${name}"
          aws s3 rm --endpoint-url "${S3_ENDPOINT}" --recursive "s3://${BUCKET}/${PREFIX}${name}" || true
          {{- else }}
          echo "    rm s3://${BUCKET}/${PREFIX}${name}"
          aws s3 rm --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${PREFIX}${name}" || true
          {{- end }}
        fi
      done
    fi ; } || true
fi
# retention-prune:end
{{- end -}}
