package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso alert` — alert rules evaluated on a 1-minute ticker.
//
//   kuso alert list
//   kuso alert create --kind http_5xx_rate --project p --service s --threshold 5 --window 5m
//   kuso alert add-log-match  --name 'OOMKilled' --query OOMKilled --threshold 1 --window 5m
//   kuso alert add-node-pressure cpu --name 'CPU>90%' --threshold-pct 90 --severity error
//   kuso alert delete <id>
//   kuso alert enable <id> | disable <id>

var alertCmd = &cobra.Command{
	Use:     "alert",
	Aliases: []string{"alerts"},
	Short:   "Manage alert rules (log matches, node pressure, HTTP errors/latency, certs, DNS)",
}

var (
	alertAddName     string
	alertAddProject  string
	alertAddService  string
	alertAddQuery    string
	alertAddThresh   int64
	alertAddPct      float64
	alertAddWindow   string
	alertAddSeverity string
	alertAddThrottle string
)

var alertListCmd = &cobra.Command{
	Use:     "list",
	Args:    cobra.NoArgs,
	Aliases: []string{"ls"},
	Short:   "List alert rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListAlerts()
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		var rules []map[string]any
		if err := json.Unmarshal(resp.Body(), &rules); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(rules)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "NAME", "KIND", "ENABLED", "PROJECT", "SERVICE", "ENV", "DETAIL", "FIRING"})
			for _, r := range rules {
				firing := ""
				if r["firingSince"] != nil {
					firing = "since " + asString(r["firingSince"])
				}
				t.Append([]string{
					asString(r["id"]),
					asString(r["name"]),
					asString(r["kind"]),
					fmt.Sprintf("%v", r["enabled"]),
					asString(r["project"]),
					asString(r["service"]),
					asString(r["env"]),
					alertDetail(r),
					firing,
				})
			}
			t.Render()
			return nil
		}
	},
}

// alertDetail renders a rule's threshold in the unit its kind uses.
func alertDetail(r map[string]any) string {
	ti, tf := r["thresholdInt"], r["thresholdFloat"]
	switch asString(r["kind"]) {
	case "log_match":
		return fmt.Sprintf("%s ≥ %v", asString(r["query"]), ti)
	case "http_5xx_rate":
		return fmt.Sprintf("5xx ≥ %v%% (min %v req)", tf, ti)
	case "http_p95_latency":
		return fmt.Sprintf("p95 ≥ %vms (min %v req)", tf, ti)
	case "cert_expiry":
		return fmt.Sprintf("expires < %vd or not Ready", ti)
	case "dns_mismatch":
		return "resolves off-cluster"
	}
	if tf != nil {
		return fmt.Sprintf("%v%%", tf)
	}
	return ""
}

// alertCreateOpts is the flag set of `kuso alert create`; *Set records
// whether the flag was passed so unset thresholds fall back to the
// server's per-kind defaults.
type alertCreateOpts struct {
	kind, name, project, service, env, query string
	threshold                                float64
	thresholdSet                             bool
	minRequests                              int64
	minRequestsSet                           bool
	window, throttle, severity               string
}

var alertCreate alertCreateOpts

// buildAlertCreateRequest maps the generic --threshold onto the field
// each kind stores: percent/ms → thresholdFloat, matches/days →
// thresholdInt. --min-requests is the http kinds' noise floor.
func buildAlertCreateRequest(o alertCreateOpts) (kusoApi.CreateAlertRequest, error) {
	req := kusoApi.CreateAlertRequest{
		Kind: o.kind, Name: o.name, Project: o.project, Service: o.service, Env: o.env,
		Query: o.query, Severity: o.severity,
	}
	if o.kind == "" {
		return req, fmt.Errorf("--kind is required")
	}
	httpKind := o.kind == "http_5xx_rate" || o.kind == "http_p95_latency"
	envKind := httpKind || o.kind == "cert_expiry" || o.kind == "dns_mismatch"
	switch o.kind {
	case "log_match":
		if o.query == "" {
			return req, fmt.Errorf("--query is required for log_match")
		}
		if o.thresholdSet {
			n := int64(o.threshold)
			req.ThresholdInt = &n
		}
	case "node_cpu", "node_mem", "node_disk", "http_5xx_rate", "http_p95_latency":
		if o.thresholdSet {
			v := o.threshold
			req.ThresholdFloat = &v
		}
	case "cert_expiry":
		if o.thresholdSet {
			if o.threshold != float64(int64(o.threshold)) {
				return req, fmt.Errorf("cert_expiry --threshold is days and must be a whole number")
			}
			n := int64(o.threshold)
			req.ThresholdInt = &n
		}
	case "dns_mismatch":
		if o.thresholdSet {
			return req, fmt.Errorf("dns_mismatch takes no --threshold")
		}
	default:
		return req, fmt.Errorf("--kind must be one of http_5xx_rate|http_p95_latency|cert_expiry|dns_mismatch|log_match|node_cpu|node_mem|node_disk")
	}
	if o.minRequestsSet {
		if !httpKind {
			return req, fmt.Errorf("--min-requests only applies to http_5xx_rate and http_p95_latency")
		}
		n := o.minRequests
		req.ThresholdInt = &n
	}
	if o.env != "" && !envKind {
		return req, fmt.Errorf("--env only applies to http_5xx_rate|http_p95_latency|cert_expiry|dns_mismatch")
	}
	var err error
	if req.WindowSeconds, err = parseAlertDuration(o.window); err != nil {
		return req, err
	}
	if req.ThrottleSeconds, err = parseAlertDuration(o.throttle); err != nil {
		return req, err
	}
	if req.Name == "" {
		req.Name = o.kind
		if scope := strings.Trim(o.project+"/"+o.service, "/"); scope != "" {
			req.Name += " · " + scope
		}
		if o.env != "" {
			req.Name += " → " + o.env
		}
	}
	return req, nil
}

var alertCreateCmd = &cobra.Command{
	Use:   "create",
	Args:  cobra.NoArgs,
	Short: "Create an alert rule of any kind",
	Long: "Create an alert rule. --threshold means, per kind:\n" +
		"  http_5xx_rate     percent of requests answering 5xx (default 5)\n" +
		"  http_p95_latency  p95 latency in ms (default 1000)\n" +
		"  cert_expiry       days before expiry (default 14); also fires on a not-Ready Certificate\n" +
		"  dns_mismatch      none — fires when an env host doesn't resolve to the cluster\n" +
		"  log_match         match count; node_cpu|node_mem|node_disk  percent\n\n" +
		"http_*, cert_expiry and dns_mismatch fire once per episode and send a\n" +
		"resolved notification when the condition clears; --throttle is the\n" +
		"minimum gap between two fires (flap damping).",
	Example: `  kuso alert create --kind http_5xx_rate --project shop --service web --threshold 5 --window 5m
  kuso alert create --kind http_p95_latency --project shop --service api --env production --threshold 800 --min-requests 50
  kuso alert create --kind cert_expiry --threshold 21 --severity error
  kuso alert create --kind dns_mismatch --project shop`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		o := alertCreate
		o.thresholdSet = cmd.Flags().Changed("threshold")
		o.minRequestsSet = cmd.Flags().Changed("min-requests")
		req, err := buildAlertCreateRequest(o)
		if err != nil {
			return err
		}
		resp, err := api.CreateAlert(req)
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		var created map[string]any
		if json.Unmarshal(resp.Body(), &created) == nil && created["id"] != nil {
			fmt.Printf("alert %q created (id %s)\n", req.Name, asString(created["id"]))
			return nil
		}
		fmt.Printf("alert %q created\n", req.Name)
		return nil
	},
}

var alertAddLogMatchCmd = &cobra.Command{
	Use:   "add-log-match",
	Args:  cobra.NoArgs,
	Short: "Add a log-match alert rule",
	Example: `  kuso alert add-log-match --name 'OOMKilled' --query OOMKilled --threshold 1 --window 5m
  kuso alert add-log-match --name 'fatal errors' --project myproj --service api --query 'fatal error' --threshold 5`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if alertAddName == "" || alertAddQuery == "" {
			return fmt.Errorf("--name and --query are required")
		}
		windowSec, err := parseAlertDuration(alertAddWindow)
		if err != nil {
			return err
		}
		throttleSec, err := parseAlertDuration(alertAddThrottle)
		if err != nil {
			return err
		}
		threshold := alertAddThresh
		req := kusoApi.CreateAlertRequest{
			Name:            alertAddName,
			Kind:            "log_match",
			Project:         alertAddProject,
			Service:         alertAddService,
			Query:           alertAddQuery,
			ThresholdInt:    &threshold,
			WindowSeconds:   windowSec,
			Severity:        alertAddSeverity,
			ThrottleSeconds: throttleSec,
		}
		resp, err := api.CreateAlert(req)
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		fmt.Printf("alert %q created\n", alertAddName)
		return nil
	},
}

var alertAddNodePressureCmd = &cobra.Command{
	Use:   "add-node-pressure <kind>",
	Short: "Add a node pressure alert (kind = cpu | mem | disk)",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso alert add-node-pressure cpu --name 'CPU>90%' --threshold-pct 90 --severity error
  kuso alert add-node-pressure disk --name 'Disk>85%' --threshold-pct 85`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		var kind string
		switch args[0] {
		case "cpu":
			kind = "node_cpu"
		case "mem", "memory":
			kind = "node_mem"
		case "disk":
			kind = "node_disk"
		default:
			return fmt.Errorf("kind must be cpu|mem|disk")
		}
		if alertAddName == "" {
			return fmt.Errorf("--name is required")
		}
		windowSec, err := parseAlertDuration(alertAddWindow)
		if err != nil {
			return err
		}
		throttleSec, err := parseAlertDuration(alertAddThrottle)
		if err != nil {
			return err
		}
		pct := alertAddPct
		req := kusoApi.CreateAlertRequest{
			Name:            alertAddName,
			Kind:            kind,
			ThresholdFloat:  &pct,
			WindowSeconds:   windowSec,
			Severity:        alertAddSeverity,
			ThrottleSeconds: throttleSec,
		}
		resp, err := api.CreateAlert(req)
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		fmt.Printf("alert %q created\n", alertAddName)
		return nil
	},
}

