// kuso uptime — per-minute reachability checks on production web
// services: read the current state, and opt a project or one service
// in or out.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var uptimeCmd = &cobra.Command{
	Use:   "uptime",
	Short: "Uptime checks for a project's production services",
}

var uptimeStatusCmd = &cobra.Command{
	Use:   "status <project>",
	Short: "Show the uptime state of every service in a project",
	Long: `Show the latest uptime check for each production web service.

STATE is one of up, failing, down, paused, disabled or pending. DETAIL
carries the last error, or why a service is failing (confirming,
crash-looping, cooldown) or paused (stopped, asleep, no-image,
scaled-to-zero, no-deployment).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetProjectUptime(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("uptime status: %w", err)
		}
		var st kusoApi.UptimeStatus
		if err := json.Unmarshal(resp.Body(), &st); err != nil {
			return fmt.Errorf("decode uptime status: %w", err)
		}
		if outputFormat == "json" {
			if st.Services == nil {
				st.Services = []kusoApi.UptimeServiceStatus{}
			}
			return jsonOut(st)
		}
		if !st.Enabled {
			fmt.Fprintln(os.Stderr, "note: uptime checks are switched off on this instance (KUSO_UPTIME_DISABLED)")
		}
		if len(st.Services) == 0 {
			fmt.Println("no services with uptime checks")
			return nil
		}
		t := tablewriter.NewWriter(os.Stdout)
		t.SetHeader([]string{"SERVICE", "STATE", "SINCE", "LATENCY", "LAST CHECK", "DETAIL"})
		for _, s := range st.Services {
			t.Append(uptimeRow(s))
		}
		t.Render()
		return nil
	},
}

func uptimeRow(s kusoApi.UptimeServiceStatus) []string {
	// Latency is only meaningful for a check that got an HTTP answer;
	// the server omits it otherwise, which decodes to 0.
	latency := "-"
	if s.StatusCode != 0 {
		latency = strconv.FormatInt(s.LatencyMs, 10) + "ms"
	}
	detail := s.Error
	if detail == "" {
		detail = s.Reason
	}
	if detail == "" {
		detail = "-"
	}
	return []string{s.Service, s.State, relativeAge(s.Since), latency, relativeAge(s.LastCheckedAt), detail}
}

func setUptimeDisabled(args []string, disabled bool) error {
	if api == nil {
		return fmt.Errorf("not logged in; run 'kuso login' first")
	}
	verb := "enabled"
	if disabled {
		verb = "disabled"
	}
	patch := kusoApi.UptimePatch{Disabled: kusoApi.BoolPtr(disabled)}
	if len(args) == 1 {
		resp, err := api.PatchProjectUptime(args[0], patch)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("update project: %w", err)
		}
		fmt.Printf("project %s uptime checks %s\n", args[0], verb)
		return nil
	}
	resp, err := api.PatchService(args[0], args[1], kusoApi.PatchServiceRequest{Uptime: &patch})
	if err := checkRespErr(resp, err); err != nil {
		return fmt.Errorf("update service: %w", err)
	}
	fmt.Printf("service %s/%s uptime checks %s\n", args[0], args[1], verb)
	return nil
}

var uptimeDisableCmd = &cobra.Command{
	Use:   "disable <project> [service]",
	Short: "Stop uptime checks for a project, or for one service",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setUptimeDisabled(args, true)
	},
}

var uptimeEnableCmd = &cobra.Command{
	Use:   "enable <project> [service]",
	Short: "Resume uptime checks for a project, or for one service",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return setUptimeDisabled(args, false)
	},
}

var uptimeSetPathCmd = &cobra.Command{
	Use:   "set-path <project> <service> <path>",
	Short: "Set the path the uptime check requests on a service",
	Long: `Set the HTTP path the uptime check requests, e.g. /health. Pass an
empty string to clear it and go back to the default path.`,
	Example: `  kuso uptime set-path shop web /health
  kuso uptime set-path shop web ""`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		path := args[2]
		if path != "" && !strings.HasPrefix(path, "/") {
			return fmt.Errorf("path must start with / (got %q)", path)
		}
		req := kusoApi.PatchServiceRequest{Uptime: &kusoApi.UptimePatch{Path: &path}}
		resp, err := api.PatchService(args[0], args[1], req)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("update service: %w", err)
		}
		if path == "" {
			fmt.Printf("service %s/%s uptime path cleared\n", args[0], args[1])
			return nil
		}
		fmt.Printf("service %s/%s uptime path set to %s\n", args[0], args[1], path)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(uptimeCmd)
	uptimeCmd.AddCommand(uptimeStatusCmd, uptimeDisableCmd, uptimeEnableCmd, uptimeSetPathCmd)
	uptimeStatusCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
}
