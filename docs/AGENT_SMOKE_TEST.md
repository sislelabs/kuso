# Agent smoke test

A standardised end-to-end protocol for verifying kuso functionality
against a live cluster. Designed for AI agents to execute autonomously
after any release that touches the user-facing platform surface
(CLI, server, operator, helm charts).

Total runtime: ~15 minutes on a warm cluster.

---

## When to run

- After every `make ship` of a release that touches one of:
  server-go, operator, helm-charts, deploy/, cli/.
- Before claiming a feature is "done end-to-end."
- As part of post-incident verification.

Skip when the change is purely doc/comment/test or non-platform
(e.g. mcp/, web/-only without API changes).

## Pre-flight

Read `agent-target.local.json` for cluster endpoint + CLI binary + SSH
key. Confirm the CLI is up to date (matches the deployed server) — if
not, rebuild via `cd cli && go build -o /tmp/kuso ./cmd` or pull the
new release asset:

```sh
CLI=dist/kuso-darwin-arm64
$CLI doctor   # all five PASS expected
$CLI upgrade --check    # CLI version should match server version
```

Pick a test project name that doesn't collide. Convention: `smoke`.

Test repo: any small public web service with one or two routes and a
Postgres-friendly env (`DATABASE_URL` is the de-facto kuso convention).
Either reuse the demo repos or substitute a project-internal one — the
protocol below assumes a Go/Postgres backend at
`https://github.com/ivo9999/kuso-demo-todo-api` exposing `/healthz` +
`/api/todos`.

## Protocol

Every step has an explicit **pass criterion**. If a step fails, stop
and surface the failure — don't paper over it with a retry, the
operator wants the signal.

### 1. Create the project

```sh
$CLI project create smoke \
  --repo https://github.com/ivo9999/kuso-demo-todo-api \
  --branch main \
  --domain smoke.sislelabs.com
```

**Pass**: stdout shows `project smoke created`. `$CLI get projects`
lists smoke with the correct repo + branch.

### 2. Add a service

```sh
$CLI service add smoke api --port 8080 --runtime nixpacks
```

**Pass**: stdout shows `service smoke/api added`.
`$CLI project describe smoke` reports `services (1), environments (1), addons (0)`.

### 3. Add a Postgres addon

```sh
$CLI project addon add smoke db --kind postgres --version 16 --size small
```

**Pass**: stdout shows `addon smoke/db (postgres) added`. The
addon-conn Secret materialises within 60s:

```sh
until kubectl -n kuso get secret smoke-db-conn 2>/dev/null; do sleep 5; done
```

### 4. Wire env-var ref + a plain marker

```sh
$CLI env set smoke api 'DATABASE_URL=${{ db.DATABASE_URL }}' 'SMOKE_TAG=baseline'
$CLI env list smoke api
```

**Pass**: `DATABASE_URL` is rendered as type `secret` (the envref auto-
translated to a `secretKeyRef`); `SMOKE_TAG` is rendered as type `plain`.

### 5. Trigger build + wait for terminal state

```sh
$CLI build trigger smoke api
# poll until terminal:
until $CLI build list smoke api 2>/dev/null \
    | grep -E 'succeeded|failed' | head -1; do sleep 15; done
```

**Pass**: build status is `succeeded`. Allow up to 5 minutes for cold-cache
nixpacks builds.

### 6. Wait for deployment + verify the live API

```sh
until kubectl -n kuso get deploy smoke-api-production \
    -o jsonpath='{.status.readyReplicas}' 2>/dev/null | grep -q '^1'; do
  sleep 5
done
curl -fsS https://api.smoke.sislelabs.com/healthz \
    -w '\nstatus=%{http_code}\n'
```

**Pass**: HTTP 200 with `{"ok":true}` (or whatever the test service's
healthz returns). If 502, the project's NetworkPolicy stack is wrong —
look at `kubectl -n kuso get networkpolicy | grep smoke` and confirm
`smoke-allow-platform` exists with traefik+cert-manager allowed.

### 7. Roundtrip the database

