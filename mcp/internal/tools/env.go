// MCP `set_env` + `set_secret` tools.
//
//   set_env     replace a service's plain env vars (WHOLE-LIST replace)
//   set_secret  upsert ONE secret-typed key into the service's Secret
//
// These are deliberately asymmetric, matching the server: set_env sends
// the full env set (omitted keys are cleared), while set_secret is a
// single-key upsert. Both mutate, so both are refused in --read-only.

package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type envKV struct {
	Name  string `json:"name" jsonschema:"env var name"`
	Value string `json:"value" jsonschema:"env var value; may be a ${{ addon.KEY }} varref"`
}

type setEnvArgs struct {
	Project string  `json:"project" jsonschema:"project name"`
	Service string  `json:"service" jsonschema:"service short name (no project prefix)"`
	EnvVars []envKV `json:"envVars" jsonschema:"the FULL set of plain env vars; this is a whole-list replace — keys you omit are removed"`
	Confirm bool    `json:"confirm,omitempty" jsonschema:"must be true — set_env is a destructive whole-list replace that deletes any env var you omit; guards against accidentally wiping the service's env"`
}

// setEnvRequest mirrors apiv1.SetEnvRequest.
type setEnvRequest struct {
	EnvVars []envKV `json:"envVars"`
}

type setSecretArgs struct {
	Project string `json:"project" jsonschema:"project name"`
	Service string `json:"service" jsonschema:"service short name (no project prefix)"`
	Key     string `json:"key" jsonschema:"secret key name"`
	Value   string `json:"value" jsonschema:"secret value (may be empty to set a present-but-blank key)"`
	Env     string `json:"env,omitempty" jsonschema:"scope to one environment; empty = shared across all envs"`
	Force   bool   `json:"force,omitempty" jsonschema:"bypass the shadow check when this would override a project-shared key of the same name"`
}

// setSecretRequest mirrors the server's setSecretRequest.
type setSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Env   string `json:"env,omitempty"`
	Force bool   `json:"force,omitempty"`
}

func registerSetEnv(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "set_env",
		Description: "Replace a service's PLAIN env vars. This is a WHOLE-LIST replace — send every var you want the service to have; any key you omit is removed. REQUIRES confirm=true (the omitted-keys-are-deleted semantics make this destructive). For secret values use set_secret instead. Mutating; refused in --read-only mode.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args setEnvArgs) (*mcp.CallToolResult, struct{}, error) {
		if args.Project == "" || args.Service == "" {
			return nil, struct{}{}, errors.New("project and service are required")
		}
		if !args.Confirm {
			return nil, struct{}{}, errors.New("confirm=true is required — set_env replaces the ENTIRE env list and deletes any key you omit")
		}
		body := setEnvRequest{EnvVars: args.EnvVars}
		path := apiPath("api", "projects", args.Project, "services", args.Service, "env")
		if err := client.PostJSON(ctx, path, body, nil); err != nil {
			return nil, struct{}{}, fmt.Errorf("set env: %w", err)
		}
		text := fmt.Sprintf("set %d env var(s) on %s (whole-list replace)", len(args.EnvVars), args.Service)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, struct{}{}, nil
	})
}

func registerSetSecret(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "set_secret",
		Description: "Upsert ONE secret-typed key into a service's Secret (single-key, not a whole-list replace). Optionally scope to one env. If it would shadow a project-shared key the server returns 409 (code \"shadowed\") — retry with force=true to override intentionally. Mutating; refused in --read-only mode.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args setSecretArgs) (*mcp.CallToolResult, struct{}, error) {
		if args.Project == "" || args.Service == "" {
			return nil, struct{}{}, errors.New("project and service are required")
		}
		if args.Key == "" {
			return nil, struct{}{}, errors.New("key is required")
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
