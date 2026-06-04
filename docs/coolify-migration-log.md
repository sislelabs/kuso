# Coolify → kuso migration — running log

Autonomous migration session started 2026-06-04. User away; working through all
phases without check-ins. Domains excluded. Coolify untouched (read-only + dumps).

## Coolify inventory (authoritative)

- Host: `91.98.200.83` (coolify-main), shared PG container
  `c4sws4osgkcg08k8ckowsswg` (postgres:17-alpine), superuser `postgres`.
- 16 live apps; ~9 dead (skipped); monitoring stack skipped.
- 31 logical DBs on the shared PG (most 8-14MB, largest raffles 41MB). Many
  `*_dev`/`*_latest` are dupes for dead/dev apps.

## Migration matrix (16 live apps)

Confirmed DATABASE_URL/REDIS_URL by sampling; remaining resolved at migrate
time. Coolify shared-PG password is the same superuser pw for all. kuso target
project names: slugified (no `kuso-` prefix; lowercase a-z0-9-). Per-project DB
on the cluster PG = `<project>_<addon>` (addon name "db" → `<project>_db`).

| App (Coolify) | runtime | repo / branch | Coolify DB | Redis? | kuso project |
|---|---|---|---|---|---|
| junior-accelerator-web | nixpacks | ja-web / main | ja-web | ? | ja-web |
| junior-accelerator-ship | nixpacks | …-ship / main | ja-ship | ? | ja-ship |
| kutiq | dockerfile | kutiq / main | kutiq | yes (redis-database-kutiq) | kutiq |
| jira-mudira | nixpacks | jira-mudira / main | ticketeer | no | jira-mudira |
| berivangold | nixpacks | berivangold / main | berivangold | ? | berivangold |
| berivangold (develop) | nixpacks | berivangold / develop | berivangold-develop | ? | berivangold-dev |
| bukvite30 | nixpacks | bukvite30 / main | bukvite | ? | bukvite30 |
| db-masterclass | nixpacks | db-masterclass / main | dbmaster | no | db-masterclass |
| s3-web | dockerfile | s3-web / main | (none?) | ? | s3-web |
| produktche | nixpacks | produktche / main | produktche | ? | produktche |
| ilikata | nixpacks | ilikata / main | ilikata | ? | ilikata |
| newsletterite | nixpacks | newsletterite / main | emailiz? | ? | newsletterite |
| boiler-code-landing | nixpacks | boiler-code-landing / main | boiler-code? | ? | boiler-code-landing |
| vibe-detector | nixpacks | vibe-detector / main | design-system? | ? | vibe-detector |
| s3 (compose) | dockercompose | s3 / main | s3 | — | Phase 3 |
| analiz (compose) | dockercompose | analiz / main | analize | — | Phase 3 |

Per-app env (incl. exact DATABASE_URL + all secrets) is fetched at migrate time
to avoid bulk secret exposure. Redis: kutiq confirmed; others TBD per-app —
Redis is usually cache, migrate data only if non-ephemeral (else fresh addon).
Coolify→kuso env rewrite rules:
- DATABASE_URL → kuso `${{ db.DATABASE_URL }}` (pooler-routed, set by kuso).
- Drop Coolify internal hosts (`pgbouncer-y0ok8…`, `n0koock…`).
- Reclassify build-time secrets as runtime.
- Flag/keep placeholder secrets (several apps have literal
  "generate-a-secret-key-here" / "change-in-production" — note but copy as-is
  unless they break boot; rotation is the user's call).

## Phase status

- [ ] Phase 0 — cluster-DB pooler (auth_query)  ← IN PROGRESS
- [ ] Phase 1 — prereqs / matrix
- [ ] Phase 2 — 14 nixpacks/dockerfile apps
- [ ] Phase 3 — compose apps (s3, analiz)

## Event log

- 2026-06-04: research complete; pooler design approved; Coolify host access
  confirmed; GitHub App confirmed on org. Starting Phase 0.
- 2026-06-04: **Phase 0 SHIPPED + VERIFIED (v0.18.18).** Shared auth_query
  PgBouncer in front of the cluster PG. E2E proven: throwaway project →
  cluster-DB addon → DATABASE_URL routes through `kuso-instance-pg-pooler:6432`
  → a client authenticated as the per-project user `pooltest_db` through the
  pooler (auth_query, no restart) and ran SELECT 1. Two bugs found+fixed during
  rollout: (a) operator image didn't auto-rebuild on chart change — forced with
  KUSO_RELEASE_OPERATOR=1; (b) non-HA POOLER_URL used sslmode=require against a
  plaintext pgbouncer → fixed to disable. Throwaway project cleaned up.

## Known follow-ups (non-blocking)

- **PVC-drift on cluster-DB disable→re-provision:** the postgres chart's PVC is
  `resource-policy=keep`, so `instancepg.Disable` (UI "Disable + delete") leaves
  `data-kuso-instance-pg-0` behind. Re-provision reuses the old data dir (old
  `kuso` role password) but writes a NEW password to the conn secret + admin
  DSN → admin DSN auth fails over the Service (loopback is trust, so it masks
  locally). Repaired live this session via `ALTER ROLE kuso WITH PASSWORD`.
  **Fix later:** `Disable` should delete the retained PVC(s) (it's gated on 0
  consumers and the button says "delete"), OR re-provision should RepairPassword
  on PVC reuse. Does NOT affect the migration (which never disables the cluster
  DB). Tracked for a follow-up TDD+ship.
