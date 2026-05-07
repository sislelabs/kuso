# kuso-server (Go)

The kuso control-plane HTTP API. Stateless above Postgres; multi-replica with `RollingUpdate`; embeds the Next.js SPA via `//go:embed`.

## Layout

```
server-go/
├── cmd/
│   ├── kuso-server/    main.go: flag parsing, signal handling, http.Server
│   └── kube-smoke/     workstation-only binary that lists KusoEnvironments
│                       from a live cluster — not shipped in the image
├── internal/
│   ├── version/        embeds VERSION (source of truth for the image tag)
│   ├── kube/           client-go config + typed wrappers + shared informer
│   │                   cache over our 6 CRDs
│   ├── db/             Postgres connection (lib/pq), embedded schema.sql,
│   │                   per-resource CRUD helpers
│   ├── leader/         coordination.k8s.io/Lease-based election for
│   │                   singleton background workers
│   ├── http/           chi router, JWT middleware, handlers/
│   ├── projects/       project + service + env domain logic
│   ├── addons/         polymorphic addon provisioning (Postgres, Redis, …)
│   ├── builds/         kaniko/buildpacks build orchestration
│   ├── secrets/        env-var rewriting + K8s Secret-backed values
│   ├── notify/         async event dispatcher (webhook fan-out + DB mirror)
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
JWT_SECRET=dev \
go run ./cmd/kuso-server

curl -s localhost:3000/healthz
# {"status":"ok","version":"v0.9.x"}
```

`KUSO_HTTP_ADDR` (or `--addr`) overrides the listen address.

## Container image

```sh
docker build -f server-go/Dockerfile -t ghcr.io/sislelabs/kuso-server-go:v0.9.x server-go
docker run --rm -p 3000:3000 \
  -e KUSO_DB_DSN=... -e JWT_SECRET=... \
  ghcr.io/sislelabs/kuso-server-go:v0.9.x
```

The image is a `FROM scratch` static binary; no shell, no package manager, no CGO.

## Versioning

`internal/version/VERSION` is the single source of truth. It is embedded at compile time via `//go:embed` and stamped into the OCI image label by the Dockerfile build arg.

## Multi-replica notes

- `kuso-server` runs as a Deployment with `RollingUpdate`. Replicas can be scaled freely.
- Singleton background workers (build poller, alert engine, nodewatch, nodemetrics, daily cleanup) are leader-elected via `coordination.k8s.io/Lease`. The ClusterRole grants the lease verbs; without them the helper falls back to "always run" and replicas double-fire.
- The notify dispatcher is per-pod (256-event buffered channel). The bell-icon feed is read from Postgres, so events survive even if a single pod's buffer drops.
- Connection pool: `MaxOpenConns=25` per replica. With 3+ replicas plus operator + logship + addon pollers, the bundled Postgres `max_connections=100` is the next ceiling — PgBouncer or managed Postgres for serious deployments.

See `.claude/skills/platform-architecture.md` for the full runtime shape.
