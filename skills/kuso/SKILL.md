---
name: kuso
description: Use when working in a project deployed to kuso (a self-hosted Kubernetes PaaS). Explains the kuso CLI, how deployments work, how to handle env vars & secrets (always `env set`/`shared-secret`, never `secret set`; per-env overrides, `${{ }}` addon aliases, least-privilege subscriptions), preview/PR environments, base + custom domains, release hooks, debugging builds/sleeping pods, and the v0.16+/v0.17+ features. Invoke whenever the user mentions deploys, builds, logs, env vars, secrets, addons (postgres/redis/etc.), subscriptions, preview/PR envs, domains, release hooks, migrations, sleeping pods, callback webhooks, or anything related to their kuso instance.
allowed-tools: Bash(kuso:*), Bash(curl:*), Bash(awk:*), Bash(ssh:*), Read, Edit, Write, Grep, Glob
---

# kuso — operating a project on this PaaS

This project is deployed via [kuso](https://github.com/sislelabs/kuso), a self-hosted Kubernetes PaaS. The user has a `kuso` CLI on their PATH and a logged-in session against their instance. **Always drive operations through `kuso`, not raw `kubectl`** — the CLI exercises the same auth/tenancy/perm layers users hit, so what you see is what they see.

This skill is current to **v0.17.26**. Run `kuso version` to confirm what's on the user's machine.

> **Env vars & secrets — the one rule that overrides everything:** set EVERY
> variable (sensitive or not) through `kuso env set` (service-level) or
> `kuso shared-secret set` (project-level). **Do NOT use `kuso secret set`** —
> it's a legacy per-env Secret escape hatch that's invisible in the Variables
> tab, the rendered spec, and the audit trail. Full rules in the
> "Env vars & secrets" section below — read it before touching any variable.

## Mental model — read this first

- **Project** = the top-level grouping. One repo or many; one base domain.
- **Service** = one deployable app inside a project. Has a runtime, a port, and env vars.
- **Environment** = one running instance of a service. Each service auto-gets a `production` env. PR previews + named clones (`staging`, `client-demo`) are extra envs.
- **Addon** = a managed datastore. Each addon writes a `<project>-<addon>-conn` Secret that kuso injects into a service via `envFromSecrets` — you do NOT wire `DATABASE_URL` etc. by hand; they appear in `process.env`. By default (legacy / `subscribedAddons` unset) every addon mounts into every service. Set a per-service subscription so a public frontend doesn't carry `DATABASE_URL`/`REDIS_URL` — see "Env vars & secrets".
- **Build** = a kaniko Job that produces an image and patches the env's `image.tag`. One build per `(service, ref)`. Helm-operator rolls the new pod.
- **Release hook** (v0.16+) = an optional Job that runs **before** the new image is promoted. Heroku-style migration phase. Set via `spec.release.command`.
- **kuso.yml** = optional config-as-code at repo root. **See "Config-as-code caveats" below before using `kuso apply`.**

The CLI is rooted at `kuso <command>`. Run `kuso <command> --help` whenever shape is unclear — every command has examples.

## Two flag conventions — learn the difference

| Command | Command argv syntax |
|---|---|
| `cron add` / `cron add-command` / `cron add-http` | `--cmd '<shell string>'` flag |
| `run` | `--` separator: `kuso run <p> <s> -- sh -c '...'` |
| `env set` | `KEY=VALUE` (multiple per command); `--env <name>` scopes to one environment |
| `env unset` | `KEY [KEY ...]`; `--env <name>` for a per-env override |
| `env share` / `env unshare` | `<p> <s> KEY [KEY ...]` — subscribe/unsubscribe a service to project/instance shared-secret keys |
| `shared-secret set` | `KEY=value` (ONE pair per call — `accepts 2 arg(s)` if you pass more) |
| `secret set` | **legacy — don't use.** 4 positional args `<p> <s> KEY VALUE`; use `env set` instead |

This inconsistency is real. When you get `Error: accepts N arg(s), received M`, you've hit the wrong convention.

> **CLI gotcha (≤ v0.17.19):** `env unset/share/unshare` with MULTIPLE keys
> historically acted on only the FIRST. Fixed in v0.17.20+, but if the user's
> CLI is older, loop one key per call. `kuso version` to check; upgrade with
> the install-cli one-liner.

## First-time setup

```bash
# Verify session — token, DNS, server reachability, auth.
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
# Other supported kinds: mailpit, nats, meilisearch, clickhouse,
# mongodb, mysql, rabbitmq, memcached, elasticsearch, kafka,
# cockroachdb, couchdb. Check what your CLI build supports with:
#   kuso project addon add --help

# 3. Service from a repo (default: build via dockerfile)
kuso project service add papelito web \
  --runtime dockerfile --port 3000

# 3b. OR: service from a pre-built registry image (no kaniko build)
#     --image-repo + --image-tag are SEPARATE; don't put X:Y in --image-repo
kuso project service add papelito web \
  --runtime image \
  --image-repo ghcr.io/sislelabs/papelito \
  --image-tag v1.2.3 \
  --port 3000

# 4. Domains
kuso domains add papelito web papelito.example.com

# 5. Env vars — ALWAYS `kuso env set` (sensitive or not). NEVER `secret set`.
kuso env set papelito web NODE_ENV=production NEXT_TELEMETRY_DISABLED=1
kuso env set papelito web RESEND_API_KEY=re_xxx STRIPE_SECRET_KEY=sk_live_xxx
# Values shared across services → project-level shared secret (one K=V/call):
kuso shared-secret set papelito JWT_SECRET=...        # subscribed services inherit it
# Addon-conn key whose NAME differs from what your app reads → ${{ }} alias:
kuso env set papelito web 'S3_ACCESS_KEY=${{ storage.S3_ACCESS_KEY_ID }}'

# 5b. Least privilege: trim a public frontend to no addons + no secrets.
kuso env share papelito web ENVIRONMENT                # only this shared key
TOKEN=$(awk '{print $2}' ~/.kuso/credentials.yaml)     # addons: PUT (no CLI verb)
curl -fsS -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"addons":[]}' https://kuso.<domain>/api/projects/papelito/services/web/subscribed-addons

# 6. Trigger first build (only needed for repo-based runtimes)
kuso build trigger papelito web

# Watch it
kuso logs papelito web -f
kuso status papelito
```

This imperative path is **the safe one**. Use it unless you have a specific reason to prefer config-as-code.

## Release hooks (v0.16+) — migrations the right way

The footgun this replaces: people stuff `migrate up && exec /app/api` into the API's entrypoint. With ≥2 replicas, both pods race the migration; with a long migration, the readiness probe fails before it finishes and the deploy thrashes.

`spec.release.command` runs as a **separate Job against the NEW build's image** before the image tag is promoted to the env. On non-zero exit, the build is marked `release-failed`, the image is NOT promoted, and existing pods keep running on the previous image. A `build.failed` notify event fires.

Configure via PATCH (or `kuso.yml`'s `services[].release`):

```bash
# Set the release hook
curl -X PATCH -H "Authorization: Bearer $(awk '{print $2}' ~/.kuso/credentials.yaml)" \
  -H "Content-Type: application/json" \
  -d '{"release":{"command":["./bin/migrate"],"timeoutSeconds":600}}' \
  https://kuso.example.com/api/projects/tickero/services/api

# Trigger a build — the release Job fires automatically before promote
kuso build trigger tickero api

# Inspect the release Job logs after the fact
ssh -i ~/.ssh/keys/hetzner root@kuso.example.com \
  "kubectl logs -n kuso job/<env-name>-release-<short-tag>"
```

Job naming: `<env-name>-release-<short-image-tag>`. Re-deploying the same tag is a no-op (Job exists, already succeeded). Job runs with the env's effective envVars + envFromSecrets, so `DATABASE_URL` etc. are available.

To clear the hook: PATCH `{"release":{"clear":true}}`.

## Sleep wakeOn excludePaths (v0.16+) — keep callback paths warm

The problem: ePay.bg / Stripe / GitHub webhooks have short retry timeouts. If your service has scale-to-zero on (`scale.min=0`), a cold-start can exceed the sender's retry window → duplicate or late deliveries.

`spec.sleep.wakeOn.excludePaths` is the "this deployment MUST stay reachable" signal. When set, the deployment stays at min 1 even when `scale.min=0`.

```bash
curl -X PATCH ... \
  -d '{"scale":{"min":0,"max":3,"targetCPU":70},
       "sleep":{"enabled":true,"wakeOn":{"excludePaths":["/api/v1/payments/notify"]}}}' \
  https://kuso.example.com/api/projects/tickero/services/api
```

**Semantic:** whole-deployment, not per-path routing. If any path matters, the whole deployment stays warm. Kube can't route per-path inside one Deployment without extra ingress plumbing. For per-path isolation, split into two services.

Clear with `{"sleep":{"wakeOn":{"clear":true}}}`.

## Cron failure webhooks (v0.16+)

KusoCrons can POST an HMAC-signed payload to a webhook when they fail. Useful for refund-deadline sweeps, voucher expiry, payout retries — anything where silent cron failure is a revenue leak.

```bash
# 1. Create the cron normally
kuso cron add-command tickero \
  --name refund-deadline-sweep \
  --schedule '0 * * * *' \
  --image ghcr.io/yourorg/api \
  --image-tag v1.2.3 \
  --cmd '/app/bin/sweep-refunds'

# 2. Attach the onFailure webhook (no CLI yet — kubectl-patch via API)
curl -X PATCH ... \
  -d '{"onFailure":{"webhookURL":"https://hooks.slack.com/services/...",
                    "secretRef":{"name":"tickero-slack-conn","key":"signing-secret"}}}' \
  https://kuso.example.com/api/projects/tickero/crons/refund-deadline-sweep
```

The watcher polls cluster-wide Jobs labeled `kuso.sislelabs.com/cron` every 30s. On terminal Failed status, it POSTs:

```json
{
  "project": "tickero",
  "service": "tickero-api",
  "cron": "tickero-refund-deadline-sweep",
  "jobName": "tickero-refund-deadline-sweep-29664488",
  "startedAt": "2026-05-27T08:08:00Z",
  "finishedAt": "2026-05-27T08:08:31Z",
  "logsURL": "https://kuso.example.com/projects/..."
}
```

With `X-Kuso-Signature: sha256=<hex>` when `secretRef` is set. Retries 3x with linear backoff (0s, 1s, 4s). Cluster-singleton — duplicate alerts won't fire from multiple replicas.

Also emits `cron.failed` to notify subscribers — make sure your Discord/Slack channel subscribes to it (it's a v0.16+ event so existing channels need the subscription added).

## External-DB backups (v0.16+) — PlanetScale / Neon / Supabase / RDS

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

The user must create the source Secret with `DATABASE_URL=postgres://...` and configure the cluster-wide `kuso-backup-s3` bucket (Settings → Backups in the UI, or `kubectl create secret generic kuso-backup-s3` with keys `bucket`, `endpoint`, `accessKeyId`, `secretAccessKey`, `region`). Without the `kuso-backup-s3` secret, the CronJob installs but every run fails — `kuso addon-backup list` will tell you.

Uses `pg_dump --no-owner --no-acl` which is what PlanetScale / Neon / Supabase recommend (managed providers strip GRANT/REVOKE you can't recreate on restore).

## Config-as-code caveats — `kuso apply`

`kuso apply` reads `kuso.yml` and reconciles it against the live project. **Known sharp edges:**

- The plan's `addonsToDelete` will list addons from OTHER projects under the same namespace if your kuso install is from before the addon-scoping fix landed. **If `--dry-run` shows deletes against addons you didn't author, STOP — running it will destroy other tenants' data.** Use the imperative path instead until the user confirms their server is patched.
- `--dry-run` prints the plan but doesn't write. Always run with `--dry-run` first; eyeball every `delete` line before running without it.
- A misspelled addon name in `addons:` looks identical to "user wants the live addon deleted." Plan diffs are merciless.

```bash
kuso init --project myproj --runtime dockerfile --port 8080
# edit kuso.yml
kuso apply --dry-run        # always first
# Read every line. Confirm only your project's resources appear.
kuso apply                  # only after the dry-run is clean
```

## Env vars & secrets — the complete model

There are FOUR places a variable can live. Pick by scope; the write command is always `env set` / `shared-secret set` (never `secret set`).

| Where | Set with | Scope |
|---|---|---|
| Service-level | `kuso env set <p> <svc> KEY=val` | all envs of one service (propagates to production + previews) |
| Per-env override | `kuso env set <p> <svc> --env <name> KEY=val` | ONE env only; wins over the service-level value for that key |
| Project shared | `kuso shared-secret set <p> KEY=value` | every service that subscribes (see subscriptions) |
| Addon-injected | (automatic) | the `<project>-<addon>-conn` Secret, mounted per subscription |

**The hard rule: NEVER `kuso secret set`.** It writes a per-env kube Secret via `envFromSecrets` that's invisible in the Variables tab, the rendered service spec, `kuso env list`, and the audit/revision history. Even highly-sensitive values (JWT secret, payment keys, API tokens) go through `kuso env set` / `kuso shared-secret set` so the user sees them in the UI and the audit trail captures them. The ONLY time you touch `secret set` is migrating OFF legacy per-env Secrets — and the target is `env set`.

### Addon connection secrets — auto-injected, but mind the key NAMES

Add a postgres addon `db` → kuso writes `<project>-db-conn` and mounts it. Keys land on the pod automatically; you do NOT set `DATABASE_URL: ${{ db.DATABASE_URL }}`.

| Addon kind | Keys it injects (verify per-instance: `kuso get addons <p> -o json`) |
|---|---|
| `postgres` | `DATABASE_URL`, `POSTGRES_HOST/PORT/USER/PASSWORD/DB`, `POOLER_*` |
| `redis`    | `REDIS_URL`, `REDIS_HOST/PORT/PASSWORD` |
| `s3`       | `S3_ENDPOINT`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`, `S3_BUCKET`, `S3_REGION`, `AWS_*` |
| `nats`     | `NATS_URL`, `NATS_HOST/PORT/TOKEN`, `NATS_MONITOR_URL` |
| `mailpit`  | `SMTP_HOST/PORT`, `MAIL_*` |

**Key-name mismatch is the #1 footgun.** kuso injects `S3_ACCESS_KEY_ID` but your app may read `S3_ACCESS_KEY`; kuso injects `DATABASE_URL` but you also want a read-replica `DATABASE_READ_URL`. Alias with `${{ <addon>.<KEY> }}`:

```bash
kuso env set <p> api 'S3_ACCESS_KEY=${{ storage.S3_ACCESS_KEY_ID }}'
kuso env set <p> api 'S3_SECRET_KEY=${{ storage.S3_SECRET_ACCESS_KEY }}'
kuso env set <p> api 'DATABASE_READ_URL=${{ db.DATABASE_URL }}'
```

### `${{ ... }}` reference syntax

The `${{ ... }}` must be the ENTIRE value (no `prefix-${{ ... }}-suffix`).

1. **Addon key (rename/alias)** — `${{ <addon-name>.<KEY> }}` → a `secretKeyRef` into `<project>-<addon>-conn`.
2. **Service-to-service URL** — `${{ api.URL }}` → `http://<project>-api-<env>.<ns>.svc.cluster.local:<port>` (in-cluster, resolves per-env). `${{ api.HOST }}`, `${{ api.PORT }}` for the parts. Use this for SERVER-SIDE calls (a Next.js app's `API_URL`); the browser-facing `NEXT_PUBLIC_API_URL` must stay the public https URL.

### Subscriptions — least privilege (don't leak DB creds into a frontend)

By default every shared-secret key and every addon mounts into every service. Lock a service down to only what it needs:

```bash
# Shared-secret keys: env share/unshare. After trimming, verify with the UI
# or the service spec's sharedEnvKeys — the CLI count messages can mislead.
kuso env unshare <p> frontend JWT_SECRET TICKET_SIGNING_SECRET EPAY_SECRET ...
kuso env share   <p> frontend ENVIRONMENT          # frontend keeps only this

# Addon subscriptions: PUT subscribed-addons (no CLI verb yet). [] = none.
TOKEN=$(awk '{print $2}' ~/.kuso/credentials.yaml)
curl -fsS -X PUT -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"addons":[]}' \
  https://kuso.<domain>/api/projects/<p>/services/frontend/subscribed-addons
# backoffice that only needs S3: '{"addons":["storage"]}'
```

Empty `[]` means "subscribe to nothing" (works on v0.17.17+ — earlier, an empty
list silently reverted to mount-all). A public Next.js frontend should have
`sharedEnvKeys=[ENVIRONMENT]` and `subscribedAddons=[]` — no JWT/ePay secrets,
no DB/Redis/NATS conns.

### Validation gotchas (the app refuses to boot if these are wrong)

- Prod apps often reject `sslmode=disable` and non-https URLs. kuso's managed
  postgres `DATABASE_URL` already uses `sslmode=require`.
- A migration release hook needs the addon ready; that's handled by the release
  Job's wait-for-addons step (v0.17.x). For `kuso run`-style one-shots against a
  just-created addon, wrap in a retry: `sh -c 'for i in $(seq 1 30); do nc -z -w2 <addon-host> 5432 && exec ./cmd; sleep 2; done'`.

## Base domain & custom domains

- `kuso project create <p> --domain <base>` / `kuso project update <p> --domain <base>`
  sets the project base domain. Each service's auto-host is `<svc>.<base>`
  (the service whose short name == project gets the apex `<base>`). Changing it
  rewrites every env still on the old default host and re-mints certs (v0.17.26+;
  earlier it silently no-op'd).
- **Custom domains** (`tickero.bg`, `api.tickero.bg`) are added with
  `kuso domains add <p> <svc> <host>`. They land as the env's `additionalHosts`
  and get their own LE cert. `kuso domains add/rm/list --env <name>` scopes to one
  env; without `--env` the host is mirrored onto the PRODUCTION env. DNS must
  already point at the cluster IP — kuso doesn't manage your registrar.
- A service can serve on its auto-host AND its custom hosts simultaneously
  (all in `tlsHosts`). Make the base domain your real domain (`--domain tickero.bg`)
  so the primary host is `<svc>.tickero.bg` rather than `<svc>.<cluster-base>`.

## Preview (PR) environments

- Enable: `kuso project update <p> --previews=on --github-installation <id>`
  (find the install id with `kuso github installations`; the GitHub App must be
  installed on the repo's org). Auto-expire: `--previews-ttl <days>`.
- On PR open/reopen/sync kuso spawns `<svc>-pr-<N>` envs (+ a cloned, seeded,
  isolated preview DB `db-pr-N`), builds from the PR branch, and tears them down
  on close/merge. Previews are pinned to 1 replica, no autoscaling.
- **Preview host base**: `kuso project update <p> --previews-domain <base>` makes
  preview hosts `<svc>-pr-N.<base>` (e.g. `frontend-pr-35.tickero.bg`) instead of
  the cluster base. Needs wildcard DNS for `*.<base>`.
- Previews respect each service's subscriptions — a `subscribedAddons=[]` frontend
  preview correctly carries no addon conns; only db-subscribers get the `db-pr-N`
  clone (never production, never non-subscribers).
- **Don't close/reopen a PR in a tight loop.** It recreates the envs; on v0.17.25+
  kuso self-heals (re-stamps the already-built image), but right after a server
  upgrade give the new pod ~60s to be the sole Running one before testing.

## The commands you'll actually use

```bash
# Where am I? What's running?
kuso get projects [-o json]                     # all projects
kuso status <project>                           # rollup: services, URLs, replicas, latest build
kuso get services <project> [-o json]           # service specs
kuso get addons <project> [-o json]             # addons + connection-secret names

# Logs
kuso logs <project> <service>                   # last 200 lines
kuso logs <project> <service> -f                # tail (^C to stop)
kuso logs <project> <service> --env <env>       # non-prod env (preview-pr-N, staging, etc.)
kuso logs <project> <service> --lines 1000      # bigger tail
kuso logs search <project> [service] --q "<query>" [--since 1h] [--limit 100]
                                                # full-text search the persisted archive
                                                # query is the --q FLAG, NOT positional

# Builds
kuso build list <project> <service>             # newest first; status = pending|running|succeeded|failed|release-failed
kuso build trigger <project> <service>          # build the project's default branch
kuso redeploy <project> <service>               # alias; --branch <name> or --ref <sha>
kuso build rollback <project> <service> <id>    # re-point production at an older successful build

# Env vars — ALWAYS env set (sensitive or not). NEVER `kuso secret set`.
kuso env list <project> <service>               # plain vars + names of secret keys
kuso env set <project> <service> KEY=val KEY2=val2     # service-level; multiple K=V OK
kuso env set <project> <service> --env <name> KEY=val  # per-env override (wins over service)
kuso env unset <project> <service> KEY [KEY...]        # --env <name> to drop an override
kuso env share <project> <service> KEY [KEY...]        # subscribe svc to shared-secret keys
kuso env unshare <project> <service> KEY [KEY...]      # unsubscribe

# Shared (project-level) secrets — inherited by every SUBSCRIBED service
kuso shared-secret set <project> KEY=value      # ONE pair per call
kuso shared-secret list <project>
kuso shared-secret unset <project> KEY

# Per-service addon subscription (no CLI verb yet — PUT the endpoint):
#   PUT /api/projects/<p>/services/<svc>/subscribed-addons  {"addons":["storage"]}

# Crons
kuso cron list <project>                                  # all crons in project
kuso cron add <project> <service> --name N --schedule '*/5 * * * *' --cmd '...'
kuso cron add-command <project> --name N --schedule '...' --image IMG --image-tag TAG --cmd '...'
kuso cron add-http <project> --name N --schedule '...' --url 'https://...'
kuso cron delete-project <project> <name>                 # for kind=http and kind=command
kuso cron delete <project> <service> <name>               # for kind=service

# One-shot runs (migrations, seeds, console)
kuso run <project> <service> -- sh -c 'rake db:seed'      # NOTE: -- separator, not --cmd

# Shells + addons + domains
kuso shell <project> <service>                  # exec into a pod (uses local kubectl context)
kuso domains add <project> <service> <host>     # add a custom domain
kuso domains rm  <project> <service> <host>     # remove
kuso domains list <project> <service>

# Imperative resource creation
kuso project create <name> --repo <url> [--domain <d>] [--branch <b>] [--previews]
kuso project update <name> [--domain <d>] [--previews=on|off] [--previews-ttl <days>] \
       [--previews-domain <base>] [--github-installation <id>]  # patch project fields
kuso project addon add <project> <name> --kind <kind> [--version <v>] [--size small|medium|large] [--ha]
kuso project service add <project> <name> --runtime <rt> [--port N] [--path <subdir>] \
       [--replicas N] [--max-replicas N] [--from-service <svc> --command ./worker]
       # runtime: dockerfile | nixpacks | buildpacks | static | worker | image
       # --path = monorepo subdir (build context + Dockerfile location)
       # for --runtime image: --image-repo X --image-tag Y (do NOT put X:Y in --image-repo)
       # runtime=worker reusing a sibling's image: --from-service api --command ./worker
kuso project delete <name> [--purge-data] [-y]  # cascades services/envs/addons/secrets;
       # PVCs KEPT unless --purge-data (required for a clean delete+recreate — else the
       # recreated postgres inherits the old data dir + password and crashloops on SASL)
kuso github installations                       # find a GitHub App installation id

# Maintenance
kuso doctor                                     # pre-flight checks
kuso version
kuso upgrade --check                            # see if a newer kuso-server is available
kuso upgrade --version vX.Y.Z                   # pin to a specific release
kuso backup --output kuso-backup-$(date +%s).sql.gz   # control-plane DB dump
kuso revision list <project> <kind> <name>      # service / project / addon — see edit history
kuso token list                                 # API tokens

# Admin-only (settings:admin role)
kuso db connect <project> <addon>               # tunnel to addon DB from laptop
kuso db port-forward <project> <addon>          # open local TCP port
kuso addon-backup list <project> <addon>        # list S3-stored addon dumps
kuso instance-secret list                       # instance-wide shared secrets
kuso node add-token / pending / revoke          # cluster node bootstrap tokens
```

## How a deployment actually flows

```
git push → GitHub webhook → kuso receives push event
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

What can go wrong, in rough order of frequency:

1. **GitHub App not installed on the repo's owner** → clone 404s. Build clones auto-resolve the installation from the repo URL; PR PREVIEWS additionally need the install bound on the project: `kuso project update <p> --github-installation <id>` (`kuso github installations` lists ids).
2. **Transient clone failure** → `fatal: unable to access … Could not resolve host: github.com` is usually a momentary DNS blip in the build pod, not a real error. Just re-trigger the build.
3. **OOMKilled during kaniko snapshot** → "container exited with code 137" on the build's failure message. Fix: trim build deps OR raise the build memory limit in **Settings → Build resources**.
4. **App reads wrong port** → kuso always sets `$PORT` to the service spec's port. Apps that hardcode `3000` while spec says `8080` fail readiness. Fix: bind to `process.env.PORT || 3000`.
5. **App redirects to wrong host on a custom domain** → kuso routes the host correctly; the app's `NEXTAUTH_URL` / `AUTH_URL` / `APP_URL` is hardcoded to the auto-domain. Fix: update that env var with `kuso env set` then `kuso redeploy`.
6. **CrashLoopBackOff with no logs** → readiness/liveness probe failing before app prints. Tail with `kuso logs <p> <s> -f`; the previous pod's last 200 lines are persisted in the BuildLog table even after pod GC.
7. **`release-failed` → new pods never come up** → `kuso build list` shows `release-failed`: the release hook (migration) blocked promote, so the env keeps its old (or no) image. Inspect the release Job logs; fix the migration; re-trigger.
8. **`InvalidImageName` / pod image `:latest`** → the env's `spec.image` is empty (never promoted). Causes: a release-failed build (see #7), or a recreated preview env whose terminal build didn't re-promote (self-heals on v0.17.25+). Fix: re-trigger the build (or for an old preview, delete the stale `<project>-<svc>-<sha>` KusoBuild then reopen the PR).

## Debugging a misbehaving service — the standard playbook

```bash
# 1. What does kuso think is running?
kuso status <project>
kuso get services <project> -o json | jq '.[] | select(.metadata.name=="<svc-fqn>")'

# 2. Latest build — succeeded? Failed with what?
kuso build list <project> <service>
# Modern builds include the actual reason in the message
# (e.g. "OOMKilled — build hit memory limit (exit 137)" or
# "fatal: repository not found" or "Job has reached the specified
# backoff limit" for release-failed).

# 3. Live logs.
kuso logs <project> <service> -f

# 4. Search the archive for an old error. NOTE the --q flag (NOT positional).
kuso logs search <project> <service> --q "ECONNREFUSED" --since 24h

# 5. Pop a shell to poke around. Needs local kubectl context.
kuso shell <project> <service>

# 6. Env vars — is what you expect actually set?
kuso env list <project> <service>

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
| `domains add <host>`           | Live — Ingress + LE cert mint, no pod restart.   |
| `domains rm <host>`            | Live — Ingress update only.                      |
| Service spec patch (port etc.) | Rolls a new pod when the field is in the template.|
| `release` block change         | Takes effect at NEXT deploy. Existing pods unaffected. |
| `wakeOn.excludePaths` change   | Re-propagates to env's replicaCount on next save.  |
| Addon password rotation        | Existing pods keep old creds until they restart. |

Only edit production env-vars when you mean to. The web UI shows a **Diff Confirm** modal before applying; the CLI applies immediately — use `kuso apply --dry-run` shapes when in doubt.

## When NOT to use kuso

- You need to inspect a non-kuso pod or raw cluster state → `kubectl` is fine, but you'll need a kubeconfig pointing at the cluster (which the user typically does NOT have on their dev machine).
- You're debugging the operator itself → `ssh` to the cluster + `kubectl logs -n kuso-operator-system deploy/kuso-operator-controller-manager`.
- A feature has no CLI verb yet (release hooks, cron `onFailure`, per-service addon subscriptions) → `curl` the REST API with the bearer token (`$(awk '{print $2}' ~/.kuso/credentials.yaml)`), as shown in those sections. That's the sanctioned path, not a workaround.

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
    # NOT expressible in kuso.yml — set them with `kuso env share/unshare`
    # and the subscribed-addons PUT API after apply (see "Env vars & secrets").

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
logs <p> <s> [-f]             tail or stream pod logs
logs search <p> <s> --q "..." search persisted archive (note: --q FLAG)
build list <p> <s>            build history; status incl release-failed
build trigger <p> <s>         manual build (runs release hook if configured)
build rollback <p> <s> <id>   re-point production at older successful build
redeploy <p> <s>              same as build trigger; --branch / --ref
run <p> <s> -- cmd…           one-shot Job (NOTE: -- separator)
shell <p> <s>                 kubectl exec into a pod
env list/set/unset            env vars (K=V; --env <n> for per-env override). USE FOR ALL VARS.
env share/unshare <p> <s> K   subscribe/unsubscribe service to shared-secret keys
shared-secret list/set/unset  project-level vars (ONE K=V/set; subscribed services inherit)
# (do NOT use `kuso secret set` — legacy; use env set. addon subscription via PUT API.)
domains add/rm/list <p> <s>   custom hostnames (--env <n> to scope; else mirrors to production)
get addons <p>                addons + their conn-secret names
cron list/add/add-http/add-command/delete[-project]
project create --repo --domain
project addon add --kind
project service add --runtime [image|dockerfile|nixpacks|buildpacks|static|worker]
project delete <p>            cascades to services/envs/addons
doctor                        pre-flight checks
backup --output <file>        control-plane pg_dump
upgrade --check / --version vX.Y.Z
version
```

When in doubt: `kuso <command> --help` always works and always has examples.
