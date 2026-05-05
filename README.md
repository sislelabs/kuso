# kuso

Self-hosted, agent-native PaaS for indie developers running a portfolio of products on Kubernetes.

## What is kuso?

kuso is a Kubernetes-native PaaS designed to be driven entirely from a terminal — by you or by an AI agent. Every operation that exists in the UI is reachable from a typed CLI command, and every CLI command is callable from a first-party MCP server. Apps sleep when idle, autoscale when busy, and the whole platform fits in your head.

## Why kuso, not Coolify (or Dokku, or CapRover)?

The honest pitch: **Coolify if you want the polished UI and the bigger community; kuso if you want the same primitives driveable by Claude Code or your own scripts, on real Kubernetes you can `kubectl edit` when things break.**

- **CLI/MCP-first, not bolted on.** Every UI action has a typed CLI command and a first-party MCP tool. AI agents can drive the platform directly. Coolify is API-first but UI-shaped — kuso is the inverse.
- **Real Kubernetes underneath.** `KusoProject`, `KusoService`, `KusoEnvironment`, `KusoAddon`, `KusoBuild`, `KusoCron` are real CRDs. GitOps works. `kubectl edit` works. When the abstraction leaks, you're on familiar ground. Coolify abstracts Docker; kuso abstracts Kubernetes.
- **Single-box honesty.** Designed for 1–3 nodes, 10–100 services. No HA, no multi-region, no edge functions. If you need those, this isn't the tool — and we say so up front instead of pretending.
- **Self-update without ssh.** `make ship` cuts a GitHub release; every running instance pulls itself forward on the next updater tick.

If you want the broadest feature surface and a Discord full of users, run Coolify. If you want infra you can drive with the same agent that writes your code, on a substrate that doesn't break when you peek under the hood, run kuso.

## Install

One-command install on a fresh Ubuntu 22/24 or Debian 12/13 box. Provisions k3s + traefik + cert-manager + Let's Encrypt + kuso operator/server/registry. ~5 minutes from `curl` to a logged-in dashboard.

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

The installer is **idempotent**: re-running it on the same box (e.g. with a newer `--server-version`) preserves the admin password, JWT secret, and GitHub App credentials, and skips any provisioning step that's already done. To roll the platform to a new tag, the supported path is `kuso upgrade` (see [Self-update](#self-update)) — re-running install is a fallback for when self-update is broken.

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

## Backups

The control-plane SQLite DB at `/var/lib/kuso/kuso.db` holds users, sessions, audit logs, GitHub App config, and instance secrets. Set `KUSO_BACKUP_ENABLED=1` on the server, then `kuso backup` pulls a consistent snapshot. See **[docs/BACKUP_RESTORE.md](./docs/BACKUP_RESTORE.md)** for the daily-snapshot pattern, recovery paths (rollback, corruption, host loss), and addon data backups. Read this once before putting anything important on the box.

## Repo layout

| Path         | What it is                                                                |
| ------------ | ------------------------------------------------------------------------- |
| `server-go/` | Go backend + REST API. Serves the embedded SPA from `internal/web`.       |
| `web/`       | Next.js 16 frontend. Built into `server-go/internal/web/dist`.            |
| `operator/`  | Kubernetes operator that reconciles `Kuso{Project,Service,...}` CRs.     |
| `cli/`       | `kuso` command-line tool (Go, Cobra).                                     |
| `mcp/`       | `kuso-mcp` Model Context Protocol server (Go).                            |
| `deploy/`    | Production manifests applied to the test cluster.                         |
| `docs/`      | Architecture + workflow docs.                                              |

## License

[AGPL-3.0](./LICENSE). Network use triggers the source-disclosure obligation — if you run kuso as a hosted service, your modifications must be available to your users.

© SisleLabs and contributors.
