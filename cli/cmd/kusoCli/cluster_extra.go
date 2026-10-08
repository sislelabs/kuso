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

// Commands for API endpoints that had no CLI surface, which pushed
// agents to kubectl: kube events, node uncordon, project metrics.

var (
	getEventsNamespace string
	getEventsType      string
)

var getEventsCmd = &cobra.Command{
	Use:   "events",
	Short: "Show recent Kubernetes events (admin), newest first",
	Long: `Show the newest 200 Kubernetes events in a namespace (default: the kuso
namespace). Projects with their own namespace pass --namespace. --type
keeps only events of that type (Warning or Normal).`,
	Example: `  kuso get events
  kuso get events --type Warning
  kuso get events --namespace koreni -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "/api/kubernetes/events"
		if getEventsNamespace != "" {
			path += "?namespace=" + url.QueryEscape(getEventsNamespace)
		}
		resp, err := api.RawGet(path)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("events: %w", err)
		}
		var events []struct {
			Type           string `json:"type"`
			Reason         string `json:"reason"`
			Message        string `json:"message"`
			Count          int    `json:"count"`
			LastTimestamp  string `json:"lastTimestamp"`
			EventTime      string `json:"eventTime"`
			InvolvedObject struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
			} `json:"involvedObject"`
		}
		switch outputFormat {
		case "json":
			var raw []map[string]any
			if err := json.Unmarshal(resp.Body(), &raw); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			// Always an array: the server sends null for an empty
			// namespace, which breaks `jq '.[]'`.
			out := make([]map[string]any, 0, len(raw))
			for _, e := range raw {
				if t, _ := e["type"].(string); eventTypeMatches(t) {
					out = append(out, e)
				}
			}
			return jsonOut(out)
		case "table", "":
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
		if err := json.Unmarshal(resp.Body(), &events); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		kept := events[:0]
		for _, e := range events {
			if eventTypeMatches(e.Type) {
				kept = append(kept, e)
			}
		}
		events = kept
		if len(events) == 0 {
			fmt.Println("no events")
			return nil
		}
		t := tablewriter.NewWriter(os.Stdout)
		t.SetHeader([]string{"AGE", "TYPE", "REASON", "OBJECT", "COUNT", "MESSAGE"})
		t.SetAutoWrapText(false)
		for _, e := range events {
			ts := e.LastTimestamp
			if ts == "" {
				ts = e.EventTime
			}
			t.Append([]string{relativeAge(ts), e.Type, e.Reason,
				strings.ToLower(e.InvolvedObject.Kind) + "/" + e.InvolvedObject.Name,
				fmt.Sprintf("%d", e.Count), strings.TrimSpace(e.Message)})
		}
		t.Render()
		return nil
	},
}

func eventTypeMatches(t string) bool {
	return getEventsType == "" || strings.EqualFold(t, getEventsType)
}

var nodeUncordonCmd = &cobra.Command{
	Use:   "uncordon <name>",
	Short: "Make a node schedulable again (admin)",
	Long: `Mark a node schedulable and drop the nodewatch "cordoned by kuso"
marker, so the failure watcher no longer treats it as a node it cordoned.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := api.RawPost("/api/kubernetes/nodes/"+url.PathEscape(args[0])+"/uncordon", nil, "application/json")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("uncordon: %w", err)
		}
		fmt.Printf("node %s uncordoned\n", args[0])
		return nil
	},
}

var projectMetricsOutput string

var projectMetricsCmd = &cobra.Command{
	Use:   "metrics <project>",
	Short: "Show a project's current CPU, memory and pod count",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resp, err := api.RawGet("/api/projects/" + url.PathEscape(args[0]) + "/metrics")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("metrics: %w", err)
		}
		var m struct {
			Project       string `json:"project"`
			CPUMillicores int64  `json:"cpuMillicores"`
			MemBytes      int64  `json:"memBytes"`
			Pods          int    `json:"pods"`
			Envs          int    `json:"envs"`
		}
		if err := json.Unmarshal(resp.Body(), &m); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		switch projectMetricsOutput {
		case "json":
			return jsonOut(m)
		case "table", "":
			fmt.Printf("project %s: cpu %s, memory %s, pods %d, envs %d\n",
				m.Project, formatMilliCPU(m.CPUMillicores), humanBytes(m.MemBytes), m.Pods, m.Envs)
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", projectMetricsOutput)
		}
	},
}

func init() {
	getCmd.AddCommand(getEventsCmd)
	getEventsCmd.Flags().StringVar(&getEventsNamespace, "namespace", "", "namespace to read (default: the kuso namespace)")
	getEventsCmd.Flags().StringVar(&getEventsType, "type", "", "only events of this type: Warning or Normal")
	nodeCmd.AddCommand(nodeUncordonCmd)
	projectCmd.AddCommand(projectMetricsCmd)
	projectMetricsCmd.Flags().StringVarP(&projectMetricsOutput, "output", "o", "table", "output format [table, json]")
}