```sh
curl -fsS -X POST https://api.smoke.sislelabs.com/api/todos \
    -H 'Content-Type: application/json' \
    -d '{"title":"smoke-test"}' \
    -w '\nstatus=%{http_code}\n'
curl -fsS https://api.smoke.sislelabs.com/api/todos
```

**Pass**: POST returns 201 with a created row; GET returns a list
containing the row. This proves the addon→envref→pod chain.

### 8. Fire a one-shot run

```sh
$CLI run smoke api --timeout-seconds 60 -- sh -c 'echo "from-run"; echo $SMOKE_TAG'
# poll until terminal:
RUN=$(kubectl -n kuso get kusoruns -l kuso.sislelabs.com/service=smoke-api \
    -o jsonpath='{.items[-1:].metadata.name}')
until kubectl -n kuso get kusorun "$RUN" \
    -o jsonpath='{.metadata.annotations.kuso\.sislelabs\.com/run-phase}' \
    2>/dev/null | grep -E 'succeeded|failed|cancelled'; do sleep 5; done
```

**Pass**: phase=succeeded. The pod inherits the service's env (it sees
`SMOKE_TAG=baseline` from step 4).

### 9. Cancel a long-running run

```sh
$CLI run smoke api --timeout-seconds 300 -- sleep 120
RUN=$(kubectl -n kuso get kusoruns -l kuso.sislelabs.com/service=smoke-api \
    -o jsonpath='{.items[-1:].metadata.name}')
# wait for it to enter running:
until kubectl -n kuso get kusorun "$RUN" \
    -o jsonpath='{.metadata.annotations.kuso\.sislelabs\.com/run-phase}' \
    2>/dev/null | grep -q running; do sleep 4; done
# cancel via API (CLI cancel is reserved for builds today):
TOK=$(awk '/HOST:/ {print $2}' ~/.kuso/credentials.yaml)
curl -fsS -X POST -H "Authorization: Bearer $TOK" \
    "https://HOST/api/projects/smoke/runs/$RUN/cancel" \
    -w '\nstatus=%{http_code}\n'
# verify:
kubectl -n kuso get kusorun "$RUN" \
    -o jsonpath='{.metadata.annotations.kuso\.sislelabs\.com/run-phase}{"\n"}'
```

**Pass**: HTTP 204, then phase=cancelled.

### 10. Trigger second build + rollback

```sh
$CLI build trigger smoke api
# wait for second build's terminal state, then rollback to FIRST build:
FIRST=$(kubectl -n kuso get kusobuilds -l kuso.sislelabs.com/service=smoke-api \
    -o jsonpath='{.items[0].metadata.name}')   # newest-first ordering
curl -fsS -X POST -H "Authorization: Bearer $TOK" \
    "https://HOST/api/projects/smoke/services/api/builds/$FIRST/rollback"
```

**Pass**: response includes `promoted-build` annotation pointing at
$FIRST. `kubectl -n kuso get kusoenvironment smoke-api-production
-o jsonpath='{.spec.image.tag}'` matches the FIRST build's tag.

### 11. Schedule a cron

```sh
$CLI cron add smoke api --name tick --schedule '*/5 * * * *' --cmd 'echo tick'
kubectl -n kuso get cronjobs | grep smoke-api-tick
```

**Pass**: kube CronJob exists with the schedule. Wait for the first
firing if you want to verify a Job lands; otherwise the existence of
the CronJob is enough — kuso's render path is the load-bearing piece.

### 12. Tail logs + log search

```sh
$CLI logs smoke api | head -5
# search via API:
curl -fsS -H "Authorization: Bearer $TOK" \
    "https://HOST/api/projects/smoke/services/api/logs/search?q=listening"
```

**Pass**: logs are present (shipper landed them in LogLine);
search returns a JSON envelope with `lines[]` matching the query.

### 13. MCP plan verb

The MCP `plan` tool POSTs to `/api/projects/{p}/apply?dryRun=1`.
Exercise the endpoint directly:

