# Docker Compose → kuso import

**Date:** 2026-06-06
**Status:** Implemented

## Implementation notes (delta from design)

- **`runtime: image` in kuso.yaml** turned out to require less than feared: the
  `projects` domain already had `ServiceImageSpec` on the create path, so wiring
  was (1) add `image` to `spec.ServiceSpec` + the patch path, (2) widen
  `validRuntime` to accept `image`/`worker`. No CRD change. `PullPolicy` was
  dropped from the kuso.yaml `ImageSpec` — the domain layer doesn't expose it.
- **Project auto-create:** `spec.Apply` creates services/addons/crons but NOT
  the project. Both `--apply` (CLI) and the web Apply now create the project
  first (ignoring 409) before applying the generated kuso.yaml.
- **env_file tolerance (bug found in testing):** compose-go stats referenced
  `env_file` paths and hard-fails if absent. Set `SkipResolveEnvironment` so a
  missing `.env` flags rather than aborts; inline `environment:` still parses.
- **Implicit `default` network** is suppressed from the report (compose adds it
  to every service; flagging it would be noise).

## Goal

Let users bring an existing `docker-compose.yml` into kuso with one command,
turning compose services into idiomatic kuso resources (KusoProject +
KusoService + KusoAddon) instead of hand-translating every field. Datastore
services become managed addons; app services become build/image services.
Everything that doesn't map cleanly is **reported, never silently dropped**.

## Non-goals

- Running compose itself, or a compose-compatible runtime. We convert, we don't emulate.
- Bidirectional sync. This is a one-shot import, not a living mirror.
- Mapping every compose key. Out-of-model keys are flagged for the user to handle by hand.
- Multi-project import. One compose file → one kuso project (matches "one apply per project").

## Architecture

A **pure converter package** with two thin frontends, mirroring the existing
Coolify importer (`coolify/` module + `cli/cmd/kusoCli/migrate.go` + a server
handler). The converter takes compose bytes and returns a `spec.File` (the
kuso.yaml shape) plus a `Report`; it does no I/O and makes no kube calls. Both
frontends then route the resulting `spec.File` through the **existing**
`spec.Apply` path, so compose import inherits diff/plan/prune semantics for free
and we never build a second write path.

```
compose/                          # NEW Go module (sibling of coolify/)
  go.mod                          — depends on compose-go (canonical compose-spec parser)
  parse.go                        — load + parse docker-compose.yml via compose-go
  convert.go                      — compose model → spec.File
  classify.go                     — datastore-image detection → addon kind + version
  report.go                       — Report type: per-decision lines + "not imported" flags
  mapping.go                      — small shared helpers (slugify, port parse, tag→version)
  mapping_test.go                 — table-driven fixtures → expected spec.File + Report

cli/cmd/kusoCli/import.go         — `kuso import compose` (dry-run default, -o, --apply, --project)
server-go/internal/http/handlers/import_compose.go  — POST /api/import/compose (preview + apply)
server-go/internal/http/router.go — mount the route
web/src/features/import/{api,hooks}.ts  — client + hooks
web/src/components/import/          — upload → preview table → Create button
```

**Why a new module, not part of `spec`:** keeps compose-specific parsing and the
`compose-go` dependency out of the server's core spec package, exactly as
`coolify/` is its own module. The converter imports `spec` (to build `spec.File`),
not the other way around.

## Mapping

The converter walks each compose service and produces either a KusoService or a
KusoAddon, accumulating a `Report` of every decision and every unmapped field.

### Service classification (first pass)

- `image:` matches a known datastore → **Addon**. Supported kinds:
  postgres, mysql, mariadb, redis, valkey, mongodb, clickhouse, kafka, rabbitmq.
  Version parsed from the image tag (e.g. `postgres:16-alpine` → version `16`).
  Report: `db → addon (kind=postgres, version=16)`.
- Otherwise → **KusoService**.

### App service → KusoService (`spec.ServiceSpec`)

