# Design: Scale-to-Zero with a Request-Holding Activator

> **IMPLEMENTED (v0.18.73+).** Live e2e validated. One design change vs the
> original proposal: the traefik **errors-middleware (mechanism 3) did NOT
> work** — traefik returned 503 to the client (~5s) instead of reliably
> forwarding/holding the request at the activator. The validated mechanism
> is **the activator as the direct Ingress backend** for sleep-enabled
> services (a clean variant of mechanism 2, with no per-state route
> flipping): the kusoenvironment Ingress points its backend at
> `kuso-activator` whenever `sleep.enabled`. Hitting the activator
> directly woke a 0-replica app and served 200 in ~3.3s; awake requests
> pass through in ~15ms. Two activator code fixes were needed: gate
> "safe to proxy" on the **Service Endpoints** being ready (not
> Deployment.ReadyReplicas), and a retry transport with a short dial
> timeout that retries the cold-start dial races ("connection refused"
> AND "i/o timeout" while kube-proxy programs the ClusterIP).

**Status:** Draft / proposal
**Author:** (design doc)
**Context:** Hosting many mostly-idle apps (e.g. an AI-app-builder backend) requires
idle apps to cost ~0 pods, with the first visitor transparently waking the app
instead of getting a 503.

---

## 1. Problem

kuso today has the *shape* of scale-to-zero but not the substance:

- `KusoService.spec.sleep.{enabled,afterMinutes,wakeOn.excludePaths}` exists in the
  CRD (`operator/config/crd/bases/...kusoservices.yaml`) and documents
  "the operator scales the deployment to 0 after N idle minutes; the first request
  wakes it back up."
- `scale.min: 0` is accepted (`effectiveScaleMin` in
  `server-go/internal/projects/services_ops.go`).
- `WakeService` (`server-go/internal/projects/wake.go`) can *manually* bump a
  service's production env back to ≥1 replica by stamping
  `status.wakeReplicas` for the operator to reconcile.

**But two load-bearing pieces do not exist:**

1. **No auto-idle controller.** Nothing observes traffic and scales an idle
   Deployment to 0. `afterMinutes` is declared, never enforced.
2. **No activator / request-holding proxy.** Traffic routes
   Ingress → `Service` (`kusoenvironment/templates/{ingress,service}.yaml`) →
   Deployment pods. At 0 replicas the Service has no Endpoints, so the **first
   request 503s**. There is no component that intercepts that request, scales
   the app up, and holds the connection until a pod is Ready.

Result: "scale to zero" today means "manually scaled to zero, and the next
visitor gets an error." Unusable for end-user apps.

### Goal

- Idle app → **0 pods**.
- First request after idle → **transparently wakes** the app and is served
  (held, not dropped), with a bounded cold-start budget.
- Per-app idle cost ≈ an Ingress rule + DB rows in a shared Postgres + objects in
  shared storage. Active cost ≈ 1 pod, briefly.
- Stay aligned with kuso's "lean, single-binary-ish, few moving parts" ethos.

### Non-goals

- Multi-tenancy / untrusted-code isolation (tracked separately; see
  `CLAUDE.md` scope guardrails — this is the harder, orthogonal problem).
- Sub-100ms cold starts. Container cold start (image already on node) is
  seconds; we make that tolerable, not invisible.
- Scaling the shared Postgres / static tier (separate work items).

---

## 2. The two halves

Scale-to-zero is always two cooperating loops:

```
  ┌────────────────────┐         idle > afterMinutes          ┌────────────┐
  │  SCALE-DOWN loop    │ ───────────────────────────────────▶│ replicas=0 │
  │ (idle detector)     │                                      └────────────┘
  └────────────────────┘                                              │
                                                                      │ request arrives
  ┌────────────────────┐    intercept → scale 1 → hold → proxy        ▼
  │  SCALE-UP path      │ ◀──────────────────────────────────── ┌────────────┐
  │ (activator)         │                                        │  visitor   │
  └────────────────────┘                                        └────────────┘
```

- **Scale-down** is easy and we own it regardless of approach (a controller that
  reads per-service request counters and patches replicas to 0).
- **Scale-up (the activator)** is the hard part and the buy-vs-build decision.

---

## 3. Options for the activator

### Option A — Knative Serving

