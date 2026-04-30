package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
	"github.com/sislelabs/kuso/mcp/internal/types"
)

// ---------- list_apps ----------

type listAppsArgs struct {
	Pipeline string `json:"pipeline,omitempty" jsonschema:"if set, only list apps in this pipeline"`
	Phase    string `json:"phase,omitempty" jsonschema:"if set, only list apps in this phase (e.g. production, staging)"`
}

type listAppsResult struct {
	Count int            `json:"count"`
	Apps  []listAppsItem `json:"apps"`
}

type listAppsItem struct {
	Name     string `json:"name"`
	Pipeline string `json:"pipeline"`
	Phase    string `json:"phase"`
	Sleep    string `json:"sleep,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Image    string `json:"image,omitempty"`
}

func runListApps(ctx context.Context, client *kusoclient.Client, args listAppsArgs) (listAppsResult, error) {
	var apps []types.App
	if err := client.GetJSON(ctx, "/api/apps", &apps); err != nil {
		return listAppsResult{}, fmt.Errorf("list apps: %w", err)
	}

	out := listAppsResult{Apps: make([]listAppsItem, 0, len(apps))}
	for _, a := range apps {
		if args.Pipeline != "" && a.Pipeline != args.Pipeline {
			continue
		}
		if args.Phase != "" && a.Phase != args.Phase {
			continue
		}
		out.Apps = append(out.Apps, listAppsItem{
			Name:     a.Name,
			Pipeline: a.Pipeline,
			Phase:    a.Phase,
			Sleep:    a.Sleep,
			Branch:   a.Branch,
			Image:    formatImage(a.Image),
		})
	}
	sort.Slice(out.Apps, func(i, j int) bool {
		if out.Apps[i].Pipeline != out.Apps[j].Pipeline {
			return out.Apps[i].Pipeline < out.Apps[j].Pipeline
		}
		if out.Apps[i].Phase != out.Apps[j].Phase {
			return out.Apps[i].Phase < out.Apps[j].Phase
		}
		return out.Apps[i].Name < out.Apps[j].Name
	})
	out.Count = len(out.Apps)
	return out, nil
}

func registerListApps(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_apps",
		Description: "List all kuso apps the caller has access to, optionally filtered by pipeline and/or phase. Returns one row per app with name, pipeline, phase, sleep state, branch, and current image. " +
			"For full configuration of a single app, prefer describe_app(name). For incident triage of a single app, prefer troubleshoot_app(name).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args listAppsArgs) (*mcp.CallToolResult, listAppsResult, error) {
		out, err := runListApps(ctx, client, args)
		if err != nil {
			return nil, listAppsResult{}, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: summarizeListApps(out, args)}},
		}, out, nil
	})
}

func summarizeListApps(r listAppsResult, args listAppsArgs) string {
	var b strings.Builder
	switch {
	case args.Pipeline != "" && args.Phase != "":
		fmt.Fprintf(&b, "%d app(s) in %s/%s.", r.Count, args.Pipeline, args.Phase)
	case args.Pipeline != "":
		fmt.Fprintf(&b, "%d app(s) in pipeline %s.", r.Count, args.Pipeline)
	case args.Phase != "":
		fmt.Fprintf(&b, "%d app(s) in phase %s.", r.Count, args.Phase)
	default:
		fmt.Fprintf(&b, "%d app(s) total.", r.Count)
	}
	if r.Count == 0 {
		return b.String()
	}
	b.WriteString("\n")
	for _, a := range r.Apps {
		fmt.Fprintf(&b, "  %s/%s/%s", a.Pipeline, a.Phase, a.Name)
		if a.Sleep == "enabled" {
			b.WriteString(" [sleeping]")
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// ---------- describe_app ----------

type describeAppArgs struct {
	Pipeline string `json:"pipeline" jsonschema:"pipeline name"`
	Phase    string `json:"phase" jsonschema:"phase name (e.g. production, staging, review)"`
	App      string `json:"app" jsonschema:"app name"`
}

type describeAppResult struct {
	App    types.App `json:"app"`
	Config struct {
		Image    types.AppImage `json:"image"`
		Web      types.AppScale `json:"web"`
		Sleep    string         `json:"sleep"`
		Branch   string         `json:"branch"`
		Domain   string         `json:"domain,omitempty"`
		Strategy string         `json:"buildstrategy,omitempty"`
	} `json:"config"`
}

func runDescribeApp(ctx context.Context, client *kusoclient.Client, args describeAppArgs) (describeAppResult, error) {
	if args.Pipeline == "" || args.Phase == "" || args.App == "" {
		return describeAppResult{}, errors.New("pipeline, phase, and app are all required")
	}

	path := fmt.Sprintf("/api/pipelines/%s/%s/%s", args.Pipeline, args.Phase, args.App)
	var app types.App
	if err := client.GetJSON(ctx, path, &app); err != nil {
		return describeAppResult{}, fmt.Errorf("describe app: %w", err)
	}

	var out describeAppResult
	out.App = app
	out.Config.Image = app.Image
	out.Config.Web = app.Web
	out.Config.Sleep = app.Sleep
	out.Config.Branch = app.Branch
	out.Config.Domain = app.Domain
	out.Config.Strategy = app.BuildStrategy
	return out, nil
}

func registerDescribeApp(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "describe_app",
		Description: "Return the current configuration of a single kuso app: image, scaling, sleep state, ingress domain, branch, build strategy. " +
			"For runtime status + recent logs + events (i.e. 'why is X broken'), prefer troubleshoot_app(pipeline, phase, app).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args describeAppArgs) (*mcp.CallToolResult, describeAppResult, error) {
		out, err := runDescribeApp(ctx, client, args)
		if err != nil {
			return nil, describeAppResult{}, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: summarizeDescribeApp(args, out.App)}},
		}, out, nil
	})
}

func summarizeDescribeApp(args describeAppArgs, app types.App) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s/%s", args.Pipeline, args.Phase, args.App)
	if app.Sleep == "enabled" {
		b.WriteString(" [sleeping]")
	}
	b.WriteString("\n")
	if img := formatImage(app.Image); img != "" {
		fmt.Fprintf(&b, "  image: %s\n", img)
	}
	if app.Branch != "" {
		fmt.Fprintf(&b, "  branch: %s\n", app.Branch)
	}
	if app.BuildStrategy != "" {
		fmt.Fprintf(&b, "  build: %s\n", app.BuildStrategy)
	}
	scale := app.Web
	if scale.Autoscaling.MaxReplicas > 0 {
		fmt.Fprintf(&b, "  scale: %d (autoscale %d→%d, cpu %d%%)\n",
			scale.ReplicaCount, scale.Autoscaling.MinReplicas, scale.Autoscaling.MaxReplicas,
			scale.Autoscaling.TargetCPUUtilizationPercentage)
	} else {
		fmt.Fprintf(&b, "  replicas: %d\n", scale.ReplicaCount)
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatImage(img types.AppImage) string {
	switch {
	case img.Repository == "" && img.Tag == "":
		return ""
	case img.Tag == "":
		return img.Repository
	default:
		return img.Repository + ":" + img.Tag
	}
}
