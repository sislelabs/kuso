# Openship-inspired improvements — design

**Date:** 2026-07-21
**Status:** approved-pending-review
**Author:** ivo (with Claude)

## Background

A code-level comparison of [Openship](https://github.com/oblien/openship) (a Docker/bare
self-hosted PaaS) against kuso surfaced a handful of ideas worth adopting — filtered hard
against kuso's scope guardrails (single-tenant; WAF/mail/CDN/multi-region explicitly out of
scope). The rejected ideas (multi-tenancy, integrated mail, CDN/HTTP-3, an app-template
marketplace kuso already has) are **not** in scope for this work.

What survived the filter, and what an audit of kuso confirmed as real gaps or weaknesses:

| # | Feature | kuso today (audited) |
| - | ------- | -------------------- |
| 1 | `kuso api` raw passthrough | **Lacks** — every endpoint needs a typed cobra command |
| 2 | Backup manifest + sha256 verify | **Lacks** — restore streams `s3 cp \| gunzip \| psql` with no integrity check |
| 3 | Backup producer registry + volume producer | **Partial** — postgres+redis only, hardcoded; clickhouse/redpanda un-dumpable |
| 4 | Pre-deploy datastore snapshot | **Lacks** — has a release-hook *gate*, but no snapshot before it |
| 5 | mysql + mongo as addon kinds (+ their producers) | **Lacks** — addon kinds are postgres/redis/clickhouse/redpanda |

This is **five decomposed pieces**, delivered in dependency order. Each piece gets its own
implementation plan and lands independently; this document is the shared roadmap and the
per-piece design.

## Guardrails honored

- **Single-tenant.** Nothing here adds orgs/teams/multi-tenancy.
- **No WAF, no mail, no CDN.** None of the rejected Openship features appear here.
- **The CLI is the contract.** `kuso api` reinforces this rather than working around it.
- **Backups stay S3-only.** We do not add SFTP/local destinations (Openship has them; kuso's
  S3-only stance is a deliberate scope choice).

## Dependency order

```
1. kuso api            (independent — no deps)
2. backup manifest     (independent — extends existing postgres+redis→S3 path)
3. producer registry   (depends on 2's manifest format) + volume producer
4. pre-deploy snapshot (depends on 3's registry; wires into release-hook seam)
5. mysql + mongo kinds (depends on 3's registry so producers just plug in)
```

Rationale: each step builds on a verified prior step, and the largest/riskiest piece
(mysql+mongo as new CRD-backed addon kinds) lands last, on top of a proven registry, rather
than everything arriving at once.

---

## Piece 1 — `kuso api` raw passthrough

### Purpose
A `gh api`-style escape hatch: hit any `/api/...` endpoint from the CLI without a dedicated
command. Keeps the CLI small and makes every new server endpoint reachable the moment it ships.

### Surface
```
kuso api <METHOD> <path> [flags]

  <METHOD>   GET | POST | PUT | PATCH | DELETE (case-insensitive; required — no default)
  <path>     /api/... (leading slash optional; "projects" → "/api/projects")

Flags:
  --data, -d <json>     raw JSON request body (string, or @file.json to read from disk)
  -f <key=value>        typed field; repeatable; assembled into a JSON object body
                        (numbers/true/false/null coerced; key=@file reads a file value)
  -F <key=value>        like -f but always string-valued (no coercion)
  --header, -H <k:v>    extra request header; repeatable
  --jq <expr>           filter the JSON response through a jq expression (uses gojq, vendored)
  --include, -i         print response status line + headers before the body
  -X <METHOD>           alternative to positional METHOD (gh-compatible)
```

### Behavior
- Reuses the existing `KusoClient` (`cli/pkg/kusoApi/main.go`) — same bearer token from
  `~/.kuso/credentials.yaml`, same base URL / localhost Host-header handling. Auth is free.
- `-f`/`-F` and `--data` are mutually exclusive (error if both). `-f` values coerce:
  `count=3` → number, `enabled=true` → bool, `name=foo` → string, `x=@f.json` → file contents.
