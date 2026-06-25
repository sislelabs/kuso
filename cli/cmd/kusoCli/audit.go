package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `kuso audit` — read the audit log. Two modes:
//
//   kuso audit                          instance-wide (admin only)
//   kuso audit --app <project>          project-scoped (Viewer is enough)
//   kuso audit --app <project>/<svc>    one service's history
//
// --limit caps rows (server default 100, max 1000). -o json dumps the
// raw envelope.

var (
	auditApp   string
	auditPhase string
	auditLimit int
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Show the audit log (instance-wide, per-project, or per-service).",
	Long: `Read the kuso audit log. With no flags this is the instance-wide view,
which is admin-only. Pass --app <project> for a project-scoped read
(project Viewer is enough), or --app <project>/<service> for one
service's deploy history. Use --phase to narrow the per-service read to
a deploy phase (default "production").`,
	Example: `  kuso audit
  kuso audit --limit 50
  kuso audit --app myproj
  kuso audit --app myproj/web
  kuso audit --app myproj/web --phase production -o json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}

		project, service := "", ""
		if auditApp != "" {
			if i := strings.Index(auditApp, "/"); i >= 0 {
				project, service = auditApp[:i], auditApp[i+1:]
			} else {
				project = auditApp
			}
		}

		var (
			r   *resty.Response
			err error
		)
		if service != "" {
			phase := auditPhase
			if phase == "" {
				phase = "production"
			}
			r, err = api.ListAuditForApp(project, phase, service, auditLimit)
		} else {
			r, err = api.ListAudit(project, auditLimit)
		}
		if err != nil {
			return err
		}
		if r.StatusCode() >= 300 {
			return fmt.Errorf("server returned %d: %s", r.StatusCode(), string(r.Body()))
		}
		if outputFormat == "json" {
			fmt.Println(string(r.Body()))
			return nil
		}
		var out kusoApi.AuditResponse
		if err := json.Unmarshal(r.Body(), &out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if len(out.Audit) == 0 {
			fmt.Println("No audit entries.")
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"When", "User", "Sev", "Action", "Project", "Service", "Message"})
		for _, e := range out.Audit {
			tw.Append([]string{
				e.Timestamp,
				dashIfEmpty(e.User),
				dashIfEmpty(e.Severity),
				dashIfEmpty(e.Action),
				dashIfEmpty(e.Pipeline),
				dashIfEmpty(e.App),
				e.Message,
			})
		}
		tw.Render()
		fmt.Printf("\n%d of %d entries (limit %d).\n", len(out.Audit), out.Count, out.Limit)
		return nil
	},
}

func init() {
	auditCmd.Flags().StringVar(&auditApp, "app", "", "scope to a project or project/service (empty = instance-wide, admin only)")
	auditCmd.Flags().StringVar(&auditPhase, "phase", "", "deploy phase for a per-service read (default production)")
	auditCmd.Flags().IntVar(&auditLimit, "limit", 0, "max rows (default 100 server-side, max 1000)")
	auditCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	rootCmd.AddCommand(auditCmd)
}
