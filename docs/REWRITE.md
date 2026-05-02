# Kuso server: NestJS → Go rewrite spec

> **Status (2026-05): rewrite complete.** The original NestJS code under
> `server/` has been deleted from the repo. The current production
> server is `server-go/`. This document is preserved as the historical
> migration plan — paths and "TS server stays runnable" guarantees below
> describe the migration window, not the current state.
>
> For the current HTTP surface, see `docs/WORKFLOWS.md`. For the
> validated cutover steps see `docs/LIVE_TEST_PLAN.md`.

---

This document was the handoff for an agent (or human) executing the full
rewrite of `server/` from NestJS/TypeScript to Go. It is committed so it
survives any context-window compaction. **Read this end-to-end before
writing a single line of Go.**

The TS server stayed runnable on `main` until the Go server reached
parity on a feature-flagged subset. Replacement was per-module, not big
bang.

---

## 0. Why we're doing this

- Single language across operator (Go), CLI (Go), MCP (Go), server (will
  be Go). One toolchain, one deploy story, one set of types.
- ~10× smaller memory footprint, single static binary, no `node_modules`
  in the install path. Matters for a self-hosted PaaS where the user
  cares about the resource cost of the control plane.
- Strong typing against the kube API via `client-go`. The TS server
  reaches into `(this.kubectl as any).coreV1Api` in several places
  because the typing ergonomics are awful.

What we are **not** doing: changing the public HTTP API, the CRD
schemas, the CLI wire format, or the Vue client. The Vue client must
continue to work unchanged, byte-for-byte, against the new server.

---

## 1. Source-of-truth inventory (as of commit dfa37c9)

```
server/src/
├── audit/         — audit log writer; Prisma-backed
├── auth/          — bcrypt + JWT issuance, OAuth callback, session check
├── cli/           — internal helper module for CLI auth flow
├── common/        — shared decorators, guards, exception filters
├── config/        — viper-equivalent: KusoConfig CR + runpacks + podsizes seed
├── database/      — Prisma client wrapper + seeding (runpacks, podsizes, perms)
├── environments/  — list/get/delete KusoEnvironment CRs
├── events/        — Kubernetes Events tailing (per-namespace)
├── github/        — App auth, installation tokens, OAuth, webhook handler
├── groups/        — UserGroup CRUD
├── kubernetes/    — low-level client wrapper + raw CR access
├── logger/        — pino-style structured logger
├── metrics/       — Prometheus counters/histograms
├── notifications/ — in-app notification persistence
├── projects/      — KusoProject/Service/Environment/Build orchestration
│                    + per-service secrets + builds + log tailing
├── roles/         — Role+Permission CRUD
├── status/        — /healthz, /api/status, kuso/operator versions
├── templates/     — built-in app templates (NodeJS/Go/Python/etc.)
├── token/         — long-lived API tokens (issue/list/revoke)
└── users/         — User CRUD + password change
```

Total: ~11.3 kLOC of TS (excluding tests and node_modules).

The five modules that contain ALL the non-trivial logic, ranked by
porting risk:

1. `projects/` (2.2 kLOC) — orchestrates 4 CRDs, secrets, builds, logs.
   Highest-value, highest-risk.
2. `config/` (1.7 kLOC) — boots from a `KusoConfig` CR + seeds DB; lots
   of "first run" branching.
3. `kubernetes/` (1.1 kLOC) — every other module depends on this.
4. `github/` (0.9 kLOC) — App auth, OAuth, webhook HMAC, Octokit calls.
5. `auth/` (0.8 kLOC) — JWT, bcrypt, permissions claim.

The other 13 modules are CRUD-over-Prisma with thin handlers; they will
fall over when the foundation is solid.

---

## 2. Target Go layout

```
server-go/
├── cmd/
│   └── kuso-server/
│       └── main.go         — flag parsing, signal handling, wires app.New()
├── internal/
│   ├── app/                — DI container assembly (no DI framework, just
│   │                         a struct of interfaces composed in main)
│   ├── http/
│   │   ├── router.go       — chi router, middleware chain
│   │   ├── middleware/     — JWT auth, request log, recover, CORS
│   │   └── handlers/       — one file per resource (projects, secrets, ...)
│   ├── auth/               — JWT issue/verify, bcrypt, permission claim
│   ├── github/             — App auth, OAuth, webhook signature
│   │   ├── app.go          — github.NewClient with App JWT
│   │   ├── installation.go — installation-token cache
│   │   ├── oauth.go        — /api/auth/github callback flow
│   │   └── webhook.go      — HMAC verify + dispatch
│   ├── kube/
│   │   ├── client.go       — *kubernetes.Clientset + dynamic.Interface
│   │   ├── crds.go         — typed wrappers over our 6 CRDs (one per kind)
│   │   ├── secrets.go      — Secret upsert/remove (race-free, see §6.4)
│   │   └── helm.go         — patch CRs that helm-operator owns
│   ├── projects/
│   │   ├── service.go      — orchestration: project ↔ services ↔ envs
│   │   ├── secrets.go      — per-env secrets (shared + scoped, secretsRev)
│   │   ├── builds.go       — KusoBuild lifecycle + image tag computation
│   │   └── logs.go         — pod log tailing
│   ├── db/
│   │   ├── schema.sql      — manual SQLite schema (no Prisma)
│   │   ├── queries.sql     — sqlc input
│   │   └── (generated)     — sqlc output checked in
│   ├── audit/, config/, ... — one package per remaining module
│   └── version/
│       └── version.go      — embed VERSION via //go:embed
├── api/
│   └── openapi.yaml        — generated from handlers; CI guards drift
├── go.mod
└── README.md
```

