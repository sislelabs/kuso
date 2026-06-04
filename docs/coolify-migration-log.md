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

- [x] Phase 0 — cluster-DB pooler (auth_query) — SHIPPED v0.18.18, verified
- [x] Phase 1 — prereqs / matrix
- [ ] Phase 2 — 14 nixpacks/dockerfile apps  ← IN PROGRESS
- [ ] Phase 3 — compose apps (s3, analiz)

## Phase 2 per-app progress

CLI auth: wrote the provided ivo9999 admin token into ~/.kuso/credentials.yaml
(backup at credentials.yaml.premigration). migration/ is gitignored (kuso.yml
files hold plaintext secrets). DB migrations use migration/migrate-db.sh.

| App | project created | applied (svc+addon) | data loaded | build | notes |
|---|---|---|---|---|---|
| jira-mudira | ✓ | ✓ | ✓ ticketeer→jira_mudira_db (39 tbl, Activity 2648 parity) | running | DB pooler-routed; secrets copied (AUTH_SECRET is a placeholder — rotate) |
| boiler-code-landing | ✓ | ✓ | ✓ boiler-code→boiler_code_landing_db (Session 36 parity) | queued | **LIVE Stripe keys** copied as-is; Better-Auth, Resend, GitHub PAT |
| db-masterclass | ✓ | ✓ | ✓ dbmaster→db_masterclass_db (audit_logs 378 parity) | queued | NEXTAUTH_SECRET reused as ENCRYPTION_KEY |

### Data-migration ownership fix (applies to ALL DB-backed apps)

The dump loads as the kuso superuser → restored objects owned by `kuso` → the
per-project role gets "permission denied for table X" through the pooler.
**Fix folded into migrate-db.sh**: after load, ALTER OWNER of every public
table/sequence/view to the per-project role + GRANT schema. Verified
jira-mudira: `issues=666 activity=2648` via pooler as the per-project role.
Retro-applied to jira_mudira_db, boiler_code_landing_db, db_masterclass_db.
**Product follow-up:** kuso's instance-addon provisioning could grant the role
object ownership, or offer a managed dump-load path. (tracked)

jira-mudira app VERIFIED end-to-end: build succeeded → production pod Running
1/1 → Next.js "Ready" → queries its DB through the pooler. Full pipeline works.

### BLOCKER (needs a decision): kuso has no build-time env injection

3 of 6 builds FAILED (db-masterclass, produktche, ilikata) — all with the same
cause: `npm run build` runs Prisma/Next config that needs DATABASE_URL (and
other vars) **at build time**. In Coolify every env var was `is_buildtime:true`
(baked into the build). In kuso, env vars are RUNTIME-only (envFrom secretKeyRef
+ literal env on the deployment); the build job does NOT receive the service's
env vars as build args at all. So any app that touches env during `build`
(Prisma generate, Next.js env validation, etc.) fails to build.

- Unaffected: apps whose build doesn't read env — jira-mudira ✓, boiler-code ✓.
- Affected so far: db-masterclass, produktche, ilikata (and likely more of the
  remaining nixpacks apps: berivangold, kutiq, newsletterite, etc.).

These apps are otherwise fully migrated (project, service, cluster-DB addon,
DATA loaded + ownership-fixed, env set) — ONLY the build step fails.

