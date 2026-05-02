# Kuso Go server: live test plan / runbook

> **Update (2026-05):** the TS server has been retired. The runbook
> below was written for the side-by-side cutover; the surviving value
> is the verification checklist (sections 1–9). Section 10 (cutover) +
> section 11 (rollback) are kept for historical reference but no
> longer apply — there is no TS deployment to roll back to.

Read alongside `WORKFLOWS.md`. This document is the journey-derived
runbook to execute against the live Hetzner cluster.

The goal is end-to-end verification: every workflow the TS server
serves, exercised against the Go binary, with pass/fail recorded
inline.

---

## 0. Pre-flight

### 0.1 Build + push the Go image

From the kuso repo root on a workstation:

```sh
docker build -f server-go/Dockerfile -t ghcr.io/sislelabs/kuso-server-go:v0.2.0-rc1 .
docker push ghcr.io/sislelabs/kuso-server-go:v0.2.0-rc1
```

Verify the image runs locally:

```sh
docker run --rm -p 13000:3000 \
  -e JWT_SECRET=devsecret \
  ghcr.io/sislelabs/kuso-server-go:v0.2.0-rc1
# in another shell:
curl -fsS localhost:13000/healthz   # → {"status":"ok","version":"v0.2.0-dev"}
curl -fsS localhost:13000/          # → text/html with placeholder index
```

### 0.2 Snapshot the live SQLite

The Go server reads the same SQLite shape Prisma writes. Before
cutover, copy the live file so you have something to fall back on:

```sh
kubectl cp kuso/$(kubectl get pod -n kuso -l app=kuso-server -o jsonpath='{.items[0].metadata.name}'):/app/server/db/kuso.sqlite ./kuso.snapshot.db
```

Open it locally with the Go binary to confirm schema compatibility:

```sh
docker run --rm \
  -e JWT_SECRET=devsecret \
  -e KUSO_DB_PATH=/data/kuso.snapshot.db \
  -v "$PWD:/data" \
  ghcr.io/sislelabs/kuso-server-go:v0.2.0-rc1
# Should boot cleanly. Stop with Ctrl-C.
```

### 0.3 Side-by-side deploy

Apply a parallel Deployment + Service that points at the Go image. The
TS server keeps serving production traffic.

`deploy/server-go.yaml` (new file you write):

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: kuso-server-go
  namespace: kuso
