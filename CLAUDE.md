# kuso — agent rules

Project-specific rules that override agent defaults. Loaded on every turn.

## Cluster inspection — always go through `kuso`, not raw kubectl

When you need to check the state of the live cluster (services, addons, builds, env vars, logs, ingress), **drive it through the `kuso` CLI** at `dist/kuso-darwin-arm64` (or whatever matches the host). Examples:

| What you want to know            | Command                                                                  |
| -------------------------------- | ------------------------------------------------------------------------ |
| What projects exist              | `kuso get projects -o json`                                              |
| Project rollup                   | `kuso status <project>`                                                  |
| Service spec                     | `kuso get services <project> -o json`                                    |
| Build state                      | `kuso build list <project> <service>`                                    |
| Logs                             | `kuso logs <project> <service> [--env <env>] [-f]`                       |
| Env vars on a service            | `kuso env list <project> <service>`                                      |
| Connect to addon DB              | `kuso get addons <project> -o json` then read DATABASE_URL               |
| Trigger a build                  | `kuso build trigger <project> <service>`                                 |
| Open a shell in a pod            | `kuso shell <project> <service>`                                         |

**Why this matters:**
- The CLI hits the same `/api/...` surface the UI uses, so what you see is what users see — no "but on my machine" mismatches.
- It exercises the auth / tenancy / perm layers you'd otherwise miss with raw kubectl.
- Bugs in the CLI become visible (we found four during the last e2e pass that way).
- The output format is stable and scriptable; raw kubectl JSON is verbose and re-shapes between server versions.

**Fall back to `kubectl` only when**:
- The CLI doesn't expose what you need (e.g. `kubectl logs` of a non-kuso pod, helm-operator state, raw CRD yaml for debugging operator reconcile bugs, kube events).
- You're debugging the CLI itself.
- You're inspecting cluster-level state (nodes, ClusterRoles, namespaces) that has no kuso-CLI equivalent.

When you do shell out to `kubectl`, run it via `ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl ..."` — the test cluster's kubeconfig isn't on the dev machine.

## Other rules

- Confirm `dist/kuso-darwin-arm64` is up to date before driving it. After server-go changes that affect the API surface, also rebuild the CLI: `cd cli && go build -o /tmp/kuso ./cmd`.
- Don't mix CLI invocations with raw kubectl in the same diagnostic — pick one and stay there. Mixing buries the actual signal in tooling noise.
- For e2e validation, the CLI is the contract. If it lies (wrong status, missing fields, decode error), that's a real bug to fix — not something to work around with a kubectl one-liner.

## Architecture cheatsheet (read this before reasoning about the codebase)

**Repo layout:**
- `server-go/` — Go HTTP API (`internal/http/handlers/*`), kube client (`internal/kube/`), domain services (`internal/projects`, `internal/addons`, `internal/builds`, `internal/secrets`, `internal/notify`, `internal/nodewatch`, `internal/nodemetrics`, `internal/nodejoin`, `internal/spec`, `internal/status`).
- `web/` — Next.js 16 App Router with `output: "export"`. Static bundle gets embedded into the server-go binary (`server-go/internal/web/dist/`).
- `operator/` — operator-sdk helm-operator. CRDs in `operator/config/crd/bases/`, helm charts in `operator/helm-charts/{kuso,kusoproject,kusoservice,kusoenvironment,kusoaddon,kusobuild}/`.
- `cli/` — single binary (`./cmd`) entry point + `cmd/kusoCli/` cobra commands + `pkg/kusoApi/` resty client + `pkg/coolify/` migration importer.
- `deploy/` — kube manifests applied during install (`server-go.yaml`, `prometheus.yaml`, `cluster-issuer.yaml`).
- `hack/release.sh` — `make ship VERSION=vX.Y.Z` does version bump + web build + cross-platform docker push to ghcr + cuts a GH release + writes `release.json`. Live instances poll the GH releases endpoint and self-update via the in-built updater (no ssh from the laptop). `make local-roll VERSION=vX.Y.Z` is the dev-only escape hatch that ssh-rolls a single test cluster — almost no one should use it. The `make release-roll` target is deprecated and exits non-zero with a helpful message; replace any reference to it with `make ship`.

