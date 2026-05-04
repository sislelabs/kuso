package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso alert` — alert rules evaluated on a 1-minute ticker.
//
//   kuso alert list
//   kuso alert add-log-match  --name 'OOMKilled' --query OOMKilled --threshold 1 --window 5m
//   kuso alert add-node-pressure cpu --name 'CPU>90%' --threshold-pct 90 --severity error
//   kuso alert delete <id>
//   kuso alert enable <id> | disable <id>

var alertCmd = &cobra.Command{
	Use:     "alert",
	Aliases: []string{"alerts"},
	Short:   "Manage alert rules (log matches, node pressure)",
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
	Aliases: []string{"ls"},
	Short:   "List alert rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListAlerts()
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
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
			t.SetHeader([]string{"ID", "NAME", "KIND", "ENABLED", "PROJECT", "SERVICE", "DETAIL"})
			for _, r := range rules {
				detail := asString(r["query"])
				if r["thresholdInt"] != nil {
					detail = fmt.Sprintf("%s ≥ %v", detail, r["thresholdInt"])
				}
				if r["thresholdFloat"] != nil {
					detail = fmt.Sprintf("%v%%", r["thresholdFloat"])
				}
				t.Append([]string{
					asString(r["id"]),
					asString(r["name"]),
					asString(r["kind"]),
					fmt.Sprintf("%v", r["enabled"]),
					asString(r["project"]),
					asString(r["service"]),
					detail,
				})
			}
			t.Render()
			return nil
		}
	},
}

var alertAddLogMatchCmd = &cobra.Command{
	Use:   "add-log-match",
	Short: "Add a log-match alert rule",
	Example: `  kuso alert add-log-match --name 'OOMKilled' --query OOMKilled --threshold 1 --window 5m
  kuso alert add-log-match --name 'fatal errors' --project myproj --service api --query '"fatal error"' --threshold 5`,
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
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
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
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("alert %q created\n", alertAddName)
		return nil
	},
}

var alertDeleteCmd = &cobra.Command{
	Use:     "delete <id>",
	Aliases: []string{"rm"},
	Short:   "Delete an alert rule",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DeleteAlert(args[0])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
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
	var resp = func() (statusBody, error) {
		if on {
			r, err := api.EnableAlert(id)
			return statusBody{r.StatusCode(), r.Body()}, err
		}
		r, err := api.DisableAlert(id)
		return statusBody{r.StatusCode(), r.Body()}, err
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

	for _, sub := range []*cobra.Command{alertAddLogMatchCmd, alertAddNodePressureCmd} {
		sub.Flags().StringVar(&alertAddName, "name", "", "human-readable name")
		sub.Flags().StringVar(&alertAddProject, "project", "", "scope to one project (log-match only)")
		sub.Flags().StringVar(&alertAddService, "service", "", "scope to one service (log-match only)")
		sub.Flags().StringVar(&alertAddQuery, "query", "", "FTS5 MATCH expression (log-match only)")
		sub.Flags().Int64Var(&alertAddThresh, "threshold", 1, "match-count threshold (log-match only)")
		sub.Flags().Float64Var(&alertAddPct, "threshold-pct", 80, "percentage threshold (node-pressure only)")
		sub.Flags().StringVar(&alertAddWindow, "window", "5m", "evaluation window (5m, 300s, 300)")
		sub.Flags().StringVar(&alertAddSeverity, "severity", "warn", "info | warn | error")
		sub.Flags().StringVar(&alertAddThrottle, "throttle", "10m", "min interval between fires (10m, 600s, 600)")
		alertCmd.AddCommand(sub)
	}
	alertCmd.AddCommand(alertDeleteCmd)
	alertCmd.AddCommand(alertEnableCmd)
	alertCmd.AddCommand(alertDisableCmd)
}
