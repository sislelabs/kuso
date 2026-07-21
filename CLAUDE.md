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
| Pods for a service               | `kuso service pods <project> <service>` (or `kuso get pods …`)           |
| Recent service errors            | `kuso service errors <project> <service>`                                |
| Query an addon DB                | `kuso db sql <project> <addon> "SELECT …"` · `kuso db tables …`          |
| Expose addon on a public port    | `kuso project addon public-tcp enable <project> <addon>`                 |
| Pin an addon to nodes            | `kuso project addon placement set <project> <addon> --label k=v`         |
| RBAC roles                       | `kuso get roles` · `kuso role create/edit/delete`                        |
| Backup policy / health           | `kuso backup settings get` · `kuso backup health`                        |
| Pod-size presets                 | `kuso instance-config podsize list`                                      |
| Hit any API endpoint directly    | `kuso api <METHOD> <path> [-f k=v] [--data @f.json] [--jq expr]`         |

**Why this matters:**
- The CLI hits the same `/api/...` surface the UI uses, so what you see is what users see — no "but on my machine" mismatches.
- It exercises the auth / tenancy / perm layers you'd otherwise miss with raw kubectl.
- Bugs in the CLI become visible (we found four during the last e2e pass that way).
- The output format is stable and scriptable; raw kubectl JSON is verbose and re-shapes between server versions.

The CLI now has web-UI parity for operator tasks: roles (`kuso role`/`get roles`),
pod-size & runpack config, backup settings/health, addon placement, the DB
browser (`kuso db sql/tables/columns/rows`), service pods/errors, and the
github/user/invite/notification helpers all have commands. `kuso <group> --help`
lists them. If you reach for the web UI or kubectl for a kuso-managed resource,
that's now a gap worth reporting, not a fallback.

`kuso api` is the raw escape hatch — any `/api` endpoint is reachable
even before it has a dedicated command (gh-api style: `kuso api GET
projects`, `kuso api POST .../builds -f branch=main`, `--jq` to filter).

**Fall back to `kubectl` only when**:
- The CLI genuinely has no equivalent: `kubectl logs` of a non-kuso pod, helm-operator state, raw CRD yaml for operator reconcile debugging, or **kube events** (no kuso endpoint exists — verified).
- You're debugging the CLI itself.
- You're inspecting cluster-level state (nodes beyond `kuso node list`, ClusterRoles, namespaces, storageclasses) that has no kuso-CLI equivalent.

When you do shell out to `kubectl`, run it via `ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl ..."` — the test cluster's kubeconfig isn't on the dev machine.

## Other rules

- **Per-machine test target lives in `agent-target.local.json` (gitignored).** Read this on session start when the user asks you to smoke-test, redeploy, or otherwise interact with a live kuso instance. It carries deploy host + SSH key path + CLI binary path + a disposable project/service to poke at. Don't prompt the user for these values when the file exists. Schema lives in `agent-target.example.json` (committed).
- Confirm `dist/kuso-darwin-arm64` is up to date before driving it. After server-go changes that affect the API surface, also rebuild the CLI: `cd cli && go build -o /tmp/kuso ./cmd`.
- Don't mix CLI invocations with raw kubectl in the same diagnostic — pick one and stay there. Mixing buries the actual signal in tooling noise.
- For e2e validation, the CLI is the contract. If it lies (wrong status, missing fields, decode error), that's a real bug to fix — not something to work around with a kubectl one-liner.
- **Before editing a running CR's spec, consult `docs/EDIT_SAFETY.md`.** It's the per-field contract for which fields are live-editable, which trigger rolling restarts, which hit Let's Encrypt rate limits, and which orphan data on removal. Use it to reason about blast radius before suggesting destructive edits to the user.

## Architecture cheatsheet (read this before reasoning about the codebase)

**Repo layout:**
- `server-go/` — Go HTTP API (`internal/http/handlers/*`), kube client (`internal/kube/`), 50+ domain packages under `internal/` (`projects`, `addons`, `builds`+`buildcontroller`, `secrets`, `notify`, `nodewatch`, `nodemetrics`, `nodejoin`, `spec`, `status`, `marketplace`, `crons`+`runs`, `previewdb`, `pkgupdates`, `scaledown`, `alerts`, `incidents`, `remediate`, `audit`, `updater`, `imagerelease`, `leader`, …). `ls server-go/internal` for the full set — don't assume this list is exhaustive.
- `web/` — Next.js 16 App Router with `output: "export"`. Static bundle gets embedded into the server-go binary (`server-go/internal/web/dist/`).
- `operator/` — operator-sdk helm-operator. CRDs in `operator/config/crd/bases/`, helm charts in `operator/helm-charts/{kuso,kusoproject,kusoservice,kusoenvironment,kusoaddon,kusobuild,kusocron,kusorun}/`.
- `cli/` — single binary (`./cmd`) entry point + `cmd/kusoCli/` cobra commands + `pkg/kusoApi/` resty client + `pkg/coolify/` migration importer.
- `deploy/` — kube manifests applied during install (`server-go.yaml`, `prometheus.yaml`, `cluster-issuer.yaml`).
- `api/` typed API surface · `mcp/` MCP server · `compose/` docker-compose importer · `scripts/` dev-ops helpers · `skills/` the published Claude Code skill (`skills/kuso/SKILL.md`, installed into consumer repos via `install.sh`).
- `hack/release.sh` — `make ship VERSION=vX.Y.Z` does version bump + web build + cross-platform docker push to ghcr + cuts a GH release + writes `release.json`. Live instances poll the GH releases endpoint and self-update via the in-built updater (no ssh from the laptop). `make local-roll VERSION=vX.Y.Z` is the dev-only escape hatch that ssh-rolls a single test cluster — almost no one should use it. The `make release-roll` target is deprecated and exits non-zero with a helpful message; replace any reference to it with `make ship`.

