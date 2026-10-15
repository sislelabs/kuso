package kusoCli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var (
	deployHookEnable  bool
	deployHookRotate  bool
	deployHookDisable bool
)

var serviceDeployHookCmd = &cobra.Command{
	Use:   "deploy-hook <project> <service>",
	Short: "Show or manage a service's deploy hook URL (deploy on push without a GitHub App)",
	Long: `A deploy hook is a secret URL that starts a build of one service. It needs
no GitHub App.

Use it in one of two ways:

  Repo webhook   paste the URL into the repo's webhook settings (GitHub:
                 Settings -> Webhooks, content type application/json, "Just
                 the push event"). kuso builds pushes to a branch one of the
                 service's environments deploys and ignores everything else.
  CI or script   curl -X POST <url>            build the default branch
                 curl -X POST '<url>?ref=<40-char sha>'

The URL is the credential: anyone who has it can start a build. --rotate
replaces it and the old URL stops working.

With no flags, shows whether the hook is enabled and its URL, changing
nothing. --enable turns it on (keeping an existing URL) and prints the URL.`,
	Example: `  kuso service deploy-hook shop api
  kuso service deploy-hook shop api --enable
  kuso service deploy-hook shop api --rotate
  kuso service deploy-hook shop api --disable`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if deployHookDisable && (deployHookRotate || deployHookEnable) {
			return fmt.Errorf("--disable can't be combined with --enable / --rotate")
		}
		project, service := args[0], args[1]
		if !deployHookEnable && !deployHookRotate && !deployHookDisable {
			resp, err := api.GetDeployHook(project, service)
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("get deploy hook: %w", err)
			}
			var out struct {
				Enabled bool   `json:"enabled"`
				URL     string `json:"url"`
			}
			if err := json.Unmarshal(resp.Body(), &out); err != nil {
				return fmt.Errorf("unexpected deploy hook response")
			}
			if !out.Enabled {
				fmt.Fprintf(cmd.ErrOrStderr(), "deploy hook for %s/%s is disabled; turn it on with: kuso service deploy-hook %s %s --enable\n", project, service, project, service)
				return nil
			}
			fmt.Println(out.URL)
			return nil
		}
		if deployHookDisable {
			resp, err := api.DeleteDeployHook(project, service)
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("disable deploy hook: %w", err)
			}
			fmt.Printf("deploy hook for %s/%s disabled\n", project, service)
			return nil
		}
		resp, err := api.EnableDeployHook(project, service, deployHookRotate)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("enable deploy hook: %w", err)
		}
		var out struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil || out.URL == "" {
			return fmt.Errorf("unexpected deploy hook response")
		}
		if deployHookRotate {
			fmt.Fprintln(cmd.ErrOrStderr(), "rotated: the previous URL no longer works")
		}
		fmt.Println(out.URL)
		return nil
	},
}

func init() {
	serviceDeployHookCmd.Flags().BoolVar(&deployHookEnable, "enable", false, "turn the hook on (keeps an existing URL) and print the URL")
	serviceDeployHookCmd.Flags().BoolVar(&deployHookRotate, "rotate", false, "replace the URL; the old one stops working")
	serviceDeployHookCmd.Flags().BoolVar(&deployHookDisable, "disable", false, "turn the hook off")
	projectServiceCmd.AddCommand(serviceDeployHookCmd)
	serviceCmd.AddCommand(aliasOf(serviceDeployHookCmd))
}
