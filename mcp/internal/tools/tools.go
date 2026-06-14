// Package tools registers the MCP tool surface on a kuso-mcp server.
//
// Current tool surface:
//
//   health             smoke test (no HTTP)
//   list_projects      list every project rolled up
//   describe_project   one project with services / envs / addons
//   bootstrap_project  create a new project (mutating)
//   update_project     edit a project (mutating)
//   add_service        add a service to a project (mutating)
//   manage_addon       add | delete an addon (mutating)
//   plan               dry-run apply: diff a desired-state kuso.yml
//                      against the live project (read-only)
//   apply              apply a desired-state kuso.yml (mutating)
//   build              trigger a build of a service (mutating)
//   build_status       newest build's status for a service (read-only)
//   set_env            replace a service's plain env vars (mutating)
//   set_secret         upsert one secret key on a service (mutating)
//   logs               tail a service's recent logs (read-only)
//   status             a project's runtime rollup (read-only)
//   run                fire a one-shot task pod against a service
//                      (migrations, seeds, scripts) — mutating
//
// With apply + build + status + logs an agent can drive a deploy
// end-to-end (init/author kuso.yml → apply → build → status → logs)
// without ever shelling out to the kuso CLI or kubectl.
//
// All mutating tools are refused in --read-only.

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
	registerPlan(server, client)
	registerApply(server, client)
	registerBuild(server, client)
	registerSetEnv(server, client)
	registerSetSecret(server, client)
	registerLogs(server, client)
	registerStatus(server, client)
	registerRun(server, client)
}