**CRD model:**
- `KusoProject` — top-level grouping. `spec.{defaultRepo, baseDomain, github, previews, placement}`.
- `KusoService` — one application within a project. `spec.{repo, runtime, port, domains, envVars, scale, sleep, placement, volumes, previews, runtime-specific blocks (static/buildpacks)}`. `runtime ∈ {dockerfile, nixpacks, buildpacks, static}`.
- `KusoEnvironment` — one deployed instance of a service (production / preview-pr-N). Carries the resolved placement + envFromSecrets + image tag.
- `KusoAddon` — managed datastore. `spec.{kind, version, size, ha, storageSize, password, database, backup, placement}`. Helm chart renders to a StatefulSet + a `<name>-conn` Secret consumed by every env in the project via `envFromSecrets`.
- `KusoBuild` — kaniko/buildpacks build pod that produces an image and patches the matching env CR. Reconciled by the server-go `buildcontroller`, NOT the helm-operator.
- `KusoCron` — scheduled job in a project (cron expr + container spec) → renders a k8s CronJob. `KusoRun` — a one-off/triggered job execution (imperative counterpart to KusoCron).

**Patterns to keep:**
- API endpoints under `/api/...`. The web client uses `lib/api-client.ts`'s `api()` wrapper which auto-injects the JWT bearer + handles 401 (clears jwt + bounces to `/login` via the global QueryClient onError).
- Server-side errors: wrap with `fmt.Errorf("%w: …", ErrConflict)` so HTTP handlers can map via `errors.Is` to the right status code. `addons.fail` is the canonical handler (passes `err.Error()` through on conflict so the UI sees "addon X/Y already exists" instead of bare "409").
- Server-side env-var rewriting: `${{ <addon>.KEY }}` resolves to `valueFrom.secretKeyRef` against `<addon>-conn`; `${{ <svc>.URL/HOST/PORT }}` resolves to in-cluster DNS. The web `EnvVarsEditor.tsx` reverses the secretKeyRef → ref form on read so the round-trip is lossless.
- Notification dispatcher (`notify.Dispatcher`) emits events that fan out to webhooks AND mirror into a `NotificationEvent` Postgres table for the bell-icon feed.
- Node sampler (`nodemetrics.Sampler`) writes one row per node every 5 min into `NodeMetric` (7-day retention, prune on tick). Read by the per-node history endpoint + sparkline modal.
- Node failure detection (`nodewatch.Watcher`) auto-cordons nodes that have been NotReady > 5 min, fires `node.unreachable` notify event, auto-uncordons + fires `node.recovered` on recovery (only uncordons if WE cordoned, marker = `kuso.sislelabs.com/cordoned-by-nodewatch` annotation).

**Shared primitives:**
- `web/src/components/ui/popover.tsx` — base-ui Popover, used everywhere. The `dropdown-menu.tsx` primitive exists but DON'T USE IT — base-ui Menu has a portal/hydration edge case in our static export that throws "This page couldn't load" on first mount. UserMenu and the bell-icon feed both use Popover instead. ServersPopover is the canonical example.
- `internal/projects.PlacementMatchesNode` (in `kube/types.go`) — canonical AND-of-labels matcher; used by service + addon placement validation.
- The `kuso.sislelabs.com/<key>` label prefix is the user-visible label namespace. Helm charts emit `nodeSelector: kuso.sislelabs.com/<key>: <value>`, the labels editor reconciles it. Bare keys without prefix are kuso-internal (e.g. `kuso.sislelabs.com/project`, `…/service`, `…/addon-kind`).

**Release flow:**
- Bump `server-go/internal/version/VERSION`, `deploy/server-go.yaml` image tag, `hack/install.sh` `KUSO_SERVER_VERSION` AND `KUSO_VERSION` defaults. Then `make ship VERSION=vX.Y.Z` (release.sh handles all four version-string rewrites + the web bundle build + docker push + GH release). Live instances pick up the new release.json on the next updater tick and roll themselves; you do NOT ssh from the laptop. CRD changes still need an explicit `kubectl apply -f operator/config/crd/bases/...yaml` via ssh — the auto-updater only flips image tags, not schemas.
- Operator helm-operator picks up CR spec changes via watch + 3m reconcile (watches Project/Service/Environment/Addon/Cron/Run — NOT Build). Schema changes to a CRD require both the YAML apply AND the operator pod to restart-or-reconnect to refresh its informer.

## Scope guardrails (resist scope creep)

kuso is **single-tenant** — one team per cluster, like Coolify. NOT a multi-tenant SaaS; that's a different product with a much stricter security model and is explicitly out of scope. Within one team kuso supports multi-node clusters, multi-replica services, Postgres-backed control plane, and HA-capable addons. Things deliberately out of scope:

- Custom DB branching (Vercel + Neon won; integrate, don't compete)
- Bespoke Grafana clone (5-min sparkline tiles + alerts is enough; ship metrics to real Grafana if you need more)
- Edge functions / serverless runtime (Cloudflare turf)
- WAF (Cloudflare-in-front does it for $0)
- Multi-region active/active (different product; integrate Cloudflare + managed multi-region Postgres if you need it)
- Native error-tracker (Sentry self-hostable is one click in marketplace)
- Vercel Next.js optimizer parity (suicide to chase)

## How to add a new CRD-backed feature

Pattern, in order:
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
