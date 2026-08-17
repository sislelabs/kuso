# kuso-mcp

A [Model Context Protocol](https://modelcontextprotocol.io) server for kuso. It lets MCP-speaking clients (Claude Code, Cursor, Claude Desktop) drive a kuso PaaS instance — list and describe apps, deploy, troubleshoot, manage secrets, etc.

**Status:** v0.1.0 — pre-release, but functional: 20 tools registered covering project bootstrap, services, addons, builds, env/secrets, logs, status, one-shot runs, rollback, addon SQL, and config-as-code plan/apply.

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

**Read-only mode.** `--read-only` refuses every mutating tool (apply,
build, rollback, set_env, set_secret, run, manage_addon, …) while
leaving the whole read surface — including `sql_query`, which the server
runs in a read-only transaction — available. Pass it via `args`; it is
NOT settable through `env`, so the copy-paste block above starts in
read-write mode:

```json
{
  "mcpServers": {
    "kuso": {
      "command": "/path/to/kuso-mcp",
      "args": ["--read-only"],
      "env": {
        "KUSO_URL": "https://kuso.example.com",
        "KUSO_TOKEN": "..."
      }
    }
  }
}
```

Worth defaulting to when the agent's job is to investigate rather than
to ship. Note the kuso token's own permissions still apply on top —
read-only mode narrows what the *tools* will do, not what the token can.

## Tool surface

All tools are project-shaped (intent-grouped, not REST-mirrored). Registered today:

| Tool                | What it does |
| ------------------- | ------------ |
| `health`            | smoke test: server reachability + read-only flag |
| `list_projects`     | all projects |
| `describe_project`  | project + services + envs + addons in one call |
| `status`            | runtime rollup for a project/service |
| `logs`              | fetch service logs |
| `bootstrap_project` | create a project (mutating; `confirm: true`) |
| `update_project`    | patch project fields (mutating) |
| `add_service`       | add a service (mutating; `confirm: true`) |
| `manage_addon`      | add / delete addons (mutating; `confirm: true`) |
| `set_env`           | set plain env vars (mutating) |
| `set_secret`        | set secret-backed vars (mutating) |
| `build`             | trigger a build (mutating) |
| `build_status`      | build state for a service |
| `run`               | one-shot Job in a service's context (mutating) |
| `plan`              | diff a kuso.yaml against live state (read-only) |
| `apply`             | reconcile kuso.yaml (mutating; `confirm: true`) |
| `list_builds`       | recent builds for a service, newest first (read-only) |
| `rollback`          | re-point an env at a previous build's image (mutating; `confirm: true`) |
| `sql_tables`        | list an addon database's tables (read-only) |
| `sql_query`         | one read-only SELECT against an addon (read-only) |

`--read-only` disables every mutating tool.

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
