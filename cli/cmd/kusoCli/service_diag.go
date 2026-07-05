// service_diag.go adds read-only service diagnostics: aggregated error
// groups and the live pod list. Wired onto BOTH the top-level
// `kuso service ...` command and `kuso project service ...` (they share
// the same RunE), plus a `kuso get pods` alias for symmetry with the
// other `get` reads.
//
//	kuso service errors <project> <service> [-o json] [--since 24h] [--limit 50]
//	kuso service pods   <project> <service> [-o json] [--env <env>]
//	kuso get pods       <project> <service> [-o json] [--env <env>]

package kusoCli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// escPath percent-escapes a path segment. Local to this file because
// kusoApi.esc is unexported; project/service names are DNS-safe but a
// stray query value (--since) shouldn't be trusted raw.
func escPath(s string) string { return url.PathEscape(s) }

var (
	serviceErrorsSince string
	serviceErrorsLimit int
	servicePodsEnv     string
)

// runServiceErrors is shared by the top-level and project-scoped
// `service errors` commands.
func runServiceErrors(cmd *cobra.Command, args []string) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	path := fmt.Sprintf("/api/projects/%s/services/%s/errors", escPath(args[0]), escPath(args[1]))
	sep := "?"
	if serviceErrorsSince != "" {
		path += sep + "since=" + escPath(serviceErrorsSince)
		sep = "&"
	}
	if serviceErrorsLimit > 0 {
		path += sep + fmt.Sprintf("limit=%d", serviceErrorsLimit)
	}
	resp, err := api.RawGet(path)
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("list errors: %w", err)
	}
	// Server returns []db.ErrorGroup (fingerprint, message, count,
	// firstSeen, lastSeen, sampleLine, sampleEnv, samplePod). Decode as
	// a plain map slice so a shape drift doesn't crash the CLI.
	var groups []map[string]any
	if err := json.Unmarshal(resp.Body(), &groups); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	switch outputFormat {
	case "json":
		return jsonOut(groups)
	case "table", "":
		if len(groups) == 0 {
			fmt.Printf("no errors for %s/%s in the lookback window\n", args[0], args[1])
			return nil
		}
		t := tablewriter.NewWriter(os.Stdout)
		t.SetHeader([]string{"COUNT", "MESSAGE", "LAST SEEN", "ENV", "POD"})
		for _, g := range groups {
			msg := asString(g["message"])
			if len(msg) > 80 {
				msg = msg[:77] + "..."
			}
			t.Append([]string{
				asString(g["count"]),
				msg,
				asString(g["lastSeen"]),
				asString(g["sampleEnv"]),
				asString(g["samplePod"]),
			})
		}
		t.Render()
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", outputFormat)
	}
}

// runServicePods is shared by `service pods` and `get pods`.
func runServicePods(cmd *cobra.Command, args []string) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	path := fmt.Sprintf("/api/projects/%s/services/%s/pods", escPath(args[0]), escPath(args[1]))
	if servicePodsEnv != "" {
		path += "?env=" + escPath(servicePodsEnv)
	}
	resp, err := api.RawGet(path)
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	// Server returns projects.PodList: {namespace, pods:[{name, ready,
	// phase, containers}]}. No node/age fields are exposed by the
	// endpoint, so the table shows what's actually there.
	var out struct {
		Namespace string           `json:"namespace"`
		Pods      []map[string]any `json:"pods"`
	}
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	switch outputFormat {
	case "json":
		return jsonOut(out)
	case "table", "":
		if len(out.Pods) == 0 {
			fmt.Printf("no pods for %s/%s (scaled to zero, or the env has none)\n", args[0], args[1])
			return nil
		}
		t := tablewriter.NewWriter(os.Stdout)
		t.SetHeader([]string{"POD", "PHASE", "READY", "CONTAINERS"})
		for _, p := range out.Pods {
			t.Append([]string{
				asString(p["name"]),
				asString(p["phase"]),
				boolText(p["ready"]),
				joinAny(p["containers"]),
			})
		}
		t.Render()
		return nil
	default:
		return fmt.Errorf("unsupported output format %q", outputFormat)
	}
}

func newServiceErrorsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "errors <project> <service>",
		Short:   "Show aggregated error groups for a service (last 24h by default)",
		Args:    cobra.ExactArgs(2),
		Example: `  kuso service errors scubatony api --since 6h -o json`,
		RunE:    runServiceErrors,
	}
	c.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	c.Flags().StringVar(&serviceErrorsSince, "since", "", "lookback window (e.g. 6h, 24h; max 30d, server default 24h)")
	c.Flags().IntVar(&serviceErrorsLimit, "limit", 0, "max groups to return (1-200, server default 50)")
	return c
}

func newServicePodsCmd(use string) *cobra.Command {
	c := &cobra.Command{
		Use:     use,
		Short:   "List the pods backing a service's environment",
		Args:    cobra.ExactArgs(2),
		Example: `  kuso service pods scubatony api --env production`,
		RunE:    runServicePods,
	}
	c.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	c.Flags().StringVar(&servicePodsEnv, "env", "", "environment (default: production)")
	return c
}

func init() {
	// projectServiceCmd + serviceCmd are defined in project.go. Wire the
	// diagnostics onto both trees, mirroring how add/set are shared.
	projectServiceCmd.AddCommand(newServiceErrorsCmd())
	projectServiceCmd.AddCommand(newServicePodsCmd("pods <project> <service>"))
	serviceCmd.AddCommand(newServiceErrorsCmd())
	serviceCmd.AddCommand(newServicePodsCmd("pods <project> <service>"))

	// `kuso get pods <project> <service>` alias for symmetry with the
	// other `get` reads. getCmd is defined in get.go.
	getCmd.AddCommand(newServicePodsCmd("pods <project> <service>"))
}
