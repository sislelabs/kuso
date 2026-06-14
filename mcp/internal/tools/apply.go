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

// applyResult mirrors spec.ApplyResult: the executed plan + per-step errors.
type applyResult struct {
	Plan struct {
		ServicesToCreate []string `json:"servicesToCreate"`
		ServicesToUpdate []string `json:"servicesToUpdate"`
		ServicesToDelete []string `json:"servicesToDelete"`
		AddonsToCreate   []string `json:"addonsToCreate"`
		AddonsToUpdate   []string `json:"addonsToUpdate"`
		AddonsToDelete   []string `json:"addonsToDelete"`
	} `json:"plan"`
	Errors []struct {
		Resource string `json:"resource"`
		Op       string `json:"op"`
		Message  string `json:"message"`
	} `json:"errors,omitempty"`
}

func registerApply(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "apply",
		Description: "Apply a desired-state kuso.yml to a live project — reconciles services + addons (create/update/delete, prune-gated). Mutating; refused in --read-only mode. Call plan first to preview the blast radius. Returns the executed plan plus any per-step errors (apply does not abort on one bad resource — it surfaces every failure).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args applyArgs) (*mcp.CallToolResult, applyResult, error) {
		if args.Project == "" {
			return nil, applyResult{}, errors.New("project is required")
		}
		if strings.TrimSpace(args.YAML) == "" {
			return nil, applyResult{}, errors.New("yaml is required")
		}
		var out applyResult
		path := "/api/projects/" + args.Project + "/apply"
		if err := client.PostRaw(ctx, path, "application/x-yaml", []byte(args.YAML), false, &out); err != nil {
			return nil, applyResult{}, fmt.Errorf("apply: %w", err)
		}
		var b strings.Builder
		writeSection(&b, "Services", "+", out.Plan.ServicesToCreate)
		writeSection(&b, "Services", "~", out.Plan.ServicesToUpdate)
		writeSection(&b, "Services", "-", out.Plan.ServicesToDelete)
		writeSection(&b, "Addons", "+", out.Plan.AddonsToCreate)
		writeSection(&b, "Addons", "~", out.Plan.AddonsToUpdate)
		writeSection(&b, "Addons", "-", out.Plan.AddonsToDelete)
		if b.Len() == 0 {
			b.WriteString("Applied: no changes — live state already matched the YAML.\n")
		} else {
			b.WriteString("Applied.\n")
		}
		if len(out.Errors) > 0 {
			fmt.Fprintf(&b, "\n%d step error(s):\n", len(out.Errors))
			for _, e := range out.Errors {
				fmt.Fprintf(&b, "  ✖ %s (%s): %s\n", e.Resource, e.Op, e.Message)
			}
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, out, nil
	})
}