| Compose | kuso | Notes |
|---|---|---|
| `build: ./dir` (string or object) | `runtime: dockerfile`, `repo: ""` | repo blank → flagged "needs repo" |
| `image: x/y:tag` (no build) | `runtime: image`, `image: {repository, tag}` | requires new `image` field on ServiceSpec (see Schema change) |
| `ports: ["8080:80"]` | `port: 80` (container side) + a `domains` entry | first published port; extra ports reported |
| `environment:` / `env_file:` | `env: {…}` | env_file read relative to the compose file |
| `volumes: [data:/var/lib]` | `volumes: [{name, mountPath, sizeGi: 5}]` | named volumes only; bind mounts flagged |
| `deploy.replicas: 3` | `scale: {min: 3, max: 3}` | |
| `command:` | `command: [...]` | |
| `depends_on: [db]` | env-ref rewrite | if `db` is an addon, rewrite matching env values referencing `db` to `${{ db.URL }}` form; each rewrite reported |

### Datastore service → KusoAddon (`spec.AddonSpec`)

`AddonSpec{name, kind, version, storageSize from the attached volume if any}`.
kuso wires the conn-secret automatically (every env in the project receives
`envFromSecrets`), so we emit **no** explicit conn env refs for the addon — we
report: "addon `db` created; services receive `<DB>_*` connection vars
automatically."

### Flagged as NOT imported (no kuso target — reported with a one-line reason)

- `healthcheck` — kuso auto-detects probes by runtime.
- `restart:` — kuso always restarts.
- `networks`, `profiles`, `secrets`/`configs`, `extra_hosts`, `cap_add`,
  `privileged`, bind-mount volumes, and any other key with no kuso equivalent.

## Schema change

`runtime: image` services need a registry pointer. The KusoService CR already
has `spec.image {repository, tag, pullPolicy}` (`server-go/internal/kube/types.go:248`),
but the kuso.yaml `spec.ServiceSpec` does **not** expose it. We add:

```go
// spec.ServiceSpec
Image *ImageSpec `yaml:"image,omitempty"` // runtime: image — registry pointer

type ImageSpec struct {
    Repository string `yaml:"repository,omitempty"`
    Tag        string `yaml:"tag,omitempty"`
    PullPolicy string `yaml:"pullPolicy,omitempty"`
}
```

Wire it through `spec.Apply` → `KusoService.Spec.Image` (one mapping line,
mirroring the CR field) and through `spec/export.go` so round-tripping stays
lossless. **No CRD schema change** — the CR field already exists. This is a
net-positive standalone improvement: it makes `runtime: image` authorable from
kuso.yaml at all, which it currently isn't.

## Frontends

### CLI — `kuso import compose`, mirroring `migrate coolify`

```
kuso import compose docker-compose.yml                 # dry-run: print report + generated kuso.yaml
kuso import compose docker-compose.yml -o kuso.yaml    # write the kuso.yaml to disk
kuso import compose docker-compose.yml --apply         # convert + apply to live instance (report first)
kuso import compose docker-compose.yml --project foo   # override project name (default: compose dir name)
```

Dry-run is the default (touches nothing), exactly like the coolify migrator.
`--apply` requires a kuso login.

### Web — `POST /api/import/compose`

Upload compose file → server runs the **same converter** → returns
`{ plan, report }` (preview, no writes) → user clicks **Create** → second call
applies. Report renders as a table: one row per resource + its mapping decisions
+ any flags. Reuses the existing apply-plan rendering.

## Error handling

- Parse failures (invalid compose) → 400 / non-zero exit with the compose-go error.
- Unknown datastore image with a datastore-ish name → treated as a plain service, flagged "looks like a datastore but image unrecognized; left as a service."
- `--apply` without login → same guard as `migrate coolify`.
- Apply itself reuses `spec.Apply`'s existing error mapping (prune gate, conflicts).

## Testing

- `compose/mapping_test.go` — table-driven fixture compose files → expected
  `spec.File` + `Report`. Covers each mapping row, each datastore kind, every
  flag case, and the `depends_on` env rewrite.
- Server handler test for `POST /api/import/compose` (preview + apply).
- `spec.Apply` test for the new `image` field (round-trip through export).
