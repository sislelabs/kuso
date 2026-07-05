package kusoCli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// backupSettingsFlags holds the settable fields of the S3 backup
// config. Each is applied only when the corresponding flag was
// explicitly changed (cmd.Flags().Changed), so `set` is a
// read-modify-write that leaves untouched fields alone — the server
// wipes any field it receives, so we must resend the current values.
var (
	backupSetBucket    string
	backupSetEndpoint  string
	backupSetRegion    string
	backupSetAccessKey string
	backupSetSecretKey string
)

// `kuso backup` — pull a gzipped pg_dump of the live kuso server's
// metadata DB to a local file. Server runs pg_dump in-process,
// streams gzip down. Admin-only.
//
// `kuso restore` — POST a backup file to /api/admin/restore. The
// server stashes the dump in a kube Secret, spawns a one-shot Job
// that pipes it through psql, and (on success) auto-rolls
// kuso-server so every replica drops its stale connection state.
// Pre-v0.9.38 the user had to do that rollout manually with kubectl;
// the server does it now.

var (
	backupOutput  string
	restoreNoWait bool
	restoreWait   time.Duration
	restoreYes    bool
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Download a gzipped pg_dump of the kuso server DB",
	Long: `Streams /api/admin/backup as gzipped pg_dump SQL and writes it to
--output (default: kuso-backup-<timestamp>.sql.gz in the current dir).

Admin role required. The dump contains user creds, JWT secrets, and
audit logs — treat the output like a credential. To restore:

    kuso restore <file>`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		out := backupOutput
		if out == "" {
			out = fmt.Sprintf("kuso-backup-%s.sql.gz", time.Now().UTC().Format("20060102T150405Z"))
		}
		resp, err := api.RawGet("/api/admin/backup")
		if err != nil {
			return fmt.Errorf("download: %w", err)
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("server returned 404 — backup endpoint disabled")
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		if err := os.WriteFile(out, resp.Body(), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
		fmt.Printf("wrote %d bytes to %s\n", len(resp.Body()), out)
		return nil
	},
}

var restoreCmd = &cobra.Command{
	Use:   "restore <file>",
	Short: "Upload a pg_dump backup to the kuso server (auto-rolls kuso-server)",
	Long: `Uploads <file> via POST /api/admin/restore. Server spawns a Job
that pipes the dump through psql; on success it auto-triggers a
rolling restart of kuso-server so every replica drops stale connection
state. Pre-v0.9.38 the rollout was a manual step; it's automatic now.

By default we poll until the Job completes (or --wait timeout); pass
--no-wait to return immediately with the Job name (useful in CI when
you want to fire-and-forget and check status separately).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(restoreYes,
			fmt.Sprintf("This OVERWRITES the current kuso control-plane database with %s and auto-rolls kuso-server. Continue?", args[0])); err != nil {
			return err
		}
		f, err := os.Open(args[0])
		if err != nil {
			return fmt.Errorf("open %s: %w", args[0], err)
		}
		defer f.Close()
		body, err := io.ReadAll(f)
		if err != nil {
			return fmt.Errorf("read %s: %w", args[0], err)
		}
		// Cheap shape check — gzipped pg_dump starts with the gzip
		// magic bytes 1f 8b. Saves a confusing server-side error for
		// the obvious mistake of pointing this at a plain .sql.
		if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
			return fmt.Errorf("%s does not look like a gzipped pg_dump (use the file produced by `kuso backup`)", args[0])
		}
		resp, err := api.RawPost("/api/admin/restore", body, "application/gzip")
		if err != nil {
			return fmt.Errorf("upload: %w", err)
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("server returned 404 — restore endpoint not available")
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var ack struct {
			JobName    string `json:"jobName"`
			SecretName string `json:"secretName"`
			StatusURL  string `json:"statusUrl"`
			Hint       string `json:"hint"`
		}
		if err := json.Unmarshal(resp.Body(), &ack); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		fmt.Printf("restore submitted: job=%s\n", ack.JobName)
		if restoreNoWait {
			fmt.Printf("  poll: kuso restore status %s\n", ack.JobName)
			return nil
		}
		fmt.Println("  waiting for completion (use --no-wait to skip)…")
		return waitForRestore(ack.StatusURL, ack.JobName, restoreWait)
	},
}

var restoreStatusCmd = &cobra.Command{
	Use:   "status <jobName>",
	Short: "Poll a running restore Job",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		path := "/api/admin/restore/" + args[0]
		resp, err := api.RawGet(path)
		if err != nil {
			return fmt.Errorf("status: %w", err)
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("status %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Println(string(resp.Body()))
		return nil
	},
}

// waitForRestore polls statusURL until phase=Succeeded or Failed
// (or the deadline), printing one line per state change.
func waitForRestore(statusURL, jobName string, deadline time.Duration) error {
	if deadline <= 0 {
		deadline = 10 * time.Minute
	}
	stop := time.Now().Add(deadline)
	lastPhase := ""
	for time.Now().Before(stop) {
		resp, err := api.RawGet(statusURL)
		if err != nil {
			// Transient — keep going.
			time.Sleep(2 * time.Second)
			continue
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("status %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		var st struct {
			Phase            string `json:"phase"`
			RolloutTriggered bool   `json:"rolloutTriggered"`
			RolloutError     string `json:"rolloutError"`
		}
		if err := json.Unmarshal(resp.Body(), &st); err != nil {
			return fmt.Errorf("decode status: %w", err)
		}
		if st.Phase != lastPhase {
			fmt.Printf("  phase=%s\n", st.Phase)
			lastPhase = st.Phase
		}
		switch st.Phase {
		case "Succeeded":
			if st.RolloutError != "" {
				return fmt.Errorf("restore Job succeeded but rollout failed: %s\n  manual step: kubectl -n kuso rollout restart deploy/kuso-server", st.RolloutError)
			}
			fmt.Println("  ✓ restore complete; kuso-server rollout triggered")
			return nil
		case "Failed":
			return fmt.Errorf("restore Job failed — check `kubectl -n kuso logs job/%s`", jobName)
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("timed out waiting for Job %s; poll with `kuso restore status %s`", jobName, jobName)
}

// ---------- backup settings / health / db-stats (admin) ----------

// backupSettingsCmd groups the S3 off-cluster backup config.
var backupSettingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Read/write the off-cluster S3 backup settings (admin)",
}

var backupSettingsGetCmd = &cobra.Command{
	Use:   "get",
	Short: "Show the S3 backup settings (secret access key is never returned)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RawGet("/api/admin/backup-settings")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("get backup settings: %w", err)
		}
		var out map[string]any
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return jsonOut(out)
	},
}

var backupSettingsSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Update S3 backup settings (read-modify-write; only changed flags are overwritten)",
	Long: `Update the off-cluster S3 backup config. Reads the current settings,
overlays only the flags you pass, and PUTs the merged object back — so
unset flags keep their existing values instead of being wiped.

bucket, endpoint and access-key-id are required by the server on first
save; secret-access-key is required the first time and preserved
server-side on subsequent saves if omitted.`,
	Args: cobra.NoArgs,
	Example: `  kuso backup settings set --bucket my-backups --endpoint https://s3.example.com --region auto --access-key-id AKIA... --secret-access-key ...
  kuso backup settings set --region us-east-1`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// Read current settings so PUT (which replaces the whole object)
		// doesn't wipe fields the user didn't touch.
		resp, err := api.RawGet("/api/admin/backup-settings")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("read current settings: %w", err)
		}
		// Decode into a plain map so we round-trip whatever the server
		// sends (drops the read-only hasSecret field on write, which the
		// server ignores anyway). The server never returns
		// secretAccessKey, so it stays absent unless --secret-access-key
		// is passed.
		body := map[string]any{}
		if err := json.Unmarshal(resp.Body(), &body); err != nil {
			return fmt.Errorf("decode current settings: %w", err)
		}
		// hasSecret is a read-only view field; don't send it back.
		delete(body, "hasSecret")
		if cmd.Flags().Changed("bucket") {
			body["bucket"] = backupSetBucket
		}
		if cmd.Flags().Changed("endpoint") {
			body["endpoint"] = backupSetEndpoint
		}
		if cmd.Flags().Changed("region") {
			body["region"] = backupSetRegion
		}
		if cmd.Flags().Changed("access-key-id") {
			body["accessKeyId"] = backupSetAccessKey
		}
		if cmd.Flags().Changed("secret-access-key") {
			body["secretAccessKey"] = backupSetSecretKey
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode settings: %w", err)
		}
		putResp, err := api.RawPut("/api/admin/backup-settings", raw, "application/json")
		if err != nil {
			return fmt.Errorf("put backup settings: %w", err)
		}
		if putResp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", putResp.StatusCode(), string(putResp.Body()))
		}
		fmt.Println("backup settings updated")
		return nil
	},
}

var backupHealthCmd = &cobra.Command{
	Use:   "health",
	Short: "Show whether the control-plane DB is actually being backed up off-cluster (admin)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RawGet("/api/admin/backup-health")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("backup health: %w", err)
		}
		var out map[string]any
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return jsonOut(out)
	},
}

var backupDBStatsCmd = &cobra.Command{
	Use:   "db-stats",
	Short: "Show control-plane Postgres pool + migration stats (admin)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.RawGet("/api/admin/db/stats")
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("db stats: %w", err)
		}
		var out map[string]any
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		return jsonOut(out)
	},
}

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.Flags().StringVarP(&backupOutput, "output", "o", "", "destination file (default: kuso-backup-<timestamp>.sql.gz)")

	// backup settings / health / db-stats
	backupCmd.AddCommand(backupSettingsCmd)
	backupSettingsCmd.AddCommand(backupSettingsGetCmd)
	backupSettingsCmd.AddCommand(backupSettingsSetCmd)
	backupSettingsSetCmd.Flags().StringVar(&backupSetBucket, "bucket", "", "S3 bucket name")
	backupSettingsSetCmd.Flags().StringVar(&backupSetEndpoint, "endpoint", "", "S3 endpoint URL")
	backupSettingsSetCmd.Flags().StringVar(&backupSetRegion, "region", "", "S3 region (default: auto)")
	backupSettingsSetCmd.Flags().StringVar(&backupSetAccessKey, "access-key-id", "", "S3 access key ID")
	backupSettingsSetCmd.Flags().StringVar(&backupSetSecretKey, "secret-access-key", "", "S3 secret access key (preserved server-side if omitted after first save)")
	backupCmd.AddCommand(backupHealthCmd)
	backupCmd.AddCommand(backupDBStatsCmd)
	rootCmd.AddCommand(restoreCmd)
	restoreCmd.Flags().BoolVarP(&restoreYes, "yes", "y", false, "skip the overwrite confirmation prompt")
	restoreCmd.Flags().BoolVar(&restoreNoWait, "no-wait", false, "return immediately after submitting; don't poll the Job")
	restoreCmd.Flags().DurationVar(&restoreWait, "wait", 10*time.Minute, "max time to wait for the Job (used with default polling)")
	restoreCmd.AddCommand(restoreStatusCmd)
}
