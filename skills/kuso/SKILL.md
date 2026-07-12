---
name: kuso
description: Use when working in a project deployed to kuso (a self-hosted Kubernetes PaaS) — any time the user mentions deploys, builds, logs, env vars, secrets, addons (postgres/redis/valkey/mongodb/rabbitmq/clickhouse/redpanda/s3/nats/…), addon subscriptions, staging/preview/PR environments, branch tracking, domains, release hooks, migrations, docker-compose import, marketplace apps, sleeping/stopped pods, callback webhooks, backups, or anything related to their kuso instance. Covers the kuso CLI surface, the deploy lifecycle, env-var `${{ }}` reference syntax, and the standard debugging playbook.
allowed-tools: Bash(kuso:*), Bash(curl:*), Bash(awk:*), Bash(ssh:*), Read, Edit, Write, Grep, Glob
---

# kuso — operating a project on this PaaS

This project is deployed via [kuso](https://github.com/sislelabs/kuso), a self-hosted Kubernetes PaaS. The user has a `kuso` CLI on their PATH and a logged-in session against their instance. **Always drive operations through `kuso`, not raw `kubectl`** — the CLI exercises the same auth/tenancy/perm layers users hit, so what you see is what they see.

This skill is current to **v0.18.128**. Run `kuso version` to confirm what's on the user's machine; several gotchas below are version-gated.

> **Env vars & secrets — the default rule:** set most variables (sensitive or
> not) through `kuso env set` (service-level) or `kuso shared-secret set`
> (project-level) so they show in the Variables tab, the rendered spec, and the
> audit trail. `kuso secret set` writes a Kubernetes-Secret-backed value that
> does NOT appear in `kuso env list` — but it IS listed (keys only) by
> `kuso secret list`, and it is the RIGHT home for external/provider secrets a
> project's own docs mandate keep out of the rendered spec (e.g. `STRIPE_SECRET_KEY`,
> `RESEND_API_KEY`, webhook signing secrets). Prefer `env set`; reach for
> `secret set` when a value must not surface in the spec/UI or a project's
> CLAUDE.md explicitly requires it. Full rules in the "Env vars & secrets"
> section below — read it before touching any variable.

## Mental model — read this first

- **Project** = the top-level grouping. One repo or many; one base domain.
- **Service** = one deployable app inside a project. Has a runtime, a port, and env vars.
- **Environment** = one running instance of a service. Each service auto-gets a `production` env. PR previews AND long-lived named envs (`staging`, `qa`) are extra envs. A named env **tracks a git branch** — pushes to that branch auto-build+deploy it (v0.18.120+). See "Persistent environments".
- **Addon** = a managed datastore. Each addon writes a `<project>-<addon>-conn` Secret that kuso injects into a service via `envFromSecrets` — you do NOT wire `DATABASE_URL` etc. by hand; they appear in `process.env`. By default (legacy / `subscribedAddons` unset) every addon mounts into every service. Trim per service with `kuso project addon subscribe/unsubscribe` so a public frontend doesn't carry `DATABASE_URL` — see "Env vars & secrets".
- **Build** = a kaniko Job that produces an image and patches the env's `image.tag`. One build per `(service, ref)`. Helm-operator rolls the new pod.
- **Deploy = push.** kuso auto-deploys on `git push` to a branch some env tracks (production tracks the service default, usually `main`): the GitHub webhook fires a build, which promotes and rolls the new pod with zero manual steps. **A merge to `main` is already a production deploy — you do NOT run anything to ship it.** `kuso build trigger` / `kuso redeploy` are only for *out-of-band* rebuilds (rebuild without a new commit, deploy a non-tracked ref, re-run after a transient failure) — not part of the normal ship flow.
- **Release hook** (v0.16+) = an optional Job that runs **before** the new image is promoted. Heroku-style migration phase. Set via `spec.release.command`.
- **kuso.yml** = optional config-as-code at repo root. **See "Config-as-code caveats" below before using `kuso apply`.**

The CLI is rooted at `kuso <command>`. Run `kuso <command> --help` whenever shape is unclear — every command has examples.

## Two flag conventions — learn the difference

| Command | Command argv syntax |
|---|---|
| `cron add` / `cron add-command` / `cron add-http` | `--cmd '<shell string>'` flag (`add-http` uses `--url`) |
| `run` | `--` separator: `kuso run <p> <s> -- sh -c '...'` |
| `env set` | `KEY=VALUE` (multiple per command); `--env <name>` scopes to one environment |
| `env unset` | `KEY [KEY ...]`; `--env <name>` for a per-env override |
| `env share` / `env unshare` | `<p> <s> KEY [KEY ...]` — subscribe/unsubscribe a service to project/instance shared-secret keys |
| `shared-secret set` | `KEY=value` (ONE pair per call — `accepts 2 arg(s)` if you pass more) |
| `secret set` | 4 positional args `<p> <s> KEY VALUE`; Kubernetes-Secret-backed, NOT in `env list` (keys show in `secret list`). Shadowing a project-shared key 409s unless `--force` |
| `instance-addon register` | positional: `register <name> <dsn>` (no `--name`/`--dsn` flags) |

This inconsistency is real. When you get `Error: accepts N arg(s), received M`, you've hit the wrong convention.

## First-time setup

```bash
# Verify session — token, DNS, server reachability, auth, GitHub webhook health.
kuso doctor

# If doctor fails on token: log in.
kuso login --api https://kuso.<your-domain> --token <pat>
```

## Imperative path (recommended) — create everything via subcommands

