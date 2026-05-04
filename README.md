# kuso

Self-hosted, agent-native PaaS for indie developers running a portfolio of products on Kubernetes.

## What is kuso?

kuso is a Kubernetes-native PaaS designed to be driven entirely from a terminal — by you or by an AI agent. Every operation that exists in the UI is reachable from a typed CLI command, and every CLI command is callable from a first-party MCP server. Apps sleep when idle, autoscale when busy, and the whole platform fits in your head.

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

By default the install uses Let's Encrypt **staging** certs (browser warns about untrusted cert) — flip to prod with one command after you've confirmed DNS works. See `--help` for all flags.

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
# Fresh install still on Let's Encrypt staging certs?
KUSO_INSECURE=1 kuso login --api https://kuso.example.com -u admin
```

After flipping the instance to LE prod, drop the `KUSO_INSECURE=1`.

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
