# Kuso server: HTTP workflows reference

Source-of-truth catalogue of every endpoint the kuso-server exposes,
the request/response shape, and the Vue page or CLI command that
triggers it. Read alongside `LIVE_TEST_PLAN.md` for the journey-derived
walkthrough used to verify the Go rewrite against the live cluster.

Conventions:

- Every authenticated route requires `Authorization: Bearer <jwt>` from
  `POST /api/auth/login` (or the OAuth flows below). `kuso.JWT_TOKEN`
  cookie is also accepted on browser-driven endpoints.
- Path params in `{curlies}` mirror chi syntax.
- `body` blocks describe the JSON request body.
- "Trigger" calls out the Vue page or CLI command that fires the route
  in production usage.
- ✅ marks routes that are implemented in `server-go/`.

---

## 0. Health + status

### `GET /healthz` ✅
- Auth: none.
- Response: `{ "status": "ok", "version": "<embedded>" }`.
- Trigger: kuberprobes, uptime monitors.

### `GET /api/status` ✅
- Auth: none (public; the Vue UI footer reads it pre-login).
- Response: `{ status, version, kubernetesVersion, operatorVersion }`.
- Trigger: Vue UI footer.

---

## 1. Authentication

### `POST /api/auth/login` ✅
- Auth: none.
- Body: `{ "username": "...", "password": "..." }`.
- Response: `{ "access_token": "<jwt>" }`.
- 401 on bad creds (constant-time, no enumeration).
- Trigger: login page; `kuso login` CLI.

### `GET /api/auth/methods` ✅
- Auth: none.
- Response: `{ "local": bool, "github": bool, "oauth2": bool }`.
- Trigger: login page (deciding which buttons to show).

### `GET /api/auth/session` ✅
- Auth: bearer.
- Response: `{ isAuthenticated, userId, username, role, userGroups, permissions, adminDisabled, templatesEnabled, consoleEnabled, metricsEnabled, sleepEnabled, auditEnabled, buildPipeline }`.
- Trigger: every Vue page on first paint; CLI `kuso whoami`.

### `GET /api/auth/github` ✅ (only when `GITHUB_CLIENT_*` are set)
- Auth: none. Sets `kuso_oauth_state` cookie, redirects to GitHub.

### `GET /api/auth/github/callback` ✅
- Auth: none. Validates state cookie, exchanges code, upserts the
  kuso User row, sets `kuso.JWT_TOKEN` cookie, persists
  `GithubUserLink`, redirects to `/`.

### `GET /api/auth/oauth2` ✅ (only when `OAUTH2_CLIENT_*` are set)
- Same pattern as github start.

### `GET /api/auth/oauth2/callback` ✅
- Same pattern; uses `OAUTH2_CLIENT_USER_INFO_URL` for profile.

---

## 2. Users

### `GET /api/users` ✅
- Auth: bearer.
- Response: array of `{ id, username, email, firstName, lastName, isActive, role }`.
- Trigger: admin Users page.

### `GET /api/users/count` ✅
- Response: `{ "count": N }`.

### `GET /api/users/profile` ✅
- Response: `{ id, username, email, firstName, lastName, role, userGroups, permissions }`.

### `PUT /api/users/profile` ✅
- Body: partial `{ firstName?, lastName?, email? }`. Strips role/active.
- 204 on success.

### `PUT /api/users/profile/password` ✅
- Body: `{ currentPassword, newPassword }`. newPassword must be ≥ 8 chars.
- 204 on success; 403 on wrong current password.

### `POST /api/users/profile/avatar` ✅
- Multipart `avatar` field. Cap 1 MiB.
- Stored as `data:` URL in `User.image`.

### `POST /api/users` ✅
- Body: `{ username, email, password, firstName?, lastName?, roleId?, isActive? }`.
- 201 with summary.

### `GET /api/users/username/{username}` ✅
- 200 with full user shape (no password); 404 on miss.

### `GET /api/users/id/{id}` ✅
- Same as above by id.

### `PUT /api/users/id/{id}` ✅
- Body: partial `{ firstName?, lastName?, email?, roleId?, isActive? }`.

### `DELETE /api/users/id/{id}` ✅
- 204 on success.

### `PUT /api/users/id/{id}/password` ✅ (admin)
- Body: `{ password }`. No current-password check.

---

## 3. Roles + groups + permissions

### `GET /api/roles` ✅
- Slim list `[{ id, name, description }]`.

### `GET /api/roles/full` ✅
- With permissions inlined: `[{ id, name, description, permissions: [{id, resource, action}] }]`.

### `POST /api/roles` ✅
- Body: `{ name, description, permissions: [{ resource, action }] }`.

### `PUT /api/roles/{id}` ✅
- Body same as POST. Replaces permission set wholesale.

