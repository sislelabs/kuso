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
# Pinned to alpine:3.21 because alpine:3.20 packages aws-cli 2.15.57,
# which crashes inside its s3errormsg customisation when talking to
# any non-AWS S3 endpoint that returns an error format aws-cli's
# error-enhancer can't parse (Hetzner Object Storage is the case we
# hit). The fix landed upstream in aws-cli 2.18+; alpine:3.21 ships
# 2.22+, which works against Hetzner / Wasabi / B2 / MinIO without
# the spurious "argument of type NoneType is not iterable" abort.
#
# Built + pushed by `make backup-image`.
FROM alpine:3.21

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
