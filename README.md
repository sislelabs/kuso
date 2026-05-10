# kuso

Self-hosted, agent-native PaaS on real Kubernetes. Multi-node out of the box, Postgres-backed control plane, HA addons, and an HTTP/CLI/MCP surface designed to be driven by humans **and** AI agents.

## What is kuso?

kuso is a Kubernetes-native PaaS where every operation in the UI is reachable from a typed CLI command, and every CLI command is callable from a first-party MCP server. Apps sleep when idle, autoscale when busy, and the platform scales horizontally — add nodes, add `kuso-server` replicas, point at managed Postgres, ship.

The platform is built for teams running serious workloads: real CRDs you can `kubectl edit`, structured outputs you can pipe, idempotent operations agents can drive, and an architecture that doesn't fall over when you stop being one person on one box.

## Why kuso?

- **Real Kubernetes underneath.** `KusoProject`, `KusoService`, `KusoEnvironment`, `KusoAddon`, `KusoBuild`, `KusoCron` are real CRDs. GitOps works. `kubectl edit` works. When the abstraction leaks, you're on familiar ground.
- **Agent-native, not bolted on.** Every UI action has a typed CLI command and a first-party MCP tool. Claude Code and other agents drive the platform end-to-end — provision a project, add a service, attach an addon, tail logs, troubleshoot.
- **Built to scale up.** Postgres-backed control plane with `RollingUpdate` and multi-replica `kuso-server`. CloudNativePG-backed HA Postgres addons (3 replicas, ~30s automatic failover). Sentinel-backed HA Redis. Multi-node clusters with token-based bootstrap, label-driven placement, and auto-cordon on node failure.
- **Zero-downtime self-update.** `make ship` cuts a GitHub release; every running instance pulls itself forward on the next updater tick. No ssh-from-laptop. CRD changes apply automatically.
- **Honest about what's missing.** Multi-region active/active and edge runtimes aren't on the roadmap — you have Cloudflare and managed Postgres for that. We do the control plane and the cluster well; we don't pretend to be Vercel + AWS in a box.

## Install

One-command install on a fresh Ubuntu 22/24 or Debian 12/13 box. Provisions k3s + traefik + cert-manager + Let's Encrypt + Postgres + the kuso operator/server/registry. ~5 minutes from `curl` to a logged-in dashboard.

**Before you run it**, point an A record at the box's public IP:

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

The install uses Let's Encrypt **prod** by default — your browser sees a real cert from the first load. The DNS pre-flight catches most misconfigs before any ACME call goes out, so the prod rate limit (50 certs/week per registered domain, 5 failed validations/hour) is rarely a problem in practice. If you're still iterating on DNS or doing repeated installs against the same domain, pass `--le-staging` to use the staging issuer (loose rate limits, but browser warns about the untrusted cert). See `--help` for all flags.

The installer is **idempotent**: re-running it on the same box (e.g. with a newer `--server-version`) preserves the admin password, JWT secret, Postgres credentials, and GitHub App configuration, and skips any provisioning step that's already done. To roll the platform to a new tag, the supported path is `kuso upgrade` (see [Self-update](#self-update)) — re-running install is a fallback for when self-update is broken.

GitHub-driven deploys can be set up two ways:

- (a) **Post-install** at `https://kuso.example.com/settings/github` — paste your GitHub App ID, slug, client secret, webhook secret, and private key. No reinstall needed.
- (b) **At install time** with `--github-wizard`, which prompts on stdin for the same values.

Without either, services still build via `kuso build trigger` against any public repo URL — the repo picker just stays empty.

## Install the CLI

On your workstation:

```bash
curl -fsSL https://kuso.example.com/install-cli.sh | sh
```

(Replace `kuso.example.com` with your instance.) The script downloads a prebuilt binary for your platform from GitHub releases — or falls back to `go install` if you have Go on PATH and no release asset matches your OS/arch. Drops `kuso` into `~/.local/bin/` (no sudo) or `/usr/local/bin/` (run as root).

Then point the CLI at your instance:

```bash
kuso login --api https://kuso.example.com -u admin
```

If you installed with `--le-staging` the cert isn't browser-trusted yet; prefix the command with `KUSO_INSECURE=1` until you flip to prod.

## Self-update

