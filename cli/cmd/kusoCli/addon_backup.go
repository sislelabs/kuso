package kusoCli

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-resty/resty/v2"
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
		if err := checkRespErr(resp, err); err != nil {
			return err
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
var addonBackupRestoreConfirm string

// resolveRestoreConfirm computes the `confirm` value to send and
// rejects an unconfirmed restore. The dumps are --clean, so the
// destination (the addon itself, or the --into sibling) is overwritten
// either way and --confirm must echo the destination's name.
func resolveRestoreConfirm(addon, into, confirmFlag string) (string, error) {
	dest := addon
	if into != "" {
		dest = into
	}
	if confirmFlag != dest {
		return "", fmt.Errorf("restore overwrites %q's live data — re-run with --confirm %s", dest, dest)
	}
	return dest, nil
}

var addonBackupRestoreCmd = &cobra.Command{
	Use:   "restore <project> <addon> <key>",
	Short: "Restore an S3 backup. Default = in-place (DESTRUCTIVE)",
	Long: `Restores the chosen backup into either the source addon (overwriting
existing data) or a sibling addon (--into <name>), whose existing tables
are overwritten while the source stays untouched. --confirm must echo the
destination name. The server creates a one-shot Job that runs gunzip | psql.

The target addon must already exist + be the same engine as the source.`,
	Example: `  kuso addon-backup restore myproj postgres myproj/postgres/20260504T030000Z.sql.gz --confirm postgres
  kuso addon-backup restore myproj postgres myproj/postgres/20260504T030000Z.sql.gz --into postgres-rehearse --confirm postgres-rehearse`,
	Args: cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		// Every restore overwrites its destination; the server demands the
		// destination addon name echoed in `confirm`. Fail early with a
		// clear message rather than letting the server 400.
		confirm, err := resolveRestoreConfirm(args[1], addonBackupRestoreInto, addonBackupRestoreConfirm)
		if err != nil {
			return err
		}
		req := kusoApi.RestoreBackupRequest{Key: args[2], Into: addonBackupRestoreInto, Confirm: confirm}
		resp, err := api.RestoreAddonBackup(args[0], args[1], req)
		if err := checkRespErr(resp, err); err != nil {
			return err
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

var (
	addonBackupDownloadOutput string
	// addonBackupDownloadLegacyOutput backs the retired -o/--output —
	// see rejectRetiredOutputFlag in backup.go for why it errors.
	addonBackupDownloadLegacyOutput string
	addonBackupDownloadForce        bool
)

var addonBackupDownloadCmd = &cobra.Command{
	Use:   "download <project> <addon>",
	Short: "Download an on-demand dump of the addon to a local file",
	Long: `Streams a fresh dump of the addon to disk — no S3 config
required. Works even when scheduled backups aren't set up. The dump is
written to a temp file next to the destination, verified, then moved
into place; it is not subject to the CLI's KUSO_CLI_TIMEOUT.

  postgres → gzipped pg_dump SQL (.sql.gz)
  s3       → gzipped tar of every object in the bucket (.tar.gz)

Other addon kinds (redis, clickhouse, redpanda) aren't supported.

The dump is the addon's entire dataset — treat the output like a
credential. Editor role required.`,
	Example: `  kuso addon-backup download myproj db
  kuso addon-backup download myproj db --file /tmp/db.sql.gz`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Retired-flag check before the login gate — see backup.go.
		if cmd.Flags().Changed("output") {
			return rejectRetiredOutputFlag("kuso addon-backup download", addonBackupDownloadLegacyOutput)
		}
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.DownloadAddonBackup(args[0], args[1])
		if err := downloadRespErr(resp, err); err != nil {
			return fmt.Errorf("download: %w", err)
		}
		defer resp.RawBody().Close()
		// --file is user-chosen and used verbatim (incl. any path they
		// typed). The server-derived Content-Disposition name is NOT
		// trusted: a hostile/spoofing server could send
		// filename="../../.ssh/authorized_keys". Constrain it to a bare
		// basename in the cwd so it can never escape the working dir.
		out := addonBackupDownloadOutput
		if out == "" {
			name := filepath.Base(filenameFromResp(resp.Header().Get("Content-Disposition")))
			if name == "" || name == "." || name == string(filepath.Separator) {
				// Fall back to a sensible default; the server always
				// sends Content-Disposition, so this only trips on an
				// odd proxy or a hostile filename we just rejected.
				name = fmt.Sprintf("%s-%s.gz", args[0], args[1])
			}
			out = name
		}
		n, err := writeVerifiedBackup(out, resp.RawBody(), responseTrailer(resp), addonBackupDownloadForce)
		if err != nil {
			return err
		}
		fmt.Printf("wrote %d bytes to %s\n", n, out)
		return nil
	},
}

// backupStatusTrailer mirrors the server's completion trailer (see
// streamGzipCommand in server-go backup_download.go).
const backupStatusTrailer = "X-Kuso-Backup-Status"

// verifyBackupDownload rejects a download that isn't a complete dump. The
// server can only discover a failed pg_dump after it has sent 200, so the
// status code proves nothing: trust the completion trailer when present
// and always decompress the whole body, since a proxy may strip trailers
// and the server leaves a failed stream's gzip unterminated. An empty
// payload is a failure too — pg_dump never emits zero bytes, and that is
// exactly what pre-fix servers shipped when the dump died.
func verifyBackupDownload(body []byte, trailer http.Header) error {
	return verifyBackupStream(bytes.NewReader(body), trailer)
}

func verifyBackupStream(body io.Reader, trailer http.Header) error {
	if st := trailer.Get(backupStatusTrailer); st != "" && st != "ok" {
		return fmt.Errorf("server reported the dump failed (%s: %s) — check the server log", backupStatusTrailer, st)
	}
	zr, err := gzip.NewReader(body)
	if err != nil {
		return fmt.Errorf("download is not a gzip archive: %w", err)
	}
	n, err := io.Copy(io.Discard, zr)
	if err != nil {
		return fmt.Errorf("download is incomplete (the dump likely failed mid-stream): %w", err)
	}
	if n == 0 {
		return fmt.Errorf("download is empty — the dump failed on the server")
	}
	return nil
}

// downloadRespErr is checkRespErr for a Download response, whose body
// was left unread: on a non-2xx it reads (a bounded slice of) the error
// body itself and closes it.
func downloadRespErr(resp *resty.Response, err error) error {
	if err != nil {
		return err
	}
	if resp == nil || resp.RawResponse == nil {
		return fmt.Errorf("nil response")
	}
	if resp.StatusCode() >= 300 {
		defer resp.RawBody().Close()
		body, _ := io.ReadAll(io.LimitReader(resp.RawBody(), 64<<10))
		return errors.New(apiErrorMessage(resp.StatusCode(), string(body)))
	}
	return nil
}

// responseTrailer returns a getter for the response trailer, which is
// only populated once the body has been read to EOF.
func responseTrailer(resp *resty.Response) func() http.Header {
	return func() http.Header {
		if resp.RawResponse == nil {
			return nil
		}
		return resp.RawResponse.Trailer
	}
}

// writeVerifiedBackup streams body to a temp file next to out, verifies
// it, and only then moves it into place, so a failed dump never leaves a
// success-looking file behind and a large dump is never held in memory.
// Without force, an existing file is never overwritten — a backup dump
// is exactly the kind of thing you don't want to clobber by accident.
// trailer may be nil; it is called after the body has been fully read.
func writeVerifiedBackup(out string, body io.Reader, trailer func() http.Header, force bool) (int64, error) {
	existsErr := fmt.Errorf("%s already exists; re-run with --force to overwrite", out)
	if !force {
		// Checked up front too so a multi-GB download isn't wasted.
		if _, err := os.Lstat(out); err == nil {
			return 0, existsErr
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), ".kuso-download-*")
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", out, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	n, err := io.Copy(tmp, body)
	if err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("download: %w", err)
	}
	var tr http.Header
	if trailer != nil {
		tr = trailer()
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return 0, fmt.Errorf("write %s: %w", out, err)
	}
	if err := verifyBackupStream(tmp, tr); err != nil {
		_ = tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("write %s: %w", out, err)
	}
	if !force {
		// Link fails atomically if out appeared meanwhile; fall back to
		// check-then-rename on filesystems without hard links.
		err := os.Link(tmpName, out)
		if err == nil {
			return n, nil
		}
		if os.IsExist(err) {
			return 0, existsErr
		}
		if _, err := os.Lstat(out); err == nil {
			return 0, existsErr
		}
	}
	if err := os.Rename(tmpName, out); err != nil {
		return 0, fmt.Errorf("write %s: %w", out, err)
	}
	return n, nil
}

// filenameFromResp pulls the attachment filename out of a
// Content-Disposition header, returning "" if absent/unparseable.
func filenameFromResp(cd string) string {
	if cd == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(cd)
	if err != nil {
		return ""
	}
	return params["filename"]
}

// Schedule flags shared between `addon-backup schedule` and the
// no-arg `addon-backup unschedule` (which is just `schedule
// --schedule=""`).
var (
	addonBackupSchedule      string
	addonBackupRetentionDays int
	addonBackupBucket        string
)

var addonBackupScheduleCmd = &cobra.Command{
	Use:   "schedule <project> <addon>",
	Short: "Set a recurring backup schedule for an addon",
	Long: `Configure the per-addon backup CronJob. Schedule is a 5-field
cron expression (UTC). RetentionDays controls how long old objects
stay in S3 — 0 means keep forever (the chart's prune step skips).
Pass --schedule="" to disable scheduled backups (chart drops the
CronJob entirely; existing S3 objects stay). "kuso addon-backup
unschedule" does the same thing.

--bucket overrides the instance-wide backup bucket for this addon
only; pass --bucket="" to clear it. Endpoint, region and credentials
still come from the instance backup settings, so the configured IAM
principal must be able to write to the bucket you name.

This requires admin S3 credentials configured at /settings/backups
(or via PUT /api/admin/backup-settings).`,
	Example: `  kuso addon-backup schedule myproj db --schedule "0 3 * * *" --retention 14
  kuso addon-backup schedule myproj db --schedule ""    # same as: kuso addon-backup unschedule myproj db
  kuso addon-backup schedule myproj db --bucket myproj-backups`,
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
		if cmd.Flags().Changed("bucket") {
			b := addonBackupBucket
			req.Backup.Bucket = &b
		}
		if req.Backup.Schedule == nil && req.Backup.RetentionDays == nil && req.Backup.Bucket == nil {
			return fmt.Errorf("pass --schedule, --retention and/or --bucket")
		}
		resp, err := api.UpdateAddon(args[0], args[1], req)
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		if req.Backup.Schedule != nil && *req.Backup.Schedule == "" {
			fmt.Printf("backup schedule disabled on %s/%s\n", args[0], args[1])
		} else {
			fmt.Printf("backup schedule updated on %s/%s\n", args[0], args[1])
		}
		return nil
	},
}

