---
name: repo-orientation
description: Use at the start of any kuso work session. Tells you where things live in the monorepo, what each subdir does, and what NOT to touch.
---

# kuso repo orientation

kuso is a hard fork of [kubero-dev/kubero](https://github.com/kubero-dev/kubero) — a self-hosted, agent-native PaaS for indie developers. Solo maintainer (Ivo Sabev / SisleLabs). Hard divergence — we do **not** track upstream.

## Layout

| Path         | Stack                                  | What it does                                                                                |
| ------------ | -------------------------------------- | ------------------------------------------------------------------------------------------- |
| `server-go/` | Go + chi + client-go                   | REST API, auth, orchestrates k8s via dynamic client. SQLite via modernc.org/sqlite (no CGO). Embeds the Vue SPA via //go:embed. |
| `client/`    | Vue 3 + Vuetify                        | Web UI. Built into `server-go/internal/web/dist`; the Go binary serves it.                  |
| `operator/`  | Go + Operator-SDK (helm-based)         | Reconciles `KusoProject`, `KusoService`, `KusoEnvironment`, `KusoBuild`, `KusoAddon` CRs.   |
| `cli/`       | Go + Cobra                             | `kuso` command-line tool. Talks to the server REST API.                                     |
| `mcp/`       | Go                                     | `kuso-mcp` Model Context Protocol server. Wraps `cli/` and REST API.                        |
| `deploy/`    | YAML manifests                         | Production manifests applied to the test cluster.                                           |
| `docs/`      | Markdown                               | PRD, REBRAND notes, REWRITE plan, WORKFLOWS reference, LIVE_TEST_PLAN runbook.              |
| `.claude/`   | Skill files (this dir)                 | Project-specific context for AI agents.                                                     |

> **Historical note:** `server/` was a NestJS+TypeScript backend that
> got rewritten into `server-go/` and removed in May 2026. See
> `docs/REWRITE.md`.

## Three things to know before editing

1. **CRD group is `application.kuso.sislelabs.com/v1alpha1`.** Anywhere you see `application.kubero.dev` it's either an unmigrated artifact (file a bug) or in `docs/REBRAND.md` as an intentional exception.

2. **Some upstream URLs are preserved on purpose.** See `docs/REBRAND.md` for the full list. Buildpack images (`ghcr.io/kubero-dev/buildpacks/*`) and template repos (`raw.githubusercontent.com/kubero-dev/templates/*`) still point upstream because we haven't mirrored them yet. Don't "fix" these without checking REBRAND.md first.

3. **`README.md`, `LICENSE`, and `NOTICE` at the repo root are attribution to upstream.** Don't replace `Kubero` with `kuso` in those — GPL-3.0 requires we preserve attribution. The brand-replacement scripts explicitly skip these three files.

## Common tasks → where to look

| Task                                     | Subdir(s)                                                |
| ---------------------------------------- | -------------------------------------------------------- |
| Add a new CLI command                    | `cli/cmd/kusoCli/` + maybe `cli/pkg/`                    |
| Add a REST endpoint                      | `server-go/internal/http/handlers/` + a service package  |
| Change CRD schema                        | `operator/helm-charts/<chart>/` + `server-go/internal/kube/types.go` |
| Add an MCP tool                          | `mcp/`                                                   |
| Update UI                                | `client/src/`                                            |
| Add a new addon                          | `operator/helm-charts/kusoaddon<name>`                   |

## Before opening a PR

- `cli/`: `cd cli && go build ./... && go vet ./...`
- `operator/`: `cd operator && make`
- `server-go/`: `cd server-go && go vet ./... && go build ./... && go test ./...`
- `client/`: `cd client && yarn build` (output lands in `server-go/internal/web/dist/`)

There is no monorepo-wide build yet — each subproject builds independently.
