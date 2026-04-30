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

| Tool         | Status      |
| ------------ | ----------- |
| `health`     | implemented |
| `list_apps`  | planned     |
| `describe_app` | planned   |
| `deploy_app` | planned     |
| `troubleshoot_app` | planned |
| `set_app_config` | planned |
| `manage_secret` | planned  |
| `tail_logs`  | planned     |
| `exec_app`   | planned     |
| `cluster_health` | planned |
| `cost_report` | planned    |
| `bootstrap_product` | planned |

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