kuso watches its own GitHub releases. When a new tag ships, every
running instance picks it up:

```bash
kuso upgrade                     # update to latest
kuso upgrade --version v0.7.13   # pin to a specific tag (rollback / hotfix)
kuso upgrade --check             # see current/latest, don't apply
```

Or click **Settings → Updates** in the dashboard. The updater is an
in-cluster Job that swaps the kuso-server image, rolls the operator
when applicable, and applies any new CRDs. ~2 minutes typically.

There's no rollout-from-laptop step in the release flow — releasing
publishes a GH release, and every kuso install pulls itself forward
on its own schedule.

## Scaling

kuso is built to grow with you:

- **Add nodes** in `Settings → Nodes → Add node`. Token-based bootstrap mints a single-use `curl … | sudo sh` one-liner; paste it on the new VM and the agent registers itself. Works behind NAT — no SSH config from kuso. Auto-cordon on node failure (NotReady > 5 min), auto-uncordon on recovery. Full details: **[docs/NODE_BOOTSTRAP.md](./docs/NODE_BOOTSTRAP.md)**.
- **Scale `kuso-server`** by bumping the deployment replicas. The control plane is stateless above the database — Postgres is the source of truth, `RollingUpdate` is the deploy strategy.
- **Run HA addons** with `KusoAddon.spec.ha = true` — CloudNativePG for Postgres (3 replicas, automatic failover), Redis Sentinel for Redis. See **[docs/ADDON_HA.md](./docs/ADDON_HA.md)**.
- **Place workloads** with the label-driven placement editor. `kuso.sislelabs.com/<key>` labels on nodes, AND-of-labels selectors on services and addons. Build node pools, GPU pools, region pools — it's just labels.
- **Point at managed Postgres** by replacing the `kuso-postgres-conn` Secret's `dsn` with your RDS / Crunchy Bridge / Supabase URI. The bundled in-cluster Postgres is for installs that want a single-binary stack; bring your own when you outgrow it.

## Backups

The control-plane Postgres database holds users, sessions, audit logs, GitHub App config, and instance secrets. Backup is enabled by default — `kuso backup` pulls a consistent dump from your workstation. See **[docs/BACKUP_RESTORE.md](./docs/BACKUP_RESTORE.md)** for the daily-snapshot pattern, recovery paths (rollback, corruption, host loss), and addon data backups. Read this once before putting anything important on the box.

## Editing live deployments safely

Some spec edits on a running service are free (env vars, scale), some trigger a rolling restart (port, image), some hit Let's Encrypt rate limits (TLS hosts), and a few will orphan data if you're not careful (volumes). The contract is in **[docs/EDIT_SAFETY.md](./docs/EDIT_SAFETY.md)** — per-field, with the blast radius spelled out. Worth a read before mass-editing live envs from a script or the CLI.

## Claude Code skill

Drop a kuso skill into any project deployed here so Claude knows the CLI surface, the deploy lifecycle, the `${{ ... }}` env-var ref syntax, and the standard debug playbook. From your project repo root:

```bash
curl -fsSL https://raw.githubusercontent.com/sislelabs/kuso/main/skills/kuso/install.sh | bash
```

That writes `.claude/skills/kuso/SKILL.md`; restart Claude Code and `/skills` will show **kuso** active. See **[skills/kuso/](./skills/kuso/)** for the contents and update flow.

## Repo layout

| Path         | What it is                                                                |
| ------------ | ------------------------------------------------------------------------- |
| `server-go/` | Go backend + REST API. Postgres-backed. Serves the embedded SPA from `internal/web`. |
| `web/`       | Next.js 16 frontend. Built into `server-go/internal/web/dist`.            |
| `operator/`  | Kubernetes operator that reconciles `Kuso{Project,Service,...}` CRs.     |
| `cli/`       | `kuso` command-line tool (Go, Cobra).                                     |
| `mcp/`       | `kuso-mcp` Model Context Protocol server (Go).                            |
| `deploy/`    | Production manifests applied to the test cluster.                         |
| `docs/`      | Architecture + workflow docs.                                              |

## License

[AGPL-3.0](./LICENSE). Network use triggers the source-disclosure obligation — if you run kuso as a hosted service, your modifications must be available to your users.

© SisleLabs and contributors.