**Single binary; no plugin system, no codegen at runtime.** `embed`
the SPA build (`client/dist`) and serve it from `/` like the TS server
does today.

---

## 3. Library choices (locked in)

| Concern | Choice | Why |
|---|---|---|
| HTTP router | `go-chi/chi v5` | Idiomatic, middleware-friendly, no magic. |
| OpenAPI | `danielgtaylor/huma/v2` | Decorator-style without reflection magic. Optional — can also hand-write spec. |
| Kube client | `k8s.io/client-go` + `k8s.io/apimachinery` | Standard. Use dynamic client for our CRDs (no codegen unless we hit pain). |
| GitHub | `google/go-github/v66` + `bradleyfalzon/ghinstallation/v2` | App JWT + installation token caching for free. |
| DB | SQLite via `mattn/go-sqlite3` (or `modernc.org/sqlite` for pure-Go) + `sqlc` for queries | Same SQLite file, no migration step on first cutover. |
| Bcrypt | `golang.org/x/crypto/bcrypt` | Stdlib-adjacent. |
| JWT | `golang-jwt/jwt/v5` | Same lib our CLI already uses. |
| Logging | `log/slog` | Stdlib. JSON output. |
| Metrics | `prometheus/client_golang` | Standard. |
| Config | `caarlos0/env/v11` for env vars, plus our `KusoConfig` CR | No viper. We already have a viper-dotted-key landmine in the CLI; do not bring it into the server. |
| Validation | `go-playground/validator/v10` | Struct tags. |
| Testing | stdlib `testing` + `stretchr/testify` for assertions | Boring. |

Prohibited (reasoning):
- **No DI framework** (wire, fx). The TS code already has a manageable
  graph; assemble in `app.New(deps)` and pass interfaces.
- **No GORM**. sqlc gives us typed queries against SQLite without ORM
  surprises.
- **No viper**. See above.

---

## 4. Database

The current TS server uses Prisma against `data/kuso.db` (SQLite). The
schema in `server/prisma/schema.prisma` defines 16 models. Two approaches:

### 4a. (Recommended) Hand-write `schema.sql` matching Prisma's emitted DDL

Run `npx prisma migrate diff --from-empty --to-schema-datamodel server/prisma/schema.prisma --script` to dump the SQL. Commit it as `internal/db/schema.sql`. On startup, `CREATE TABLE IF NOT EXISTS` from
that file. Use sqlc for queries.

The Go server should be able to **open the existing live SQLite file
without any migration**. Verify on the Hetzner box: stop kuso-server,
copy /var/lib/kuso/kuso.db locally, point a Go test binary at it, run
some queries.

### 4b. (Fallback) Re-derive schema in Go

Only if 4a hits a Prisma-emitted column we can't reproduce. Unlikely.

### Models that matter

The big ones — anything that touches the live data at cutover:
- `User` (admin login, password hashes)
- `Token` (long-lived API JWTs — must keep working without re-issue)
- `GithubInstallation` (App installation IDs the user has connected)
- `GithubUserLink` (OAuth user ↔ GitHub user mapping)
- `Role`, `Permission` (RBAC)

The seed-only models (`Runpack`, `PodSize`, etc.) just need their
upsert-on-boot logic ported.

---

## 5. Port order

Bottom-up. **Do not skip ahead.** Each step ends with the new code
running locally + tests passing before you start the next.

### Phase 0: scaffold (½ day)
- `go mod init kuso/server`
- `cmd/kuso-server/main.go` that boots a chi server with one route:
  `GET /healthz` returning `{"status":"ok","version":"<embedded>"}`.
- Dockerfile producing a static binary. Verify it runs.

