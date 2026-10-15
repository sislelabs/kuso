// service_metrics.go — `kuso service metrics`: the service overlay's
// Metrics tab. Per-pod CPU/memory from metrics-server plus the
// request-rate / 5xx-ratio / p95 series from the in-cluster Prometheus.
// Wired onto both `kuso service` and `kuso project service`.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

type envPodMetrics struct {
	Env    string `json:"env"`
	Window string `json:"window,omitempty"`
	Pods   []struct {
		Pod           string `json:"pod"`
		Timestamp     string `json:"timestamp,omitempty"`
		CPUMillicores int64  `json:"cpuMillicores"`
		MemBytes      int64  `json:"memBytes"`
	} `json:"pods"`
}

type envTimeseries struct {
	Env    string                  `json:"env"`
	Range  string                  `json:"range"`
	Step   string                  `json:"step"`
	Series map[string][][2]float64 `json:"series"`
}

// seriesStats summarises one [t, v] series: last value, mean, max.
func seriesStats(points [][2]float64) (last, avg, max float64, ok bool) {
	if len(points) == 0 {
		return 0, 0, 0, false
	}
	var sum float64
	max = points[0][1]
	for _, p := range points {
		sum += p[1]
		if p[1] > max {
			max = p[1]
		}
	}
	return points[len(points)-1][1], sum / float64(len(points)), max, true
}

func newServiceMetricsCmd() *cobra.Command {
	var env, rangeStr, step, output string
	c := &cobra.Command{
		Use:   "metrics <project> <service>",
		Short: "Show a service's pod CPU/memory and request rate, error rate and p95 latency",
		Long: `Show per-pod CPU and memory for one environment of a service (from
metrics-server) and its traffic over --range: requests/s, 5xx error ratio
and p95 latency (from the in-cluster Prometheus, measured at Traefik).

Missing backends produce empty results, not errors: no pods means the env
is scaled to zero or metrics-server isn't installed; no traffic series
means nothing hit the env in the window.`,
		Example: `  kuso service metrics shop api
  kuso service metrics shop api --env staging --range 24h
  kuso service metrics shop api --range 7d --step 1h -o json`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if api == nil {
				return fmt.Errorf("not logged in; run 'kuso login' first")
			}
			if output != "table" && output != "json" && output != "" {
				return fmt.Errorf("unsupported output format %q", output)
			}
			envCR := envCRName(args[0], args[1], env)
			resp, err := api.EnvMetrics(envCR)
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("metrics for %s: %w", envCR, err)
			}
			var pods envPodMetrics
			if err := json.Unmarshal(resp.Body(), &pods); err != nil {
				return fmt.Errorf("decode metrics: %w", err)
			}
			resp, err = api.EnvTimeseries(envCR, rangeStr, step)
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("timeseries for %s: %w", envCR, err)
			}
			var ts envTimeseries
			if err := json.Unmarshal(resp.Body(), &ts); err != nil {
				return fmt.Errorf("decode timeseries: %w", err)
			}
			if output == "json" {
				return jsonOut(map[string]any{"metrics": pods, "timeseries": ts})
			}

			fmt.Printf("env %s\n", envCR)
			if len(pods.Pods) == 0 {
				fmt.Println("no pod metrics (scaled to zero, or metrics-server has no sample yet)")
			} else {
				t := tablewriter.NewWriter(os.Stdout)
				t.SetHeader([]string{"POD", "CPU", "MEMORY"})
				var cpu, mem int64
				for _, p := range pods.Pods {
					cpu += p.CPUMillicores
					mem += p.MemBytes
					t.Append([]string{p.Pod, formatMilliCPU(p.CPUMillicores), humanBytes(p.MemBytes)})
				}
				if len(pods.Pods) > 1 {
					t.SetFooter([]string{"total", formatMilliCPU(cpu), humanBytes(mem)})
				}
				t.Render()
			}

			fmt.Printf("\ntraffic over %s (step %s)\n", ts.Range, ts.Step)
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"METRIC", "LAST", "AVG", "MAX"})
			for _, m := range []struct{ key, label, unit string }{
				{"requests", "requests/s", ""},
				{"errors", "5xx ratio", "%"},
				{"p95ms", "p95 latency", "ms"},
			} {
				last, avg, max, ok := seriesStats(ts.Series[m.key])
				if !ok {
					t.Append([]string{m.label, "-", "-", "-"})
					continue
				}
				f := func(v float64) string {
					switch m.unit {
					case "%":
						return fmt.Sprintf("%.2f%%", v*100)
					case "ms":
						return fmt.Sprintf("%.0fms", v)
					default:
						return fmt.Sprintf("%.2f", v)
					}
				}
				t.Append([]string{m.label, f(last), f(avg), f(max)})
			}
			t.Render()
			return nil
		},
	}
	c.Flags().StringVar(&env, "env", "production", "environment (production, staging, preview-pr-N)")
	c.Flags().StringVar(&rangeStr, "range", "1h", "traffic window: 15m, 1h, 24h, 7d (max 30d)")
	c.Flags().StringVar(&step, "step", "", "traffic resolution, e.g. 30s, 5m (default: picked from --range)")
	c.Flags().StringVarP(&output, "output", "o", "table", "output format [table, json]")
	return c
}

func init() {
	projectServiceCmd.AddCommand(newServiceMetricsCmd())
	serviceCmd.AddCommand(newServiceMetricsCmd())
}
