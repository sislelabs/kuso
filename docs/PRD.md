# Kubero++ — PRD for the Agentic, Indie-Dev Dream PaaS

**Author:** Ivo Sabev (SisleLabs)
**Status:** Draft v0.1
**Last updated:** 2026-04-30

---

## 1. Vision

Kubero is the only open-source self-hosted PaaS with sleeping containers and real Kubernetes-native autoscaling. That alone makes it a category leader for indie devs running a portfolio of products at uneven scale. What it lacks today is the headless, agentic surface that would let an operator (human or AI) run the whole platform from a terminal — every UI action available as a typed CLI command, every CLI command callable from an MCP server, every piece of state declarable in YAML and reconciled by the operator.

Kubero++ is the work to close that gap. The end state: a single developer with Claude Code can spin up a new product — git repo, pipeline, staging + production environments, domain, TLS, secrets, database addon, cron jobs, sleep policy, autoscaling — in one conversation, with no clicks. Every running app is observable, every cost is visible, every secret is encrypted at rest, and the entire platform fits in someone's head.

## 2. Problem Statement

Today, an indie dev running 5–10 products on Kubero will hit these walls:

1. **The CLI is a wizard, not a tool.** `kubero app create` walks you through prompts. You can't pipe it, script it, or call it from an agent. Half the UI's surface area isn't reachable from the CLI at all (env vars, domain attach, scaling, sleep toggle, logs, exec).
2. **No first-party MCP server.** Generic `kubernetes-mcp-server` works at the pod level — wrong abstraction. Agents end up reasoning about deployments and configmaps when they should be reasoning about apps.
3. **Secrets are plaintext in etcd.** `KuberoApp.spec.envVars` only supports `name + value`. There is no `valueFrom: secretKeyRef`. Anyone with read access to the CR can see your Stripe keys.
4. **Apps aren't fully declarable as code.** You can `kubectl apply` a `KuberoApp` CR, but pipelines, secrets, and addons assume UI-driven creation. There's no canonical "everything for one product in one git repo" pattern.
5. **Cost is invisible.** Sleeping containers are the killer feature, but you have no dashboard answering "how much did sleep save me last month?"
6. **Observability is bring-your-own.** Logs are per-pod via UI; no aggregation, no structured export by default.
7. **Multi-server ergonomics aren't first-class.** Kubernetes handles scheduling, but the Kubero UI/CLI doesn't surface "which node is this app on, is it the right one, why is this node hot."

## 3. Goals & Non-Goals

### Goals

- **G1.** Every meaningful Kubero operation reachable headlessly via CLI and MCP, with `-o json` / structured output.
- **G2.** A first-party `kubero-mcp` server with intent-grouped tools designed for AI agents.
- **G3.** Production-grade secrets: no app-owner secret should ever land in etcd as plaintext.
- **G4.** "One repo per product" pattern: a single `kubero.yaml` (or set of CRs) in a git repo can fully define a product.
- **G5.** Cost visibility: every app shows current monthly cost, sleep savings, and projected cost at next traffic tier.
- **G6.** Built-in log + metric aggregation that works without extra setup, exportable to external sinks.
- **G7.** Stack-aware bootstrap: `kubero new product` for Go + Next.js + Postgres should work in one command and produce sensible defaults.

### Non-Goals

- Replacing kubectl. Power users still drop down to k8s primitives when they need to.
- Multi-tenancy beyond what already exists. This PRD is for solo / small-team operators.
- Cloud provider abstractions beyond what Kubero already offers (Hetzner, DO, Linode, GKE, kind). No AWS/Azure-specific work.
- Replacing dedicated APM (Datadog, Sentry). The observability work targets "good enough out of the box," not "enterprise APM."
- Web UI overhaul. UI improvements are welcome but not required for this PRD.

## 4. Personas

**Primary: The portfolio-indie-dev (me).** Runs 4–12 products, uneven scale (0 to ~1000 users each), strong DevOps/Kubernetes background, Go + Next.js + PostgreSQL stack, Hetzner-hosted. Lives in the terminal. Pairs with Claude Code for most operational work.