var alertDeleteYes bool

var alertDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"rm"},
	Short:   "Delete an alert rule",
	Long: "Delete an alert rule.\n\n" +
		"Its condition stops being watched immediately and the rule is not\n" +
		"recoverable — recreate it with 'kuso alert add-log-match' or\n" +
		"'add-node-pressure'. To silence a rule temporarily, use\n" +
		"'kuso alert disable' instead.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(alertDeleteYes,
			fmt.Sprintf("Delete alert rule %s? Its condition stops being watched immediately and the rule is not recoverable.", args[0])); err != nil {
			return err
		}
		resp, err := api.DeleteAlert(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		fmt.Printf("alert %s deleted\n", args[0])
		return nil
	},
}

var alertEnableCmd = &cobra.Command{
	Use:   "enable <id>",
	Short: "Enable an alert rule",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return alertToggle(args[0], true) },
}

var alertDisableCmd = &cobra.Command{
	Use:   "disable <id>",
	Short: "Disable an alert rule",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return alertToggle(args[0], false) },
}

func alertToggle(id string, on bool) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	// resp() must check err BEFORE dereferencing r — when resty's
	// transport fails (TCP refused, DNS, TLS), it returns (nil, err)
	// and r.StatusCode() panics with a nil pointer. The previous code
	// always dereferenced r on the same line as `return`, which made
	// `kuso alert {enable,disable}` panic with a Go stack trace
	// instead of printing "connection refused" cleanly.
	var resp = func() (statusBody, error) {
		var r *resty.Response
		var err error
		if on {
			r, err = api.EnableAlert(id)
		} else {
			r, err = api.DisableAlert(id)
		}
		if err != nil {
			return statusBody{}, err
		}
		return statusBody{r.StatusCode(), r.Body()}, nil
	}
	r, err := resp()
	if err != nil {
		return err
	}
	if r.code >= 300 {
		return fmt.Errorf("server returned %d: %s", r.code, string(r.body))
	}
	state := "enabled"
	if !on {
		state = "disabled"
	}
	fmt.Printf("alert %s %s\n", id, state)
	return nil
}

