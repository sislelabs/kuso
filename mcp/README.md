# kuso-mcp

A [Model Context Protocol](https://modelcontextprotocol.io) server for kuso. It lets MCP-speaking clients (Claude Code, Cursor, Claude Desktop) drive a kuso PaaS instance — list and describe apps, deploy, troubleshoot, manage secrets, etc.

**Status:** v0.1.0 — pre-release. Skeleton only; the only registered tool today is `health` (smoke test).

## Run

Set environment variables, then build and run:

```bash
export KUSO_URL=https://kuso.example.com
export KUSO_TOKEN=<your-api-token>
make build
./bin/kuso-mcp                 # full mode
./bin/kuso-mcp --read-only     # disables mutating tools
```

The server speaks MCP over stdio. Wire it up in your client by pointing at the `kuso-mcp` binary.

### Claude Code config (example)

```json
{
  "mcpServers": {
    "kuso": {
      "command": "/path/to/kuso-mcp",
      "env": {
        "KUSO_URL": "https://kuso.example.com",
        "KUSO_TOKEN": "..."
      }
    }
  }
}
```

## Tool surface

The full v1 tool surface is specified in [`docs/PRD.md` Workstream B](../docs/PRD.md). Current state:

| Tool                | Status      |
| ------------------- | ----------- |
| `health`            | implemented |
| `list_apps`         | implemented |
| `describe_app`      | implemented |
| `troubleshoot_app`  | implemented |
| `tail_logs`         | implemented |
| `restart_app`       | implemented (mutating; requires `confirm: true`) |
| `deploy_app`        | planned     |
| `set_app_config`    | planned (blocked on full IApp shape modeling) |
| `manage_secret`     | planned (blocked on operator envFrom support — Workstream C) |
| `exec_app`          | planned     |
| `cluster_health`    | planned     |
| `cost_report`       | planned     |
| `bootstrap_product` | planned     |

## Layout

```
mcp/
├── main.go               # entrypoint: parses flags, builds server, wires transports
├── go.mod
├── Makefile
├── internal/
│   ├── config/           # KUSO_URL / KUSO_TOKEN / --read-only
│   ├── kusoclient/       # HTTP client for the kuso server REST API
│   └── tools/            # MCP tool implementations
└── README.md
```

Adding a new tool: create a `register<Name>` function in `internal/tools/`, call it from `Register()`, define an args struct (with jsonschema tags) and a result struct.

## Tests

Unit tests (httptest-driven, fast):

```bash
go test ./...
```

Integration tests (build the binary, spawn it, drive it via the MCP SDK over stdio):

```bash
go test -tags=integration ./...
```

The integration suite is the strongest local check we have short of a real kuso install — it catches tool-registration regressions, JSON shape bugs, transport wiring issues, env var handling, and the read-only flag plumbing.
