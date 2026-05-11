package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// `kuso revision` — list, show, and revert resource revisions.
//
//   kuso revision list <project> <kind> <name>      # service|environment|addon|cron
//   kuso revision show <project> <id>
//   kuso revision revert <project> <id>
//
// Revisions are stored every time a server-side update path writes a
// CR; the History tab in the UI renders the same data.

var revisionCmd = &cobra.Command{
	Use:     "revision",
	Aliases: []string{"revisions", "rev"},
	Short:   "List and replay stored snapshots of a resource",
}

var revisionListCmd = &cobra.Command{
	Use:     "list <project> <kind> <name>",
	Aliases: []string{"ls"},
	Short:   "List recent revisions for a resource (newest first)",
	Long: "kind ∈ {service, environment, addon, cron}. name is the resource's " +
		"short name within the project (e.g. for a service: just `web`, not " +
		"`<project>-web`).",
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListRevisions(args[0], args[1], args[2])
		if err != nil {
			return fmt.Errorf("list revisions: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		type row struct {
			ID        string `json:"id"`
			Project   string `json:"project"`
			Kind      string `json:"kind"`
			Name      string `json:"name"`
			Reason    string `json:"reason"`
			Actor     string `json:"actor"`
			CreatedAt string `json:"createdAt"`
		}
		var items []row
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(items)
		case "table", "":
			if len(items) == 0 {
				fmt.Println("no revisions yet")
				return nil
			}
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"ID", "ACTOR", "REASON", "AGE"})
			for _, r := range items {
				reason := r.Reason
				if len(reason) > 50 {
					reason = reason[:47] + "..."
				}
				t.Append([]string{r.ID, r.Actor, reason, relativeAge(r.CreatedAt)})
			}
			t.Render()
			return nil
		default:
			return fmt.Errorf("unsupported output format %q", outputFormat)
		}
	},
}

var revisionShowCmd = &cobra.Command{
	Use:   "show <project> <id>",
	Short: "Show a revision's full snapshot",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.GetRevision(args[0], args[1])
		if err != nil {
			return fmt.Errorf("get revision: %w", err)
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("revision %s not found", args[1])
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		// Just dump the JSON body — the snapshot field is the most
		// useful thing here and it's already structured. Honour
		// --output for consistency, but the default is also JSON
		// because there's no useful table layout for an arbitrary
		// resource snapshot.
		var pretty any
		if err := json.Unmarshal(resp.Body(), &pretty); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		return jsonOut(pretty)
	},
}

var revisionRevertCmd = &cobra.Command{
	Use:   "revert <project> <id>",
	Short: "Replay a revision's snapshot back through the matching update path",
	Long: "Server-side this re-runs the patch against the live CR. A fresh " +
		"revision is recorded with reason=\"revert: <original id>\" so the " +
		"history stays linear (you can revert the revert to roll forward).",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RevertRevision(args[0], args[1])
		if err != nil {
			return fmt.Errorf("revert revision: %w", err)
		}
		if resp.StatusCode() == 501 {
			return fmt.Errorf("server returned 501: revert is only supported for kind=service today")
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("revision %s reverted\n", args[1])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(revisionCmd)
	revisionCmd.AddCommand(revisionListCmd)
	revisionListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	revisionCmd.AddCommand(revisionShowCmd)
	revisionShowCmd.Flags().StringVarP(&outputFormat, "output", "o", "json", "output format [json]")
	revisionCmd.AddCommand(revisionRevertCmd)
}