- Response: pretty-print JSON bodies by default; pass non-JSON through raw. `--jq` filters
  JSON (error if body isn't JSON).
- Exit code: non-2xx → non-zero exit (so it scripts cleanly), body still printed to stdout,
  a one-line `HTTP <status>` note to stderr.

### Components
- `cli/cmd/kusoCli/api.go` — new cobra command (positional METHOD + path, flags above).
- `cli/pkg/kusoApi/raw.go` — one new method `Raw(method, path string, body []byte, headers map[string]string) (*resty.Response, error)` on `KusoClient`, reusing `k.client`.
- `--jq` needs a JSON query lib. Use `github.com/itchyny/gojq` (pure-Go, no cgo). If pulling a
  new dep is undesirable, `--jq` is the one droppable sub-feature — mark it optional in the plan.

### Testing
- Unit: field-coercion (`-f count=3` → `{"count":3}`), `--data @file`, method validation,
  mutually-exclusive-flags error.
- Integration (against a test server or httptest): GET returns body; non-2xx → non-zero exit;
  `--jq '.items[].name'` filters.

### Out of scope
No pagination auto-follow, no `--paginate`, no templating. It's a thin passthrough.

---

## Piece 2 — Backup manifest + sha256 verify

### Purpose
Close a **correctness** gap: today a truncated/corrupt S3 upload restores silently. Add a
`manifest.json` written alongside every backup artifact, and verify each artifact's sha256 on
restore before applying — aborting on mismatch.

### Manifest format (`manifest.json`, stored next to the artifact in S3)
```json
{
  "schemaVersion": 1,
  "createdAt": "2026-07-21T12:00:00Z",
  "project": "acme",
  "addon": "db",
  "addonKind": "postgres",
  "producer": "pg_dump",
  "artifacts": [
    { "key": "acme/db/2026-07-21T12-00-00Z/dump.sql.gz", "sha256": "…", "bytes": 12345, "payloadKind": "pg_dump" }
  ]
}
```
- One manifest per backup run. `artifacts[]` is a list to accommodate multi-artifact producers
  later (e.g. a volume producer that splits, or an addon with multiple DBs). Postgres/redis
  today emit exactly one artifact.
- **No secret values** ever recorded in the manifest (mirrors kuso's existing "secrets stay in
  encrypted columns" rule). Env-var *keys* only, if we record any (we don't need to for v1).

### Where sha256 is computed
The backup runs inside a Job (`kuso-backup` image) as `pg_dump | gzip | aws s3 cp`. To hash
without buffering the whole dump:
- Pipe through `tee >(sha256sum > /tmp/sum)` before `aws s3 cp`, OR compute the hash on a
  second streamed pass, OR (cleanest) have the Job script write the artifact to a temp file,
  `sha256sum` it, then `s3 cp` both the artifact and the generated `manifest.json`.
- **Decision:** temp-file approach in the Job script. Dumps are already bounded by disk on the
  backup pod; simplicity + correctness beats streaming cleverness here. The plan will confirm
  the `kuso-backup` image has room / an emptyDir for the temp file.

### Restore verification
The restore Job (`backups.go` ~448) currently does `s3 cp | gunzip | psql`. New flow:
1. `s3 cp` the artifact **and** its `manifest.json` to temp.
2. `sha256sum -c` the artifact against the manifest entry. **Abort (non-zero exit) on mismatch**
   before touching the target DB.
3. Then `gunzip | psql` as today.

### Backward compatibility
Backups taken before this change have no manifest. Restore must handle a missing manifest:
warn ("no manifest — integrity not verified") and proceed, rather than fail. New backups always
get one. A `--require-manifest` flag (default off) can force strict mode later.

### Components
- `kuso-backup` image script (backup path): write temp → sha256 → upload artifact + manifest.
- `server-go/internal/http/handlers/backups.go`: restore Job script gains download-manifest +
  verify step; List surfaces manifest presence.
- Helm `kusoaddon/templates/backup-cronjob.yaml`: the scheduled backup path emits a manifest too
  (same script logic).
- A small Go helper for manifest marshal/unmarshal in a new `server-go/internal/backup/` package
  (the same package Piece 3's registry lives in), shared between the handler and the registry.

### Testing
- Unit: manifest marshal/unmarshal, schema version handling.
- Integration: round-trip a real dump → manifest sha256 matches; corrupt the artifact → restore
  aborts; missing manifest → restore proceeds with warning.

---

## Piece 3 — Backup producer registry + volume producer

### Purpose
Refactor the currently-hardcoded producer logic (postgres pg_dump, redis rdb) into a small
registry keyed by `payloadKind`, so new producers plug in. Ship the **volume** producer (tar +
zstd of the addon PVC) — the universal fallback that makes clickhouse/redpanda (currently
un-dumpable) backable. This is the seam mysql/mongo (Piece 5) plug into.

### Model (server-side, Go)
```go
type Producer interface {
    // Kind is the payloadKind string recorded in the manifest.
    Kind() string
    // Detects reports whether this producer handles the given addon.
    Detects(addon AddonRef) bool
    // BackupScript / RestoreScript return the shell fragments the Job runs,
    // parameterized by the addon's -conn Secret env.
    BackupScript(a AddonRef) string
    RestoreScript(a AddonRef) string
}
```
- A registry resolves `producerFor(addon)` by walking registered producers in priority order —
  **specific kinds first** (postgres, redis, mysql, mongo), **volume last** as the catch-all.
  This mirrors Openship's ordering (DB producers win auto-detect before the volume fallback).
- Producers emit shell fragments (not Go that talks to the DB) because the actual work runs in
  the `kuso-backup` Job pod against the addon's `-conn` Secret — consistent with today's design.

### Volume producer
- Backup: `tar -C /data -cf - . | zstd -c -3 > dump.tar.zst` where `/data` is the addon PVC
  mounted read-only into the Job (or a VolumeSnapshot-backed clone if the storageclass supports
  it — the plan evaluates; default is a direct RO mount with a note that the addon should ideally
  be quiesced/paused for a consistent volume backup).
- Restore: stop addon → replace PVC contents from `zstd -d | tar -x` → start addon. This is the
  first restore path that must **stop/start the addon**, so it needs the two-phase treatment
  (see Cross-cutting).
- Manifest `payloadKind: "volume"`, one artifact.

### Consistency caveat (documented, not hidden)
A live-volume tar of a running database is crash-consistent at best. The volume producer is the
right tool for clickhouse/redpanda and generic PVCs; for postgres/redis the native dump
producers remain the default (registry priority ensures this). This tradeoff goes in the addon
backup docs.

### Components
- `server-go/internal/backup/` (new package, or a `producers/` subtree under an existing backup
  location): registry + Producer implementations (postgres, redis extracted from current
  hardcode; volume new).
- Wire `backups.go` handler + `backup-cronjob.yaml` to resolve scripts via the registry instead
  of inline `{{ if eq .kind "postgres" }}`.
- Manifest from Piece 2 gains `volume` as a valid payloadKind.

### Testing
- Unit: registry resolution order (postgres addon → pg_dump not volume; clickhouse addon →
  volume); script generation snapshot tests.
- Integration: volume backup+restore round-trip against a throwaway addon PVC; verify manifest.

---

## Piece 4 — Pre-deploy postgres snapshot

### Purpose
Auto-snapshot subscribed **postgres** addons immediately before the release-hook (migration) Job
runs, so a migration that succeeds-but-corrupts (or a bad deploy) has a one-click undo. Directly
targets kuso's most incident-prone surface (migration failures).

### Trigger & flow (chosen option: "before release-hook, auto-restore *prompt* on fail")
Hook point: `builds.go`, the `if shouldRunRelease(&e) && p.ReleaseRunner != nil` block
(~line 2657), **before** `p.ReleaseRunner.Run(...)`.

```
promote loop, per env:
  if shouldRunRelease(env):
    if snapshotBeforeDeploy enabled for this service/env:
       snapshotIDs = snapshot each subscribed postgres addon   ← NEW
       record snapshotIDs on the build (annotation) + notify
    res = ReleaseRunner.Run(...)         ← existing release-hook gate
    if res != succeeded:
       markReleaseFailed(...)            ← existing
       # NEW: surface a "restore to pre-deploy snapshot" action referencing snapshotIDs
       #      (does NOT auto-restore — user confirms)
       continue
  promoteEnvImageCAS(...)                ← existing
```

### Scope of the snapshot
- **Postgres only** (chosen). Targets = project addons where `kind == postgres` AND the service
  subscribes to them (via the existing `envFromSecrets` / sharedEnvKeys subscription the audit
  described). Non-postgres subscribed addons are ignored (logged).
- Reuses the Piece-3 registry's postgres producer → the snapshot is a normal manifested backup
  with a distinguishing marker (`trigger: "pre_deploy"`, `buildRef: <build>`), so it shows in the
  existing backup list and restores through the existing (Piece-2-verified) restore path.

### Enablement
- New field: `KusoService.spec.snapshotBeforeDeploy` (bool, default **false**). Only services
  that opt in pay the snapshot cost. Because release-hooks are the risk surface, the snapshot
  only fires when a release-hook is also present (`shouldRunRelease` true) — even if the flag is
  on, no hook = no migration = no snapshot.
- Must be added to **both** `propagate.go` and the AddService env literal (per the
  `[[addservice-env-literal-drops-fields]]` memory — new service-spec fields silently drop
  otherwise).

### Failure semantics
- Snapshot fails (infra error) → **block the deploy** (treat like a release infra error:
  `releaseBlocked = true`, skip promote, retry next tick). Rationale: if we promised a safety net
  and couldn't take it, don't proceed into the risky migration.
- Release-hook fails after a good snapshot → existing `markReleaseFailed` + a surfaced restore
  action (build annotation + notify event carrying the snapshot key). No auto-restore.

### Components
- `server-go/internal/kube/types.go`: `snapshotBeforeDeploy` on KusoService spec (+ CRD YAML +
  helm chart value passthrough).
- `server-go/internal/builds/builds.go`: the snapshot call before `ReleaseRunner.Run`, snapshot-ID
  recording, restore-action surfacing.
- A `Snapshotter` seam (interface, like `ReleaseRunner`) so builds.go stays testable and the
  actual backup Job creation lives in the backup package.
- Notify event: `deploy.snapshot.taken` / restore-available on `release.failed`.
- Web + CLI: show the pre-deploy snapshot on the build/deployment view with a restore button
  (reuses existing restore endpoint).

### Testing
- Unit (builds.go with a fake Snapshotter + ReleaseRunner): snapshot precedes hook; snapshot
  infra-fail blocks promote; hook-fail surfaces restore action with the snapshot key; flag off →
  no snapshot; no release-hook → no snapshot even with flag on.
- Integration: opt-in service with a failing migration → snapshot exists, image not promoted,
  restore action present, restore returns DB to pre-deploy state.

---

## Piece 5 — mysql + mongo as addon kinds (+ producers)

### Purpose
Add MySQL and MongoDB as first-class kuso addon kinds, following the established 11-step
"add a CRD-backed feature" pattern. Their backup producers plug into the Piece-3 registry.
This is the largest piece and is deliberately last.

### Scope
For **each** of mysql, mongo:
1. Helm chart under `operator/helm-charts/kusoaddon/` — extend the existing addon chart with
   `kind: mysql` / `kind: mongo` StatefulSet + Service + `<release>-conn` Secret templates
   (mirroring postgres/redis). Includes the `helm.sh/resource-policy: keep` on the conn Secret +
   PVC per `[[addon-conn-secret-must-keep-with-pvc]]`.
2. `-conn` Secret keys: mysql → `MYSQL_*` + `DATABASE_URL`; mongo → `MONGO_*` + `DATABASE_URL`,
   consumable via the existing `${{ <addon>.KEY }}` env rewriting.
3. Size → resources mapping via the existing `kusoaddon.resources` helper
   (per `[[addon-size-never-maps-to-resources]]`).
4. Backup producer (registry, Piece 3): mysql → `mysqldump`; mongo → `mongodump` (archive+gzip),
   both manifested + sha256-verified. Restore scripts symmetric.
5. Go types / GVR getters (`kube/types.go`, `crds.go`) — kind enum extended.
6. Validation: addon-kind allowlist updated across server + CLI + web.
7. CLI: `kuso get addons` / addon creation already generic over kind — extend kind validation +
   any kind-specific help. DB browser (`kuso db …`) support is **out of scope for v1** (postgres
   SQL browser stays postgres-only; mysql/mongo browsing is a later item).
8. Web: addon-create dialog kind picker gains mysql/mongo; conn-info display.
9. Apply the extended CRD/chart to the live cluster (ssh kubectl apply) — the auto-updater only
   flips image tags, not schemas.

### Explicitly deferred (YAGNI for v1)
- HA for mysql/mongo (postgres HA uses CNPG; mysql/mongo HA is a separate large effort).
- DB browser (`kuso db sql/tables`) for mysql/mongo.
- previewdb clone support for mysql/mongo.

These are noted so the plan doesn't silently imply parity with postgres.

### Testing
- Chart render tests (helm template) for both kinds: StatefulSet, Service, conn Secret, resources,
  keep-policy.
- Producer round-trip (backup+restore+manifest verify) for each kind against a throwaway addon.
- End-to-end via the CLI (the contract): create addon → subscribe a service → env rewrite
  resolves `${{ <addon>.DATABASE_URL }}` → backup → restore.

---

## Cross-cutting concerns

### Two-phase, stop/start restores
Piece 3 (volume) and Piece 5 (mysql/mongo, if a producer needs the addon stopped) introduce
restores that must stop the addon, replace data, and restart. The restore orchestration should:
- transition through explicit phases (download → verify → stop → apply → start), each logged;
- on failure mid-apply, leave the addon stopped with a clear error rather than half-restored;
- be safe to retry.
Postgres/redis logical restores (psql/redis-cli) don't need stop/start and keep their current
single-phase path. Verification (Piece 2) applies to **all** restores.

### `kuso-backup` image
Pieces 2, 3, 5 all evolve the `kuso-backup` container's script surface. Keep the per-kind logic
in the registry-generated script fragments; the image just needs the tooling installed
(`pg_dump`, `redis-cli`, `mysqldump`, `mongodump`, `zstd`, `tar`, `sha256sum`, `aws`). The plan
must bump the image + confirm tools present.

### Release flow
- CRD schema changes (Pieces 4 spec field, 5 new kinds) require an explicit `kubectl apply` of the
  CRD YAML on the live cluster in addition to `make ship` — the auto-updater only flips image
  tags. Call this out in each affected plan.
- CLI changes (Piece 1, and CLI surfaces of 4/5) require the `cd cli && go build` rebuild noted in
  CLAUDE.md, and `dist/kuso-darwin-arm64` refresh for local e2e.

### Docs
- `docs/EDIT_SAFETY.md`: add `snapshotBeforeDeploy` (live-editable, no restart) and the new addon
  kinds' field contracts.
- Addon backup docs: document the volume producer's crash-consistency caveat and the
  manifest/verify guarantee.

## Success criteria

1. `kuso api GET /api/projects` returns the same JSON the typed command would, authenticated.
2. A backup taken post-change carries a `manifest.json`; a corrupted artifact makes restore abort
   before touching the target.
3. A clickhouse/redpanda addon can be backed up and restored via the volume producer.
4. An opt-in service with a failing migration keeps its old image AND exposes a working
   restore-to-pre-deploy-snapshot action; a passing migration promotes as before.
5. A mysql and a mongo addon can be created, subscribed, backed up, and restored through the CLI.

## Non-goals (restated)

Multi-tenancy, mail, WAF, CDN/HTTP-3, SFTP/local backup destinations, mysql/mongo HA, mysql/mongo
DB-browser, an app-template marketplace expansion. None are in scope.
