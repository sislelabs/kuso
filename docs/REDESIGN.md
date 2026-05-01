# Redesign: Projects, not Pipelines

**Status:** spec, pre-implementation
**Last updated:** 2026-05-01
**Replaces:** the existing `KusoPipeline` / `KusoApp` / per-addon CRDs and the dashboard's "pipelines"-first UX.

## Vision

kuso v0.2 ships a Railway/Coolify-style developer experience:

> Connect a GitHub repo. kuso detects the runtime, builds it, gives you a URL, and rolls a fresh deploy on every push to main. Open a pull request and you get a preview URL that tears down on merge. Add a Postgres with one click and `DATABASE_URL` shows up as an env var in every service in the project.

No pipelines. No phases. No "review/staging/production" CRD topology. The branch IS the environment.

---

## Concepts

### Project

A **project** is one product. It owns:

- A **default repo** (used by every service in the project unless overridden).
- A **base domain** (`kuso.example.com`), under which services get auto-generated subdomains.
- A **previews flag** — when `true`, every PR opened against the default repo spawns a preview environment.
- A list of **services** (deployable processes).
- A list of **addons** (Postgres, Redis, etc., shared across all services in the project).
- A **GitHub App installation reference** (the kuso GitHub App must be installed on the org/user that owns the repo).

### Service

A **service** is one deployable process inside a project. Examples: `web` (Next.js), `api` (Go), `worker` (background queue consumer).

A service has:

- A **name** (unique within project).
- An optional **repo override** (defaults to project repo).
- An optional **path** for monorepo support — e.g. `services/api`. Build context is rooted here.
- A **runtime** auto-detected from the file tree at `path` (`dockerfile` / `nixpacks` / `buildpacks` / `static`), overridable.
- Per-environment **scale, sleep, env vars, domains**.
- A **default port** auto-detected (Dockerfile EXPOSE, package.json scripts.start, etc.).

A service does NOT belong to a phase — it has multiple `KusoEnvironment` resources tracking it in different environments.

### Environment

An **environment** is one running instance of a service in a project. Two kinds:

- **`production`** — always exists. Tracks the project's default branch (configurable, defaults to `main`). One per project.
- **`preview-pr-<n>`** — ephemeral. Spawned when a PR is opened (if previews are enabled), torn down when the PR closes or merges. One per (PR, service).

The environment holds:

- A reference to its parent service.
- The **branch** + **commit** currently deployed.
- Image tag, replica count, sleep state, ingress hostname.
- A reference to the originating PR (if a preview).
- A **TTL** — preview envs default to 7 days, refreshed on every push to the PR. Belt-and-braces in case the PR-closed webhook is missed.

### Addon

An **addon** is a managed dependency (Postgres, Redis, MongoDB, MySQL, RabbitMQ, Memcached, ClickHouse, Elasticsearch, Kafka, CockroachDB, CouchDB) that belongs to a project.

When an addon is reconciled, the operator:

1. Renders the right helm chart (CloudNativePG for postgres, redis-cluster, etc.).
2. Generates a connection-info Secret named `<project>-<addon-name>-conn` containing standard envs (`DATABASE_URL`, `REDIS_URL`, etc.).
3. Adds that Secret to **every** service Deployment in the project via `envFrom`.

This is the auto-shared model: services in the project just get the env vars. No explicit attach/detach.

Addons are **per-project**, not per-environment. Production and preview share a single Postgres unless the user explicitly opts into per-environment addons (deferred).

---

## CRDs

All under `application.kuso.sislelabs.com/v1alpha1`. The old CRDs (`KusoPipeline`, `KusoApp`, `KusoBuild`, `KusoAddonPostgres`, `KusoAddonRedis`, `KusoAddonMysql`, `KusoAddonMongodb`, `KusoAddonRabbitmq`, `KusoAddonMemcached`, `KusoCouchdb`, `KusoElasticsearch`, `KusoKafka`, `KusoMemcached`, `KusoMongodb`, `KusoMysql`, `KusoPostgresql`, `KusoRedis`, `KusoRabbitmq`, `KusoMail`, `KusoPrometheus`, `KusoEs`) are deleted wholesale.

### `KusoProject`

