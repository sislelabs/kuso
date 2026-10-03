# Uptime checks — design

**Date:** 2026-10-02
**Status:** approved-pending-review
**Author:** ivo (with Claude)

## Goal

kuso pings every production service once a minute and posts to Discord when one stops
answering or hangs. Every project is checked by default; projects and services can opt out.
The posting rules are built to stay quiet: one message when a service goes down, one when it
comes back, nothing in between, and nothing for failures that aren't real outages.

Success looks like this:

- A real outage of three minutes or more produces exactly one "down" message and one
  "recovered" message.
- Deploys, sleeping services, stopped services and blips produce nothing.
- A kuso-server restart or leader handover in the middle of an outage sends nothing new.
- When a node dies, the outage arrives as one message, not one per service.

## What exists today

- **Discord delivery is done.** `notify.Dispatcher` writes to an outbox. Discord is a
  first-class channel, channels route by event type and project, and each event type can
  carry its own `@here` or role mention. This feature only needs two new event types.
- **Nothing makes HTTP requests to apps.** `internal/health` alerts on pod crash-loops
  (`pod.crashed` → `pod.recovered`). It keeps its alert state in memory, so a restart sends
  every current crash again.
- **The alerts engine** (`internal/alerts`) has episodic rules (5xx rate, p95 latency, cert
  expiry, DNS mismatch) that fire once and resolve once. We are not reusing it, for the
  reasons under "Alternatives considered".
- **The sleep trap.** A request to a sleep-enabled service's public URL goes through the
  activator, which wakes the service and stamps `last-activity`. Pinging that URL every
  minute would keep every sleepy service awake forever.

## Alternatives considered

- **A new episodic kind in the alerts engine.**
  - Rules are explicit, opt-in objects scoped to one project, so "every project unless
    opted out" doesn't fit.
  - It fires on the first bad check and has nowhere to store "failed N times in a row".
  - Its resolutions go out as `alert.fired`. The incident hook already misreads those as a
    recurrence.
  - Bending it to fit costs more than a small dedicated loop.
- **Uptime Kuma from the marketplace.** No code, but every monitor is set up by hand.
  Nothing is discovered automatically and there is no opt-out model.
- **Pinging through Traefik or the public URL.** This would also cover routing, TLS and DNS,
  but it wakes sleeping services and runs into the in-cluster loopback problem tenant pods
  already hit. DNS and cert expiry already have alert rules.

## Design

### Targets

A KusoEnvironment is a target when all of these hold:

- it is the production environment (same rules as `scaledown.isProductionEnv`, so env-group
  clones, previews and custom envs are excluded);
- its runtime is not `worker` and it is not `internal`;
- its project does not have `spec.uptime.disabled`;
- its service does not have `spec.uptime.disabled`.

A target that isn't serving on purpose is **paused** for that tick. Paused means the
Deployment is missing, its `spec.replicas` is 0 (that covers stopped, asleep and no image
yet), or the env or service has `stopped: true`. A paused target's open outage closes
quietly: no message, streaks reset. `lastAlertAt` is kept.

### The check

- **Request:** `GET http://<env>.<namespace>.svc.cluster.local<path>`.
  - The URL is built with `url.URL`. The host always comes from the env CR's name and
    namespace. The path is parsed, and only its path and query are used, so a user-set path
    can never change the host.
  - Path precedence: `spec.uptime.path`, then `spec.healthcheck.path`, then `/`.
- **Why a separate `uptime.path`:** setting `healthcheck.path` also switches the pod's kube
  probes to HTTP, including a liveness probe that can restart pods. Someone who only wants
  a better uptime path shouldn't have to accept that.
- **Client:**
  - timeout 10s;
  - redirects not followed;
  - keep-alives off, so each check makes a fresh connection and kube-proxy picks a pod each
    time;
  - `User-Agent: kuso-uptime/1`;
  - at most 64 KiB of the body is read, then discarded.
  - The response body is never stored or shown. Only the status code and an error class are
    kept.
- **Result:**
  - **ok:** any response with status below 500. 3xx, 401 and 404 all mean the app answered.
  - **fail:** a timeout, a connection error, or status 500 and above.
