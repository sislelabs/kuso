package kusoCli

import (
	"encoding/json"
	"fmt"
	"net/url"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var backupProjectKeyCmd = &cobra.Command{
	Use:   "project-key",
	Short: "Give a project its own backup key, limited to its prefix (admin)",
	Long: `Every backup pod uses the shared backup key unless its project has its
own. Create a key on your S3 provider whose policy covers only
<bucket>/<project>/ ('kuso backup settings project-key policy <project>'
prints one for AWS/MinIO), then register it with 'set'. kuso refuses a key
that can write or list outside the project's prefix. The project's backup
CronJobs switch to it on their next reconcile.`,
}

var backupProjectKeyList = &cobra.Command{
	Use:   "list",
	Short: "List projects with their own backup key",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RawGet("/api/admin/backup-settings/projects")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list project keys: %w", err)
		}
		var rows []struct {
			Project     string `json:"project"`
			AccessKeyID string `json:"accessKeyId"`
		}
		if err := json.Unmarshal(resp.Body(), &rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PROJECT\tACCESS KEY")
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\n", r.Project, r.AccessKeyID)
		}
		return tw.Flush()
	},
}

var backupProjectKeyPolicy = &cobra.Command{
	Use:   "policy <project>",
	Short: "Print an S3 policy limiting a key to the project's backup prefix",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RawGet("/api/admin/backup-settings/projects/" + url.PathEscape(args[0]) + "/policy")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("policy: %w", err)
		}
		var out any
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return jsonOut(out)
	},
}

var backupProjectKeyAK, backupProjectKeySK string

var backupProjectKeySet = &cobra.Command{
	Use:   "set <project>",
	Short: "Register a project's own backup key (verified against the bucket first)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if backupProjectKeyAK == "" || backupProjectKeySK == "" {
			return fmt.Errorf("--access-key-id and --secret-access-key are required")
		}
		raw, _ := json.Marshal(map[string]string{"accessKeyId": backupProjectKeyAK, "secretAccessKey": backupProjectKeySK})
		resp, err := api.RawPut("/api/admin/backup-settings/projects/"+url.PathEscape(args[0]), raw, "application/json")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("set project key: %w", err)
		}
		var out map[string]string
		_ = json.Unmarshal(resp.Body(), &out)
		fmt.Printf("%s now backs up with its own key (%s). %s\n", args[0], out["secret"], out["message"])
		return nil
	},
}

var backupProjectKeyUnset = &cobra.Command{
	Use:   "unset <project>",
	Short: "Put a project back on the shared backup key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(backupProjectKeyUnsetYes, fmt.Sprintf(
			"Put %s back on the shared backup key (its backup pods regain access to every project's dumps)?", args[0])); err != nil {
			return err
		}
		resp, err := api.Raw("DELETE", "/api/admin/backup-settings/projects/"+url.PathEscape(args[0]), nil, nil)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("unset project key: %w", err)
		}
		fmt.Printf("%s backs up with the shared key again\n", args[0])
		return nil
	},
}

func init() {
	backupSettingsCmd.AddCommand(backupProjectKeyCmd)
	backupProjectKeyCmd.AddCommand(backupProjectKeyList, backupProjectKeyPolicy, backupProjectKeySet, backupProjectKeyUnset)
	backupProjectKeySet.Flags().StringVar(&backupProjectKeyAK, "access-key-id", "", "the project's S3 access key ID")
	backupProjectKeySet.Flags().StringVar(&backupProjectKeySK, "secret-access-key", "", "the project's S3 secret access key")
	backupProjectKeyUnset.Flags().BoolVarP(&backupProjectKeyUnsetYes, "yes", "y", false, "skip the confirmation prompt")
}

var backupProjectKeyUnsetYes bool
