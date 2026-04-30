package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

// ---------- restart_app ----------

type restartAppArgs struct {
	Pipeline string `json:"pipeline" jsonschema:"pipeline name"`
	Phase    string `json:"phase" jsonschema:"phase name (e.g. production, staging, review)"`
	App      string `json:"app" jsonschema:"app name"`
	Confirm  bool   `json:"confirm" jsonschema:"must be true to actually restart — prevents accidental restarts of running production apps"`
}

type restartAppResult struct {
	Pipeline string `json:"pipeline"`
	Phase    string `json:"phase"`
	App      string `json:"app"`
	Status   string `json:"status"`
}

func runRestartApp(ctx context.Context, client *kusoclient.Client, args restartAppArgs) (restartAppResult, error) {
	if args.Pipeline == "" || args.Phase == "" || args.App == "" {
		return restartAppResult{}, errors.New("pipeline, phase, and app are all required")
	}
	if !args.Confirm {
		return restartAppResult{}, errors.New("confirm=true is required for restart_app — guard against accidental restarts")
	}
	if client.ReadOnly() {
		return restartAppResult{}, errors.New("kuso-mcp is in read-only mode; refusing to restart")
	}

	// The kuso server's restart endpoint is GET (legacy quirk inherited from upstream).
	path := fmt.Sprintf("/api/apps/%s/%s/%s/restart", args.Pipeline, args.Phase, args.App)
	if err := client.GetJSON(ctx, path, nil); err != nil {
		return restartAppResult{}, fmt.Errorf("restart app: %w", err)
	}
	return restartAppResult{
		Pipeline: args.Pipeline, Phase: args.Phase, App: args.App,
		Status: "restart triggered",
	}, nil
}

func registerRestartApp(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "restart_app",
		Description: "Trigger a rolling restart of an app's pods. The pods are terminated and recreated from the current spec — no config change. Idempotent. " +
			"REQUIRES confirm=true. Refused when kuso-mcp is started with --read-only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args restartAppArgs) (*mcp.CallToolResult, restartAppResult, error) {
		out, err := runRestartApp(ctx, client, args)
		if err != nil {
			return nil, restartAppResult{}, err
		}
		summary := fmt.Sprintf("%s/%s/%s: %s", out.Pipeline, out.Phase, out.App, out.Status)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: summary}},
		}, out, nil
	})
}

// ---------- tail_logs ----------

type tailLogsArgs struct {
	Pipeline  string `json:"pipeline" jsonschema:"pipeline name"`
	Phase     string `json:"phase" jsonschema:"phase name"`
	App       string `json:"app" jsonschema:"app name"`
	Container string `json:"container,omitempty" jsonschema:"container name (default: web). Common values: web, kuso-build, kuso-fetch."`
	Lines     int    `json:"lines,omitempty" jsonschema:"how many of the most recent log lines to return (default 200, max 2000)"`
}

type tailLogsResult struct {
	Pipeline  string   `json:"pipeline"`
	Phase     string   `json:"phase"`
	App       string   `json:"app"`
	Container string   `json:"container"`
	Lines     []string `json:"lines"`
}

func runTailLogs(ctx context.Context, client *kusoclient.Client, args tailLogsArgs) (tailLogsResult, error) {
	if args.Pipeline == "" || args.Phase == "" || args.App == "" {
		return tailLogsResult{}, errors.New("pipeline, phase, and app are all required")
	}
	container := args.Container
	if container == "" {
		container = "web"
	}
	lines := args.Lines
	switch {
	case lines <= 0:
		lines = 200
	case lines > 2000:
		lines = 2000
	}

	path := fmt.Sprintf("/api/logs/%s/%s/%s/%s/history", args.Pipeline, args.Phase, args.App, container)
	var all []string
	if err := client.GetJSON(ctx, path, &all); err != nil {
		return tailLogsResult{}, fmt.Errorf("tail logs: %w", err)
	}
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return tailLogsResult{
		Pipeline: args.Pipeline, Phase: args.Phase, App: args.App,
		Container: container,
		Lines:     all,
	}, nil
}

func registerTailLogs(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "tail_logs",
		Description: "Return the most recent N log lines from a specific container of an app. Read-only; safe in --read-only mode. " +
			"For one-shot incident triage prefer troubleshoot_app — it bundles logs with status, pods, and events. Use tail_logs when you specifically want more lines, or a non-default container.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args tailLogsArgs) (*mcp.CallToolResult, tailLogsResult, error) {
		out, err := runTailLogs(ctx, client, args)
		if err != nil {
			return nil, tailLogsResult{}, err
		}
		summary := fmt.Sprintf("%s/%s/%s [%s]: %d log lines",
			out.Pipeline, out.Phase, out.App, out.Container, len(out.Lines))
		if len(out.Lines) > 0 {
			summary += "\n  …" + strings.TrimRight(out.Lines[len(out.Lines)-1], "\n")
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: summary}},
		}, out, nil
	})
}