```bash
# 1. Project. --repo is REQUIRED. --domain sets the base domain.
kuso project create papelito \
  --repo https://github.com/biznesguys/papelito \
  --domain papelito.example.com

# 2. Addons. Their conn secret auto-injects into every service that
#    subscribes (default: all — tighten per service in step 5b).
kuso project addon add papelito db --kind postgres --version 16 --size small
kuso project addon add papelito storage --kind s3
kuso project addon add papelito cache --kind redis
# All IMPLEMENTED kinds (chart renders a real workload + conn secret):
#   postgres, redis, valkey, mongodb, rabbitmq, s3, mailpit, nats,
#   meilisearch, clickhouse, redpanda (Kafka API).
#   (CLIs ≤ v0.18.128 reject --kind valkey — upgrade the CLI or use kuso.yml.)
# RESERVED-but-not-implemented (creating one renders only a "pending"
# marker — DON'T use as if it works): mysql, memcached, elasticsearch,
# kafka (use redpanda), cockroachdb, couchdb.
# Postgres wire TLS (off by default): --tls require  (see "Addon TLS").

# 3. Service from a repo (default: build via dockerfile)
kuso project service add papelito web \
  --runtime dockerfile --port 3000
# 3a. Monorepo with a non-standard Dockerfile name/path (v0.18+):
#     --dockerfile is RELATIVE to --path; default is "Dockerfile".
kuso project service add papelito web \
  --runtime dockerfile --path . --dockerfile apps/web/Dockerfile.dev --port 3000

# 3b. OR: service from a pre-built registry image (no kaniko build)
#     --image-repo + --image-tag are SEPARATE; don't put X:Y in --image-repo
kuso project service add papelito web \
  --runtime image \
  --image-repo ghcr.io/sislelabs/papelito \
  --image-tag v1.2.3 \
  --port 3000

# 4. Domains
kuso domains add papelito web papelito.example.com

# 5. Env vars — default `kuso env set` (visible in spec/audit). Provider secrets
#    that must stay out of the spec (STRIPE_SECRET_KEY, …) → `kuso secret set`.
kuso env set papelito web NODE_ENV=production NEXT_TELEMETRY_DISABLED=1
kuso secret set papelito web STRIPE_SECRET_KEY sk_live_xxx
# Values shared across services → project-level shared secret (one K=V/call):
kuso shared-secret set papelito JWT_SECRET=...        # subscribed services inherit it
# Addon-conn key whose NAME differs from what your app reads → ${{ }} alias:
kuso env set papelito web 'S3_ACCESS_KEY=${{ storage.S3_ACCESS_KEY_ID }}'

# 5b. Least privilege: trim a public frontend to no addons + no secrets.
kuso env share papelito web ENVIRONMENT                # only this shared key
kuso project addon unsubscribe papelito web db cache   # drop DB/redis conns
kuso project addon list papelito web                   # verify what's mounted

# 6. Trigger the FIRST build (repo-based runtimes only). One-time bootstrap:
#    there's no commit-since-creation to have fired a webhook yet. After this,
#    every push to the tracked branch auto-builds+deploys — you won't run this again.
kuso build trigger papelito web

# Watch it
kuso logs papelito web -f
kuso status papelito
```

This imperative path is **the safe one**. Use it unless you have a specific reason to prefer config-as-code.

## Persistent environments & branch tracking (v0.18.120+)

Long-lived non-production envs (`staging`, `qa`, `client-demo`) are first-class:

```bash
# Create a staging env for one service, tracking the develop branch.
kuso environment add papelito web staging --branch develop

# Options:
#   --host <fqdn>       custom host for the env (else auto <svc>-staging.<base>)
#   --seed-from <env>   copy a DB snapshot from another env (e.g. production)
#   --addons <kinds>    which stateful kinds get their OWN per-env instance
#   --share-addons      legacy: share production's addons instead (no isolation)
kuso environment list papelito
kuso environment delete papelito web-staging      # production can't be deleted

# Per-env extra hostnames:
kuso environment domain add papelito web staging staging.papelito.bg
```

- **By default a new env gets its own isolated addons** (its own DB/redis), same machinery as PR-preview DB clones. `--share-addons` opts back into sharing production's.
- **Pushes to the tracked branch auto-build that env** — same webhook flow as production. First build after creation may need a manual `kuso build trigger`.
- **Config-as-code stays production-safe:** a push to a non-default branch deploys its **image only**; `kuso.yml` changes are applied from the default branch alone, so a staging branch can't rewrite production-shared settings.
- Per-env env-var overrides: `kuso env set <p> <svc> --env staging KEY=val`.
- `kuso env-group create <p> <name>` clones every service+addon into a whole new env set at once (project-wide staging).

## Release hooks (v0.16+) — migrations the right way

The footgun this replaces: people stuff `migrate up && exec /app/api` into the API's entrypoint. With ≥2 replicas, both pods race the migration; with a long migration, the readiness probe fails before it finishes and the deploy thrashes.

`spec.release.command` runs as a **separate Job against the NEW build's image** before the image tag is promoted to the env. On non-zero exit, the build is marked `release-failed`, the image is NOT promoted, and existing pods keep running on the previous image. A `build.failed` notify event fires.

There is no CLI flag for the release block — set it via `kuso.yml`'s `services[].release` (then `kuso apply`), or PATCH:

```bash
# Set the release hook
curl -X PATCH -H "Authorization: Bearer $(awk '{print $2}' ~/.kuso/credentials.yaml)" \
  -H "Content-Type: application/json" \
  -d '{"release":{"command":["./bin/migrate"],"timeoutSeconds":600}}' \
  https://kuso.example.com/api/projects/tickero/services/api

# Trigger a build — the release Job fires automatically before promote
kuso build trigger tickero api

# Inspect a failed release: build why classifies it, or read the Job logs
kuso build why tickero api
```

Job naming: `<env-name>-release-<short-image-tag>`. Re-deploying the same tag is a no-op (Job exists, already succeeded). The Job runs with the env's effective envVars + envFromSecrets (so `DATABASE_URL` etc. are available) and waits for addons to be reachable first.

To clear the hook: PATCH `{"release":{"clear":true}}`.

## Sleep, wake, and hard-stop

Three distinct states — don't conflate them:

- **Sleep (scale-to-zero):** `scale.min=0` + `sleep.enabled` — idle pods scale to zero; the activator wakes them on the next request (hold-and-proxy: the request waits for cold-start, it is not replayed).
- **Wake:** `kuso project service wake <p> <s>` forces a sleeping service up now.
- **Hard-stop:** `kuso project service stop <p> <s>` (or `kuso project stop <p>` for everything) pins 0 replicas and does NOT wake on traffic — visitors get a "service stopped" page until `... start`.

### wakeOn excludePaths — keep callback paths warm

ePay.bg / Stripe / GitHub webhooks have short retry timeouts; a cold-start can exceed the sender's window. `spec.sleep.wakeOn.excludePaths` is the "this deployment MUST stay reachable" signal: when set, the deployment stays at min 1 even with `scale.min=0`. No CLI flag — kuso.yml or PATCH:

