// service_diag.go adds read-only service diagnostics: aggregated error
// groups and the live pod list. Wired onto BOTH the top-level
// `kuso service ...` command and `kuso project service ...` (they share
// the same RunE), plus a `kuso get pods` alias for symmetry with the
// other `get` reads.
//
//	kuso service errors <project> <service> [-o json] [--since 24h|--since-deploy] [--resolved] [--limit 50] [--env <env>]
//	kuso service errors show|resolve|reopen <project> <service> <fingerprint>
//	kuso service pods   <project> <service> [-o json] [--env <env>]
//	kuso get pods       <project> <service> [-o json] [--env <env>]

package kusoCli

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// escPath percent-escapes a path segment. Local to this file because
// kusoApi.esc is unexported; project/service names are DNS-safe but a
// stray query value (--since) shouldn't be trusted raw.
func escPath(s string) string { return url.PathEscape(s) }

var (
	serviceErrorsSince       string
	serviceErrorsLimit       int
	serviceErrorsOffset      int
	serviceErrorsEnv         string
	serviceErrorsResolved    bool
	serviceErrorsSinceDeploy bool
	servicePodsEnv           string
)

// runServiceErrors is shared by the top-level and project-scoped
// `service errors` commands.
func runServiceErrors(cmd *cobra.Command, args []string) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	since, env, err := errorsWindow(args[0], args[1], serviceErrorsSince, serviceErrorsEnv, serviceErrorsSinceDeploy)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/api/projects/%s/services/%s/errors", escPath(args[0]), escPath(args[1]))
	sep := "?"
	if since != "" {
		path += sep + "since=" + escPath(since)
		sep = "&"
	}
	if serviceErrorsResolved {
		path += sep + "resolved=include"
		sep = "&"
	}
	if serviceErrorsLimit > 0 {
		path += sep + fmt.Sprintf("limit=%d", serviceErrorsLimit)
		sep = "&"
	}
	if serviceErrorsOffset > 0 {
		path += sep + fmt.Sprintf("offset=%d", serviceErrorsOffset)
		sep = "&"
	}
	if env != "" {
		path += sep + "env=" + url.QueryEscape(env)
	}
	resp, err := api.RawGet(path)
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("list errors: %w", err)
	}
	// Truncation signal (X-Kuso-Truncated header). Stderr so `-o json`
	// stdout stays machine-parseable.
	if resp.Header().Get("X-Kuso-Truncated") == "true" {
		if next := resp.Header().Get("X-Kuso-Next-Offset"); next != "" {
			fmt.Fprintf(os.Stderr, "note: result truncated — more error groups exist; continue with --offset %s\n", next)
		}
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
		t.SetHeader([]string{"FINGERPRINT", "STATE", "COUNT", "MESSAGE", "LAST SEEN", "ENV"})
		for _, g := range groups {
			t.Append([]string{
				asString(g["fingerprint"]),
				errorGroupState(asString(g["lastSeen"]), asString(g["resolvedAt"])),
				asString(g["count"]),
				truncate(asString(g["message"]), 70),
				relativeAge(asString(g["lastSeen"])) + " ago",
				asString(g["sampleEnv"]),
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
	// phase, containers, restarts}]}. No node/age fields are exposed by the
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
		t.SetHeader([]string{"POD", "PHASE", "READY", "RESTARTS", "CONTAINERS"})
		for _, p := range out.Pods {
			t.Append([]string{
				asString(p["name"]),
				asString(p["phase"]),
				boolText(p["ready"]),
				asString(p["restarts"]),
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
		Use:   "errors <project> <service>",
		Short: "Show aggregated error groups for a service (last 24h by default)",
		Long: `Show a service's error groups. Resolved groups stay hidden until they
recur (STATE "recurred"); --resolved lists them too. Resolve or reopen a
group by its FINGERPRINT with the resolve / reopen subcommands.`,
		Args: cobra.ExactArgs(2),
		Example: `  kuso service errors scubatony api --since 6h -o json
  kuso service errors tickero api --env production
  kuso service errors tickero api --since-deploy
  kuso service errors show tickero api 065dc963009d57e2
  kuso service errors resolve tickero api 065dc963009d57e2`,
		RunE: runServiceErrors,
	}
	c.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	c.Flags().StringVar(&serviceErrorsSince, "since", "", "lookback window (e.g. 6h, 24h; max 30d, server default 24h)")
	c.Flags().BoolVar(&serviceErrorsSinceDeploy, "since-deploy", false, "only errors since the env's current deploy went live (the --env env, default production)")
	c.Flags().BoolVar(&serviceErrorsResolved, "resolved", false, "also list resolved groups that haven't recurred")
	c.Flags().IntVar(&serviceErrorsLimit, "limit", 0, "max groups to return (1-200, server default 50)")
	c.Flags().IntVar(&serviceErrorsOffset, "offset", 0, "groups to skip (from X-Kuso-Next-Offset when a page was truncated)")
	c.Flags().StringVar(&serviceErrorsEnv, "env", "", "only this environment's errors (production, staging, preview-pr-N); default: every env")
	c.MarkFlagsMutuallyExclusive("since", "since-deploy")
	c.AddCommand(newServiceErrorShowCmd(), newServiceErrorResolveCmd(false), newServiceErrorResolveCmd(true))
	return c
}

func newServiceErrorShowCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "show <project> <service> <fingerprint>",
		Short:   "Show one error group: occurrence histogram and newest raw lines",
		Args:    cobra.ExactArgs(3),
		Example: `  kuso service errors show tickero api 065dc963009d57e2 --since 7d`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			since, env, err := errorsWindow(args[0], args[1], serviceErrorsSince, serviceErrorsEnv, serviceErrorsSinceDeploy)
			if err != nil {
				return err
			}
			q := url.Values{}
			if since != "" {
				q.Set("since", since)
			}
			if env != "" {
				q.Set("env", env)
			}
			path := fmt.Sprintf("/api/projects/%s/services/%s/errors/%s", escPath(args[0]), escPath(args[1]), escPath(args[2]))
			if enc := q.Encode(); enc != "" {
				path += "?" + enc
			}
			resp, err := api.RawGet(path)
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("error group: %w", err)
			}
			var d struct {
				Fingerprint   string     `json:"fingerprint"`
				BucketSeconds int64      `json:"bucketSeconds"`
				Buckets       [][2]int64 `json:"buckets"`
				Occurrences   []struct {
					Ts      string `json:"ts"`
					Env     string `json:"env"`
					Pod     string `json:"pod"`
					RawLine string `json:"rawLine"`
				} `json:"occurrences"`
			}
			if err := json.Unmarshal(resp.Body(), &d); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			if outputFormat == "json" {
				return jsonOut(d)
			}
			var total int64
			for _, b := range d.Buckets {
				total += b[1]
			}
			fmt.Printf("fingerprint %s: %d occurrence(s) in %d bucket(s) of %ds\n", d.Fingerprint, total, len(d.Buckets), d.BucketSeconds)
			if len(d.Occurrences) == 0 {
				fmt.Println("no occurrences in the window")
				return nil
			}
			fmt.Println("newest occurrences:")
			for _, o := range d.Occurrences {
				fmt.Printf("  %s  %s  %s\n    %s\n", o.Ts, o.Env, o.Pod, o.RawLine)
			}
			return nil
		},
	}
	c.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	c.Flags().StringVar(&serviceErrorsSince, "since", "", "lookback window (e.g. 6h, 7d; max 30d, server default 24h)")
	c.Flags().BoolVar(&serviceErrorsSinceDeploy, "since-deploy", false, "only occurrences since the env's current deploy went live")
	c.Flags().StringVar(&serviceErrorsEnv, "env", "", "only this environment's occurrences")
	c.MarkFlagsMutuallyExclusive("since", "since-deploy")
	return c
}

// newServiceErrorResolveCmd mints `errors resolve` (POST) or, with reopen,
// `errors reopen` (DELETE). A resolved group is hidden from the list and
// the counts until a newer occurrence arrives.
func newServiceErrorResolveCmd(reopen bool) *cobra.Command {
	use, short := "resolve", "Mark an error group resolved (hidden until it recurs)"
	if reopen {
		use, short = "reopen", "Reopen a resolved error group"
	}
	return &cobra.Command{
		Use:   use + " <project> <service> <fingerprint>",
		Short: short,
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			path := fmt.Sprintf("/api/projects/%s/services/%s/errors/%s/resolve", escPath(args[0]), escPath(args[1]), escPath(args[2]))
			method := "POST"
			if reopen {
				method = "DELETE"
			}
			resp, err := api.Raw(method, path, nil, nil)
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("%s error group: %w", use, err)
			}
			if reopen {
				fmt.Printf("reopened %s on %s/%s\n", args[2], args[0], args[1])
			} else {
				fmt.Printf("resolved %s on %s/%s; it shows again if it recurs\n", args[2], args[0], args[1])
			}
			return nil
		},
	}
}

