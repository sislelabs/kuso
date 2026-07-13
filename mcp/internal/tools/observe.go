// MCP `logs` + `status` tools — the read-only observability pair.
//
//   logs    tail a service's recent log lines (synchronous, not streaming)
//   status  a project's runtime rollup: per-env phase / replicas / url
//
// Both are read-only (GET), so allowed even in --read-only mode. They
// close the agent deploy loop: apply → build → status (did it roll?) →
// logs (why not?).

package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type logsArgs struct {
	Project string `json:"project" jsonschema:"project name"`
	Service string `json:"service" jsonschema:"service short name (no project prefix)"`
	Env     string `json:"env,omitempty" jsonschema:"environment to tail; empty = production"`
	Lines   int    `json:"lines,omitempty" jsonschema:"number of lines to tail; default 200, server caps at 2000"`
}

type logsResult struct {
	Project string   `json:"project"`
	Service string   `json:"service"`
	Env     string   `json:"env"`
	Lines   []string `json:"lines"`
}

func registerLogs(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "logs",
		Description: "Tail a service's recent log lines (synchronous snapshot, not a stream). Read-only. Defaults to the production env and 200 lines (server caps at 2000). Use after a failed build_status/status to see why a deploy didn't come up.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args logsArgs) (*mcp.CallToolResult, logsResult, error) {
		if args.Project == "" || args.Service == "" {
			return nil, logsResult{}, errors.New("project and service are required")
		}
		path := apiPath("api", "projects", args.Project, "services", args.Service, "logs")
		q := url.Values{}
		if args.Env != "" {
			q.Set("env", args.Env)
		}
		if args.Lines > 0 {
			q.Set("lines", strconv.Itoa(args.Lines))
		}
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		var out logsResult
		if err := client.GetJSON(ctx, path, &out); err != nil {
			return nil, logsResult{}, fmt.Errorf("tail logs: %w", err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "logs %s (env=%s, %d lines):\n", args.Service, out.Env, len(out.Lines))
		for _, l := range out.Lines {
			b.WriteString(l)
			b.WriteString("\n")
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, out, nil
	})
}

type statusArgs struct {
	Project string `json:"project" jsonschema:"project name"`
}

// statusEnv is the projected per-environment runtime view.
type statusEnv struct {
	Service  string `json:"service"`
	Kind     string `json:"kind"`
	Phase    string `json:"phase"`
	Replicas string `json:"replicas"`
	URL      string `json:"url,omitempty"`
}

type statusResult struct {
	Project      string      `json:"project"`
	BaseDomain   string      `json:"baseDomain,omitempty"`
	Environments []statusEnv `json:"environments"`
}

func registerStatus(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "status",
		Description: "Get a project's runtime rollup: each environment's phase, ready/desired replicas, and live URL. Read-only. This is the runtime view (is it up?) — use describe_project for the config view (what's declared).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args statusArgs) (*mcp.CallToolResult, statusResult, error) {
		if args.Project == "" {
			return nil, statusResult{}, errors.New("project is required")
		}
		// The /api/projects/{p} rollup; environments[].status is an
		// operator-written map, so decode it loosely and project the
		// phase/replicas/url fields (mirrors `kuso status`).
		var rollup struct {
			Project struct {
				Metadata struct {
					Name string `json:"name"`
				} `json:"metadata"`
				Spec struct {
					BaseDomain string `json:"baseDomain"`
				} `json:"spec"`
			} `json:"project"`
			Environments []struct {
				Spec struct {
					Service string `json:"service"`
					Kind    string `json:"kind"`
				} `json:"spec"`
				Status map[string]any `json:"status"`
			} `json:"environments"`
		}
		if err := client.GetJSON(ctx, apiPath("api", "projects", args.Project), &rollup); err != nil {
			return nil, statusResult{}, fmt.Errorf("get status: %w", err)
		}
		out := statusResult{
			Project:    rollup.Project.Metadata.Name,
			BaseDomain: rollup.Project.Spec.BaseDomain,
		}
		var b strings.Builder
		fmt.Fprintf(&b, "project %s\n", out.Project)
		for _, e := range rollup.Environments {
			phase, _ := e.Status["phase"].(string)
			if phase == "" {
				phase = "unknown"
			}
			url, _ := e.Status["url"].(string)
			replicas := "-"
			if r, ok := e.Status["replicas"].(map[string]any); ok {
				ready, _ := r["ready"].(float64)
				desired, _ := r["desired"].(float64)
				replicas = fmt.Sprintf("%d/%d", int(ready), int(desired))
			}
			se := statusEnv{Service: shortServiceName(e.Spec.Service, out.Project), Kind: e.Spec.Kind, Phase: phase, Replicas: replicas, URL: url}
			out.Environments = append(out.Environments, se)
			fmt.Fprintf(&b, "  %s/%s  %s  replicas=%s", se.Service, se.Kind, se.Phase, se.Replicas)
			if se.URL != "" {
				fmt.Fprintf(&b, "  %s", se.URL)
			}
			b.WriteString("\n")
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, out, nil
	})
}

// shortServiceName strips the "<project>-" prefix a service CR name
// carries, matching the CLI's short() helper.
func shortServiceName(full, project string) string {
	return strings.TrimPrefix(full, project+"-")
}
