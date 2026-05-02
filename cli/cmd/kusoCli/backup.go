package kusoCli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// `kuso backup` — pull a sqlite snapshot of the live kuso server's DB
// to a local file. Requires the server to have KUSO_BACKUP_ENABLED=1
// and the caller to be an admin.
//
// `kuso restore` — POST a sqlite snapshot back. The server swaps the
// file on disk; the operator MUST restart the kuso-server pod for the
// change to take effect (the running process is still pointed at the
// pre-swap file via its open *sql.DB).

var (
	backupOutput string
)

var backupCmd = &cobra.Command{
	Use:   "backup",
	Short: "Download a sqlite snapshot of the kuso server DB",
	Long: `Pulls /api/admin/backup as a binary stream and writes it to
--output (default: kuso-backup-<timestamp>.sqlite in the current dir).

The server must be started with KUSO_BACKUP_ENABLED=1; admin role
required. The DB contains user creds, JWT secrets, and audit logs —
treat the output like a credential.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		out := backupOutput
		if out == "" {
			out = fmt.Sprintf("kuso-backup-%s.sqlite", time.Now().UTC().Format("20060102T150405Z"))
		}
		// Use the underlying resty request; Body() returns []byte we can
		// dump to disk. The endpoint returns octet-stream so resty leaves
		// it as raw bytes.
		resp, err := api.RawGet("/api/admin/backup")
		if err != nil {
			return fmt.Errorf("download: %w", err)
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("server returned 404 — backup endpoint disabled (KUSO_BACKUP_ENABLED=1 required on the server)")
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
	Short: "Upload a sqlite snapshot to the kuso server (REQUIRES POD RESTART)",
	Long: `Uploads <file> via POST /api/admin/restore. The server swaps
the file on disk atomically; the running process is still pointed at the
pre-swap file via its open connection, so a pod restart is REQUIRED for
the change to take effect:

    kubectl -n kuso rollout restart deployment/kuso-server

Refused unless the server has KUSO_BACKUP_ENABLED=1 and the caller is
an admin.`,
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
		resp, err := api.RawPost("/api/admin/restore", body, "application/octet-stream")
		if err != nil {
			return fmt.Errorf("upload: %w", err)
		}
		if resp.StatusCode() == 404 {
			return fmt.Errorf("server returned 404 — restore endpoint disabled (KUSO_BACKUP_ENABLED=1 required)")
		}
		if resp.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", resp.StatusCode(), string(resp.Body()))
		}
		fmt.Printf("restore accepted: %s\n", string(resp.Body()))
		fmt.Println("Now restart the kuso server pod:")
		fmt.Println("  kubectl -n kuso rollout restart deployment/kuso-server")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(backupCmd)
	backupCmd.Flags().StringVarP(&backupOutput, "output", "o", "", "destination file (default: kuso-backup-<timestamp>.sqlite)")
	rootCmd.AddCommand(restoreCmd)
}
