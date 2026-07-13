// MCP `apply` tool — apply a desired-state kuso.yml to a live project.
//
// The committing companion of `plan`: same /apply endpoint, without
// ?dryRun=1, so the server reconciles the YAML against the live project
// (create/update/delete services + addons). Mutating, so refused in
// --read-only mode (PostRaw with readOnlyOk=false).
//
// Agent flow: plan (see the blast radius) → apply (commit) → build →
// status. Pair with bootstrap_project / add_service for greenfield, or
// just author a full kuso.yml and apply it.

package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type applyArgs struct {
	Project string `json:"project" jsonschema:"project name; must match the project: field in the YAML"`
	YAML    string `json:"yaml" jsonschema:"the full kuso.yml as a string"`
}

// applyResult mirrors spec.ApplyResult: the executed plan + per-step
// errors. The plan reuses the planResult projection (services + addons
// + crons + wouldDelete) so plan and apply report the same shape.
type applyResult struct {
	Plan   planResult  `json:"plan"`
	Errors []stepError `json:"errors,omitempty"`
}

// stepError mirrors spec.StepError.
type stepError struct {
	Resource string `json:"resource"` // "service:api" / "addon:db" / "cron:nightly"
	Op       string `json:"op"`       // "create" / "update" / "delete" / …
	Message  string `json:"message"`
}

func registerApply(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "apply",
		Description: "Apply a desired-state kuso.yml to a live project — reconciles services + addons + crons (create/update/delete, prune-gated; wouldDelete reports skipped prune candidates). Mutating; refused in --read-only mode. Call plan first to preview the blast radius. Returns the executed plan plus any per-step errors (apply does not abort on one bad resource — it surfaces every failure). Any per-step error marks the tool result as an error: treat that as a partially-failed apply, not success.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args applyArgs) (*mcp.CallToolResult, applyResult, error) {
		if args.Project == "" {
			return nil, applyResult{}, errors.New("project is required")
		}
		if strings.TrimSpace(args.YAML) == "" {
			return nil, applyResult{}, errors.New("yaml is required")
		}
		var out applyResult
		path := apiPath("api", "projects", args.Project, "apply")
		if err := client.PostRaw(ctx, path, "application/x-yaml", []byte(args.YAML), false, &out); err != nil {
			return nil, applyResult{}, fmt.Errorf("apply: %w", err)
		}
		var b strings.Builder
		writePlanSections(&b, out.Plan)
		// The server intentionally returns per-step errors with HTTP
		// 200 (it doesn't abort on one bad resource). For the calling
		// agent a partial failure IS a failure: mark the tool result
		// IsError so it can't be mistaken for a clean apply. The
		// structured payload still carries the executed plan + errors.
		if len(out.Errors) > 0 {
			fmt.Fprintf(&b, "Apply PARTIALLY FAILED — %d step error(s); the remaining steps were applied:\n", len(out.Errors))
			for _, e := range out.Errors {
				fmt.Fprintf(&b, "  ✖ %s (%s): %s\n", e.Resource, e.Op, e.Message)
			}
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
			}, out, nil
		}
		if b.Len() == 0 {
			b.WriteString("Applied: no changes — live state already matched the YAML.\n")
		} else {
			b.WriteString("Applied.\n")
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, out, nil
	})
}
