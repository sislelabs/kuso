---
name: kuso
description: Use when working in a project deployed to kuso (a self-hosted Kubernetes PaaS). Explains the kuso CLI, how deployments work, how to debug a failed build or a sleeping pod, the env-var reference syntax, and the v0.16+ release hooks / wakeOn / cron onFailure / external-DB backup features. Invoke whenever the user mentions deploys, builds, logs, env vars, addons (postgres/redis/etc.), release hooks, migrations, sleeping pods, callback webhooks, or anything related to their kuso instance.
allowed-tools: Bash(kuso:*), Bash(curl:*), Read, Edit, Write, Grep, Glob
---

# kuso — operating a project on this PaaS

This project is deployed via [kuso](https://github.com/sislelabs/kuso), a self-hosted Kubernetes PaaS. The user has a `kuso` CLI on their PATH and a logged-in session against their instance. **Always drive operations through `kuso`, not raw `kubectl`** — the CLI exercises the same auth/tenancy/perm layers users hit, so what you see is what they see.

This skill is current to **v0.16.2**. Run `kuso version` to confirm what's on the user's machine.

## Mental model — read this first

- **Project** = the top-level grouping. One repo or many; one base domain.
- **Service** = one deployable app inside a project. Has a runtime, a port, and env vars.
- **Environment** = one running instance of a service. Each service auto-gets a `production` env. PR previews + named clones (`staging`, `client-demo`) are extra envs.
- **Addon** = a managed datastore. Each addon writes a `<project>-<addon>-conn` Secret that **kuso auto-injects into every service in the same project via `envFromSecrets`** — you do NOT need to wire `DATABASE_URL` etc. by hand. They appear in `process.env` automatically.
- **Build** = a kaniko Job that produces an image and patches the env's `image.tag`. One build per `(service, ref)`. Helm-operator rolls the new pod.
- **Release hook** (v0.16+) = an optional Job that runs **before** the new image is promoted. Heroku-style migration phase. Set via `spec.release.command`.
- **kuso.yml** = optional config-as-code at repo root. **See "Config-as-code caveats" below before using `kuso apply`.**

The CLI is rooted at `kuso <command>`. Run `kuso <command> --help` whenever shape is unclear — every command has examples.

## Two flag conventions — learn the difference

| Command | Command argv syntax |
|---|---|
| `cron add` / `cron add-command` / `cron add-http` | `--cmd '<shell string>'` flag |
| `run` | `--` separator: `kuso run <p> <s> -- sh -c '...'` |
| `env set` | `KEY=VALUE` (multiple per command) |
| `secret set` | **4 positional args**: `<p> <s> KEY VALUE` (NOT `KEY=val`) |
| `shared-secret set` | `KEY=VALUE` |

This inconsistency is real. When you get `Error: accepts N arg(s), received M`, you've hit the wrong convention.

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

# 2. Addons (auto-inject their conn secrets into every service)
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

# 5. Env vars (use `secret` for anything sensitive — values are never returned)
kuso env set papelito web NODE_ENV=production NEXT_TELEMETRY_DISABLED=1
kuso secret set papelito web RESEND_API_KEY re_xxx       # 4 args, no '='
kuso secret set papelito web STRIPE_SECRET_KEY sk_live_xxx

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

## Don't manually wire addon env vars

When you add a postgres addon called `db`, kuso writes a Secret named `<project>-db-conn` and adds it to the service's `envFromSecrets`. **The keys land on the pod automatically:**

| Addon kind  | Keys auto-injected (most common)                              |
| ----------- | ------------------------------------------------------------- |
| `postgres`  | `DATABASE_URL`, `PGUSER`, `PGPASSWORD`, `PGHOST`, `PGPORT`, `PGDATABASE` |
| `redis`     | `REDIS_URL`                                                   |
| `s3`        | `S3_ENDPOINT`, `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY`, `S3_BUCKET` |
| `mailpit`   | `SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`        |
| `nats`      | `NATS_HOST`, `NATS_PORT`, `NATS_TOKEN`, `NATS_URL`            |

Inspect what your instance actually writes with:

```bash
kuso get addons <project> -o json
# look at .status.connectionSecret + the actual Secret keys via the UI
```

You don't need to set `DATABASE_URL: ${{ db.DATABASE_URL }}` in your service's env — it's already there.

## When you DO need `${{ ... }}` references

Only in two cases:

1. **Service-to-service URL** — service `web` needs to talk to service `api`:
   - `${{ api.URL }}` → `http://<project>-api.<ns>.svc.cluster.local:<port>`
   - `${{ api.HOST }}` → bare hostname
   - `${{ api.PORT }}` → numeric port
2. **Renaming an auto-injected key** — your app reads `MAILER_URL` instead of the addon's `SMTP_HOST`. Then:
   - `MAILER_URL: ${{ mail.SMTP_URL }}` (composite values are NOT supported — the `${{ ... }}` must be the entire value, not `prefix-${{ ... }}-suffix`).

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

# Env vars
kuso env list <project> <service>               # plain vars + names of secret keys
kuso env set <project> <service> KEY=val KEY2=val2  # multiple K=V pairs OK
kuso env unset <project> <service> KEY
kuso secret set <project> <service> KEY VALUE   # 4 POSITIONAL args, no '='
kuso secret unset <project> <service> KEY
kuso secret list <project> <service>

# Shared (project-level) secrets — attached to every service in the project
kuso shared-secret set <project> KEY=value
kuso shared-secret list <project>
kuso shared-secret unset <project> KEY

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
kuso project addon add <project> <name> --kind <kind> [--version <v>] [--size small|medium|large]
kuso project service add <project> <name> --runtime <rt> [--port N]
       # runtime: dockerfile | nixpacks | buildpacks | static | worker | image
       # for --runtime image: --image-repo X --image-tag Y (do NOT put X:Y in --image-repo)
kuso project delete <name>                      # cascades to services/envs/addons

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

1. **GitHub App not installed on the repo's owner** → clone 404s. Modern kuso (≥0.9.54) auto-resolves the installation from the repo URL; older builds need the user to install the App on the org/user.
2. **OOMKilled during kaniko snapshot** → "container exited with code 137" on the build's failure message. Fix: trim build deps OR raise the build memory limit in **Settings → Build resources**.
3. **App reads wrong port** → kuso always sets `$PORT` to the service spec's port. Apps that hardcode `3000` while spec says `8080` fail readiness. Fix: bind to `process.env.PORT || 3000`.
4. **App redirects to wrong host on a custom domain** → kuso routes the host correctly; the app's `NEXTAUTH_URL` / `AUTH_URL` / `APP_URL` is hardcoded to the auto-domain. Fix: update that env var with `kuso env set` then `kuso redeploy`.
5. **CrashLoopBackOff with no logs** → readiness/liveness probe failing before app prints. Tail with `kuso logs <p> <s> -f`; the previous pod's last 200 lines are persisted in the BuildLog table even after pod GC.
6. **Build succeeded but new pods never come up** → check `kuso build list` for `release-failed` status. The release hook (migration) blocked promote. Inspect the release Job's logs in the cluster.

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
| `secret set KEY VAL`           | Rolls a new pod.                                 |
| `shared-secret set KEY=...`    | Rolls every service in the project.              |
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
env list/set/unset            plain env vars (K=V K=V on set)
secret list/set/unset         secret-typed env vars (4 ARGS on set: <p> <s> KEY VAL)
shared-secret list/set/unset  project-level secrets (every service)
domains add/rm/list <p> <s>   custom hostnames
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
