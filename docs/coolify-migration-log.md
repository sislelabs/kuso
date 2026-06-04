# Coolify → kuso migration — running log

Autonomous migration session started 2026-06-04. User away; working through all
phases without check-ins. Domains excluded. Coolify untouched (read-only + dumps).

## Coolify inventory (authoritative)

- Host: `91.98.200.83` (coolify-main), shared PG container
  `c4sws4osgkcg08k8ckowsswg` (postgres:17-alpine), superuser `postgres`.
- 16 live apps; ~9 dead (skipped); monitoring stack skipped.
- 31 logical DBs on the shared PG (most 8-14MB, largest raffles 41MB). Many
  `*_dev`/`*_latest` are dupes for dead/dev apps.

## Logical DB → app mapping (to fill in during Phase 2)

| Coolify DB | App | Live? | kuso project_addon | Migrated |
|---|---|---|---|---|
| ticketeer | jira-mudira | yes | jiramudira_pg | |
| dbmaster | db-masterclass | yes | dbmasterclass_pg | |
| (others resolved per-app from each DATABASE_URL) | | | | |

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