**CRD model:**
- `KusoProject` — top-level grouping. `spec.{defaultRepo, baseDomain, github, previews, placement}`.
- `KusoService` — one application within a project. `spec.{repo, runtime, port, domains, envVars, scale, sleep, placement, volumes, previews, runtime-specific blocks (static/buildpacks)}`. `runtime ∈ {dockerfile, nixpacks, buildpacks, static}`.
- `KusoEnvironment` — one deployed instance of a service (production / preview-pr-N). Carries the resolved placement + envFromSecrets + image tag.
- `KusoAddon` — managed datastore. `spec.{kind, version, size, ha, storageSize, password, database, backup, placement}`. Helm chart renders to a StatefulSet + a `<name>-conn` Secret consumed by every env in the project via `envFromSecrets`.
- `KusoBuild` — kaniko/buildpacks build pod that produces an image and patches the matching env CR.

**Patterns to keep:**
- API endpoints under `/api/...`. The web client uses `lib/api-client.ts`'s `api()` wrapper which auto-injects the JWT bearer + handles 401 (clears jwt + bounces to `/login` via the global QueryClient onError).
- Server-side errors: wrap with `fmt.Errorf("%w: …", ErrConflict)` so HTTP handlers can map via `errors.Is` to the right status code. `addons.fail` is the canonical handler (passes `err.Error()` through on conflict so the UI sees "addon X/Y already exists" instead of bare "409").
- Server-side env-var rewriting: `${{ <addon>.KEY }}` resolves to `valueFrom.secretKeyRef` against `<addon>-conn`; `${{ <svc>.URL/HOST/PORT }}` resolves to in-cluster DNS. The web `EnvVarsEditor.tsx` reverses the secretKeyRef → ref form on read so the round-trip is lossless.
- Notification dispatcher (`notify.Dispatcher`) emits events that fan out to webhooks AND mirror into a `NotificationEvent` SQLite table for the bell-icon feed.
- Node sampler (`nodemetrics.Sampler`) writes one row per node every 5 min into `NodeMetric` (7-day retention, prune on tick). Read by the per-node history endpoint + sparkline modal.
- Node failure detection (`nodewatch.Watcher`) auto-cordons nodes that have been NotReady > 5 min, fires `node.unreachable` notify event, auto-uncordons + fires `node.recovered` on recovery (only uncordons if WE cordoned, marker = `kuso.sislelabs.com/cordoned-by-nodewatch` annotation).

**Shared primitives:**
- `web/src/components/ui/popover.tsx` — base-ui Popover, used everywhere. The `dropdown-menu.tsx` primitive exists but DON'T USE IT — base-ui Menu has a portal/hydration edge case in our static export that throws "This page couldn't load" on first mount. UserMenu and the bell-icon feed both use Popover instead. ServersPopover is the canonical example.
- `internal/projects.PlacementMatchesNode` (in `kube/types.go`) — canonical AND-of-labels matcher; used by service + addon placement validation.
- The `kuso.sislelabs.com/<key>` label prefix is the user-visible label namespace. Helm charts emit `nodeSelector: kuso.sislelabs.com/<key>: <value>`, the labels editor reconciles it. Bare keys without prefix are kuso-internal (e.g. `kuso.sislelabs.com/project`, `…/service`, `…/addon-kind`).

**Release flow:**
- Bump `server-go/internal/version/VERSION`, `deploy/server-go.yaml` image tag, `hack/install.sh` `KUSO_SERVER_VERSION` AND `KUSO_VERSION` defaults. Then `make ship VERSION=vX.Y.Z` (release.sh handles all four version-string rewrites + the web bundle build + docker push + GH release). Live instances pick up the new release.json on the next updater tick and roll themselves; you do NOT ssh from the laptop. CRD changes still need an explicit `kubectl apply -f operator/config/crd/bases/...yaml` via ssh — the auto-updater only flips image tags, not schemas.
- Operator helm-operator picks up CR spec changes via watch + 3m reconcile. Schema changes to a CRD require both the YAML apply AND the operator pod to restart-or-reconnect to refresh its informer.

## Active product roadmap (post-v0.6.29)

This is the prioritized list from the PaaS competitive audit. Ship in order; each is sized realistically.