### `DELETE /api/roles/{id}` ✅

### `GET /api/groups` ✅
- `[{ id, name, description }]`.

### `POST /api/groups` ✅
- Body: `{ name, description }`.

### `PUT /api/groups/{id}` ✅
- Body same.

### `DELETE /api/groups/{id}` ✅
- Cascades the `_UserToUserGroup` pivot.

---

## 4. Tokens

### `GET /api/tokens/my` ✅
- The current user's tokens.
- Response: `[{ id, name, createdAt, expiresAt, isActive }]`.

### `POST /api/tokens/my` ✅
- Body: `{ name, expiresAt }` (RFC 3339).
- Response: `{ name, token, expiresAt }`. The JWT is only returned here;
  the row stores metadata, never the secret.

### `DELETE /api/tokens/my/{id}` ✅

### `GET /api/tokens` ✅ (admin)
- All tokens with `{ user: {id, username, email} }` join.

### `POST /api/tokens/user/{userId}` ✅ (admin)
- Body same as `/my`. Mints for an arbitrary user.

### `DELETE /api/tokens/{id}` ✅ (admin)

---

## 5. Audit

### `GET /api/audit?limit=N` ✅
- Limit defaults to 100, clamped to [1, 1000].
- Response: `{ audit: [...], count, limit }`.

### `GET /api/audit/app/{pipeline}/{phase}/{app}?limit=N` ✅

---

## 6. Notifications

Wire envelope: `{ success: bool, data?: ..., message?: string }`.

### `GET /api/notifications` ✅
### `GET /api/notifications/{id}` ✅
### `POST /api/notifications` ✅
- Body: `{ name, enabled, type, pipelines: [], events: [], config: {...} }`.
- `type` ∈ {slack, webhook, discord}; `config.url` required;
  slack also needs `config.channel`.
### `PUT /api/notifications/{id}` ✅
### `DELETE /api/notifications/{id}` ✅

---

## 7. Config

