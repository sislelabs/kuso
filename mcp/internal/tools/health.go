package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type healthArgs struct{}

type healthResult struct {
	ServerURL string `json:"server_url"`
	ReadOnly  bool   `json:"read_only"`
	Status    string `json:"status"`
}

func registerHealth(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "health",
		Description: "Reports kuso-mcp configuration: configured server URL and read-only flag. Useful as a smoke test for the MCP connection — does not make any HTTP calls to the kuso server.",
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ healthArgs) (*mcp.CallToolResult, healthResult, error) {
		out := healthResult{
			ServerURL: client.URL(),
			ReadOnly:  client.ReadOnly(),
			Status:    "ok",
		}
		summary := "kuso-mcp reachable; configured server: " + out.ServerURL
		if out.ReadOnly {
			summary += " (read-only)"
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: summary}},
		}, out, nil
	})
}
