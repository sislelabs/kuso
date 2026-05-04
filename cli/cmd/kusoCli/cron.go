package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso cron` — schedule recurring jobs against a service's image.

var cronCmd = &cobra.Command{
	Use:     "cron",
	Aliases: []string{"crons"},
	Short:   "Schedule recurring jobs against a service",
}

var (
	cronAddName              string
	cronAddSchedule          string
	cronAddCmdString         string
	cronAddSuspend           bool
	cronAddConcurrencyPolicy string
)

var cronListCmd = &cobra.Command{
	Use:   "list <project> [service]",
	Short: "List crons in a project (optionally filtered by service)",
	Args:  cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		var r *resty.Response
		var err error
		if len(args) == 2 {
			r, err = api.ListCronsForService(args[0], args[1])
		} else {
			r, err = api.ListCrons(args[0])
		}
		if err != nil {
			return err
		}
		if r.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", r.StatusCode(), string(r.Body()))
		}
		var items []map[string]any
		if err := json.Unmarshal(r.Body(), &items); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(items)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"NAME", "SERVICE", "SCHEDULE", "SUSPEND", "COMMAND"})
			for _, c := range items {
				spec, _ := c["spec"].(map[string]any)
				meta, _ := c["metadata"].(map[string]any)
				cmdParts, _ := spec["command"].([]any)
				cmdStrs := make([]string, 0, len(cmdParts))
				for _, p := range cmdParts {
					if s, ok := p.(string); ok {
						cmdStrs = append(cmdStrs, s)
					}
				}
				t.Append([]string{
					asString(meta["name"]),
					asString(spec["service"]),
					asString(spec["schedule"]),
					fmt.Sprintf("%v", spec["suspend"]),
					strings.Join(cmdStrs, " "),
				})
			}
			t.Render()
			return nil
		}
	},
}

var cronAddCommand = &cobra.Command{
	Use:   "add <project> <service>",
	Short: "Schedule a new cron",
	Args:  cobra.ExactArgs(2),
	Example: `  kuso cron add myproj api --name daily-cleanup --schedule '0 3 * * *' --cmd 'rails runner Cleanup.run'
  kuso cron add myproj api --name every-15min --schedule '*/15 * * * *' --cmd 'sh -c "echo tick"'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if cronAddName == "" || cronAddSchedule == "" || cronAddCmdString == "" {
			return fmt.Errorf("--name, --schedule, and --cmd are required")
		}
		argv := strings.Fields(cronAddCmdString)
		req := kusoApi.CreateCronRequest{
			Name:              cronAddName,
			Schedule:          cronAddSchedule,
			Command:           argv,
			Suspend:           cronAddSuspend,
			ConcurrencyPolicy: cronAddConcurrencyPolicy,
		}
		resp, err := api.AddCron(args[0], args[1], req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("cron %s/%s created\n", args[1], cronAddName)
		return nil
	},
}

var cronDeleteCmd = &cobra.Command{
	Use:     "delete <project> <service> <name>",
	Aliases: []string{"rm"},
	Short:   "Delete a cron",
	Args:    cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DeleteCron(args[0], args[1], args[2])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("cron %s/%s deleted\n", args[1], args[2])
		return nil
	},
}

var cronSyncCmd = &cobra.Command{
	Use:   "sync <project> <service> <name>",
	Short: "Re-resolve image + envFromSecrets from the production env",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.SyncCron(args[0], args[1], args[2])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("cron %s/%s synced\n", args[1], args[2])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(cronCmd)
	cronCmd.AddCommand(cronListCmd)
	cronListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	cronCmd.AddCommand(cronAddCommand)
	cronAddCommand.Flags().StringVar(&cronAddName, "name", "", "cron name (required)")
	cronAddCommand.Flags().StringVar(&cronAddSchedule, "schedule", "", "cron expression — '*/15 * * * *' (required)")
	cronAddCommand.Flags().StringVar(&cronAddCmdString, "cmd", "", "command argv (split on whitespace) (required)")
	cronAddCommand.Flags().BoolVar(&cronAddSuspend, "suspend", false, "create suspended")
	cronAddCommand.Flags().StringVar(&cronAddConcurrencyPolicy, "concurrency", "Forbid", "Allow|Forbid|Replace")
	cronCmd.AddCommand(cronDeleteCmd)
	cronCmd.AddCommand(cronSyncCmd)
}
