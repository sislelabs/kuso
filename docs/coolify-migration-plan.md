# Coolify → kuso migration plan

Migrate the 16 live Coolify apps to kuso (everything **except domains** — no DNS
cutover, no Let's Encrypt issuance for now), kusify env vars, route DB-backed
apps through a **shared PgBouncer in front of the cluster DB**, and **dump the
Coolify shared Postgres data into kuso**.

## Hard constraints discovered (read this first)

1. **PgBouncer in front of the cluster DB does NOT exist yet.** kuso's cluster
   DB (`kuso-instance-pg`) is direct-only; per-project provisioning writes a
   direct `kuso-instance-pg:5432` DSN, and the kusoaddon chart's pooler is
   explicitly disabled for `useInstanceAddon` projects. You chose to **build it
   first**. → **Phase 0 is a kuso feature, shipped before any app migrates.**
2. **No SSH access to the Coolify host** (`91.98.200.83`). The data dump
   (`pg_dump` of `postgrres-main`) must be run by **you** (or you grant access).
   I have the superuser password (harvested from app env vars). I CAN load the
   resulting dump into kuso (I have kuso cluster access).
3. **Domains are out of scope** — apps deploy on kuso's default
   `*.<baseDomain>` hosts (or no host), Coolify keeps serving the real domains
   until you later cut DNS. No app traffic moves in this migration.
4. **Coolify stays untouched** (your standing instruction) — read-only there;
   you decommission each app yourself after verifying it on kuso.
5. **Secrets are compromised** (surfaced via the API earlier). Migration
   re-enters them into kuso; rotate the sensitive ones (OpenAI, R2, OAuth, the
   PG superuser) as we go.

## Phase 0 — Build the shared cluster-DB PgBouncer (kuso feature)

Brainstorm → spec → TDD → ship, as its own change before migrating apps.

- Lift the kusoaddon pooler so a pooler can also front the **instance** PG
  (`kuso-instance-pg`), OR add a dedicated shared pooler Deployment in the
  cluster-DB provisioning path (mirrors `deploy/postgres.yaml`'s control-plane
  pooler). Transaction-pool mode, userlist rendered from per-project roles.
- Populate `POOLER_HOST`/`POOLER_PORT`/`POOLER_URL` in **both** the
  `kuso-instance-pg-conn` secret and each per-project `<addon>-conn` secret, so
  a project's `DATABASE_URL` can point at the pooler (`<pooler>:6432`) instead
  of `:5432`.
- Decide pooler auth model for per-project users (PgBouncer `auth_query`
  against the instance PG vs. per-user userlist) — design question for the spec.
- Ship + verify: a project on the cluster DB connects through the pooler.
- **Open design Q for the spec:** one shared pooler for all cluster-DB
  projects (simplest, matches Coolify) vs. per-project pooler. Default: one
  shared pooler.

## Phase 1 — Migration prerequisites (one-time)

1. **GitHub App**: confirm the kuso GitHub App is installed on the `biznesguys`
   (+ `Kaisiq`) org so kuso can build all repos + drive PR previews. One install
   covers all 16 apps.
2. **Cluster DB**: already provisioned (`kuso-instance-pg`, READY) + pooler from
   Phase 0.
3. **Data discovery**: you run `psql -l` on `postgrres-main` (Coolify host) and
   share the logical DB list + sizes. Known so far: `ticketeer` (jira-mudira),
   `dbmaster` (db-masterclass); the rest are inside each app's `DATABASE_URL`.
4. **Per-app classification** (done during each app's migration, not upfront):
   DB-backed (needs data + DB routing) vs. stateless (env + deploy only).

## Phase 2 — Per-app migration loop (one app at a time)

Order: stateless first (lowest risk), then DB-backed, then compose apps last.
For EACH app:

1. **Create the kuso project + service** pointing at the same Git repo/branch,
   runtime mapped from Coolify's build pack (nixpacks→nixpacks, dockerfile→
   dockerfile; compose apps decomposed — see Phase 3).
2. **If DB-backed:**
   a. Opt the project into the cluster DB (`useInstanceAddon: "pg"`) → kuso
      creates `CREATE DATABASE "<project>_pg"` + a per-project role + a
      `<addon>-conn` secret.
   b. **Dump + load the data:** you `pg_dump` the app's logical DB from
      `postgrres-main`; I load it into the new `<project>_pg` database on
      `kuso-instance-pg` (via a one-shot Job or `psql` from the cluster).
      Verify row counts match.
3. **Kusify env vars:** copy the app's Coolify env vars into the kuso service,
   EXCEPT rewrite `DATABASE_URL` to kuso's per-project conn — routed through the
   **pooler** (Phase 0): `kuso ${{ pg.POOLER_URL }}`-style ref (exact templating
   confirmed in Phase 0). Drop Coolify-internal hostnames
   (`pgbouncer-y0ok8…`). Reclassify build-time secrets as runtime. Flag
   weak/placeholder secrets (e.g. `AUTH_SECRET=development-...`).
4. **Deploy on kuso** (default host, no custom domain). Build → release/migrate
   → running.
5. **Verify**: app boots, connects to its kuso DB, serves on its kuso URL.
6. Leave Coolify running. You decommission that Coolify app once satisfied.

## Phase 3 — The compose apps (s3, analiz; others are dead)

Only `s3` + `analiz` are live compose apps. Each decomposed into kuso
services + addons (analiz = web + ingest; s3 = MinIO-style storage → kuso
service + volume or storage addon). Done last, individually.

## What I need from you to proceed

- **Approval to build Phase 0** (the pooler) first — it's a real kuso feature
  with its own design cycle.
- **Coolify host access OR you run the dumps**: either add the Coolify host to a
  key I can use, or run `pg_dump`/`psql -l` yourself and hand me the dumps.
- **GitHub App install** confirmation on the org.

## Testing / verification per phase

- Phase 0: Go TDD (pooler rendering, conn-secret POOLER_* population) + live
  (a cluster-DB project connects through the pooler).
- Phase 2: per app — row-count parity after data load; app health 200 on its
  kuso URL; DB queries succeed through the pooler.
- No domain/DNS changes, so production traffic is unaffected throughout.

## Out of scope (explicit)

- Domains / DNS / TLS cutover (later, your call).
- The `monitoring` service (use kuso-native metrics).
- The ~9 dead/exited Coolify apps (skip unless you say otherwise).
- Touching/decommissioning anything in Coolify (you do that).
