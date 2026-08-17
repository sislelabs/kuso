// MCP `list_builds` + `rollback` tools.
//
//   list_builds  recent builds for a service, newest first (read-only)
//   rollback     re-point an environment at a previous build's image
//                (mutating)
//
// Why these exist: an agent could deploy through MCP but could not undo
// a deploy through MCP — rollback existed only in the CLI. That forced a
// surface switch at exactly the wrong moment, during an incident the
// agent itself caused. list_builds is its companion: rollback needs a
// build id, and without a listing tool the agent had no way to discover
// one.
//
// Rollback is promotion-only. It re-points the environment at an image
// an earlier build already pushed; it does not rebuild, and it cannot
// resurrect a build whose image aged out of the retention window (the
// server returns 400 in that case, which surfaces here as a tool error).

package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type listBuildsArgs struct {
	Project string `json:"project" jsonschema:"project name"`
	Service string `json:"service" jsonschema:"service short name (no project prefix)"`
	Limit   int    `json:"limit,omitempty" jsonschema:"max builds to return (default 10, max 50)"`
}

type rollbackArgs struct {
	Project string `json:"project" jsonschema:"project name"`
	Service string `json:"service" jsonschema:"service short name (no project prefix)"`
	Build   string `json:"build" jsonschema:"the build id to roll back TO — get one from list_builds; it must have status=succeeded and still have its image"`
	Env     string `json:"env,omitempty" jsonschema:"environment to roll back; defaults to production. Use this to target staging/qa/preview-pr-N instead"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true — rollback re-points a LIVE environment at an older image, which is a production deploy in its own right"`
}

func registerRollback(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_builds",
		Description: "List recent builds for a service, newest first. Read-only. " +
			"Use this to find a rollback target: pick a build with status=succeeded that " +
			"predates the bad deploy, then pass its id to rollback.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listBuildsArgs) (*mcp.CallToolResult, []buildSummary, error) {
		if args.Project == "" || args.Service == "" {
			return nil, nil, errors.New("project and service are required")
		}
		limit := args.Limit
		if limit <= 0 {
			limit = 10
		}
		if limit > 50 {
			limit = 50
		}
		var out []buildSummary
		path := apiPath("api", "projects", args.Project, "services", args.Service, "builds")
		if err := client.GetJSON(ctx, path, &out); err != nil {
			return nil, nil, fmt.Errorf("list builds: %w", err)
		}
		if len(out) > limit {
			out = out[:limit]
		}
		var b strings.Builder
		if len(out) == 0 {
			b.WriteString("No builds found for this service.")
		} else {
			fmt.Fprintf(&b, "%d build(s), newest first:\n", len(out))
			for _, s := range out {
				fmt.Fprintf(&b, "  %s  %-10s %s", s.ID, s.Status, s.Branch)
				if s.CommitSha != "" {
					fmt.Fprintf(&b, " @%s", s.CommitSha)
				}
				if s.FinishedAt != "" {
					fmt.Fprintf(&b, "  (%s)", s.FinishedAt)
				}
				b.WriteString("\n")
			}
			b.WriteString("Only a build with status=succeeded can be a rollback target.")
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name: "rollback",
		Description: "Roll an environment back to a previous build's image. REQUIRES confirm=true — " +
			"this re-points a LIVE environment at an older image and is a production deploy in its own " +
			"right. Mutating; refused in --read-only mode. Defaults to the production environment; pass " +
			"env to target staging/qa/preview-pr-N. Promotion-only: it does NOT rebuild, and it fails if " +
			"the target build never succeeded or its image has aged out of the retention window. Call " +
			"list_builds first to choose a target.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args rollbackArgs) (*mcp.CallToolResult, buildSummary, error) {
		if args.Project == "" || args.Service == "" || args.Build == "" {
			return nil, buildSummary{}, errors.New("project, service and build are required")
		}
		if !args.Confirm {
			return nil, buildSummary{}, errors.New(
				"confirm=true is required — rollback re-points a live environment at an older image " +
					"(a production deploy). Run list_builds first to confirm the target build succeeded")
		}
		if client.ReadOnly() {
			return nil, buildSummary{}, errors.New("kuso-mcp is running in --read-only mode; rollback is refused")
		}
		path := apiPath("api", "projects", args.Project, "services", args.Service, "builds", args.Build, "rollback")
		if env := strings.TrimSpace(args.Env); env != "" {
			path += "?env=" + url.QueryEscape(env)
		}
		var out buildSummary
		if err := client.PostJSON(ctx, path, struct{}{}, &out); err != nil {
			return nil, buildSummary{}, fmt.Errorf("rollback: %w", err)
		}
		target := args.Env
		if target == "" {
			target = "production"
		}
		text := fmt.Sprintf("Rolled %s/%s (%s) back to build %s", args.Project, args.Service, target, args.Build)
		if out.ImageTag != "" {
			text += fmt.Sprintf(" — now serving image tag %s", out.ImageTag)
		}
		text += ".\nThe helm-operator rolls the new pod; poll status to watch it become ready."
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, out, nil
	})
}
