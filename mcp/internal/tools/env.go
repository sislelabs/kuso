// MCP `get_env`, `set_env` + `set_secret` tools.
//
//   get_env     read a service's env vars, including valueFrom refs
//   set_env     upsert / remove individual plain env vars (per key)
//   set_secret  upsert ONE secret-typed key into the service's Secret
//
// set_env used to POST the whole list to /env, which replaced
// spec.envVars: keys the agent omitted were deleted, and since its
// {name,value} shape can't carry valueFrom, every secretKeyRef (addon
// refs) was dropped on every call. It now goes through the per-key
// PUT/DELETE /env-vars/{name} routes, so keys it doesn't name are left
// alone. All writes are refused in --read-only.

package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type envKV struct {
	Name  string `json:"name" jsonschema:"env var name"`
	Value string `json:"value" jsonschema:"env var value; may be a ${{ addon.KEY }} varref"`
}

type setEnvArgs struct {
	Project string   `json:"project" jsonschema:"project name"`
	Service string   `json:"service" jsonschema:"service short name (no project prefix)"`
	EnvVars []envKV  `json:"envVars,omitempty" jsonschema:"env vars to create or overwrite; keys not listed here are left untouched"`
	Unset   []string `json:"unset,omitempty" jsonschema:"env var names to remove"`
	Confirm bool     `json:"confirm,omitempty" jsonschema:"must be true — set_env writes to the live service and triggers a rolling restart"`
}

// setEnvVarRequest mirrors apiv1.SetEnvVarRequest (plain-literal form).
type setEnvVarRequest struct {
	Value string `json:"value"`
}

type getEnvArgs struct {
	Project string `json:"project" jsonschema:"project name"`
	Service string `json:"service" jsonschema:"service short name (no project prefix)"`
	Env     string `json:"env,omitempty" jsonschema:"read one environment's overrides (staging, preview-pr-N) instead of the service-level list"`
}

// envVarOut mirrors projects.EnvVar. ValueFrom carries secretKeyRef
// pointers (addon / shared-secret refs) that a plain value can't express.
type envVarOut struct {
	Name      string         `json:"name"`
	Value     string         `json:"value,omitempty"`
	ValueFrom map[string]any `json:"valueFrom,omitempty"`
	Source    string         `json:"source,omitempty"`
	Addon     string         `json:"addon,omitempty"`
}

type getEnvResult struct {
	EnvVars []envVarOut `json:"envVars"`
	Masked  bool        `json:"masked"`
}

type setSecretArgs struct {
	Project string `json:"project" jsonschema:"project name"`
	Service string `json:"service" jsonschema:"service short name (no project prefix)"`
	Key     string `json:"key" jsonschema:"secret key name"`
	Value   string `json:"value" jsonschema:"secret value (may be empty to set a present-but-blank key)"`
	Env     string `json:"env,omitempty" jsonschema:"scope to one environment; empty = shared across all envs"`
	Force   bool   `json:"force,omitempty" jsonschema:"bypass the shadow check when this would override a project-shared key of the same name"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true — set_secret writes a secret to the live service (and can overwrite an existing credential, triggering a rolling restart); guards against an unintended secret write"`
}

// setSecretRequest mirrors the server's setSecretRequest.
type setSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Env   string `json:"env,omitempty"`
	Force bool   `json:"force,omitempty"`
}

func registerGetEnv(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_env",
		Description: "List a service's env vars, including valueFrom secretKeyRef entries (addon / shared-secret refs) and kuso-managed secret keys. Values are masked unless the token may read secrets. Read-only. Call this before set_env to see what is already there.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args getEnvArgs) (*mcp.CallToolResult, getEnvResult, error) {
		if args.Project == "" || args.Service == "" {
			return nil, getEnvResult{}, errors.New("project and service are required")
		}
		path := apiPath("api", "projects", args.Project, "services", args.Service, "env")
		if env := strings.TrimSpace(args.Env); env != "" {
			path += "?env=" + url.QueryEscape(env)
		}
		var out getEnvResult
		if err := client.GetJSON(ctx, path, &out); err != nil {
			return nil, getEnvResult{}, fmt.Errorf("get env: %w", err)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d env var(s) on %s", len(out.EnvVars), args.Service)
		if out.Masked {
			b.WriteString(" (values masked)")
		}
		b.WriteString(":\n")
		for _, e := range out.EnvVars {
			switch {
			case e.ValueFrom != nil:
				fmt.Fprintf(&b, "  %s  <from %s>\n", e.Name, describeValueFrom(e.ValueFrom))
			case e.Source != "":
				fmt.Fprintf(&b, "  %s=%s  (%s)\n", e.Name, e.Value, e.Source)
			default:
				fmt.Fprintf(&b, "  %s=%s\n", e.Name, e.Value)
			}
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: b.String()}},
		}, out, nil
	})
}