var addonBackupUnscheduleYes bool

var addonBackupUnscheduleCmd = &cobra.Command{
	Use:     "unschedule <project> <addon>",
	Aliases: []string{"disable"},
	Short:   "Disable scheduled backups for an addon (keeps existing S3 objects)",
	Long: `Disable scheduled backups for an addon. Same as
kuso addon-backup schedule <project> <addon> --schedule "". Existing S3
objects are kept, but no new backups are taken, so it asks for
confirmation; pass --yes to skip it.`,
	Example: "  kuso addon-backup unschedule myproj db",
	Args:    cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if err := confirmDestructive(addonBackupUnscheduleYes, fmt.Sprintf(
			"Stop scheduled backups of %s/%s? No new backups are taken until you re-schedule.", args[0], args[1])); err != nil {
			return err
		}
		empty := ""
		req := kusoApi.UpdateAddonRequest{
			Backup: &kusoApi.UpdateAddonBackup{Schedule: &empty},
		}
		resp, err := api.UpdateAddon(args[0], args[1], req)
		if err := checkRespErr(resp, err); err != nil {
			return err
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
	addonBackupRestoreCmd.Flags().StringVar(&addonBackupRestoreConfirm, "confirm", "", "echo the destination addon name (the addon, or --into) to acknowledge the overwrite")
	addonBackupCmd.AddCommand(addonBackupDownloadCmd)
	// Long-only --file for the destination; -o/--output is retired here
	// (it used to mean the FILE while meaning the FORMAT everywhere
	// else, so `-o json` wrote a file named "json").
	addonBackupDownloadCmd.Flags().StringVar(&addonBackupDownloadOutput, "file", "", "output file (default: name from server, e.g. <project>-<addon>-<ts>.sql.gz)")
	addonBackupDownloadCmd.Flags().StringVarP(&addonBackupDownloadLegacyOutput, "output", "o", "", "retired — use --file (elsewhere -o selects the output format)")
	addonBackupDownloadCmd.Flags().BoolVar(&addonBackupDownloadForce, "force", false, "overwrite the output file if it already exists")
	addonBackupCmd.AddCommand(addonBackupScheduleCmd)
	addonBackupScheduleCmd.Flags().StringVar(&addonBackupSchedule, "schedule", "", "5-field cron expression (UTC); empty disables")
	addonBackupScheduleCmd.Flags().IntVar(&addonBackupRetentionDays, "retention", 14, "delete S3 objects older than N days; 0 = keep forever")
	addonBackupScheduleCmd.Flags().StringVar(&addonBackupBucket, "bucket", "", "override the instance-wide backup bucket for this addon; empty clears")
	addonBackupCmd.AddCommand(addonBackupUnscheduleCmd)
	addonBackupUnscheduleCmd.Flags().BoolVarP(&addonBackupUnscheduleYes, "yes", "y", false, "skip the confirmation prompt")
}
