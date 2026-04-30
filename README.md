# kuso

Self-hosted, agent-native PaaS for indie developers running a portfolio of products on Kubernetes.

**Status:** v0.1.0 — pre-release. Forked from [kubero-dev/kubero](https://github.com/kubero-dev/kubero) (GPL-3.0). Hard divergence — not tracking upstream.

## What is kuso?

kuso is a Kubernetes-native PaaS designed to be driven entirely from a terminal — by you or by an AI agent. Every operation that exists in the UI is reachable from a typed CLI command, and every CLI command is callable from a first-party MCP server. Apps sleep when idle, autoscale when busy, and the whole platform fits in your head.

## Repo layout

| Path        | What it is                                                  |
| ----------- | ----------------------------------------------------------- |
| `server/`   | NestJS backend + REST API (TypeScript)                      |
| `client/`   | Vue.js frontend (TypeScript)                                |
| `operator/` | Kubernetes operator that reconciles `KusoApp` CRs (Go)      |
| `cli/`      | `kuso` command-line tool (Go, Cobra)                        |
| `mcp/`      | `kuso-mcp` Model Context Protocol server (Go) — coming soon |
| `docs/`     | Product docs and PRDs                                       |

## License

GPL-3.0. See [LICENSE](./LICENSE).

This project is a fork of [kubero-dev/kubero](https://github.com/kubero-dev/kubero) and inherits its GPL-3.0 license. Original copyright belongs to the Kubero authors; modifications and new code in this repo are © SisleLabs and contributors.
