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

// Project-scoped cron flags. Shared between `cron add-http`,
// `cron add-command`, and `cron edit`.
var (
	pCronName                  string
	pCronDisplayName           string
	pCronSchedule              string
	pCronURL                   string
	pCronImage                 string
	pCronImageTag              string
	pCronCmdString             string
	pCronSuspend               bool
	pCronConcurrencyPolicy     string
)

var cronAddHTTPCmd = &cobra.Command{
	Use:   "add-http <project>",
	Short: "Schedule a recurring HTTP probe (curl <url>; fail on non-2xx)",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso cron add-http myproj --name healthcheck --schedule '*/5 * * * *' --url https://api.example.com/healthz`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if pCronName == "" || pCronSchedule == "" || pCronURL == "" {
			return fmt.Errorf("--name, --schedule, --url are required")
		}
		req := kusoApi.CreateProjectCronRequest{
			Name:              pCronName,
			DisplayName:       pCronDisplayName,
			Kind:              "http",
			Schedule:          pCronSchedule,
			URL:               pCronURL,
			Suspend:           pCronSuspend,
			ConcurrencyPolicy: pCronConcurrencyPolicy,
		}
		resp, err := api.AddProjectCron(args[0], req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("cron %s/%s (http) created\n", args[0], pCronName)
		return nil
	},
}

var cronAddCommandCmd = &cobra.Command{
	Use:   "add-command <project>",
	Short: "Schedule a recurring command run (user-supplied image + argv)",
	Args:  cobra.ExactArgs(1),
	Example: `  kuso cron add-command myproj --name nightly-prune --schedule '0 4 * * *' --image alpine:3.21 --cmd 'sh -c "rm -rf /tmp/*"'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if pCronName == "" || pCronSchedule == "" || pCronImage == "" || pCronCmdString == "" {
			return fmt.Errorf("--name, --schedule, --image, --cmd are required")
		}
		req := kusoApi.CreateProjectCronRequest{
			Name:              pCronName,
			DisplayName:       pCronDisplayName,
			Kind:              "command",
			Schedule:          pCronSchedule,
			Image:             &kusoApi.CronImage{Repository: pCronImage, Tag: pCronImageTag},
			Command:           strings.Fields(pCronCmdString),
			Suspend:           pCronSuspend,
			ConcurrencyPolicy: pCronConcurrencyPolicy,
		}
		resp, err := api.AddProjectCron(args[0], req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("cron %s/%s (command) created\n", args[0], pCronName)
		return nil
	},
}

var cronEditCmd = &cobra.Command{
	Use:   "edit <project> <name>",
	Short: "Edit a project-scoped cron (kind=http or kind=command)",
	Long: `Patch fields on an existing project-scoped cron in place.
Service-attached crons (kind=service) use 'kuso cron sync' for the
parent-image refresh and the schedule/command pair on each delete +
add roundtrip via the per-service path.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req := kusoApi.UpdateProjectCronRequest{}
		if cmd.Flags().Changed("display-name") {
			v := pCronDisplayName
			req.DisplayName = &v
		}
		if cmd.Flags().Changed("schedule") {
			v := pCronSchedule
			req.Schedule = &v
		}
		if cmd.Flags().Changed("suspend") {
			v := pCronSuspend
			req.Suspend = &v
		}
		if cmd.Flags().Changed("url") {
			v := pCronURL
			req.URL = &v
		}
		if cmd.Flags().Changed("image") || cmd.Flags().Changed("image-tag") {
			req.Image = &kusoApi.CronImage{Repository: pCronImage, Tag: pCronImageTag}
		}
		if cmd.Flags().Changed("cmd") {
			req.Command = strings.Fields(pCronCmdString)
		}
		if cmd.Flags().Changed("concurrency") {
			v := pCronConcurrencyPolicy
			req.ConcurrencyPolicy = &v
		}
		resp, err := api.UpdateProjectCron(args[0], args[1], req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("cron %s/%s updated\n", args[0], args[1])
		return nil
	},
}

var cronProjectDeleteCmd = &cobra.Command{
	Use:     "delete-project <project> <name>",
	Aliases: []string{"rm-project"},
	Short:   "Delete a project-scoped cron",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DeleteProjectCron(args[0], args[1])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("cron %s/%s deleted\n", args[0], args[1])
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

	// Project-scoped commands.
	for _, c := range []*cobra.Command{cronAddHTTPCmd, cronAddCommandCmd, cronEditCmd} {
		c.Flags().StringVar(&pCronName, "name", "", "cron name")
		c.Flags().StringVar(&pCronDisplayName, "display-name", "", "free-form label shown in canvas")
		c.Flags().StringVar(&pCronSchedule, "schedule", "", "cron expression — '*/15 * * * *'")
		c.Flags().StringVar(&pCronURL, "url", "", "target URL (kind=http only)")
		c.Flags().StringVar(&pCronImage, "image", "", "container image repo (kind=command only)")
		c.Flags().StringVar(&pCronImageTag, "image-tag", "latest", "container image tag")
		c.Flags().StringVar(&pCronCmdString, "cmd", "", "command argv (split on whitespace)")
		c.Flags().BoolVar(&pCronSuspend, "suspend", false, "create / set suspended")
		c.Flags().StringVar(&pCronConcurrencyPolicy, "concurrency", "Forbid", "Allow|Forbid|Replace")
	}
	cronCmd.AddCommand(cronAddHTTPCmd)
	cronCmd.AddCommand(cronAddCommandCmd)
	cronCmd.AddCommand(cronEditCmd)
	cronCmd.AddCommand(cronProjectDeleteCmd)
}
