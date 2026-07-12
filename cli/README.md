# kuso CLI

A command-line client for [kuso](https://github.com/sislelabs/kuso), a
self-hosted, Kubernetes-native PaaS.

The CLI talks to a running kuso server. Cluster install is a separate
one-shot — see `hack/install.sh` in the kuso repo.

---

## Install

### One-liner (recommended)

Each kuso instance serves the installer at `/install-cli.sh`. Replace
`kuso.example.com` with your instance:

```sh
curl -fsSL https://kuso.example.com/install-cli.sh | sh
```

The script picks a prebuilt binary from GitHub releases for your OS/arch,
or falls back to `go install` if you have Go on PATH and no asset matches.
It writes to `~/.local/bin/kuso` (no sudo) or `/usr/local/bin/kuso`
(when run as root).

### Build from source

```sh
git clone https://github.com/sislelabs/kuso
cd kuso/cli
go build -o kuso ./cmd
sudo mv kuso /usr/local/bin/kuso
```

### Supported platforms

- macOS (amd64, arm64)
- Linux (amd64, arm64)
- Windows (amd64) — via WSL recommended

---

## First run

```sh
kuso login --api https://kuso.example.com -u admin -p '<password>'
```

The `--instance` flag lets you save multiple servers under different
labels:

```sh
kuso login --api https://staging.kuso.example.com -u admin -p '...' --instance staging
kuso remote select prod    # switch to the prod instance
```

---

## Command overview

The CLI has web-UI parity — roughly 50 top-level commands. The ones you'll
reach for first, by theme:

```
# Session & orientation
kuso login --api https://kuso.example.com     # auth (also: --token, --instance)
kuso doctor                                   # pre-flight: DNS, TLS, webhook health
kuso status [project]                         # project rollup
kuso get projects|services|envs|addons|pods|roles
kuso version / kuso upgrade                   # client version / server self-update

# Resources
kuso project create|update|describe|delete|stop|start|export <name>
kuso project service add|set|delete|rename|stop|start|wake <project> <service>
kuso project addon add|update|delete|subscribe|unsubscribe|placement|public-tcp …
kuso environment add|delete|list              # long-lived envs (staging, …)
kuso domains add|remove|list <project> <service>

# Env vars & secrets
kuso env list|set|unset|share|unshare <project> <service> …
kuso secret list|set|unset <project> <service> …   # K8s-Secret-backed
kuso shared-secret list|set|unset <project>        # project-level

# Builds, runs, logs, debugging
kuso build trigger|list|latest|rollback|cancel|why <project> <service>
kuso redeploy <project> <service>
kuso run <project> <service> -- <cmd…>        # one-shot Job
kuso logs <project> <service> [-f]  ·  kuso logs search --q "…"
kuso shell <project> <service>
kuso service errors|pods|drift <project> <service>
kuso db sql|tables|columns|rows|connect|port-forward <project> <addon>  # admin
kuso health [fix <resource>]                  # reconcile health + remediation

# Operations & admin
kuso addon-backup list|download|restore|schedule <project> <addon>
kuso backup [settings|health|db-stats]  ·  kuso restore <file>
kuso cron list|add|add-http|add-command|edit|sync|delete …
kuso node add-token|list|label|updates|apply-updates …
kuso role|user|invite|group|token|instance-config|instance-secret …
kuso marketplace list|info|deploy  ·  kuso import compose  ·  kuso apply
```

Add `--help` to any command or group for the full tree and flags. Add
`-o json` to most read-only commands for machine output.

---

## Examples

### Bootstrap a project from an existing repo

```sh
kuso project create my-app \
  --repo https://github.com/me/my-app \
  --branch main --previews

kuso project service add my-app web \
  --runtime dockerfile --port 8080
```

The first build kicks off automatically when you push to `main`. Open a
PR and a preview env spawns at `https://web-pr-<n>.my-app.<your-domain>`.

### Manage env vars and secrets

```sh
# plain env vars (sit on the service spec; visible in YAML)
kuso env set my-app web LOG_LEVEL=info FEATURE_X=1

# secret-backed (mounted from a K8s Secret; values never round-trip)
kuso secret set my-app web DATABASE_URL "postgres://..."
kuso secret set my-app web SENTRY_DSN "$dsn" --env production
```

### Long-lived API tokens (CI)

```sh
kuso token create --name 'github-actions' --expires 90d
# print the token ONCE — save it. Then use:
kuso login --api https://kuso.example.com --token '<that token>'
```

### Flip previews on an existing project

```sh
kuso project update my-app --previews=on
```

---

## Development

```sh
git clone https://github.com/sislelabs/kuso
cd kuso/cli
go build -o kuso ./cmd
go test ./...
```

Issues and PRs welcome at
[github.com/sislelabs/kuso](https://github.com/sislelabs/kuso).

---

## License

See LICENSE in the repo root.