**Secondary: The agent.** Claude Code, Cursor, or any MCP-speaking client. Needs intent-grouped tools, structured outputs, idempotent operations, and clear error messages it can self-correct against.

**Tertiary: The technical co-founder.** Comfortable with git and a deploy dashboard, not necessarily kubectl. Should be able to onboard to a Kubero++ instance in <30 minutes and ship a Next.js app the same day.

## 5. User Journeys

### J1. New product, zero clicks

Ivo opens Claude Code and says: _"Create a new product called marshal.bg. Stack is Go API + Next.js frontend + Postgres. Wire up the analiz-monorepo style pipeline. Domain marshal.bg, staging at staging.marshal.bg. Sleep both. Stripe key in secrets."_

Claude Code, via `kubero-mcp`, scaffolds a `kubero.yaml` in the repo, creates the pipeline, both apps, the Postgres addon, configures domains with TLS, registers the Stripe secret in the secret store, and reports back URLs + first-deploy progress. Total interaction: one prompt, ~90 seconds.

### J2. Cost cleanup

Ivo: _"Show me what I'm paying for and which apps haven't seen traffic in 30 days."_ Claude returns a cost-ranked list with sleep stats. Ivo: _"Archive the bottom three."_ Claude moves them to an `archived` phase that scales to zero with no warm-up policy and removes their domains.

### J3. Incident

A staging deploy fails. Claude: _"The staging deploy of analiz failed at the build step. Last 50 lines of build log show a missing `DATABASE_URL` env var. The production app has it set; staging doesn't. Want me to copy it from production?"_ One yes and the fix lands.

### J4. New server

Ivo provisions a new Hetzner CX42. _"Add 167.235.x.x to the cluster as a worker, label it for the heavy-CPU build pool."_ Claude runs the join, applies the labels, verifies the node is `Ready`, and reports back.

## 6. Functional Requirements

### Workstream A — CLI completion

The existing `kubero-cli` (Go, MIT) gains the following commands. All commands support `-o json|yaml|table` and `--context` for multi-cluster.

**Apply / config:**

- `kubero apply -f <file>` — declarative create-or-update for `KuberoApp`, `KuberoPipeline`, addons, secrets. Diff before apply with `--dry-run`.
- `kubero get app|pipeline|addon|secret <name>` — fetch current state.
- `kubero diff -f <file>` — show what would change.

**App lifecycle:**

- `kubero app deploy <name> [--tag <image-tag>] [--branch <branch>]`
- `kubero app restart <name>`
- `kubero app scale <name> --replicas N` and `kubero app autoscale <name> --min N --max M --cpu 80`
- `kubero app sleep enable|disable <name>`
- `kubero app logs <name> [-f] [--lines N] [--container build|run]`
- `kubero app exec <name> -- <cmd>`
- `kubero app status <name>` (replicas, sleep state, last deploy, health, addon connections)
- `kubero app cost <name>` (current monthly burn + sleep savings)

**Env / domain / secrets:**

- `kubero app env set|unset <name> KEY[=VALUE]`
- `kubero app env import <name> --file .env` (maps to a managed Secret, not plaintext envVars)
- `kubero app domain add|remove <name> <host>` (handles cert-manager TLS automatically)
- `kubero secret create|update|delete <name> --from-literal=...`

**Pipeline / addon:**

- `kubero pipeline phases <name>` and `kubero pipeline promote <pipeline> <from> <to>`
- `kubero addon install|uninstall <name> --kind postgresql --version 16`

**Cluster / nodes:**

- `kubero node list|drain|cordon <name>`
- `kubero node join --hetzner-id <id> --role worker --labels k=v`

**Acceptance:** Every command above produces parseable JSON with `--output json`. Every command is idempotent (safe to retry). Failures return non-zero with structured error JSON when requested.

**Build vs contribute:** All of this is a contribution upstream to `kubero-dev/kubero-cli`. MIT license, the maintainer accepts PRs, and we get free maintenance.

### Workstream B — `kubero-mcp` server

A new Go project, `kubero-mcp`, MIT licensed, lives at `sislelabs/kubero-mcp` (offered upstream as a sibling repo). Implements the MCP protocol over stdio + HTTP. Tools are intent-grouped per Anthropic's MCP design guidance — not a 1:1 wrap of REST endpoints.

