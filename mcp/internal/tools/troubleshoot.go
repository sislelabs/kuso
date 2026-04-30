package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
	"github.com/sislelabs/kuso/mcp/internal/types"
)

type troubleshootArgs struct {
	Pipeline string `json:"pipeline" jsonschema:"pipeline name"`
	Phase    string `json:"phase" jsonschema:"phase name (e.g. production, staging, review)"`
	App      string `json:"app" jsonschema:"app name"`
	LogLines int    `json:"log_lines,omitempty" jsonschema:"how many log lines to fetch (default 200, max 1000)"`
}

type troubleshootResult struct {
	Pipeline string         `json:"pipeline"`
	Phase    string         `json:"phase"`
	App      string         `json:"app"`
	Spec     *types.App     `json:"spec,omitempty"`
	Pods     []podSummary   `json:"pods,omitempty"`
	Logs     []string       `json:"logs,omitempty"`
	Events   []eventSummary `json:"events,omitempty"`
	Errors   []string       `json:"errors,omitempty"`
}

type podSummary struct {
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
	Phase  string `json:"phase,omitempty"`
	Image  string `json:"image,omitempty"`
}

type eventSummary struct {
	Type    string `json:"type,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Object  string `json:"object,omitempty"`
	Message string `json:"message,omitempty"`
}

func runTroubleshootApp(ctx context.Context, client *kusoclient.Client, args troubleshootArgs) (troubleshootResult, error) {
	if args.Pipeline == "" || args.Phase == "" || args.App == "" {
		return troubleshootResult{}, errors.New("pipeline, phase, and app are all required")
	}
	lines := args.LogLines
	switch {
	case lines <= 0:
		lines = 200
	case lines > 1000:
		lines = 1000
	}

	out := troubleshootResult{Pipeline: args.Pipeline, Phase: args.Phase, App: args.App}
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	recordErr := func(label string, err error) {
		if err == nil {
			return
		}
		mu.Lock()
		out.Errors = append(out.Errors, label+": "+err.Error())
		mu.Unlock()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		path := fmt.Sprintf("/api/pipelines/%s/%s/%s", args.Pipeline, args.Phase, args.App)
		var app types.App
		if err := client.GetJSON(ctx, path, &app); err != nil {
			recordErr("spec", err)
			return
		}
		mu.Lock()
		out.Spec = &app
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		path := fmt.Sprintf("/api/apps/%s/%s/%s/pods", args.Pipeline, args.Phase, args.App)
		var pods []podSummary
		if err := client.GetJSON(ctx, path, &pods); err != nil {
			recordErr("pods", err)
			return
		}
		mu.Lock()
		out.Pods = pods
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		path := fmt.Sprintf("/api/logs/%s/%s/%s/web/history", args.Pipeline, args.Phase, args.App)
		var logs []string
		if err := client.GetJSON(ctx, path, &logs); err != nil {
			recordErr("logs", err)
			return
		}
		if len(logs) > lines {
			logs = logs[len(logs)-lines:]
		}
		mu.Lock()
		out.Logs = logs
		mu.Unlock()
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		var events []eventSummary
		if err := client.GetJSON(ctx, "/api/kubernetes/namespace", &events); err != nil {
			recordErr("events", err)
			return
		}
		mu.Lock()
		out.Events = events
		mu.Unlock()
	}()

	wg.Wait()
	return out, nil
}

func registerTroubleshootApp(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "troubleshoot_app",
		Description: "Composite diagnostic for a kuso app. Fetches the current spec, pod status, last N runtime log lines, and recent kubernetes events in parallel and returns one structured analysis blob. " +
			"Prefer this over chaining describe_app + tail_logs when investigating 'why is X broken'. " +
			"Errors from individual sub-fetches are reported in result.errors instead of failing the whole tool — partial diagnostics are still useful.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args troubleshootArgs) (*mcp.CallToolResult, troubleshootResult, error) {
		out, err := runTroubleshootApp(ctx, client, args)
		if err != nil {
			return nil, troubleshootResult{}, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: summarizeTroubleshoot(out)}},
		}, out, nil
	})
}

func summarizeTroubleshoot(r troubleshootResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s/%s troubleshoot:\n", r.Pipeline, r.Phase, r.App)
	if r.Spec != nil {
		state := "running"
		if r.Spec.Sleep == "enabled" {
			state = "sleeping"
		}
		fmt.Fprintf(&b, "  state: %s\n", state)
		if img := formatImage(r.Spec.Image); img != "" {
			fmt.Fprintf(&b, "  image: %s\n", img)
		}
	}
	fmt.Fprintf(&b, "  pods: %d\n", len(r.Pods))
	fmt.Fprintf(&b, "  log lines: %d\n", len(r.Logs))
	fmt.Fprintf(&b, "  events: %d\n", len(r.Events))
	if len(r.Errors) > 0 {
		b.WriteString("  partial — errors:\n")
		for _, e := range r.Errors {
			fmt.Fprintf(&b, "    %s\n", e)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
