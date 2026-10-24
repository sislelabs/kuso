package kusoCli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
)

// `kuso health` — platform-trust reconcile.
//
//   kuso health [-o table|json]
//   kuso health fix <resource> [--action <action>] [-y]
//
// `health` runs the read-only reconcile scan and prints the flagged
// issues (drifted/unsafe resources) with a severity rollup footer. When
// an issue carries a one-shot `fix` hint, it's printed indented under the
// row so the operator has a copy-pasteable next step.
//
// `health fix` applies the server's suggested remediation for a single
// issue. It mutates live infra, so it prompts for confirmation unless
// -y/--yes is given (or stdin isn't a TTY, matching the other
// destructive commands).

// healthIssue mirrors one entry in the reconcile scan's `issues` array.
// Severity is one of critical|warning|info. Fix is an optional one-shot
// command hint; RunbookCmd points at deeper docs.
type healthIssue struct {
	Resource   string `json:"resource"`
	Namespace  string `json:"namespace"`
	Project    string `json:"project"`
	Type       string `json:"type"`
	AddonKind  string `json:"addonKind"`
	Kind       string `json:"kind"`
	Severity   string `json:"severity"`
	Summary    string `json:"summary"`
	Detail     string `json:"detail"`
	Action     string `json:"action"`
	Safe       bool   `json:"safe"`
	Fix        string `json:"fix"`
	RunbookCmd string `json:"runbookCmd"`
}

// healthReport is the full reconcile scan response.
type healthReport struct {
	Issues   []healthIssue `json:"issues"`
	Healthy  int           `json:"healthy"`
	Scanned  int           `json:"scanned"`
	Critical int           `json:"critical"`
	Warning  int           `json:"warning"`
	Info     int           `json:"info"`
}

var healthCmd = &cobra.Command{
	Use:   "health",
	Args:  cobra.NoArgs,
	Short: "Run the platform-trust reconcile scan over cluster resources",
	Long: `Scan cluster resources for drift and unsafe configuration and report
any issues, grouped by severity. Read-only — nothing is changed. Use
'kuso health fix <resource>' to apply a flagged issue's remediation.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resp, err := api.ReconcileHealth()
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		if outputFormat == "json" {
			fmt.Println(string(resp.Body()))
			return nil
		}
		var rep healthReport
		if err := json.Unmarshal(resp.Body(), &rep); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		if len(rep.Issues) == 0 {
			fmt.Printf("No issues. %d healthy / %d scanned.\n", rep.Healthy, rep.Scanned)
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"SEVERITY", "RESOURCE", "KIND", "SUMMARY"})
		for _, is := range rep.Issues {
			tw.Append([]string{
				is.Severity,
				is.Resource,
				dashIfEmpty(is.Kind),
				is.Summary,
			})
		}
		tw.Render()
		// Surface one-shot fix hints under the table — the tablewriter
		// frame can't hold a multi-cell continuation, so print them as
		// indented follow-on lines keyed by resource.
		for _, is := range rep.Issues {
			if is.Fix != "" {
				fmt.Printf("  fix %s: %s\n", is.Resource, is.Fix)
			}
		}
		fmt.Printf("\n%d healthy / %d scanned · %d critical %d warning %d info\n",
			rep.Healthy, rep.Scanned, rep.Critical, rep.Warning, rep.Info)
		return nil
	},
}

var healthFixAction string
var healthFixYes bool

var healthFixCmd = &cobra.Command{
	Use:   "fix <resource>",
	Short: "Apply the reconcile remediation for a single flagged resource",
	Long: `Apply the server's suggested remediation for one reconcile issue. This
mutates live infra. Without --action the server re-scans to resolve the
issue's canonical action itself. Prompts for confirmation unless --yes.`,
	Example: `  kuso health fix myproject/postgres-conn
  kuso health fix myproject/postgres-conn --action recreate-secret -y`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		resource := args[0]
		prompt := fmt.Sprintf("Remediate %s", resource)
		if healthFixAction != "" {
			prompt += fmt.Sprintf(" (action=%s)", healthFixAction)
		}
		prompt += "? This mutates live infrastructure."
		if err := confirmDestructive(healthFixYes, prompt); err != nil {
			return err
		}
		resp, err := api.Remediate(resource, healthFixAction)
		if err := checkRespErr(resp, err); err != nil {
			return err
		}
		var out struct {
			Resource string `json:"resource"`
			Action   string `json:"action"`
			Applied  bool   `json:"applied"`
			Message  string `json:"message"`
		}
		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return fmt.Errorf("decode: %w", err)
		}
		msg := out.Message
		if msg == "" {
			if out.Applied {
				msg = "remediation applied"
			} else {
				msg = "no change"
			}
		}
		fmt.Printf("%s: %s\n", out.Resource, msg)
		return nil
	},
}

// orphanKinds are the reconcile-health issue kinds DELETE /api/admin/orphans
// accepts.
var orphanKinds = map[string]bool{"orphan_conn_secret": true, "orphan_addon_pvc": true}

func fetchOrphans() ([]healthIssue, error) {
	resp, err := api.ReconcileHealth()
	if err := checkRespErr(resp, err); err != nil {
		return nil, err
	}
	var rep healthReport
	if err := json.Unmarshal(resp.Body(), &rep); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	out := []healthIssue{}
	for _, is := range rep.Issues {
		if orphanKinds[is.Kind] {
			out = append(out, is)
		}
	}
	return out, nil
}