Knative is the reference implementation of request-driven scale-to-zero. Its
`activator` buffers requests for a scaled-to-zero Revision, triggers scale-up,
and proxies once Ready; the `autoscaler` (KPA) handles idle scale-down from
request concurrency metrics.

- **Pros:** Battle-tested; exactly this problem; gives request-buffering,
  concurrency-based autoscale, and zero-downtime revisions for free.
- **Cons:** **Heavy.** Pulls in Knative Serving + a compatible networking layer
  (Kourier/Istio/Contour). It wants to *own* routing and the Service/Deployment
  shape — kuso renders its own Deployment + Service + traefik Ingress via the
  kusoenvironment chart, so adopting Knative means either replacing that with
  Knative `Service` objects (large chart rewrite) or running Knative alongside
  (two routing planes). Big operational + RAM footprint on the small single-node
  boxes kuso targets. Against the "lean" ethos.

### Option B — KEDA + KEDA HTTP Add-on

KEDA's `http-add-on` deploys an **interceptor** (the activator) and a
**scaler**: the interceptor sits in the request path, holds requests for
scaled-to-zero workloads, and tells KEDA to scale the target from 0→N; KEDA's
`ScaledObject` scales it back to 0 on idle.

- **Pros:** Purpose-built for HTTP scale-to-zero; far lighter than Knative; keeps
  our existing Deployment (KEDA scales an existing Deployment via a
  `ScaledObject`, it doesn't replace it). The HTTP add-on's interceptor is the
  request-holding proxy we'd otherwise have to build. Mature, CNCF.
- **Cons:** Two new cluster components (KEDA operator + HTTP add-on
  interceptor/scaler). Routing must funnel through the interceptor: traefik
  Ingress → KEDA interceptor Service → app Service. That's one extra hop and a
  per-service `HTTPScaledObject`. Still lighter than Knative but not zero deps.

### Option C — Native kuso activator

Build a small activator into kuso: a single shared proxy Deployment that traefik
routes *misses* to (i.e. it's the fallback backend for scaled-to-zero services),
which scales the target up via the existing `WakeService` plumbing and proxies
once Ready. The idle detector lives in kuso-server (it already runs samplers and
watchers).

- **Pros:** Leanest — one small Go proxy + one controller loop, no third-party
  operators. Reuses the existing `wake.go` / `status.wakeReplicas` mechanism and
  the operator's reconcile. Full control over cold-start UX (custom "waking…"
  holding page for browsers, retry/queue semantics). Fits the single-binary
  ethos; nothing new for the operator to learn.
- **Cons:** **We own it.** It's real proxy + controller engineering: connection
  holding, concurrency limits, the traefik "route to activator when 0 replicas,
  route to app when up" switch, race handling (N simultaneous first-requests),
  health/readiness gating, and the idle detector's traffic accounting. Re-implements
  a slice of what Knative/KEDA already solved.

### Recommendation

**Option C (native activator), scoped tightly** — *if* we accept owning a small
proxy — because:

1. kuso's whole value prop is "few moving parts, runs on one small box." Knative
   violates that outright; KEDA-HTTP softens it but still adds two operators and a
   routing hop to every app.
2. We already have half the machinery: `WakeService`, the `status.wakeReplicas`
   hint, the operator reconcile loop, and kuso-server's existing watcher/sampler
   pattern for the idle detector.
3. The activator only needs to be good, not Knative-grade: hold the request,
   trigger wake, poll readiness, proxy. For the AI-builder audience a 1–3s
   "waking your app…" hold on the *first* hit after idle is acceptable.

**Fallback:** if owning a proxy proves too costly, **Option B (KEDA HTTP add-on)**
is the pragmatic buy — adopt it as an optional, off-by-default cluster component
that the builder profile turns on, so core kuso stays lean for everyone else.

The rest of this doc designs **Option C**, with notes on where B would slot in.

---

## 4. Design (Option C)

### 4.1 Components

| Component | Where | Role |
|---|---|---|
| **Idle detector** | new loop in `server-go` (mirrors `nodewatch`/`nodemetrics` watcher pattern) | Reads per-service request counts; when a sleep-enabled service has had 0 requests for `afterMinutes`, scales its prod env to 0. |
| **Activator proxy** | new tiny Deployment `kuso-activator` (1–2 replicas, shared across all apps) | Traefik's fallback backend for scaled-to-zero services. On request: trigger wake, hold/poll until the app has a Ready endpoint, reverse-proxy the request, then get out of the path. |
| **Routing switch** | kusoenvironment chart (`ingress.yaml`) + operator | When a service is asleep (0 replicas), its Ingress/route points at `kuso-activator`; when awake, at its own Service. |
| **Request counter** | activator + app-level metrics | Source of truth for "last request at". Cheapest source: traefik access metrics or the activator/ingress seeing traffic. See 4.4. |

### 4.2 Wake-up flow

```
1. App idle → idle detector patches env: replicas 0  (reuses scale path)
2. Operator reconciles → Deployment replicas=0, 0 pods. Route now → kuso-activator.
3. Visitor request → traefik → kuso-activator.
4. Activator: look up target service from Host header.
5. Activator: trigger wake (bump replicas → 1) via the existing wake mechanism
   (status.wakeReplicas / a server endpoint), idempotent under concurrent hits.
6. Activator: poll the target Service's Endpoints until ≥1 Ready (bounded, e.g. 30s).
7. Activator: reverse-proxy the held request to the now-Ready pod; stream response.
8. Subsequent requests: once the app has Ready endpoints, the route flips back to
   the app's own Service (no activator hop) until it idles again.
```

Browser GETs that wait can optionally get a lightweight "starting your app…"
interstitial that auto-retries; API clients just experience a slower first call.

### 4.3 The routing switch (the crux)

We need traffic to reach the **activator when replicas=0** and the **app when
replicas>0**, without a 503 in between. Three candidate mechanisms, in order of
preference for traefik (kuso's ingress):

1. **Endpoints-failover via a single shared route + readiness:** keep the app's
   Ingress pointing at the app Service, but make the app Service's selector
   resolve to the **activator** when the app has no Ready pods. Not natively
   possible with a plain Service, so:
2. **Two Ingress states reconciled by the operator:** the operator (or
   kuso-server) rewrites the route target between `kuso-activator` and the app
   Service based on `replicas == 0`. Simple, but there's a reconcile-lag window;
   acceptable if the activator is *also* always a safe fallback (it can wake +
   proxy any service, so even a stale "route to app" that 503s can be caught by a
   traefik errors-middleware → activator). **Recommended.**
3. **traefik custom middleware / errors page → activator:** configure a traefik
   `errors` middleware so any 502/503 from an app route is caught and forwarded to
   `kuso-activator`, which wakes + proxies. This makes the activator a universal
   safety net with **no per-app route rewriting at all** — the cheapest to operate
   and the most robust against races. **Strongly consider as the primary
   mechanism;** (2) becomes unnecessary if this works cleanly with traefik.

> Decision to validate in a spike: can a traefik `errors` middleware (or
> equivalent) reliably re-issue the original request to the activator with method,
> path, headers, and body intact? If yes, mechanism (3) is by far the leanest:
> apps always route to themselves; the activator only ever sees first-hit-after-idle
> traffic via the error path. If traefik can't replay the body, fall back to (2)
> (operator flips the route to the activator while asleep).

### 4.4 Idle detection — cheapest accurate signal

We must know "time since last request" per service without adding per-app
sidecars. Candidates:

- **(preferred) Activator + traefik metrics:** when mechanism (3) is used, the
  activator inherently sees every first-hit. For ongoing "is it still being used"
  we read traefik's per-router request count (Prometheus — kuso already runs
  prometheus, `deploy/prometheus.yaml`) and compute idle from the delta. Zero
  per-app cost.
- **(fallback) App access logs / a request-count annotation** bumped by an
  ingress plugin. More moving parts; avoid.

The idle detector loop (in kuso-server, leader-elected like the existing
singletons) every ~1 min: for each sleep-enabled service, if `now - lastRequestAt
> afterMinutes` and `replicas > 0` and no `wakeOn.excludePaths` (those force
min 1), scale to 0.

### 4.5 wakeOn.excludePaths

Already in the CRD: if a service has must-stay-warm paths (payment webhooks
etc.), `effectiveScaleMin` already forces min=1. The idle detector must honor the
same guard (never scale such a service to 0). No change to the contract — just
enforce it in the detector.

---

## 5. Per-app cost with this in place

Per generated app (builder profile):

| Tier | Idle pods | Active pods | Dedicated DB pods |
|---|---|---|---|
| Static frontend (shared static tier) | 0 | 0 | 0 |
| Backend/API (scale-to-zero) | **0** | 1 (transient) | 0 |
| Postgres (shared instance addon) | 0 | 0 | 0 (shared server) |

Shared, fixed cost (amortized across *all* apps): `kuso-activator` (1–2 pods) +
the shared Postgres + the shared static tier + kuso control plane. So 1,000 idle
apps cost ~the shared components, not 1,000–3,000 pods.

---

## 6. Changes required (Option C)

**CRD / contract** — mostly already present:
- Honor `sleep.afterMinutes` (currently declared, unenforced).
- No new fields needed for v1; `sleep.enabled` + `scale.min:0` + `wakeOn` suffice.

**operator / charts:**
- `kusoenvironment/templates/ingress.yaml`: attach the traefik errors-middleware
  (mechanism 3) OR support the activator-target route state (mechanism 2).
- New `deploy/kuso-activator.yaml`: the shared activator Deployment + Service +
  the traefik middleware definition.

**server-go:**
- New `internal/activator/` (or extend `internal/projects`): the idle-detector
  loop (leader-elected), reading traefik/prometheus request counts.
- The activator proxy itself: either a tiny separate binary/image, or a mode of
  kuso-server (`kuso-server --activator`) reusing the same image to avoid a new
  build artifact. **Prefer a mode of the existing image** (lean: no new image).
- Reuse `WakeService`; add a fast internal "wake now and tell me when Ready"
  call the activator uses.

**web:**
- Optional: a "waking…" interstitial; a per-service "Sleep" toggle already maps to
  `spec.sleep`. Surface scale-to-zero in the service UI.

**release:** activator image (if separate) added to the release/visibility
checks; CRD unchanged so no schema apply needed.

---

## 7. Risks & edge cases

- **Cold-start budget:** image must be on the node (it usually is, post first
  deploy) or the wake includes an image pull → slow. Mitigate: keep images warm
  on nodes; bound the activator hold (e.g. 30s) then show a retry page.
- **Thundering herd:** N simultaneous first-hits must trigger exactly one
  scale-up; the wake must be idempotent (it is — replica bump is level-triggered)
  and the activator must coalesce waiters per service.
- **Webhooks / payments:** must NOT scale to zero — enforced via
  `wakeOn.excludePaths` (already in contract). Loud documentation; default the
  builder profile to *not* exclude unless declared.
- **Stateful/worker runtimes:** workers have no HTTP surface (no ingress) so the
  activator can't wake them on request — scale-to-zero applies to HTTP services
  only. Workers stay out of scope for v1.
- **Reconcile lag (mechanism 2):** the error-middleware fallback (3) covers the
  window; design so a stale route can't strand a request.
- **traefik body-replay (mechanism 3):** unproven for large POST bodies — spike
  first; fall back to (2) if needed.

---

## 8. Milestones

1. **Spike:** prove the traefik errors-middleware → activator request-replay
   (mechanism 3) end-to-end on a throwaway service. Decides 3 vs 2. *(small)*
2. **Activator proxy:** `kuso-server --activator` mode: wake + poll-Ready +
   reverse-proxy, with per-service waiter coalescing and a bounded hold. *(large)*
3. **Idle detector:** leader-elected loop scaling sleep-enabled services to 0 on
   `afterMinutes` using prometheus/traefik request counts; honor
   `wakeOn.excludePaths`. *(medium)*
4. **Chart + deploy wiring:** `deploy/kuso-activator.yaml` + ingress middleware;
   make scale-to-zero the default for builder-profile services. *(medium)*
5. **UX + docs:** service-overlay Sleep toggle, "waking…" page, webhook warnings.
   *(small)*

---

## 9. Open decisions

- **Buy vs build, final call:** native activator (this doc) vs KEDA HTTP add-on
  as an optional component. Recommend native for lean default; revisit if (2)
  the spike shows traefik replay is unreliable AND a native proxy balloons.
- **Activator as kuso-server mode vs separate image** — recommend mode (no new
  artifact).
- **Idle signal source** — confirm traefik per-router metrics are exposed to the
  existing prometheus and are cheap to query at the cadence we need.