// errorGroupState mirrors the web's errorGroupState: open (never
// resolved), recurred (seen again after it was resolved) or resolved.
func errorGroupState(lastSeen, resolvedAt string) string {
	if resolvedAt == "" {
		return "open"
	}
	ls, err1 := time.Parse(time.RFC3339, lastSeen)
	ra, err2 := time.Parse(time.RFC3339, resolvedAt)
	if err1 == nil && err2 == nil && ls.After(ra) {
		return "recurred"
	}
	return "resolved"
}

// errorsWindow resolves the since/env query for the errors reads. With
// sinceDeploy it looks up when the env's current deploy went live and
// turns that into a whole-minute lookback (capped at 30d, like the web's
// sinceDeployWindow), scoping the query to that env.
func errorsWindow(project, service, since, env string, sinceDeploy bool) (string, string, error) {
	if !sinceDeploy {
		return since, env, nil
	}
	group := env
	if group == "" {
		group = "production"
	}
	resp, err := api.GetEnvironments(project)
	if err := checkRespErr(resp, err); err != nil {
		return "", "", fmt.Errorf("look up the deploy time: %w", err)
	}
	var envs []map[string]any
	if err := json.Unmarshal(resp.Body(), &envs); err != nil {
		return "", "", fmt.Errorf("decode environments: %w", err)
	}
	fq := project + "-" + stripPrefix(service, project+"-")
	for _, e := range envs {
		if asString(mapAt(e, "spec")["service"]) != fq || envGroupOf(e) != group {
			continue
		}
		at, ok := envDeployedAt(e)
		if !ok {
			return "", "", fmt.Errorf("%s/%s [%s] has no deploy time yet; use --since", project, service, group)
		}
		return sinceDeployWindow(at, time.Now()), group, nil
	}
	return "", "", fmt.Errorf("no %s environment for %s/%s", group, project, service)
}