var healthOrphansOutput string

var healthOrphansCmd = &cobra.Command{
	Use:   "orphans",
	Short: "List addon leftovers (conn Secrets, data volumes) whose addon is gone",
	Long: `List the orphaned addon resources the reconcile scan flags: a conn
Secret or data PVC whose KusoAddon no longer exists. Addon delete keeps
both on purpose, and while they exist the addon name can't be reused.
Delete one with 'kuso health orphans delete'.`,
	Example: `  kuso health orphans
  kuso health orphans -o json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		orphans, err := fetchOrphans()
		if err != nil {
			return err
		}
		switch healthOrphansOutput {
		case "json":
			return jsonOut(orphans)
		case "table", "":
		default:
			return fmt.Errorf("unsupported output format %q", healthOrphansOutput)
		}
		if len(orphans) == 0 {
			fmt.Println("no orphaned addon resources")
			return nil
		}
		tw := tablewriter.NewWriter(os.Stdout)
		tw.SetHeader([]string{"KIND", "NAMESPACE", "NAME", "PROJECT", "SUMMARY"})
		tw.SetAutoWrapText(false)
		for _, is := range orphans {
			tw.Append([]string{is.Kind, is.Namespace, is.Resource, dashIfEmpty(is.Project), is.Summary})
		}
		tw.Render()
		return nil
	},
}

var healthOrphansDelete struct {
	kind, namespace, name string
	yes, dryRun           bool
}

var healthOrphansDeleteCmd = &cobra.Command{
	Use:     "delete",
	Aliases: []string{"rm"},
	Short:   "Delete one orphaned addon conn Secret or data volume",
	Long: `Delete one orphan the reconcile scan flagged. The server re-checks
that the owning addon is really gone and refuses (409) otherwise. Deleting
an orphan_addon_pvc destroys that volume's data and it is not recoverable;
an orphan_conn_secret holds the old addon's password. Back up anything you
need first. Prompts for the name unless --yes. --dry-run only confirms the
target is currently flagged as an orphan.`,
	Example: `  kuso health orphans delete --kind orphan_addon_pvc --namespace kuso-shop --name data-shop-db-0
  kuso health orphans delete --kind orphan_conn_secret --namespace kuso --name shop-cache-conn --yes
  kuso health orphans delete --kind orphan_addon_pvc --namespace kuso-shop --name data-shop-db-0 --dry-run`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if api == nil {
			return fmt.Errorf("not logged in; run 'kuso login' first")
		}
		f := &healthOrphansDelete
		if !orphanKinds[f.kind] {
			return fmt.Errorf("--kind must be orphan_conn_secret or orphan_addon_pvc")
		}
		if f.namespace == "" || f.name == "" {
			return fmt.Errorf("--namespace and --name are required (see 'kuso health orphans')")
		}
		if f.dryRun {
			orphans, err := fetchOrphans()
			if err != nil {
				return err
			}
			for _, is := range orphans {
				if is.Kind == f.kind && is.Namespace == f.namespace && is.Resource == f.name {
					fmt.Printf("dry run: would delete %s %s/%s: %s\n", f.kind, f.namespace, f.name, is.Summary)
					return nil
				}
			}
			return fmt.Errorf("dry run: %s %s/%s is not flagged as an orphan; the delete would be refused", f.kind, f.namespace, f.name)
		}
		if err := confirmTypedName(f.yes, f.name,
			fmt.Sprintf("Delete %s %s/%s? This is not recoverable.", f.kind, f.namespace, f.name)); err != nil {
			return err
		}
		resp, err := api.DeleteOrphan(f.kind, f.namespace, f.name)
		if err := checkRespErr(resp, err); err != nil {
			return fmt.Errorf("delete orphan: %w", err)
		}
		fmt.Printf("deleted %s %s/%s\n", f.kind, f.namespace, f.name)
		return nil
	},
}

func init() {
	healthCmd.AddCommand(healthOrphansCmd)
	healthOrphansCmd.AddCommand(healthOrphansDeleteCmd)
	healthOrphansCmd.Flags().StringVarP(&healthOrphansOutput, "output", "o", "table", "output format [table, json]")
	of := healthOrphansDeleteCmd.Flags()
	of.StringVar(&healthOrphansDelete.kind, "kind", "", "orphan_conn_secret or orphan_addon_pvc")
	of.StringVar(&healthOrphansDelete.namespace, "namespace", "", "namespace of the orphan")
	of.StringVar(&healthOrphansDelete.name, "name", "", "Secret or PVC name")
	of.BoolVarP(&healthOrphansDelete.yes, "yes", "y", false, "skip the confirmation prompt")
	of.BoolVar(&healthOrphansDelete.dryRun, "dry-run", false, "only check the target is currently flagged as an orphan")
}

func init() {
	healthCmd.Flags().StringVarP(&outputFormat, "output", "o", "table", "output format [table, json]")
	healthFixCmd.Flags().StringVar(&healthFixAction, "action", "", "remediation action (default: server resolves it)")
	healthFixCmd.Flags().BoolVarP(&healthFixYes, "yes", "y", false, "skip the confirmation prompt")
	healthCmd.AddCommand(healthFixCmd)
	rootCmd.AddCommand(healthCmd)
}
