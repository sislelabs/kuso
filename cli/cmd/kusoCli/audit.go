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
	auditAfter int64
)

var auditCmd = &cobra.Command{
	Use:   "audit",
	Args:  cobra.NoArgs,
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
			r, err = api.ListAudit(project, auditLimit, auditAfter)
		}
		if err := checkRespErr(r, err); err != nil {
			return err
		}
		// Truncation signal (X-Kuso-Truncated header): tell the user the
		// page was cut and how to continue. Stderr so `-o json` stdout
		// stays machine-parseable.
		if r.Header().Get("X-Kuso-Truncated") == "true" {
			if next := r.Header().Get("X-Kuso-Next-After"); next != "" {
				fmt.Fprintf(os.Stderr, "note: result truncated — older entries exist; continue with --after %s\n", next)
			}
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

var (
	auditVerifyChain string
	auditVerifySeq   int64
	auditVerifyHash  string
)

var auditVerifyCmd = &cobra.Command{
	Use:   "verify",
	Args:  cobra.NoArgs,
	Short: "Check the audit log's hash chains for edited or deleted rows (admin only).",
	Long: `Walk both audit hash chains (security and routine) from the oldest row
retention kept to the current head and report the first row that was
edited, deleted, or inserted out of order.

A chain that verifies on its own only proves the rows are consistent with
each other: someone with database access can rewrite a whole chain. Pass
the values from an audit.checkpoint notification (sent to your unscoped
webhook/Discord channels) to prove the history up to that point is the
one that was published. Exits non-zero when anything fails.`,
	Example: `  kuso audit verify
  kuso audit verify --chain security --seq 1042 --hash 9f2c...e1
  kuso audit verify -o json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		if (auditVerifyChain != "") != (auditVerifyHash != "") || (auditVerifyChain != "" && auditVerifySeq <= 0) {
			return fmt.Errorf("--chain, --seq and --hash go together")
		}
		r, err := api.VerifyAudit(auditVerifyChain, auditVerifySeq, auditVerifyHash)
		if err := checkRespErr(r, err); err != nil {
			return err
		}
		var rep kusoApi.AuditVerifyReport
		if err := json.Unmarshal(r.Body(), &rep); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if outputFormat == "json" {
			fmt.Println(string(r.Body()))
		} else {
			printAuditVerify(rep)
		}
		if !rep.OK {
			return fmt.Errorf("audit log verification failed")
		}
		return nil
	},
}

func printAuditVerify(rep kusoApi.AuditVerifyReport) {
	tw := tablewriter.NewWriter(os.Stdout)
	tw.SetHeader([]string{"Chain", "Status", "Rows", "Oldest kept", "Head seq", "Head hash"})
	for _, c := range rep.Chains {
		status := "ok"
		if !c.OK {
			status = "BROKEN"
		}
		tw.Append([]string{c.Chain, status, fmt.Sprint(c.Rows), fmt.Sprint(c.FloorSeq), fmt.Sprint(c.HeadSeq), c.HeadHash})
	}
	tw.Render()
	for _, c := range rep.Chains {
		if c.Break == nil {
			continue
		}
		fmt.Printf("\n%s chain breaks at seq %d: %s\n", c.Chain, c.Break.Seq, c.Break.Reason)
		if c.Break.Expected != "" {
			fmt.Printf("  expected %s\n  actual   %s\n", c.Break.Expected, dashIfEmpty(c.Break.Actual))
		}
	}
	if cp := rep.Checkpoint; cp != nil {
		if cp.OK {
			fmt.Printf("\ncheckpoint %s seq %d matches.\n", cp.Chain, cp.Seq)
		} else {
			fmt.Printf("\ncheckpoint %s seq %d does NOT match: %s\n", cp.Chain, cp.Seq, cp.Reason)
		}
	}
	if rep.UnchainedSinceStart > 0 {
		fmt.Printf("\nnote: %d audit rows written since the chains started carry no chain position "+
			"(a pre-upgrade replica, a failed chained insert, or rows inserted directly).\n", rep.UnchainedSinceStart)
	}
}

func init() {
	auditVerifyCmd.Flags().StringVar(&auditVerifyChain, "chain", "", "checkpoint chain: security or routine")
	auditVerifyCmd.Flags().Int64Var(&auditVerifySeq, "seq", 0, "checkpoint seq")
	auditVerifyCmd.Flags().StringVar(&auditVerifyHash, "hash", "", "checkpoint hash (64 hex characters)")
	auditVerifyCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	auditCmd.AddCommand(auditVerifyCmd)

	auditCmd.Flags().StringVar(&auditApp, "app", "", "scope to a project or project/service (empty = instance-wide, admin only)")
	auditCmd.Flags().StringVar(&auditPhase, "phase", "", "deploy phase for a per-service read (default production)")
	auditCmd.Flags().IntVar(&auditLimit, "limit", 0, "max rows (default 100 server-side, max 1000)")
	auditCmd.Flags().Int64Var(&auditAfter, "after", 0, "keyset cursor: only rows older than this id (from X-Kuso-Next-After / the last row's id)")
	auditCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	rootCmd.AddCommand(auditCmd)
}
