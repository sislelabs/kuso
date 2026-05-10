---
name: kuso
description: Use when working in a project deployed to kuso (a self-hosted Kubernetes PaaS). Explains the kuso CLI, how deployments work, how to debug a failed build or a sleeping pod, and the env-var reference syntax. Invoke whenever the user mentions deploys, builds, logs, env vars, addons (postgres/redis/etc.), or anything related to their kuso instance.
allowed-tools: Bash(kuso:*), Bash(curl:*), Read, Edit, Write, Grep, Glob
---

# kuso — operating a project on this PaaS

This project is deployed via [kuso](https://github.com/sislelabs/kuso), a self-hosted Kubernetes PaaS. The user has a `kuso` CLI on their PATH and a logged-in session against their instance. **Always drive operations through `kuso`, not raw `kubectl`** — the CLI exercises the same auth/tenancy/perm layers users hit, so what you see is what they see.

## Mental model — read this first

- **Project** = the top-level grouping. One repo or many; one base domain.
- **Service** = one deployable app inside a project. Has a runtime, a port, and env vars.
- **Environment** = one running instance of a service. Each service auto-gets a `production` env. PR previews + named clones (`staging`, `client-demo`) are extra envs.
- **Addon** = a managed datastore. Each addon writes a `<project>-<addon>-conn` Secret that **kuso auto-injects into every service in the same project via `envFromSecrets`** — you do NOT need to wire `DATABASE_URL` etc. by hand. They appear in `process.env` automatically.
- **Build** = a kaniko Job that produces an image and patches the env's `image.tag`. One build per `(service, ref)`. Helm-operator rolls the new pod.
- **kuso.yml** = optional config-as-code at repo root. **See "Config-as-code caveats" below before using `kuso apply`.**

The CLI is rooted at `kuso <command>`. Run `kuso <command> --help` whenever shape is unclear — every command has examples.

## First-time setup

```bash
# Verify session — token, DNS, server reachability, auth.
kuso doctor

# If doctor fails on token: log in.
kuso login --api https://kuso.<your-domain> --token <pat>
```

## Imperative path (recommended) — create everything via subcommands

```bash
# 1. Project (skip if it already exists; kuso project list to check)
kuso project create papelito --base-domain papelito.example.com

# 2. Addons (auto-inject their conn secrets into every service)
kuso project addon add papelito db --kind postgres --version 16 --size small
kuso project addon add papelito storage --kind s3
kuso project addon add papelito cache --kind redis
# Other supported kinds: mailpit, nats, meilisearch, clickhouse,
# mongodb, mysql, rabbitmq, memcached, elasticsearch, kafka,
# cockroachdb, couchdb. Check what your CLI build supports with:
#   kuso project addon add --help

# 3. Service from a repo (default: build via nixpacks)
kuso project service add papelito web \
  --runtime dockerfile --port 3000

# 3b. OR: service from a pre-built registry image (no kaniko build)
kuso project service add papelito web \
  --runtime image \
  --image-repo ghcr.io/sislelabs/papelito \
  --image-tag v1.2.3 \
  --port 3000

# 4. Domains
kuso domains add papelito web papelito.example.com

# 5. Env vars (use `secret` for anything sensitive — values are never returned)
kuso env set papelito web NODE_ENV=production NEXT_TELEMETRY_DISABLED=1
kuso secret set papelito web RESEND_API_KEY=re_xxx
kuso secret set papelito web STRIPE_SECRET_KEY=sk_live_xxx

# 6. Trigger first build (only needed for repo-based runtimes)
kuso build trigger papelito web

# Watch it
kuso logs papelito web -f
kuso status papelito
```

This imperative path is **the safe one**. Use it unless you have a specific reason to prefer config-as-code.

## Config-as-code caveats — `kuso apply`

`kuso apply` reads `kuso.yml` and reconciles it against the live project. **Known sharp edges:**

- The plan's `addonsToDelete` will list addons from OTHER projects under the same namespace if your kuso install is from before the addon-scoping fix landed. **If `--dry-run` shows deletes against addons you didn't author, STOP — running it will destroy other tenants' data.** Use the imperative path instead until the user confirms their server is patched.
- `--dry-run` prints the plan but doesn't write. Always run with `--dry-run` first; eyeball every `delete` line before running without it.
- A misspelled addon name in `addons:` looks identical to "user wants the live addon deleted." Plan diffs are merciless.

```bash
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

## The 12 commands you'll actually use

```bash
# Where am I? What's running?
kuso get projects -o json                       # all projects
kuso status <project>                           # rollup: services, URLs, replicas, latest build
kuso get services <project> -o json             # service specs
kuso get addons <project> -o json               # addons + connection-secret names

# Logs
kuso logs <project> <service>                   # last 200 lines
kuso logs <project> <service> -f                # tail (^C to stop)
kuso logs <project> <service> --env <env>       # non-prod env (preview-pr-N, staging, etc.)
kuso logs <project> <service> --lines 1000      # bigger tail
kuso logs search <project> <service> "<query>"  # full-text search the persisted archive

# Builds
kuso build list <project> <service>             # newest first; status = pending|running|succeeded|failed
kuso build trigger <project> <service>          # build the project's default branch
kuso redeploy <project> <service>               # alias; --branch <name> or --ref <sha>
kuso build rollback <project> <service> <id>    # re-point production at an older successful build

# Env vars
kuso env list <project> <service>               # plain vars + names of secret keys
kuso env set <project> <service> KEY=value      # plain
kuso secret set <project> <service> KEY=value   # secret-typed (Kubernetes Secret-backed)

# Shells + addons + domains
kuso shell <project> <service>                  # exec into a pod (uses local kubectl)
kuso domains add <project> <service> <host>     # add a custom domain
kuso domains rm  <project> <service> <host>     # remove

# Imperative resource creation
kuso project create <name> [--base-domain ...]
kuso project addon add <project> <name> --kind <kind>
kuso project service add <project> <name> --runtime <rt> [--port N]
```

## How a deployment actually flows

```
git push → GitHub webhook → kuso receives push event
  → creates a KusoBuild CR with the commit SHA
  → operator renders a kaniko Job
    → init: clone (with App-installation token if private)
    → init: env-detect (scans repo for ${process.env.X} usages)
    → kaniko: build image, push to in-cluster registry
  → on success: build poller patches env.spec.image.tag
  → operator reconciles: updates Deployment template
  → kube rolls a new ReplicaSet (maxSurge:1, maxUnavailable:0 — zero downtime)
  → old pod terminates once new pod's readinessProbe passes
```

What can go wrong, in rough order of frequency:

1. **GitHub App not installed on the repo's owner** → clone 404s. Modern kuso (≥0.9.54) auto-resolves the installation from the repo URL; older builds need the user to install the App on the org/user.
2. **OOMKilled during kaniko snapshot** → "container exited with code 137" on the build's failure message. Fix: trim build deps OR raise the build memory limit in **Settings → Build resources**.
3. **App reads wrong port** → kuso always sets `$PORT` to the service spec's port. Apps that hardcode `3000` while spec says `8080` fail readiness. Fix: bind to `process.env.PORT || 3000`.
4. **App redirects to wrong host on a custom domain** → kuso routes the host correctly; the app's `NEXTAUTH_URL` / `AUTH_URL` / `APP_URL` is hardcoded to the auto-domain. Fix: update that env var with `kuso env set` then `kuso redeploy`.
5. **CrashLoopBackOff with no logs** → readiness/liveness probe failing before app prints. Tail with `kuso logs <p> <s> -f`; the previous pod's last 200 lines are persisted in the BuildLog table even after pod GC.

## Debugging a misbehaving service — the standard playbook

```bash
# 1. What does kuso think is running?
kuso status <project>
kuso get services <project> -o json | jq '.[] | select(.metadata.name=="<svc-fqn>")'

# 2. Latest build — succeeded? Failed with what?
kuso build list <project> <service>
# Modern builds include the actual reason in the message
# (e.g. "OOMKilled — build hit memory limit (exit 137)" or
# "fatal: repository not found").

# 3. Live logs.
kuso logs <project> <service> -f

# 4. Search the archive for an old error.
kuso logs search <project> <service> "ECONNREFUSED"

# 5. Pop a shell to poke around.
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
| `secret set KEY=...`           | Rolls a new pod.                                 |
| `domains add <host>`           | Live — Ingress + LE cert mint, no pod restart.   |
| `domains rm <host>`            | Live — Ingress update only.                      |
| Service spec patch (port etc.) | Rolls a new pod when the field is in the template.|
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

addons:
  - name: db
    kind: postgres
    version: "16"
    size: small
  - name: cache
    kind: redis
```

## Quick reference card

```text
get projects                  list every project
status <p>                    project rollup (services, URLs, replicas, builds)
logs <p> <s> [-f]             tail or stream pod logs
build list <p> <s>            build history
build trigger <p> <s>         manual build
redeploy <p> <s>              same as build trigger; --branch / --ref
shell <p> <s>                 kubectl exec into a pod
env list/set/unset            plain env vars
secret list/set/unset         secret-backed env vars
domains add/rm <p> <s> <h>    custom hostnames
get addons <p>                addons + their conn-secret names
project addon add             create an addon (auto-injects into every service)
project service add           create a service (--runtime image for pre-built)
doctor                        pre-flight checks
```

When in doubt: `kuso <command> --help` always works and always has examples.
