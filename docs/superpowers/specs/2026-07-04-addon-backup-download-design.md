# Direct addon backup download

**Date:** 2026-07-04
**Status:** approved

## Problem

The addon **Backups** tab only lists and restores S3-scheduled backups. When
the cluster-wide S3 backup config is missing, the tab shows a "Backups
unavailable: backup not configured: PUT /api/admin/backup-settings first"
banner and there is **no way to get the data out at all**.

We want a "dump it now, stream it to my browser/disk" button that works
independently of the S3-scheduled-backup config — it must work precisely when
that banner is showing.

## Scope

Supported addon kinds:

- **postgres** — in-process `pg_dump`, streamed as gzipped plain SQL.
- **s3 / minio** — list the bucket, stream every object into a single
  gzipped tar.

All other kinds (redis, clickhouse, redpanda) return `400 direct download not
supported for <kind> addons`. Their dump tooling differs and they are out of
scope for this change.

## Reference implementation

`server-go/internal/http/handlers/backup.go:116` (`Download`) already does the
Postgres case for the **control-plane metadata DB**: in-process `pg_dump` →
gzip → `http.ResponseWriter` with a `Content-Disposition` attachment header,
5-minute timeout with a `?timeout=` override, streamed so a 50 GB dump never
buffers in RAM. We copy that pattern, pointed at the addon's `<release>-conn`
Secret instead of `kuso-postgres-conn`.

The server image already bundles `postgresql16-client` (`server-go/Dockerfile:119`),
so `pg_dump`/`psql` are on `$PATH`. All live PG addons are v16, so the client
version matches.

## API

New route on `BackupsHandler` (`server-go/internal/http/handlers/backups.go`):

```
GET /api/projects/{project}/addons/{addon}/backups/download
```

- Role gate: **editor/admin** — same as `Restore`; it exfiltrates the whole
  database.
- Timeout: 5-minute default, `?timeout=` override capped at 1h (mirrors
  `backup.go`).
- Never touches the `kuso-backup-s3` Secret, so it works when S3 is
  unconfigured.
- Dispatches on the addon's `spec.kind` (read from the KusoAddon CR):

**postgres**
- Resolve a DSN from `<release>-conn` (a DSN-string sibling of the existing
  `pgConn` in `backups.go:504`, which returns a `*sql.DB`).
- `pg_dump --format=plain --no-owner --no-acl --clean --if-exists <dsn>` →
  gzip → body.
- `Content-Disposition: attachment; filename="<project>-<addon>-<ts>.sql.gz"`.

**s3 / minio**
- Creds from `<release>-conn`: `S3_ENDPOINT`, `S3_BUCKET`,
  `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`. The endpoint is in-cluster
  (`http://<release>-storage:9000`) and reachable from kuso-server.
- Build an aws-sdk-v2 S3 client (path-style, static creds — same shape as
  `s3Client` in `backups.go:436`), `ListObjectsV2` paginated, stream each
  object body into a `tar` writer wrapped in a `gzip` writer → body.
- `Content-Disposition: attachment; filename="<project>-<addon>-<ts>.tar.gz"`.

**other kinds** → `400`.

### Streaming failure semantics

Both paths stream; nothing is buffered whole in RAM. If the source fails
mid-stream (pg_dump dies, an S3 GET errors), the gzip is truncated and the
client errors on decompress — the same tradeoff the control-plane handler
already documents. Preferable to silently shipping a half-dump.

### Version skew (postgres)

Bundled client is `postgresql16-client`. `pg_dump` 16 dumps PG ≤16 fine but
refuses PG 17+ with "server version too new". All live PG addons are v16, so
this is not a current problem. Leave a code comment flagging it; a future
PG-17 addon would see a failed download rather than a corrupt one.

## Web UI

`web/src/components/addon/overlay/BackupsTab.tsx`:

- Add a **"Download backup now"** button in the tab header, shown for
  postgres + s3 addons (hidden for other kinds).
- Shown **even when the "Backups unavailable" banner is up** — that is the
  point.
- Because the request needs the JWT bearer, trigger the download via an
  authenticated `fetch` → `blob` → programmatic `<a download>` click, not a
  bare `href`.
- Show a "Preparing…" / spinner state while the stream runs; re-enable on
  completion or error.

Web API layer (`web/src/features/projects/api.ts`): a `downloadAddonBackup`
helper that fetches with the bearer and returns the blob + filename.

## CLI

- `cli/pkg/kusoApi/addon_backups.go`: `DownloadAddonBackup(project, addon)` —
  raw GET returning the bytes (mirrors the raw-GET used by `kuso backup`).
- `cli/cmd/kusoCli/addon_backup.go`: `kuso addon-backup download <project>
  <addon> [-o file]` — writes the streamed body to `-o` (or a default
  `<project>-<addon>-<ts>.{sql,tar}.gz` name inferred from the response
  `Content-Disposition`).

## Out of scope

- Redis / ClickHouse / Redpanda dumps.
- Restoring an addon from a locally-downloaded dump (restore already exists
  for S3-scheduled backups; local-file restore is a separate feature).
- Async/job-based download for very large datasets (synchronous streaming
  with a timeout is enough for now).