func describeValueFrom(vf map[string]any) string {
	if ref, ok := vf["secretKeyRef"].(map[string]any); ok {
		name, _ := ref["name"].(string)
		key, _ := ref["key"].(string)
		return "secret " + name + "/" + key
	}
	return "valueFrom"
}

func registerSetEnv(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "set_env",
		Description: "Create/overwrite the plain env vars listed in envVars and remove the names listed in unset. Per-key: env vars you don't mention (including addon secretKeyRef entries) are left untouched. REQUIRES confirm=true (a write to the live service; pods roll). Use get_env to read the current list first. For secret values use set_secret instead. Mutating; refused in --read-only mode.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args setEnvArgs) (*mcp.CallToolResult, struct{}, error) {
		if args.Project == "" || args.Service == "" {
			return nil, struct{}{}, errors.New("project and service are required")
		}
		if len(args.EnvVars) == 0 && len(args.Unset) == 0 {
			return nil, struct{}{}, errors.New("nothing to do: pass envVars and/or unset")
		}
		for _, kv := range args.EnvVars {
			if kv.Name == "" {
				return nil, struct{}{}, errors.New("every envVars entry needs a name")
			}
		}
		if !args.Confirm {
			return nil, struct{}{}, errors.New("confirm=true is required — set_env writes to the live service and triggers a rolling restart")
		}
		set, unset := 0, 0
		for _, kv := range args.EnvVars {
			path := apiPath("api", "projects", args.Project, "services", args.Service, "env-vars", kv.Name)
			if err := client.PutJSON(ctx, path, setEnvVarRequest{Value: kv.Value}, nil); err != nil {
				return nil, struct{}{}, fmt.Errorf("set env %s (after %d set, %d unset): %w", kv.Name, set, unset, err)
			}
			set++
		}
		for _, name := range args.Unset {
			path := apiPath("api", "projects", args.Project, "services", args.Service, "env-vars", name)
			if err := client.DeleteJSON(ctx, path); err != nil {
				return nil, struct{}{}, fmt.Errorf("unset env %s (after %d set, %d unset): %w", name, set, unset, err)
			}
			unset++
		}
		text := fmt.Sprintf("set %d and removed %d env var(s) on %s; other keys unchanged", set, unset, args.Service)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, struct{}{}, nil
	})
}

func registerSetSecret(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "set_secret",
		Description: "Upsert ONE secret-typed key into a service's Secret (single-key, not a whole-list replace). Optionally scope to one env. REQUIRES confirm=true (a secret write to the live service, possibly overwriting an existing credential). If it would shadow a project-shared key the server returns 409 (code \"shadowed\") — retry with force=true to override intentionally. Mutating; refused in --read-only mode.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args setSecretArgs) (*mcp.CallToolResult, struct{}, error) {
		if args.Project == "" || args.Service == "" {
			return nil, struct{}{}, errors.New("project and service are required")
		}
		if args.Key == "" {
			return nil, struct{}{}, errors.New("key is required")
		}
		if !args.Confirm {
			return nil, struct{}{}, errors.New("confirm=true is required — set_secret writes a secret to the live service and can overwrite an existing credential")
		}
		body := setSecretRequest{Key: args.Key, Value: args.Value, Env: args.Env, Force: args.Force}
		path := apiPath("api", "projects", args.Project, "services", args.Service, "secrets")
		if err := client.PostJSON(ctx, path, body, nil); err != nil {
			return nil, struct{}{}, fmt.Errorf("set secret: %w", err)
		}
		scope := "shared"
		if args.Env != "" {
			scope = "env=" + args.Env
		}
		text := fmt.Sprintf("set secret %s on %s (%s)", args.Key, args.Service, scope)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, struct{}{}, nil
	})
}
