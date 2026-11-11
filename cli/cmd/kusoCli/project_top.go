package kusoCli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"sort"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

type projectTopRow struct {
	Project     string `json:"project"`
	Workload    string `json:"workload"`
	Pod         string `json:"pod"`
	Container   string `json:"container"`
	Node        string `json:"node"`
	Phase       string `json:"phase"`
	HasUsage    bool   `json:"hasUsage"`
	CPUm        int64  `json:"cpuMillicores"`
	CPURequestm int64  `json:"cpuRequestMillicores"`
	CPULimitm   int64  `json:"cpuLimitMillicores"`
	MemBytes    int64  `json:"memBytes"`
	MemRequest  int64  `json:"memRequestBytes"`
	MemLimit    int64  `json:"memLimitBytes"`
	Restarts    int32  `json:"restarts"`
}

var (
	projectTopOutput string
	projectTopSort   string
)

var projectTopCmd = &cobra.Command{
	Use:   "top [project]",
	Short: "Per-container CPU/memory used vs requests and limits",
	Long: `Show live CPU and memory use (metrics-server) next to each container's
requests and limits, for spotting over- and under-provisioned workloads.
Without a project, covers every project you can see.`,
	Example: `  kuso project top tickero
  kuso project top --sort cpu
  kuso project top tickero -o json`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		var projects []string
		if len(args) == 1 {
			projects = args
		} else {
			resp, err := api.GetProjects()
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("fetch projects: %w", err)
			}
			projects = namesFromList(resp.Body())
		}
		var rows []projectTopRow
		noMetrics := []string{}
		for _, p := range projects {
			resp, err := api.RawGet("/api/projects/" + url.PathEscape(p) + "/top")
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("top %s: %w", p, err)
			}
			var out struct {
				Metrics bool            `json:"metricsAvailable"`
				Rows    []projectTopRow `json:"rows"`
			}
			if err := json.Unmarshal(resp.Body(), &out); err != nil {
				return fmt.Errorf("decode %s: %w", p, err)
			}
			if !out.Metrics {
				noMetrics = append(noMetrics, p)
			}
			for _, r := range out.Rows {
				r.Project = p
				rows = append(rows, r)
			}
		}
		if err := sortProjectTop(rows, projectTopSort); err != nil {
			return err
		}
		switch projectTopOutput {
		case "json":
			if rows == nil {
				rows = []projectTopRow{}
			}
			return jsonOut(rows)
		case "table", "":
			renderProjectTop(os.Stdout, rows, len(args) == 0)
			if len(noMetrics) > 0 {
				fmt.Fprintf(os.Stderr, "metrics-server returned nothing for: %v (usage columns show -)\n", noMetrics)
			}
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", projectTopOutput)
		}
	},
}

func sortProjectTop(rows []projectTopRow, by string) error {
	var key func(r projectTopRow) int64
	switch by {
	case "mem", "":
		key = func(r projectTopRow) int64 { return r.MemBytes }
	case "cpu":
		key = func(r projectTopRow) int64 { return r.CPUm }
	case "mem-request":
		key = func(r projectTopRow) int64 { return r.MemRequest }
	case "cpu-request":
		key = func(r projectTopRow) int64 { return r.CPURequestm }
	case "restarts":
		key = func(r projectTopRow) int64 { return int64(r.Restarts) }
	default:
		return fmt.Errorf("unsupported --sort %q (mem, cpu, mem-request, cpu-request, restarts)", by)
	}
	sort.SliceStable(rows, func(i, j int) bool { return key(rows[i]) > key(rows[j]) })
	return nil
}

func renderProjectTop(w io.Writer, rows []projectTopRow, withProject bool) {
	if len(rows) == 0 {
		fmt.Fprintln(w, "No running pods.")
		return
	}
	cpu := func(m int64) string {
		if m == 0 {
			return "-"
		}
		return fmt.Sprintf("%dm", m)
	}
	mem := func(b int64) string {
		if b == 0 {
			return "-"
		}
		return humanBytes(b)
	}
	header := []string{"Workload", "Pod/Container", "Node", "CPU Used", "CPU Req", "CPU Lim", "Mem Used", "Mem Req", "Mem Lim", "Restarts"}
	if withProject {
		header = append([]string{"Project"}, header...)
	}
	tw := tablewriter.NewWriter(w)
	tw.SetHeader(header)
	for _, r := range rows {
		usedCPU, usedMem := "-", "-"
		if r.HasUsage {
			usedCPU, usedMem = fmt.Sprintf("%dm", r.CPUm), humanBytes(r.MemBytes)
		}
		line := []string{
			r.Workload, r.Pod + "/" + r.Container, r.Node,
			usedCPU, cpu(r.CPURequestm), cpu(r.CPULimitm),
			usedMem, mem(r.MemRequest), mem(r.MemLimit),
			fmt.Sprintf("%d", r.Restarts),
		}
		if withProject {
			line = append([]string{r.Project}, line...)
		}
		tw.Append(line)
	}
	tw.Render()
}

func init() {
	projectCmd.AddCommand(projectTopCmd)
	projectTopCmd.Flags().StringVarP(&projectTopOutput, "output", "o", "table", "output format [table, json]")
	projectTopCmd.Flags().StringVar(&projectTopSort, "sort", "mem", "sort by: mem, cpu, mem-request, cpu-request, restarts")
}