1. **Cron CRD + UI** — `KusoCron` CRD in operator, helm chart that emits a kube `CronJob`. New CLI `kuso cron {create,list,delete}`. UI: a Cron tab on the service overlay with schedule picker + recent-run history. Half-week scope.
2. **Worker service type** — `spec.runtime: "worker"` (or a separate KusoWorker CRD if cleaner; decide first). No ingress, no health probes (or a configurable command-based liveness), command override required, scale knob with default 1. Half-week.
3. **Scheduled Postgres backups → S3 + restore-into-new-instance** — addon helm chart already has a `backup-cronjob.yaml` template; needs S3 destination wiring + retention + a Restore button on the addon Settings tab that creates a NEW addon and pipes pg_dump output. Per-addon S3 creds OR global config. 1–2 weeks.
4. **One-click rollback** — surface `helm history` per env (server-side: kube CR has the build SHAs in spec.image.tag history; query Build CRs for the project/service ordered by createdAt). UI: Deployments tab grows a "rollback to <sha>" button with confirmation. Few days.
5. **Per-PR preview envs with seeded DB** — GitHub App PR webhook → kuso server creates a `KusoEnvironment` with `kind=preview, pullRequest={number, headRef}` → operator helm renders an isolated deploy → seed addon DB by `pg_dump` from prod into a fresh per-PR addon (or share with a per-PR schema if cheap). Comment URL on the PR. TTL on close. Tear down on PR close. 1–2 weeks.
6. **Searchable logs + alert rules** — pick: Loki + Promtail (heaviest, the default), OR Vector → ClickHouse (lighter, faster), OR Vector → SQLite with FTS (lightest, kuso-shaped). Rule engine evaluates "error rate > X over Y", "OOMKilled in last Z min", "deploy failed" — fans out via the existing notify dispatcher. UI: log search + alert rule editor. 2 weeks.
7. **Email integration (Resend/Postmark) marketplace tile** — pure UX: a tile on the project Addons screen that, on click, opens a dialog asking for the API key, stores it as a kuso secret named `RESEND_API_KEY` (or `POSTMARK_*`), and updates docs. No Helm chart. Few days.

Shipped through v0.6.29:
- v0.6.23 — multi-node SSH join + node placement labels + topologySpread + nodewatch auto-cordon
- v0.6.24 — addon green border, project-card LIVE counter shape fix, profile ErrorBoundary
- v0.6.25 — global 401 redirect + addon Placement UI parity
- v0.6.26 — UserMenu rewrite (Popover, drop base-ui DropdownMenu)
- v0.6.27 — group create form polish
- v0.6.28 — `kuso migrate coolify` CLI + 5-min sampler + live chart marker
- v0.6.29 — addon edit (version/size/HA/storage), in-app notifications feed (bell icon popover), em-dash placeholder cleanup, ApiError prefers response body

Things explicitly NOT to build (from the audit; resist scope creep):
- Custom DB branching (Vercel + Neon won; integrate, don't compete)
- Bespoke Grafana clone (5-min sparkline tiles + alerts is enough)
- Edge functions / serverless runtime (Cloudflare turf)
- WAF (Cloudflare-in-front does it for $0)
- Multi-region failover (single-box indies need backups, not failover)
- Native error-tracker (Sentry self-hostable is one click in marketplace)
- Vercel Next.js optimizer parity (suicide to chase)

## How to add a new CRD-backed feature (cron / worker / etc)

Pattern, in order — copy this for items 1 and 2:
1. Define the CRD YAML at `operator/config/crd/bases/application.kuso.sislelabs.com_<plural>.yaml`. Use `x-kubernetes-preserve-unknown-fields: true` at spec level if iteration speed matters.
2. Define the Go shape in `server-go/internal/kube/types.go` + the GVR + getters/setters in `server-go/internal/kube/crds.go`.
3. Define the helm chart at `operator/helm-charts/<name>/` (Chart.yaml, values.yaml, templates/, _helpers.tpl mirroring `kusoaddon`).
4. Add the watch entry to `operator/watches.yaml`.
5. Build a domain service at `server-go/internal/<name>/` with `Service` struct + `Add/List/Update/Delete` methods.
6. Build an HTTP handler at `server-go/internal/http/handlers/<name>.go`. Mount routes; wire `errors.Is` mapping in a `fail()` helper; pass the wrapped err string through on Conflict so the UI shows useful messages.
7. Wire the handler into `internal/http/router.go`.
8. Add the Go types + resty methods to `cli/pkg/kusoApi/<name>.go`. Add a cobra subcommand at `cli/cmd/kusoCli/<name>.go`.
9. Add the API client + hooks to `web/src/features/<name>/{api,hooks}.ts`. Re-export from `web/src/features/<name>/index.ts`.
10. Build the UI section/tab/dialog under `web/src/components/<name>/` or as a tab on the existing service overlay.
11. Apply the new CRD to the live cluster: `scp` it then `ssh … "kubectl apply -f /tmp/<name>.yaml"`. The release auto-updater (`make ship`, then instances pull) only flips image tags — it does NOT apply CRD schema changes.

When in doubt, mirror how addons or environments do it. They're the most complete examples of this pattern in the codebase.