- **Why in-cluster:** it skips Traefik and the activator, so it never wakes anything and
  never counts as traffic for sleep. Project network policies already allow ingress from
  kuso-server.

### The loop

- Runs in the `kuso-server-cluster-singletons` lease, alongside nodewatch and scaledown.
- Ticks every 60s with 16 probe workers, and gives up on a tick after 50s. Targets not probed
  by then don't change this tick. A tick that runs long delays the next one rather than
  overlapping it.
- **No state is kept in memory between ticks.** Each tick reads every `UptimeState` row,
  decides, and writes the result back. A new leader therefore picks up exactly where the old
  one stopped.
- Registers `serverstate.LoopUptime` and beats every tick, including ticks with zero targets.
  The name joins the lease's unregister list.
- `KUSO_UPTIME_DISABLED=true` skips both registering the heartbeat and starting the loop.
  Registering a loop that never beats would make the liveness probe fail.
- Each tick:
  1. **Self-check.** `GET http://kuso-server.<kuso-ns>.svc.cluster.local/healthz`. Any HTTP
     response passes; `/healthz` can return 503 while a loop is stale, and that still proves
     the network works. A transport error or timeout makes the tick **inconclusive**: no
     counters move, nothing is written, and a warning is logged.
  2. **Load state.** Read projects, services and envs from the kube cache, plus Deployments
     and pods, then read all `UptimeState` rows. If the cache isn't synced or the read fails,
     skip the tick.
  3. **Probe** every non-paused target.
  4. **Decide.** Run the state machine below for each target.
  5. **Persist** every target's row in one transaction. Delete rows for envs that are no
     longer targets (env deleted, opted out, now a worker or internal), but only if step 2
     succeeded.
  6. **Emit, only after the commit succeeds.** Group this tick's down and recovered actions
     and send them through `notify.Emit`.

### State machine (per target)

Persisted per target:

| Field | Meaning |
|---|---|
| `failStreak` | failed checks in a row |
| `okStreak` | good checks in a row |
| `downSince` | time of the first failed check of the open outage; nil means up |
| `okSince` | time of the first good check of the current good streak |
| `alerted` | whether a "down" message went out for this outage |
| `lastAlertAt` | when the last "down" message was sent; survives across outages |
| last-check fields | `lastCheckedAt`, `lastResult`, `lastStatusCode`, `lastLatencyMs`, `lastError` |

Constants:

| Constant | Value |
|---|---|
| `downAfter` | 3 |
| `upAfter` | 3 |
| `cooldown` | 30 min |
| `interval` | 60s |
| `timeout` | 10s |
| `stormProjects` | 3 |

On each result:

- **fail:**
  - Increment `failStreak` and set `okStreak = 0`. If `downSince` is nil, set it to now.
  - If `failStreak >= downAfter` and the outage hasn't been alerted:
    1. **Pods crash-looping or failing to pull their image** (same reasons `health` uses:
       CrashLoopBackOff, ImagePullBackOff, ErrImagePull, CreateContainerConfigError).
       Don't alert; `pod.crashed` already covered it. If the crash-loop stops and the app is
       still failing, the next failed check alerts.
    2. **`lastAlertAt` is less than `cooldown` ago.** Hold. Checks continue, and if it is
       still failing once the cooldown has passed, it alerts then.
    3. **Otherwise** emit **down**, set `alerted = true` and `lastAlertAt = now`.
- **ok:**
  - Increment `okStreak` and set `failStreak = 0`. If `okStreak` is now 1, set `okSince`.
  - If `downSince` is set and `okStreak >= upAfter`, close the outage:
    - if it was alerted, emit **recovered** with downtime `okSince − downSince`;
    - reset `downSince` and `alerted` either way.
- **paused:** close any open outage quietly (see Targets).
- **inconclusive tick:** no change at all.

Results of these rules:

- Something that fails now and then (fail, fail, ok, …) never reaches three in a row, so it
  never alerts. Partial degradation is the 5xx-rate alert's job.
- A flapping service sends at most one "down" and one "recovered" per 30 minutes. An outage
  that started silently during the cooldown also closes silently.
