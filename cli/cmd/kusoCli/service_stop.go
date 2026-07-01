// service_stop.go hangs the stop + start commands off the
// `kuso project service` subtree (projectServiceCmd, defined in
// project.go). Kept in a separate file to avoid churning project.go,
// mirroring service_extra.go.
//
//	kuso project service stop  <project> <service> [-y]
//	kuso project service start <project> <service>

package kusoCli

import (
	"fmt"

	"github.com/spf13/cobra"
)

var serviceStopYes bool

var serviceStopCmd = &cobra.Command{
	Use:   "stop <project> <service>",
	Short: "Hard-stop a service: pin it to 0 replicas and disable wake-on-traffic",
	Long: `Take a service offline by pinning it to 0 replicas and disabling
wake-on-traffic. Unlike sleep (scale-to-zero), a stopped service will
NOT wake when a request arrives — visitors get a 503 until you run
'kuso project service start'.

Prompts for confirmation unless --yes.`,
	Example: `  kuso project service stop scubatony api
  kuso project service stop scubatony api --yes`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(serviceStopYes,
			fmt.Sprintf("Stop %s/%s? Visitors will get a 503 until you start it again.", args[0], args[1])); err != nil {
			return err
		}
		resp, err := api.StopService(args[0], args[1])
		if err != nil {
			return fmt.Errorf("stop service: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("service %s/%s stopped — pinned to 0 replicas, won't wake on traffic. Run 'kuso project service start' to restore.\n", args[0], args[1])
		return nil
	},
}

var serviceStartCmd = &cobra.Command{
	Use:   "start <project> <service>",
	Short: "Clear a hard-stop, restoring the service to its configured replicas",
	Long: `Clear a hard-stop set by 'kuso project service stop', restoring the
service to its configured replicas. The scale-up is asynchronous — the
pods come up on the next reconcile.`,
	Example: `  kuso project service start scubatony api`,
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.StartService(args[0], args[1])
		if err != nil {
			return fmt.Errorf("start service: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("service %s/%s started.\n", args[0], args[1])
		return nil
	},
}

func init() {
	// projectServiceCmd is the package-level var defined in project.go.
	projectServiceCmd.AddCommand(serviceStopCmd)
	projectServiceCmd.AddCommand(serviceStartCmd)
	serviceStopCmd.Flags().BoolVarP(&serviceStopYes, "yes", "y", false, "skip the confirmation prompt")
}
