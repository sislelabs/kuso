# kuso-server (Go)

The kuso control-plane HTTP API. Stateless above Postgres; multi-replica with `RollingUpdate`; embeds the Next.js SPA via `//go:embed`.

## Layout

```
server-go/
├── cmd/
│   ├── kuso-server/    main.go: flag parsing, signal handling, http.Server
│   ├── kube-smoke/     workstation-only binary that lists KusoEnvironments
│   │                   from a live cluster — not shipped in the image
│   └── uipreview/      dev-only: serves a local web/out export and proxies
│                       read-only API GETs to a live instance (UI review)
├── internal/
│   ├── version/        embeds VERSION (source of truth for the image tag)
│   ├── kube/           client-go config + typed wrappers + shared informer
│   │                   cache over our CRDs (Project/Service/Environment/
│   │                   Addon/Build/Cron/Run)
│   ├── db/             Postgres connection (lib/pq), embedded schema.sql +
│   │                   numbered migrations/, per-resource CRUD helpers
│   ├── leader/         coordination.k8s.io/Lease-based election for
│   │                   singleton background workers
│   ├── http/           chi router, JWT middleware, handlers/
│   ├── projects/       project + service + env domain logic
│   ├── addons/         polymorphic addon provisioning (Postgres, Redis, …)
│   ├── builds/         build orchestration (shared buildkitd + buildpacks)
│   ├── secrets/        env-var rewriting + K8s Secret-backed values
│   ├── notify/         event dispatcher (DB outbox → webhook fan-out + DB mirror)
│   ├── nodewatch/      auto-cordon on NotReady
│   ├── nodemetrics/    5-min sampler with rolling retention
│   ├── github/         App auth + webhook receiver + repo browser
│   └── ...             see CLAUDE.md for the full domain map
├── go.mod
└── README.md
```

## Build

```sh
cd server-go
go build ./...
go test ./...
```

To run locally:

```sh
KUSO_DB_DSN="postgres://kuso:dev@localhost:5432/kuso?sslmode=disable" \
JWT_SECRET=dev KUSO_ALLOW_INSECURE_JWT=1 \
go run ./cmd/kuso-server

curl -s localhost:3000/healthz
# {"status":"ok","version":"vX.Y.Z"}   (whatever internal/version/VERSION says)
```

`KUSO_HTTP_ADDR` (or `--addr`) overrides the listen address (default `:3000`). A fresh install refuses to boot with a short `JWT_SECRET` unless `KUSO_ALLOW_INSECURE_JWT=1` is set — fine for a throwaway dev DB, never for a real one.

### Rotating `JWT_SECRET`

Tokens carry a `kid` header derived from the secret that signed them. `KUSO_JWT_PREVIOUS_SECRETS` (comma-separated) lists retired secrets that still verify tokens but never sign new ones. Tokens without a `kid` (everything older kuso versions issued) are checked against the current secret, then each previous one.

To rotate without logging anyone out:

1. In Secret `kuso/kuso-server-secrets`, set `KUSO_JWT_PREVIOUS_SECRETS` to the old value and `JWT_SECRET` to a new 32+ character random string.
2. Restart kuso-server: `kubectl -n kuso rollout restart deploy/kuso-server`.
3. Once everyone has signed in again and API tokens have been re-issued, remove the old value from `KUSO_JWT_PREVIOUS_SECRETS` and restart again. Never-expiring tokens keep verifying until you do.

If you're rotating because the old secret leaked or is weak, skip the previous list: that secret can still forge tokens for as long as it's listed. kuso-server logs a `SECURITY` error at boot when a previous secret is weak.

## Container image

```sh
# run from the repo root: the image also builds ../web
docker build -f server-go/Dockerfile -t ghcr.io/sislelabs/kuso-server-go:vX.Y.Z .
docker run --rm -p 3000:3000 \
  -e KUSO_DB_DSN=... -e JWT_SECRET=... \
  ghcr.io/sislelabs/kuso-server-go:vX.Y.Z
```

The image is a `FROM scratch` static binary; no shell, no package manager, no CGO.

## Versioning

`internal/version/VERSION` is the single source of truth. It is embedded at compile time via `//go:embed` and stamped into the OCI image label by the Dockerfile build arg.

## Multi-replica notes

- `kuso-server` runs as a Deployment with `RollingUpdate`. Replicas can be scaled freely.
- Singleton background workers (build poller, alert engine, nodewatch, nodemetrics, daily cleanup, …) are leader-elected via `coordination.k8s.io/Lease`. Lease writes are granted by the home-namespace Role; without them no replica ever wins and the singletons never run. `KUSO_DISABLE_LEADER_ELECTION=true` runs them unconditionally (single-replica only, or replicas double-fire).
- The notify dispatcher enqueues every event into a Postgres outbox from any replica; delivery is a leader-gated drain (`FOR UPDATE SKIP LOCKED`, retries with backoff), so webhooks are at-least-once and not N-times. The bell-icon feed is read from Postgres too.
- Connection pool: `MaxOpenConns=25` per replica (`KUSO_DB_MAX_CONNS` overrides). The bundled install routes the DSN through `kuso-pgbouncer` in front of the CNPG cluster (`max_connections=100`); with managed Postgres, point `KUSO_DB_DSN` at its pooler.

See the repo-root `CLAUDE.md` for the full runtime shape.
