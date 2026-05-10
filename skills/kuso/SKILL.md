---
name: kuso
description: Use when working in a project deployed to kuso (a self-hosted Kubernetes PaaS). Explains the kuso CLI, how deployments work, how to debug a failed build or a sleeping pod, and the env-var reference syntax. Invoke whenever the user mentions deploys, builds, logs, env vars, addons (postgres/redis/etc.), or anything related to their kuso instance.
allowed-tools: Bash(kuso:*), Bash(curl:*), Read, Edit, Write, Grep, Glob
---

# kuso — operating a project on this PaaS

This project is deployed via [kuso](https://github.com/sislelabs/kuso), a self-hosted Kubernetes PaaS. The user has a `kuso` CLI on their PATH and a logged-in session against their instance. **Always drive operations through `kuso`, not raw `kubectl`** — the CLI exercises the same auth/tenancy/perm layers users hit, so what you see is what they see.

## Mental model — read this first

- **Project** = the top-level grouping. One repo or many; one base domain.
- **Service** = one deployable app inside a project. Has a runtime (`dockerfile` / `nixpacks` / `static` / `buildpacks` / `image`), a port, and env vars.
- **Environment** = one running instance of a service. Each service auto-gets a `production` env. PR previews + named clones (`staging`, `client-demo`) are extra envs.
- **Addon** = a managed datastore (`postgres`, `redis`, `s3`, `mailpit`, `nats`, `meilisearch`, `clickhouse`). Each addon writes a `<addon>-conn` Secret that services consume via env-var refs.
- **Build** = a kaniko Job that produces an image and patches the env's `image.tag`. One build per `(service, ref)`. Helm-operator rolls the new pod.
- **kuso.yml** = optional config-as-code at repo root. `kuso apply` reconciles it against the live project.

The CLI is rooted at `kuso <command>`. Run `kuso <command> --help` whenever shape is unclear — every command has examples.

## First-time setup

```bash
# Verify session — token, DNS, server reachability, auth.
kuso doctor

# If doctor fails on token: log in (interactive or with --token).
kuso login --api https://kuso.<your-domain> --token <pat>
```

## The 12 commands you'll actually use

```bash
# Where am I? What's running?
kuso get projects -o json                       # all projects
kuso status <project>                           # rollup: services, URLs, replicas, latest build
kuso get services <project> -o json             # service specs

# Logs
kuso logs <project> <service>                   # last 200 lines
kuso logs <project> <service> -f                # tail (^C to stop)
kuso logs <project> <service> --env <env>       # non-prod env (preview-pr-N, staging, etc.)
kuso logs <project> <service> --lines 1000      # bigger tail
kuso logs search <project> <service> "<query>"  # full-text search the persisted archive

# Builds
kuso build list <project> <service>             # newest first; status = pending|running|succeeded|failed
kuso build trigger <project> <service>          # build the project's default branch
kuso redeploy <project> <service>               # alias; --branch <name> or --ref <sha> to target
kuso build rollback <project> <service> <id>    # re-point production at an older successful build's image

# Env vars
kuso env list <project> <service>               # plain vars + names of secret keys
kuso env set <project> <service> KEY=value      # plain
kuso secret set <project> <service> KEY=value   # secret-typed (Kubernetes Secret-backed; values never returned)

# Shells + addons
kuso shell <project> <service>                  # exec into a pod (uses local kubectl; needs kubeconfig)
kuso get addons <project> -o json               # see DATABASE_URL etc. for project's addons

# Config-as-code
kuso init                                       # write a starter kuso.yml in CWD
kuso apply --dry-run                            # plan without writing
kuso apply                                      # reconcile kuso.yml → live project
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

1. **GitHub App not installed on the repo's owner** → clone 404s. Fix: install the App on the org/user, or check the service's installation picker (auto-resolves from URL since v0.9.54).
2. **OOMKilled during kaniko snapshot** → "container exited with code 137" on the build's failure message. Fix: trim build deps OR raise the build memory limit in **Settings → Build resources**.
3. **App reads wrong port** → kuso always sets `$PORT` to the service spec's port. Apps that hardcode `3000` while spec says `8080` fail readiness. Fix: bind to `process.env.PORT || 3000`.
4. **App redirects to wrong host on a custom domain** → kuso routes the host correctly; the app's `NEXTAUTH_URL` / `AUTH_URL` / `APP_URL` is hardcoded to the auto-domain. Fix: update that env var and `kuso redeploy`.
5. **CrashLoopBackOff with no logs** → readiness/liveness probe failing before app prints. Tail with `kuso logs <p> <s> -f`; the previous pod's last 200 lines are persisted to the BuildLog table even after pod GC.

## Env-var reference syntax (the magic `${{ ... }}`)

In an env-var **value** (not the name), kuso recognizes:

- `${{ <addon>.KEY }}` — resolves to a `valueFrom.secretKeyRef` against `<addon>-conn`. Survives addon password rotation. Examples for a postgres addon named `db`:
  - `${{ db.DATABASE_URL }}` → full DSN
  - `${{ db.PGUSER }}`, `${{ db.PGPASSWORD }}`, `${{ db.PGHOST }}`, `${{ db.PGPORT }}`, `${{ db.PGDATABASE }}`
- `${{ <service>.URL }}` → `http://<svc-fqn>.<ns>.svc.cluster.local:<port>` (in-cluster)
- `${{ <service>.HOST }}` → `<svc-fqn>.<ns>.svc.cluster.local`
- `${{ <service>.PORT }}` → numeric port

**Rules:**
- Reference must be the **entire value** — no `prefix-${{ ... }}-suffix`. Server returns 400 on mixed.
- `addon.KEY` is checked against the addon's actual conn secret. Bad key = save fails with the available keys listed.
- These resolve at save time and round-trip cleanly: read returns the same `${{ ... }}` token, never the raw secret.

## Debugging a misbehaving service — the standard playbook

```bash
# 1. What does kuso think is running?
kuso status <project>
kuso get services <project> -o json | jq '.[] | select(.metadata.name=="<svc-fqn>")'

# 2. Latest build — succeeded? Failed with what?
kuso build list <project> <service>
# If failed, the message includes the actual reason
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
| `domains remove <host>`        | Live — Ingress update only.                      |
| Service spec patch (port, etc.)| Rolls a new pod when the field is in the template.|
| Addon password rotation        | Existing pods keep old creds until they restart. |

Only edit production env-vars when you mean to. The web UI shows a **Diff Confirm** modal before applying; the CLI applies immediately — use `--dry-run` shapes (`kuso apply --dry-run`) when in doubt.

## When NOT to use kuso

- You need to inspect a non-kuso pod or raw cluster state → `kubectl` is fine, but you'll need a kubeconfig pointing at the cluster (which the user typically does NOT have on their dev machine).
- You're debugging the operator itself → `ssh` to the cluster + `kubectl logs -n kuso-operator-system deploy/kuso-operator-controller-manager`.

For everything else — **reach for `kuso`**. If a CLI command fails or returns confusing output, that's a real bug; don't paper over it with raw `kubectl`.

## kuso.yml shape (config-as-code)

```yaml
project: my-product
baseDomain: my-product.example.com
defaultRepo:
  url: https://github.com/me/my-product
  defaultBranch: main

services:
  - name: web
    runtime: nixpacks
    port: 3000
    domains: [{ host: my-product.com, tls: true }]
    envVars:
      DATABASE_URL: ${{ db.DATABASE_URL }}
      REDIS_URL:    ${{ cache.REDIS_URL }}
      API_URL:      ${{ api.URL }}
    scale: { min: 1, max: 5, targetCPU: 70 }

  - name: api
    runtime: dockerfile
    port: 8080

addons:
  - name: db
    kind: postgres
    version: "16"
    size: small
  - name: cache
    kind: redis
```

Apply with `kuso apply` (or `kuso apply --dry-run` first). Editing in the web UI and applying via CLI both write the same CRs — don't mix in one flow.

## Quick reference card

```text
get projects            list every project
status <p>              project rollup (services, URLs, replicas, builds)
logs <p> <s> [-f]       tail or stream pod logs
build list <p> <s>      build history
build trigger <p> <s>   manual build
redeploy <p> <s>        same as build trigger; --branch / --ref
shell <p> <s>           kubectl exec into a pod
env list/set/unset      plain env vars
secret list/set/unset   secret-backed env vars
get addons <p>          addons + their conn-secret names
apply [--dry-run]       reconcile kuso.yml
doctor                  pre-flight checks
```

When in doubt: `kuso <command> --help` always works and always has examples.