```sh
curl -fsS -X POST -H "Authorization: Bearer $TOK" \
    -H 'Content-Type: application/x-yaml' \
    "https://HOST/api/projects/smoke/apply?dryRun=1" \
    --data-binary $'project: smoke\nservices:\n  - name: api\n    repo: https://github.com/ivo9999/kuso-demo-todo-api\n    runtime: nixpacks\n    port: 8080\n  - name: api2\n    repo: https://github.com/ivo9999/kuso-demo-todo-api\n    runtime: nixpacks\n    port: 8080\naddons:\n  - name: db\n    kind: postgres\n'
```

**Pass**: response is `{"servicesToCreate":["api2"], "servicesToUpdate":["api"], "addonsToUpdate":["db"], ...}`.

### 14. Audit + notify integration

Audit log:

```sh
kubectl -n kuso exec -i kuso-postgres-1 -c postgres -- \
    env PGPASSWORD=$DB_PW psql -h kuso-postgres-rw -U kuso -d kuso -c \
    "SELECT action, COUNT(*) FROM \"Audit\"
     WHERE \"createdAt\" > NOW() - INTERVAL '1 hour'
       AND (pipeline = 'smoke' OR action LIKE 'run.%')
     GROUP BY action ORDER BY action;"
```

**Pass**: rows include at minimum `addon.create`, `service.setEnv`,
`run.create`, `run.cancel`.

Notification events:

```sh
kubectl -n kuso exec -i kuso-postgres-1 -c postgres -- \
    env PGPASSWORD=$DB_PW psql -h kuso-postgres-rw -U kuso -d kuso -c \
    "SELECT type, COUNT(*) FROM \"NotificationEvent\"
     WHERE \"createdAt\" > NOW() - INTERVAL '1 hour'
       AND project = 'smoke'
     GROUP BY type ORDER BY type;"
```

**Pass**: rows include at minimum `build.succeeded`, `run.started`,
`run.succeeded`.

`$DB_PW` comes from `kubectl -n kuso get secret kuso-postgres-conn -o jsonpath='{.data.password}' | base64 -d`.

### 15. UI smoke (manual — operator drives)

This step is a human-driven sub-test the agent flags as
"please verify in the browser" rather than executes itself:

- Open `https://HOST/projects/smoke` in a browser.
- Confirm every overlay tab renders: Deployments, Variables, Metrics,
  Logs, Errors, Crons, Runs, Settings.
- The Runs tab shows the runs fired in steps 8–9 with their phase
  pills. The composer (command + env overlay) accepts a fresh run.
- Settings → Variables shows DATABASE_URL as `<secret>` and SMOKE_TAG.

Skip the UI check on a headless-agent run; capture it for the human
review.

### 16. Cleanup

```sh
$CLI project delete smoke
# wait for cascade:
until kubectl -n kuso get kusoproject smoke 2>&1 | grep -q NotFound; do
  sleep 5
done
# verify nothing left:
kubectl -n kuso get kusoservices,kusoaddons,kusoruns,networkpolicies \
    -l kuso.sislelabs.com/project=smoke
```

**Pass**: cascade completes (typically ~60s) and the post-delete query
returns "No resources found." If anything lingers past 5 minutes, the
finalizer is stuck — `kubectl -n kuso describe kusoproject smoke` will
show why.

---

## Marketplace one-click deploy

The marketplace ships curated app templates (embedded kuso.yaml + a
prompt manifest). A template renders to kuso.yaml server-side and is
created through the same `POST /api/projects/{p}/apply` config-as-code
path as `kuso import compose`, so this smoke exercises render →
project-create → apply → cert → rollout end to end. Uptime Kuma is the
zero-addon showcase (single `runtime: image` service + a volume), so it
reaches Ready without waiting on a datastore.

1. **Catalog lists.** `kuso marketplace list` → prints ≥8 apps
   (uptime-kuma, umami, n8n, vaultwarden, gitea, metabase, plausible,
   listmonk). `kuso marketplace info uptime-kuma` → shows its one
   required `host` (domain) prompt.

