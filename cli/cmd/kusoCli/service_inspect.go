// service_inspect.go — the service overlay's diagnostics and deploy
// checks, wired onto both `kuso service` and `kuso project service`:
//
//	kuso service crash            <project> <service> [--env]       last container death + log tail + diagnosis
//	kuso service events           <project> <service> [--env]       recent pod/deployment events
//	kuso service test-healthcheck <project> <service> --path /health [--port] [--env]
//	kuso service image-check      <project> <service> <image[:tag]> does the registry have it?
//	kuso service branches         <project> <service>               the repo's remote branches

package kusoCli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

func checkOutput(output string) error {
	if output != "table" && output != "json" && output != "" {
		return fmt.Errorf("unsupported output format %q", output)
	}
	return nil
}

func servicePath(project, service, rest string) string {
	return fmt.Sprintf("/api/projects/%s/services/%s/%s", escPath(project), escPath(service), rest)
}

func withEnvQuery(path, env string) string {
	if env == "" {
		return path
	}
	return path + "?env=" + url.QueryEscape(env)
}

type crashReport struct {
	Pod            string   `json:"pod"`
	Container      string   `json:"container"`
	Restarts       int32    `json:"restarts"`
	Reason         string   `json:"reason"`
	ExitCode       int32    `json:"exitCode"`
	FinishedAt     string   `json:"finishedAt"`
	Log            []string `json:"log"`
	Classification *struct {
		Kind        string `json:"kind"`
		Summary     string `json:"summary"`
		LineHint    string `json:"lineHint"`
		Remediation *struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
			Fix    string `json:"fix"`
		} `json:"remediation"`
	} `json:"classification"`
}

