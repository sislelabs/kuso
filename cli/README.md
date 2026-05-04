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

```
kuso login                 # auth against a kuso server
kuso get projects|services|envs|builds|tokens|addons
kuso project create|update|describe|delete <name>
kuso project service add|delete <project> <service>
kuso project env delete <project> <env>
kuso project addon add|delete <project> <addon>
kuso env list|set|unset <project> <service> ...    # plain env vars
kuso secret list|set|unset <project> <service> ... # K8s-Secret-backed
kuso build trigger|list <project> <service>
kuso token create|list|revoke
kuso github installations
kuso logs <project> <service>
kuso config show
kuso debug                  # print client + server versions
```

Add `--help` to any command for full flags. Add `-o json` to most
read-only commands for machine output.

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