- After a restart the persisted `alerted` and `lastAlertAt` values stop anything from being
  sent twice. The streak counters are persisted too, so a restart doesn't reset an outage
  that is part-way to being confirmed.

### Grouping

All down and recovered actions from one tick are grouped before they're sent:

- **Projects muted with the existing project mute** get per-project events as usual. The
  dispatcher already keeps those to the bell feed only. They are left out of the
  cluster-wide check below.
- **Per project:** one event per project per action. If more than one of the project's
  services changed in this tick, the event lists them all.
- **Cluster-wide:** if the unmuted down actions span `stormProjects` (3) or more projects,
  send one event with no project instead of one per project. For example: "8 services down
  across 5 projects. Several projects failing in the same minute usually means a node or
  platform problem." Recovered actions follow the same rule.
  - Project-less events skip channel project filters. That's acceptable for something that
    really is cluster-wide.

### Events

- `notify.EventUptimeDown = "uptime.down"`:
  - catalogue group `runtime`, default mention `@here`, severity `error`;
  - title `🔴 Down · <project> / <service>`;
  - fields: reason (e.g. "timed out after 10s", "connection refused", "HTTP 502"), down
    since, and checks failed;
  - link to the service page in kuso.
- `notify.EventUptimeRecovered = "uptime.recovered"`:
  - catalogue group `runtime`, severity `info`, green tone in `eventTone`;
  - title `🟢 Back up · <project> / <service>`, body "Was down for 14m".
- Constructors go in `notify/events.go`, with a separate one for the cluster-wide variant.
- Both types must be added to the const block **and** `EventCatalogue`.
- Discord channels set to all events get these automatically. Channels with a picked event
  list need them ticked.
- The incident hook only acts on `pod.crashed`, `alert.fired` and `node.unreachable`, so it
  ignores these.

### Data

**Migration `0014_uptime_state.sql`** (applied at boot under the existing advisory lock):

```sql
CREATE TABLE IF NOT EXISTS "UptimeState" (
  "namespace"      TEXT NOT NULL,
  "env"            TEXT NOT NULL,
  "project"        TEXT NOT NULL,
  "service"        TEXT NOT NULL,
  "paused"         TEXT,              -- NULL, or why: stopped | asleep | no-image | scaled-to-zero | no-deployment
  "failStreak"     INT  NOT NULL DEFAULT 0,
  "okStreak"       INT  NOT NULL DEFAULT 0,
  "downSince"      TIMESTAMPTZ,
  "okSince"        TIMESTAMPTZ,
  "alerted"        BOOLEAN NOT NULL DEFAULT false,
  "lastAlertAt"    TIMESTAMPTZ,
  "lastCheckedAt"  TIMESTAMPTZ,
  "lastResult"     TEXT,              -- ok | fail
  "lastStatusCode" INT,
  "lastLatencyMs"  INT,
  "lastError"      TEXT,
  "updatedAt"      TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY ("namespace", "env")
);
CREATE INDEX IF NOT EXISTS "UptimeState_project_idx" ON "UptimeState" ("project");
```

Every query is parameterised.

**CRD fields (additive only):**

- KusoProject and KusoService both get `spec.uptime: {disabled: boolean}`.
- KusoService also gets `spec.uptime.path: string`.
- The loop reads these straight from the project and service CRs in the cache. They are
  **not** copied onto KusoEnvironment, so `propagate.go` and the AddService env literal stay
  untouched.
- Changes go into the CRD YAMLs, the `crd_schema` testdata copies, and
  `kube.KusoProjectSpec` / `kube.KusoServiceSpec`.
- `kube.CheckSchemas` puts the server in degraded mode when a Go field is missing from the
  live CRD. The CRD therefore has to be applied before the new server runs: the updater does
  this on upgrade, and on the test cluster it's applied over ssh first.

### API, CLI, kuso.yml, web

- **API**
  - Project PATCH and service PATCH accept `uptime: {disabled?, path?}`, with pointer fields
    so a missing field leaves the value unchanged.
  - `GET /api/projects/{project}/uptime` returns one entry per production web service:
    `{service, env, state, since, lastCheckedAt, latencyMs, statusCode, error}`.
    - `state` is one of `up`, `failing`, `down`, `paused`, `disabled`, `pending`.
      `failing` covers "confirming", "crash-looping" and "cooldown", with the sub-reason in
      a `reason` field. `pending` means not checked yet.
    - Uses the same project read-permission check as the other project GET endpoints.