```yaml
apiVersion: application.kuso.sislelabs.com/v1alpha1
kind: KusoProject
metadata:
  name: analiz
  namespace: kuso
spec:
  description: "Analytics product"
  baseDomain: analiz.sislelabs.com   # optional; defaults to <name>.<global-base>
  defaultRepo:
    url: https://github.com/sislelabs/analiz
    defaultBranch: main
  github:
    installationId: 12345678         # GitHub App installation that grants repo access
  previews:
    enabled: true
    ttlDays: 7
status:
  services: 3
  environments: 5                    # production + 4 active previews
  addons: 2
  ready: true
  conditions: [...]
```

### `KusoService`

```yaml
apiVersion: application.kuso.sislelabs.com/v1alpha1
kind: KusoService
metadata:
  name: analiz-api
  namespace: kuso
  labels:
    kuso.sislelabs.com/project: analiz
spec:
  project: analiz
  repo:                              # optional override, defaults to project.defaultRepo
    url: https://github.com/sislelabs/analiz
    path: services/api               # monorepo support; default "."
  runtime: dockerfile                # dockerfile | nixpacks | buildpacks | static; auto-detected on create
  port: 8080                         # auto-detected; user-overridable
  domains:                           # optional per-service overrides; defaults to <service>.<project>.<baseDomain>
    - host: api.analiz.sislelabs.com
      tls: true
  envVars:
    - name: LOG_LEVEL
      value: info
    - name: STRIPE_SECRET_KEY
      valueFrom:
        secretKeyRef:
          name: analiz-secrets
          key: stripe
  scale:
    min: 1
    max: 5
    targetCPU: 70
  sleep:
    enabled: true
    afterMinutes: 30
status:
  detectedRuntime: dockerfile
  detectedPort: 8080
  environments:                      # rolled-up state
    - name: production
      branch: main
      commit: abc1234
      ready: true
      url: https://api.analiz.sislelabs.com
    - name: preview-pr-42
      branch: feat/foo
      commit: def5678
      ready: true
      url: https://api-pr-42.analiz.sislelabs.com
```

### `KusoEnvironment`

```yaml
apiVersion: application.kuso.sislelabs.com/v1alpha1
kind: KusoEnvironment
metadata:
  name: analiz-api-production
  namespace: kuso
  labels:
    kuso.sislelabs.com/project: analiz
    kuso.sislelabs.com/service: analiz-api
    kuso.sislelabs.com/env: production
spec:
  project: analiz
  service: analiz-api
  kind: production                   # production | preview
  branch: main
  pullRequest:                       # only set when kind=preview
    number: 42
    headRef: feat/foo
  ttl:
    expiresAt: "2026-05-08T10:00:00Z"  # only for previews
  overrides:                         # per-environment overrides on the parent service spec
    envVars: []
    scale: {}
status:
  commit: abc1234
  imageTag: ghcr.io/.../analiz-api:abc1234
  ready: true
  url: https://api.analiz.sislelabs.com
  lastDeployedAt: "2026-05-01T12:00:00Z"
```

The reconciler renders Deployment + Service + Ingress (+HPA + Cert) for each `KusoEnvironment`, owned by it. Deleting the env deletes everything.

### `KusoAddon` (polymorphic)

```yaml
apiVersion: application.kuso.sislelabs.com/v1alpha1
kind: KusoAddon
metadata:
  name: analiz-pg
  namespace: kuso
  labels:
    kuso.sislelabs.com/project: analiz
spec:
  project: analiz
  kind: postgres                     # postgres | redis | mongodb | mysql | rabbitmq |
                                     # memcached | clickhouse | elasticsearch | kafka |
                                     # cockroachdb | couchdb
  version: "16"
  size: small                        # small | medium | large
  ha: false                          # if true, use cluster chart variants where supported
  backup:
    schedule: "0 3 * * *"
    retentionDays: 14
status:
  ready: true
  connectionSecret: analiz-pg-conn   # services in the project mount this via envFrom
  endpoint: analiz-pg.kuso.svc.cluster.local:5432
```

The operator dispatches on `spec.kind` to pick the right helm chart internally. From the user's perspective there's just one CRD type.

---

## Auto-detect runtime