```bash
curl -X PATCH ... \
  -d '{"scale":{"min":0,"max":3,"targetCPU":70},
       "sleep":{"enabled":true,"wakeOn":{"excludePaths":["/api/v1/payments/notify"]}}}' \
  https://kuso.example.com/api/projects/tickero/services/api
```

**Semantic:** whole-deployment, not per-path routing. If any path matters, the whole deployment stays warm. For per-path isolation, split into two services. Clear with `{"sleep":{"wakeOn":{"clear":true}}}`.

## Cron jobs & failure webhooks

```bash
kuso cron add <p> <svc> --name nightly --schedule '0 3 * * *' --cmd './bin/sweep'   # runs on the service's image
kuso cron add-command <p> --name sweep --schedule '0 * * * *' --image ghcr.io/org/api --image-tag v1.2.3 --cmd '/app/bin/sweep-refunds'
kuso cron add-http <p> --name ping --schedule '*/5 * * * *' --url 'https://...'
kuso cron sync <p> <svc> <name>       # re-resolve image/env from production after a deploy
kuso cron edit <p> <name> --schedule '...' --suspend=false
```

Crons can POST an HMAC-signed payload to a webhook when they fail — anything where silent cron failure is a revenue leak. No CLI flag yet; PATCH the cron:

```bash
curl -X PATCH ... \
  -d '{"onFailure":{"webhookURL":"https://hooks.slack.com/services/...",
                    "secretRef":{"name":"tickero-slack-conn","key":"signing-secret"}}}' \
  https://kuso.example.com/api/projects/tickero/crons/refund-deadline-sweep
```

The watcher polls Jobs labeled `kuso.sislelabs.com/cron` every 30s; on terminal failure it POSTs `{project, service, cron, jobName, startedAt, finishedAt, logsURL}` with `X-Kuso-Signature: sha256=<hex>` when `secretRef` is set, retrying 3x with linear backoff. It also emits `cron.failed` to notify subscribers — make sure the Discord/Slack channel subscribes to that event.

## Backups — addon data and control plane

```bash
# On-demand dump straight to your disk — no S3 config needed (editor role).
# postgres → .sql.gz, s3 → .tar.gz; other kinds not supported.
kuso addon-backup download <p> <addon> [-o dump.sql.gz] [--force]

# Scheduled dumps to the cluster-wide S3 bucket:
kuso addon-backup schedule <p> <addon> --schedule '0 3 * * *' --retention 14
kuso addon-backup list <p> <addon>                 # stored dumps
kuso addon-backup restore <p> <addon> <s3-key>     # --into <sibling> or in-place (DESTRUCTIVE, needs --confirm)

# The S3 bucket + health for all of the above (admin):
kuso backup settings get / set --bucket ... --endpoint ... --access-key-id ...
kuso backup health                                  # are backups actually landing?
kuso backup --output kuso-backup-$(date +%s).sql.gz # control-plane DB dump
```

### External-DB backups — PlanetScale / Neon / Supabase / RDS

When your addon is `external` (BYO managed Postgres via `spec.external.secretName`), kuso renders a `pg_dump` CronJob that snapshots to the cluster-wide S3 bucket:

```yaml
apiVersion: application.kuso.sislelabs.com/v1alpha1
kind: KusoAddon
spec:
  project: tickero
  kind: postgres
  external:
    secretName: tickero-planetscale   # user creates this Secret with DATABASE_URL
  backup:
    schedule: "0 3 * * *"
    retentionDays: 14
```

The user must create the source Secret with `DATABASE_URL=postgres://...` and configure the S3 bucket (`kuso backup settings set`, or Settings → Backups in the UI). Without it, the CronJob installs but every run fails — `kuso addon-backup list` and `kuso backup health` will tell you. Uses `pg_dump --no-owner --no-acl`, which is what managed providers recommend.

## Config-as-code caveats — `kuso apply`

`kuso apply` reads `kuso.yml` and reconciles it against the live project. Discipline:

- `--dry-run` prints the plan but doesn't write. **Always run with `--dry-run` first; eyeball every `delete` line before running without it.**
- A misspelled addon name in `addons:` looks identical to "user wants the live addon deleted." Plan diffs are merciless.
- (Historical: kuso servers older than the addon-scoping fix could list OTHER projects' addons in `addonsToDelete`. Current servers scope the plan to the file's project — but the dry-run discipline stays.)
- `--rotate-secrets` re-mints `{generate:}` secrets — only when you mean it.

```bash
kuso init --project myproj --runtime dockerfile --port 8080
# edit kuso.yml
kuso apply --dry-run        # always first
# Read every line. Confirm only your project's resources appear.
kuso apply                  # only after the dry-run is clean
```

## Importing a docker-compose project (v0.18+)

`kuso import compose <docker-compose.yml>` converts a local compose file into kuso resources. Datastore services become managed **addons**; app services become build (`runtime=dockerfile`) or image (`runtime=image`) services; `depends_on` env refs are rewritten to `${{ addon.KEY }}`. Anything kuso has no equivalent for is **reported, never silently dropped**.

```bash
kuso import compose docker-compose.yml                  # dry-run: prints the report + generated kuso.yaml
kuso import compose docker-compose.yml -o kuso.yaml      # write the kuso.yaml for review
kuso import compose docker-compose.yml --apply           # create resources (auto-creates the project first)
kuso import compose docker-compose.yml --project shop --apply
```

Caveats:
- Only **implemented** addon kinds map; images with no managed addon equivalent stay as flagged image services.
- `build:` services land with a blank `repo:` — set the git repo before they'll build (the report flags this).
- It does NOT migrate data — move addon data separately (`kuso addon-backup`, or pg_dump/restore).

## Marketplace — one-click self-hosted apps

```bash
kuso marketplace list                     # catalog: gitea, metabase, n8n, plausible,
kuso marketplace info uptime-kuma         #   umami, uptime-kuma, vaultwarden, ...
kuso marketplace deploy uptime-kuma --project tools --set domain=status.example.com
kuso marketplace deploy n8n --dry-run     # render without applying
```

Deploy renders the app's kuso.yaml server-side (prompts filled via `--set`), ensures the project exists, and applies it through the normal apply flow — so the result is ordinary kuso services/addons you manage like any other.

**Images that drop root themselves (setpriv/gosu) crash with exit 127** because kuso drops ALL capabilities by default. Fix is the opt-in per-service securityContext:

```bash
kuso project service set tools uptime-kuma \
  --cap-add SETUID --cap-add SETGID --allow-privilege-escalation on
```