**Tool surface (v1):**

- `list_apps(filter?)` — apps with status, cost, last-deploy.
- `describe_app(name)` — everything: state, env (keys only, not values), domains, addons, recent deploys, recent logs.
- `deploy_app(name, options?)` — trigger deploy, optionally with image tag or branch override; streams deploy log to caller.
- `troubleshoot_app(name)` — composite tool: fetches status + last 200 log lines + recent events + addon health, returns a single structured analysis blob. The single highest-leverage tool for agents.
- `set_app_config(name, patch)` — partial update: env vars, scaling, sleep, domain, replicas. Idempotent.
- `manage_secret(app, action, key, value?)` — create/update/delete secret entries via Secret store, never via CR.
- `tail_logs(name, lines?, follow?)` — runtime logs.
- `exec_app(name, command)` — gated, requires explicit caller permission.
- `cluster_health()` — node states, resource pressure, anomalies.
- `cost_report(period?)` — cost per app + savings from sleep.
- `bootstrap_product(name, stack, options)` — full product scaffold (the J1 journey).

**Design rules:**

1. Every tool returns structured data + a human-readable summary string.
2. Destructive operations (`exec_app`, `delete_app`, `manage_secret delete`) require confirmation flag in the call.
3. Tool descriptions explicitly tell the agent when to prefer composite tools (e.g., "use `troubleshoot_app` rather than chaining `describe_app` + `tail_logs`").
4. Auth via Kubero API token from env (`KUBERO_TOKEN`, `KUBERO_URL`). Read-only mode supported via `--read-only` flag.

**Acceptance:** Claude Code, Cursor, and Claude Desktop can all add `kubero-mcp` via standard MCP config. The J1, J2, J3, J4 journeys are completable end-to-end with this MCP alone.

**Build vs contribute:** Standalone repo. No fork required. Calls Kubero's existing REST API; lifecycle is independent of the main project.

### Workstream C — Secrets done right

The current `KuberoApp.spec.envVars` only supports `value`. This is unacceptable for production secrets. Two changes:

**C1. CRD extension (upstream PR to `kubero-operator`):** add `valueFrom` and `envFrom` support to the env block, mapping directly to the Kubernetes pod env semantics:

```yaml
envVars:
  - name: LOG_LEVEL
    value: info
  - name: STRIPE_SECRET_KEY
    valueFrom:
      secretKeyRef:
        name: analiz-secrets
        key: stripe_secret_key
envFrom:
  - secretRef: { name: analiz-shared-secrets }
```

**C2. Managed secret store:** the CLI / MCP creates and manages a Kubernetes Secret named `<app>-managed-secrets` per app by default. `kubero app env set` writes to this Secret and inserts a `valueFrom` ref into the CR. Plaintext `value:` entries become opt-in for non-sensitive config only.

**C3. Optional: external-secrets-operator integration.** For users who want SOPS / Vault / 1Password / AWS SM, document the ESO integration and add a single `--secrets-backend=eso` flag that routes secret writes through ESO instead of native Secrets.

**Acceptance:** No app-level secret ever appears in plaintext in any `KuberoApp` CR. `kubero get app` redacts secret values by default. Existing apps can be migrated with `kubero app secrets migrate <name>`.

### Workstream D — Observability included

**D1. Logs:** Default-on log aggregation via Loki + Promtail (or Vector → Loki) deployed as part of `kubero install`. `kubero app logs` reads from Loki, not directly from pods, so logs persist past pod restarts. Configurable retention, default 7 days. Export to external sinks (Axiom, Better Stack, Grafana Cloud) via single config flag.

**D2. Metrics:** Default-on Prometheus + node-exporter + kube-state-metrics. Per-app dashboards auto-generated. App-level metrics surfaced via `kubero app metrics <name>` (RPS, p50/p95/p99 latency, error rate, CPU, memory). Already partially in Kubero today; this work makes it work out-of-the-box and exposes it through the CLI/MCP.

**D3. Traces (P2):** OpenTelemetry collector deployed by default, configured to no-op until an export endpoint is set. Apps can ship traces by setting standard OTel env vars; Kubero auto-injects them when traces are enabled per-app.