When a service is created (or its repo/path changes), the server fetches the file tree at the configured ref via the GitHub App and runs detection rules in order. First match wins:

| Rule | Runtime |
| --- | --- |
| `Dockerfile` exists at path | `dockerfile` |
| `package.json` AND no `Dockerfile` | `nixpacks` |
| `go.mod` AND no `Dockerfile` | `nixpacks` |
| `Cargo.toml` AND no `Dockerfile` | `nixpacks` |
| `requirements.txt` OR `pyproject.toml` AND no `Dockerfile` | `nixpacks` |
| `index.html` only (no package.json) | `static` |
| user explicitly picks Heroku-style buildpack | `buildpacks` |
| nothing matches | unknown — UI prompts |

Auto-detected port:

| Rule | Port |
| --- | --- |
| `Dockerfile` has `EXPOSE n` | `n` |
| `package.json` has `"start"` script with `-p N` or `--port N` | `N` |
| Common framework defaults (Next.js 3000, Vite 5173, FastAPI 8000) | framework default |
| fallback | 8080, prompt user to confirm |

Detection runs server-side. Frontend just shows the result and a "change" button.

---

## GitHub App

A single GitHub App per kuso instance. Owner installs it on their org/user once; every project that needs repo access references the installation id.

**Permissions (minimum):**
- Repository: contents (read), metadata (read), pull_requests (read & write — for status checks), webhooks (write — auto-register)
- Account: email (read, for first-user provisioning)
- Webhook events: `push`, `pull_request`, `installation`, `installation_repositories`

**OAuth flow for sign-in:**
- New user clicks "Sign in with GitHub" → standard OAuth → kuso creates a local user record linked to GitHub user id + token.
- Already-installed App: user picks from their accessible repos when creating a project.
- Not yet installed: kuso pushes the user through the App install flow and returns when complete.

**Webhook handler endpoint:**
- `POST /api/webhooks/github` — verifies signature, dispatches:
  - `push` → look up matching `KusoEnvironment`s by `(project, branch, commit)`, trigger rebuild.
  - `pull_request` (`opened`/`reopened`/`synchronize`) → ensure preview env exists for each service in the project, kick a build.
  - `pull_request` (`closed`) → mark preview env for deletion.
  - `installation`/`installation_repositories` → update GitHub App installation cache.

**Storage:**
- Two new env vars on kuso-server: `GITHUB_APP_ID`, `GITHUB_APP_PRIVATE_KEY` (PEM).
- Per-installation token caching in the database (Prisma model `GithubInstallation`).

**v0.2 doesn't include:** GitLab, Bitbucket, self-hosted Gitea/Gogs. Hard fork — we'll add those when there's a user.

---

## Domains

**Default:** `<service>.<project>.<base-domain>` — e.g. `api.analiz.kuso.sislelabs.com`.
**Preview default:** `<service>-pr-<n>.<project>.<base-domain>` — e.g. `api-pr-42.analiz.kuso.sislelabs.com`.
**Override:** any service can declare its own `domains[]` and the operator wires those instead. cert-manager handles TLS for either path.

This needs a wildcard cert at `*.<project>.<base-domain>` per project, OR a cert-per-host (default). cert-manager supports both; we default to per-host because it's simpler and only the per-host TLS works without a DNS solver. Wildcard is a future flag.

---

## Server API surface

### New

```
GET    /api/projects                         list user's projects
POST   /api/projects                         create project; auto-creates production env
GET    /api/projects/:name                   project + services + envs + addons rolled up
DELETE /api/projects/:name                   cascade delete

GET    /api/projects/:name/services          list services
POST   /api/projects/:name/services          add a service; runs runtime detection
GET    /api/projects/:name/services/:svc     service detail
PUT    /api/projects/:name/services/:svc     update service spec
DELETE /api/projects/:name/services/:svc

GET    /api/projects/:name/envs              list environments
GET    /api/projects/:name/envs/:env         env detail (status, recent deploys, logs URL)
POST   /api/projects/:name/envs/:env/redeploy

GET    /api/projects/:name/addons            list addons
POST   /api/projects/:name/addons            create addon (kind in body)
DELETE /api/projects/:name/addons/:addon

GET    /api/projects/:name/repo/tree         file tree at HEAD; used by runtime detector
GET    /api/projects/:name/repo/branches     branch list

POST   /api/webhooks/github                  signed webhook receiver

GET    /api/github/installations             list user's GitHub App installations
GET    /api/github/installations/:id/repos   list repos in an installation
POST   /api/github/install-url               return URL to install/manage the GitHub App
```

