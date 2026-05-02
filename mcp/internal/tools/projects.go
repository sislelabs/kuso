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

// ---------- list_projects ----------

type listProjectsArgs struct{}

type listProjectsItem struct {
	Name     string `json:"name"`
	Repo     string `json:"repo,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Previews bool   `json:"previews"`
}

type listProjectsResult struct {
	Count    int                `json:"count"`
	Projects []listProjectsItem `json:"projects"`
}

func runListProjects(ctx context.Context, client *kusoclient.Client) (listProjectsResult, error) {
	var projects []types.Project
	if err := client.GetJSON(ctx, "/api/projects", &projects); err != nil {
		return listProjectsResult{}, fmt.Errorf("list projects: %w", err)
	}
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].Metadata.Name < projects[j].Metadata.Name
	})
	out := listProjectsResult{Count: len(projects), Projects: make([]listProjectsItem, 0, len(projects))}
	for _, p := range projects {
		out.Projects = append(out.Projects, listProjectsItem{
			Name:     p.Metadata.Name,
			Repo:     p.Spec.DefaultRepo.URL,
			Branch:   p.Spec.DefaultRepo.DefaultBranch,
			Previews: p.Spec.Previews.Enabled,
		})
	}
	return out, nil
}

func registerListProjects(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "list_projects",
		Description: "List every kuso project the caller has access to. Returns name, repo, default branch, and previews-on/off per project. " +
			"For full configuration of a single project (services, envs, addons), prefer describe_project(name).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listProjectsArgs) (*mcp.CallToolResult, listProjectsResult, error) {
		out, err := runListProjects(ctx, client)
		if err != nil {
			return nil, listProjectsResult{}, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d project(s).", out.Count)
		for _, p := range out.Projects {
			fmt.Fprintf(&b, "\n  %s", p.Name)
			if p.Repo != "" {
				fmt.Fprintf(&b, "  (%s@%s)", p.Repo, p.Branch)
			}
			if p.Previews {
				b.WriteString("  [previews on]")
			}
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, out, nil
	})
}

// ---------- describe_project ----------

type describeProjectArgs struct {
	Project string `json:"project" jsonschema:"project name"`
}

type describeProjectResult struct {
	Project      types.Project       `json:"project"`
	Services     []types.Service     `json:"services"`
	Environments []types.Environment `json:"environments"`
	Addons       []types.Addon       `json:"addons"`
}

func runDescribeProject(ctx context.Context, client *kusoclient.Client, args describeProjectArgs) (describeProjectResult, error) {
	if args.Project == "" {
		return describeProjectResult{}, errors.New("project is required")
	}
	var detail types.ProjectDetail
	if err := client.GetJSON(ctx, "/api/projects/"+args.Project, &detail); err != nil {
		return describeProjectResult{}, fmt.Errorf("describe project: %w", err)
	}
	return describeProjectResult{
		Project:      detail.Project,
		Services:     detail.Services,
		Environments: detail.Environments,
		Addons:       detail.Addons,
	}, nil
}

func registerDescribeProject(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "describe_project",
		Description: "Return everything about a kuso project: project metadata, all services, all environments (production + preview), all addons. " +
			"For incident triage of a specific running env, prefer troubleshoot_environment(project, env) once it lands.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args describeProjectArgs) (*mcp.CallToolResult, describeProjectResult, error) {
		out, err := runDescribeProject(ctx, client, args)
		if err != nil {
			return nil, describeProjectResult{}, err
		}
		var b strings.Builder
		fmt.Fprintf(&b, "project: %s\n", args.Project)
		fmt.Fprintf(&b, "  repo: %s@%s\n",
			out.Project.Spec.DefaultRepo.URL, out.Project.Spec.DefaultRepo.DefaultBranch)
		fmt.Fprintf(&b, "  services: %d, envs: %d, addons: %d",
			len(out.Services), len(out.Environments), len(out.Addons))
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, out, nil
	})
}

// ---------- bootstrap_project ----------

type bootstrapProjectArgs struct {
	Name     string `json:"name" jsonschema:"project name (lowercase, alphanumeric + hyphens)"`
	RepoURL  string `json:"repo_url" jsonschema:"git repo URL (https://github.com/...)"`
	Branch   string `json:"branch,omitempty" jsonschema:"default branch (default: main)"`
	Domain   string `json:"domain,omitempty" jsonschema:"base domain (services get <name>.<this>; default: cluster default)"`
	Previews bool   `json:"previews,omitempty" jsonschema:"enable PR-based preview environments (default: false)"`
	Confirm  bool   `json:"confirm" jsonschema:"must be true to actually create — prevents accidental project creation"`
}

type bootstrapProjectResult struct {
	Project string `json:"project"`
	Status  string `json:"status"`
}

func runBootstrapProject(ctx context.Context, client *kusoclient.Client, args bootstrapProjectArgs) (bootstrapProjectResult, error) {
	if !args.Confirm {
		return bootstrapProjectResult{}, errors.New("confirm=true is required for bootstrap_project")
	}
	if client.ReadOnly() {
		return bootstrapProjectResult{}, errors.New("kuso-mcp is in read-only mode; refusing to create")
	}
	if args.Name == "" || args.RepoURL == "" {
		return bootstrapProjectResult{}, errors.New("name and repo_url are required")
	}
	body := map[string]any{
		"name":        args.Name,
		"defaultRepo": map[string]string{"url": args.RepoURL, "defaultBranch": fallback(args.Branch, "main")},
		"previews":    map[string]any{"enabled": args.Previews},
	}
	if args.Domain != "" {
		body["baseDomain"] = args.Domain
	}
	if err := client.PostJSON(ctx, "/api/projects", body, nil); err != nil {
		return bootstrapProjectResult{}, fmt.Errorf("create project: %w", err)
	}
	return bootstrapProjectResult{Project: args.Name, Status: "created"}, nil
}

func registerBootstrapProject(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "bootstrap_project",
		Description: "Create a new kuso project from a git repo. Production environment is auto-created when services are added later via add_service. " +
			"REQUIRES confirm=true. Refused when kuso-mcp is started with --read-only.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args bootstrapProjectArgs) (*mcp.CallToolResult, bootstrapProjectResult, error) {
		out, err := runBootstrapProject(ctx, client, args)
		if err != nil {
			return nil, bootstrapProjectResult{}, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("project %s %s", out.Project, out.Status)}},
		}, out, nil
	})
}

// ---------- add_service ----------

type addServiceArgs struct {
	Project string `json:"project" jsonschema:"project name"`
	Name    string `json:"name" jsonschema:"service name (e.g. web, api, worker)"`
	Path    string `json:"path,omitempty" jsonschema:"monorepo subpath (default '.')"`
	Runtime string `json:"runtime,omitempty" jsonschema:"dockerfile (default; nixpacks/buildpacks/static aren't wired through the build chart yet)"`
	Port    int    `json:"port,omitempty" jsonschema:"container port (default: 8080)"`
	Confirm bool   `json:"confirm" jsonschema:"must be true to actually add"`
}

type addServiceResult struct {
	Project string `json:"project"`
	Service string `json:"service"`
	Status  string `json:"status"`
}

func runAddService(ctx context.Context, client *kusoclient.Client, args addServiceArgs) (addServiceResult, error) {
	if !args.Confirm {
		return addServiceResult{}, errors.New("confirm=true is required")
	}
	if client.ReadOnly() {
		return addServiceResult{}, errors.New("kuso-mcp is in read-only mode; refusing to add")
	}
	if args.Project == "" || args.Name == "" {
		return addServiceResult{}, errors.New("project and name are required")
	}
	body := map[string]any{
		"name":    args.Name,
		"repo":    map[string]string{"path": fallback(args.Path, ".")},
		"runtime": fallback(args.Runtime, "dockerfile"),
		"port":    fallbackInt(args.Port, 8080),
	}
	if err := client.PostJSON(ctx, "/api/projects/"+args.Project+"/services", body, nil); err != nil {
		return addServiceResult{}, fmt.Errorf("add service: %w", err)
	}
	return addServiceResult{Project: args.Project, Service: args.Name, Status: "added"}, nil
}

func registerAddService(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "add_service",
		Description: "Add a service to a project. Auto-creates a production environment with default-branch tracking. " +
			"REQUIRES confirm=true. Refused in --read-only mode.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args addServiceArgs) (*mcp.CallToolResult, addServiceResult, error) {
		out, err := runAddService(ctx, client, args)
		if err != nil {
			return nil, addServiceResult{}, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("%s/%s %s", out.Project, out.Service, out.Status)}},
		}, out, nil
	})
}

// ---------- manage_addon ----------

type manageAddonArgs struct {
	Project string `json:"project" jsonschema:"project name"`
	Action  string `json:"action" jsonschema:"add | delete"`
	Name    string `json:"name" jsonschema:"addon name (used in connection-secret name)"`
	Kind    string `json:"kind,omitempty" jsonschema:"required for add: postgres|redis|mongodb|mysql|rabbitmq|memcached|clickhouse|elasticsearch|kafka|cockroachdb|couchdb"`
	Version string `json:"version,omitempty"`
	Size    string `json:"size,omitempty" jsonschema:"small|medium|large (default: small)"`
	HA      bool   `json:"ha,omitempty"`
	Confirm bool   `json:"confirm" jsonschema:"must be true to actually mutate"`
}

type manageAddonResult struct {
	Project string `json:"project"`
	Addon   string `json:"addon"`
	Action  string `json:"action"`
	Status  string `json:"status"`
}

var allowedAddonKinds = map[string]bool{
	"postgres": true, "redis": true, "mongodb": true, "mysql": true,
	"rabbitmq": true, "memcached": true, "clickhouse": true,
	"elasticsearch": true, "kafka": true, "cockroachdb": true, "couchdb": true,
}

func runManageAddon(ctx context.Context, client *kusoclient.Client, args manageAddonArgs) (manageAddonResult, error) {
	if !args.Confirm {
		return manageAddonResult{}, errors.New("confirm=true is required")
	}
	if client.ReadOnly() {
		return manageAddonResult{}, errors.New("kuso-mcp is in read-only mode; refusing to mutate")
	}
	if args.Project == "" || args.Name == "" {
		return manageAddonResult{}, errors.New("project and name are required")
	}
	switch args.Action {
	case "add":
		if !allowedAddonKinds[args.Kind] {
			return manageAddonResult{}, fmt.Errorf("kind %q is not supported", args.Kind)
		}
		body := map[string]any{
			"name":    args.Name,
			"kind":    args.Kind,
			"version": args.Version,
			"size":    fallback(args.Size, "small"),
			"ha":      args.HA,
		}
		if err := client.PostJSON(ctx, "/api/projects/"+args.Project+"/addons", body, nil); err != nil {
			return manageAddonResult{}, fmt.Errorf("add addon: %w", err)
		}
		return manageAddonResult{Project: args.Project, Addon: args.Name, Action: "add", Status: "added"}, nil
	case "delete":
		if err := client.DeleteJSON(ctx, "/api/projects/"+args.Project+"/addons/"+args.Name); err != nil {
			return manageAddonResult{}, fmt.Errorf("delete addon: %w", err)
		}
		return manageAddonResult{Project: args.Project, Addon: args.Name, Action: "delete", Status: "deleted"}, nil
	default:
		return manageAddonResult{}, fmt.Errorf("unknown action %q (want add | delete)", args.Action)
	}
}

func registerManageAddon(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "manage_addon",
		Description: "Add or delete an addon on a project. Adding emits a connection-info Secret that's auto-injected as envFrom into every service in the project (DATABASE_URL etc.). " +
			"Supported kinds today: postgres, redis. Other kinds are reserved (placeholder created, no workload). " +
			"REQUIRES confirm=true. Refused in --read-only mode.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args manageAddonArgs) (*mcp.CallToolResult, manageAddonResult, error) {
		out, err := runManageAddon(ctx, client, args)
		if err != nil {
			return nil, manageAddonResult{}, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("addon %s/%s %s", out.Project, out.Addon, out.Status)}},
		}, out, nil
	})
}

// ---------- helpers ----------

func fallback(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func fallbackInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
