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
# postgresql{16,17,18}-client: we install SEVERAL, deliberately.
#
# pg_dump refuses a server NEWER than itself ("aborting because of
# server version mismatch"), so the client has to cover the newest
# Postgres an addon can point at — external managed providers
# (PlanetScale ships 18) as well as the in-cluster charts.
#
# But "a newer pg_dump dumps older servers fine" is only half true, and
# the missing half cost us every Postgres restore on the cluster: an 18
# client emits `SET transaction_timeout = 0` at line 13 of the dump, a
# parameter that does not exist before 17. Piped back into a PG 16
# server under `psql -v ON_ERROR_STOP=1 --single-transaction`, the
# restore aborts on line 13 before creating a single table. The dumps
# looked perfect — right size, checksum verified — and were
# unrestorable. Restore rehearsals had been failing for 19 days.
#
# So the version matters in BOTH directions, and neither a floor nor a
# ceiling alone is right. backup-cronjob.yaml reads the target's
# server_version_num at runtime and calls the matching client out of
# /usr/libexec/postgresql<major>/, falling back to the newest installed
# one for a server newer than anything we ship (which dumps fine; it is
# only the restore direction that breaks).
#
# alpine:3.23 packages 16, 17 and 18 side by side.
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
FROM alpine:3.23

RUN apk add --no-cache \
      bash \
      ca-certificates \
      aws-cli \
      rclone \
      postgresql16-client \
      postgresql17-client \
      postgresql18-client \
      redis \
      mongodb-tools \
      mysql-client \
      gzip
# rclone      -> cross-endpoint bucket sync for the s3 addon backup.
#                `aws s3 sync` can only do s3->s3 within ONE endpoint and
#                credential set, so the old code staged the whole bucket
#                on local disk and re-uploaded. That broke on any object
#                whose key exceeds the filesystem NAME_MAX of 255 bytes:
#                a real 252-byte Cyrillic PDF name plus aws's 9-byte temp
#                suffix came to 261 and killed the backup with
#                "[Errno 36] Filename too long" after ~460MB. rclone
#                streams between two independently-configured remotes, so
#                nothing touches a filesystem and a 1024-byte S3 key is
#                fine. It also parallelises properly -- the interim
#                per-object `aws s3 cp` loop needed two CLI startups per
#                object, far too slow for a 3168-object bucket.
# redis        -> redis-cli, needed by the redis backup CronJob branch
#                (it was referenced but missing from this image before).
# mongodb-tools-> mongodump / mongorestore for the mongodb producer.
# mysql-client -> mysqldump / mysql for the mysql producer (alpine's
#                mysql-client is the mariadb-client, wire-compatible).

# Run as non-root so a compromised CronJob can't touch the host.
RUN adduser -D -u 1001 kuso
USER 1001

WORKDIR /home/kuso
ENTRYPOINT ["/bin/bash", "-c"]
