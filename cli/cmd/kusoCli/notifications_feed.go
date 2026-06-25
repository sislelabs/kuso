package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso notifications feed` — the in-app bell-icon feed. Registered on
// the existing notificationsCmd parent (defined in notifications.go).
// Admin-gated server-side.
//
//   kuso notifications feed                 list recent events
//   kuso notifications feed --unread        only unread
//   kuso notifications feed --unread-count  cheap counter the badge polls
//   kuso notifications feed --read-all      mark all read
//   kuso notifications feed --clear         delete every event

var (
	notifFeedLimit       int
	notifFeedUnread      bool
	notifFeedUnreadCount bool
	notifFeedReadAll     bool
	notifFeedClear       bool
)

var notificationsFeedCmd = &cobra.Command{
	Use:   "feed",
	Short: "Show the in-app notification feed (the bell-icon view).",
	Long: `Read the in-app notification feed — instance-wide deploy outcomes,
node health, and backup events. Admin-gated. With no flags it lists the
most recent events; --unread narrows to unread ones. The mutating flags
are mutually exclusive with each other and with the list:
  --unread-count  print just the unread counter
  --read-all      mark every event read
  --clear         delete every event in the feed`,
	Example: `  kuso notifications feed
  kuso notifications feed --unread --limit 20
  kuso notifications feed --unread-count
  kuso notifications feed --read-all
  kuso notifications feed --clear`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}

		// Guard the mutually exclusive actions.
		actions := 0
		for _, b := range []bool{notifFeedUnreadCount, notifFeedReadAll, notifFeedClear} {
			if b {
				actions++
			}
		}
		if actions > 1 {
			return fmt.Errorf("--unread-count, --read-all, and --clear are mutually exclusive")
		}

		switch {
		case notifFeedUnreadCount:
			resp, err := api.NotificationFeedUnreadCount()
			if err != nil {
				return err
			}
			if resp.StatusCode() >= 300 {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
			}
			if outputFormat == "json" {
				fmt.Println(string(resp.Body()))
				return nil
			}
			var out struct {
				Unread int `json:"unread"`
			}
			if err := json.Unmarshal(resp.Body(), &out); err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			fmt.Printf("%d unread\n", out.Unread)
			return nil

		case notifFeedReadAll:
			resp, err := api.NotificationFeedReadAll()
			if err != nil {
				return err
			}
			if resp.StatusCode() >= 300 {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
			}
			fmt.Println("Marked all feed events read.")
			return nil

		case notifFeedClear:
			resp, err := api.NotificationFeedClear()
			if err != nil {
				return err
			}
			if resp.StatusCode() >= 300 {
				return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
			}
			fmt.Println("Cleared the notification feed.")
			return nil
		}

		// Default: list.
		resp, err := api.NotificationFeed(notifFeedLimit, notifFeedUnread)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		if outputFormat == "json" {
			fmt.Println(string(resp.Body()))
			return nil
		}
		var events []kusoApi.NotificationEvent
		if err := json.Unmarshal(resp.Body(), &events); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if len(events) == 0 {
			fmt.Println("No notification events.")
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"When", "Sev", "Type", "Project", "Service", "Title", "Read"})
		for _, e := range events {
			read := "•"
			if e.ReadAt != nil {
				read = ""
			}
			tw.Append([]string{
				e.CreatedAt,
				dashIfEmpty(e.Severity),
				dashIfEmpty(e.Type),
				dashIfEmpty(e.Project),
				dashIfEmpty(e.Service),
				e.Title,
				read,
			})
		}
		tw.Render()
		return nil
	},
}

func init() {
	notificationsFeedCmd.Flags().IntVar(&notifFeedLimit, "limit", 0, "max events (default 50 server-side)")
	notificationsFeedCmd.Flags().BoolVar(&notifFeedUnread, "unread", false, "list only unread events")
	notificationsFeedCmd.Flags().BoolVar(&notifFeedUnreadCount, "unread-count", false, "print just the unread counter")
	notificationsFeedCmd.Flags().BoolVar(&notifFeedReadAll, "read-all", false, "mark every event read")
	notificationsFeedCmd.Flags().BoolVar(&notifFeedClear, "clear", false, "delete every event in the feed")
	notificationsFeedCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	notificationsCmd.AddCommand(notificationsFeedCmd)
}