**Acceptance:** A fresh `kubero install` includes log + metric aggregation with zero extra config. `kubero app logs` and `kubero app metrics` work for any deployed app immediately. Resource overhead documented and bounded (sub-1GB RAM total for a small cluster).

### Workstream E — Sleep mode polish

The single most important Kubero feature for the indie-dev persona deserves better tooling.

- **E1. Per-app sleep policies:** sleep after N minutes idle, with configurable warmup hints (e.g., warm at 8am Sofia time on weekdays). Stored in CR.
- **E2. Cold-start telemetry:** every cold start logs latency. `kubero app status` shows recent cold-start p50/p95.
- **E3. Cost dashboard:** "you saved $X this month from sleep" surfaced at the cluster and per-app level. Backed by simple math (replicas × pod-size × sleep-time × estimated $/core-hour).
- **E4. Pre-warm on deploy:** new deploys briefly disable sleep so the first user request after a deploy isn't a cold start.
- **E5. Sleep groups:** declare that app A and app B should wake together (e.g., a Next.js frontend and its Go API). Avoids cascading cold starts.

### Workstream F — Multi-server / multi-cluster ergonomics

For the 4–10 servers case:

- **F1. Node pool labels.** First-class concept: `build`, `web`, `worker`, `database` pools. CLI for assignment. Apps express pool affinity in CR.
- **F2. `kubero node join --hetzner-id`.** One command provisions and joins. Uses Hetzner Cloud API directly when configured. (DO and Linode equivalents follow same pattern.)
- **F3. Node hot-spot view.** `kubero cluster top` shows nodes sorted by pressure. MCP exposes this for the J3 incident journey.
- **F4. Multi-cluster contexts (already in CLI).** Polish: cross-cluster app listing, easy promotion of a CR from one cluster to another (e.g., dev cluster → prod cluster).

### Workstream G — GitOps / declarative everything

Make "one repo per product" a paved road.

- **G1. `kubero.yaml` schema.** A single file at the root of a product repo that declares pipeline + apps + addons + cronjobs + secret references (not values). Example:

  ```yaml
  apiVersion: kubero.dev/v1
  kind: Product
  metadata: { name: analiz }
  spec:
    pipeline:
      phases: [review, staging, production]
      gitrepo:
        {
          url: https://github.com/sislelabs/analiz,
          branch_strategy: production-on-main,
        }
    apps:
      - name: web
        stack: nextjs
        ingress: { hosts: [analiz.dev] }
        sleep: { production: false, staging: true }
        autoscale: { min: 1, max: 5, cpu: 70 }
      - name: api
        stack: go
        port: 8080
        envFrom: [analiz-shared-secrets]
    addons:
      - kind: postgresql
        version: "16"
        size: small
  ```

- **G2. `kubero apply` reconciles a `kubero.yaml`.** Translates to underlying CRs. Idempotent. Diff-friendly.
- **G3. CI integration template.** GitHub Actions workflow that runs `kubero apply --dry-run` on PR and `kubero apply` on merge.
- **G4. Drift detection.** `kubero drift <product>` reports differences between the cluster state and the product's `kubero.yaml`.

### Workstream H — Indie-dev quality of life

- **H1. `kubero new product`** — interactive _or_ flag-driven scaffolder. Generates a `kubero.yaml`, a `.github/workflows/deploy.yml`, sensible Dockerfiles for the stack, and a `secrets.example` file. Stacks shipped at v1: `nextjs`, `go-api`, `go-htmx`, `nextjs+go-api+postgres`.
- **H2. Templates marketplace polish.** The 160+ existing templates get a CLI: `kubero template list|install <name>`.
- **H3. One-command backup/restore.** `kubero backup create <product>` snapshots all CRs + addon data to S3. `kubero backup restore` brings them back, optionally to a different cluster.
- **H4. Single-binary install.** Already mostly true (`kubero install`), but the bootstrapped cluster should include all observability + secrets + GitOps tooling, not require post-install steps.

## 7. Architecture

