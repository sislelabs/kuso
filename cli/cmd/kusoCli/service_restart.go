package kusoCli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var serviceRestartEnv string

var serviceRestartCmd = &cobra.Command{
	Use:   "restart <project> <service>",
	Short: "Roll a service's pods on their current image, without building",
	Long: `Restart an environment's pods on the image they already run — no
build, no new image. Pods roll one at a time (the new pod must be Ready
before an old one stops). Defaults to production; --env picks another
environment (staging, preview-pr-N).`,
	Example: `  kuso service restart tickero api
  kuso service restart tickero api --env staging`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RestartService(args[0], args[1], serviceRestartEnv)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("restart service: %w", err)
		}
		var out struct {
			RestartedAt string `json:"restartedAt"`
		}
		_ = json.Unmarshal(resp.Body(), &out)
		env := serviceRestartEnv
		if env == "" {
			env = "production"
		}
		fmt.Printf("restarting %s/%s (%s) at %s — pods roll on their current image\n", args[0], args[1], env, out.RestartedAt)
		return nil
	},
}

func init() {
	serviceRestartCmd.Flags().StringVar(&serviceRestartEnv, "env", "", "environment to restart (default production)")
	projectServiceCmd.AddCommand(serviceRestartCmd)
	serviceCmd.AddCommand(aliasOf(serviceRestartCmd))
}
