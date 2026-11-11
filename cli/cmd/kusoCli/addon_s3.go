package kusoCli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var addonS3Cmd = &cobra.Command{
	Use:   "s3",
	Short: "S3 (MinIO) addon credentials",
}

var addonS3RotateRoot bool

var addonS3RotateCmd = &cobra.Command{
	Use:   "rotate-credentials <project> <addon>",
	Short: "Give the addon's conn Secret a new bucket-scoped key",
	Long: `Creates a new MinIO user limited to the addon's bucket and writes its
key into <addon>-conn. Services pick it up on their next restart.

Addons created before scoped credentials still hand MinIO's root key to
every subscribed service; rotating once moves them to a scoped user (MinIO
restarts once to read root from <addon>-root).

Otherwise the old key keeps working until you run revoke-previous, so
restart the services that use the addon first.

--root also replaces the MinIO root password (MinIO restarts once), so
root keys that leaked while apps held them stop working. On an addon that
still hands out root, every subscribed service loses access until it is
restarted onto the new scoped key. Apps that use buckets other than the
addon's own need root and break under a scoped key: don't rotate those.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RotateS3Credentials(args[0], args[1], kusoApi.RotateS3CredentialsRequest{RotateRoot: addonS3RotateRoot})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("rotate: %w", err)
		}
		var out struct {
			AccessKeyID         string `json:"accessKeyId"`
			PreviousAccessKeyID string `json:"previousAccessKeyId"`
			Message             string `json:"message"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("new access key: %s\n%s\n", out.AccessKeyID, out.Message)
		if out.PreviousAccessKeyID != "" {
			fmt.Printf("then: kuso project addon s3 revoke-previous %s %s\n", args[0], args[1])
		}
		return nil
	},
}

var addonS3RevokeCmd = &cobra.Command{
	Use:   "revoke-previous <project> <addon>",
	Short: "Remove the MinIO user a rotation replaced",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RevokePreviousS3Credentials(args[0], args[1])
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("revoke: %w", err)
		}
		var out map[string]string
		_ = json.Unmarshal(resp.Body(), &out)
		fmt.Printf("revoked %s\n", out["revokedAccessKeyId"])
		return nil
	},
}

var backupOrphansOutput string

type backupOrphanView struct {
	Project string    `json:"project"`
	Addon   string    `json:"addon"`
	Reason  string    `json:"reason"`
	Objects int       `json:"objects"`
	Bytes   int64     `json:"bytes"`
	Newest  time.Time `json:"newest"`
}

var backupOrphansCmd = &cobra.Command{
	Use:   "orphans",
	Short: "List backups no live addon owns: deleted addons, or a re-created addon's predecessor (admin)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListOrphanBackups()
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("list orphaned backups: %w", err)
		}
		var rows []backupOrphanView
		if err := json.Unmarshal(resp.Body(), &rows); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		if backupOrphansOutput == "json" {
			return jsonOut(rows)
		}
		tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "PROJECT\tADDON\tREASON\tOBJECTS\tBYTES\tNEWEST")
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\n", r.Project, r.Addon, r.Reason, r.Objects, r.Bytes, r.Newest.Format(time.RFC3339))
		}
		return tw.Flush()
	},
}

var backupPurgeYes bool

var backupPurgeCmd = &cobra.Command{
	Use:   "purge <project> <addon>",
	Short: "Permanently delete the orphaned backups under <project>/<addon>/ (admin, audited)",
	Long: `Deletes only what 'kuso backup orphans' lists for that prefix: every
object when the addon no longer exists, or only the predecessor's dumps
when a same-name addon was re-created. The live addon's own backups are
never touched. This cannot be undone.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		target := args[0] + "/" + args[1]
		if !backupPurgeYes {
			fmt.Printf("permanently delete orphaned backups under %s/? type %q to confirm: ", target, target)
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(line) != target {
				return fmt.Errorf("aborted")
			}
		}
		resp, err := api.PurgeOrphanBackups(kusoApi.PurgeOrphanBackupsRequest{Project: args[0], Addon: args[1], Confirm: target})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("purge: %w", err)
		}
		var out map[string]any
		_ = json.Unmarshal(resp.Body(), &out)
		fmt.Printf("deleted %v objects (%v)\n", out["deleted"], out["reason"])
		return nil
	},
}

func init() {
	projectAddonCmd.AddCommand(addonS3Cmd)
	addonS3Cmd.AddCommand(addonS3RotateCmd, addonS3RevokeCmd)
	addonS3RotateCmd.Flags().BoolVar(&addonS3RotateRoot, "root", false, "also replace the MinIO root password (restarts MinIO)")
	backupCmd.AddCommand(backupOrphansCmd, backupPurgeCmd)
	backupOrphansCmd.Flags().StringVarP(&backupOrphansOutput, "output", "o", "table", "output format: table|json")
	backupPurgeCmd.Flags().BoolVarP(&backupPurgeYes, "yes", "y", false, "skip the typed confirmation")
}
