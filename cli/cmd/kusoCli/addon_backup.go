package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso addon-backup` — list S3 backups + restore (in-place or
// into a sibling). Distinct from `kuso backup` which dumps the
// kuso server's own SQLite.

var addonBackupCmd = &cobra.Command{
	Use:     "addon-backup",
	Aliases: []string{"addon-backups", "abackup"},
	Short:   "List addon S3 backups + restore",
}

var addonBackupListCmd = &cobra.Command{
	Use:     "list <project> <addon>",
	Aliases: []string{"ls"},
	Short:   "List S3 backup objects for an addon",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ListAddonBackups(args[0], args[1])
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var items []map[string]any
		if err := json.Unmarshal(resp.Body(), &items); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		switch outputFormat {
		case "json":
			return jsonOut(items)
		default:
			t := tablewriter.NewWriter(os.Stdout)
			t.SetHeader([]string{"KEY", "SIZE", "WHEN"})
			for _, b := range items {
				t.Append([]string{
					asString(b["key"]),
					fmt.Sprintf("%v", b["size"]),
					asString(b["when"]),
				})
			}
			t.Render()
			return nil
		}
	},
}

var addonBackupRestoreInto string

var addonBackupRestoreCmd = &cobra.Command{
	Use:   "restore <project> <addon> <key>",
	Short: "Restore an S3 backup. Default = in-place (DESTRUCTIVE)",
	Long: `Restores the chosen backup into either the source addon (overwriting
existing data) or a sibling addon (--into <name>) leaving the source
untouched. The server creates a one-shot Job that runs gunzip | psql.

The target addon must already exist + be the same engine as the source.`,
	Example: `  kuso addon-backup restore myproj postgres myproj/postgres/20260504T030000Z.sql.gz
  kuso addon-backup restore myproj postgres myproj/postgres/20260504T030000Z.sql.gz --into postgres-rehearse`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req := kusoApi.RestoreBackupRequest{Key: args[2], Into: addonBackupRestoreInto}
		resp, err := api.RestoreAddonBackup(args[0], args[1], req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var body struct {
			Job string `json:"job"`
		}
		_ = json.Unmarshal(resp.Body(), &body)
		dest := args[1] + " (in-place / destructive)"
		if addonBackupRestoreInto != "" {
			dest = addonBackupRestoreInto + " (non-destructive)"
		}
		fmt.Printf("restore job %s started → %s\n", body.Job, dest)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(addonBackupCmd)
	addonBackupCmd.AddCommand(addonBackupListCmd)
	addonBackupListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	addonBackupCmd.AddCommand(addonBackupRestoreCmd)
	addonBackupRestoreCmd.Flags().StringVar(&addonBackupRestoreInto, "into", "", "destination addon (empty = restore in-place, DESTRUCTIVE)")
}