```
                                    ┌──────────────┐
                                    │  Claude Code │
                                    │  / Cursor    │
                                    └──────┬───────┘
                                           │ MCP (stdio/HTTP)
                                           ▼
                                    ┌──────────────┐
                                    │  kubero-mcp  │  ← new Go project, MIT
                                    └──────┬───────┘
                                           │ REST + token
                                           ▼
   ┌──────────┐    CLI       ┌──────────────────────────┐
   │   user   ├─────────────►│      kubero-server       │ ← existing
   └──────────┘              │    (UI + REST API)       │
                             └──────────────┬───────────┘
                                            │ k8s API
                                            ▼
                             ┌──────────────────────────┐
                             │    kubero-operator       │ ← existing, extended
                             │  reconciles KuberoApp,   │   (envFrom support,
                             │  KuberoPipeline CRs      │    sleep groups)
                             └──────────────┬───────────┘
                                            │
                            ┌───────────────┼───────────────┐
                            ▼               ▼               ▼
                      ┌──────────┐   ┌────────────┐   ┌───────────┐
                      │ App pods │   │  Loki +    │   │ Prometheus│
                      │          │   │  Promtail  │   │ + Grafana │
                      └──────────┘   └────────────┘   └───────────┘
```

The architecture preserves Kubero's two-container core (UI + Operator) and adds three new artifacts: an extended CLI, a standalone MCP server, and an opinionated default observability stack bundled with `kubero install`.

## 8. Success Metrics

- **M1.** Time from "I have an idea" to "live URL with TLS, secrets, autoscale, sleep" — target ≤5 minutes via Claude Code + `bootstrap_product`.
- **M2.** Number of UI-only operations: 0 for the documented happy paths.
- **M3.** Cold-start p95 on sleeping apps: <2s for typical Go binaries, <5s for typical Next.js.
- **M4.** Monthly cost savings from sleep: visible in dashboard, target ≥30% on a portfolio of >5 mostly-idle apps.
- **M5.** Mean time to incident triage with `troubleshoot_app`: <60s from "something's broken" to "here's the cause and proposed fix."
- **M6.** Zero plaintext secrets in any CR after migration.
- **M7.** `kubero apply` round-trip on a typical `kubero.yaml`: <10s.

## 9. Phasing

### P0 — "Claude Code can deploy" (4–6 weekends)

- A: CLI commands for apply, deploy, env, domain, logs, status, scale, sleep, restart.
- B: `kubero-mcp` v1 with `list_apps`, `describe_app`, `deploy_app`, `troubleshoot_app`, `set_app_config`, `tail_logs`, `manage_secret`.
- C1+C2: `envFrom` support in CRD + managed secret store.
- G1+G2: `kubero.yaml` schema + `kubero apply`.

This is the minimum to make Kubero a Claude-Code-native PaaS.

### P1 — "Production-grade indie PaaS" (2–3 months after P0)

- D1+D2: bundled Loki + Prometheus.
- E1–E4: sleep policy improvements + cost dashboard.
- F1+F2: node pools + Hetzner-aware join.
- H1: `kubero new product` scaffolder.
- G3+G4: CI integration + drift detection.

### P2 — "Polished" (later)

- D3: traces.
- E5: sleep groups.
- C3: external-secrets integration.
- H3: backup/restore.
- F4: cross-cluster ergonomics.

## 10. Out of Scope

- Web UI rewrite. Existing UI is fine; CLI/MCP-first is the thesis.
- Windows server support (k8s nodes).
- Built-in CDN. Use Cloudflare in front of ingress.
- Built-in DNS management. Use external-dns + your DNS provider.
- Replacing dedicated APM, APM-grade tracing, or full SIEM.
- Marketplace billing / paid template ecosystem.

## 11. Risks & Mitigations

