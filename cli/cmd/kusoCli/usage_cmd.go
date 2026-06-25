package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso usage` — the read-only cost rollup the web /settings/usage page
// drives. Two views:
//
//   kuso usage                 cluster + per-node totals + 30-day projection
//   kuso usage --projects      per-project breakdown with % of cluster
//
// Both accept --days N (default server-side 30, max 365) and -o json.
// Cost figures only appear when the operator has configured cost rates
// (spec.cost.* on the Kuso CR); otherwise you see usage with no dollars.

var (
	usageDays     int
	usageProjects bool
)

var usageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Show cluster/per-node (or per-project) resource usage + cost rollup.",
	Long: `Show the resource-usage cost rollup over a recent window. By default
this is the cluster + per-node view with a next-30-days cost projection.
Pass --projects for the per-project breakdown (with each project's share
of the cluster). Cost figures require the operator to have configured
cost rates; without them you still see CPU/memory usage.`,
	Example: `  kuso usage
  kuso usage --days 7
  kuso usage --projects
  kuso usage --projects --days 90 -o json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if usageProjects {
			return runUsageProjects()
		}
		return runUsageCluster()
	},
}

func runUsageCluster() error {
	resp, err := api.Usage(usageDays)
	if err != nil {
		return err
	}
	if resp.StatusCode() >= 300 {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
	}
	if outputFormat == "json" {
		fmt.Println(string(resp.Body()))
		return nil
	}
	var out kusoApi.UsageResponse
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if len(out.Totals) == 0 {
		fmt.Printf("No usage samples yet (the sampler runs every ~5 min). Window: %d days.\n", out.Days)
		return nil
	}
	tw := tablewriter.NewWriter(os.Stdout)
	tw.SetHeader([]string{"Node", "CPU (core-h)", "Mem (GB-h)"})
	for _, t := range out.Totals {
		tw.Append([]string{
			t.Node,
			fmt.Sprintf("%.1f", float64(t.CPUMilliHours)/1000),
			fmt.Sprintf("%.1f", t.MemGBHours),
		})
	}
	tw.Render()
	fmt.Printf("\nWindow: %d days. Projected next 30 days: %.1f core-h, %.1f GB-h",
		out.Days,
		float64(out.Projected.CPUMilliHours)/1000,
		out.Projected.MemGBHours)
	if out.Projected.CostTotal > 0 {
		fmt.Printf(" ≈ %.2f %s", out.Projected.CostTotal, out.Rates.Currency)
	} else {
		fmt.Print(" (no cost rates configured)")
	}
	fmt.Println(".")
	return nil
}

func runUsageProjects() error {
	resp, err := api.ProjectUsage(usageDays)
	if err != nil {
		return err
	}
	if resp.StatusCode() >= 300 {
		return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
	}
	if outputFormat == "json" {
		fmt.Println(string(resp.Body()))
		return nil
	}
	var out kusoApi.ProjectUsageResponse
	if err := json.Unmarshal(resp.Body(), &out); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if len(out.Projects) == 0 {
		fmt.Printf("No per-project usage samples yet. Window: %d days.\n", out.Days)
		return nil
	}
	tw := tablewriter.NewWriter(os.Stdout)
	tw.SetHeader([]string{"Project", "CPU (core-h)", "Mem (GB-h)", "Cost", "% cluster"})
	for _, p := range out.Projects {
		cost := "—"
		if p.Cost > 0 {
			cost = fmt.Sprintf("%.2f %s", p.Cost, out.Rates.Currency)
		}
		tw.Append([]string{
			p.Project,
			fmt.Sprintf("%.1f", float64(p.CPUMilliHours)/1000),
			fmt.Sprintf("%.1f", p.MemGBHours),
			cost,
			fmt.Sprintf("%.1f%%", p.SharePct),
		})
	}
	tw.Render()
	fmt.Printf("\nWindow: %d days (projected). Cluster total: %.1f core-h, %.1f GB-h",
		out.Days,
		float64(out.ClusterTotal.CPUMilliHours)/1000,
		out.ClusterTotal.MemGBHours)
	if out.ClusterTotal.CostTotal > 0 {
		fmt.Printf(" ≈ %.2f %s", out.ClusterTotal.CostTotal, out.Rates.Currency)
	}
	fmt.Println(".")
	return nil
}

func init() {
	usageCmd.Flags().IntVar(&usageDays, "days", 0, "window in days (default 30 server-side, max 365)")
	usageCmd.Flags().BoolVar(&usageProjects, "projects", false, "show the per-project breakdown instead of per-node")
	usageCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	rootCmd.AddCommand(usageCmd)
}
