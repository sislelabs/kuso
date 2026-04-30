---
name: mcp-development
description: Use when adding tools to kuso-mcp, debugging MCP integration with Claude Code, or designing new tool shapes. Tells you where things live and the conventions for tool design.
---

# kuso-mcp development

`kuso-mcp` is the Model Context Protocol server that lets agents drive a kuso PaaS. It speaks MCP over stdio and calls the kuso server REST API under the hood.

## Layout

```
mcp/
├── main.go                  # entrypoint
├── go.mod                   # module: github.com/sislelabs/kuso/mcp
├── Makefile                 # build, test, vet, run, clean
├── internal/
│   ├── config/              # KUSO_URL / KUSO_TOKEN / --read-only
│   │   ├── config.go
│   │   └── config_test.go
│   ├── kusoclient/          # HTTP wrapper for kuso server REST API
│   │   └── client.go
│   └── tools/               # one file per topic: tools.go, apps.go, secrets.go, ...
│       └── tools.go
└── README.md
```

The SDK is [`github.com/modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) v1.6+. Requires Go 1.25 (auto-bumped via `toolchain` directive).

## Tool design rules

These come from `docs/PRD.md` Workstream B and Anthropic's MCP guidance. Don't ship a tool that violates them.

1. **Intent-grouped, not REST-mirrored.** Wrong: `get_app`, `get_app_logs`, `get_app_status`. Right: `describe_app` returns all of that in one call. Wrong: `delete_env`, `set_env`, `update_env`. Right: `set_app_config(name, patch)` with idempotent partial updates.

2. **Composite tools beat chains.** `troubleshoot_app(name)` should return status + recent logs + recent events + addon health in one structured response. Agents make worse decisions when they have to chain three tools to debug something.

3. **Every tool returns structured data + a human-readable summary.** The SDK's `CallToolResult` has `Content` (the human summary) and a typed return value (the structured data). Use both.

4. **Destructive operations require an explicit `confirm: true` arg.** This is enforced in the tool itself, not the SDK. Examples: `exec_app`, `delete_app`, `manage_secret(action="delete")`.

5. **Honor `--read-only`.** Mutating tools should check `client.ReadOnly()` and return an error if set. Read tools can ignore the flag.

6. **Tool descriptions explicitly tell the agent which composite to prefer.** Example for `tail_logs`: "Use this for live log streaming; for one-shot debugging, prefer `troubleshoot_app` which bundles logs with status and events."

## Adding a new tool

1. Create or extend a file in `internal/tools/` named after the topic (e.g. `apps.go`, `secrets.go`, `cluster.go`).
2. Define an args struct with `json:""` and `jsonschema:""` tags. The jsonschema tag is the description the agent sees.
3. Define a result struct (also exported, also tagged) so callers get typed JSON.
4. Write a `register<Name>(server, client)` function that calls `mcp.AddTool`. The handler returns `(*mcp.CallToolResult, <YourResult>, error)`.
5. Call your `register<Name>` from `Register()` in `tools.go`.
6. Write a test in the same package (table-driven, hits a stub HTTP server).

Example from `tools.go`:

```go
type healthArgs struct{}
type healthResult struct {
    ServerURL string `json:"server_url"`
    ReadOnly  bool   `json:"read_only"`
    Status    string `json:"status"`
}

func registerHealth(server *mcp.Server, client *kusoclient.Client) {
    mcp.AddTool(server, &mcp.Tool{
        Name:        "health",
        Description: "...",
    }, func(ctx context.Context, _ *mcp.CallToolRequest, _ healthArgs) (*mcp.CallToolResult, healthResult, error) {
        ...
    })
}
```

## Local testing

Bring up a local MCP client, e.g. with the SDK's `CommandTransport`, or wire `kuso-mcp` into Claude Code (see README). The fastest smoke test is the `health` tool — it doesn't need a running kuso server, just env vars set.

```bash
KUSO_URL=https://example.com KUSO_TOKEN=fake make run
# then issue: tools/list, then tools/call name=health
```

## Why a separate module?

`mcp/` is a separate Go module from `cli/` for two reasons:
- Different dependency surface — the MCP SDK is heavy and `cli/` doesn't need it.
- Different release cadence — MCP tool changes ship fast; CLI changes are user-facing and want more care.

The two modules can still share types via a future shared `internal/` package promoted to the repo root if duplication becomes painful. For now, prefer copying small types over premature sharing.
