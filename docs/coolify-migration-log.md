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