const promotedAtAnnotation = "kuso.sislelabs.com/promoted-at"

// envDeployedAt is when the env's serving version went live: the newest
// of the promotion stamp (only once its rollout finished), lastDeployedAt
// and rolloutCompletedAt; mid failing rollout, when the previous version
// started serving. Mirrors lastDeployedAt in web/src/features/projects/facts.ts.
func envDeployedAt(e map[string]any) (time.Time, bool) {
	status := mapAt(e, "status")
	parse := func(s string) (time.Time, bool) {
		t, err := time.Parse(time.RFC3339, s)
		return t, err == nil
	}
	var best time.Time
	consider := func(s string) {
		if t, ok := parse(s); ok && t.After(best) {
			best = t
		}
	}
	completed, hasCompleted := parse(asString(status["rolloutCompletedAt"]))
	if ann, ok := mapAt(e, "metadata")["annotations"].(map[string]any); ok {
		if promoted, ok := parse(asString(ann[promotedAtAnnotation])); ok {
			// A promotion whose rollout never finished isn't serving.
			if hasCompleted && !completed.Before(promoted) {
				best = promoted
			} else if st := asString(status["state"]); st != "rollout_failing" && st != "stuck" && st != "deploying" {
				best = promoted
			}
		}
	}
	consider(asString(status["lastDeployedAt"]))
	consider(asString(status["rolloutCompletedAt"]))
	if best.IsZero() && asString(status["state"]) == "rollout_failing" {
		consider(asString(mapAt(status, "rollout")["previousSince"]))
	}
	return best, !best.IsZero()
}

// sinceDeployWindow is the errors API lookback from at to now: whole
// minutes rounded up, at least 1m, at most 30d.
func sinceDeployWindow(at, now time.Time) string {
	m := int(math.Ceil(now.Sub(at).Minutes()))
	m = max(1, min(m, 30*24*60))
	return fmt.Sprintf("%dm", m)
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