Capabilities pass a server-side allowlist (escape-equivalent caps like `SYS_ADMIN` are rejected).

## Env vars & secrets — the complete model

There are four places a variable can live. Pick by scope. Default to `env set` /
`shared-secret set` (visible in the spec + audit trail); use `secret set` for
values that must stay OUT of the rendered spec — see the note under the table.

| Where | Set with | Scope |
|---|---|---|
| Service-level | `kuso env set <p> <svc> KEY=val` | all envs of one service (propagates to production + previews) |
| Per-env override | `kuso env set <p> <svc> --env <name> KEY=val` | ONE env only; wins over the service-level value for that key |
| Project shared | `kuso shared-secret set <p> KEY=value` | every service that subscribes (see subscriptions) |
| Addon-injected | (automatic) | the `<project>-<addon>-conn` Secret, mounted per subscription |

**The default: `kuso env set` / `kuso shared-secret set`, not `secret set`.**
`secret set` writes a Kubernetes-Secret-backed value that does NOT appear in the
Variables tab, the rendered service spec, or `kuso env list` — so most values
(including JWT secrets, most API tokens) belong in `env set` where the user sees
them in the UI and the audit trail captures them.

**But `secret set` is first-class, not forbidden.** Its keys ARE discoverable via
`kuso secret list <p> <svc>` (values never returned), it supports `--env` scoping
and a shadow-check against project-shared keys (409 unless `--force`), and it is
the correct home for **external/provider secrets that must not surface in the
committed spec** — `STRIPE_SECRET_KEY`, `RESEND_API_KEY`, `OPENAI_API_KEY`,
webhook signing secrets. Several project scaffolds' CLAUDE.md hard-rules mandate
exactly this. Decision: value must stay out of the rendered spec, or a project
CLAUDE.md says so → `secret set`; everything else → `env set`.

### Addon connection secrets — auto-injected, but mind the key NAMES

Add a postgres addon `db` → kuso writes `<project>-db-conn` and mounts it. Keys land on the pod automatically; you do NOT set `DATABASE_URL: ${{ db.DATABASE_URL }}`.

| Addon kind | Keys it injects (verify per-instance: `kuso get addons <p> -o json`) |
|---|---|
| `postgres` | `DATABASE_URL` (pooled when a pooler runs), `DIRECT_URL` (always direct — use for migrations), `POSTGRES_HOST/PORT/USER/PASSWORD/DB`, `POOLER_*` |
| `redis`    | `REDIS_URL`, `REDIS_HOST/PORT/PASSWORD` |
| `valkey`   | `VALKEY_URL/HOST/PORT` + Redis-compatible `REDIS_URL/HOST/PORT/PASSWORD` |
| `mongodb`  | `MONGO_URL`, `MONGODB_URI`, `DATABASE_URL`, `MONGO_HOST/PORT/USER/PASSWORD/DB` |
| `rabbitmq` | `AMQP_URL`, `RABBITMQ_URL`, `RABBITMQ_HOST/PORT/USER/PASSWORD` |
| `s3`       | `S3_ENDPOINT`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`, `S3_BUCKET`, `S3_REGION`, `AWS_*` |
| `nats`     | `NATS_URL`, `NATS_HOST/PORT/TOKEN`, `NATS_MONITOR_URL` |
| `clickhouse` | `CLICKHOUSE_URL`, `CLICKHOUSE_HOST/HTTP_PORT/NATIVE_PORT/USER/PASSWORD/DATABASE`, `CLICKHOUSE_NATIVE_URL` |
| `redpanda` | `KAFKA_BROKERS` (bootstrap host:port), `KAFKA_HOST/PORT`, `REDPANDA_URL`, `REDPANDA_ADMIN/SCHEMA_REGISTRY/PROXY_URL` |
| `mailpit`  | `SMTP_HOST/PORT/FROM`, `MAIL_*`, `MAILPIT_UI_URL` |

> The canonical "connection URL" key is **per kind** — `postgres`→`DATABASE_URL`,
> `redis`→`REDIS_URL`, `clickhouse`→`CLICKHOUSE_URL`, `redpanda`→`KAFKA_BROKERS`.
> There is **no generic `.URL` key on an addon** (that's only the
> service-to-service form, `${{ api.URL }}`). Writing `${{ db.URL }}` for a
> postgres addon resolves to a non-existent secret key and the pod fails with
> `couldn't find key URL in Secret`. Always use the kind's real key name.

**Key-name mismatch is the #1 footgun.** kuso injects `S3_ACCESS_KEY_ID` but your app may read `S3_ACCESS_KEY`; kuso injects `DATABASE_URL` but you also want a read-replica `DATABASE_READ_URL`. Alias with `${{ <addon>.<KEY> }}`:

```bash
kuso env set <p> api 'S3_ACCESS_KEY=${{ storage.S3_ACCESS_KEY_ID }}'
kuso env set <p> api 'S3_SECRET_KEY=${{ storage.S3_SECRET_ACCESS_KEY }}'
kuso env set <p> api 'DATABASE_READ_URL=${{ db.DATABASE_URL }}'
```

### Addon TLS (postgres, v0.18.123+)

- **Default is `sslmode=disable`** — the managed postgres serves plaintext and
  `DATABASE_URL`/`DIRECT_URL` say so. Apps that reject non-TLS DSNs need the
  addon flipped: `kuso project addon add ... --tls require` at create, or
  `kuso project addon update <p> <addon> --tls require` live.
- `--tls require` is **postgres-only**; serves a self-signed `<name>-tls` cert
  (node-postgres rejects it; Go pgx/libpq accept it). `POOLER_URL` is **always**
  `sslmode=disable` — PgBouncer serves plaintext on :6432.
- A live flip is data-safe BUT consuming pods resolve `envFrom` at container
  start — they keep the stale `sslmode` until restarted. Flip the addon, then
  restart every subscribed env (`kuso redeploy` or an env-var touch).

### Public (external) addon access — the `:PORT` NodePort + `PUBLIC_URL`

The conn keys above are all **in-cluster** — `POSTGRES_HOST` is an in-cluster DNS
name only a pod inside kuso can reach. To connect from **outside** (laptop, CI,
migration tool), the addon must be explicitly exposed. It is **off by default**.

