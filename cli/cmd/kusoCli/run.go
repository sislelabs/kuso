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
	"strings"

	"kuso/pkg/kusoApi"

	"github.com/spf13/cobra"
)

var (
	runTimeoutSeconds int
	runEnvFlags       []string
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
		if err := json.Unmarshal(resp.Body(), &data); err == nil {
			phase := data.Metadata.Annotations["kuso.sislelabs.com/run-phase"]
			fmt.Printf("run %s created (phase=%s)\n  command: %s\n",
				data.Metadata.Name, phase, strings.Join(command, " "))
			return nil
		}
		fmt.Println(string(resp.Body()))
		return nil
	},
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
	rootCmd.AddCommand(runCmd)
}
