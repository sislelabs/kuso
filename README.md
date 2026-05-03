# kuso

Self-hosted, agent-native PaaS for indie developers running a portfolio of products on Kubernetes.

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
| `docs/`      | Architecture + workflow docs.                                              |

## License

[AGPL-3.0](./LICENSE). Network use triggers the source-disclosure obligation — if you run kuso as a hosted service, your modifications must be available to your users.

© SisleLabs and contributors.