### Removed

```
GET/POST/PUT/DELETE /api/pipelines/...       (entire surface)
GET/POST/PUT/DELETE /api/apps/...            (entire surface)
```

---

## CLI

```
kuso project create <name> --repo <url> [--branch main] [--previews]
kuso project list [-o json]
kuso project describe <name>
kuso project delete <name>

kuso service add <project> <name> [--path <subpath>] [--runtime <r>] [--port <p>]
kuso service list <project>
kuso service describe <project>/<service>
kuso service deploy <project>/<service> [--branch <b>]
kuso service env set <project>/<service> KEY=VAL [--secret]
kuso service scale <project>/<service> --min N --max M
kuso service sleep <project>/<service> enable|disable
kuso service logs <project>/<service> [--env production|preview-pr-N] [-f]

kuso env list <project>
kuso env redeploy <project>/<env>
kuso env delete <project>/<env>            # only preview envs

kuso addon add <project> <name> --kind postgres [--version 16] [--size small]
kuso addon list <project>
kuso addon delete <project>/<addon>

kuso get projects | services | envs | addons -o json
```

The legacy `kuso pipeline` / `kuso app` commands are removed.

---

## MCP tools (rewrite)

Replace the existing tools entirely. Hard fork.

| Tool | Purpose |
| --- | --- |
| `list_projects(filter?)` | All projects with rolled-up status |
| `describe_project(name)` | Full picture: services, envs, addons, recent deploys |
| `bootstrap_project(name, repo, branch?, previews?)` | One-call provision a new project |
| `add_service(project, name, repo?, path?)` | Add a service; returns detected runtime |
| `set_service_config(project, service, patch)` | Idempotent partial update |
| `deploy_service(project, service, ref?)` | Trigger redeploy of a service in a specific env |
| `tail_logs(project, service, env?, lines?)` | Logs |
| `troubleshoot_service(project, service, env?)` | Composite: spec + status + recent logs + events |
| `manage_addon(project, action, kind, name?, options?)` | add/remove/list addons |
| `manage_env(project, env, action)` | redeploy/delete preview env |
| `cluster_health()` | Node states, addon health |

All mutating tools require `confirm: true`. All read-only tools are safe in `--read-only` mode.

---

## UI flow

### New project (Coolify-style multi-step)

1. **Name** — auto-suggest from repo name once selected.
2. **Repo picker** — list repos from user's installed GitHub App installations. If none, "Install kuso GitHub App" CTA → opens GitHub install flow → returns.
3. **Detected services** — kuso scans the repo and proposes services:
   - Single Dockerfile at root → 1 service named after repo.
   - `services/*/` or `apps/*/` with package.json or Dockerfile → 1 service per subpath.
   - Monorepo conventions (Turborepo, Nx) → use those.
   - User can add/remove/edit before continuing.
4. **Addons** — pick from {postgres, redis, mongodb, mysql, rabbitmq, memcached, clickhouse, elasticsearch, kafka, cockroachdb, couchdb}. Each prompts for size + version + (optionally) HA.
5. **Domain** — confirm `<service>.<project>.<base>` or override per service.
6. **Previews** — toggle (default on if GitHub App installed; off otherwise).
7. **Create** — backend creates `KusoProject` + N `KusoService`s + M `KusoAddon`s. Each service starts a build immediately.

### Project page

Tabs:
- **Overview** — service cards (each with live URL, status, last deploy, scale info), addon cards (status, connection secret reference), recent activity.
- **Services** — full list, click to drill into a service.
- **Environments** — production + active previews, click to inspect.
- **Addons** — list, status, connection info.
- **Settings** — domain, previews toggle, GitHub repo, danger zone.

### Service page

- **Status** card with current env (production by default; environment switcher to view a preview).
- **Recent deploys** with commit messages + status + redeploy button.
- **Logs** tab.
- **Env vars** tab (split plain / secret).
- **Settings** — runtime, port, scale, sleep, domains.

