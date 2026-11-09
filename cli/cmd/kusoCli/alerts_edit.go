package kusoCli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso alert edit <id>` — change a rule in place (PUT /api/alerts/{id}).
// The server replaces the whole definition, so the current rule is read
// first and only the flags passed are changed. The kind can't change:
// delete and recreate for that.

var alertEdit alertCreateOpts

func applyAlertEdits(req *kusoApi.CreateAlertRequest, o alertCreateOpts, changed func(string) bool) error {
	if changed("name") {
		req.Name = o.name
	}
	if changed("project") {
		req.Project = o.project
	}
	if changed("service") {
		req.Service = o.service
	}
	if changed("env") {
		req.Env = o.env
	}
	if changed("query") {
		req.Query = o.query
	}
	if changed("severity") {
		req.Severity = o.severity
	}
	var err error
	if changed("window") {
		if req.WindowSeconds, err = parseAlertDuration(o.window); err != nil {
			return err
		}
	}
	if changed("throttle") {
		if req.ThrottleSeconds, err = parseAlertDuration(o.throttle); err != nil {
			return err
		}
	}
	httpKind := req.Kind == "http_5xx_rate" || req.Kind == "http_p95_latency"
	if changed("threshold") {
		switch req.Kind {
		case "node_cpu", "node_mem", "node_disk", "http_5xx_rate", "http_p95_latency":
			v := o.threshold
			req.ThresholdFloat = &v
		case "log_match", "cert_expiry", "crashloop", "healthcheck_failing":
			if o.threshold != float64(int64(o.threshold)) {
				return fmt.Errorf("%s --threshold must be a whole number", req.Kind)
			}
			n := int64(o.threshold)
			req.ThresholdInt = &n
		default:
			return fmt.Errorf("%s takes no --threshold", req.Kind)
		}
	}
	if changed("min-requests") {
		if !httpKind {
			return fmt.Errorf("--min-requests only applies to http_5xx_rate and http_p95_latency")
		}
		n := o.minRequests
		req.ThresholdInt = &n
	}
	return nil
}

var alertEditCmd = &cobra.Command{
	Use:   "edit <id>",
	Short: "Change an alert rule's name, scope, threshold, window or severity",
	Long: "Change an existing alert rule in place; flags you don't pass keep their\n" +
		"current value. --threshold is in the rule kind's unit (see 'kuso alert create --help').\n" +
		"Pass an empty value (--env '') to clear a scope. The kind can't be changed.",
	Example: `  kuso alert edit 3f9c1d2e --threshold 10 --window 10m
  kuso alert edit 3f9c1d2e --env staging --severity error`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if cmd.Flags().NFlag() == 0 {
			return fmt.Errorf("nothing to change: pass at least one flag")
		}
		resp, err := api.ListAlerts()
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		var rules []kusoApi.CreateAlertRequest
		var ids []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(resp.Body(), &rules); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		_ = json.Unmarshal(resp.Body(), &ids)
		idx := -1
		for i := range ids {
			if ids[i].ID == args[0] {
				idx = i
			}
		}
		if idx < 0 {
			return fmt.Errorf("alert %s not found (kuso alert list)", args[0])
		}
		req := rules[idx]
		if err := applyAlertEdits(&req, alertEdit, cmd.Flags().Changed); err != nil {
			return err
		}
		resp, err = api.UpdateAlert(args[0], req)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("update alert: %w", err)
		}
		fmt.Printf("alert %s updated\n", args[0])
		return nil
	},
}

func init() {
	f := alertEditCmd.Flags()
	f.StringVar(&alertEdit.name, "name", "", "human-readable name")
	f.StringVar(&alertEdit.project, "project", "", "scope to one project ('' clears)")
	f.StringVar(&alertEdit.service, "service", "", "scope to one service ('' clears)")
	f.StringVar(&alertEdit.env, "env", "", "scope to one env ('' clears)")
	f.StringVar(&alertEdit.query, "query", "", "log_match: case-insensitive substring")
	f.Float64Var(&alertEdit.threshold, "threshold", 0, "threshold in the kind's unit")
	f.Int64Var(&alertEdit.minRequests, "min-requests", 0, "http_*: ignore envs with fewer requests in the window")
	f.StringVar(&alertEdit.window, "window", "", "evaluation window (5m, 300s, 300)")
	f.StringVar(&alertEdit.severity, "severity", "", "info | warn | error")
	f.StringVar(&alertEdit.throttle, "throttle", "", "min gap between fires (10m, 600s, 600)")
	alertCmd.AddCommand(alertEditCmd)
}