### Phase 1: kube client (1 day)
- `internal/kube/client.go` — in-cluster + out-of-cluster config (KUBECONFIG).
- Typed wrappers for the 6 CRDs:
  `KusoConfig`, `KusoProject`, `KusoService`, `KusoEnvironment`,
  `KusoAddon`, `KusoBuild`. Use the dynamic client; add typed structs
  in `internal/kube/types.go` mirroring the OpenAPI in
  `operator/config/crd/bases/`.
- Test: list KusoEnvironments from the live Hetzner cluster.

### Phase 2: db + auth (1.5 days)
- sqlc setup; emit `internal/db/queries/`.
- bcrypt password verify against existing User rows.
- JWT issue/verify (must produce tokens valid for the existing CLI —
  match the claim shape: `sub, exp, permissions[]`).
- `POST /api/auth/login` returning a JWT.
- Middleware that parses the bearer token and stuffs claims in context.
- Test: log in with the live admin creds, verify the issued JWT works
  against the *TS* server's `/api/auth/session`.

### Phase 3: projects core (2-3 days)
- KusoProject + KusoService CRUD.
- Environment listing (read-only initially).
- Per-service env vars (the plain-text ones).
- Test: `kuso project list` against the Go server returns the same
  output as against the TS server.

### Phase 4: secrets + secretsRev (1-2 days)
- Per-env secrets with the **race-free patch** logic from
  `dfa37c9` — see §6.4 below for the exact landmine.
- `bumpSecretsRev` on the affected env CR(s).
- Test: 6-way parallel `kuso secret set` for distinct keys all survive.

### Phase 5: builds (2 days)
- KusoBuild CR creation.
- Image tag = first 12 chars of git SHA (matches existing TS).
- Hand off to operator (helm-chart-driven kaniko job); the server only
  watches build CR phase transitions.
- `GET /api/projects/:p/services/:s/builds` paginated list.

### Phase 6: GitHub App + webhook (3-4 days, hardest)
- `internal/github/app.go` — App JWT signed with the PEM in the
  `kuso-github-app` Secret.
- `internal/github/installation.go` — token cache with TTL (use
  ghinstallation, but DO NOT regenerate per request — that hits GitHub
  rate limits hard at scale).
- OAuth flow: `/api/auth/github/callback` exchange + state cookie.
- Webhook handler at `/api/github/webhook`:
  - HMAC verify with the secret from `kuso-github-app`.
  - Dispatch by event type: `pull_request`, `push`, `installation`,
    `installation_repositories`.
  - Push → trigger build for the affected service(s).
  - PR opened/sync → create preview env (KusoEnvironment CR).
  - PR closed/merged → delete preview env.
- Test: real GitHub event replays from Hetzner audit logs.

### Phase 7: logs (½ day)
- Pod-log tailing via `coreV1.RESTClient().Get().Resource("pods").
  SubResource("log").Stream(ctx)`.
- Match the JSON shape `{lines: [{pod, line}]}`.

### Phase 8: remaining CRUD (1 day total)
- audit, users, roles, groups, tokens, notifications.
- All thin DB wrappers, write them in a sitting.

### Phase 9: cutover (1 day)
- Build new image `ghcr.io/sislelabs/kuso-server-go:v0.2.0-rc1`.
- Deploy to Hetzner alongside the TS server (different deployment
  name, point at the same SQLite PV via a 1-replica strategy — SQLite
  cannot be written by two processes safely; cutover means TS down →
  Go up, not blue/green).
- Run the resilience-sweep probes again (see git log around `dfa37c9`).
- If anything fails, scale TS back up; the new code is opt-in.

---

## 6. Landmines (real ones we hit, with fixes)

These are things you will rediscover the hard way if you don't read them
here first. Each was a multi-hour debug in the TS implementation.

### 6.1 KusoConfig CRD has `x-kubernetes-preserve-unknown-fields: false` on spec

Adding a new field to `spec.*` requires a CRD update — the API server
silently drops unknown fields. We hit this with `secretsRev` (see commit
f17285b). Fix: edit
`operator/config/crd/bases/application.kuso.sislelabs.com_*.yaml` AND
`kubectl apply -f` to the cluster. The Go server doesn't change this
landmine, just must not fall into the same hole when adding fields.

### 6.2 `envFromSecrets` value-only changes don't restart pods

K8s does not restart pods when a Secret referenced via `envFrom` has its
data updated. The TS fix (commit f17285b) bumps
`spec.secretsRev` on the env CR, and the helm chart stamps that into a
pod-template annotation, forcing a new ReplicaSet. **Replicate this
exactly.** Files:
- `operator/helm-charts/kusoenvironment/values.yaml` (`secretsRev: ""`)
- `operator/helm-charts/kusoenvironment/templates/deployment.yaml`
  (`kuso.sislelabs.com/secrets-rev` annotation)
- The Go server must call `bumpSecretsRev` after every secret write.

### 6.3 GitHub installation tokens cache (1h TTL)