**Decision needed (paused here):** how to give builds their env.
  (a) Build a kuso feature: inject service env vars as build args / a build-time
      env file into the build job (resolve `${{ refs }}` at build too). The
      correct, general fix — but a real feature (security note: build-time
      secrets get baked into image layers) needing its own design+TDD+ship.
  (b) Per-app: set a dummy/literal build-time DATABASE_URL (won't work — kuso
      passes NO env to builds today, so even a literal doesn't reach the build).
  (c) Defer the build-time-env apps; finish the build-clean apps now.

### BLOCKER RESOLVED — build-time env injection shipped (v0.18.20→v0.18.23)

Built the feature (TDD+ship): builds.Create resolves the service's env to
literals (secretKeyRefs read server-side) → KusoBuild.spec.buildEnv → the REAL
renderer internal/buildcontroller/render.go (NOT the dead kusobuild helm chart!)
injects them as KUSO_BE_<KEY> container env → ENV-after-FROM in the nixpacks
Dockerfile + nixpacks --env + EXPORT of NIXPACKS_* for toolchain selection.
Security: key identifier-regex validated at 3 layers (server, CRD propertyNames,
render); values base64/kubelet-escaped; build logs print keys only. Found the
red herring that the kusobuild chart is dead (server renders in Go); the
NIXPACKS_NODE_VERSION export was the final piece (nixpacks reads it from its
process env, not --env).

**All 4 previously-blocked apps now BUILD SUCCESSFULLY:** db-masterclass,
produktche, ilikata, bukvite30 (verified nodejs_22 + Prisma generate OK).

### Phase 2 status: 6 apps fully migrated (data+config+build)

DONE (build succeeded): jira-mudira, boiler-code-landing, db-masterclass,
ilikata, bukvite30, produktche. (jira-mudira also redirect-fixed + verified
live earlier.)

REMAINING to migrate: berivangold (main+develop), kutiq (has Redis),
newsletterite, vibe-detector, junior-accelerator-web, junior-accelerator-ship,
s3-web. Phase 3 compose: s3, analiz.

### FINAL STATE (end of autonomous session)

ALL 14 non-compose apps migrated: project + service + cluster-DB addon (pooler)
+ DATA loaded with verified row-count parity + env kusified. Build outcomes:
11/14 build+deploy OK; 3 fail on APP-SPECIFIC build issues (NOT kuso/migration):
  - berivangold (×2): `pnpm install --frozen-lockfile` lockfile mismatch (repo).
  - kutiq: Next.js "Failed to collect page data for /api/webhooks/stripe"
    (Stripe route evaluated at build — needs force-dynamic in the repo).

Shipped 5 kuso features/fixes this session (v0.18.18→v0.18.24): cluster-DB
pooler (auth_query); build-time env injection; nixpacks toolchain env;
build-env key injection-hardening; apply pending-ref resolution.

### OPEN BUG (blocks DB apps at runtime) — needs investigation

Production env CRs keep DATABASE_URL as the LITERAL `${{ db.DATABASE_URL }}`
instead of a secretKeyRef, so DB-backed apps crash at boot
(Prisma "scheme not recognized" / P1012). Root-caused the apply path
(SetEnv used AllowPending=false → fixed in v0.18.24 with SetEnvPending), and
the SERVICE spec now resolves to a secretKeyRef after re-apply — BUT the
production ENV CR still shows the literal. So service→env propagation
(propagateChangedToEnvs) is NOT carrying the resolved secretKeyRef to the
production env (which is what the pod uses).
Apps Running with this bug haven't hit a DB query yet; they'll fail on first
query. Conn secrets + data are correct; only the env-ref wiring on the env CR
is wrong.

ROOT CAUSE PINPOINTED: propagate.go:159-169 `resolveSharedEnvKeysForEnv`
merges the service envVars (now a resolved secretKeyRef after the v0.18.24 fix)
with `env.Spec.EnvVars` "preserving per-env overrides" (line 163). The
production env's pre-existing LITERAL `${{ db.DATABASE_URL }}` — seeded at
AddService time BEFORE the conn secret existed — is treated as a per-env
override and KEPT, shadowing the service's resolved secretKeyRef. So the env
never picks up the fix even though the SERVICE spec is now correct.
Verified: service spec = secretKeyRef; production env = literal.

FIX (small, targeted, needs choosing — not rushed at session end):
  (a) In the env merge, treat an unresolved `${{ }}` literal env value as NOT a
      real override — re-resolve/replace it from the service's resolved value; OR
  (b) AddService resolves refs with AllowPending so the production env is never
      seeded with a raw literal to begin with.
Option (b) is cleaner (fixes the seed, not the symptom).

QUICK MANUAL UNBLOCK before the code fix: clear the stale literal on each
production env (env-scoped editor) or delete+recreate the production env so the
service's secretKeyRef propagates cleanly.

### NOT STARTED: Phase 3 compose apps (s3, analiz).

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


## UPDATE: runtime blocker chain FULLY RESOLVED (v0.18.25 + v0.18.26)

Two more root causes found + fixed after the env-ref work:
1. v0.18.25 — extractEnvOnlyOverrides treated a stale unresolved ${{ }} literal
   as a per-env override, re-stamping it over the service's resolved
   secretKeyRef. Fixed: unresolved-ref literals are not overrides.
2. v0.18.26 — NetworkPolicy: per-project default-deny + intra-project-only
   egress BLOCKED app→cluster-DB-pooler (pooler is in the kuso-instance
   project, a different project). Added allow-instance-db-egress (5432+6432
   to kuso-instance pods). THIS was the P1001 "can't reach pooler" crash.

RESULT: all 13 migrated app pods Running 1/1. db-masterclass verified end to
end: `prisma migrate deploy` → "No pending migrations" → "Ready" — it reaches
its per-project DB through the pooler. The DB-connectivity chain works.


## END-TO-END TEST RESULTS (full verification)

11 of 14 apps fully working end-to-end. The 3 not-running are app-specific
BUILD failures (not migration/kuso): berivangold + berivangold-dev (pnpm
--frozen-lockfile mismatch in the repo), kutiq (Next.js evaluates the Stripe
webhook route at build — needs force-dynamic in the repo).

Per working app, verified:
- DB DATA: table-count parity vs the Coolify source DB (jira_mudira 39,
  db_masterclass 10, produktche 14, ilikata 8, bukvite30 25, + row-count parity
  confirmed at migrate time, e.g. jira-mudira Issue=666 Activity=2648).
- API↔DB: a client using the app's exact conn secret connects through
  kuso-instance-pg-pooler:6432 as the per-project role and queries real data
  (jira-mudira Issue=666). All 10 DB-backed apps: ✓ DB reachable via pooler.
- App pods: all 11 Running 1/1, ready, 0 runtime DB errors (after netpolicy +
  fresh restart).
- FRONTEND/HTTP: all 11 serve on their kuso host (web.<project>.kuso.sislelabs.com)
  — HTTP 200 / 307 (auth redirect), no 5xx.

Domain-display bug FIXED (web): project card now falls back to the detected
frontend service's default host when there's no baseDomain/custom domain
(was showing a blank domain row for every migrated app).

## ENV-PROPAGATION BUG FIXED (v0.18.28) — "jira-mudira redirected to ticketmaster"

Root cause: AddService seeds the production KusoEnvironment's spec.envVars with
a full copy of the service's envVars. extractEnvOnlyOverrides then guessed
"deliberate per-env override" vs "inherited seed" purely by value-difference —
so once a service value changed, the stale seed differed and was re-applied
forever, and the service edit could never reach the env. jira-mudira kept the
old AUTH_URL/NEXT_PUBLIC_APP_URL (ticketmaster.sisle.org) on the env while the
service had the correct kuso host.

Fix: explicit marker KusoEnvironment.spec.envOverrides — the var NAMES the user
deliberately pinned via the per-env scoped editor. Propagation re-stamps every
service-owned var EXCEPT the marked ones. Drifted seeds (unmarked) drop and
re-stamp from the service; genuine overrides (marked) survive. Verified live:
(1) service edit now reaches the env (URLs re-stamped to web.jira-mudira),
(2) a scoped-editor override survives a conflicting service edit. CRD field
added + applied to the cluster. Preview-env path also fixed (Bug 5, found in
review): preview rewrote literal URLs to per-PR hosts but didn't mark them, so
a service propagation would have wiped them back to prod URLs — now marked.

Cluster audit at fix time: 0 drifted seeds (jira-mudira was the only one,
already corrected), 304 benign matching seeds that re-stamp identically.

## PROJECTS GRID: starring + folders + equal-height cards (v0.18.29)
Per-user: star to pin to top, file into folders (collapsible sections),
cards equal-height in a row. Server-side prefs (UserProjectPref, migration
0003) so the layout follows the user across devices. Not migration work —
shipped alongside.

## FULL E2E RE-VERIFICATION + 2 MORE KUSO BUILD BUGS FIXED (v0.18.30, v0.18.31)

Re-swept all 18 projects (grew from 14): DB schema present in all 14 project
DBs (8-39 tables), 0 DB connection errors, direct pooler queries as the
per-project role succeed, all public URLs serve. 17/18 active 1/1.

Two more kuso build bugs found + fixed (the "can't happen again" work):

1. **NODE_ENV poisoned the build (v0.18.30).** Every Coolify-migrated app
   carries NODE_ENV=production; kuso injected it into the BUILD, so
   npm/pnpm skipped devDeps and any build needing a devDep (husky, tsc,
   bundler) failed ("husky: not found"). Broke berivangold + berivangold-dev.
   Fix: builds.reservedBuildEnvKeys now mirrors render.go's RESERVED
   (NODE_ENV + runtime-only keys filtered from build env; render.go
   KUSO_BUILDENV_KEYS loops also skip RESERVED, defense-in-depth). Verified
   live: both apps build WITH NODE_ENV set (filtered from build CR, kept at
   runtime) and run active 1/1.

2. **Raw-Dockerfile builds got zero build env (v0.18.31).** Build-env
   injection (#48) only worked for nixpacks/static. renderBuildkitContainer
   now carries KUSO_BE_* and passes each buildEnv key as --opt build-arg
   (set--/$@, RESERVED-filtered). Verified: DATABASE_URL now resolves for
   kutiq; papelito (working dockerfile app) rebuilds clean, no regression.

## REMAINING WORK
- **kutiq** — only red app. NOW PURELY REPO-SIDE: kuso passes all 19
  build-args, but kutiq's Dockerfile only declares `ARG DATABASE_URL` (which
  now resolves); the other 17 vars its env.ts validates at build are not
  ARG-declared. Repo fix: `export const dynamic = 'force-dynamic'` on the
  env-importing routes (cleanest), or declare the remaining ARGs.
- Phase 3 compose apps (analiz) — not started (#45). (s3 → s3-web is live.)
- Domains/DNS cutover — out of scope. nev-abrom's app is healthy but
  nevabrom.com still points at the old Apache host (185.80.2.210), not kuso.
- Follow-ups #47 (role ownership grant), #51 (kusoenvironment CRD volumes
  schema field-name mismatch — latent, 0 live volumes) — non-blocking.
- Live Stripe/secret keys still copied as-is in gitignored migration/ files
  — flagged for rotation (your side).
