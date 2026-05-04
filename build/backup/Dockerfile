# kuso-backup — the runtime image used by the per-addon backup
# CronJob (operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml).
#
# Contents are intentionally minimal:
#   - aws-cli for S3 transfers (works with any S3-compatible store
#     when --endpoint-url is set; we read it from kuso-backup-s3 Secret)
#   - postgresql-client for pg_dump / pg_restore
#   - mariadb-client + mongodb-tools deferred until v2 — postgres
#     covers the addon kinds users actually self-host today
#
# Built + pushed by `make backup-image`.
FROM alpine:3.20

RUN apk add --no-cache \
      bash \
      ca-certificates \
      aws-cli \
      postgresql16-client \
      gzip

# Run as non-root so a compromised CronJob can't touch the host.
RUN adduser -D -u 1001 kuso
USER 1001

WORKDIR /home/kuso
ENTRYPOINT ["/bin/bash", "-c"]
