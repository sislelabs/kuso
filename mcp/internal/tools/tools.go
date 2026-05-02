// Package tools registers the MCP tool surface on a kuso-mcp server.
//
// v0.2 reshape — see docs/REDESIGN.md. Pipelines / apps / phases tools
// have been removed. Current surface:
//
//   health             smoke test (no HTTP)
//   list_projects      list every project rolled up
//   describe_project   one project with services / envs / addons
//   bootstrap_project  create a new project (mutating, confirm-required)
//   add_service        add a service to a project (mutating)
//   manage_addon       add | delete an addon (mutating)
//
// All mutating tools require confirm: true and are refused in --read-only.

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
	registerListProjects(server, client)
	registerDescribeProject(server, client)
	registerBootstrapProject(server, client)
	registerUpdateProject(server, client)
	registerAddService(server, client)
	registerManageAddon(server, client)
}
