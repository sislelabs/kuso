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
	"gopkg.in/yaml.v3"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type applyArgs struct {
	Project string `json:"project" jsonschema:"project name; must match the project: field in the YAML"`
	YAML    string `json:"yaml" jsonschema:"the full kuso.yml as a string"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true when the YAML sets prune: true — guards the destructive delete of live services/addons/crons absent from the YAML"`
}

// yamlSetsPruneTrue reports whether the kuso.yml sets the top-level
// `prune: true`, decoding it the way the server does (yaml.v3, first
// document, into spec.File's `prune` bool) so flow mappings, JSON,
// quoted keys, tags and anchors can't slip past a text heuristic. The
// server has no prune confirmation of its own, so anything that doesn't
// decode cleanly fails closed and needs confirm too.
func yamlSetsPruneTrue(y string) bool {
	var f struct {
		Prune bool `yaml:"prune"`
	}
	if err := yaml.Unmarshal([]byte(y), &f); err != nil {
		return true
	}
	return f.Prune
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
		Description: "Apply a desired-state kuso.yml to a live project — reconciles services + addons + crons (create/update/delete, prune-gated; wouldDelete reports skipped prune candidates). Mutating; refused in --read-only mode. Call plan first to preview the blast radius. When the YAML sets prune: true (which DELETES live services/addons/crons absent from the YAML) you MUST also pass confirm=true. Returns the executed plan plus any per-step errors (apply does not abort on one bad resource — it surfaces every failure). Any per-step error marks the tool result as an error: treat that as a partially-failed apply, not success.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args applyArgs) (*mcp.CallToolResult, applyResult, error) {
		if args.Project == "" {
			return nil, applyResult{}, errors.New("project is required")
		}
		if strings.TrimSpace(args.YAML) == "" {
			return nil, applyResult{}, errors.New("yaml is required")
		}
		// prune: true deletes every live service/addon/cron missing from
		// the YAML. Gate that behind confirm=true, matching the
		// confirm-convention of the other mutating tools (bootstrap_project,
		// update_project, manage_addon delete). Run plan first to see what
		// would be removed.
		if yamlSetsPruneTrue(args.YAML) && !args.Confirm {
			return nil, applyResult{}, errors.New("this YAML sets prune: true (or couldn't be parsed to rule it out); prune DELETES live resources absent from the YAML — retry with confirm=true (run plan first to see the blast radius)")
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