```bash
kuso project addon public-tcp enable <project> <addon>    # admin-only
#   → "addon <project>/<addon> exposed on public TCP port <PORT>"
kuso project addon public-tcp disable <project> <addon>   # frees the port
```

- **The port is server-allocated from a fixed pool** (`KUSO_TCP_PROXY_PORTS`).
  Read it back via the UI's `public · :<PORT>` badge or `kuso get addons <p> -o json`
  → `spec.publicTCP.{enabled,port}`. If the cluster has no pool configured the
  enable call fails with "public TCP proxy is not configured".
- **Connect at `<cluster-host>:<PORT>`** (e.g. `kuso.sislelabs.com:30001`), NOT
  at `POSTGRES_HOST:5432`.
- **`PUBLIC_URL` is a convenience DSN shown in the UI overview only** — the
  primary conn URL with its host rewritten to `<cluster-host>:<PORT>`. It is
  **NOT injected into pods** — no `${{ db.PUBLIC_URL }}`, no `PUBLIC_URL` in
  `process.env`. Its `?sslmode=` mirrors the addon's TLS setting (`disable`
  unless `--tls require`).
- **Auth is the addon's own protocol auth ONLY** — kuso adds no extra gate, so
  the endpoint is internet-reachable by anyone with the creds. Prefer a
  firewall/allowlist in front, and disable when done. Not every kind supports it.

### Querying an addon DB without exposing it

You don't need public-TCP (or a port-forward) just to run a query — the CLI
proxies read queries through the kuso API (postgres AND clickhouse addons):

```bash
kuso db tables <project> <addon>                       # list tables
kuso db columns <project> <addon> --table users        # schema of one table
kuso db sql <project> <addon> "SELECT count(*) FROM users"   # ad-hoc query
kuso db rows <project> <addon> --table users --limit 20      # browse rows
```

All `kuso db` commands are **admin-gated**. `kuso db sql` is read-oriented. For
an interactive session use `kuso db connect <p> <addon>` (`--exec` launches
psql / redis-cli / mongosh; clickhouse gets a built-in HTTP shell that reads SQL
on stdin — handy for migrations); for a raw local socket use
`kuso db port-forward`. Row writes are intentionally NOT in the CLI — do those
from a migration or the web data-grid.

### `${{ ... }}` reference syntax

The `${{ ... }}` must be the ENTIRE value (no `prefix-${{ ... }}-suffix`).

