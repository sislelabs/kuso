package kusoCli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
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

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.Flags().StringVarP(&backupOutput, "output", "o", "", "destination file (default: kuso-backup-<timestamp>.sql.gz)")
	rootCmd.AddCommand(restoreCmd)
	restoreCmd.Flags().BoolVar(&restoreNoWait, "no-wait", false, "return immediately after submitting; don't poll the Job")
	restoreCmd.Flags().DurationVar(&restoreWait, "wait", 10*time.Minute, "max time to wait for the Job (used with default polling)")
	restoreCmd.AddCommand(restoreStatusCmd)
}
