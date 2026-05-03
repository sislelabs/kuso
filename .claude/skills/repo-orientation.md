---
name: repo-orientation
description: Use at the start of any kuso work session. Tells you where things live in the monorepo, what each subdir does, and what NOT to touch.
---

# kuso repo orientation

kuso is a self-hosted, agent-native PaaS for indie developers. AGPL-3.0. Solo maintainer (Ivo Sabev / SisleLabs).

## Layout

| Path         | Stack                                  | What it does                                                                                |
| ------------ | -------------------------------------- | ------------------------------------------------------------------------------------------- |
| `server-go/` | Go + chi + client-go                   | REST API, auth, orchestrates k8s via dynamic client. SQLite via modernc.org/sqlite (no CGO). Embeds the Next.js SPA via //go:embed. |
| `web/`       | Next.js 16 (App Router, static export) | Web UI. Built into `server-go/internal/web/dist`; the Go binary serves it.                  |
| `operator/`  | Go + Operator-SDK (helm-based)         | Reconciles `KusoProject`, `KusoService`, `KusoEnvironment`, `KusoBuild`, `KusoAddon` CRs.   |
| `cli/`       | Go + Cobra                             | `kuso` command-line tool. Talks to the server REST API.                                     |
| `mcp/`       | Go                                     | `kuso-mcp` Model Context Protocol server. Wraps `cli/` and REST API.                        |
| `deploy/`    | YAML manifests                         | Production manifests applied to the test cluster.                                           |
| `docs/`      | Markdown                               | REDESIGN, REWRITE, WORKFLOWS, GITHUB_APP_SETUP, LIVE_TEST_PLAN.                              |
| `.claude/`   | Skill files (this dir)                 | Project-specific context for AI agents.                                                     |

## Two things to know before editing

1. **CRD group is `application.kuso.sislelabs.com/v1alpha1`.** That's the canonical group; anything else in the repo is a bug.

2. **`LICENSE` is AGPL-3.0.** Network-use triggers the source-disclosure obligation — important for a self-hosted PaaS where competitors might host kuso-as-a-service. Don't slip in MIT/Apache code without checking compatibility.

## Common tasks → where to look

| Task                                     | Subdir(s)                                                |
| ---------------------------------------- | -------------------------------------------------------- |
| Add a new CLI command                    | `cli/cmd/kusoCli/` + maybe `cli/pkg/`                    |
| Add a REST endpoint                      | `server-go/internal/http/handlers/` + a service package  |
| Change CRD schema                        | `operator/helm-charts/<chart>/` + `server-go/internal/kube/types.go` |
| Add an MCP tool                          | `mcp/`                                                   |
| Update UI                                | `web/src/`                                               |
| Add a new addon                          | `operator/helm-charts/kusoaddon<name>`                   |

## Before opening a PR

- `cli/`: `cd cli && go build ./... && go vet ./...`
- `operator/`: `cd operator && make`
- `server-go/`: `cd server-go && go vet ./... && go build ./... && go test ./...`
- `web/`: `cd web && pnpm build` (output lands in `server-go/internal/web/dist/`)

There is no monorepo-wide build yet — each subproject builds independently.