1. **Addon key (rename/alias)** — `${{ <addon-name>.<KEY> }}` → a `secretKeyRef` into `<project>-<addon>-conn`.
2. **Service-to-service URL** — `${{ api.URL }}` → `http://<project>-api-<env>.<ns>.svc.cluster.local:<port>` (in-cluster, resolves per-env). `${{ api.HOST }}`, `${{ api.PORT }}` for the parts. Use this for SERVER-SIDE calls (a Next.js app's `API_URL`); the browser-facing `NEXT_PUBLIC_API_URL` must stay the public https URL.

`kuso run` jobs resolve `${{ }}` aliases too (v0.18.116+ — on older servers a
`DATABASE_URI: ${{ db.DATABASE_URL }}` alias was dropped in runs; if you hit
that, map it inside the job: `sh -c 'export DATABASE_URI="${DATABASE_URI:-$DATABASE_URL}"; <cmd>'`).

### Subscriptions — least privilege (don't leak DB creds into a frontend)

By default every shared-secret key and every addon mounts into every service. Lock a service down to only what it needs:

```bash
# Shared-secret keys: env share/unshare. After trimming, verify with the UI
# or the service spec's sharedEnvKeys — the CLI count messages can mislead.
kuso env unshare <p> frontend JWT_SECRET TICKET_SIGNING_SECRET EPAY_SECRET ...
kuso env share   <p> frontend ENVIRONMENT          # frontend keeps only this

# Addon subscriptions — first-class CLI verbs:
kuso project addon list <p> frontend               # what's mounted now
kuso project addon unsubscribe <p> frontend db cache queue
kuso project addon subscribe <p> backoffice storage   # only S3 for this one
```

An explicitly-empty subscription list means "mount nothing" (an UNSET list is
legacy mount-all). A public Next.js frontend should end up with
`sharedEnvKeys=[ENVIRONMENT]` and no addon subscriptions — no JWT/payment
secrets, no DB/Redis/NATS conns. Previews respect subscriptions too.

### Validation gotchas (the app refuses to boot if these are wrong)

- Prod apps often reject `sslmode=disable` DSNs. kuso's managed postgres is
  **plaintext by default** — flip `--tls require` (see "Addon TLS") or point the
  app at `DATABASE_URL` as-is if it tolerates it. Prisma migrations: use
  `DIRECT_URL` for `directUrl` (the pooled `DATABASE_URL` breaks advisory locks).
- A migration release hook needs the addon ready; the release Job's
  wait-for-addons step handles that. For `kuso run` one-shots against a
  just-created addon, wrap in a retry: `sh -c 'for i in $(seq 1 30); do nc -z -w2 <addon-host> 5432 && exec ./cmd; sleep 2; done'`.
- `PORT` is reserved — kuso injects it (= the service port); you cannot
  override it via `env set`.

## Base domain & custom domains

- `kuso project create <p> --domain <base>` / `kuso project update <p> --domain <base>`
  sets the project base domain. Each service's auto-host is `<svc>.<base>`
  (the service whose short name == project gets the apex `<base>`). Changing it
  rewrites every env still on the old default host and re-mints certs (v0.17.26+).
- **Custom domains** (`tickero.bg`, `api.tickero.bg`) are added with
  `kuso domains add <p> <svc> <host>` (`--no-tls` for HTTP-only). They land as the
  env's `additionalHosts` and get their own LE cert. `--env <name>` scopes to one
  env; without it the host is mirrored onto the PRODUCTION env. DNS must already
  point at the cluster IP — kuso doesn't manage your registrar.
- A service can serve on its auto-host AND its custom hosts simultaneously.
  Make the base domain your real domain (`--domain tickero.bg`) so the primary
  host is `<svc>.tickero.bg` rather than `<svc>.<cluster-base>`.

## Preview (PR) environments

- Enable: `kuso project update <p> --previews=on --github-installation <id>`
  (find the install id with `kuso github installations`; the GitHub App must be
  installed on the repo's org). Auto-expire: `--previews-ttl <days>`.
- On PR open/reopen/sync kuso spawns `<svc>-pr-<N>` envs (+ a cloned, seeded,
  isolated preview DB `<addon>-pr-<N>`), builds from the PR branch, and tears
  them down on close/merge (in-flight preview builds are auto-cancelled).
  Previews are pinned to 1 replica, no autoscaling.
- **Preview host base**: `kuso project update <p> --previews-domain <base>` makes
  preview hosts `<svc>-pr-N.<base>` (e.g. `frontend-pr-35.tickero.bg`) instead of
  the cluster base. Needs wildcard DNS for `*.<base>`.
- Previews respect each service's subscriptions — a no-addons frontend preview
  correctly carries no addon conns; only db-subscribers get the `<addon>-pr-N`
  clone (never production, never non-subscribers).

## The commands you'll actually use

```bash
# Where am I? What's running?
kuso get projects [-o json]                     # all projects
kuso status <project>                           # rollup: services, URLs, replicas, latest build
kuso get services <project> [-o json]           # service specs
kuso get addons <project> [-o json]             # addons + connection-secret names
kuso service pods <project> <service> [--env <e>]   # pods backing an env
kuso service errors <project> <service> [--since 6h] # aggregated error groups
kuso service drift <project> <service>          # spec vs live-pod drift report
kuso health [fix <resource>]                    # cluster-wide reconcile health + one-shot remediation
kuso usage                                      # node/project resource + cost rollup

# Logs
kuso logs <project> <service>                   # last 200 lines
kuso logs <project> <service> -f                # tail (^C to stop)
kuso logs <project> <service> --env <env>       # non-prod env (staging, web-pr-N, ...)
kuso logs <project> <service> --build <id>      # a build pod's logs
kuso logs search <project> [service] --q "<query>" [--since 1h] [--limit 100]
                                                # full-text search the persisted archive
                                                # query is the --q FLAG, NOT positional

# Builds  (NB: a normal `git push`/merge to a tracked branch already deploys —
#          these are for OUT-OF-BAND rebuilds, not the normal ship flow.)
kuso build list <project> <service>             # newest first; status = pending|running|succeeded|failed|release-failed|cancelled
kuso build why <project> <service> [id]         # classified failure cause + suggested fix
kuso build trigger <project> <service>          # manual rebuild of the default branch
kuso redeploy <project> <service>               # alias; --branch <name> or --ref <sha> for a non-tracked ref
kuso build rollback <project> <service> <id>    # re-point production at an older successful build
kuso build cancel <project> <service> <id>      # kill an in-flight build
#   (branch deletes / force-pushes auto-cancel their in-flight builds)

# Env vars & secrets — see "Env vars & secrets" for the env-set-vs-secret-set rule.
kuso env list <project> <service>               # plain vars + names of secret keys
kuso env set <project> <service> KEY=val KEY2=val2     # service-level; multiple K=V OK
kuso env set <project> <service> --env <name> KEY=val  # per-env override (wins over service)
kuso env unset <project> <service> KEY [KEY...]        # --env <name> to drop an override
kuso env share <project> <service> KEY [KEY...]        # subscribe svc to shared-secret keys
kuso env unshare <project> <service> KEY [KEY...]      # unsubscribe
kuso secret list|set|unset <project> <service> ...     # K8s-Secret-backed (KEY VALUE positional)
kuso shared-secret set <project> KEY=value      # ONE pair per call; subscribed services inherit
kuso shared-secret list|unset <project> [KEY]

# Addon subscriptions (least privilege)
kuso project addon list <project> <service>
kuso project addon subscribe|unsubscribe <project> <service> <addon> [addon...]

# Service spec edits (patch-shaped: only passed flags change)
kuso project service set <project> <service> [--port N] [--runtime rt] \
    [--domains h1,h2] [--replicas N] [--max-replicas N] [--branch b] [--path dir] \
    [--internal on|off] [--private-egress on|off] \
    [--cap-add CAP]... [--allow-privilege-escalation on|off]
#   NOT settable here (kuso.yml / PATCH only): release hook, sleep/wakeOn,
#   container command override, dockerfile path (create-time --dockerfile only).

# Crons
kuso cron list <project> [service]
kuso cron add <project> <service> --name N --schedule '*/5 * * * *' --cmd '...'
kuso cron add-command <project> --name N --schedule '...' --image IMG --image-tag TAG --cmd '...'
kuso cron add-http <project> --name N --schedule '...' --url 'https://...'
kuso cron sync <project> <service> <name>       # re-resolve image/env from production
kuso cron edit <project> <name> [--schedule ...] [--suspend ...]
kuso cron delete <project> <service> <name>     # kind=service
kuso cron delete-project <project> <name>       # kind=http|command

# One-shot runs (migrations, seeds, console)
kuso run <project> <service> -- sh -c 'rake db:seed'      # NOTE: -- separator, not --cmd
kuso run <project> <service> --follow -- <cmd>            # -f streams logs + blocks; exit code = the run's
kuso run <project> <service> --env DEBUG=1 --timeout-seconds 600 -- <cmd>
kuso run cancel <project> <run>                           # kill a running one-shot

# Shells + domains
kuso shell <project> <service>                  # exec into a pod (uses local kubectl context)
kuso domains add <project> <service> <host>     # add a custom domain (--no-tls for HTTP-only)
kuso domains remove <project> <service> <host>  # alias: rm
kuso domains list <project> <service>

# Imperative resource creation
kuso project create <name> --repo <url> [--domain <d>] [--branch <b>] [--previews]
kuso project update <name> [--domain <d>] [--previews=on|off] [--previews-ttl <days>] \
       [--previews-domain <base>] [--github-installation <id>] [--always-on on|off]
kuso project addon add <project> <name> --kind <kind> [--version <v>] \
       [--size small|medium|large] [--ha] [--tls disable|require]
kuso project addon update <project> <addon> [--tls ...] [--version ...] [--size ...]
       # NB: version/size/ha/storageSize are rejected as immutable on live addons —
       # migrate via addon-backup instead. --tls flips live (restart consumers after).
kuso project addon placement show|set|clear <project> <addon>   # pin to labeled nodes
kuso project service add <project> <name> --runtime <rt> [--port N] [--path <subdir>] \
       [--dockerfile <rel>] [--replicas N] [--max-replicas N] \
       [--from-service <svc> --command ./worker]
       # runtime: dockerfile | nixpacks | buildpacks | static | worker | image
       # for --runtime image: --image-repo X --image-tag Y (do NOT put X:Y in --image-repo)
kuso environment add <project> <service> <name> --branch <b> [--seed-from <env>] \
       [--addons <kinds>] [--share-addons] [--host <fqdn>]
kuso project delete <name> [--purge-data] [-y]  # cascades services/envs/addons/secrets;
       # PVCs KEPT unless --purge-data (required for a clean delete+recreate — else the
       # recreated postgres inherits the old data dir + password and crashloops on SASL)
kuso project export <name>                      # dump live state as kuso.yaml
kuso github installations                       # find a GitHub App installation id

# Maintenance
kuso doctor                                     # pre-flight checks (incl. webhook health)
kuso version
kuso upgrade --check                            # see if a newer kuso-server is available
kuso upgrade --version vX.Y.Z                   # pin to a specific release
kuso revision list <project> <kind> <name>      # service / project / addon — edit history
kuso revision revert <project> <id>             # replay an old snapshot
kuso token create --name ci --expires 90d       # long-lived API token (printed ONCE)

# Admin-only (settings:admin role)
kuso db sql|tables|columns|rows <p> <addon> ... # read-only SQL browser (postgres + clickhouse)
kuso db connect <project> <addon> [--exec]      # tunnel + client from laptop
kuso db port-forward <project> <addon>          # open local TCP port
# GOTCHA — both `db` tunnels front the kube pods/portforward subresource. If the
# tunnel LISTENS locally but every connection dies with "connection terminated
# unexpectedly", the server tells you the RBAC fix since v0.18.118; when the
# tunnel is unavailable, run DB work IN-CLUSTER via `kuso run` instead.
kuso backup settings get|set / backup health    # addon-backup S3 bucket config
kuso instance-secret list                       # instance-wide shared secrets
kuso role list|create|edit|delete               # RBAC roles
kuso node add-token / pending / list / label    # cluster node bootstrap + labels
kuso instance-config podsize list               # pod-size presets
```

## How a deployment actually flows

```
git push → GitHub webhook → kuso receives push event
  → matches services whose default branch OR a persistent env's tracked branch == pushed branch
  → creates a KusoBuild CR with the commit SHA
  → operator renders a kaniko Job
    → init: clone (with App-installation token if private)
    → init: env-detect (scans repo for ${process.env.X} usages)
    → kaniko: build image, push to in-cluster registry
  → on success: build poller checks for spec.release.command
    → IF release.command set:
        → create <env>-release-<short-tag> Job with the new image + env's envVars/envFromSecrets
        → poll until Complete or Failed (or timeout)
        → on Failed/timeout: mark build release-failed, do NOT promote, fire notify event
        → on Complete: proceed to image promote
    → ELSE: skip directly to image promote
  → patches env.spec.image.tag → operator reconciles → updates Deployment template
  → kube rolls a new ReplicaSet (maxSurge:1, maxUnavailable:0 — zero downtime)
  → old pod terminates once new pod's readinessProbe passes
```

What can go wrong, in rough order of frequency (start with `kuso build why`):

1. **GitHub App not installed on the repo's owner** → clone 404s. Build clones auto-resolve the installation from the repo URL; PR PREVIEWS additionally need the install bound on the project: `kuso project update <p> --github-installation <id>`.
2. **Transient clone failure** → `Could not resolve host: github.com` is usually a momentary DNS blip in the build pod. Just re-trigger.
3. **OOMKilled during kaniko snapshot** → "exit code 137" in the build's failure message. Fix: trim build deps OR raise the build memory limit (Settings → Build resources).
4. **App reads wrong port** → kuso always sets `$PORT` to the service spec's port. Apps that hardcode `3000` while spec says `8080` fail readiness. Fix: bind to `process.env.PORT || 3000`.
5. **App redirects to wrong host on a custom domain** → kuso routes correctly; the app's `NEXTAUTH_URL` / `AUTH_URL` / `APP_URL` is hardcoded to the auto-domain. Fix: `kuso env set` then `kuso redeploy`.
6. **CrashLoopBackOff with no logs** → readiness/liveness probe failing before the app prints. Tail with `kuso logs -f`; the previous pod's last 200 lines persist in the archive even after pod GC. Also check `kuso service errors`.
7. **`release-failed` → new pods never come up** → the release hook (migration) blocked promote BY DESIGN; the env keeps its last GREEN image. `kuso build why` + fix the migration + re-trigger.
8. **`InvalidImageName` / pod image `:latest`** → the env's `spec.image` is empty (never promoted). Causes: a release-failed build (see #7), or a recreated preview env whose terminal build didn't re-promote (self-heals on v0.17.25+). Fix: re-trigger the build.
9. **Exit 127 right at container start (marketplace-style images)** → image drops root itself (setpriv/gosu) but kuso strips capabilities. Fix: `--cap-add SETUID --cap-add SETGID --allow-privilege-escalation on` (see "Marketplace").

## Debugging a misbehaving service — the standard playbook

```bash
# 1. What does kuso think is running?
kuso status <project>
kuso service pods <project> <service>           # pods + phases for the env

# 2. Latest build — succeeded? Failed with what? Let kuso classify it.
kuso build list <project> <service>
kuso build why <project> <service>              # cause + suggested fix

# 3. Live logs.
kuso logs <project> <service> -f

# 4. Aggregated recent errors + archive search (NOTE the --q flag).
kuso service errors <project> <service> --since 6h
kuso logs search <project> <service> --q "ECONNREFUSED" --since 24h

# 5. Env vars — is what you expect actually set? Any drift?
kuso env list <project> <service>
kuso service drift <project> <service>

# 6. Pop a shell to poke around. Needs local kubectl context.
kuso shell <project> <service>

# 7. Force a fresh build + roll.
kuso redeploy <project> <service>
```

## Editing safely — what's hot-swappable vs. what triggers a rollout

| Change                         | Effect                                           |
| ------------------------------ | ------------------------------------------------ |
| `env set KEY=...`              | Rolls a new pod (envVars are part of pod spec).  |
| `env set --env <n> KEY=...`    | Rolls only that one env's pod.                   |
| `env share/unshare KEY`        | Rolls the service's pod(s) (changes mounted secrets). |
| `shared-secret set KEY=...`    | Rolls every SUBSCRIBED service in the project.   |
| `addon subscribe/unsubscribe`  | Rolls that service's pod(s).                     |
| `domains add <host>`           | Live — Ingress + LE cert mint, no pod restart.   |
| `domains remove <host>`        | Live — Ingress update only.                      |
| `service set --port/...`       | Rolls a new pod when the field is in the template.|
| `release` block change         | Takes effect at NEXT deploy. Existing pods unaffected. |
| `wakeOn.excludePaths` change   | Re-propagates to env's replicaCount on next save.  |
| `addon update --tls`           | Addon pod restarts; consumers keep stale sslmode until THEY restart. |
| Addon password rotation        | Existing pods keep old creds until they restart. |
| `command` override (v0.18+)    | Rolls a new pod with the container CMD replaced.  |

**Container command override (v0.18+):** any runtime (not just `worker`)
can override the image's default CMD via `spec.command`. Use it when an
image bundles several processes that contend for the same `PORT` — point
the command at the single process kuso should serve. `worker` runtime has
`--command` on `service add`; for other runtimes it's kuso.yml/API-only:
`PATCH /api/projects/<p>/services/<s>` with `{"command":["node","server.js"]}`.

Only edit production env-vars when you mean to. The web UI shows a **Diff Confirm** modal before applying; the CLI applies immediately. Every spec edit is snapshotted — `kuso revision list/revert` undoes a bad one.

## When NOT to use kuso

- You need to inspect a non-kuso pod or raw cluster state → `kubectl` is fine, but you'll need a kubeconfig pointing at the cluster (which the user typically does NOT have on their dev machine).
- You're debugging the operator itself → `ssh` to the cluster + `kubectl logs -n kuso-operator-system deploy/kuso-operator-controller-manager`.
- A feature has no CLI verb yet (release hooks, cron `onFailure`, `sleep.wakeOn`, non-worker `command` override) → set it in `kuso.yml` + `kuso apply`, or `curl` the REST API with the bearer token (`$(awk '{print $2}' ~/.kuso/credentials.yaml)`), as shown in those sections. That's the sanctioned path, not a workaround.

For everything else — **reach for `kuso`**. If a CLI command fails or returns confusing output, that's a real bug; don't paper over it with raw `kubectl`.

## kuso.yml shape (reference only — prefer the imperative path)

```yaml
project: my-product
baseDomain: my-product.example.com
defaultRepo:
  url: https://github.com/me/my-product
  defaultBranch: main

services:
  - name: web
    runtime: dockerfile
    port: 3000
    domains: [{ host: my-product.com, tls: true }]
    # NO need to set DATABASE_URL etc. here — addons auto-inject.
    envVars:
      NODE_ENV: production
    scale: { min: 1, max: 5, targetCPU: 70 }
    # NOTE: per-service subscriptions (subscribedAddons / sharedEnvKeys) are
    # NOT expressible in kuso.yml — use `kuso env share/unshare` and
    # `kuso project addon subscribe/unsubscribe` after apply.

  - name: api
    runtime: dockerfile
    port: 8080
    # v0.16+ release hook — runs as a Job before image promote
    release:
      command: [./bin/migrate]
      timeoutSeconds: 600
    sleep:
      enabled: true
      wakeOn:
        excludePaths: [/api/v1/payments/notify]
    scale: { min: 0, max: 5, targetCPU: 70 }

addons:
  - name: db
    kind: postgres
    version: "16"
    size: small
    tls: require          # v0.18.123+ — postgres wire TLS (default: disable)
    backup:
      schedule: "0 3 * * *"
      retentionDays: 14
  - name: cache
    kind: redis
  - name: queue
    kind: nats
    ha: true              # v0.15+ — 3-replica clustered JetStream

  # External addon (PlanetScale / Neon / RDS / Hetzner Cloud)
  - name: prod-db
    kind: postgres
    external:
      secretName: tickero-planetscale   # user creates this with DATABASE_URL
    backup:
      schedule: "0 3 * * *"
      retentionDays: 14
```

## Quick reference card

```text
get projects                  list every project
status <p>                    project rollup (services, URLs, replicas, builds)
logs <p> <s> [-f]             tail or stream pod logs (--build <id> for build logs)
logs search <p> <s> --q "..." search persisted archive (note: --q FLAG)
service errors|pods|drift     aggregated errors / pod list / spec-vs-live drift
# NB: push/merge to a tracked branch AUTO-deploys — the build cmds below are out-of-band.
build list <p> <s>            build history; status incl release-failed, cancelled
build why <p> <s>             classified failure cause + suggested fix
build trigger <p> <s>         MANUAL rebuild w/o a new commit (runs release hook)
build rollback <p> <s> <id>   re-point production at older successful build
build cancel <p> <s> <id>     kill an in-flight build
redeploy <p> <s>              same as build trigger; --branch / --ref for other refs
run <p> <s> -- cmd…           one-shot Job (NOTE: -- separator); run cancel to kill
shell <p> <s>                 kubectl exec into a pod
env list/set/unset            plain vars (K=V; --env <n> per-env). DEFAULT for most vars.
env share/unshare <p> <s> K   subscribe/unsubscribe service to shared-secret keys
secret list/set/unset         K8s-Secret-backed (KEY VALUE positional) — provider secrets only
shared-secret list/set/unset  project-level vars (ONE K=V/set; subscribed services inherit)
project addon subscribe/unsubscribe/list   per-service addon mounts (least privilege)
domains add/remove/list       custom hostnames (--env <n> to scope; else mirrors to production)
get addons <p>                addons + their conn-secret names
environment add <p> <s> <n> --branch <b>   long-lived env tracking a branch
addon-backup download|list|restore|schedule    addon data backups
cron list/add/add-http/add-command/sync/edit/delete[-project]
marketplace list/info/deploy  one-click apps (gitea, n8n, uptime-kuma, ...)
import compose <file>         convert docker-compose (dry-run by default)
project create --repo --domain
project addon add --kind [--tls require]
project service add --runtime [image|dockerfile|nixpacks|buildpacks|static|worker]
project service set           patch port/domains/replicas/branch/caps on a live service
project service stop|start    hard-stop (no wake-on-traffic) / resume
project delete <p>            cascades to services/envs/addons
health [fix <r>]              reconcile health + one-click remediation
doctor                        pre-flight checks
backup --output <file>        control-plane pg_dump; backup settings/health for addon S3
upgrade --check / --version vX.Y.Z
version
```

When in doubt: `kuso <command> --help` always works and always has examples.
