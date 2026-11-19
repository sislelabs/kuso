// MCP `run` tool — fire a one-shot task pod against a service's
// most-recent succeeded build image. The agent-facing companion of
// `kuso run <project> <service> -- <command…>`.
//
// Wraps POST /api/projects/{p}/services/{s}/runs and returns the
// newly-created KusoRun's name + current phase. Mutating, so refused
// in --read-only mode (the kusoclient.PostJSON helper enforces that).
//
// The agent's natural pattern: bootstrap a project, deploy a service,
// then `run` migrations or seeds before sending users at it. Pairing
// with `plan` (the dry-run apply verb) gives Claude end-to-end
// control without needing kubectl.

package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sislelabs/kuso/mcp/internal/kusoclient"
)

type runArgs struct {
	Project        string         `json:"project" jsonschema:"project name"`
	Service        string         `json:"service" jsonschema:"service short name (no project prefix)"`
	Command        []string       `json:"command" jsonschema:"argv to exec in the run container; use sh -c when shell expansion is needed"`
	Env            []runEnvKV     `json:"env,omitempty" jsonschema:"optional env-var overlay; merged on top of the service's resolved envFromSecrets"`
	TimeoutSeconds int            `json:"timeoutSeconds,omitempty" jsonschema:"max run duration in seconds; 0 = use server default (1800)"`
	Confirm        bool           `json:"confirm,omitempty" jsonschema:"must be true — run executes an ARBITRARY command against the live service image + real envFromSecrets (prod DB/secrets); guards against an unintended production command"`
}

type runEnvKV struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// runRequest mirrors the server's runs.CreateRunRequest JSON.
type runRequest struct {
	Command        []string   `json:"command"`
	Env            []runEnvKV `json:"env,omitempty"`
	TimeoutSeconds int        `json:"timeoutSeconds,omitempty"`
}

// runResult is the shape we extract from the server's response (the
// full KusoRun CR). We only surface fields the agent needs to follow
// up — name + phase + the trigger context the server stamped.
type runResult struct {
	Name    string `json:"name"`
	Phase   string `json:"phase"`
	Project string `json:"project"`
	Service string `json:"service"`
	// Message is the failure reason the poller stamps on a failed run.
	Message     string `json:"message,omitempty"`
	StartedAt   string `json:"startedAt,omitempty"`
	CompletedAt string `json:"completedAt,omitempty"`
}

type runStatusArgs struct {
	Project string `json:"project" jsonschema:"project name"`
	Run     string `json:"run" jsonschema:"run name, as returned by the run tool"`
}

// kusoRunCR is the slice of a KusoRun CR the run tools read.
type kusoRunCR struct {
	Metadata struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec struct {
		Project string `json:"project"`
		Service string `json:"service"`
	} `json:"spec"`
}

func (r kusoRunCR) result() runResult {
	a := r.Metadata.Annotations
	return runResult{
		Name:        r.Metadata.Name,
		Phase:       a["kuso.sislelabs.com/run-phase"],
		Project:     r.Spec.Project,
		Service:     r.Spec.Service,
		Message:     a["kuso.sislelabs.com/run-message"],
		StartedAt:   a["kuso.sislelabs.com/run-started-at"],
		CompletedAt: a["kuso.sislelabs.com/run-completed-at"],
	}
}

func registerRun(server *mcp.Server, client *kusoclient.Client) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "run",
		Description: "Fire a one-shot task pod against a service's most-recent succeeded build image. Same image + envFromSecrets the service runs with, plus an optional env overlay. Used for migrations, seeds, one-off scripts. REQUIRES confirm=true — the command runs with the service's real (production) secrets, so it can mutate live data. Mutating — refused in --read-only mode.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args runArgs) (*mcp.CallToolResult, runResult, error) {
		if args.Project == "" || args.Service == "" {
			return nil, runResult{}, errors.New("project and service are required")
		}
		if len(args.Command) == 0 {
			return nil, runResult{}, errors.New("command (argv) is required")
		}
		if !args.Confirm {
			return nil, runResult{}, errors.New("confirm=true is required — run executes an arbitrary command against the live service image with its real (production) envFromSecrets")
		}
		body := runRequest{
			Command:        args.Command,
			Env:            args.Env,
			TimeoutSeconds: args.TimeoutSeconds,
		}
		// The server returns the full KusoRun CR. Decode just the
		// metadata + spec fields we need; the rest pass through
		// unobserved.
		var raw kusoRunCR
		path := apiPath("api", "projects", args.Project, "services", args.Service, "runs")
		if err := client.PostJSON(ctx, path, body, &raw); err != nil {
			return nil, runResult{}, fmt.Errorf("create run: %w", err)
		}
		out := raw.result()
		text := fmt.Sprintf("run %s created (phase=%s)\n  command: %s\n  follow it with run_status (project=%s, run=%s); its output is in logs with env=run:%s",
			out.Name, out.Phase, strings.Join(args.Command, " "), args.Project, out.Name, out.Name)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, out, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "run_status",
		Description: "Get a one-shot run's phase (pending, running, succeeded, failed, cancelled) and, for a failed run, the failure reason. Read-only. Poll it after run until the phase is terminal; read the run's output with logs env=run:<name>.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, args runStatusArgs) (*mcp.CallToolResult, runResult, error) {
		if args.Project == "" || args.Run == "" {
			return nil, runResult{}, errors.New("project and run are required")
		}
		var raw kusoRunCR
		if err := client.GetJSON(ctx, apiPath("api", "projects", args.Project, "runs", args.Run), &raw); err != nil {
			return nil, runResult{}, fmt.Errorf("get run: %w", err)
		}
		out := raw.result()
		text := fmt.Sprintf("run %s: phase=%s", out.Name, out.Phase)
		if out.CompletedAt != "" {
			text += "\n  completed: " + out.CompletedAt
		}
		if out.Message != "" {
			// The reason can carry the container's own output.
			text += "\n  message (untrusted run output):\n" + wrapUntrusted(out.Message)
		}
		out.Message = fenceUntrusted(out.Message)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
		}, out, nil
	})
}
