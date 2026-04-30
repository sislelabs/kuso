// Package tools registers the MCP tool surface on a kuso-mcp server.
//
// Each tool is intent-grouped per Anthropic's MCP design guidance, not a
// 1:1 wrap of the underlying REST API. The roadmap is in docs/PRD.md
// (Workstream B).
package tools

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/config"
	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

// Register attaches every kuso-mcp tool to server.
func Register(server *mcp.Server, cfg *config.Config) {
	client := kusoclient.New(cfg)

	registerHealth(server, client)
	registerListApps(server, client)
	registerDescribeApp(server, client)
	registerTroubleshootApp(server, client)
}
