package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// getCmd is the agent-friendly read entrypoint. Unlike `kuso app` which
// uses interactive prompts to fill in pipeline/stage/app, `kuso get`
// works in flag-driven, non-interactive mode and supports -o json for
// machine consumption.
//
// The subcommand surface is intentionally narrow (`apps`, `pipelines`)
// so the JSON shape is stable. Add new subcommands as new resources
// become first-class, not by overloading the existing ones.
var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Read kuso resources non-interactively",
	Long: `Read kuso resources non-interactively. Supports -o json|table for
machine and human consumption respectively. Designed to be safe to call
from scripts, CI, and AI agents.`,
}

var (
	getPipelineFilter string
	getPhaseFilter    string
)

var getAppsCmd = &cobra.Command{
	Use:     "apps",
	Aliases: []string{"app"},
	Short:   "List apps the caller has access to",
	Example: `  kuso get apps
  kuso get apps -o json
  kuso get apps --pipeline analiz --phase production -o json | jq '.[] | .name'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetApps()
		if err != nil {
			return fmt.Errorf("fetch apps: %w", err)
		}

		var apps []map[string]any
		if err := json.Unmarshal(resp.Body(), &apps); err != nil {
			return fmt.Errorf("decode apps response: %w", err)
		}

		filtered := apps[:0]
		for _, a := range apps {
			if getPipelineFilter != "" && asString(a["pipeline"]) != getPipelineFilter {
				continue
			}
			if getPhaseFilter != "" && asString(a["phase"]) != getPhaseFilter {
				continue
			}
			filtered = append(filtered, a)
		}
		sort.Slice(filtered, func(i, j int) bool {
			pi, pj := asString(filtered[i]["pipeline"]), asString(filtered[j]["pipeline"])
			if pi != pj {
				return pi < pj
			}
			phi, phj := asString(filtered[i]["phase"]), asString(filtered[j]["phase"])
			if phi != phj {
				return phi < phj
			}
			return asString(filtered[i]["name"]) < asString(filtered[j]["name"])
		})

		switch outputFormat {
		case "json":
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(filtered)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"PIPELINE", "PHASE", "APP", "SLEEP", "BRANCH"})
			for _, a := range filtered {
				t.Append([]string{
					asString(a["pipeline"]),
					asString(a["phase"]),
					asString(a["name"]),
					asString(a["sleep"]),
					asString(a["branch"]),
				})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q (want: table, json)", outputFormat)
		}
	},
}

var getPipelinesCmd = &cobra.Command{
	Use:     "pipelines",
	Aliases: []string{"pipeline"},
	Short:   "List pipelines the caller has access to",
	Example: `  kuso get pipelines
  kuso get pipelines -o json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetPipelines()
		if err != nil {
			return fmt.Errorf("fetch pipelines: %w", err)
		}

		var pipelines []map[string]any
		if err := json.Unmarshal(resp.Body(), &pipelines); err != nil {
			return fmt.Errorf("decode pipelines response: %w", err)
		}
		sort.Slice(pipelines, func(i, j int) bool {
			return asString(pipelines[i]["name"]) < asString(pipelines[j]["name"])
		})

		switch outputFormat {
		case "json":
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(pipelines)
		case "table", "":
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "PHASES"})
			for _, p := range pipelines {
				phases := ""
				if list, ok := p["phases"].([]any); ok {
					for i, ph := range list {
						if i > 0 {
							phases += ","
						}
						if m, ok := ph.(map[string]any); ok {
							phases += asString(m["name"])
						}
					}
				}
				t.Append([]string{asString(p["name"]), phases})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q (want: table, json)", outputFormat)
		}
	},
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func init() {
	rootCmd.AddCommand(getCmd)
	getCmd.AddCommand(getAppsCmd)
	getCmd.AddCommand(getPipelinesCmd)

	getCmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	getAppsCmd.Flags().StringVar(&getPipelineFilter, "pipeline", "", "if set, only show apps in this pipeline")
	getAppsCmd.Flags().StringVar(&getPhaseFilter, "phase", "", "if set, only show apps in this phase (production, staging, ...)")
}