---

## Migration from v0.1

There is none. v0.2 deletes the old CRDs entirely.

A one-shot `kuso migrate from-pipelines` CLI command will be provided that reads existing `KusoPipeline`/`KusoApp` resources, prints a translation to the new model, and (if `--apply`) creates `KusoProject`+`KusoService`+`KusoEnvironment` and deletes the old CRs. Used once during the v0.1 → v0.2 install upgrade. Not in v0.2.0; ship in v0.2.1.

For the existing Hetzner cluster: the `hello` `KusoApp` we deployed during the v0.1 smoke test will be deleted manually as part of Phase 7.

---

## Phasing (committed in tracked order)

| Phase | What | Approx work |
| --- | --- | --- |
| 0 | This doc | done |
| 1 | New CRDs + helm charts; delete old CRDs | 1 session |
| 2 | Server: project/service/env/addon controllers + new API surface | 2 sessions |
| 3 | GitHub App: registration, OAuth, webhooks, installation cache | 2 sessions |
| 4 | UI: drop pipelines, build new project flow + dashboards | 2-3 sessions |
| 5 | CLI + MCP rewrite | 1 session |
| 6 | Preview env controller + TTL + cleanup loop | 1 session |
| 7 | End-to-end smoke on Hetzner with real repo | 1 session |

**Total: ~10 sessions.** This is a real reshape, not a polish.

Each phase ends in a commit + push. `main` stays runnable; if a phase breaks the cluster, the previous tag works.

## v0.2.0 status (after Phase 7)

End-to-end smoke on the live Hetzner cluster passed:

- Created `KusoProject` "smoke" via `POST /api/projects` ✅
- Service "echo" added; production env auto-emitted ✅
- Operator reconciled env into Deployment + Service + Ingress ✅
- `https://echo.smoke.sislelabs.com` returned `hello-world` over HTTPS ✅
- Postgres addon "pg" added; `StatefulSet`, `Service`, `Secret/smoke-pg-conn` rendered ✅
- Service deployment picked up `envFrom: [smoke-pg-conn]` ✅

Bugs found and fixed during the smoke:

- `kusoaddon` chart's connection-secret name had a doubled project prefix (`<project>-<release>-conn`); release name is already prefixed. Reduced to `<release>-conn`.
- `ProjectsService.refreshEnvironmentsAddonSecrets` used delete+create on envs; that races with helm-operator's uninstall finalizer and ends in "object is being deleted". Replaced with merge-patch.
- Several stuck CRs from intermediate states had to be force-cleared with `kubectl patch ... finalizers=[]`. The legitimate path through the API doesn't hit this; it was a side effect of the partial reset.

## Deferred cleanup (v0.2.1)

The server's `app.module.ts` still imports v0.1 modules that reference deleted CRDs:

- `AppsModule`, `PipelinesModule`, `DeploymentsModule` — entirely v0.1-shaped, dead routes.
- `RepoModule`, `AddonsModule` (the v1 one) — reference deleted concepts.
- The `SecurityModule` has a hard import of `PipelinesModule`; refactor before deleting.
- Several Vue views under `client/src/views/` (Activity, Notifications, Podsizes, Runpacks, Pipeline) and components (`pipelines/`, `apps/`) are unrouted but still on disk.

Build still works because none of the deleted CRDs are referenced at compile time — only at runtime, where the routes 404 from the kubernetes API. Cleanup was deferred to keep Phase 7's blast radius manageable.

## Open questions parked for later

- **Wildcard certs per project.** Defer; per-host certs are fine for v0.2.
- **Per-environment addons** (separate Postgres for previews vs production). Defer; v0.2 shares one addon across envs.
- **Cron jobs and one-off jobs.** v0.1 had cron support in `KusoApp`. Re-add as a `KusoJob` CRD post-v0.2.
- **Build cache.** First deploy is slow because every build clones the repo. Add a per-service ephemeral cache PVC in v0.3.
- **Custom Docker registry credentials** for private base images. Add per-service `imagePullSecrets` in v0.3.
- **Other Git providers** (GitLab, Bitbucket, Gitea). Out of scope until a user asks.