func newServiceCrashCmd() *cobra.Command {
	var env, output string
	c := &cobra.Command{
		Use:   "crash <project> <service>",
		Short: "Show why a service's container last died: exit, log tail and diagnosis",
		Long: `Show the last time one of the env's containers died: exit reason and
code, what it printed before dying, and kuso's reading of that output (the
same diagnosis the dashboard shows on a crash-looping service).`,
		Example: `  kuso service crash shop api
  kuso service crash shop api --env staging -o json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			if err := checkOutput(output); err != nil {
				return err
			}
			resp, err := api.RawGet(withEnvQuery(servicePath(args[0], args[1], "crash"), env))
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("crash report: %w", err)
			}
			var out struct {
				Crash *crashReport `json:"crash"`
			}
			if err := json.Unmarshal(resp.Body(), &out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			if output == "json" {
				return jsonOut(out)
			}
			if out.Crash == nil {
				fmt.Printf("no container of %s/%s has died\n", args[0], args[1])
				return nil
			}
			fmt.Print(renderCrash(out.Crash))
			return nil
		},
	}
	c.Flags().StringVar(&env, "env", "", "environment (default production)")
	c.Flags().StringVarP(&output, "output", "o", "table", "output format [table, json]")
	return c
}

func renderCrash(c *crashReport) string {
	var b strings.Builder
	exit := fmt.Sprintf("exit code %d", c.ExitCode)
	if c.Reason != "" {
		exit = c.Reason + ", " + exit
	}
	fmt.Fprintf(&b, "%s/%s died (%s), %d restart(s)", c.Pod, c.Container, exit, c.Restarts)
	if c.FinishedAt != "" {
		fmt.Fprintf(&b, ", last %s ago", relativeAge(c.FinishedAt))
	}
	b.WriteString("\n")
	if cl := c.Classification; cl != nil {
		fmt.Fprintf(&b, "\ndiagnosis: %s\n", cl.Summary)
		if cl.LineHint != "" {
			fmt.Fprintf(&b, "  line: %s\n", cl.LineHint)
		}
		if r := cl.Remediation; r != nil {
			fmt.Fprintf(&b, "  %s\n", r.Title)
			if r.Detail != "" {
				fmt.Fprintf(&b, "  %s\n", r.Detail)
			}
			if r.Fix != "" {
				fmt.Fprintf(&b, "  fix:\n    %s\n", strings.ReplaceAll(strings.TrimRight(r.Fix, "\n"), "\n", "\n    "))
			}
		}
	}
	if len(c.Log) > 0 {
		b.WriteString("\nlast output:\n")
		for _, l := range c.Log {
			fmt.Fprintf(&b, "  %s\n", l)
		}
	}
	return b.String()
}

func newServiceEventsCmd() *cobra.Command {
	var env, output string
	c := &cobra.Command{
		Use:     "events <project> <service>",
		Short:   "Show recent Kubernetes events for a service env's pods and deployment",
		Example: `  kuso service events shop api --env staging`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			if err := checkOutput(output); err != nil {
				return err
			}
			resp, err := api.RawGet(withEnvQuery(servicePath(args[0], args[1], "events"), env))
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("pod events: %w", err)
			}
			var out struct {
				Namespace string `json:"namespace"`
				Events    []struct {
					Type     string `json:"type"`
					Reason   string `json:"reason"`
					Message  string `json:"message"`
					Summary  string `json:"summary"`
					Object   string `json:"object"`
					Count    int32  `json:"count"`
					LastSeen string `json:"lastSeen"`
				} `json:"events"`
			}
			if err := json.Unmarshal(resp.Body(), &out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			if output == "json" {
				return jsonOut(out)
			}
			if len(out.Events) == 0 {
				fmt.Printf("no recent events for %s/%s\n", args[0], args[1])
				return nil
			}
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"AGE", "TYPE", "REASON", "OBJECT", "COUNT", "MESSAGE"})
			t.SetAutoWrapText(false)
			for _, e := range out.Events {
				msg := e.Summary
				if msg == "" {
					msg = e.Message
				}
				t.Append([]string{relativeAge(e.LastSeen), e.Type, e.Reason, e.Object, fmt.Sprint(e.Count), truncate(oneLine(msg), 90)})
			}
			t.Render()
			return nil
		},
	}
	c.Flags().StringVar(&env, "env", "", "environment (default production)")
	c.Flags().StringVarP(&output, "output", "o", "table", "output format [table, json]")
	return c
}

func newServiceTestHealthcheckCmd() *cobra.Command {
	var env, path, output string
	var port int32
	c := &cobra.Command{
		Use:   "test-healthcheck <project> <service>",
		Short: "Probe a health-check path on one of the service's running pods",
		Long: `Send GET <path> to one of the env's ready pods from inside the cluster and
print the status, latency and the start of the body. The path doesn't have
to be saved first: test it here, then set it with
kuso service set <project> <service> --healthcheck-path <path>.`,
		Example: `  kuso service test-healthcheck shop api --path /healthz
  kuso service test-healthcheck shop api --path /ready --port 9000 --env staging`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			if err := checkOutput(output); err != nil {
				return err
			}
			if !strings.HasPrefix(path, "/") {
				return fmt.Errorf("--path must start with /")
			}
			body, _ := json.Marshal(map[string]any{"path": path, "port": port, "env": env})
			resp, err := api.Raw("POST", servicePath(args[0], args[1], "healthcheck/test"), body, nil)
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("test health check: %w", err)
			}
			var r struct {
				Status      int    `json:"status"`
				StatusText  string `json:"statusText"`
				LatencyMs   int64  `json:"latencyMs"`
				BodySnippet string `json:"bodySnippet"`
				Location    string `json:"location"`
				Pod         string `json:"pod"`
				URL         string `json:"url"`
				Error       string `json:"error"`
			}
			if err := json.Unmarshal(resp.Body(), &r); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			if output == "json" {
				return jsonOut(r)
			}
			fmt.Printf("GET %s on %s\n", r.URL, r.Pod)
			if r.Error != "" {
				fmt.Printf("  failed: %s (%dms)\n", r.Error, r.LatencyMs)
				return fmt.Errorf("health check would fail")
			}
			fmt.Printf("  %d %s in %dms\n", r.Status, r.StatusText, r.LatencyMs)
			if r.Location != "" {
				fmt.Printf("  redirects to %s (a probe counts 3xx as passing, but check it's intended)\n", r.Location)
			}
			if r.BodySnippet != "" {
				fmt.Printf("  body: %s\n", truncate(oneLine(r.BodySnippet), 200))
			}
			// Kubernetes HTTP probes pass on 200-399.
			if r.Status < 200 || r.Status >= 400 {
				return fmt.Errorf("health check would fail: HTTP %d", r.Status)
			}
			return nil
		},
	}
	c.Flags().StringVar(&path, "path", "", "path to probe, e.g. /healthz (required)")
	c.Flags().Int32Var(&port, "port", 0, "port to probe (default: the service port)")
	c.Flags().StringVar(&env, "env", "", "environment whose pod to probe (default production)")
	c.Flags().StringVarP(&output, "output", "o", "table", "output format [table, json]")
	_ = c.MarkFlagRequired("path")
	return c
}

// splitImageRef splits "repo[:tag]" at the tag colon (not a registry
// port: the tag colon comes after the last slash). A digest isn't a tag.
func splitImageRef(ref string) (repo, tag string) {
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

func newServiceImageCheckCmd() *cobra.Command {
	var output string
	c := &cobra.Command{
		Use:   "image-check <project> <service> <image[:tag]>",
		Short: "Check that a registry has an image tag before deploying it",
		Long: `Ask the image's registry whether the tag exists (anonymously; tag
defaults to latest). Answers found, not_found or unknown: unknown means the
registry couldn't be asked (private, rate-limited, unreachable), not that
the image is missing. A service with a pull secret always answers unknown.`,
		Example: `  kuso service image-check shop api ghcr.io/acme/api:v1.4.2
  kuso service image-check shop api nginx`,
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			if err := checkOutput(output); err != nil {
				return err
			}
			repo, tag := splitImageRef(args[2])
			q := url.Values{"repository": {repo}}
			if tag != "" {
				q.Set("tag", tag)
			}
			resp, err := api.RawGet(servicePath(args[0], args[1], "image-check") + "?" + q.Encode())
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("image check: %w", err)
			}
			var r struct {
				Status string `json:"status"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(resp.Body(), &r); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			if output == "json" {
				return jsonOut(r)
			}
			line := r.Status
			if r.Detail != "" {
				line += ": " + r.Detail
			}
			fmt.Println(line)
			if r.Status == "not_found" {
				return fmt.Errorf("%s is not in the registry", args[2])
			}
			return nil
		},
	}
	c.Flags().StringVarP(&output, "output", "o", "table", "output format [table, json]")
	return c
}

