// MCP `plan` tool — diff a desired-state kuso.yml against the live
// project and return the set of changes without applying.
//
// Agents use this as the "terraform plan" of kuso: paste a kuso.yml,
// see what would happen, decide whether to commit. The same /apply
// endpoint serves both plan and apply paths — we just hit it with
// ?dryRun=1 so the server returns the diff and skips the writes.
//
// Plan is read-only on the kube side, so this tool is allowed even
// when kuso-mcp runs in --read-only mode (configured via
// kusoclient.PostRaw's readOnlyOk=true).

package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type planArgs struct {
	Project string `json:"project" jsonschema:"project name; must match the project: field in the YAML"`
	YAML    string `json:"yaml" jsonschema:"the full kuso.yml as a string"`
}

// planResult mirrors spec.Plan from the server side. Field names match
// the JSON the apply endpoint returns under dryRun=1.
type planResult struct {
	ServicesToCreate []string `json:"servicesToCreate"`
	ServicesToUpdate []string `json:"servicesToUpdate"`
	ServicesToDelete []string `json:"servicesToDelete"`
	AddonsToCreate   []string `json:"addonsToCreate"`
	AddonsToUpdate   []string `json:"addonsToUpdate"`
	AddonsToDelete   []string `json:"addonsToDelete"`
}

func registerPlan(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "plan",
		Description: "Diff a desired-state kuso.yml against a live project. Returns the set of services/addons that would be created, updated, or deleted by an apply. Read-only — does NOT mutate the cluster. Use this before calling apply to see the blast radius.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args planArgs) (*mcp.CallToolResult, planResult, error) {
		if args.Project == "" {
			return nil, planResult{}, errors.New("project is required")
		}
		if strings.TrimSpace(args.YAML) == "" {
			return nil, planResult{}, errors.New("yaml is required")
		}
		var out planResult
		path := "/api/projects/" + args.Project + "/apply?dryRun=1"
		if err := client.PostRaw(ctx, path, "application/x-yaml", []byte(args.YAML), true, &out); err != nil {
			return nil, planResult{}, fmt.Errorf("plan: %w", err)
		}
		// Human-readable summary for the text Content. Mirrors the
		// shape `terraform plan` uses: `+` for create, `~` for
		// update, `-` for delete.
		var b strings.Builder
		writeSection(&b, "Services", "+", out.ServicesToCreate)
		writeSection(&b, "Services", "~", out.ServicesToUpdate)
		writeSection(&b, "Services", "-", out.ServicesToDelete)
		writeSection(&b, "Addons", "+", out.AddonsToCreate)
		writeSection(&b, "Addons", "~", out.AddonsToUpdate)
		writeSection(&b, "Addons", "-", out.AddonsToDelete)
		if b.Len() == 0 {
			b.WriteString("Plan: no changes — live state matches the YAML.")
		} else {
			fmt.Fprintf(&b, "Plan: %d service create, %d service update, %d service delete, %d addon create, %d addon update, %d addon delete.",
				len(out.ServicesToCreate), len(out.ServicesToUpdate), len(out.ServicesToDelete),
				len(out.AddonsToCreate), len(out.AddonsToUpdate), len(out.AddonsToDelete))
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, out, nil
	})
}

func writeSection(b *strings.Builder, kind, glyph string, names []string) {
	if len(names) == 0 {
		return
	}
	for _, n := range names {
		fmt.Fprintf(b, "  %s %s %s\n", glyph, kind, n)
	}
}