### `GET /api/config` ✅
- Returns `{ settings, secrets }`. `settings` is the Kuso CR spec map
  (not the typed config); `secrets` is currently `{}` (TS server returns
  a list of optional integration env keys but we don't surface them).

### `POST /api/config` ✅
- Body: `{ settings: {...} }`. Replaces the Kuso CR spec wholesale.

### `GET /api/config/banner` ✅
- `{ show, text, bgcolor, fontcolor }` from `kuso.banner` or defaults.

### `GET /api/config/registry` ✅
- The `registry` block from the Kuso CR spec.

### `GET /api/config/clusterissuer` ✅
- `{ clusterissuer: "letsencrypt-prod" }` (or override from spec).

### `GET /api/config/templates` ✅
- `{ enabled, catalogs }`.

### `GET /api/config/runpacks` ✅
- Full runpack list with phases + capabilities joined.

### `DELETE /api/config/runpacks/{id}` ✅
- Cascades the 3 phase rows.

### `GET /api/config/podsizes` ✅
### `POST /api/config/podsizes` ✅
- Body: `{ id?, name, cpuLimit, memoryLimit, cpuRequest, memoryRequest, description? }`.
### `PUT /api/config/podsizes/{id}` ✅
### `DELETE /api/config/podsizes/{id}` ✅

> **Deferred from TS** (rare admin paths): `GET /api/config/setup/check/{component}`, `POST /api/config/setup/kubeconfig/validate`, `POST /api/config/setup/save`. These are install-flow only and the kubeconfig + namespace handling is done by `hack/install.sh` outside the server.

---

## 8. Projects

### `GET /api/projects` ✅
- All `KusoProject` CRs in the namespace.

### `POST /api/projects` ✅
- Body: `{ name, description?, baseDomain?, defaultRepo: { url, defaultBranch? }, github?: { installationId }, previews?: { enabled, ttlDays? } }`.
- 409 on duplicate name; 400 on missing fields.

### `GET /api/projects/{project}` ✅
- Rolled-up describe: `{ project, services, environments }`.
- *(Addon list is fetched separately; the TS Describe also bundled
  addons but the Vue UI calls them via /addons anyway.)*

### `DELETE /api/projects/{project}` ✅
- Cascade-deletes envs + services. Addon deletion happens via the
  addons handler.

### `GET /api/projects/{project}/services` ✅
### `POST /api/projects/{project}/services` ✅
- Body: `{ name, repo?: { url, path }, runtime?, port?, domains?, envVars?, scale?, sleep? }`.
- Auto-creates the production env on success.

### `GET /api/projects/{project}/services/{service}` ✅
### `DELETE /api/projects/{project}/services/{service}` ✅
- Cascades to every env in this service.

### `GET /api/projects/{project}/services/{service}/env` ✅
- `{ envVars: [{ name, value, valueFrom }] }`. Secret-backed entries
  redact the value.

### `POST /api/projects/{project}/services/{service}/env` ✅
- Body: `{ envVars: [...] }`. Replaces the service's env list wholesale.

### `GET /api/projects/{project}/envs` ✅
### `GET /api/projects/{project}/envs/{env}` ✅ (rejects cross-project guesses)
### `DELETE /api/projects/{project}/envs/{env}` ✅
- Refuses production envs.

### `GET /api/projects/{project}/addons` ✅
### `POST /api/projects/{project}/addons` ✅
- Body: `{ name, kind, version?, size?, ha?, storageSize?, database? }`.
- After create, every env in the project gets its `envFromSecrets`
  patched to include the new addon's `<cr-name>-conn` secret.
### `DELETE /api/projects/{project}/addons/{addon}` ✅
- Same envFromSecrets refresh after delete.

---

## 9. Secrets (per-service)

### `GET /api/projects/{project}/services/{service}/secrets?env=X` ✅
- `{ keys: [...], env: null|X }`. Values are NEVER returned; only keys.

### `POST /api/projects/{project}/services/{service}/secrets` ✅
- Body: `{ key, value, env? }`.
- Race-free merge-patch (§6.4): two parallel POSTs for distinct keys
  cannot lose updates.
- Bumps `spec.secretsRev` on affected env(s) so helm-operator rolls the
  Deployment (§6.2).

### `DELETE /api/projects/{project}/services/{service}/secrets/{key}?env=X` ✅
- 404 when the key wasn't present (or got concurrent-removed).
- Last-key removal also deletes the Secret CR + detaches it from
  envFromSecrets.

---

## 10. Builds

### `GET /api/projects/{project}/services/{service}/builds` ✅
- Newest first.

### `POST /api/projects/{project}/services/{service}/builds` ✅
- Body: `{ branch?, ref? }`. Empty body is legal.
- Image tag = first 12 chars of SHA, otherwise verbatim.
- Branch → SHA resolution via GitHub App requires `installationId` on
  the project's spec.github (Phase 6).

### Background poller
- Every 30s by default (disable with `KUSO_BUILD_POLLER_DISABLED=true`).
- Reads the kaniko Job for each build whose status.phase isn't
  succeeded/failed. JobComplete=True → mark succeeded + patch the
  production env's spec.image with the new tag. JobFailed=True →
  mark failed.

---

## 11. Logs

### `GET /api/projects/{project}/services/{service}/logs?env=X&lines=N` ✅
- Returns `{ project, service, env, lines: [{pod, line}] }`.
- Default 200 lines, capped 2000. env defaults to "production".
- One-shot tail (no streaming yet).

---

## 12. Kubernetes admin

### `GET /api/kubernetes/events?namespace=X` ✅
- Newest first by lastTimestamp, capped at 200.

### `GET /api/kubernetes/storageclasses` ✅
### `GET /api/kubernetes/domains` ✅
- Union of every Ingress host across the cluster.

> **Deferred from TS**: `/api/kubernetes/contexts`. Kuso server-go is
> always single-context (in-cluster ServiceAccount or KUBECONFIG with
> a specific current-context).

---

## 13. GitHub App

### `POST /api/webhooks/github` ✅
- Auth: HMAC sha256 against `X-Hub-Signature-256` using
  `GITHUB_APP_WEBHOOK_SECRET`. No bearer.
- Events handled:
  - `push` → trigger build for every service in the project whose
    repo URL matches and whose default branch matches.
  - `pull_request` opened/reopened/synchronize → ensure preview
    KusoEnvironment per service + trigger a build.
  - `pull_request` closed → delete preview env.
  - `installation` (created / new_permissions_accepted / suspend /
    unsuspend / deleted) → refresh or delete the
    `GithubInstallation` cache row.
  - `installation_repositories` → refresh that installation's repo
    list.

### `GET /api/github/setup-callback` ✅
- Public. Refreshes the installation cache + redirects to
  `/projects/new?github=installed`.

### `GET /api/github/install-url` ✅ (auth)
- `{ configured: bool, url: "https://github.com/apps/<slug>/installations/new" }`.

### `GET /api/github/installations` ✅
- Cached list with repos inlined.

### `GET /api/github/installations/{id}/repos` ✅
- Cached repo list for one installation.

### `POST /api/github/installations/refresh` ✅
- Forces a refresh from GitHub.

### `GET /api/github/installations/{id}/repos/{owner}/{repo}/tree?branch=X&path=Y` ✅
- Recursive git tree at HEAD of branch. Optional path-prefix filter.

### `POST /api/github/detect-runtime` ✅
- Body: `{ installationId, owner, repo, branch, path? }`.
- Response: `{ runtime, port, reason }`.
- Rules: Dockerfile → port from EXPOSE; index.html-only → static port
  80; package.json → nixpacks 3000; go.mod / Cargo.toml / Python →
  nixpacks 8080; fallback nixpacks 8080.

---

## 14. SPA (catch-all)

Any GET that doesn't match an `/api/*` or `/healthz` or
`/api/webhooks/*` route returns `index.html` from the embedded Vue
bundle (`internal/web/dist`). The Vue router takes over from there.

---

## Triggers index

| Vue page / CLI command           | Endpoints exercised                                                                                                                                        |
| -------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Login page                       | `GET /api/auth/methods`, `POST /api/auth/login`, `GET /api/auth/github` (start), `GET /api/auth/oauth2` (start)                                            |
| Every authenticated page         | `GET /api/auth/session`, `GET /api/status`                                                                                                                  |
| Dashboard                        | `GET /api/users/count`, `GET /api/audit?limit=20`, `GET /api/projects`                                                                                     |
| Projects list                    | `GET /api/projects`                                                                                                                                          |
| Project detail                   | `GET /api/projects/{p}`, `GET /api/projects/{p}/services`, `GET /api/projects/{p}/envs`, `GET /api/projects/{p}/addons`                                    |
| Project create                   | `GET /api/github/install-url`, `GET /api/github/installations`, `GET /api/github/installations/{id}/repos`, `POST /api/github/detect-runtime`, `POST /api/projects` |
| Service detail                   | `GET /api/projects/{p}/services/{s}`, `GET /api/projects/{p}/services/{s}/env`, `GET /api/projects/{p}/services/{s}/secrets`, `GET /api/projects/{p}/services/{s}/builds`, `GET /api/projects/{p}/services/{s}/logs` |
| Build trigger                    | `POST /api/projects/{p}/services/{s}/builds`                                                                                                                 |
| Secret edit                      | `POST /api/projects/{p}/services/{s}/secrets`, `DELETE /api/projects/{p}/services/{s}/secrets/{k}`                                                          |
| Settings → Users                 | `GET /api/users`, `POST /api/users`, `PUT /api/users/id/{id}`, `DELETE /api/users/id/{id}`                                                                   |
| Settings → Roles                 | `GET /api/roles/full`, `POST /api/roles`, `PUT /api/roles/{id}`, `DELETE /api/roles/{id}`                                                                    |
| Settings → Groups                | `GET /api/groups`, `POST /api/groups`, `PUT /api/groups/{id}`, `DELETE /api/groups/{id}`                                                                     |
| Settings → Notifications         | full `/api/notifications` CRUD                                                                                                                                |
| Settings → Config                | `GET /api/config`, `POST /api/config`, runpacks/podsizes CRUD                                                                                                |
| Settings → Tokens (My / Admin)   | `/api/tokens/my*` and `/api/tokens*`                                                                                                                          |
| GitHub webhooks (server-side)    | `POST /api/webhooks/github`                                                                                                                                   |
| GitHub install redirect          | `GET /api/github/setup-callback`                                                                                                                              |
| `kuso login`                     | `POST /api/auth/login`                                                                                                                                        |
| `kuso project list`              | `GET /api/projects`                                                                                                                                           |
| `kuso project create`            | `POST /api/projects`                                                                                                                                          |
| `kuso project service add`       | `POST /api/projects/{p}/services`                                                                                                                             |
| `kuso build trigger`             | `POST /api/projects/{p}/services/{s}/builds`                                                                                                                  |
| `kuso secret set` / `unset`      | `POST` / `DELETE` `/api/projects/{p}/services/{s}/secrets[/{k}]`                                                                                              |
| `kuso logs`                      | `GET /api/projects/{p}/services/{s}/logs`                                                                                                                     |
| `kuso token issue` / `list` / `revoke` | `/api/tokens/my*`                                                                                                                                       |

---

## Known parity deferrals

These TS endpoints are intentionally not ported in the initial cut.
None are load-bearing for the cutover.

| Endpoint                                          | Reason                                                                                  |
| ------------------------------------------------- | --------------------------------------------------------------------------------------- |
| `GET /api/config/setup/check/{component}`         | Install-flow only; `hack/install.sh` covers it.                                         |
| `POST /api/config/setup/kubeconfig/validate`      | Same.                                                                                   |
| `POST /api/config/setup/save`                     | Same.                                                                                   |
| `GET /api/kubernetes/contexts`                    | Go server is single-context.                                                             |
| WebSocket gateway (events.gateway.ts)             | Streaming logs / interactive console — replaced by the one-shot `/logs` route for now. |
| Templates module                                  | Out of scope per project decision (you create from custom repos).                       |
