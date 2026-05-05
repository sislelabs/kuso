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

// Schedule flags shared between `addon-backup schedule` and the
// no-arg `addon-backup unschedule` (which is just `schedule
// --schedule=""`).
var (
	addonBackupSchedule      string
	addonBackupRetentionDays int
)

var addonBackupScheduleCmd = &cobra.Command{
	Use:   "schedule <project> <addon>",
	Short: "Set a recurring backup schedule for an addon",
	Long: `Configure the per-addon backup CronJob. Schedule is a 5-field
cron expression (UTC). RetentionDays controls how long old objects
stay in S3 — 0 means keep forever (the chart's prune step skips).
Pass --schedule="" to disable scheduled backups (chart drops the
CronJob entirely; existing S3 objects stay).

This requires admin S3 credentials configured at /settings/backups
(or via PUT /api/admin/backup-settings).`,
	Example: `  kuso addon-backup schedule hui hui-postgres --schedule "0 3 * * *" --retention 14
  kuso addon-backup schedule hui hui-postgres --schedule ""`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		req := kusoApi.UpdateAddonRequest{Backup: &kusoApi.UpdateAddonBackup{}}
		if cmd.Flags().Changed("schedule") {
			s := addonBackupSchedule
			req.Backup.Schedule = &s
		}
		if cmd.Flags().Changed("retention") {
			r := addonBackupRetentionDays
			req.Backup.RetentionDays = &r
		}
		if req.Backup.Schedule == nil && req.Backup.RetentionDays == nil {
			return fmt.Errorf("pass --schedule and/or --retention")
		}
		resp, err := api.UpdateAddon(args[0], args[1], req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		if req.Backup.Schedule != nil && *req.Backup.Schedule == "" {
			fmt.Printf("backup schedule disabled on %s/%s\n", args[0], args[1])
		} else {
			fmt.Printf("backup schedule updated on %s/%s\n", args[0], args[1])
		}
		return nil
	},
}

var addonBackupUnscheduleCmd = &cobra.Command{
	Use:     "unschedule <project> <addon>",
	Aliases: []string{"disable"},
	Short:   "Disable scheduled backups for an addon (keeps existing S3 objects)",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		empty := ""
		req := kusoApi.UpdateAddonRequest{
			Backup: &kusoApi.UpdateAddonBackup{Schedule: &empty},
		}
		resp, err := api.UpdateAddon(args[0], args[1], req)
		if err != nil {
			return err
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("backup schedule disabled on %s/%s\n", args[0], args[1])
		return nil
	},
}

func init() {
	rootCmd.AddCommand(addonBackupCmd)
	addonBackupCmd.AddCommand(addonBackupListCmd)
	addonBackupListCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	addonBackupCmd.AddCommand(addonBackupRestoreCmd)
	addonBackupRestoreCmd.Flags().StringVar(&addonBackupRestoreInto, "into", "", "destination addon (empty = restore in-place, DESTRUCTIVE)")
	addonBackupCmd.AddCommand(addonBackupScheduleCmd)
	addonBackupScheduleCmd.Flags().StringVar(&addonBackupSchedule, "schedule", "", "5-field cron expression (UTC); empty disables")
	addonBackupScheduleCmd.Flags().IntVar(&addonBackupRetentionDays, "retention", 14, "delete S3 objects older than N days; 0 = keep forever")
	addonBackupCmd.AddCommand(addonBackupUnscheduleCmd)
}