func newServiceBranchesCmd() *cobra.Command {
	var output string
	c := &cobra.Command{
		Use:   "branches <project> <service>",
		Short: "List the branches of a service's git repo",
		Long: `List the remote branches of the service's repo, read with the same
credentials a build clones with. The default branch is marked with *.`,
		Example: `  kuso service branches shop api`,
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			if err := checkOutput(output); err != nil {
				return err
			}
			resp, err := api.RawGet(servicePath(args[0], args[1], "branches"))
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("list branches: %w", err)
			}
			var r struct {
				DefaultBranch string   `json:"defaultBranch"`
				Branches      []string `json:"branches"`
				Truncated     bool     `json:"truncated"`
			}
			if err := json.Unmarshal(resp.Body(), &r); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			if output == "json" {
				return jsonOut(r)
			}
			for _, b := range r.Branches {
				mark := " "
				if b == r.DefaultBranch {
					mark = "*"
				}
				fmt.Printf("%s %s\n", mark, b)
			}
			if r.Truncated {
				fmt.Fprintln(os.Stderr, "note: the repo has more branches than the server lists")
			}
			return nil
		},
	}
	c.Flags().StringVarP(&output, "output", "o", "table", "output format [table, json]")
	return c
}

func init() {
	for _, parent := range []*cobra.Command{projectServiceCmd, serviceCmd} {
		parent.AddCommand(
			newServiceCrashCmd(),
			newServiceEventsCmd(),
			newServiceTestHealthcheckCmd(),
			newServiceImageCheckCmd(),
			newServiceBranchesCmd(),
		)
	}
}