type statusBody struct {
	code int
	body []byte
}

// parseAlertDuration accepts "5m" / "300s" / "300" → seconds. Empty
// returns 0 → server uses default.
func parseAlertDuration(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	if d, err := time.ParseDuration(s); err == nil {
		return int(d.Seconds()), nil
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil && n > 0 {
		return n, nil
	}
	return 0, fmt.Errorf("could not parse duration %q (try 5m, 300s, or 300)", s)
}

func init() {
	rootCmd.AddCommand(alertCmd)
	alertCmd.AddCommand(alertListCmd)
	alertListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")

	// Flags shared by BOTH add- subcommands.
	for _, sub := range []*cobra.Command{alertAddLogMatchCmd, alertAddNodePressureCmd} {
		sub.Flags().StringVar(&alertAddName, "name", "", "human-readable name")
		sub.Flags().StringVar(&alertAddWindow, "window", "5m", "evaluation window (5m, 300s, 300)")
		sub.Flags().StringVar(&alertAddSeverity, "severity", "warn", "info | warn | error")
		sub.Flags().StringVar(&alertAddThrottle, "throttle", "10m", "min interval between fires (10m, 600s, 600)")
		alertCmd.AddCommand(sub)
	}
	// Log-match-only flags. These used to be registered on BOTH commands
	// with a "(log-match only)" note in the description — but
	// add-node-pressure's RunE never reads them, so
	// `alert add-node-pressure cpu --project myproj` was accepted
	// silently and created a CLUSTER-WIDE alert. Registering them only
	// where they work turns that into an "unknown flag" error.
	alertAddLogMatchCmd.Flags().StringVar(&alertAddProject, "project", "", "scope to one project")
	alertAddLogMatchCmd.Flags().StringVar(&alertAddService, "service", "", "scope to one service")
	alertAddLogMatchCmd.Flags().StringVar(&alertAddQuery, "query", "", "case-insensitive substring to match in log lines (not FTS — no quoting/AND/OR/prefix)")
	alertAddLogMatchCmd.Flags().Int64Var(&alertAddThresh, "threshold", 1, "match-count threshold")
	// Node-pressure-only.
	alertAddNodePressureCmd.Flags().Float64Var(&alertAddPct, "threshold-pct", 80, "percentage threshold")
	cf := alertCreateCmd.Flags()
	cf.StringVar(&alertCreate.kind, "kind", "", "http_5xx_rate | http_p95_latency | cert_expiry | dns_mismatch | log_match | node_cpu | node_mem | node_disk")
	cf.StringVar(&alertCreate.name, "name", "", "human-readable name (default: kind + scope)")
	cf.StringVar(&alertCreate.project, "project", "", "scope to one project")
	cf.StringVar(&alertCreate.service, "service", "", "scope to one service (needs --project)")
	cf.StringVar(&alertCreate.env, "env", "", "scope to one env, e.g. production (needs --project)")
	cf.StringVar(&alertCreate.query, "query", "", "log_match: case-insensitive substring")
	cf.Float64Var(&alertCreate.threshold, "threshold", 0, "threshold in the kind's unit (see --help)")
	cf.Int64Var(&alertCreate.minRequests, "min-requests", 20, "http_*: ignore envs with fewer requests in the window")
	cf.StringVar(&alertCreate.window, "window", "5m", "evaluation window (5m, 300s, 300)")
	cf.StringVar(&alertCreate.severity, "severity", "warn", "info | warn | error")
	cf.StringVar(&alertCreate.throttle, "throttle", "10m", "min gap between fires (10m, 600s, 600)")
	alertCmd.AddCommand(alertCreateCmd)
	alertDeleteCmd.Flags().BoolVarP(&alertDeleteYes, "yes", "y", false, "skip the confirmation prompt")
	alertCmd.AddCommand(alertDeleteCmd)
	alertCmd.AddCommand(alertEnableCmd)
	alertCmd.AddCommand(alertDisableCmd)
}
