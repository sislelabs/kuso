// kuso project mute/unmute — per-project notification silencing.
// Muting stops external channel delivery (Discord/Slack/webhook/email)
// for the project's events; the in-app bell feed keeps recording them,
// so nothing is lost from the audit trail.

package kusoCli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var projectMuteCmd = &cobra.Command{
	Use:   "mute <project>",
	Short: "Silence external notifications (Discord/Slack/webhook) for a project",
	Long: `Mute a project's notifications. Events from the project stop going to
external channels (Discord, Slack, webhooks, email, Telegram, Pushover)
but keep appearing in the in-app bell feed, so the audit trail survives.
Error-severity alerts still page through — a mute silences chatter, not
"service down". Undo with 'kuso project unmute <project>'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.MuteProjectNotifications(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("mute: %w", err)
		}
		fmt.Printf("project %s notifications muted (bell feed still records events)\n", args[0])
		return nil
	},
}

var projectUnmuteCmd = &cobra.Command{
	Use:   "unmute <project>",
	Short: "Re-enable external notifications for a project",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.UnmuteProjectNotifications(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("unmute: %w", err)
		}
		fmt.Printf("project %s notifications unmuted\n", args[0])
		return nil
	},
}

var projectMutesCmd = &cobra.Command{
	Use:   "mutes",
	Short: "List projects with muted notifications (admin)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListMutedProjects()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list mutes: %w", err)
		}
		var mutes []struct {
			Project   string `json:"project"`
			CreatedAt string `json:"createdAt"`
			CreatedBy string `json:"createdBy"`
		}
		if err := json.Unmarshal(resp.Body(), &mutes); err != nil {
			return fmt.Errorf("decode mutes: %w", err)
		}
		if len(mutes) == 0 {
			fmt.Println("no muted projects")
			return nil
		}
		for _, m := range mutes {
			line := m.Project
			if m.CreatedAt != "" {
				line += "\tsince " + m.CreatedAt
			}
			if m.CreatedBy != "" {
				line += "\tby " + m.CreatedBy
			}
			fmt.Println(line)
		}
		return nil
	},
}

var projectMuteStatusCmd = &cobra.Command{
	Use:   "mute-status <project>",
	Short: "Show whether a project's notifications are muted",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetProjectNotificationMute(args[0])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("mute status: %w", err)
		}
		var st struct {
			Muted bool   `json:"muted"`
			Since string `json:"since"`
			By    string `json:"by"`
		}
		if err := json.Unmarshal(resp.Body(), &st); err != nil {
			return fmt.Errorf("decode mute status: %w", err)
		}
		if !st.Muted {
			fmt.Printf("project %s notifications are not muted\n", args[0])
			return nil
		}
		line := fmt.Sprintf("project %s notifications are MUTED", args[0])
		if st.Since != "" {
			line += " since " + st.Since
		}
		if st.By != "" {
			line += " by " + st.By
		}
		fmt.Println(line)
		return nil
	},
}

func init() {
	projectCmd.AddCommand(projectMuteCmd, projectUnmuteCmd, projectMutesCmd, projectMuteStatusCmd)
}