- **CLI**
  - `kuso uptime status <project>` shows a table, or JSON with `-o json`.
  - `kuso uptime disable <project> [service]` and `kuso uptime enable <project> [service]`
    toggle the opt-out.
  - `kuso uptime set-path <project> <service> <path>` sets `uptime.path`.
- **kuso.yml:** service `uptime: {disabled: true, path: /health}`, added to
  `spec/spec.go`, `export.go`, `apply.go` and `diff.go`. `diff.go` keeps a hand-written
  field list, so the new field has to be added there by hand. kuso.yml has no project-level
  toggles today, so the project opt-out stays API/CLI/web only.
- **Web**
  - An "Uptime checks" toggle in project settings (`projects/[project]/settings/view.tsx`).
  - An "Uptime checks" toggle plus an optional path field in the service settings panel
    (`components/service/overlay/settings/`).
  - The notification settings event picker reads the catalogue from the API, so it should
    pick up the new events automatically. The plan verifies this.

## Error handling

- **DB commit fails:** log it and send nothing. The next tick reads the old rows and decides
  again, so the failed tick's results are lost: an alert can arrive one minute late, but it
  can't be sent twice.
- **Commit succeeds but sending fails** (the outbox insert inside `Emit` errors): the outage
  is recorded as alerted but no message goes out. Sending only after the commit is
  deliberate. The other order would risk duplicates, and a lost message only happens if the
  DB fails between two writes a few milliseconds apart.
- **notify.Emit** has no return value and is backed by the outbox, so delivery retries are
  already handled.
- **A panic in a tick** is recovered by `goSafe`. The heartbeat then goes stale, which is the
  existing liveness signal.

## Testing

- **State-machine table tests** using a pure `decide(state, result, podsBad, now)` function:
  - a single blip;
  - three failures → alert;
  - flapping inside the cooldown, and an alert once the cooldown ends;
  - crash-loop suppression, and an alert after the crash-loop clears while still failing;
  - a restart with an outage already alerted;
  - pausing while alerted;
  - recovery needing three good checks.
- **Grouping tests:**
  - one project with several services;
  - three or more projects → one cluster-wide event;
  - muted projects left out of the cluster-wide count.
- **Probe tests** against `httptest` servers: 200, 302, 404, 500, a handler that hangs past
  the timeout, and a closed port.
- **Path handling:** a hostile `uptime.path` such as `//evil.com/x` or `http://evil/` keeps the
  target host.
- **DB tests** with `KUSO_TEST_PG_DSN`: upserting rows, pruning only after a successful list,
  and the persisted state surviving a reload.
- **Mutation-test each regression test:** break the implementation on purpose and confirm the
  test fails.
- **Live check on the test cluster** (only after asking, per the live-instance rule):
  - point a Discord channel at the disposable project from `agent-target.local.json`;
  - make it return 500 through `uptime.path`, and confirm exactly one "down" message after
    about three minutes;
  - fix it and confirm exactly one "recovered";
  - restart kuso-server mid-outage and confirm nothing new is sent.

## Rollout notes

- **First enable:** every service that is already broken alerts once. Cluster-wide grouping
  turns that into one message if three or more projects are affected.
- **Apps where `/` takes longer than 10s or returns 5xx on purpose** alert once. The fix is
  `kuso uptime set-path` or opting out.
- **Traffic cost:** each pinged service gets one request a minute with
  `User-Agent: kuso-uptime/1`, which apps can filter out of analytics.
- **RBAC:** no new rules. Everything read comes from caches kuso-server already has.

## Out of scope (v1)

- An external dead-man's switch for kuso itself. If the cluster or kuso-server dies, nothing
  alerts.
- Pinging previews, staging, env-group clones, internal services or workers.
- Per-service thresholds, timeouts or intervals.
- A status badge in the web UI. The API and CLI expose status.
- Reminders while a service stays down.
- Triggering the incident agent from `uptime.down`.
- Prometheus metrics for the checker.