2. **Dry-run render writes nothing.**
   `kuso marketplace deploy uptime-kuma --project mkt-smoke --set host=mkt-smoke.<baseDomain> --dry-run`
   → prints a kuso.yaml with `project: mkt-smoke` and the `host`
   substituted into the service domain, plus a plan (`[service] …`,
   `[domain] …`). `kuso get projects -o json` still shows no `mkt-smoke`.

3. **Real deploy.** Drop `--dry-run`. The command creates project
   `mkt-smoke` (409-tolerant), applies, and reports success. Watch the
   rollout: `kuso status mkt-smoke` then `kuso get services mkt-smoke -o json`
   → the service reaches Ready. (A partial apply returns per-step
   errors, not a hard failure — the CLI prints them; the web dialog
   surfaces `result.errors` instead of a false success.)

4. **Live URL + cert.** `curl -I https://mkt-smoke.<baseDomain>` → 200
   (allow ~1 min for the ACME cert + first rollout). Browser shows a
   real Let's Encrypt cert.

5. **Web parity (optional).** `/marketplace` in the dashboard shows the
   card grid; deploying Uptime Kuma via the dialog lands on the project
   canvas.

6. **Cleanup.** Delete the `mkt-smoke` project (dashboard or CLI); confirm
   it drains as in the teardown step above.

Multi-addon path (deeper check, optional): `plausible` provisions a
postgres **and** a clickhouse addon and wires both via
`${{ plausible-db.DATABASE_URL }}` / `${{ plausible-ch.CLICKHOUSE_URL }}`.
Use it to confirm addon conn-secret keys resolve; it takes longer to
reach Ready than uptime-kuma.

---

## Failure modes seen in past runs

These are real bugs the smoke test caught against `kuso.sislelabs.com`.
Recording them here so future agents recognise the shape:

- **Marketplace `uptime-kuma` crash-loops `setpriv: setgroups failed:
  Operation not permitted` (exit 127)** — the image starts as root and
  self-drops to its app user at runtime, which needs SETUID/SETGID caps.
  kuso drops ALL container capabilities + `allowPrivilegeEscalation:
  false` unconditionally, and there is no per-service securityContext
  knob in the KusoService CRD. Caught on the first live marketplace
  smoke (v0.18.105, 2026-07-02). Same class as the named-user runAsNonRoot
  fix (v0.13.1). Marketplace templates must use fixed non-root images or
  rootless variants; a real fix is an opt-in caps/securityContext field.

- **Build pod 503s waiting for buildkitd** → default-deny NetworkPolicy
  doesn't allow build-pod egress to `kuso-buildkitd:1234`. Fixed in
  v0.13.4.
- **HTTP 502 from `api.smoke...`** → platform-NP doesn't allow ingress
  from the `traefik` namespace (k3s default). Fixed in v0.13.5.
- **`secret "X-shared" not found` on KusoRun pod** → kusorun chart
  didn't mark envFromSecrets `optional: true`. Fixed in v0.13.2.
- **`kusoruns is forbidden`** on POST /runs → kuso-server ClusterRole
  missing kusoruns verbs. Fixed in v0.13.2.
- **Run pods `CreateContainerConfigError` on every named-user image** →
  kusorun chart had `runAsNonRoot: true` at pod level. Fixed in v0.13.1.

A new bug surfaces here → fix it, ship the patch, re-run the smoke
test from step 1.

---

## What this DOESN'T cover

By design the smoke test stays inside one project. The following
surfaces need their own runbooks:

- Cluster-level admin (instance-pg, instance-secrets, instance-addons,
  node management, OAuth provider config).
- Settings UI (users, roles, groups, audit log explorer, backups,
  GitHub App config).
- Multi-project preview environments (the `/previews` flow).
- Disaster recovery (backup → restore round-trip).

Future work: extend this doc with a `BACKUP_RESTORE_SMOKE.md` and a
`PREVIEWS_SMOKE.md` once those surfaces have stable agent-runnable
contracts.
