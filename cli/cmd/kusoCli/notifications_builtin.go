package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

var notificationsTestEventProject string

var notificationsTestEventCmd = &cobra.Command{
	Use:   "test-event <event-type>",
	Short: "Send a test of a built-in alert (build.failed, pod.crashed, …) through its routing",
	Long: `Send a synthetic event of one built-in type, marked as a test, to every
channel a real one would reach (event list, project filter), and print each
delivery result. Unlike 'kuso notifications test <id>', which targets one
channel directly, this checks the routing too.`,
	Example: `  kuso notifications test-event build.failed
  kuso notifications test-event pod.crashed --project shop`,
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeNotifEvents,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.TestNotificationEventType(args[0], notificationsTestEventProject)
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		var out struct {
			Results []struct {
				Name  string `json:"name"`
				Type  string `json:"type"`
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			} `json:"results"`
			Muted bool `json:"muted"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode test result: %w", err)
		}
		if len(out.Results) == 0 {
			fmt.Printf("no notification channel receives %s (check the channels' events and project filters)\n", args[0])
			return nil
		}
		failed := 0
		for _, r := range out.Results {
			if r.OK {
				fmt.Printf("delivered  %s (%s)\n", r.Name, r.Type)
				continue
			}
			failed++
			fmt.Printf("FAILED     %s (%s): %s\n", r.Name, r.Type, r.Error)
		}
		if out.Muted {
			fmt.Println("note: the project is muted, so real events from it are held back")
		}
		if failed > 0 {
			return fmt.Errorf("%d of %d channels failed", failed, len(out.Results))
		}
		return nil
	},
}

type notifLatestDelivery struct {
	EventType   string    `json:"eventType"`
	RuleID      string    `json:"ruleId,omitempty"`
	Channel     string    `json:"channel"`
	ChannelName string    `json:"channelName"`
	ChannelType string    `json:"channelType"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	At          time.Time `json:"at"`
}

var notificationsDeliveriesOutput string

var notificationsDeliveriesCmd = &cobra.Command{
	Use:   "deliveries",
	Short: "Show the last delivery of each event type (and alert rule) to each channel",
	Long: `Show the newest delivery per event type, alert rule and channel, read from
the delivery queue. Delivered rows are pruned after the notify retention
window (KUSO_NOTIFY_RETENTION_DAYS, default 7), so an event that hasn't fired
since then has no row. Test sends aren't recorded.`,
	Example: `  kuso notifications deliveries
  kuso notifications deliveries -o json | jq '.[] | select(.status != "delivered")'`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.LatestNotificationDeliveries()
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		var rows []notifLatestDelivery
		if err := json.Unmarshal(resp.Body(), &rows); err != nil {
			return fmt.Errorf("decode deliveries: %w", err)
		}
		switch notificationsDeliveriesOutput {
		case "json":
			return jsonOut(rows)
		case "table", "":
		default:
			return fmt.Errorf("unsupported output format %q", notificationsDeliveriesOutput)
		}
		if len(rows) == 0 {
			fmt.Println("no deliveries recorded in the retention window")
			return nil
		}
		t := tablewriter.NewWriter(os.Stdout)
		t.SetHeader([]string{"EVENT", "RULE", "CHANNEL", "STATUS", "ERROR", "AT"})
		for _, r := range rows {
			t.Append([]string{r.EventType, dashIfEmpty(r.RuleID), r.ChannelName + " (" + r.ChannelType + ")", r.Status, dashIfEmpty(r.Error), r.At.Local().Format(time.DateTime)})
		}
		t.Render()
		return nil
	},
}

func init() {
	notificationsCmd.AddCommand(notificationsTestEventCmd)
	notificationsTestEventCmd.Flags().StringVar(&notificationsTestEventProject, "project", "", "test the routing a real event from this project would get")
	notificationsCmd.AddCommand(notificationsDeliveriesCmd)
	notificationsDeliveriesCmd.Flags().StringVarP(&notificationsDeliveriesOutput, "output", "o", "table", "output format [table, json]")
}