| Risk                                                             | Likelihood     | Mitigation                                                                                                                                             |
| ---------------------------------------------------------------- | -------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Upstream maintainer slow to merge CRD changes                    | Medium         | Land CLI/MCP first against current API; CRD extensions are additive (new fields), so a fork-then-merge path works without breaking existing users.     |
| MCP protocol churn                                               | Low–Medium     | Pin to a stable MCP SDK version; design tool surface to be transport-agnostic.                                                                         |
| Bundled observability blows up resource budget on small clusters | Medium         | Make Loki/Prometheus opt-in via `kubero install --with-observability`; document RAM floor (~1 GB extra).                                               |
| Sleep cold-start UX is bad enough that users disable it          | Medium         | E4 (pre-warm on deploy) + good cold-start telemetry surfaced clearly so users see actual numbers, not anxiety.                                         |
| Bus factor — one Kubero maintainer                               | Medium         | The CRDs are standard k8s; if Kubero stops, the underlying KuberoApp resources remain valid k8s objects and migration to plain helm/k8s is mechanical. |
| Secrets migration breaks existing apps                           | High if rushed | Migration command (`kubero app secrets migrate`) runs in shadow mode first, with `--apply` flag for actual cutover. Rollback documented.               |

## 12. Open Questions

1. Does the existing Kubero REST API expose enough surface for every CLI command above, or are there server-side gaps requiring API additions?
2. Is `mms-gianni` (lead Kubero maintainer) interested in having P0 contributed upstream, or would a `sislelabs/kubero-cli` fork be a better starting point with eventual upstreaming?
3. Should `kubero.yaml` (Workstream G) live as a new CRD (`Product`) reconciled by the operator, or as a CLI-side concept that fans out into existing CRs? CRD is cleaner; CLI-side is faster to ship.
4. How do we handle the GPL-3 vs MIT/Apache split if the `Product` CRD lives in the GPL-3 main repo while CLI/MCP are MIT?
5. Cost calculation needs node $-per-hour data. Do we ship per-provider price tables (Hetzner, DO, Linode) or require user-supplied?

---

## Appendix A — `kubero-mcp` example tool descriptors

Format suitable for direct registration in the MCP server.

```go
{
  Name: "troubleshoot_app",
  Description: `Composite diagnostic for a Kubero app. Fetches current status,
last 200 lines of runtime logs, recent build logs if a deploy is in progress,
addon health, and recent k8s events. Returns structured analysis. Prefer this
over chaining describe_app + tail_logs when investigating "why is X broken".`,
  Input: { name: "string (app name)" },
}

{
  Name: "set_app_config",
  Description: `Partial update of an app's configuration. Supports env vars,
domains, scaling, sleep policy, replicas. Idempotent. Use this rather than
re-creating the app.`,
  Input: {
    name: "string",
    patch: {
      env_set: "map[string]string (sets non-secret env vars)",
      env_secret_set: "map[string]string (writes to managed Secret, refs from CR)",
      domains_add: "[]string",
      domains_remove: "[]string",
      replicas: "int?",
      sleep: "enabled|disabled?",
      autoscale: "{min, max, cpu}?",
    }
  }
}
```

## Appendix B — Reference `kubero.yaml`

Full example for an analiz-shaped product. Pasted above in §6 G1; reproduced here as a contract.

```yaml
apiVersion: kubero.dev/v1
kind: Product
metadata:
  name: analiz
spec:
  pipeline:
    gitrepo:
      url: https://github.com/sislelabs/analiz
    phases:
      - name: review
        sleep: { enabled: true, after: 10m }
      - name: staging
        sleep: { enabled: true, after: 30m }
      - name: production
        sleep: { enabled: false }

  apps:
    - name: web
      stack: nextjs
      port: 3000
      ingress:
        production: { hosts: [analiz.dev], tls: true }
        staging: { hosts: [staging.analiz.dev], tls: true }
      autoscale: { min: 1, max: 5, cpu: 70 }
      envFrom: [analiz-shared]

    - name: api
      stack: go
      port: 8080
      ingress:
        production: { hosts: [api.analiz.dev], tls: true }
      autoscale: { min: 1, max: 10, cpu: 75 }
      envFrom: [analiz-shared]

  addons:
    - kind: postgresql
      version: "16"
      size: small
      backup: { schedule: "0 3 * * *", retention_days: 14 }

  secrets:
    - name: analiz-shared
      keys: [DATABASE_URL, STRIPE_SECRET_KEY, REDIS_URL]
      # values managed via `kubero secret set` or external-secrets

  cronjobs:
    - name: rollups
      schedule: "0 2 * * *"
      app: api
      command: ["/app/jobs/rollup"]
```
