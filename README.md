# kuso

**Self-hosted, agent-native PaaS on real Kubernetes.**
Push to deploy. Sleep when idle. Drive it from a dashboard, a CLI, an MCP server — or let your AI agent run the whole thing.

[![Latest release](https://img.shields.io/github/v/release/sislelabs/kuso)](https://github.com/sislelabs/kuso/releases)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue)](./LICENSE)

---

## What is kuso?

kuso is a **single-tenant** Kubernetes-native PaaS — one team per cluster, like [Coolify](https://coolify.io/), but Kubernetes-native and built for the age of AI agents. Connect a GitHub repo, and every push builds, migrates, and rolls a zero-downtime deploy with a real Let's Encrypt cert. Idle services scale to zero and wake on the next request. Postgres, Redis, ClickHouse, Kafka-compatible Redpanda and more are one command away, with credentials injected straight into your pods.

Underneath, it's honest Kubernetes: `KusoProject`, `KusoService`, `KusoEnvironment`, `KusoAddon`, `KusoBuild`, `KusoCron`, and `KusoRun` are real CRDs. GitOps works. `kubectl edit` works. When the abstraction leaks, you're on familiar ground — not trapped inside someone's proprietary control plane.

```bash
kuso project create shop --repo https://github.com/you/shop --domain shop.example.com
kuso project addon add shop db --kind postgres --version 16
kuso project service add shop web --runtime dockerfile --port 3000
kuso build trigger shop web        # first build only — every push after this auto-deploys
kuso logs shop web -f
```

## Why kuso?

- **Agent-native, not bolted on.** Every UI action has a typed CLI command (~50 top-level commands, `-o json` everywhere) and a first-party MCP server. There's a [drop-in Claude Code skill](./skills/kuso/) that teaches an agent the full operating surface — deploy, debug, migrate, backup — so "fix the failing deploy" is a one-line request.
- **Deploy = push.** GitHub webhooks drive builds; a merge to `main` *is* the production deploy. Release hooks run your migrations as a gated Job before the new image is promoted — a failed migration never takes down running pods.
- **Environments that match how teams work.** PR previews with cloned, seeded, isolated databases. Long-lived `staging`/`qa` envs that track their own branch and auto-deploy on push. Per-env config overrides that can't rewrite production.
- **Sleep, wake, stop.** Idle services scale to zero and wake on the next request via the activator. Payment-webhook paths can pin a service warm. Or hard-stop a service (or a whole project) — deliberately off, with a clean "stopped" page, until you say otherwise.
- **Managed addons with batteries.** Postgres (optionally HA via CloudNativePG — 3 replicas, ~30s failover), Redis/Valkey, MongoDB, RabbitMQ, ClickHouse, Redpanda (Kafka API), NATS, S3-compatible storage, Meilisearch, Mailpit. Connection secrets auto-inject; per-service subscriptions keep DB creds out of your public frontend; scheduled and on-demand backups included.
- **A marketplace for the usual suspects.** `kuso marketplace deploy n8n` — Gitea, Metabase, n8n, Plausible, Umami, Uptime Kuma, Vaultwarden, rendered into ordinary kuso services you manage like everything else.
- **Self-healing, not just self-hosting.** `kuso doctor` diagnoses first-run setup (DNS, TLS, webhook delivery). `kuso health` flags stuck helm releases and drift cluster-wide, with one-command remediation. Failed builds come back with a classified cause and a suggested fix (`kuso build why`), not a raw log dump.
- **Zero-downtime self-update.** `make ship` cuts a GitHub release; every running instance pulls itself forward on the next updater tick — image swap, operator roll, CRD apply, all in-cluster. No ssh-from-laptop, ever.
- **Built to scale up.** Postgres-backed, stateless, multi-replica control plane. Multi-node clusters with token-based bootstrap (NAT-friendly), label-driven placement, auto-cordon on node failure. Point the control plane at managed Postgres when you outgrow the bundled one.
- **Honest about what's missing.** Multi-region active/active, edge functions, a WAF, a Grafana clone — not on the roadmap. Cloudflare and managed Postgres already do those well. kuso does the control plane and the cluster, and does them properly.

## Install

One command on a fresh Ubuntu 22/24 or Debian 12/13 box. Provisions k3s + Traefik + cert-manager + Let's Encrypt + Postgres + the kuso operator/server/registry. About 5 minutes from `curl` to a logged-in dashboard.

**Before you run it**, point DNS at the box's public IP:

```
kuso.example.com         A   <your-server-ip>
*.kuso.example.com       A   <your-server-ip>
```

Then on the server:

```bash
curl -sfL https://raw.githubusercontent.com/sislelabs/kuso/main/hack/install.sh \
  | sudo bash -s -- --domain kuso.example.com --email you@example.com
```

The script prints the admin password at the end. Log in at `https://kuso.example.com`.

Certificates come from Let's Encrypt **prod** by default — a real cert from the first page load. A DNS pre-flight catches most misconfigs before any ACME call, so rate limits rarely bite; if you're still iterating on DNS, pass `--le-staging`. The installer is **idempotent**: re-running preserves the admin password, JWT secret, Postgres credentials, and GitHub App config.

### Connect GitHub (one click)

Open **Settings → GitHub → Create GitHub App**. kuso authors the entire app manifest — name, URLs, permissions, webhook events — GitHub creates it, and the credentials (private key included) flow back automatically. Nothing to copy-paste, no `.pem` to download. A manual paste-the-secrets path exists behind "set up manually" if you prefer.

Without GitHub connected, services still build from any public repo URL via `kuso build trigger` — the repo picker just stays empty.

### Install the CLI

On your workstation (replace the host with your instance):

```bash
curl -fsSL https://kuso.example.com/install-cli.sh | sh
kuso login --api https://kuso.example.com -u admin
kuso doctor          # verifies DNS, TLS, auth, and the GitHub webhook round-trip
```

### Set up your agent

From any project repo deployed on kuso:

```bash
curl -fsSL https://raw.githubusercontent.com/sislelabs/kuso/main/skills/kuso/install.sh | bash
```

That drops the [kuso Claude Code skill](./skills/kuso/) into `.claude/skills/`, teaching the agent the CLI surface, the deploy lifecycle, the `${{ ... }}` env-reference syntax, and the standard debugging playbook. Prefer MCP? `mcp/` ships a first-party [MCP server](./mcp/) with 16 intent-grouped tools and a `--read-only` mode.

## A tour in ten commands

```bash
kuso status shop                                  # what's running, where, on which build
kuso logs shop web -f                             # stream logs; `logs search --q "..."` for the archive
kuso build why shop web                           # classified failure cause + suggested fix
kuso env set shop web FEATURE_X=1                 # rolls a new pod; per-env with --env staging
kuso environment add shop web staging --branch develop --seed-from production
kuso db sql shop db "SELECT count(*) FROM users"  # query an addon without exposing it
kuso addon-backup download shop db                # on-demand pg_dump to your laptop
kuso run shop api -- ./bin/console               # one-shot Job with the service's env
kuso marketplace deploy uptime-kuma --project tools
kuso upgrade --check                              # is a newer kuso out?
```

Everything supports `--help` with examples and `-o json` for scripting.

## How a deploy flows

```
git push → GitHub webhook → KusoBuild CR → kaniko build → image pushed
   → release hook Job (your migrations — failure blocks promotion)
   → env image tag patched → operator reconciles
   → rolling update (maxSurge 1, maxUnavailable 0) → zero downtime
```

Previews follow the same flow per PR, with their own cloned database, and are torn down on merge.

## Migrating from somewhere else?

- **docker-compose:** `kuso import compose docker-compose.yml` converts services and datastores into kuso resources — dry-run by default, with a report of anything that has no equivalent.
- **Coolify v4:** `kuso migrate coolify` walks an existing instance.

## Operations

| Concern | Where to look |
| --- | --- |
| Backups (control plane + addon data, DR paths) | [docs/BACKUP_RESTORE.md](./docs/BACKUP_RESTORE.md) |
| External managed DBs (Neon / Supabase / RDS) with kuso-run backups | [docs/EXTERNAL_DB_BACKUPS.md](./docs/EXTERNAL_DB_BACKUPS.md) |
| What's safe to edit on a live deployment (per-field blast radius) | [docs/EDIT_SAFETY.md](./docs/EDIT_SAFETY.md) |
| Adding nodes, node pools, placement labels | [docs/NODE_BOOTSTRAP.md](./docs/NODE_BOOTSTRAP.md) · [docs/BUILD_NODE_POOL.md](./docs/BUILD_NODE_POOL.md) |
| HA addons (CloudNativePG, Redis Sentinel) | [docs/ADDON_HA.md](./docs/ADDON_HA.md) |
| Sharing one Postgres server across projects | [docs/SHARED_ADDONS.md](./docs/SHARED_ADDONS.md) |
| Prometheus metrics + what to alert on | [docs/METRICS.md](./docs/METRICS.md) |
| Every HTTP endpoint, request/response shapes | [docs/WORKFLOWS.md](./docs/WORKFLOWS.md) |
| GitHub App manual setup | [docs/GITHUB_APP_SETUP.md](./docs/GITHUB_APP_SETUP.md) |

## Repo layout

| Path         | What it is                                                                |
| ------------ | ------------------------------------------------------------------------- |
| `server-go/` | Go backend + REST API. Postgres-backed. Serves the embedded SPA from `internal/web`. Also runs the **activator** (scale-to-zero / stopped-service proxy) in `--activator` mode. |
| `web/`       | Next.js 16 frontend. Built into `server-go/internal/web/dist`.            |
| `operator/`  | Kubernetes operator that reconciles the `Kuso*` CRs.                      |
| `cli/`       | `kuso` command-line tool (Go, Cobra).                                     |
| `mcp/`       | `kuso-mcp` Model Context Protocol server (Go).                            |
| `skills/`    | The published Claude Code skill.                                          |
| `deploy/`    | Production manifests applied at install time.                             |
| `docs/`      | Architecture + operations docs.                                           |

## License

[AGPL-3.0](./LICENSE). Network use triggers the source-disclosure obligation — if you run kuso as a hosted service, your modifications must be available to your users.

© SisleLabs and contributors.
