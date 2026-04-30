// Package tools registers the MCP tool surface on a kuso-mcp server.
//
// Each tool is intent-grouped per Anthropic's MCP design guidance, not a
// 1:1 wrap of the underlying REST API. The roadmap is in docs/PRD.md
// (Workstream B); v0.1 ships only health() so the server has something to
// answer for tools/list and round-trip integration tests can pass.
package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/config"
	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

// Register attaches every kuso-mcp tool to server.
func Register(server *mcp.Server, cfg *config.Config) {
	client := kusoclient.New(cfg)
	registerHealth(server, client)
}

type healthArgs struct{}

type healthResult struct {
	ServerURL string `json:"server_url"`
	ReadOnly  bool   `json:"read_only"`
	Status    string `json:"status"`
}

func registerHealth(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "health",
		Description: "Reports kuso-mcp configuration and a placeholder server status. Useful as a smoke test for the MCP connection.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ healthArgs) (*mcp.CallToolResult, healthResult, error) {
		out := healthResult{
			ServerURL: client.URL(),
			ReadOnly:  client.ReadOnly(),
			Status:    "ok",
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{
				&mcp.TextContent{Text: "kuso-mcp reachable; configured server: " + out.ServerURL},
			},
		}, out, nil
	})
}
