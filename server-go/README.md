# kuso-server (Go)

Go rewrite of the Kuso control-plane HTTP API. Replaces `server/` (NestJS)
per `kuso/docs/REWRITE.md`.

**Status:** Phase 1 — kube client wired (read-only typed wrappers over the
six Kuso CRDs). The TS server in `server/` remains the production backend;
do not deploy this image yet.

## Layout

```
server-go/
├── cmd/
│   ├── kuso-server/    main.go: flag parsing, signal handling, http.Server
│   └── kube-smoke/     workstation-only binary that lists KusoEnvironments
│                       from a live cluster — not shipped in the image
├── internal/
│   ├── version/        embeds VERSION (source of truth for the image tag)
│   └── kube/           client-go config + typed wrappers over our 6 CRDs
│                       (Kuso, KusoProject, KusoService, KusoEnvironment,
│                        KusoAddon, KusoBuild)
├── go.mod
└── README.md
```

## Live cluster smoke test (Phase 1 acceptance)

```sh
KUBECONFIG=~/.kube/hetzner.yaml go run ./cmd/kube-smoke -namespace kuso
```

Should print a tab-separated list of every KusoEnvironment in the namespace
plus the `secretsRev` value the secret-write logic in Phase 4 will bump.

The full target layout (handlers, kube client, db, github, ...) is enumerated
in `kuso/docs/REWRITE.md` §2 and gets filled in across Phases 1–8.

## Build

```sh
cd kuso/server-go
go build ./...
go test ./...
```

To run locally:

```sh
go run ./cmd/kuso-server
curl -s localhost:3000/healthz
# {"status":"ok","version":"v0.2.0-dev"}
```

`KUSO_HTTP_ADDR` (or `--addr`) overrides the listen address.

## Container image

```sh
docker build -f kuso/server-go/Dockerfile -t ghcr.io/sislelabs/kuso-server-go:v0.2.0-dev kuso/server-go
docker run --rm -p 3000:3000 ghcr.io/sislelabs/kuso-server-go:v0.2.0-dev
```

The image is a `FROM scratch` static binary; no shell, no package manager.

## Versioning

`internal/version/VERSION` is the single source of truth. It is embedded at
compile time via `//go:embed` and stamped into the OCI image label by the
Dockerfile build arg.
