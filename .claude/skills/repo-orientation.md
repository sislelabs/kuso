---
name: repo-orientation
description: Use at the start of any kuso work session. Tells you where things live in the monorepo, what each subdir does, and what NOT to touch.
---

# kuso repo orientation

kuso is a hard fork of [kubero-dev/kubero](https://github.com/kubero-dev/kubero) — a self-hosted, agent-native PaaS for indie developers. Solo maintainer (Ivo Sabev / SisleLabs). Hard divergence — we do **not** track upstream.

## Layout

| Path        | Stack                                  | What it does                                                            |
| ----------- | -------------------------------------- | ----------------------------------------------------------------------- |
| `server/`   | NestJS (TypeScript)                    | REST API, auth, orchestrates k8s via the operator. Uses Prisma + sqlite.|
| `client/`   | Vue 3 + Vuetify                        | Web UI. Talks only to `server/` REST API.                               |
| `operator/` | Go + Operator-SDK (helm-based)         | Reconciles `KusoApp`, `KusoPipeline`, `KusoBuild`, addon CRs.            |
| `cli/`      | Go + Cobra                             | `kuso` command-line tool. Talks to `server/` REST API.                  |
| `mcp/`      | Go (planned)                           | `kuso-mcp` Model Context Protocol server. Wraps `cli/` and REST API.    |
| `docs/`     | Markdown                               | PRD, REBRAND notes, architecture docs.                                  |
| `.claude/`  | Skill files (this dir)                 | Project-specific context for AI agents.                                 |

## Three things to know before editing

1. **CRD group is `application.kuso.sislelabs.com/v1alpha1`.** Anywhere you see `application.kubero.dev` it's either an unmigrated artifact (file a bug) or in `docs/REBRAND.md` as an intentional exception.

2. **Some upstream URLs are preserved on purpose.** See `docs/REBRAND.md` for the full list. Buildpack images (`ghcr.io/kubero-dev/buildpacks/*`) and template repos (`raw.githubusercontent.com/kubero-dev/templates/*`) still point upstream because we haven't mirrored them yet. Don't "fix" these without checking REBRAND.md first.

3. **`README.md`, `LICENSE`, and `NOTICE` at the repo root are attribution to upstream.** Don't replace `Kubero` with `kuso` in those — GPL-3.0 requires we preserve attribution. The brand-replacement scripts explicitly skip these three files.

## Common tasks → where to look

| Task                                     | Subdir(s)                              |
| ---------------------------------------- | -------------------------------------- |
| Add a new CLI command                    | `cli/cmd/kusoCli/` + maybe `cli/pkg/`  |
| Add a REST endpoint                      | `server/src/<module>/`                 |
| Change CRD schema                        | `operator/helm-charts/<chart>/`        |
| Add an MCP tool                          | `mcp/` (TBD — not yet implemented)     |
| Update UI                                | `client/src/`                          |
| Add a new addon                          | `operator/helm-charts/kusoaddon<name>` |

## Before opening a PR

- `cli/`: `cd cli && go build ./... && go vet ./...`
- `operator/`: `cd operator && make`
- `server/`: `cd server && yarn lint && yarn test`
- `client/`: `cd client && yarn lint && yarn build`

There is no monorepo-wide build yet — each subproject builds independently.