The TS `octokit` client transparently refreshes; `go-github` does not.
Use `ghinstallation.NewKeyFromFile(itr, appID, instID, pemPath)` and
**reuse the transport** across requests. A common bug: making a fresh
`http.Client` per webhook event, which exhausts GitHub rate limits
inside an hour. The transport caches the installation token correctly —
don't second-guess it.

### 6.4 Read-modify-write loses concurrent secret writes

**Critical.** Commit `dfa37c9` fixed a lost-update bug where
`setSecret` did `read → modify → replaceNamespacedSecret`. Two parallel
writes for *different keys* would both read the same Secret, each add
their key, both replace — last write wins, the other key is lost.

The Go port MUST use:
- For set: `application/merge-patch+json` with body
  `{"data":{"<key>":"<base64>"}}` — additive on `.data`.
- For unset: `application/json-patch+json` with body
  `[{"op":"remove","path":"/data/<escaped-key>"}]`. RFC 6901 escape: `~`
  → `~0`, `/` → `~1`.
- 422 on patch → key already gone (treat as not-found).

Verified live with 6-way parallel set+unset; do not regress.

### 6.5 Helm-operator finalizer on hand-crafted CRs

If you create a `KusoEnvironment` via `kubectl apply` (not via the
server flow), helm-operator can't uninstall it on delete because there's
no helm release to uninstall. The CR's resources get cleaned up but the
finalizer stays dangling. Server flow is fine; just don't synthesize CRs
in tests without going through the helm install path or expect to
manually patch finalizers.

### 6.6 SQLite single-writer constraint

The SQLite database CANNOT have two writers (TS server + Go server)
running concurrently. Cutover is "TS down → Go up", not blue/green. Plan
for ~30s of unavailability or do it during a quiet window.

### 6.7 SPA serving and CORS

The TS server embeds the Vue build under `dist/public/` and serves it
with a fall-through to `index.html` for client-side routes. The Go
server must do the same with `embed.FS` + `http.FileServer` + a
fallback handler. Don't enable CORS in prod (same-origin); enable it
explicitly when `KUSO_DEV_CORS=1`.

### 6.8 The CLI uses dotted instance names like `kuso.sislelabs.com`

This bit us in the CLI (viper splits on `.`). The Go server doesn't use
viper — but if you write any config-file-loading helper, do not use
viper, or set `KeyDelimiter("\x00")` per the existing CLI workaround.

---

## 7. What stays unchanged

- `client/` (Vue 3 + Vuetify) — not touched. Continues to talk JSON.
- `cli/` (Go) — not touched. Continues to call the same endpoints.
- `mcp/` (Go) — not touched.
- `operator/` (helm-operator) — not touched.
- All CRD schemas in `operator/config/crd/bases/`.
- `deploy/*.yaml` manifests, except `deploy/server.yaml` swaps the
  image tag.
- `hack/install.sh` — the install flow is identical from the user's POV.

---

## 8. Acceptance criteria

The Go server replaces the TS server when **all** of these pass against
the live Hetzner cluster:

1. `kuso login` works with existing credentials, issued JWTs are
   accepted by both servers (forward-compatible JWT).
2. `kuso project list`, `kuso project create`, `kuso project service add`,
   `kuso build trigger`, `kuso secret set/list/unset` (shared + per-env)
   produce identical output and identical cluster state.
3. GitHub webhook end-to-end: open a PR in the test repo, preview env
   spins up, close PR, preview env tears down.
4. The 7 resilience probes from commit `dfa37c9` (kill server, kill
   operator, kill registry, kill app pod, stuck finalizer recovery,
   6-way parallel secret writes for different keys, 6-way parallel
   secret writes for same key) all behave at least as well as TS.
5. Memory at idle ≤ 80 MB (TS server idles around 350 MB).
6. Cold-start ≤ 3 seconds (TS server takes ~14s for DB seed + Nest
   bootstrap).
7. The Vue UI loads, login works, all panels render. No JS console
   errors.

---

## 9. Estimated effort

3-4 weeks of focused work for a single agent doing a faithful port. The
phase breakdown above sums to ~14 working days, plus contingency for
GitHub-flow edge cases (always more than expected) and the cutover
itself.

The TS server stays the source of truth and remains deployable
throughout. There is no "broken middle state" — every phase ends with
both servers operational, with the Go server doing strictly more on
each iteration.

---

## 10. First action for the next agent

1. Read this whole document.
2. `cd kuso && go work init && go work use ./cli ./mcp && mkdir server-go && cd server-go && go mod init kuso/server`
3. Phase 0 scaffold (cmd + main.go + Dockerfile + healthz).
4. **Stop.** Open a PR against `main`. Get it reviewed before touching
   Phase 1.

Per-phase PRs keep the diff reviewable and the bisect lane clean.
