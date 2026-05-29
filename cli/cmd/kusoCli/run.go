// `kuso run` — fire a one-shot task pod against a service's
// most-recent succeeded build image. Closes the kubectl-exec gap
// for migrations, seeds, one-off scripts.
//
//   kuso run <project> <service> -- <command…>
//   kuso run alpha web -- python manage.py migrate
//   kuso run alpha web --env FOO=bar -- ./scripts/seed.sh
//
// The trailing args (after --) are the argv for the run container.
// We avoid shell expansion on the CLI side; users who need a shell
// pass `sh -c "your command"` explicitly.

package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"kuso/pkg/kusoApi"

	"github.com/spf13/cobra"
)

var (
	runTimeoutSeconds int
	runEnvFlags       []string
	runFollow         bool
)

var runCmd = &cobra.Command{
	Use:   "run <project> <service> -- <command…>",
	Short: "Run a one-shot task in a service's image (migrations, seeds, scripts)",
	// We require at least project + service + one command arg. The
	// `--` separator is conventionally where command args start;
	// cobra stops parsing flags after it.
	Args: cobra.MinimumNArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		project, service := args[0], args[1]
		command := args[2:]
		envVars, err := parseRunEnvFlags(runEnvFlags)
		if err != nil {
			return err
		}
		req := kusoApi.CreateRunRequest{
			Command:        command,
			Env:            envVars,
			TimeoutSeconds: runTimeoutSeconds,
		}
		resp, err := api.CreateRun(project, service, req)
		if err != nil {
			return fmt.Errorf("create run: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		// Server returns the full KusoRun CR; pluck name + phase.
		var data struct {
			Metadata struct {
				Name        string            `json:"name"`
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			fmt.Println(string(resp.Body()))
			return nil
		}
		runName := data.Metadata.Name
		phase := data.Metadata.Annotations["kuso.sislelabs.com/run-phase"]
		fmt.Fprintf(os.Stderr, "run %s created (phase=%s)\n  command: %s\n",
			runName, phase, strings.Join(command, " "))

		if !runFollow {
			return nil
		}
		return streamRunUntilDone(project, service, runName)
	},
}

// streamRunUntilDone polls the run's phase, streams its logs once
// the pod has produced any, and exits with the run's exit code (1
// for failed/cancelled, 0 for succeeded).
//
// The poll loop is the simplest correct shape — there's no
// server-side `runs/{run}/follow` WebSocket today and adding one
// is a bigger lift than this CLI-side polling. Cadence (1s while
// pending, 2s while running) keeps cost down on long migrations
// while staying snappy on quick ones.
func streamRunUntilDone(project, service, runName string) error {
	var lastLineCount int
	for {
		resp, err := api.GetRun(project, runName)
		if err != nil {
			return fmt.Errorf("get run: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("get run: server returned %d", resp.StatusCode())
		}
		var data struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal(resp.Body(), &data); err != nil {
			return fmt.Errorf("decode run: %w", err)
		}
		phase := data.Metadata.Annotations["kuso.sislelabs.com/run-phase"]
		// Try to pull logs every iteration once the pod might exist
		// (running or terminal). New lines since last fetch are
		// printed. We fetch a bounded tail (500 lines) — anything
		// past that on a long run is best-read via `kuso logs`.
		if phase == "running" || phase == "succeeded" || phase == "failed" || phase == "cancelled" {
			lines, err := fetchRunLines(project, service, runName, 500)
			if err == nil && len(lines) > lastLineCount {
				for _, ln := range lines[lastLineCount:] {
					fmt.Println(ln)
				}
				lastLineCount = len(lines)
			}
		}
		switch phase {
		case "succeeded":
			return nil
		case "failed":
			msg := data.Metadata.Annotations["kuso.sislelabs.com/run-message"]
			if msg == "" {
				msg = "run failed"
			}
			return fmt.Errorf("%s", msg)
		case "cancelled":
			return fmt.Errorf("run cancelled")
		case "running":
			time.Sleep(2 * time.Second)
		default:
			time.Sleep(1 * time.Second)
		}
	}
}

// fetchRunLines pulls the run pod's log tail via the existing
// /logs endpoint with env=run:<runName>. Reuses the server's
// tailRunPods machinery so we don't open a parallel code path.
func fetchRunLines(project, service, runName string, lines int) ([]string, error) {
	path := fmt.Sprintf("/api/projects/%s/services/%s/logs?env=%s&lines=%d",
		project, service, "run%3A"+runName, lines)
	resp, err := api.RawGet(path)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode() >= 300 {
		return nil, fmt.Errorf("get logs: %d", resp.StatusCode())
	}
	var data struct {
		Lines []struct {
			Line string `json:"line"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(resp.Body(), &data); err != nil {
		return nil, err
	}
	out := make([]string, len(data.Lines))
	for i, l := range data.Lines {
		out[i] = l.Line
	}
	return out, nil
}

// parseRunEnvFlags turns []string{"K=V", "X=Y"} into the typed
// EnvVar slice the API expects. Empty values are allowed (FOO= ).
func parseRunEnvFlags(flags []string) ([]kusoApi.RunEnvVar, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	out := make([]kusoApi.RunEnvVar, 0, len(flags))
	for _, f := range flags {
		i := strings.IndexByte(f, '=')
		if i <= 0 {
			return nil, fmt.Errorf("--env %q: expected KEY=VALUE", f)
		}
		out = append(out, kusoApi.RunEnvVar{Name: f[:i], Value: f[i+1:]})
	}
	return out, nil
}

func init() {
	runCmd.Flags().IntVar(&runTimeoutSeconds, "timeout-seconds", 0, "max run duration in seconds (default 1800 / 30 min)")
	runCmd.Flags().StringArrayVar(&runEnvFlags, "env", nil, "extra env var (KEY=VALUE), repeatable")
	runCmd.Flags().BoolVarP(&runFollow, "follow", "f", false, "stream logs + block until the run completes; exit code matches the run's exit code")
	rootCmd.AddCommand(runCmd)
}
