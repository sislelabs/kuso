// MCP `build` + `build_status` tools.
//
//   build         trigger a build of a service from a branch/ref (mutating)
//   build_status  the newest build's status for a service (read-only)
//
// build wraps POST /api/projects/{p}/services/{s}/builds; build_status
// wraps GET on the same path (there is no GET-single-build route) and
// surfaces the newest entry. Together with apply + status they let an
// agent ship a change and watch it land without kubectl.

package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type buildArgs struct {
	Project string `json:"project" jsonschema:"project name"`
	Service string `json:"service" jsonschema:"service short name (no project prefix)"`
	Branch  string `json:"branch,omitempty" jsonschema:"branch to build; defaults to the service's default branch"`
	Ref     string `json:"ref,omitempty" jsonschema:"explicit git ref/SHA to build; overrides branch"`
	DryRun  bool   `json:"dryRun,omitempty" jsonschema:"compile + assemble layers but skip registry push and env promotion"`
}

// buildRequest mirrors the server's builds.CreateBuildRequest.
type buildRequest struct {
	Branch string `json:"branch,omitempty"`
	Ref    string `json:"ref,omitempty"`
	DryRun bool   `json:"dryRun,omitempty"`
}

// buildSummary mirrors the handler's wire shape (newest-first in the list).
type buildSummary struct {
	ID            string `json:"id"`
	ServiceName   string `json:"serviceName"`
	Branch        string `json:"branch,omitempty"`
	CommitSha     string `json:"commitSha,omitempty"`
	CommitMessage string `json:"commitMessage,omitempty"`
	ImageTag      string `json:"imageTag,omitempty"`
	Status        string `json:"status"`
	StartedAt     string `json:"startedAt,omitempty"`
	FinishedAt    string `json:"finishedAt,omitempty"`
	ErrorMessage  string `json:"errorMessage,omitempty"`
}

func registerBuild(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "build",
		Description: "Trigger a build of a service from a branch or ref. Mutating; refused in --read-only mode. Returns the created build's id + status. Poll build_status to follow it to succeeded/failed.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args buildArgs) (*mcp.CallToolResult, buildSummary, error) {
		if args.Project == "" || args.Service == "" {
			return nil, buildSummary{}, errors.New("project and service are required")
		}
		body := buildRequest{Branch: args.Branch, Ref: args.Ref, DryRun: args.DryRun}
		var out buildSummary
		path := "/api/projects/" + args.Project + "/services/" + args.Service + "/builds"
		if err := client.PostJSON(ctx, path, body, &out); err != nil {
			return nil, buildSummary{}, fmt.Errorf("trigger build: %w", err)
		}
		text := fmt.Sprintf("build %s triggered (status=%s)", out.ID, out.Status)
		if out.Branch != "" {
			text += "\n  branch: " + out.Branch
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "build_status",
		Description: "Get the newest build's status for a service (status is one of pending/building/succeeded/failed/…). Read-only. Use after build to follow a deploy; the errorMessage field carries the failure cause for failed builds.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args buildArgs) (*mcp.CallToolResult, buildSummary, error) {
		if args.Project == "" || args.Service == "" {
			return nil, buildSummary{}, errors.New("project and service are required")
		}
		var list []buildSummary
		path := "/api/projects/" + args.Project + "/services/" + args.Service + "/builds"
		if err := client.GetJSON(ctx, path, &list); err != nil {
			return nil, buildSummary{}, fmt.Errorf("list builds: %w", err)
		}
		if len(list) == 0 {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "no builds yet for " + args.Service}},
			}, buildSummary{}, nil
		}
		out := list[0] // newest-first
		text := fmt.Sprintf("build %s: status=%s", out.ID, out.Status)
		if out.ImageTag != "" {
			text += "\n  image: " + out.ImageTag
		}
		if out.ErrorMessage != "" {
			text += "\n  error: " + out.ErrorMessage
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, out, nil
	})
}
