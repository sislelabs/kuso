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
// the JSON the apply endpoint returns under dryRun=1. Shared with the
// apply tool (spec.ApplyResult embeds the same Plan).
type planResult struct {
	ServicesToCreate []string `json:"servicesToCreate"`
	ServicesToUpdate []string `json:"servicesToUpdate"`
	ServicesToDelete []string `json:"servicesToDelete"`
	AddonsToCreate   []string `json:"addonsToCreate"`
	AddonsToUpdate   []string `json:"addonsToUpdate"`
	AddonsToDelete   []string `json:"addonsToDelete"`
	CronsToCreate    []string `json:"cronsToCreate"`
	CronsToUpdate    []string `json:"cronsToUpdate"`
	CronsToDelete    []string `json:"cronsToDelete"`
	// WouldDelete lists live resources absent from the YAML when
	// prune is false — reported by the server, not executed. Entries
	// are "kind:name", e.g. "service:old".
	WouldDelete []string `json:"wouldDelete,omitempty"`
	// Changes is the field-level diff behind every *ToUpdate entry, plus
	// addon drift apply won't act on (NotApplied).
	Changes []resourceChange `json:"changes,omitempty"`
	// Warnings are things apply can't fix (e.g. a {secret: true} key the
	// service's Secret doesn't hold).
	Warnings []string `json:"warnings,omitempty"`
	// ExecChanges are planned changes that pick code run with the
	// service's secrets; applying them takes the project admin role.
	ExecChanges []execChange `json:"execChanges,omitempty"`
}

type resourceChange struct {
	Resource   string        `json:"resource"`
	Fields     []fieldChange `json:"fields"`
	NotApplied bool          `json:"notApplied,omitempty"`
}

// fieldChange mirrors spec.FieldChange. Destructive marks volume/domain
// removal, a cleared release hook, or scale to zero.
type fieldChange struct {
	Field       string `json:"field"`
	From        string `json:"from"`
	To          string `json:"to"`
	Destructive bool   `json:"destructive,omitempty"`
}

type execChange struct {
	Resource string   `json:"resource"`
	Fields   []string `json:"fields"`
}

// destructiveChanges lists the destructive field changes apply would
// make, as "resource.field".
func (p planResult) destructiveChanges() []string {
	var out []string
	for _, c := range p.Changes {
		if c.NotApplied {
			continue
		}
		for _, f := range c.Fields {
			if f.Destructive {
				out = append(out, c.Resource+"."+f.Field)
			}
		}
	}
	return out
}

// changeCount is the number of create/update/delete steps in the plan
// (WouldDelete excluded — those are reported, not executed).
func (p planResult) changeCount() int {
	return len(p.ServicesToCreate) + len(p.ServicesToUpdate) + len(p.ServicesToDelete) +
		len(p.AddonsToCreate) + len(p.AddonsToUpdate) + len(p.AddonsToDelete) +
		len(p.CronsToCreate) + len(p.CronsToUpdate) + len(p.CronsToDelete)
}

// writePlanSections renders the +/~/- change lines plus the
// would-delete report. Shared by the plan and apply summaries.
func writePlanSections(b *strings.Builder, p planResult) {
	writeSection(b, "Services", "+", p.ServicesToCreate)
	writeSection(b, "Services", "~", p.ServicesToUpdate)
	writeSection(b, "Services", "-", p.ServicesToDelete)
	writeSection(b, "Addons", "+", p.AddonsToCreate)
	writeSection(b, "Addons", "~", p.AddonsToUpdate)
	writeSection(b, "Addons", "-", p.AddonsToDelete)
	writeSection(b, "Crons", "+", p.CronsToCreate)
	writeSection(b, "Crons", "~", p.CronsToUpdate)
	writeSection(b, "Crons", "-", p.CronsToDelete)
	for _, c := range p.Changes {
		fmt.Fprintf(b, "  %s\n", c.Resource)
		if c.NotApplied {
			b.WriteString("    (drift — apply doesn't modify this)\n")
		}
		for _, f := range c.Fields {
			mark := ""
			if f.Destructive && !c.NotApplied {
				mark = "  [DESTRUCTIVE]"
			}
			fmt.Fprintf(b, "    %s: %s → %s%s\n", f.Field, f.From, f.To, mark)
		}
	}
	for _, wd := range p.WouldDelete {
		fmt.Fprintf(b, "  ! would delete %s (prune: false — skipped)\n", wd)
	}
	for _, w := range p.Warnings {
		fmt.Fprintf(b, "  ! warning: %s\n", w)
	}
	for _, e := range p.ExecChanges {
		fmt.Fprintf(b, "  ! needs the project admin role (runs code with the service's secrets): %s %s\n", e.Resource, strings.Join(e.Fields, ", "))
	}
}

func registerPlan(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "plan",
		Description: "Diff a desired-state kuso.yml against a live project. Returns the set of services/addons/crons that would be created, updated, or deleted by an apply, plus wouldDelete: live resources absent from the YAML that a prune: true apply would remove. changes carries the field-level diff; fields marked destructive (volume or domain removal, release hook cleared, scale to 0) lose data or traffic, and apply needs confirm=true for them. Read-only — does NOT mutate the cluster. Use this before calling apply to see the blast radius.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args planArgs) (*mcp.CallToolResult, planResult, error) {
		if args.Project == "" {
			return nil, planResult{}, errors.New("project is required")
		}
		if strings.TrimSpace(args.YAML) == "" {
			return nil, planResult{}, errors.New("yaml is required")
		}
		var out planResult
		path := apiPath("api", "projects", args.Project, "apply") + "?dryRun=1"
		if err := client.PostRaw(ctx, path, "application/x-yaml", []byte(args.YAML), true, &out); err != nil {
			return nil, planResult{}, fmt.Errorf("plan: %w", err)
		}
		// Human-readable summary for the text Content. Mirrors the
		// shape `terraform plan` uses: `+` for create, `~` for
		// update, `-` for delete, `!` for skipped would-deletes.
		var b strings.Builder
		writePlanSections(&b, out)
		if b.Len() == 0 {
			b.WriteString("Plan: no changes — live state matches the YAML.")
		} else {
			fmt.Fprintf(&b, "Plan: %d change(s) — services +%d ~%d -%d, addons +%d ~%d -%d, crons +%d ~%d -%d.",
				out.changeCount(),
				len(out.ServicesToCreate), len(out.ServicesToUpdate), len(out.ServicesToDelete),
				len(out.AddonsToCreate), len(out.AddonsToUpdate), len(out.AddonsToDelete),
				len(out.CronsToCreate), len(out.CronsToUpdate), len(out.CronsToDelete))
			if len(out.WouldDelete) > 0 {
				fmt.Fprintf(&b, " %d resource(s) would be deleted under prune: true.", len(out.WouldDelete))
			}
			if d := out.destructiveChanges(); len(d) > 0 {
				fmt.Fprintf(&b, " %d DESTRUCTIVE change(s) (data or traffic loss) — apply needs confirm=true.", len(d))
			}
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
