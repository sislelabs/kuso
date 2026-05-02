# kuso

Self-hosted, agent-native PaaS for indie developers running a portfolio of products on Kubernetes.

**Status:** v0.2.0 — live release. Forked from [kubero-dev/kubero](https://github.com/kubero-dev/kubero) (GPL-3.0). Hard divergence — not tracking upstream.

## What is kuso?

kuso is a Kubernetes-native PaaS designed to be driven entirely from a terminal — by you or by an AI agent. Every operation that exists in the UI is reachable from a typed CLI command, and every CLI command is callable from a first-party MCP server. Apps sleep when idle, autoscale when busy, and the whole platform fits in your head.

## Repo layout

| Path         | What it is                                                                |
| ------------ | ------------------------------------------------------------------------- |
| `server-go/` | Go backend + REST API. Serves the embedded SPA from `internal/web`.       |
| `web/`       | Next.js 16 frontend. Built into `server-go/internal/web/dist`.            |
| `operator/`  | Kubernetes operator that reconciles `Kuso{Project,Service,...}` CRs.     |
| `cli/`       | `kuso` command-line tool (Go, Cobra).                                     |
| `mcp/`       | `kuso-mcp` Model Context Protocol server (Go).                            |
| `deploy/`    | Production manifests applied to the test cluster.                         |
| `docs/`      | Product docs and PRDs (incl. `docs/superpowers/specs/` for in-flight specs). |

> **History:** the original NestJS server lived under `server/` and was
> retired in May 2026 after the Go rewrite reached parity. The Vue 3
> dashboard lived under `client/` and was retired alongside Phase F of
> the Next.js rewrite. See `docs/REWRITE.md` for the server migration
> plan, `docs/superpowers/specs/2026-05-02-frontend-rewrite-nextjs-design.md`
> for the frontend rewrite, and `docs/WORKFLOWS.md` for the HTTP surface
> reference.

## License

GPL-3.0. See [LICENSE](./LICENSE).

This project is a fork of [kubero-dev/kubero](https://github.com/kubero-dev/kubero) and inherits its GPL-3.0 license. Original copyright belongs to the Kubero authors; modifications and new code in this repo are © SisleLabs and contributors.
