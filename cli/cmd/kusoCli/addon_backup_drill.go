package kusoCli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

var (
	addonBackupDrillKey             string
	addonBackupDrillAllowUnverified bool
	addonBackupDrillNoWait          bool
	addonBackupDrillTimeout         time.Duration
)

type drillStatus struct {
	Job     string `json:"job"`
	Key     string `json:"key"`
	Phase   string `json:"phase"`
	Tables  *int64 `json:"tables"`
	Rows    *int64 `json:"rows"`
	Message string `json:"message"`
}

// drillOutcome turns a finished drill into the command's result: an error
// for a failed drill, so scripts and cron can alert on the exit code.
func drillOutcome(st drillStatus) (string, error) {
	if st.Phase != "Succeeded" {
		if st.Message == "" {
			st.Message = "no detail"
		}
		return "", fmt.Errorf("restore drill of %s FAILED: %s", st.Key, st.Message)
	}
	if st.Tables == nil || st.Rows == nil {
		return "", fmt.Errorf("restore drill of %s finished but reported no counts (job %s)", st.Key, st.Job)
	}
	return fmt.Sprintf("restore drill OK: %s restored %d tables, ~%d rows (scratch database dropped)", st.Key, *st.Tables, *st.Rows), nil
}

var addonBackupDrillCmd = &cobra.Command{
	Use:   "drill <project> <addon>",
	Short: "Prove a backup restores: replay it into a scratch database and count it",
	Long: `Restores a stored backup (the newest, or --key) through the same restore
pipeline a real restore uses, into a temporary database on the addon's own
server, reports how many tables and rows came back, then drops it. The live
database is not touched. Postgres addons only; needs a role that can CREATE
DATABASE and enough free volume for a second copy (the drill refuses when a
copy would push the volume past 85%).`,
	Example: `  kuso addon-backup drill myproj db
  kuso addon-backup drill myproj db --key myproj/db/20261101T030000Z.sql.gz`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DrillAddonBackup(args[0], args[1], kusoApi.DrillBackupRequest{Key: addonBackupDrillKey, AllowUnverified: addonBackupDrillAllowUnverified})
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("start drill: %w", err)
		}
		var st drillStatus
		if err := json.Unmarshal(resp.Body(), &st); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		fmt.Printf("restore drill job %s started for %s\n", st.Job, st.Key)
		if addonBackupDrillNoWait {
			return nil
		}
		deadline := time.Now().Add(addonBackupDrillTimeout)
		for st.Phase == "Running" {
			if time.Now().After(deadline) {
				return fmt.Errorf("drill job %s still running after %s; check it later with the API or the job log", st.Job, addonBackupDrillTimeout)
			}
			time.Sleep(5 * time.Second)
			resp, err := api.AddonBackupDrillStatus(args[0], args[1], st.Job)
			if err := checkRespErr(resp, err); err != nil {
				return fmt.Errorf("drill status: %w", err)
			}
			key := st.Key
			if err := json.Unmarshal(resp.Body(), &st); err != nil {
				return fmt.Errorf("decode: %w", err)
			}
			if st.Key == "" {
				st.Key = key
			}
		}
		msg, err := drillOutcome(st)
		if err != nil {
			return err
		}
		fmt.Println(msg)
		return nil
	},
}

func init() {
	addonBackupCmd.AddCommand(addonBackupDrillCmd)
	addonBackupDrillCmd.Flags().StringVar(&addonBackupDrillKey, "key", "", "drill this backup (from 'addon-backup list') instead of the newest")
	addonBackupDrillCmd.Flags().BoolVar(&addonBackupDrillAllowUnverified, "allow-unverified", false, "drill a backup that has no checksum manifest")
	addonBackupDrillCmd.Flags().BoolVar(&addonBackupDrillNoWait, "no-wait", false, "start the drill and return without waiting for the result")
	addonBackupDrillCmd.Flags().DurationVar(&addonBackupDrillTimeout, "timeout", 2*time.Hour, "how long to wait for the drill to finish")
}