spec:
  replicas: 1
  selector: { matchLabels: { app: kuso-server-go } }
  template:
    metadata: { labels: { app: kuso-server-go } }
    spec:
      serviceAccountName: kuso-server   # reuse TS RBAC
      containers:
        - name: server
          image: ghcr.io/sislelabs/kuso-server-go:v0.2.0-rc1
          ports: [{ containerPort: 3000 }]
          env:
            - { name: JWT_SECRET, valueFrom: { secretKeyRef: { name: kuso-server, key: JWT_SECRET } } }
            - { name: KUSO_SESSION_KEY, valueFrom: { secretKeyRef: { name: kuso-server, key: KUSO_SESSION_KEY } } }
            - { name: KUSO_NAMESPACE, value: kuso }
            - { name: KUSO_DB_PATH, value: /data/kuso.sqlite }
            # GitHub App
            - { name: GITHUB_APP_ID, valueFrom: { secretKeyRef: { name: kuso-github-app, key: APP_ID } } }
            - { name: GITHUB_APP_PRIVATE_KEY, valueFrom: { secretKeyRef: { name: kuso-github-app, key: PRIVATE_KEY } } }
            - { name: GITHUB_APP_WEBHOOK_SECRET, valueFrom: { secretKeyRef: { name: kuso-github-app, key: WEBHOOK_SECRET } } }
            - { name: GITHUB_APP_SLUG, value: <your-app-slug> }
            # GitHub OAuth (sign-in)
            - { name: GITHUB_CLIENT_ID, valueFrom: { secretKeyRef: { name: kuso-server, key: GITHUB_CLIENT_ID } } }
            - { name: GITHUB_CLIENT_SECRET, valueFrom: { secretKeyRef: { name: kuso-server, key: GITHUB_CLIENT_SECRET } } }
            - { name: GITHUB_CLIENT_CALLBACKURL, value: https://kuso-go.example.com/api/auth/github/callback }
            - { name: GITHUB_CLIENT_ORG, value: sislelabs }
          volumeMounts:
            - { name: data, mountPath: /data }
      volumes:
        - { name: data, persistentVolumeClaim: { claimName: kuso-server-data } }
---
apiVersion: v1
kind: Service
metadata: { name: kuso-server-go, namespace: kuso }
spec:
  selector: { app: kuso-server-go }
  ports: [{ port: 80, targetPort: 3000 }]
```

> ⚠️ **SQLite single-writer constraint** (REWRITE.md §6.6): the Go
> Deployment MUST share the same PVC the TS server uses, BUT only one
> server may write at a time. For side-by-side testing, scale the Go
> deployment to 1 and treat the TS deployment as read-only — every
> write workflow below is run *only* against the Go server. Do NOT
> hit `POST` / `PUT` / `DELETE` on the TS server during the test.

Add an Ingress that routes `kuso-go.example.com` to `kuso-server-go`:

```yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: kuso-go
  namespace: kuso
  annotations: { cert-manager.io/cluster-issuer: letsencrypt-prod }
spec:
  ingressClassName: traefik
  tls: [{ hosts: [kuso-go.example.com], secretName: kuso-go-tls }]
  rules:
    - host: kuso-go.example.com
      http:
        paths: [{ path: /, pathType: Prefix, backend: { service: { name: kuso-server-go, port: { number: 80 } } } }]
```

Verify TLS is up:

```sh
curl -fsS https://kuso-go.example.com/healthz
```

---

## 1. Auth journey

Run from the workstation with `KUSO=https://kuso-go.example.com`.

### 1.1 Local password login
- [ ] `curl -X POST $KUSO/api/auth/login -d '{"username":"admin","password":"<pw>"}' -H 'Content-Type: application/json'` returns `200` with `access_token`.
- [ ] Capture token: `TOK=$(jq -r .access_token <<< "$resp")`.
- [ ] `curl -H "Authorization: Bearer $TOK" $KUSO/api/auth/session` returns `200` with `username=admin`, populated `permissions` array, populated feature flags.
- [ ] `curl $KUSO/api/auth/methods` returns `{"local":true,"github":true,"oauth2":...}` matching env.

### 1.2 Bad credentials
- [ ] `POST /api/auth/login` with wrong password returns `401`.
- [ ] `POST /api/auth/login` with unknown user returns `401`. Timing similar to 1.2 (constant-time guard).

### 1.3 GitHub OAuth sign-in (browser-driven)
- [ ] Open `https://kuso-go.example.com/api/auth/github` in a private window.
- [ ] Browser redirects to github.com/login/oauth/authorize. Authorize.
- [ ] Browser lands on `/` with `kuso.JWT_TOKEN` cookie set.
- [ ] Vue UI shows the user as logged in.
- [ ] In SQLite: `SELECT * FROM "GithubUserLink" WHERE userId='<your kuso id>';` returns one row with the github login + access token.

### 1.4 Token issue + use (CLI scenario)
- [ ] `POST /api/tokens/my` with body `{"name":"ci","expiresAt":"<RFC3339 +30d>"}` returns `{name, token, expiresAt}`.
- [ ] `curl -H "Authorization: Bearer <new token>" $KUSO/api/auth/session` returns 200, with `strategy=token` in claims.
- [ ] `GET /api/tokens/my` lists the issued token.
- [ ] `DELETE /api/tokens/my/{id}` returns 204; subsequent use of the JWT keeps working until expiry (JWTs are stateless — the row is metadata only).

---

## 2. Project create + service add journey

### 2.1 Create project from custom repo
- [ ] In the Vue UI, click **New project**. Confirm the GitHub installation picker is populated. If not, hit `POST /api/github/installations/refresh` and reload.
- [ ] Pick a repo. Project name = `smoke`. Default branch = `main`. Previews enabled.
- [ ] Confirm `POST /api/projects` returns 201.
- [ ] `kubectl get kusoprojects.application.kuso.sislelabs.com -n kuso smoke` shows the CR with the right spec fields.

### 2.2 Add a service
- [ ] In the Vue UI: Add Service `web`, runtime auto-detected (verify `POST /api/github/detect-runtime` was called and the response was sane).
- [ ] After save: `POST /api/projects/smoke/services` returns 201.
- [ ] `kubectl get kusoservices -n kuso smoke-web` exists.
- [ ] `kubectl get kusoenvironments -n kuso smoke-web-production` exists, with `spec.kind=production` and a populated `spec.host`.

### 2.3 Per-service plain env vars
- [ ] In the UI, add `LOG_LEVEL=info` to the service env.
- [ ] `GET /api/projects/smoke/services/web/env` returns the new entry.
- [ ] `kubectl get kusoservice -n kuso smoke-web -o jsonpath='{.spec.envVars}'` matches.

### 2.4 Trigger build (manual)
- [ ] In the UI, click **Trigger build** on the production env. Body: `{branch:"main"}`.
- [ ] `kubectl get kusobuilds -n kuso -l kuso.sislelabs.com/service=smoke-web` shows a new CR.
- [ ] Within 1 min, a kaniko Job appears (`kubectl get jobs -n kuso`).
- [ ] Within 5 min, `kubectl get kusobuild -n kuso <name> -o jsonpath='{.status.phase}'` reports `succeeded`.
- [ ] After success, `kubectl get kusoenvironment -n kuso smoke-web-production -o jsonpath='{.spec.image.tag}'` matches the build's tag (image promotion verified).
- [ ] The pod rolls and serves traffic at `https://web-smoke.<baseDomain>`.

### 2.5 Tail logs
- [ ] `GET /api/projects/smoke/services/web/logs?lines=50` returns 50 lines mixing `pod` + `line`.
- [ ] Lines visibly come from the pod that was just rolled.

---

## 3. Secrets journey (CRITICAL — §6.4 race-free patch)

### 3.1 Set + list a single key
- [ ] `POST /api/projects/smoke/services/web/secrets` body `{key:"DB_URL", value:"postgres://test"}` returns 200.
- [ ] `GET /api/projects/smoke/services/web/secrets` returns `keys: ["DB_URL"]`.
- [ ] `kubectl get secret -n kuso smoke-web-secrets -o jsonpath='{.data.DB_URL}' | base64 -d` matches.
- [ ] `kubectl get kusoenvironment -n kuso smoke-web-production -o jsonpath='{.spec.envFromSecrets}'` includes `smoke-web-secrets`.
- [ ] `kubectl get kusoenvironment -n kuso smoke-web-production -o jsonpath='{.spec.secretsRev}'` is non-empty.
- [ ] Pod was rolled after the patch (helm-operator added the annotation).

### 3.2 Resilience probe — 6-way parallel set, distinct keys
On a workstation with `xargs -P 6`:

```sh
seq 1 6 | xargs -P 6 -I {} curl -fsS -X POST $KUSO/api/projects/smoke/services/web/secrets \
  -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"key":"K{}","value":"V{}"}'
```

- [ ] All 6 calls return 200.
- [ ] `GET /api/projects/smoke/services/web/secrets` returns `keys` containing all of K1..K6 (plus DB_URL).
- [ ] `kubectl get secret -n kuso smoke-web-secrets -o jsonpath='{.data}'` shows all 7 keys.
- [ ] **No key is missing.** This is the §6.4 regression check.

### 3.3 Last-key removal cascades
- [ ] Repeatedly `DELETE /api/projects/smoke/services/web/secrets/{key}` for each of K1..K6 + DB_URL.
- [ ] After the last delete: `kubectl get secret -n kuso smoke-web-secrets` returns `NotFound`.
- [ ] `kubectl get kusoenvironment -n kuso smoke-web-production -o jsonpath='{.spec.envFromSecrets}'` no longer includes `smoke-web-secrets`.

### 3.4 Per-env scoping
- [ ] Open a PR on the connected repo. Verify a preview env spins up.
- [ ] Set a key with `env="preview-pr-<n>"`. Confirm only the preview env's `envFromSecrets` got the entry; production's didn't.

---

## 4. Addons journey

### 4.1 Add a postgres addon
- [ ] In the UI, add an addon: name=`pg`, kind=`postgres`, version=`16`.
- [ ] `POST /api/projects/smoke/addons` returns 201.
- [ ] `kubectl get kusoaddons.application.kuso.sislelabs.com -n kuso smoke-pg` exists.
- [ ] **Important**: every env in the project (production + previews) now has `smoke-pg-conn` in `spec.envFromSecrets`. Run:
  ```sh
  kubectl get kusoenvironment -n kuso -l kuso.sislelabs.com/project=smoke -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.spec.envFromSecrets}{"\n"}{end}'
  ```
- [ ] After helm-operator reconciles, the pod sees the connection env vars.

### 4.2 Delete the addon
- [ ] `DELETE /api/projects/smoke/addons/pg` returns 204.
- [ ] `kubectl get kusoaddons -n kuso smoke-pg` returns NotFound.
- [ ] Every env's `envFromSecrets` no longer includes `smoke-pg-conn`.

---

## 5. GitHub PR / preview-env flow

### 5.1 Open a PR
- [ ] Open a PR on the connected repo against `main`.
- [ ] Within 30s the GitHub webhook lands at `POST /api/webhooks/github`.
  Verify in the server logs.
- [ ] `kubectl get kusoenvironments -n kuso -l kuso.sislelabs.com/project=smoke,kuso.sislelabs.com/env=preview-pr-<N>` returns 1 env.
- [ ] Spec has `kind=preview`, `branch=<head ref>`, populated `host`, populated `ttl.expiresAt`.
- [ ] A KusoBuild was triggered for the PR's head SHA.
- [ ] Within 5 min the preview env serves traffic at the auto-generated host.

### 5.2 Push another commit to the PR (synchronize)
- [ ] Push to the PR branch.
- [ ] `synchronize` webhook arrives.
- [ ] Preview env recreated (delete + create) — operator reconciles
  helm release.
- [ ] New build triggered.

### 5.3 Close the PR
- [ ] Close the PR.
- [ ] `closed` webhook arrives.
- [ ] Preview env CR is deleted within 30s.

### 5.4 Webhook signature failure
- [ ] `curl -X POST $KUSO/api/webhooks/github -d '{}' -H 'X-GitHub-Event: push' -H 'X-Hub-Signature-256: sha256=deadbeef'` returns 400 (bad signature).
- [ ] `curl -X POST $KUSO/api/webhooks/github -d '{}'` (no signature header) returns 400.

### 5.5 Preview-cleanup safety net
- [ ] Set the preview env's `spec.ttl.expiresAt` to 1 minute in the past (manual `kubectl patch`).
- [ ] Within 5 minutes (next preview-cleanup tick), the env is gone.
- [ ] Server logs show `preview-cleanup deleted count=1`.

---

## 6. Admin journey

### 6.1 Users CRUD
- [ ] `GET /api/users` lists current users.
- [ ] `POST /api/users` body `{username:"alice", email:"alice@example.com", password:"hunter2hunter2"}` returns 201.
- [ ] Login as `alice` with that password — succeeds.
- [ ] `PUT /api/users/id/<id>/password` body `{password:"new-pw-12345678"}` (admin) — succeeds.
- [ ] Login as alice with new password.
- [ ] `PUT /api/users/profile/password` body `{currentPassword:"new-pw-12345678", newPassword:"newer-pw-1234"}` from alice's session — succeeds.
- [ ] `DELETE /api/users/id/<id>` from admin — succeeds.

### 6.2 Roles + permissions
- [ ] `POST /api/roles` body `{name:"viewer", description:"read only", permissions:[{resource:"app", action:"read"}]}` returns 201.
- [ ] `GET /api/roles/full` shows the new role with its 1 permission.
- [ ] `PUT /api/roles/<id>` replaces permission set with 2 entries; verify via `/api/roles/full`.
- [ ] `DELETE /api/roles/<id>` 204; permission rows removed (check `_PermissionToRole` table empty for that role).

### 6.3 Groups
- [ ] `POST /api/groups` body `{name:"ops"}` 201.
- [ ] `PUT /api/groups/<id>` updates description.
- [ ] `DELETE /api/groups/<id>` 204.

### 6.4 Notifications
- [ ] `POST /api/notifications` body `{name:"#alerts", enabled:true, type:"slack", pipelines:["smoke"], events:["build-failed"], config:{url:"https://hooks.slack.com/...", channel:"#kuso-alerts"}}` returns 201.
- [ ] `GET /api/notifications` returns the row in the `data` envelope.
- [ ] `PUT /api/notifications/<id>` updates `enabled=false`.
- [ ] `DELETE /api/notifications/<id>` returns 200 with `success:true`.

### 6.5 Audit feed
- [ ] `GET /api/audit?limit=50` returns recent rows. Confirm a `login` action appears for the most recent login.
- [ ] `GET /api/audit/app/<pipeline>/<phase>/<app>` filters correctly.

### 6.6 Settings → config
- [ ] `GET /api/config` returns the full Kuso CR spec map under `settings`.
- [ ] Edit something benign (e.g. `kuso.banner.text="hello"`) via the UI; confirm `POST /api/config` lands and the banner shows up on the next page load via `GET /api/config/banner`.

### 6.7 Runpacks + podsizes
- [ ] `GET /api/config/runpacks` returns a list (may be empty on a fresh DB).
- [ ] `GET /api/config/podsizes` same.
- [ ] `POST /api/config/podsizes` body `{name:"micro", cpuLimit:"100m", memoryLimit:"128Mi", cpuRequest:"50m", memoryRequest:"64Mi"}` 201.
- [ ] `PUT /api/config/podsizes/<id>` updates a field.
- [ ] `DELETE /api/config/podsizes/<id>` 204.

---

## 7. Resilience sweeps

These are the same probes from REWRITE.md §8 acceptance #4.

### 7.1 Kill kuso-server-go
- [ ] `kubectl rollout restart deployment/kuso-server-go -n kuso`.
- [ ] Within 30s, the new pod is ready.
- [ ] `GET /healthz` is back.
- [ ] Open builds (those still running on cluster) are picked up by the
  poller and reach succeeded/failed.

### 7.2 Kill kuso-operator
- [ ] `kubectl rollout restart deployment/kuso-operator-controller-manager -n kuso-operator-system`.
- [ ] No effect on the API server.

### 7.3 Kill registry
- [ ] `kubectl rollout restart deployment/kuso-registry -n kuso`.
- [ ] In-flight build that was about to push reports failure cleanly
  (status.phase=failed, message includes "push" or HTTP error).
- [ ] Trigger a fresh build after the registry recovers — succeeds.

### 7.4 Kill app pod
- [ ] `kubectl delete pod -n kuso -l kuso.sislelabs.com/service=smoke-web`.
- [ ] Pod recreates from the same image; no build needed.

### 7.5 Stuck finalizer recovery
- [ ] `kubectl patch kusoenvironment -n kuso <stuck-env> -p '{"metadata":{"finalizers":[]}}' --type=merge`.
- [ ] Env is removed cleanly.

### 7.6 6-way parallel SAME-key writes
- [ ] Same xargs -P 6 from §3.2 but all writing to the same key with
  different values.
- [ ] All 6 return 200.
- [ ] Last write wins (no error). Acceptable per spec.

### 7.7 Kube events tail under load
- [ ] `GET /api/kubernetes/events?namespace=kuso&limit=200` while a
  build is running — returns events ordered newest-first.

---

## 8. Memory / cold-start (acceptance §8.5–8.6)

### 8.1 Idle memory
- [ ] After 10 minutes of idle: `kubectl top pod -n kuso -l app=kuso-server-go` reports < 80 MiB.

### 8.2 Cold start
- [ ] `kubectl rollout restart deployment/kuso-server-go -n kuso` and time
  until /healthz responds. Should be < 3 seconds.

---

## 9. Vue UI smoke

Browser-driven, with the Vue client pointed at `kuso-go.example.com`.

- [ ] `/` → login page renders, no JS console errors.
- [ ] Log in → dashboard renders. Banner from `kuso.banner` shows
  correctly.
- [ ] Settings → Users / Roles / Groups / Notifications all render and
  data round-trips.
- [ ] Projects list → click into smoke → all tabs (Services, Envs,
  Addons, Builds, Logs, Secrets) render.
- [ ] **No JS console errors** end-to-end.

---

## 10. Cutover

Only after every box above is checked.

### 10.1 Drain TS
- [ ] `kubectl scale deployment/kuso-server -n kuso --replicas=0`.
- [ ] Confirm the kuso-server pod terminated.

### 10.2 Move Ingress
- [ ] Update the production Ingress (`deploy/server.yaml` or whichever
  manifests host point at the kuso UI / API) to route to
  `kuso-server-go` instead of `kuso-server`.
- [ ] Verify TLS keeps working (cert-manager keeps the same secret).

### 10.3 Watch
- [ ] For 1 hour, watch:
  - server logs (no error spam)
  - `kubectl top pod`
  - GitHub webhook deliveries (Settings → Advanced on the App page —
    every delivery should be 204)
  - audit row count growing as expected
- [ ] Trigger one final build via the CLI to confirm everything works.

### 10.4 Retire TS
- [ ] `kubectl delete deployment/kuso-server -n kuso`.
- [ ] Remove the now-dead Ingress / Service that pointed at it.
- [ ] Bump the Go deployment to 2 replicas (only safe AFTER SQLite is
  swapped for the operator-managed Postgres in a follow-up — keep at
  1 until then).
- [ ] Tag a release: `git tag v0.2.0 && git push --tags`. Bump
  `internal/version/VERSION` to drop the `-dev` suffix on `main` if you
  want.

---

## 11. Rollback

If anything fails during 10.3:

```sh
kubectl scale deployment/kuso-server -n kuso --replicas=1
kubectl scale deployment/kuso-server-go -n kuso --replicas=0
# revert Ingress to point at kuso-server
```

The TS server reads the same SQLite file. Rollback time is bounded by
how long `kubectl rollout status` takes — typically under a minute.
